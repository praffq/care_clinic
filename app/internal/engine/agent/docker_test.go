package agent

import (
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ohcnetwork/care_desktop/app/internal/engine/protocol"
)

type fakeRun struct {
	stdout, stderr string
	err            error
}

// fakeRunner records commands and answers them from respond.
type fakeRunner struct {
	mu      sync.Mutex
	cmds    []Command
	stdin   [][]byte
	respond func(c Command) fakeRun
}

func (r *fakeRunner) run(_ context.Context, c Command) error {
	var in []byte
	if c.Stdin != nil {
		in, _ = io.ReadAll(c.Stdin)
	}
	r.mu.Lock()
	r.cmds = append(r.cmds, c)
	r.stdin = append(r.stdin, in)
	r.mu.Unlock()
	var res fakeRun
	if r.respond != nil {
		res = r.respond(c)
	}
	if c.Stdout != nil {
		_, _ = io.WriteString(c.Stdout, res.stdout)
	}
	if c.Stderr != nil {
		_, _ = io.WriteString(c.Stderr, res.stderr)
	}
	return res.err
}

func (r *fakeRunner) argv() [][]string {
	var out [][]string
	for _, c := range r.cmds {
		out = append(out, c.Args)
	}
	return out
}

type recEmitter struct {
	mu       sync.Mutex
	progress []protocol.Progress
	logs     []protocol.LogLine
}

func (e *recEmitter) Progress(p protocol.Progress) {
	e.mu.Lock()
	e.progress = append(e.progress, p)
	e.mu.Unlock()
}
func (e *recEmitter) Log(l protocol.LogLine) { e.mu.Lock(); e.logs = append(e.logs, l); e.mu.Unlock() }

func newDocker(respond func(Command) fakeRun) (*DockerExecutor, *fakeRunner) {
	r := &fakeRunner{respond: respond}
	return &DockerExecutor{Run: r.run, Now: func() time.Time { return time.Date(2026, 10, 18, 5, 4, 3, 0, time.UTC) }}, r
}

var composePrefix = []string{"docker", "compose", "--project-name", "care", "--file", "/var/lib/care/clinic/docker-compose.yml", "--project-directory", "/var/lib/care/clinic"}

func c(args ...string) []string { return append(slices.Clone(composePrefix), args...) }

func expectArgv(t *testing.T, r *fakeRunner, want ...[]string) {
	t.Helper()
	got := r.argv()
	if len(got) != len(want) {
		t.Fatalf("argv =\n%q\nwant\n%q", got, want)
	}
	for i := range want {
		if !slices.Equal(got[i], want[i]) {
			t.Fatalf("argv[%d] =\n%q\nwant\n%q", i, got[i], want[i])
		}
	}
}

func digestOf(ch string) protocol.Digest { return protocol.Digest("sha256:" + strings.Repeat(ch, 64)) }

func TestDockerStackCommands(t *testing.T) {
	ctx := context.Background()
	em := &recEmitter{}
	for _, tc := range []struct {
		name string
		run  func(d *DockerExecutor) error
		want []string
	}{
		{"up all", func(d *DockerExecutor) error { return d.StackUp(ctx, protocol.StackUp{}, em) },
			c("up", "--detach", "--remove-orphans")},
		{"up wait default", func(d *DockerExecutor) error {
			return d.StackUp(ctx, protocol.StackUp{Services: []protocol.Service{"db", "redis"}, Wait: true}, em)
		}, c("up", "--detach", "--remove-orphans", "--wait", "--wait-timeout", "300", "db", "redis")},
		{"up wait timeout", func(d *DockerExecutor) error {
			return d.StackUp(ctx, protocol.StackUp{Wait: true, TimeoutSec: 60}, em)
		}, c("up", "--detach", "--remove-orphans", "--wait", "--wait-timeout", "60")},
		{"stop", func(d *DockerExecutor) error {
			return d.StackStop(ctx, protocol.StackStop{Services: []protocol.Service{"backend"}, TimeoutSec: 30}, em)
		}, c("stop", "--timeout", "30", "backend")},
		{"down", func(d *DockerExecutor) error { return d.StackDown(ctx, protocol.StackDown{}, em) },
			c("down", "--remove-orphans")},
		{"down purge", func(d *DockerExecutor) error {
			return d.StackDown(ctx, protocol.StackDown{RemoveVolumes: true, ConfirmPurge: protocol.PurgeConfirmation}, em)
		}, c("down", "--remove-orphans", "--volumes")},
		{"logs", func(d *DockerExecutor) error {
			since := time.Date(2026, 10, 18, 0, 0, 0, 0, time.FixedZone("IST", 19800))
			return d.StackLogs(ctx, protocol.StackLogs{Service: "backend", TailLines: 200, Since: &since}, em)
		}, c("logs", "--no-color", "--no-log-prefix", "--tail", "200", "--since", "2026-10-17T18:30:00Z", "backend")},
		{"migrations", func(d *DockerExecutor) error { return d.RunMigrations(ctx, em) },
			c("run", "--rm", "-T", "migrate")},
		{"backup", func(d *DockerExecutor) error { return d.BackupRun(ctx, em) },
			c("exec", "-T", "backup", "/bin/sh", "/backup.sh", "once", "manual-20261018T050403Z")},
		{"ping", func(d *DockerExecutor) error { return d.Ping(ctx) },
			[]string{"docker", "version", "--format", "{{.Server.Version}}"}},
		{"set time", func(d *DockerExecutor) error { return d.SetTime(ctx, time.Unix(1792280000, 0)) },
			[]string{"date", "-u", "-s", "@1792280000"}},
		{"poweroff", func(d *DockerExecutor) error { return d.PowerOff(ctx) }, []string{"poweroff"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, r := newDocker(nil)
			if err := tc.run(d); err != nil {
				t.Fatal(err)
			}
			expectArgv(t, r, tc.want)
			if r.cmds[0].Env != nil {
				t.Fatalf("unexpected env %q", r.cmds[0].Env)
			}
		})
	}
}

func TestDockerStreamsOutputAndFailure(t *testing.T) {
	d, _ := newDocker(func(Command) fakeRun {
		return fakeRun{stdout: "line one\nline two", stderr: "warn\nError: DB_PASSWORD=abc failed\n", err: errors.New("exit status 1")}
	})
	em := &recEmitter{}
	err := d.StackLogs(context.Background(), protocol.StackLogs{Service: "db", TailLines: 5}, em)
	var oe *protocol.OpError
	if !errors.As(err, &oe) || oe.Code != CodeCommandFailed || !strings.Contains(oe.Message, "compose logs failed: exit status 1: Error:") {
		t.Fatalf("err = %v", err)
	}
	var lines []string
	for _, l := range em.logs {
		if l.Service != "db" {
			t.Fatalf("log without service: %+v", l)
		}
		lines = append(lines, l.Stream+":"+l.Line)
	}
	slices.Sort(lines)
	want := []string{"stderr:Error: DB_PASSWORD=abc failed", "stderr:warn", "stdout:line one", "stdout:line two"}
	if !slices.Equal(lines, want) {
		t.Fatalf("lines = %q", lines)
	}
	// The server redacts the message; the executor error itself is raw.
	if got := toOpError(context.Background(), err).Message; strings.Contains(got, "abc") {
		t.Fatalf("redacted message = %q", got)
	}
}

func TestDockerStackStatus(t *testing.T) {
	img := "ghcr.io/ohcnetwork/care@" + string(digestOf("b"))
	for name, out := range map[string]string{
		"ndjson": `{"Service":"db","State":"running","Health":"healthy","Image":"postgres:16"}
{"Service":"backend","State":"exited","Health":"","Image":"` + img + `"}
{"Service":"sidecar","State":"running"}
`,
		"array": `[{"Service":"backend","State":"exited","Image":"` + img + `"},{"Service":"db","State":"running","Health":"healthy","Image":"postgres:16"}]`,
	} {
		t.Run(name, func(t *testing.T) {
			d, r := newDocker(func(Command) fakeRun { return fakeRun{stdout: out} })
			res, err := d.StackStatus(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			expectArgv(t, r, c("ps", "--all", "--format", "json"))
			want := []protocol.ServiceState{
				{Name: "backend", State: "exited", Image: digestOf("b")},
				{Name: "db", State: "running", Health: "healthy"},
			}
			if !slices.Equal(res.Services, want) {
				t.Fatalf("services = %+v", res.Services)
			}
		})
	}
	d, _ := newDocker(func(Command) fakeRun { return fakeRun{stdout: ""} })
	if res, err := d.StackStatus(context.Background()); err != nil || len(res.Services) != 0 || res.Services == nil {
		t.Fatalf("empty: %+v %v", res, err)
	}
}

func TestDockerImagePull(t *testing.T) {
	dg := digestOf("c")
	ref := "ghcr.io/ohcnetwork/care@" + string(dg)
	inspect := func(repoDigests string) func(Command) fakeRun {
		return func(c Command) fakeRun {
			if c.Args[1] == "image" {
				return fakeRun{stdout: `{"Id":"sha256:` + strings.Repeat("9", 64) + `","RepoDigests":` + repoDigests + "}\n"}
			}
			return fakeRun{stdout: "Status: Downloaded newer image\n"}
		}
	}
	d, r := newDocker(inspect(`["` + ref + `"]`))
	em := &recEmitter{}
	if err := d.ImagePull(context.Background(), protocol.ImagePull{Role: "backend", Ref: "ghcr.io/ohcnetwork/care", Digest: dg}, em); err != nil {
		t.Fatal(err)
	}
	expectArgv(t, r,
		[]string{"docker", "pull", ref},
		[]string{"docker", "image", "inspect", "--format", "{{json .}}", ref})

	// A ref that already embeds the digest is not doubled.
	d, r = newDocker(inspect(`["` + ref + `"]`))
	if err := d.ImagePull(context.Background(), protocol.ImagePull{Role: "backend", Ref: ref, Digest: dg}, em); err != nil {
		t.Fatal(err)
	}
	if got := r.argv()[0]; !slices.Equal(got, []string{"docker", "pull", ref}) {
		t.Fatalf("argv = %q", got)
	}

	d, _ = newDocker(inspect(`["ghcr.io/ohcnetwork/care@` + string(digestOf("d")) + `"]`))
	err := d.ImagePull(context.Background(), protocol.ImagePull{Role: "backend", Ref: "ghcr.io/ohcnetwork/care", Digest: dg}, em)
	var oe *protocol.OpError
	if !errors.As(err, &oe) || oe.Code != CodeDigestMismatch {
		t.Fatalf("err = %v", err)
	}
}

func TestDockerImageLoad(t *testing.T) {
	dg := digestOf("e")
	respond := func(id string) func(Command) fakeRun {
		return func(c Command) fakeRun {
			switch {
			case slices.Equal(c.Args, []string{"docker", "load"}):
				return fakeRun{stdout: "Loaded image: care/backend:local\n"}
			case c.Args[1] == "image" && c.Args[2] == "inspect":
				return fakeRun{stdout: `{"Id":"` + id + `","RepoDigests":[]}`}
			}
			return fakeRun{}
		}
	}
	d, r := newDocker(respond(string(dg)))
	em := &recEmitter{}
	if err := d.ImageLoad(context.Background(), protocol.ImageLoad{Role: "backend", Digest: dg}, strings.NewReader("TAR"), em); err != nil {
		t.Fatal(err)
	}
	expectArgv(t, r,
		[]string{"docker", "load"},
		[]string{"docker", "image", "inspect", "--format", "{{json .}}", "care/backend:local"})
	if string(r.stdin[0]) != "TAR" {
		t.Fatalf("stdin = %q", r.stdin[0])
	}
	if len(em.progress) == 0 || em.progress[len(em.progress)-1].Bytes != 3 {
		t.Fatalf("progress = %+v", em.progress)
	}

	d, r = newDocker(respond(string(digestOf("f"))))
	err := d.ImageLoad(context.Background(), protocol.ImageLoad{Role: "backend", Digest: dg}, strings.NewReader("TAR"), em)
	var oe *protocol.OpError
	if !errors.As(err, &oe) || oe.Code != CodeDigestMismatch {
		t.Fatalf("err = %v", err)
	}
	if got := r.argv()[2]; !slices.Equal(got, []string{"docker", "image", "rm", "care/backend:local"}) {
		t.Fatalf("mismatch cleanup argv = %q", got)
	}

	d, _ = newDocker(func(Command) fakeRun { return fakeRun{stdout: "nothing useful\n"} })
	if err := d.ImageLoad(context.Background(), protocol.ImageLoad{Role: "backend", Digest: dg}, strings.NewReader("TAR"), em); err == nil {
		t.Fatal("expected error when docker load reports no image")
	}
}

func TestDockerImagePrune(t *testing.T) {
	keepID, keepDigest := string(digestOf("1")), string(digestOf("2"))
	ls := strings.Join([]string{
		`{"ID":"` + keepID + `","Repository":"care/backend","Tag":"local","Digest":"<none>"}`,
		`{"ID":"sha256:` + strings.Repeat("3", 64) + `","Repository":"ghcr.io/x/frontend","Tag":"<none>","Digest":"` + keepDigest + `"}`,
		`{"ID":"sha256:` + strings.Repeat("4", 64) + `","Repository":"old/app","Tag":"v1","Digest":"<none>"}`,
		`{"ID":"sha256:` + strings.Repeat("4", 64) + `","Repository":"old/app","Tag":"v2","Digest":"<none>"}`,
		`{"ID":"sha256:` + strings.Repeat("5", 64) + `","Repository":"<none>","Tag":"<none>","Digest":"<none>"}`,
		`{"ID":"sha256:` + strings.Repeat("6", 64) + `","Repository":"busy/app","Tag":"v1","Digest":"<none>"}`,
	}, "\n")
	d, r := newDocker(func(c Command) fakeRun {
		if c.Args[2] == "ls" {
			return fakeRun{stdout: ls}
		}
		if slices.Contains(c.Args, "busy/app:v1") {
			return fakeRun{stderr: "Error: image is being used by running container\n", err: errors.New("exit status 1")}
		}
		return fakeRun{}
	})
	em := &recEmitter{}
	if err := d.ImagePrune(context.Background(), protocol.ImagePrune{Keep: []protocol.Digest{protocol.Digest(keepID), protocol.Digest(keepDigest)}}, em); err != nil {
		t.Fatal(err)
	}
	expectArgv(t, r,
		[]string{"docker", "image", "ls", "--digests", "--no-trunc", "--format", "{{json .}}"},
		[]string{"docker", "image", "rm", "old/app:v1", "old/app:v2"},
		[]string{"docker", "image", "rm", "sha256:" + strings.Repeat("5", 64)},
		[]string{"docker", "image", "rm", "busy/app:v1"})
	if last := em.progress[len(em.progress)-1]; last.Message != "removed 2 images" {
		t.Fatalf("progress = %+v", last)
	}
}

func TestDockerManageKeepsPasswordOutOfArgv(t *testing.T) {
	const pw = "Sup3r-Secret"
	for _, tc := range []struct {
		op      protocol.ManageCmd
		wantEnv []string
		check   func(t *testing.T, args []string)
	}{
		{
			op:      protocol.ManageCmd{Command: "createsuperuser", Username: "admin", PasswordFile: "pw"},
			wantEnv: []string{"DJANGO_SUPERUSER_PASSWORD=" + pw},
			check: func(t *testing.T, args []string) {
				want := c("exec", "-T", "-e", "DJANGO_SUPERUSER_PASSWORD", "backend", "python", "manage.py",
					"createsuperuser", "--noinput", "--username", "admin", "--email", "admin@care.local")
				if !slices.Equal(args, want) {
					t.Fatalf("argv = %q", args)
				}
			},
		},
		{
			op:      protocol.ManageCmd{Command: "changepassword", Username: "nurse@clinic.org", PasswordFile: "pw"},
			wantEnv: []string{"CARE_MANAGE_USERNAME=nurse@clinic.org", "CARE_MANAGE_PASSWORD=" + pw},
			check: func(t *testing.T, args []string) {
				want := c("exec", "-T", "-e", "CARE_MANAGE_USERNAME", "-e", "CARE_MANAGE_PASSWORD", "backend", "python", "manage.py", "shell", "-c", changePasswordCode)
				if !slices.Equal(args, want) {
					t.Fatalf("argv = %q", args)
				}
			},
		},
	} {
		t.Run(string(tc.op.Command), func(t *testing.T) {
			d, r := newDocker(nil)
			if err := d.Manage(context.Background(), tc.op, []byte(pw+"\n"), &recEmitter{}); err != nil {
				t.Fatal(err)
			}
			cmd := r.cmds[0]
			for _, a := range cmd.Args {
				if strings.Contains(a, pw) {
					t.Fatalf("password in argv: %q", cmd.Args)
				}
			}
			if !slices.Equal(cmd.Env, tc.wantEnv) {
				t.Fatalf("env = %q", cmd.Env)
			}
			tc.check(t, cmd.Args)
		})
	}

	d, r := newDocker(nil)
	if err := d.Manage(context.Background(), protocol.ManageCmd{Command: "sync_valueset"}, nil, &recEmitter{}); err != nil {
		t.Fatal(err)
	}
	expectArgv(t, r, c("exec", "-T", "backend", "python", "manage.py", "sync_valueset"))

	d, r = newDocker(nil)
	if err := d.Manage(context.Background(), protocol.ManageCmd{Command: "createsuperuser", Username: "a", PasswordFile: "pw"}, []byte("\n"), &recEmitter{}); err == nil || len(r.cmds) != 0 {
		t.Fatalf("empty password accepted: %v", err)
	}
}

func TestDockerNotImplemented(t *testing.T) {
	d, r := newDocker(nil)
	ctx, em := context.Background(), &recEmitter{}
	_, e1 := d.ImageBuild(ctx, protocol.ImageBuild{}, em)
	_, e2 := d.DBDump(ctx, protocol.DBDump{}, em)
	e3 := d.DBRestore(ctx, protocol.DBRestore{}, em)
	for _, err := range []error{e1, e2, e3} {
		var oe *protocol.OpError
		if !errors.As(err, &oe) || oe.Code != CodeNotImplemented {
			t.Fatalf("err = %v", err)
		}
	}
	if len(r.cmds) != 0 {
		t.Fatalf("commands ran: %q", r.argv())
	}
}

func TestDockerContainers(t *testing.T) {
	d, r := newDocker(func(Command) fakeRun {
		return fakeRun{stdout: "care-db-1\tpostgres\trunning\tUp\ncare-backend-1\tcare\texited\tExited (1)\n"}
	})
	lines, err := d.Containers(context.Background())
	if err != nil || len(lines) != 2 {
		t.Fatalf("lines = %q, err = %v", lines, err)
	}
	expectArgv(t, r, []string{"docker", "ps", "--all", "--no-trunc", "--format", "{{.Names}}\t{{.Image}}\t{{.State}}\t{{.Status}}"})
}

func TestDockerCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	d, _ := newDocker(func(Command) fakeRun { return fakeRun{err: errors.New("signal: killed")} })
	err := d.StackUp(ctx, protocol.StackUp{}, &recEmitter{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if got := toOpError(ctx, err); got.Code != CodeCanceled {
		t.Fatalf("op error = %+v", got)
	}
}
