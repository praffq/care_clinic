package client

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/ohcnetwork/care_desktop/app/internal/engine/agent"
)

type testCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
}

func newCA(t *testing.T) *testCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "care test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return &testCA{cert: cert, key: key}
}

func (ca *testCA) pool() *x509.CertPool {
	p := x509.NewCertPool()
	p.AddCert(ca.cert)
	return p
}

func (ca *testCA) issue(t *testing.T, cn string, usage x509.ExtKeyUsage) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: cn},
		DNSNames:     []string{cn},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{usage},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

func TestTLSDialer(t *testing.T) {
	ca := newCA(t)
	serverCfg := &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{ca.issue(t, "care-engine-agent", x509.ExtKeyUsageServerAuth)},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    ca.pool(),
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{
		Handler:  agent.NewServer(agent.Config{EngineVersion: "tls", DataRoot: t.TempDir(), SchemaFile: "/nonexistent"}, &agent.FakeExecutor{}),
		ErrorLog: log.New(io.Discard, "", 0),
	}
	go func() { _ = srv.Serve(tls.NewListener(l, serverCfg)) }()
	t.Cleanup(func() { _ = srv.Close() })
	addr := l.Addr().String()

	clientCfg := &tls.Config{
		ServerName:   "care-engine-agent",
		RootCAs:      ca.pool(),
		Certificates: []tls.Certificate{ca.issue(t, "care-desktop", x509.ExtKeyUsageClientAuth)},
	}
	c := New(TLSDialer(addr, clientCfg))
	st, err := c.Status(ctxT(t))
	if err != nil || st.EngineVersion != "tls" || !st.DockerReady {
		t.Fatalf("status = %+v, %v", st, err)
	}
	if clientCfg.MinVersion != 0 {
		t.Fatal("TLSDialer mutated the caller's config")
	}

	// No client certificate: the agent rejects the handshake.
	noCert := clientCfg.Clone()
	noCert.Certificates = nil
	if _, err := New(TLSDialer(addr, noCert)).Status(ctxT(t)); err == nil {
		t.Fatal("status without client certificate succeeded")
	}
	// A server certificate from another CA is rejected by the client.
	other := clientCfg.Clone()
	other.RootCAs = newCA(t).pool()
	if _, err := New(TLSDialer(addr, other)).Status(ctxT(t)); err == nil {
		t.Fatal("status with an untrusted server certificate succeeded")
	}
}
