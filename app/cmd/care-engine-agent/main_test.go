package main

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type testCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pem  []byte
}

func newKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func newCA(t *testing.T, isCA bool) testCA {
	t.Helper()
	key := newKey(t)
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "care test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  isCA,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return testCA{cert, key, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}
}

// issue returns a leaf certificate and PKCS#8 key, both PEM.
func (ca testCA) issue(t *testing.T, cn string, usage x509.ExtKeyUsage) (certPEM, keyPEM []byte) {
	t.Helper()
	key := newKey(t)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{usage},
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1)},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	kder, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: kder})
}

func bundle(parts ...[]byte) []byte { return bytes.Join(parts, nil) }

func TestParseTLSBundle(t *testing.T) {
	ca := newCA(t, true)
	cert, key := ca.issue(t, "engine", x509.ExtKeyUsageServerAuth)
	_, otherKey := ca.issue(t, "other", x509.ExtKeyUsageServerAuth)
	notCA := newCA(t, false)

	cfg, err := parseTLSBundle(bundle(ca.pem, cert, key))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ClientAuth != tls.RequireAndVerifyClientCert || cfg.ClientCAs == nil || len(cfg.Certificates) != 1 || cfg.MinVersion != tls.VersionTLS13 {
		t.Fatalf("unexpected config: %+v", cfg)
	}

	for name, tc := range map[string]struct {
		data []byte
		want string
	}{
		"empty":         {nil, "want 3 PEM blocks"},
		"missing key":   {bundle(ca.pem, cert), "want 3 PEM blocks"},
		"extra block":   {bundle(ca.pem, cert, key, ca.pem), "want 3 PEM blocks"},
		"key first":     {bundle(key, ca.pem, cert), "must be certificates"},
		"not a CA":      {bundle(notCA.pem, cert, key), "not a CA"},
		"mismatch key":  {bundle(ca.pem, cert, otherKey), "server key pair"},
		"garbage cert":  {bundle(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("x")}), cert, key), "CA certificate"},
		"swapped order": {bundle(cert, ca.pem, key), "not a CA"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := parseTLSBundle(tc.data)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestTLSListenerAbsentBundle(t *testing.T) {
	l, err := tlsListener(filepath.Join(t.TempDir(), "missing"), "127.0.0.1:0")
	if err != nil || l != nil {
		t.Fatalf("got %v, %v; want no listener and no error", l, err)
	}
}

func TestTLSListenerRequiresClientCert(t *testing.T) {
	ca := newCA(t, true)
	srvCert, srvKey := ca.issue(t, "engine", x509.ExtKeyUsageServerAuth)
	path := filepath.Join(t.TempDir(), "raw")
	if err := os.WriteFile(path, bundle(ca.pem, srvCert, srvKey), 0o600); err != nil {
		t.Fatal(err)
	}
	l, err := tlsListener(path, "127.0.0.1:0")
	if err != nil || l == nil {
		t.Fatalf("tlsListener: %v, %v", l, err)
	}
	srv := &http.Server{
		Handler:           http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok") }),
		ReadHeaderTimeout: 5 * time.Second,
		ErrorLog:          log.New(io.Discard, "", 0),
	}
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() { _ = srv.Close() })
	url := "https://" + l.Addr().String() + "/"

	roots := x509.NewCertPool()
	roots.AddCert(ca.cert)
	get := func(certs ...tls.Certificate) error {
		c := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{
			RootCAs: roots, Certificates: certs, MinVersion: tls.VersionTLS13,
		}}}
		defer c.CloseIdleConnections()
		resp, err := c.Get(url)
		if err != nil {
			return err
		}
		defer func() { _ = resp.Body.Close() }()
		_, err = io.ReadAll(resp.Body)
		return err
	}
	pair := func(c, k []byte) tls.Certificate {
		p, err := tls.X509KeyPair(c, k)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}

	if err := get(pair(ca.issue(t, "host", x509.ExtKeyUsageClientAuth))); err != nil {
		t.Fatalf("client signed by CA: %v", err)
	}
	if err := get(); err == nil {
		t.Fatal("request without a client certificate succeeded")
	}
	other := newCA(t, true)
	if err := get(pair(other.issue(t, "host", x509.ExtKeyUsageClientAuth))); err == nil {
		t.Fatal("request with a client certificate from another CA succeeded")
	}
}
