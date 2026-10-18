package clinic

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/ohcnetwork/care_desktop/app/internal/engine/agent"
	"github.com/ohcnetwork/care_desktop/app/internal/engine/protocol"
	"github.com/ohcnetwork/care_desktop/app/internal/progress"
	"github.com/ohcnetwork/care_desktop/app/internal/stack"
	"github.com/ohcnetwork/care_desktop/app/internal/stack/stacktest"
)

func digestOf(name string) protocol.Digest {
	s := sha256.Sum256([]byte(name))
	return protocol.Digest("sha256:" + hex.EncodeToString(s[:]))
}

func feedRelease() *Release {
	rel := &Release{Version: "3.1.0"}
	for _, role := range stack.RequiredRoles {
		rel.Images = append(rel.Images, stack.Image{Role: role, Repo: "ghcr.io/example/" + string(role), Digest: digestOf(string(role))})
	}
	rel.Pull = slices.Clone(rel.Images)
	for _, role := range protocol.BuilderRoles {
		rel.Pull = append(rel.Pull, stack.Image{Role: role, Repo: "ghcr.io/example/" + string(role), Digest: digestOf(string(role))})
	}
	return rel
}

type pullRecorder struct {
	mu     sync.Mutex
	pulled []protocol.Digest
	fail   map[protocol.ImageRole]error
}

func (p *pullRecorder) handle(_ context.Context, op protocol.Op, _ agent.Emitter) (any, error) {
	pull, ok := op.(*protocol.ImagePull)
	if !ok {
		return nil, nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.fail[pull.Role]; err != nil {
		delete(p.fail, pull.Role)
		return nil, err
	}
	p.pulled = append(p.pulled, pull.Digest)
	return nil, nil
}

func (p *pullRecorder) take() []protocol.Digest {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := p.pulled
	p.pulled = nil
	return out
}

func digests(imgs []stack.Image) []protocol.Digest {
	var out []protocol.Digest
	for _, i := range imgs {
		out = append(out, i.Digest)
	}
	return out
}

func TestSetupInstallsTheFeedReleaseAndResumesAfterAFailedDownload(t *testing.T) {
	dir := t.TempDir()
	if err := os.CopyFS(dir, os.DirFS("../../../deployments")); err != nil {
		t.Fatal(err)
	}
	svc := stacktest.New(t)
	rec := &pullRecorder{fail: map[protocol.ImageRole]error{protocol.ImageRoleCaddy: errors.New("dial tcp: lookup ghcr.io: i/o timeout")}}
	svc.Fake.Handle = rec.handle
	rel := feedRelease()
	have := map[protocol.Digest]bool{}
	asked := 0
	newClinic := func() *Clinic {
		return &Clinic{
			InstallDir: dir, MDNSName: "north",
			Stack: &stack.Runtime{Service: svc, StateDir: filepath.Join(dir, "state"), Have: have},
			FromFeed: func(context.Context) (*Release, error) {
				asked++
				return rel, nil
			},
		}
	}

	var details []string
	e := newClinic()
	e.Progress = func(ev progress.Event) {
		if ev.Step == StepImages && ev.Detail != "" && (len(details) == 0 || details[len(details)-1] != ev.Detail) {
			details = append(details, ev.Detail)
		}
	}
	err := e.Setup()
	var ie *stack.ImageError
	if !errors.As(err, &ie) || ie.Image.Role != protocol.ImageRoleCaddy || ie.Missing() {
		t.Fatalf("setup error = %v", err)
	}
	if got := rec.take(); !slices.Equal(got, digests(rel.Pull[:2])) {
		t.Fatalf("pulled before the failure = %v", got)
	}
	if want := []string{"Finding the newest CARE release", "Downloading CARE server (1 of 9)", "Downloading CARE web app (2 of 9)", "Downloading web proxy (3 of 9)"}; !slices.Equal(details, want) {
		t.Fatalf("progress = %q, want %q", details, want)
	}

	if err := newClinic().Setup(); err != nil {
		t.Fatal(err)
	}
	if got := rec.take(); !slices.Equal(got, digests(rel.Pull[2:])) {
		t.Fatalf("retry pulled %v, want only the images not yet present", got)
	}
	if asked != 2 {
		t.Fatalf("feed asked %d times", asked)
	}
	env, err := os.ReadFile(filepath.Join(svc.Files, "config", "stack.env"))
	if err != nil {
		t.Fatal(err)
	}
	for _, img := range rel.Images {
		if !strings.Contains(string(env), img.Ref()) {
			t.Fatalf("stack.env lacks %s:\n%s", img.Ref(), env)
		}
	}
	if strings.Contains(string(env), "builder") {
		t.Fatalf("stack.env pins a builder:\n%s", env)
	}

	before := len(svc.Methods())
	if err := newClinic().Setup(); err != nil {
		t.Fatal(err)
	}
	if asked != 2 || slices.Contains(svc.Methods()[before:], "ImagePull") {
		t.Fatalf("a configured clinic went back to the feed (%d) or pulled again", asked)
	}
}

func TestImageErrorTellsAMissingImageFromANetworkFailure(t *testing.T) {
	svc := stacktest.New(t)
	rec := &pullRecorder{fail: map[protocol.ImageRole]error{
		protocol.ImageRoleBackend: errors.New("image pull: exit status 1: manifest for ghcr.io/example/backend not found: manifest unknown"),
	}}
	svc.Fake.Handle = rec.handle
	r := &stack.Runtime{Service: svc, StateDir: t.TempDir()}
	err := r.Images(context.Background(), feedRelease().Pull)
	var ie *stack.ImageError
	if !errors.As(err, &ie) || !ie.Missing() || ie.Image.Title() != "CARE server" {
		t.Fatalf("error = %v", err)
	}
	if !strings.Contains(err.Error(), "CARE server image ghcr.io/example/backend@sha256:") {
		t.Fatalf("error does not name the image: %v", err)
	}
}
