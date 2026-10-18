package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ohcnetwork/care_desktop/app/internal/engine/protocol"
)

type testEnv struct {
	srv  *httptest.Server
	fake *FakeExecutor
	cfg  Config
}

func newTestEnv(t *testing.T, mod ...func(*Config)) *testEnv {
	t.Helper()
	dir := t.TempDir()
	cfg := Config{
		EngineVersion: "0.1.0-test",
		DataRoot:      dir,
		SchemaFile:    filepath.Join(dir, ".data-schema"),
		FilesRoot:     filepath.Join(dir, "files"),
		BackupsRoot:   filepath.Join(dir, "backups"),
		Log:           NewLogRing(100),
	}
	for _, m := range mod {
		m(&cfg)
	}
	fake := &FakeExecutor{}
	srv := httptest.NewServer(NewServer(cfg, fake))
	t.Cleanup(srv.Close)
	return &testEnv{srv: srv, fake: fake, cfg: cfg}
}

// testResp is a fully read response.
type testResp struct {
	StatusCode int
	Header     http.Header
	Body       *bytes.Reader
}

func (e *testEnv) do(t *testing.T, method, path string, body io.Reader) *testResp {
	t.Helper()
	req, err := http.NewRequest(method, e.srv.URL+path, body)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return &testResp{StatusCode: resp.StatusCode, Header: resp.Header, Body: bytes.NewReader(b)}
}

func readAll(t *testing.T, r io.Reader) string {
	t.Helper()
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func decodeJSON[T any](t *testing.T, r io.Reader) T {
	t.Helper()
	var v T
	if err := json.NewDecoder(r).Decode(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func readFrames(t *testing.T, r io.Reader) []protocol.Frame {
	t.Helper()
	var frames []protocol.Frame
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		var f protocol.Frame
		if err := json.Unmarshal(sc.Bytes(), &f); err != nil {
			t.Fatalf("bad frame %q: %v", sc.Text(), err)
		}
		frames = append(frames, f)
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return frames
}

func expectStatus(t *testing.T, resp *testResp, want int) {
	t.Helper()
	if resp.StatusCode != want {
		t.Fatalf("status = %d, want %d; body: %s", resp.StatusCode, want, readAll(t, resp.Body))
	}
}

func methods(calls []FakeCall) []string {
	var out []string
	for _, c := range calls {
		out = append(out, c.Method)
	}
	return out
}

func TestStatus(t *testing.T) {
	e := newTestEnv(t)
	if err := os.WriteFile(e.cfg.SchemaFile, []byte("3\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	resp := e.do(t, "GET", "/v1/status", nil)
	expectStatus(t, resp, 200)
	st := decodeJSON[protocol.Status](t, resp.Body)
	if st.EngineVersion != "0.1.0-test" || !st.DockerReady || st.DataSchema != 3 {
		t.Fatalf("status = %+v", st)
	}
	// The agent runs in the Linux guest; disk usage is only implemented on unix.
	if runtime.GOOS != "windows" && (st.DiskTotalBytes == 0 || st.DiskFreeBytes > st.DiskTotalBytes) {
		t.Fatalf("disk = %d/%d", st.DiskFreeBytes, st.DiskTotalBytes)
	}
}

func TestStatusDockerDownNoSchema(t *testing.T) {
	e := newTestEnv(t)
	e.fake.PingErr = errors.New("no daemon")
	resp := e.do(t, "GET", "/v1/status", nil)
	expectStatus(t, resp, 200)
	st := decodeJSON[protocol.Status](t, resp.Body)
	if st.DockerReady || st.DataSchema != 0 {
		t.Fatalf("status = %+v", st)
	}
}

func TestOpStreamsFramesInOrder(t *testing.T) {
	e := newTestEnv(t)
	e.fake.Handle = func(_ context.Context, op protocol.Op, em Emitter) (any, error) {
		em.Progress(protocol.Progress{Service: protocol.ServiceDB, State: "starting"})
		em.Log(protocol.LogLine{Service: protocol.ServiceDB, Stream: "stdout", Line: "POSTGRES_PASSWORD=hunter2 ready"})
		em.Progress(protocol.Progress{Service: protocol.ServiceDB, State: "running", Message: "url postgres://care:s3cret@db/care"})
		return nil, nil
	}
	resp := e.do(t, "POST", "/v1/ops/stack.up", strings.NewReader(`{"services":["db"],"wait":true}`))
	expectStatus(t, resp, 200)
	if ct := resp.Header.Get("Content-Type"); ct != "application/x-ndjson" {
		t.Fatalf("content-type = %q", ct)
	}
	frames := readFrames(t, resp.Body)
	if len(frames) != 4 {
		t.Fatalf("got %d frames: %+v", len(frames), frames)
	}
	if frames[0].Progress == nil || frames[0].Progress.State != "starting" {
		t.Fatalf("frame 0 = %+v", frames[0])
	}
	if frames[1].Log == nil || frames[1].Log.Line != "POSTGRES_PASSWORD=*** ready" {
		t.Fatalf("frame 1 = %+v", frames[1].Log)
	}
	if frames[2].Progress == nil || strings.Contains(frames[2].Progress.Message, "s3cret") {
		t.Fatalf("frame 2 = %+v", frames[2].Progress)
	}
	if !frames[3].Terminal() || string(frames[3].Result) != "{}" {
		t.Fatalf("frame 3 = %+v", frames[3])
	}
	calls := e.fake.Calls()
	if len(calls) != 1 || calls[0].Method != "StackUp" {
		t.Fatalf("calls = %v", methods(calls))
	}
	if up := calls[0].Op.(*protocol.StackUp); !up.Wait || !slices.Equal(up.Services, []protocol.Service{protocol.ServiceDB}) {
		t.Fatalf("op = %+v", up)
	}
}

func TestOpResultPayload(t *testing.T) {
	e := newTestEnv(t)
	e.fake.Handle = func(context.Context, protocol.Op, Emitter) (any, error) {
		return protocol.StackStatusResult{Services: []protocol.ServiceState{{Name: protocol.ServiceDB, State: "running", Health: "healthy"}}}, nil
	}
	resp := e.do(t, "POST", "/v1/ops/stack.status", nil)
	expectStatus(t, resp, 200)
	frames := readFrames(t, resp.Body)
	if len(frames) != 1 || frames[0].Result == nil {
		t.Fatalf("frames = %+v", frames)
	}
	var res protocol.StackStatusResult
	if err := json.Unmarshal(frames[0].Result, &res); err != nil {
		t.Fatal(err)
	}
	if len(res.Services) != 1 || res.Services[0].Health != "healthy" {
		t.Fatalf("result = %+v", res)
	}
}

func TestBackupRunResult(t *testing.T) {
	e := newTestEnv(t)
	e.fake.Handle = func(context.Context, protocol.Op, Emitter) (any, error) {
		return protocol.BackupRunResult{BackupID: "manual-20261018T050403Z"}, nil
	}
	resp := e.do(t, "POST", "/v1/ops/backup.run", nil)
	expectStatus(t, resp, 200)
	frames := readFrames(t, resp.Body)
	var res protocol.BackupRunResult
	if len(frames) != 1 || json.Unmarshal(frames[0].Result, &res) != nil || res.BackupID != "manual-20261018T050403Z" {
		t.Fatalf("frames = %+v", frames)
	}
}

func TestOpErrorFrames(t *testing.T) {
	for _, tc := range []struct {
		name     string
		err      error
		wantCode string
		wantMsg  string
	}{
		{"plain", errors.New("boom token=abc123"), protocol.CodeOpFailed, "boom token=***"},
		{"typed", &protocol.OpError{Code: protocol.CodeCommandFailed, Message: "x: Authorization: Bearer abc.def"}, protocol.CodeCommandFailed, "x: Authorization: Bearer ***"},
		{"not implemented", ErrNotImplemented, protocol.CodeNotImplemented, ErrNotImplemented.Message},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newTestEnv(t)
			e.fake.Handle = func(_ context.Context, _ protocol.Op, em Emitter) (any, error) {
				em.Log(protocol.LogLine{Line: "working"})
				return nil, tc.err
			}
			resp := e.do(t, "POST", "/v1/ops/backup.run", nil)
			expectStatus(t, resp, 200)
			frames := readFrames(t, resp.Body)
			if len(frames) != 2 || frames[0].Log == nil || frames[1].Error == nil {
				t.Fatalf("frames = %+v", frames)
			}
			if got := frames[1].Error; got.Code != tc.wantCode || got.Message != tc.wantMsg {
				t.Fatalf("error = %+v", got)
			}
		})
	}
}

func TestOpBadRequests(t *testing.T) {
	for _, tc := range []struct {
		name, path, body string
		status           int
		errKind          string
	}{
		{"invalid params", "/v1/ops/stack.logs", `{"service":"nope","tail_lines":0}`, 400, protocol.CodeInvalidParams},
		{"missing params", "/v1/ops/stack.logs", ``, 400, protocol.CodeInvalidParams},
		{"malformed", "/v1/ops/stack.up", `{"services":`, 400, protocol.CodeBadRequest},
		{"unknown field", "/v1/ops/stack.up", `{"cmd":"rm -rf /"}`, 400, protocol.CodeBadRequest},
		{"not an object", "/v1/ops/stack.up", `["db"]`, 400, protocol.CodeBadRequest},
		{"purge without confirmation", "/v1/ops/stack.down", `{"remove_volumes":true}`, 400, protocol.CodeInvalidParams},
		{"params query on body op", "/v1/ops/stack.up?params=%7B%7D", `{}`, 400, protocol.CodeBadRequest},
		{"image.load bad params", "/v1/ops/image.load?params=" + url.QueryEscape(`{"role":"backend","digest":"sha256:xyz"}`), "tar", 400, protocol.CodeInvalidParams},
		{"image.load no params", "/v1/ops/image.load", "tar", 400, protocol.CodeInvalidParams},
		{"unknown op", "/v1/ops/shell.exec", `{}`, 404, protocol.CodeUnknownOp},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newTestEnv(t)
			resp := e.do(t, "POST", tc.path, strings.NewReader(tc.body))
			expectStatus(t, resp, tc.status)
			body := decodeJSON[map[string]any](t, resp.Body)
			if body["error"] != tc.errKind {
				t.Fatalf("body = %v", body)
			}
			if calls := e.fake.Calls(); len(calls) != 0 {
				t.Fatalf("executor called: %v", methods(calls))
			}
		})
	}
}

func TestBusy(t *testing.T) {
	e := newTestEnv(t)
	started, release := make(chan struct{}), make(chan struct{})
	e.fake.Handle = func(ctx context.Context, op protocol.Op, em Emitter) (any, error) {
		if op.OpName() == protocol.OpStackUp {
			close(started)
			<-release
		}
		return nil, nil
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		resp, err := http.Post(e.srv.URL+"/v1/ops/stack.up", "application/json", nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer func() { _ = resp.Body.Close() }()
		frames := readFrames(t, resp.Body)
		if len(frames) != 1 || frames[0].Result == nil {
			t.Errorf("first op frames = %+v", frames)
		}
	}()
	<-started

	for _, path := range []string{"/v1/ops/stack.stop", "/v1/ops/migrations.run", "/v1/shutdown"} {
		resp := e.do(t, "POST", path, nil)
		expectStatus(t, resp, 409)
		body := decodeJSON[map[string]string](t, resp.Body)
		if body["error"] != "busy" || body["op"] != protocol.OpStackUp {
			t.Fatalf("%s: body = %v", path, body)
		}
	}
	// Read-only ops bypass the lock.
	resp := e.do(t, "POST", "/v1/ops/stack.status", nil)
	expectStatus(t, resp, 200)
	readFrames(t, resp.Body)
	resp = e.do(t, "POST", "/v1/ops/stack.logs", strings.NewReader(`{"service":"backend","tail_lines":10}`))
	expectStatus(t, resp, 200)
	readFrames(t, resp.Body)

	close(release)
	wg.Wait()
	resp = e.do(t, "POST", "/v1/ops/stack.stop", nil)
	expectStatus(t, resp, 200)
	readFrames(t, resp.Body)
}

func TestImageLoadStreamsBody(t *testing.T) {
	e := newTestEnv(t)
	digest := "sha256:" + strings.Repeat("a", 64)
	params := url.QueryEscape(`{"role":"backend","digest":"` + digest + `"}`)
	resp := e.do(t, "POST", "/v1/ops/image.load?params="+params, strings.NewReader("TARBALL-BYTES"))
	expectStatus(t, resp, 200)
	frames := readFrames(t, resp.Body)
	if len(frames) != 1 || frames[0].Result == nil {
		t.Fatalf("frames = %+v", frames)
	}
	calls := e.fake.Calls()
	if len(calls) != 1 || string(calls[0].Body) != "TARBALL-BYTES" {
		t.Fatalf("calls = %+v", calls)
	}
	if op := calls[0].Op.(*protocol.ImageLoad); op.Role != protocol.ImageRoleBackend || string(op.Digest) != digest {
		t.Fatalf("op = %+v", op)
	}
}

func TestManageReadsPasswordFile(t *testing.T) {
	e := newTestEnv(t)
	body := `{"command":"createsuperuser","username":"admin","password_file":"admin-pw"}`

	resp := e.do(t, "POST", "/v1/ops/manage", strings.NewReader(body))
	expectStatus(t, resp, 400)
	if got := decodeJSON[map[string]any](t, resp.Body); got["error"] != protocol.CodeInvalidParams {
		t.Fatalf("body = %v", got)
	}

	resp = e.do(t, "PUT", "/v1/files/config/admin-pw", strings.NewReader("s3cret\n"))
	expectStatus(t, resp, 204)
	resp = e.do(t, "POST", "/v1/ops/manage", strings.NewReader(body))
	expectStatus(t, resp, 200)
	readFrames(t, resp.Body)
	calls := e.fake.Calls()
	if len(calls) != 1 || string(calls[0].Password) != "s3cret\n" {
		t.Fatalf("calls = %+v", calls)
	}
}

func TestFilesRoundTrip(t *testing.T) {
	e := newTestEnv(t)
	resp := e.do(t, "PUT", "/v1/files/build/plugins.json", strings.NewReader(`{"a":1}`))
	expectStatus(t, resp, 204)

	path := filepath.Join(e.cfg.FilesRoot, "build", "plugins.json")
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// Windows has no POSIX permission bits; files inherit the folder ACL.
	if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v", fi.Mode())
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Fatalf("leftover temp files: %v", entries)
	}

	resp = e.do(t, "PUT", "/v1/files/build/plugins.json", strings.NewReader(`{"a":2}`))
	expectStatus(t, resp, 204)
	resp = e.do(t, "GET", "/v1/files/build/plugins.json", nil)
	expectStatus(t, resp, 200)
	if got := readAll(t, resp.Body); got != `{"a":2}` {
		t.Fatalf("body = %q", got)
	}

	resp = e.do(t, "DELETE", "/v1/files/build/plugins.json", nil)
	expectStatus(t, resp, 204)
	resp = e.do(t, "GET", "/v1/files/build/plugins.json", nil)
	expectStatus(t, resp, 404)
	resp = e.do(t, "DELETE", "/v1/files/build/plugins.json", nil)
	expectStatus(t, resp, 404)
}

func TestFilesSizeLimit(t *testing.T) {
	e := newTestEnv(t, func(c *Config) { c.MaxFileBytes = 8; c.MaxRestoreInputBytes = 16 })
	resp := e.do(t, "PUT", "/v1/files/config/big", strings.NewReader("0123456789"))
	expectStatus(t, resp, 413)
	// Chunked body without Content-Length is cut off by the reader limit.
	resp = e.do(t, "PUT", "/v1/files/config/big", io.MultiReader(strings.NewReader("01234"), strings.NewReader("56789")))
	expectStatus(t, resp, 413)
	if entries, _ := os.ReadDir(filepath.Join(e.cfg.FilesRoot, "config")); len(entries) != 0 {
		t.Fatalf("files left behind: %v", entries)
	}
	resp = e.do(t, "PUT", "/v1/files/restore-input/big", strings.NewReader("0123456789"))
	expectStatus(t, resp, 204)
}

func TestFilesRejectTraversal(t *testing.T) {
	e := newTestEnv(t)
	outside := filepath.Join(e.cfg.DataRoot, "outside")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{
		"/v1/files/config/..",
		"/v1/files/config/.hidden",
		"/v1/files/config/..%2Foutside",
		"/v1/files/config/a%2Fb",
		"/v1/files/config/%2E%2E",
		"/v1/files/..%2F..%2Fetc/passwd",
		"/v1/files/logs/x",
		"/v1/files/config/../../outside",
		"/v1/files/config/" + strings.Repeat("a", 65),
	} {
		for _, m := range []string{"GET", "PUT", "DELETE"} {
			resp := e.do(t, m, p, strings.NewReader("x"))
			if resp.StatusCode < 300 {
				t.Errorf("%s %s = %d", m, p, resp.StatusCode)
			}
		}
	}
	if b, err := os.ReadFile(outside); err != nil || string(b) != "secret" {
		t.Fatalf("outside file changed: %q %v", b, err)
	}

	// A symlink planted in an area must not lead outside it.
	area := filepath.Join(e.cfg.FilesRoot, "config")
	if err := os.MkdirAll(area, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(area, "link")); err != nil {
		t.Fatal(err)
	}
	resp := e.do(t, "GET", "/v1/files/config/link", nil)
	if resp.StatusCode == 200 {
		t.Fatalf("symlink escape served: %s", readAll(t, resp.Body))
	}
}

const sha = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func writeSet(t *testing.T, root, id string, files map[string]string, manifest *Manifest) {
	t.Helper()
	dir := filepath.Join(root, id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for n, c := range files {
		if err := os.WriteFile(filepath.Join(dir, n), []byte(c), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if manifest != nil {
		b, _ := json.Marshal(manifest)
		if err := os.WriteFile(filepath.Join(dir, ManifestName), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestBackups(t *testing.T) {
	e := newTestEnv(t)
	root := e.cfg.BackupsRoot
	t1 := time.Date(2026, 10, 1, 2, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 10, 2, 2, 0, 0, 0, time.UTC)
	writeSet(t, root, "nightly-1", map[string]string{"db.dump": "DUMP", "notes.txt": "unlisted"},
		&Manifest{CreatedAt: t1, Files: []protocol.BackupFile{{Name: "db.dump", Size: 4, SHA256: sha}}})
	writeSet(t, root, "nightly-2", map[string]string{"db.dump": "DU"},
		&Manifest{CreatedAt: t2, Files: []protocol.BackupFile{{Name: "db.dump", Size: 4, SHA256: sha}}})
	writeSet(t, root, "partial", map[string]string{"db.dump": "x"}, nil)
	writeSet(t, root, "bad.id", map[string]string{"db.dump": "x"}, nil)
	// Sets being written are hidden until renamed into place.
	writeSet(t, root, ".partial-daily-3", map[string]string{"db.dump": "DUMP"},
		&Manifest{CreatedAt: t2, Files: []protocol.BackupFile{{Name: "db.dump", Size: 4, SHA256: sha}}})
	// A manifest copied from another set does not complete this one.
	writeSet(t, root, "copied", map[string]string{"db.dump": "DUMP"},
		&Manifest{ID: "nightly-1", CreatedAt: t2, Files: []protocol.BackupFile{{Name: "db.dump", Size: 4, SHA256: sha}}})

	resp := e.do(t, "GET", "/v1/backups", nil)
	expectStatus(t, resp, 200)
	sets := decodeJSON[[]protocol.BackupSet](t, resp.Body)
	byID := map[string]protocol.BackupSet{}
	for _, s := range sets {
		byID[s.ID] = s
	}
	if len(sets) != 4 {
		t.Fatalf("sets = %+v", sets)
	}
	if s := byID["copied"]; s.Complete {
		t.Fatalf("copied = %+v", s)
	}
	if s := byID["nightly-1"]; !s.Complete || !s.CreatedAt.Equal(t1) || len(s.Files) != 1 || s.Files[0].SHA256 != sha {
		t.Fatalf("nightly-1 = %+v", s)
	}
	if s := byID["nightly-2"]; s.Complete {
		t.Fatalf("size mismatch reported complete: %+v", s)
	}
	if s := byID["partial"]; s.Complete || len(s.Files) != 1 || s.Files[0].SHA256 != "" {
		t.Fatalf("partial = %+v", s)
	}

	resp = e.do(t, "GET", "/v1/backups/nightly-1/db.dump", nil)
	expectStatus(t, resp, 200)
	if resp.Header.Get(protocol.BackupSHA256Header) != sha || readAll(t, resp.Body) != "DUMP" {
		t.Fatal("export mismatch")
	}
	for _, p := range []string{
		"/v1/backups/nightly-1/notes.txt",
		"/v1/backups/nightly-1/manifest.json",
		"/v1/backups/partial/db.dump",
		"/v1/backups/missing/db.dump",
	} {
		resp := e.do(t, "GET", p, nil)
		expectStatus(t, resp, 404)
	}
	for _, p := range []string{"/v1/backups/%2E%2E/outside", "/v1/backups/nightly-1/..%2F..%2Fx", "/v1/backups/bad.id/db.dump"} {
		resp := e.do(t, "GET", p, nil)
		if resp.StatusCode < 400 {
			t.Errorf("GET %s = %d", p, resp.StatusCode)
		}
	}

	resp = e.do(t, "DELETE", "/v1/backups/nightly-1", nil)
	expectStatus(t, resp, 204)
	if _, err := os.Stat(filepath.Join(root, "nightly-1")); !os.IsNotExist(err) {
		t.Fatalf("set not deleted: %v", err)
	}
	resp = e.do(t, "DELETE", "/v1/backups/nightly-1", nil)
	expectStatus(t, resp, 404)
	resp = e.do(t, "DELETE", "/v1/backups/%2E%2E", nil)
	expectStatus(t, resp, 400)
}

func TestDiagnosticsRedacted(t *testing.T) {
	e := newTestEnv(t)
	e.fake.ContainerList = []string{"care-db-1\tpostgres\trunning\tUp 1 minute", "care-backend-1\tDATABASE_URL=postgres://care:pw@db/care"}
	e.fake.Handle = func(context.Context, protocol.Op, Emitter) (any, error) {
		return nil, errors.New("failed with secret=topsecret")
	}
	resp := e.do(t, "POST", "/v1/ops/stack.up", nil)
	readFrames(t, resp.Body)

	resp = e.do(t, "GET", "/v1/diagnostics", nil)
	expectStatus(t, resp, 200)
	raw := readAll(t, resp.Body)
	if strings.Contains(raw, "topsecret") || strings.Contains(raw, ":pw@") {
		t.Fatalf("diagnostics leaked secrets: %s", raw)
	}
	var rep protocol.DiagnosticsReport
	if err := json.Unmarshal([]byte(raw), &rep); err != nil {
		t.Fatal(err)
	}
	if len(rep.Containers) != 2 || (runtime.GOOS != "windows" && rep.DiskTotalBytes == 0) || rep.EngineVersion != "0.1.0-test" {
		t.Fatalf("report = %+v", rep)
	}
	if !slices.ContainsFunc(rep.AgentLog, func(l string) bool { return strings.Contains(l, "op stack.up: failed") }) {
		t.Fatalf("agent log = %v", rep.AgentLog)
	}
}

func TestTimeSync(t *testing.T) {
	e := newTestEnv(t)
	now := time.Date(2026, 10, 18, 5, 0, 0, 0, time.UTC)
	resp := e.do(t, "POST", "/v1/time", strings.NewReader(`{"now":"2026-10-18T05:00:00Z"}`))
	expectStatus(t, resp, 204)
	calls := e.fake.Calls()
	if len(calls) != 1 || calls[0].Method != "SetTime" || !calls[0].Time.Equal(now) {
		t.Fatalf("calls = %+v", calls)
	}
	for _, body := range []string{`{}`, `{"now":"yesterday"}`, `{"now":"2026-10-18T05:00:00Z","x":1}`} {
		resp := e.do(t, "POST", "/v1/time", strings.NewReader(body))
		expectStatus(t, resp, 400)
	}
	e.fake.SetTimeErr = errors.New("date: permission denied")
	resp = e.do(t, "POST", "/v1/time", strings.NewReader(`{"now":"2026-10-18T05:00:00Z"}`))
	expectStatus(t, resp, 500)
}

func TestShutdown(t *testing.T) {
	e := newTestEnv(t)
	e.fake.PoweredOff = make(chan struct{})
	resp := e.do(t, "POST", "/v1/shutdown", nil)
	expectStatus(t, resp, 202)
	select {
	case <-e.fake.PoweredOff:
	case <-time.After(5 * time.Second):
		t.Fatal("PowerOff not called")
	}
	resp = e.do(t, "POST", "/v1/ops/stack.up", nil)
	expectStatus(t, resp, 409)
	if body := decodeJSON[map[string]string](t, resp.Body); body["op"] != "shutdown" {
		t.Fatalf("body = %v", body)
	}
}

func TestShutdownFailureReleasesLock(t *testing.T) {
	e := newTestEnv(t)
	e.fake.PoweredOff = make(chan struct{})
	e.fake.PowerOffErr = errors.New("poweroff failed")
	resp := e.do(t, "POST", "/v1/shutdown", nil)
	expectStatus(t, resp, 202)
	<-e.fake.PoweredOff
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp := e.do(t, "POST", "/v1/ops/stack.status", nil)
		readFrames(t, resp.Body)
		resp = e.do(t, "POST", "/v1/ops/stack.stop", nil)
		if resp.StatusCode == 200 {
			readFrames(t, resp.Body)
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("lock not released, status %d", resp.StatusCode)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestMethodNotAllowed(t *testing.T) {
	e := newTestEnv(t)
	resp := e.do(t, "GET", "/v1/ops/stack.up", nil)
	expectStatus(t, resp, 405)
}

// startCanceledOp starts path with a client context, waits for the executor to
// start, cancels the client and returns the op's context once it finishes.
func startCanceledOp(t *testing.T, e *testEnv, path string, body func() io.Reader) (opCtxErr chan error, release chan struct{}) {
	t.Helper()
	started := make(chan struct{})
	release = make(chan struct{})
	opCtxErr = make(chan error, 1)
	e.fake.Handle = func(ctx context.Context, op protocol.Op, em Emitter) (any, error) {
		em.Log(protocol.LogLine{Line: "before disconnect"})
		close(started)
		<-release
		// Let the server notice the disconnect before emitting again.
		time.Sleep(50 * time.Millisecond)
		for range 100 {
			em.Log(protocol.LogLine{Line: "after disconnect"})
		}
		opCtxErr <- ctx.Err()
		return nil, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, "POST", e.srv.URL+path, body())
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
	}()
	<-started
	cancel()
	<-done
	return opCtxErr, release
}

func TestMutatingOpSurvivesClientDisconnect(t *testing.T) {
	e := newTestEnv(t)
	opCtxErr, release := startCanceledOp(t, e, "/v1/ops/migrations.run", func() io.Reader { return nil })

	// The op is still running and holds the lock.
	resp := e.do(t, "POST", "/v1/ops/stack.stop", nil)
	expectStatus(t, resp, 409)
	if body := decodeJSON[map[string]string](t, resp.Body); body["op"] != protocol.OpRunMigrations {
		t.Fatalf("body = %v", body)
	}

	close(release)
	select {
	case err := <-opCtxErr:
		if err != nil {
			t.Fatalf("op context was cancelled: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("op did not finish")
	}
	waitForIdle(t, e)
	if !slices.ContainsFunc(e.cfg.Log.Lines(), func(l string) bool { return strings.Contains(l, "op migrations.run: done") }) {
		t.Fatalf("agent log = %v", e.cfg.Log.Lines())
	}
}

func TestImageLoadCancelledOnDisconnect(t *testing.T) {
	e := newTestEnv(t)
	params := url.QueryEscape(`{"role":"backend","digest":"sha256:` + strings.Repeat("a", 64) + `"}`)
	// The fake reads the whole body before Handle runs, so a finite body is fine.
	opCtxErr, release := startCanceledOp(t, e, "/v1/ops/image.load?params="+params, func() io.Reader { return strings.NewReader("TAR") })
	close(release)
	select {
	case err := <-opCtxErr:
		if err == nil {
			t.Fatal("image.load context not cancelled on disconnect")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("op did not finish")
	}
	waitForIdle(t, e)
}

func TestMutatingOpMaxDuration(t *testing.T) {
	e := newTestEnv(t, func(c *Config) { c.MaxOpDuration = 50 * time.Millisecond })
	e.fake.Handle = func(ctx context.Context, _ protocol.Op, _ Emitter) (any, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	resp := e.do(t, "POST", "/v1/ops/stack.up", nil)
	expectStatus(t, resp, 200)
	frames := readFrames(t, resp.Body)
	if len(frames) != 1 || frames[0].Error == nil || frames[0].Error.Code != protocol.CodeCanceled {
		t.Fatalf("frames = %+v", frames)
	}
}

// waitForIdle polls until a mutating op is accepted again.
func waitForIdle(t *testing.T, e *testEnv) {
	t.Helper()
	e.fake.Handle = nil
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp := e.do(t, "POST", "/v1/ops/stack.stop", nil)
		if resp.StatusCode == 200 {
			if frames := readFrames(t, resp.Body); len(frames) != 1 || frames[0].Result == nil {
				t.Fatalf("frames = %+v", frames)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("lock not released: %d", resp.StatusCode)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
