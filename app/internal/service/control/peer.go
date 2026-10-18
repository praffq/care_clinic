package control

import (
	"fmt"
	"os/user"
	"slices"
	"strconv"
)

// Peer is the identity of a process connected to the control socket.
type Peer struct {
	UID  uint32
	GIDs []uint32 // effective and supplementary groups
}

// Policy decides which peers may use the control socket: root, OwnerUID
// (the service's own user) and, when Group is set, members of that group.
type Policy struct {
	OwnerUID uint32
	Group    *uint32
	// UserGroups lists a user's groups from the user database; it is
	// consulted when the peer's credentials lack Group, so membership granted
	// after the peer's login (or beyond the kernel's credential group limit)
	// still counts. Nil means the system user database.
	UserGroups func(uid uint32) []uint32
}

// Allow reports whether p may connect.
func (pol Policy) Allow(p Peer) bool {
	if p.UID == 0 || p.UID == pol.OwnerUID {
		return true
	}
	if pol.Group == nil {
		return false
	}
	if slices.Contains(p.GIDs, *pol.Group) {
		return true
	}
	lookup := pol.UserGroups
	if lookup == nil {
		lookup = userGroups
	}
	return slices.Contains(lookup(p.UID), *pol.Group)
}

// LookupGroup returns the GID of a group name.
func LookupGroup(name string) (uint32, error) {
	g, err := user.LookupGroup(name)
	if err != nil {
		return 0, err
	}
	gid, err := strconv.ParseUint(g.Gid, 10, 32)
	if err != nil {
		return 0, fmt.Errorf("group %s: bad gid %q", name, g.Gid)
	}
	return uint32(gid), nil
}

func userGroups(uid uint32) []uint32 {
	u, err := user.LookupId(strconv.FormatUint(uint64(uid), 10))
	if err != nil {
		return nil
	}
	ids, err := u.GroupIds()
	if err != nil {
		return nil
	}
	var gids []uint32
	for _, s := range ids {
		if g, err := strconv.ParseUint(s, 10, 32); err == nil {
			gids = append(gids, uint32(g))
		}
	}
	return gids
}
