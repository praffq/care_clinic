package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/ohcnetwork/care_desktop/app/internal/feed"
)

// Key files hold a standard-base64 Ed25519 seed (32 bytes) and a newline;
// public key files hold a standard-base64 public key, the format of
// app/internal/feed/devkeys/dev.pub and feed.ProductionKey*.

func encodeKey(b []byte) []byte {
	return []byte(base64.StdEncoding.EncodeToString(b) + "\n")
}

func loadPrivateKey(path string) (ed25519.PrivateKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(data)))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	switch len(b) {
	case ed25519.SeedSize:
		return ed25519.NewKeyFromSeed(b), nil
	case ed25519.PrivateKeySize:
		return ed25519.PrivateKey(b), nil
	}
	return nil, fmt.Errorf("%s: want a %d-byte Ed25519 seed, got %d bytes", path, ed25519.SeedSize, len(b))
}

func loadPublicKey(path string) (ed25519.PublicKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	pub, err := feed.ParsePublicKey(string(data))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return pub, nil
}

// writeNew writes data to path, refusing to replace an existing file.
func writeNew(path string, data []byte, perm os.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

func cmdKeygen(args []string, stdout io.Writer) error {
	fs := newFlags("keygen")
	out := fs.String("out", "", "private key file to create (mode 0600)")
	pubOut := fs.String("pub", "", "public key file to create (default: --out with .key replaced by .pub)")
	if err := parse(fs, args, "out"); err != nil {
		return err
	}
	if *pubOut == "" {
		*pubOut = strings.TrimSuffix(*out, filepath.Ext(*out)) + ".pub"
	}
	if *pubOut == *out {
		return errors.New("--pub must differ from --out")
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	if err := writeNew(*out, encodeKey(priv.Seed()), 0o600); err != nil {
		return err
	}
	if err := writeNew(*pubOut, encodeKey(pub), 0o644); err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "private key: %s\npublic key:  %s\nkey id:      %s\npublic key (base64, for feed.ProductionKey* or devkeys/dev.pub):\n%s",
		*out, *pubOut, feed.KeyID(pub), encodeKey(pub))
	return err
}

func cmdSign(args []string, stdout io.Writer) error {
	fs := newFlags("sign")
	keyPath := fs.String("key", "", "private key file")
	in := fs.String("in", "", "updates.json to sign")
	out := fs.String("out", "", "signature file (default: --in + .sig)")
	if err := parse(fs, args, "key", "in"); err != nil {
		return err
	}
	if *out == "" {
		*out = *in + ".sig"
	}
	priv, err := loadPrivateKey(*keyPath)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(*in)
	if err != nil {
		return err
	}
	sig := feed.Sign(data, priv)
	if err := os.WriteFile(*out, sig.Marshal(), 0o644); err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "signed %s with key %s -> %s\n", *in, sig.KeyID, *out)
	return err
}

func cmdVerify(args []string, stdout io.Writer) error {
	fs := newFlags("verify")
	pubPath := fs.String("pub", "", "trusted public key file")
	in := fs.String("in", "", "updates.json to verify")
	sigPath := fs.String("sig", "", "signature file (default: --in + .sig)")
	minRev := fs.Uint64("min-revision", 0, "reject feeds with a lower revision")
	insecure := fs.Bool("allow-insecure", false, "allow http:// URLs (development feeds)")
	if err := parse(fs, args, "pub", "in"); err != nil {
		return err
	}
	if *sigPath == "" {
		*sigPath = *in + ".sig"
	}
	pub, err := loadPublicKey(*pubPath)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(*in)
	if err != nil {
		return err
	}
	sig, err := os.ReadFile(*sigPath)
	if err != nil {
		return err
	}
	m, err := feed.Verify(data, sig, feed.VerifyOptions{
		Keys:              feed.NewTrustedKeys(pub),
		MinRevision:       *minRev,
		AllowInsecureURLs: *insecure,
	})
	if err != nil {
		return err
	}
	return summarize(stdout, m, feed.KeyID(pub))
}

func summarize(w io.Writer, m *feed.Manifest, keyID string) error {
	var b strings.Builder
	fmt.Fprintf(&b, "OK: revision %d, signed by %s\n", m.Revision, keyID)
	fmt.Fprintf(&b, "published %s, expires %s\n", m.PublishedAt.Format("2006-01-02T15:04:05Z07:00"), m.Expires.Format("2006-01-02T15:04:05Z07:00"))
	for _, d := range m.Desktop {
		fmt.Fprintf(&b, "desktop %s requires engine %s\n", d.Version, d.Requires.Engine)
	}
	for _, e := range m.Engine {
		fmt.Fprintf(&b, "engine  %s requires desktop %s (data_schema %d)\n", e.Version, e.Requires.Desktop, e.DataSchema)
	}
	for _, c := range m.Care {
		fmt.Fprintf(&b, "care    %s released %s requires desktop %s, engine %s\n", c.Version, c.ReleasedAt.Format("2006-01-02T15:04:05Z07:00"), c.Requires.Desktop, c.Requires.Engine)
	}
	if len(m.Blocked) > 0 {
		fmt.Fprintf(&b, "blocked %s\n", strings.Join(m.Blocked, ", "))
	}
	_, err := io.WriteString(w, b.String())
	return err
}
