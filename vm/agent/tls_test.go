package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"io"
	"log"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestAgentTLSRequiresPerInstallClientIdentity(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), DNSNames: []string{"care-engine"},
		NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true,
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	private, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certificate := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	bundle := append(append([]byte{}, certificate...), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: private})...)
	cfg, err := serverTLS(bundle)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	server.TLS = cfg
	server.StartTLS()
	defer server.Close()
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(certificate)
	for _, authenticated := range []bool{false, true} {
		clientTLS := &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: pool, ServerName: "care-engine"}
		if authenticated {
			clientTLS.Certificates = cfg.Certificates
		}
		transport := &http.Transport{TLSClientConfig: clientTLS}
		client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
		resp, err := client.Get(server.URL + "/v1/status")
		if resp != nil {
			_ = resp.Body.Close()
		}
		transport.CloseIdleConnections()
		if (err == nil) != authenticated {
			t.Fatalf("authenticated=%v error=%v", authenticated, err)
		}
	}
}
