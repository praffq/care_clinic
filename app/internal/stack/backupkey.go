package stack

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"time"

	"github.com/ohcnetwork/care_desktop/app/internal/engine/protocol"
)

// Backup encryption files. The certificate is uploaded to the engine, where
// every backup set is CMS-encrypted to it; the recovery key (its private key)
// never leaves the host and is needed to restore.
const (
	BackupCert        = "backup-cert.pem"
	BackupRecoveryKey = "backup-recovery-key.pem"
)

// backupKeyBits is a variable so tests can generate smaller keys.
var backupKeyBits = 4096

// GenerateBackupKey returns a self-signed encryption certificate and its
// recovery key (PKCS#1 RSA private key, PEM).
func GenerateBackupKey() (cert, recovery []byte, err error) {
	key, err := rsa.GenerateKey(rand.Reader, backupKeyBits)
	if err != nil {
		return nil, nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial, Subject: pkix.Name{CommonName: "care-backup"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.AddDate(100, 0, 0),
		KeyUsage: x509.KeyUsageKeyEncipherment,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	cert = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	recovery = pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	return cert, recovery, nil
}

// ParseBackupCert checks that data is a single PEM RSA certificate.
func ParseBackupCert(data []byte) (*x509.Certificate, error) {
	block, rest := pem.Decode(data)
	if block == nil || block.Type != "CERTIFICATE" || len(bytes.TrimSpace(rest)) != 0 {
		return nil, errors.New("invalid backup certificate")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("invalid backup certificate: %w", err)
	}
	if _, ok := cert.PublicKey.(*rsa.PublicKey); !ok {
		return nil, errors.New("invalid backup certificate: not an RSA key")
	}
	return cert, nil
}

// ReadRecoveryKey reads a recovery key file and checks it is an RSA key.
func ReadRecoveryKey(path string) ([]byte, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("recovery key: %w", err)
	}
	if !fi.Mode().IsRegular() || fi.Size() == 0 || fi.Size() > 16<<10 {
		return nil, fmt.Errorf("recovery key %s: not a key file", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, rest := pem.Decode(data)
	if block == nil || block.Type != "RSA PRIVATE KEY" || len(bytes.TrimSpace(rest)) != 0 {
		return nil, fmt.Errorf("%s is not a CARE backup recovery key", path)
	}
	if _, err := x509.ParsePKCS1PrivateKey(block.Bytes); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return data, nil
}

// BackupCertificate returns the clinic's backup certificate, creating it
// once. Like Secrets, the host copy is the source of truth and the engine's
// copy is adopted when the host one is missing. A new certificate comes with
// a recovery key written next to it (0600); the admin must keep a copy of it
// somewhere safe, since backups cannot be restored without it.
func (r *Runtime) BackupCertificate(ctx context.Context) ([]byte, error) {
	dir, err := r.stateDir()
	if err != nil {
		return nil, err
	}
	path := filepath.Join(dir, BackupCert)
	if data, err := os.ReadFile(path); err == nil {
		if _, err := ParseBackupCert(data); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		return data, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	rc, err := r.Service.Agent().GetFile(ctx, protocol.FileAreaConfig, BackupCert)
	switch {
	case err == nil:
		data, rerr := io.ReadAll(io.LimitReader(rc, 64<<10))
		_ = rc.Close()
		if rerr != nil {
			return nil, rerr
		}
		if _, err := ParseBackupCert(data); err != nil {
			return nil, fmt.Errorf("engine %s: %w", BackupCert, err)
		}
		r.logf("Adopted the backup certificate from the engine; restoring needs the recovery key saved when it was created")
		return data, writeNew(path, data, 0o644)
	case !isNotFound(err):
		return nil, err
	}
	cert, key, err := GenerateBackupKey()
	if err != nil {
		return nil, err
	}
	keyPath := filepath.Join(dir, BackupRecoveryKey)
	if err := writeNew(keyPath, key, 0o600); err != nil {
		return nil, err
	}
	if err := writeNew(path, cert, 0o644); err != nil {
		return nil, err
	}
	r.logf("Generated the backup recovery key: %s", keyPath)
	r.logf("IMPORTANT: copy it somewhere safe, off this computer. Backups cannot be restored without it.")
	return cert, nil
}

func (r *Runtime) stateDir() (string, error) {
	if r.StateDir != "" {
		return r.StateDir, nil
	}
	return DefaultStateDir()
}

// writeNew writes data to a new file; it never replaces an existing one.
func writeNew(path string, data []byte, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(path)
	}
	return err
}

// PosixTZ is a POSIX TZ for now's UTC offset, e.g. "<+0530>-05:30", so the
// backup container's midnight is the host's. It is a fixed offset: Configure
// runs on every install, which picks up a DST change at the next one.
func PosixTZ(now time.Time) string {
	_, offset := now.Zone()
	name, sign := "+", "-"
	if offset < 0 {
		name, sign, offset = "-", "+", -offset
	}
	h, m := offset/3600, offset%3600/60
	return fmt.Sprintf("<%s%02d%02d>%s%02d:%02d", name, h, m, sign, h, m)
}
