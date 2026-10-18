package control

import "log/slog"

// ListenConfig describes the control socket (a named pipe on Windows).
type ListenConfig struct {
	Path string
	// Group, when set, owns the socket and its members may connect. The
	// socket is then 0666 and the peer check is the gate: it also reads
	// membership from the user database, so a user added to the group can
	// connect at once, without logging in again (a 0660 socket would refuse
	// them until their next login). Without it (dev mode) the socket is 0600
	// and only the service's own user (and root) may connect.
	//
	// On Windows the pipe's DACL is the gate instead: SYSTEM,
	// Administrators, the group and, so a user just added by the installer
	// need not log in again, each of the group's members when Listen runs.
	// Without a group only SYSTEM and the service's own user may connect.
	Group string
	Log   *slog.Logger
}
