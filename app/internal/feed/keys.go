package feed

import "crypto/ed25519"

// Production signing keys (standard-base64 Ed25519 public keys). Release
// builds trust only these. Leave a slot empty when unused; during a rotation
// set ProductionKeyNext to the new key, ship a desktop release, then start
// signing with it and move it to ProductionKeyCurrent. The `release keygen`
// command (S7) prints the value to paste here.
//
// Both are empty until the production key is generated, so release builds
// currently reject every feed.
const (
	ProductionKeyCurrent = ""
	ProductionKeyNext    = ""
)

// DefaultKeys returns the keys this build trusts: the production keys, plus
// the committed dev key in builds tagged devfeed.
func DefaultKeys() (TrustedKeys, error) {
	var pubs []ed25519.PublicKey
	for _, s := range append([]string{ProductionKeyCurrent, ProductionKeyNext}, extraKeys()...) {
		if s == "" {
			continue
		}
		p, err := ParsePublicKey(s)
		if err != nil {
			return nil, err
		}
		pubs = append(pubs, p)
	}
	return NewTrustedKeys(pubs...), nil
}
