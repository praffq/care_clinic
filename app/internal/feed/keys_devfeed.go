//go:build devfeed

package feed

import _ "embed"

// devPublicKey's private key is committed at demo/dev.key on purpose so anyone
// can sign demo feeds; it is only ever trusted by devfeed builds.
//
//go:embed devkeys/dev.pub
var devPublicKey string

// DevBuild reports whether this build trusts the dev key and plain-http URLs.
const DevBuild = true

func extraKeys() []string { return []string{devPublicKey} }
