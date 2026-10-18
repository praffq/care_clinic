package hostcheck

import (
	"context"
	"errors"
	"testing"

	"github.com/ohcnetwork/care_desktop/app/internal/sys/diskspace"
)

func good() Probes {
	return Probes{
		Virtualization:    func() (Virtualization, error) { return Virtualization{Supported: true}, nil },
		Memory:            func() (uint64, error) { return 16 * gib, nil },
		Disk:              func() (diskspace.Usage, error) { return diskspace.Usage{Free: 100 * gib}, nil },
		ServiceInstalled:  func() bool { return true },
		ServicePing:       func(context.Context) error { return nil },
		CanInstallService: true,
	}
}

func level(r Report, id string) Check {
	for _, c := range r.Checks {
		if c.ID == id {
			return c
		}
	}
	return Check{}
}

func TestAllGood(t *testing.T) {
	r := Run(context.Background(), good())
	if !r.Ready || len(r.Checks) != 4 {
		t.Fatalf("%+v", r)
	}
	for _, c := range r.Checks {
		if c.Level != LevelOK {
			t.Errorf("%s: %s", c.ID, c.Level)
		}
	}
}

func TestFailures(t *testing.T) {
	cases := []struct {
		name   string
		mod    func(*Probes)
		id     string
		want   Level
		action string
		ready  bool
	}{
		{"intel", func(p *Probes) {
			p.Virtualization = func() (Virtualization, error) { return Virtualization{Problem: "needs Apple silicon"}, nil }
		}, CheckVirtualization, LevelFail, "", false},
		{"no hv", func(p *Probes) {
			p.Virtualization = func() (Virtualization, error) { return Virtualization{}, nil }
		}, CheckVirtualization, LevelFail, "", false},
		{"whpx off", func(p *Probes) {
			p.Virtualization = func() (Virtualization, error) { return Virtualization{Problem: "turn it on", Fixable: true}, nil }
		}, CheckVirtualization, LevelFail, "install-service", false},
		{"whpx off, can't install", func(p *Probes) {
			p.Virtualization = func() (Virtualization, error) { return Virtualization{Problem: "turn it on", Fixable: true}, nil }
			p.CanInstallService = false
		}, CheckVirtualization, LevelFail, "", false},
		{"low mem", func(p *Probes) { p.Memory = func() (uint64, error) { return 4 * gib, nil } }, CheckMemory, LevelFail, "", false},
		{"mem err", func(p *Probes) { p.Memory = func() (uint64, error) { return 0, errors.New("x") } }, CheckMemory, LevelWarn, "", true},
		{"no disk", func(p *Probes) {
			p.Disk = func() (diskspace.Usage, error) { return diskspace.Usage{Free: 5 * gib}, nil }
		}, CheckDisk, LevelFail, "", false},
		{"low disk", func(p *Probes) {
			p.Disk = func() (diskspace.Usage, error) { return diskspace.Usage{Free: 30 * gib}, nil }
		}, CheckDisk, LevelWarn, "", true},
		{"no service", func(p *Probes) {
			p.ServiceInstalled = func() bool { return false }
			p.ServicePing = func(context.Context) error { return errors.New("no socket") }
		}, CheckService, LevelFail, "install-service", false},
		{"no service unsupported", func(p *Probes) {
			p.ServiceInstalled = func() bool { return false }
			p.ServicePing = func(context.Context) error { return errors.New("no socket") }
			p.CanInstallService = false
		}, CheckService, LevelFail, "", false},
		{"unreachable", func(p *Probes) {
			p.ServicePing = func(context.Context) error { return errors.New("permission denied") }
		}, CheckService, LevelFail, "install-service", false},
		{"nil probes", func(p *Probes) { p.Virtualization, p.Memory = nil, nil }, CheckMemory, LevelWarn, "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := good()
			c.mod(&p)
			r := Run(context.Background(), p)
			got := level(r, c.id)
			if got.Level != c.want || got.Action != c.action || r.Ready != c.ready {
				t.Errorf("got %+v ready=%v", got, r.Ready)
			}
		})
	}
}
