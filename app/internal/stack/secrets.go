package stack

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Secrets are the clinic's generated credentials. They are created once and
// never regenerated: the database password is baked into the postgres volume
// on first start, so a new one would lock CARE out of its own data.
type Secrets struct {
	DjangoKey     string
	DBPassword    string
	StorageKey    string
	StorageSecret string
}

// NewSecrets generates fresh random secrets.
func NewSecrets() Secrets {
	gen := func(n int) string {
		b := make([]byte, n)
		_, _ = rand.Read(b) // never fails (crypto/rand panics instead)
		return hex.EncodeToString(b)
	}
	return Secrets{DjangoKey: gen(32), DBPassword: gen(24), StorageKey: gen(12), StorageSecret: gen(24)}
}

// Env renders secrets.env. Values are hex, so they need no quoting.
func (s Secrets) Env() []byte {
	var b bytes.Buffer
	for _, kv := range [][2]string{
		{"DJANGO_SECRET_KEY", s.DjangoKey},
		{"POSTGRES_PASSWORD", s.DBPassword},
		{"DATABASE_URL", "postgres://postgres:" + s.DBPassword + "@db:5432/care"},
		{"BUCKET_KEY", s.StorageKey},
		{"BUCKET_SECRET", s.StorageSecret},
		{"MINIO_ROOT_USER", s.StorageKey},
		{"MINIO_ROOT_PASSWORD", s.StorageSecret},
	} {
		fmt.Fprintf(&b, "%s=%s\n", kv[0], kv[1])
	}
	return b.Bytes()
}

// ParseSecrets reads a secrets.env written by Env.
func ParseSecrets(data []byte) (Secrets, error) {
	env := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		k, v, ok := strings.Cut(strings.TrimSpace(sc.Text()), "=")
		if ok && !strings.HasPrefix(k, "#") {
			env[k] = v
		}
	}
	s := Secrets{
		DjangoKey:     env["DJANGO_SECRET_KEY"],
		DBPassword:    env["POSTGRES_PASSWORD"],
		StorageKey:    env["BUCKET_KEY"],
		StorageSecret: env["BUCKET_SECRET"],
	}
	return s, s.validate()
}

const secretChars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_"

func (s Secrets) validate() error {
	for name, v := range map[string]string{
		"DJANGO_SECRET_KEY": s.DjangoKey, "POSTGRES_PASSWORD": s.DBPassword,
		"BUCKET_KEY": s.StorageKey, "BUCKET_SECRET": s.StorageSecret,
	} {
		if len(v) < 16 || strings.Trim(v, secretChars) != "" {
			return fmt.Errorf("secrets: %s is missing or malformed", name)
		}
	}
	return nil
}

// LoadSecrets reads path; ok is false when it does not exist.
func LoadSecrets(path string) (s Secrets, ok bool, err error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, false, nil
	}
	if err != nil {
		return s, false, err
	}
	s, err = ParseSecrets(data)
	if err != nil {
		return s, false, fmt.Errorf("%s: %w", path, err)
	}
	return s, true, nil
}

// SaveSecrets writes s to path (0600). It never replaces an existing file.
func SaveSecrets(path string, s Secrets) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(s.Env()); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return err
	}
	return f.Close()
}
