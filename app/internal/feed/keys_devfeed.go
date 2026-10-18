//go:build devfeed

package feed

import _ "embed"

//go:embed devkeys/dev.pub
var devPublicKey string

// DevBuild reports whether this build trusts the dev key and plain-http URLs.
const DevBuild = true

func extraKeys() []string { return []string{devPublicKey} }
