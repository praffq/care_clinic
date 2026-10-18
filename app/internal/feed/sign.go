package feed

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// Signature is the decoded contents of updates.json.sig: a detached Ed25519
// signature over the exact bytes of updates.json.
type Signature struct {
	KeyID string `json:"key_id"`
	Sig   string `json:"sig"`
}

// KeyID identifies a public key: hex of the first 8 bytes of sha256(pub).
func KeyID(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return hex.EncodeToString(sum[:8])
}

// Sign produces a detached signature over data.
func Sign(data []byte, priv ed25519.PrivateKey) Signature {
	pub, _ := priv.Public().(ed25519.PublicKey)
	return Signature{
		KeyID: KeyID(pub),
		Sig:   base64.StdEncoding.EncodeToString(ed25519.Sign(priv, data)),
	}
}

// Marshal encodes the signature as written to updates.json.sig.
func (s Signature) Marshal() []byte {
	b, _ := json.Marshal(s)
	return append(b, '\n')
}

// ParseSignature strictly decodes updates.json.sig.
func ParseSignature(data []byte) (Signature, error) {
	var s Signature
	if err := decodeStrict(data, &s); err != nil {
		return s, fmt.Errorf("signature file: %w", err)
	}
	return s, nil
}

// ParsePublicKey decodes a standard-base64 Ed25519 public key, the format of
// devkeys/dev.pub and the production key constants.
func ParsePublicKey(s string) (ed25519.PublicKey, error) {
	b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil {
		return nil, fmt.Errorf("public key: %w", err)
	}
	if len(b) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("public key: want %d bytes, got %d", ed25519.PublicKeySize, len(b))
	}
	return ed25519.PublicKey(b), nil
}

// TrustedKeys is the set of public keys a client accepts, indexed by KeyID.
// It normally holds the current key and, during a rotation, the next one.
type TrustedKeys map[string]ed25519.PublicKey

// NewTrustedKeys builds a key set from public keys.
func NewTrustedKeys(pubs ...ed25519.PublicKey) TrustedKeys {
	k := make(TrustedKeys, len(pubs))
	for _, p := range pubs {
		k[KeyID(p)] = p
	}
	return k
}

// verify checks sig against data using the key named by the signature.
func (k TrustedKeys) verify(data, sigFile []byte) error {
	s, err := ParseSignature(sigFile)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrBadSignature, err)
	}
	pub, ok := k[s.KeyID]
	if !ok {
		return fmt.Errorf("%w: key %q", ErrUnknownKey, s.KeyID)
	}
	raw, err := base64.StdEncoding.DecodeString(s.Sig)
	if err != nil || len(raw) != ed25519.SignatureSize {
		return fmt.Errorf("%w: malformed signature", ErrBadSignature)
	}
	if !ed25519.Verify(pub, data, raw) {
		return ErrBadSignature
	}
	return nil
}

func decodeStrict(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return errors.New("trailing data after JSON value")
	}
	return nil
}
