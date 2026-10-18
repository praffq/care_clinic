// Package hostcheck runs onboarding's computer checks: hardware
// virtualization, memory, free disk and whether care-clinic-service is
// installed and reachable. The probes are injectable so the outcome logic
// is tested without the real machine.
package hostcheck

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ohcnetwork/care_desktop/app/internal/sys/diskspace"
)

const (
	gib = uint64(1) << 30

	// MinMemory leaves the host usable next to the 4 GiB engine VM.
	MinMemory = 8 * gib
	// MinFree and LowFree bound the free space for the engine's disk,
	// images and the clinic's data.
	MinFree = 20 * gib
	LowFree = 40 * gib
)

// Level is a check's outcome; only LevelFail blocks setup.
type Level string

const (
	LevelOK   Level = "ok"
	LevelWarn Level = "warn"
	LevelFail Level = "fail"
)

// Check IDs.
const (
	CheckVirtualization = "virtualization"
	CheckMemory         = "memory"
	CheckDisk           = "disk"
	CheckService        = "service"
)

type Check struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Level  Level  `json:"level"`
	Detail string `json:"detail"`
	// Action names a fix the app can run: "install-service".
	Action string `json:"action,omitempty"`
}

// Report is every check; Ready means none failed.
type Report struct {
	Checks []Check `json:"checks"`
	Ready  bool    `json:"ready"`
}

// Virtualization is what the virtualization probe found.
type Virtualization struct {
	// Supported: the hypervisor is available to this process.
	Supported bool
	// Arm64 is false on a Mac the arm64 engine can't run on.
	Arm64 bool
}

// Probes reach the machine. Nil probes report a warning that the check
// isn't available on this system.
type Probes struct {
	Virtualization func() (Virtualization, error)
	Memory         func() (uint64, error)
	Disk           func() (diskspace.Usage, error)
	// ServiceInstalled reports whether the service is registered with the
	// system (launchd plist present).
	ServiceInstalled func() bool
	// ServicePing reaches the control socket.
	ServicePing func(ctx context.Context) error
	// CanInstallService: the app can install the service itself.
	CanInstallService bool
}

// Run runs every check.
func Run(ctx context.Context, p Probes) Report {
	r := Report{Checks: []Check{
		virtualization(p.Virtualization),
		memory(p.Memory),
		disk(p.Disk),
		service(ctx, p),
	}}
	r.Ready = true
	for _, c := range r.Checks {
		if c.Level == LevelFail {
			r.Ready = false
		}
	}
	return r
}

func unavailable(id, title string) Check {
	return Check{ID: id, Title: title, Level: LevelWarn, Detail: "This check isn't available on this system yet."}
}

func virtualization(probe func() (Virtualization, error)) Check {
	const title = "Virtualization"
	if probe == nil {
		return unavailable(CheckVirtualization, title)
	}
	v, err := probe()
	switch {
	case err != nil:
		return Check{ID: CheckVirtualization, Title: title, Level: LevelWarn, Detail: "Couldn't check for virtualization support: " + err.Error()}
	case !v.Arm64:
		return Check{ID: CheckVirtualization, Title: title, Level: LevelFail, Detail: "CARE Clinic needs a Mac with Apple silicon (M1 or later)."}
	case !v.Supported:
		return Check{ID: CheckVirtualization, Title: title, Level: LevelFail, Detail: "This computer doesn't offer hardware virtualization, which CARE Clinic needs to run the clinic."}
	}
	return Check{ID: CheckVirtualization, Title: title, Level: LevelOK, Detail: "Hardware virtualization is available."}
}

func memory(probe func() (uint64, error)) Check {
	const title = "Memory"
	if probe == nil {
		return unavailable(CheckMemory, title)
	}
	total, err := probe()
	switch {
	case err != nil:
		return Check{ID: CheckMemory, Title: title, Level: LevelWarn, Detail: "Couldn't read this computer's memory: " + err.Error()}
	case total < MinMemory:
		return Check{ID: CheckMemory, Title: title, Level: LevelFail,
			Detail: fmt.Sprintf("%s of memory; CARE Clinic needs at least %s.", human(total), human(MinMemory))}
	}
	return Check{ID: CheckMemory, Title: title, Level: LevelOK, Detail: human(total) + " of memory."}
}

func disk(probe func() (diskspace.Usage, error)) Check {
	const title = "Disk space"
	if probe == nil {
		return unavailable(CheckDisk, title)
	}
	u, err := probe()
	switch {
	case err != nil:
		return Check{ID: CheckDisk, Title: title, Level: LevelWarn, Detail: "Couldn't read the free disk space: " + err.Error()}
	case u.Free < MinFree:
		return Check{ID: CheckDisk, Title: title, Level: LevelFail,
			Detail: fmt.Sprintf("%s free; CARE Clinic needs at least %s. Free up space and check again.", human(u.Free), human(MinFree))}
	case u.Free < LowFree:
		return Check{ID: CheckDisk, Title: title, Level: LevelWarn,
			Detail: fmt.Sprintf("%s free. That is enough to start, but the clinic's records will need more over time.", human(u.Free))}
	}
	return Check{ID: CheckDisk, Title: title, Level: LevelOK, Detail: human(u.Free) + " free."}
}

func service(ctx context.Context, p Probes) Check {
	const title = "CARE Clinic service"
	c := Check{ID: CheckService, Title: title}
	installed := p.ServiceInstalled != nil && p.ServiceInstalled()
	pingErr := errors.New("not checked")
	if p.ServicePing != nil {
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		pingErr = p.ServicePing(ctx)
		cancel()
	}
	switch {
	case pingErr == nil:
		c.Level, c.Detail = LevelOK, "Installed and running. It runs the clinic in the background, even before anyone logs in."
	case !installed && p.CanInstallService:
		c.Level, c.Detail, c.Action = LevelFail, "Not installed yet. CARE Clinic installs it with one administrator prompt.", "install-service"
	case !installed:
		c.Level, c.Detail = LevelFail, "Not installed. Installing it from the app isn't supported on this system yet."
	default:
		c.Level, c.Detail = LevelFail, "Installed, but CARE Clinic can't reach it: "+pingErr.Error()
		if p.CanInstallService {
			c.Detail += ". Installing it again usually fixes this."
			c.Action = "install-service"
		}
	}
	return c
}

func human(b uint64) string {
	if b >= 10*gib {
		return fmt.Sprintf("%d GB", b/gib)
	}
	return fmt.Sprintf("%.1f GB", float64(b)/float64(gib))
}
