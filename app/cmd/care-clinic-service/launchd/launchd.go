// Package launchd holds the service's LaunchDaemon plist, so the desktop
// app installs the same file scripts/service-macos.sh does.
package launchd

import _ "embed"

// Plist is org.ohc.care-clinic.plist.
//
//go:embed org.ohc.care-clinic.plist
var Plist []byte
