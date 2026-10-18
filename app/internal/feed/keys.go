package feed

import "crypto/ed25519"

// Production signing keys (standard-base64 Ed25519 public keys). Release
// builds trust only these. Leave a slot empty when unused; during a rotation
// set ProductionKeyNext to the new key, ship a desktop release, then start
// signing with it and move it to ProductionKeyCurrent. `release keygen`
// (app/cmd/release) prints the value to paste here.
//
// While both are empty (as on a fork without FEED_SIGNING_KEY), builds accept
// an unsigned feed with a warning; see UnsignedFeedAllowed. Once either is
// set, a missing or bad signature is rejected.
const (
	ProductionKeyCurrent = ""
	ProductionKeyNext    = ""
)

// UnsignedFeedAllowed reports whether this build accepts an unsigned feed
// (VerifyOptions.AllowUnsigned): only when no production key is compiled in.
func UnsignedFeedAllowed() bool {
	return ProductionKeyCurrent == "" && ProductionKeyNext == ""
}

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
