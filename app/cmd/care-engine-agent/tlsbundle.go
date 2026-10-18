package main

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
)

// DefaultTLSBundle is where QEMU exposes "-fw_cfg name=opt/care/tls,file=…"
// inside the guest (qemu_fw_cfg sysfs). Virtualization.framework has no
// fw_cfg, so the TCP listener never starts there.
const DefaultTLSBundle = "/sys/firmware/qemu_fw_cfg/by_name/opt/care/tls/raw"

// parseTLSBundle parses a PEM bundle holding, in order, the CA certificate,
// the server certificate and the server private key, and returns a server
// config that requires TLS 1.3 and a client certificate issued by that CA.
func parseTLSBundle(data []byte) (*tls.Config, error) {
	var blocks []*pem.Block
	for {
		var b *pem.Block
		b, data = pem.Decode(data)
		if b == nil {
			break
		}
		blocks = append(blocks, b)
	}
	if len(blocks) != 3 {
		return nil, fmt.Errorf("tls bundle: want 3 PEM blocks (CA cert, server cert, server key), got %d", len(blocks))
	}
	if blocks[0].Type != "CERTIFICATE" || blocks[1].Type != "CERTIFICATE" {
		return nil, errors.New("tls bundle: the first two PEM blocks must be certificates")
	}
	ca, err := x509.ParseCertificate(blocks[0].Bytes)
	if err != nil {
		return nil, fmt.Errorf("tls bundle: CA certificate: %w", err)
	}
	if !ca.IsCA || !ca.BasicConstraintsValid {
		return nil, errors.New("tls bundle: the first certificate is not a CA")
	}
	cert, err := tls.X509KeyPair(pem.EncodeToMemory(blocks[1]), pem.EncodeToMemory(blocks[2]))
	if err != nil {
		return nil, fmt.Errorf("tls bundle: server key pair: %w", err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{cert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    pool,
	}, nil
}
