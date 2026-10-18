//go:build !devfeed

package feed

// DevBuild reports whether this build trusts the dev key and plain-http URLs.
const DevBuild = false

func extraKeys() []string { return nil }
