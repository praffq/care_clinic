package stack

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ohcnetwork/care_desktop/app/internal/stack/stacktest"

	"github.com/ohcnetwork/care_desktop/app/internal/engine/agent"
	"github.com/ohcnetwork/care_desktop/app/internal/engine/protocol"
)

var kit = os.DirFS("../../../deployments")

func testDigest(c byte) protocol.Digest {
	return protocol.Digest("sha256:" + strings.Repeat(string(c), 64))
}

func testImages() []Image {
	var out []Image
	for i, role := range RequiredRoles {
		out = append(out, Image{Role: role, Repo: "example/" + string(role), Digest: testDigest("abcdef"[i])})
	}
	return out
}

func envMap(t *testing.T, b []byte) map[string]string {
	t.Helper()
	m := map[string]string{}
	for line := range strings.Lines(string(b)) {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			t.Fatalf("bad env line %q", line)
		}
		m[k] = v
	}
	return m
}

func TestRenderKit(t *testing.T) {
	sec := NewSecrets()
	files, err := Render(kit, Config{Host: "care.local", Origins: []string{"https://192.168.1.5"}, Images: testImages(), Secrets: sec})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"compose.yml", "backend.env", "Caddyfile", "minio-entrypoint.sh", "setup.html", StackEnv, ClinicEnv, SecretsEnv} {
		if len(files[name]) == 0 {
			t.Errorf("%s missing", name)
		}
		if !protocol.ValidFileName(name) {
			t.Errorf("%s is not a valid file area name", name)
		}
	}
	st := envMap(t, files[StackEnv])
	if got, want := st["IMAGE_BACKEND"], "example/backend@"+string(testDigest('a')); got != want {
		t.Errorf("IMAGE_BACKEND = %q, want %q", got, want)
	}
	if st["CARE_HOST"] != "care.local" || len(st["CADDY_CONFIG_SHA"]) != 16 {
		t.Errorf("stack.env = %v", st)
	}
	for _, role := range RequiredRoles {
		v := "IMAGE_" + strings.ToUpper(string(role))
		if !bytes.Contains(files["compose.yml"], []byte("${"+v+":?")) {
			t.Errorf("compose.yml does not use %s", v)
		}
	}
	cl := envMap(t, files[ClinicEnv])
	var csrf, cors []string
	if err := json.Unmarshal([]byte(cl["CSRF_TRUSTED_ORIGINS"]), &csrf); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(cl["CORS_ALLOWED_ORIGINS"]), &cors); err != nil {
		t.Fatal(err)
	}
	if want := []string{"https://care.local", "https://localhost", "https://192.168.1.5"}; strings.Join(cors, " ") != strings.Join(want, " ") {
		t.Errorf("CORS = %v, want %v", cors, want)
	}
	if len(csrf) != len(cors)+1 || csrf[len(csrf)-1] != homeAccessOrigin {
		t.Errorf("CSRF = %v", csrf)
	}
	if cl["BUCKET_EXTERNAL_ENDPOINT"] != "https://care.local" {
		t.Errorf("clinic.env = %v", cl)
	}
	got, err := ParseSecrets(files[SecretsEnv])
	if err != nil || got != sec {
		t.Errorf("secrets.env round trip = %+v, %v", got, err)
	}

	// A different Caddyfile changes the hash so compose recreates caddy.
	files2, err := Render(withFile(t, "Caddyfile", "# changed\n"), Config{Host: "care.local", Images: testImages(), Secrets: sec})
	if err != nil {
		t.Fatal(err)
	}
	if envMap(t, files2[StackEnv])["CADDY_CONFIG_SHA"] == st["CADDY_CONFIG_SHA"] {
		t.Error("CADDY_CONFIG_SHA did not change with the Caddyfile")
	}
}

// withFile copies the kit to a temp dir with name replaced.
func withFile(t *testing.T, name, content string) fs.FS {
	t.Helper()
	dir := t.TempDir()
	if err := os.CopyFS(dir, kit); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return os.DirFS(dir)
}

func TestRenderRejects(t *testing.T) {
	ok := Config{Host: "care.local", Images: testImages(), Secrets: NewSecrets()}
	for name, mut := range map[string]func(*Config){
		"host":          func(c *Config) { c.Host = "care.local\nX=1" },
		"empty host":    func(c *Config) { c.Host = "" },
		"missing image": func(c *Config) { c.Images = c.Images[1:] },
		"bad digest": func(c *Config) {
			c.Images = append(testImages()[1:], Image{Role: protocol.ImageRoleBackend, Repo: "x", Digest: "sha256:zz"})
		},
		"http origin": func(c *Config) { c.Origins = []string{"http://x.local"} },
		"origin path": func(c *Config) { c.Origins = []string{"https://x.local/a"} },
		"origin nl":   func(c *Config) { c.Origins = []string{"https://x.local\nA=b"} },
		"secrets":     func(c *Config) { c.Secrets = Secrets{} },
	} {
		c := ok
		mut(&c)
		if _, err := Render(kit, c); err == nil {
			t.Errorf("%s: Render succeeded", name)
		}
	}
	if _, err := Render(kit, ok); err != nil {
		t.Fatal(err)
	}
}

func TestSecretsFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", SecretsEnv)
	if _, ok, err := LoadSecrets(path); ok || err != nil {
		t.Fatalf("missing file: ok=%v err=%v", ok, err)
	}
	s := NewSecrets()
	if s == NewSecrets() {
		t.Fatal("secrets are not random")
	}
	if err := SaveSecrets(path, s); err != nil {
		t.Fatal(err)
	}
	if err := SaveSecrets(path, NewSecrets()); !errors.Is(err, os.ErrExist) {
		t.Errorf("second save = %v, want ErrExist", err)
	}
	fi, err := os.Stat(path)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, %v", fi.Mode(), err)
	}
	got, ok, err := LoadSecrets(path)
	if !ok || err != nil || got != s {
		t.Errorf("load = %+v %v %v", got, ok, err)
	}
	if err := os.WriteFile(path, []byte("POSTGRES_PASSWORD=x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadSecrets(path); err == nil {
		t.Error("loaded malformed secrets")
	}
}

func writeImageTar(t *testing.T, path, name string) {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	idx := `{"manifests":[{"annotations":{"io.containerd.image.name":"` + name + `"}}]}`
	for _, f := range []struct{ name, body string }{{"oci-layout", "{}"}, {"index.json", idx}} {
		if err := tw.WriteHeader(&tar.Header{Name: f.name, Mode: 0o644, Size: int64(len(f.body))}); err != nil {
			t.Fatal(err)
		}
		_, _ = tw.Write([]byte(f.body))
	}
	_ = tw.Close()
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestDevImages(t *testing.T) {
	dir := t.TempDir()
	names := map[string]string{
		"care-backend-A": "care-clinic-dev/care-backend-a:arm64", "care-backend-B": "care-clinic-dev/care-backend-b:arm64",
		"care-frontend-A": "care-clinic-dev/care-frontend-a:arm64", "care-caddy": "localhost:5000/care-caddy:dev",
		"postgres": "docker.io/library/postgres:16", "redis": "docker.io/library/redis:7", "silo": "minio/minio",
	}
	for file, ref := range names {
		writeImageTar(t, filepath.Join(dir, file+".tar"), ref)
		if err := os.WriteFile(filepath.Join(dir, file+".tar.digest"), []byte(testDigest('a')+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	imgs, err := DevImages(dir, map[protocol.ImageRole]string{protocol.ImageRoleBackend: "care-backend-B"})
	if err != nil {
		t.Fatal(err)
	}
	got := map[protocol.ImageRole]string{}
	for _, img := range imgs {
		got[img.Role] = img.Repo
		if img.Digest != testDigest('a') || !strings.HasPrefix(img.Tarball, dir) {
			t.Errorf("%+v", img)
		}
	}
	want := map[protocol.ImageRole]string{
		protocol.ImageRoleBackend: "care-clinic-dev/care-backend-b", protocol.ImageRoleFrontend: "care-clinic-dev/care-frontend-a",
		protocol.ImageRoleCaddy: "localhost:5000/care-caddy", protocol.ImageRolePostgres: "docker.io/library/postgres",
		protocol.ImageRoleRedis: "docker.io/library/redis", protocol.ImageRoleSilo: "minio/minio",
	}
	for role, repo := range want {
		if got[role] != repo {
			t.Errorf("%s repo = %q, want %q", role, got[role], repo)
		}
	}
	_ = os.Remove(filepath.Join(dir, "redis.tar.digest"))
	if _, err := DevImages(dir, nil); err == nil || !strings.Contains(err.Error(), "dev-images.sh") {
		t.Errorf("missing digest: %v", err)
	}
}

func ctxT(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestInstall(t *testing.T) {
	svc := stacktest.New(t)
	state := t.TempDir()
	r := &Runtime{Service: svc, StateDir: state}
	imgs := testImages()
	imgs[0].Tarball = filepath.Join(t.TempDir(), "backend.tar")
	if err := os.WriteFile(imgs[0].Tarball, []byte("tar"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := r.Install(ctxT(t), kit, "care.local", nil, imgs); err != nil {
		t.Fatal(err)
	}
	want := "ImageLoad ImagePull ImagePull ImagePull ImagePull ImagePull RunMigrations StackUp"
	if got := strings.Join(svc.Methods(), " "); got != want {
		t.Errorf("calls = %s\nwant    %s", got, want)
	}
	if up := svc.Fake.Calls()[len(svc.Fake.Calls())-1].Op.(*protocol.StackUp); !up.Wait {
		t.Errorf("StackUp = %+v, want Wait", up)
	}
	if u := svc.Updates(); len(u) != 1 || u[0].ProxyProtocol == nil || !*u[0].ProxyProtocol {
		t.Errorf("config updates = %+v, want proxy_protocol on", u)
	}
	hostSec, ok, err := LoadSecrets(filepath.Join(state, SecretsEnv))
	if !ok || err != nil {
		t.Fatalf("host secrets: %v %v", ok, err)
	}
	vm, err := os.ReadFile(filepath.Join(svc.Files, "config", SecretsEnv))
	if err != nil || !bytes.Equal(vm, hostSec.Env()) {
		t.Errorf("engine secrets.env differs from the host copy (%v)", err)
	}
	for _, name := range []string{"compose.yml", "Caddyfile", StackEnv, ClinicEnv} {
		if _, err := os.Stat(filepath.Join(svc.Files, "config", name)); err != nil {
			t.Error(err)
		}
	}

	// Reinstalling keeps the secrets.
	if err := r.Install(ctxT(t), kit, "clinic.local", nil, testImages()); err != nil {
		t.Fatal(err)
	}
	if vm2, _ := os.ReadFile(filepath.Join(svc.Files, "config", SecretsEnv)); !bytes.Equal(vm, vm2) {
		t.Error("reinstall changed the secrets")
	}
}

func TestSecretsAdoptedFromEngine(t *testing.T) {
	svc := stacktest.New(t)
	s := NewSecrets()
	if err := os.MkdirAll(filepath.Join(svc.Files, "config"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(svc.Files, "config", SecretsEnv), s.Env(), 0o600); err != nil {
		t.Fatal(err)
	}
	state := t.TempDir()
	r := &Runtime{Service: svc, StateDir: state}
	got, err := r.Secrets(ctxT(t))
	if err != nil || got != s {
		t.Fatalf("Secrets = %+v, %v; want the engine's", got, err)
	}
	if saved, ok, _ := LoadSecrets(filepath.Join(state, SecretsEnv)); !ok || saved != s {
		t.Error("adopted secrets were not saved on the host")
	}
}

func TestCreateSuperuser(t *testing.T) {
	svc := stacktest.New(t)
	r := &Runtime{Service: svc, StateDir: t.TempDir()}
	if err := r.CreateSuperuser(ctxT(t), "admin", "s3cret-pass"); err != nil {
		t.Fatal(err)
	}
	calls := svc.Fake.Calls()
	if len(calls) != 1 || string(calls[0].Password) != "s3cret-pass" {
		t.Fatalf("calls = %+v", calls)
	}
	if op := calls[0].Op.(*protocol.ManageCmd); op.Username != "admin" || op.Command != protocol.ManageCreateSuperuser {
		t.Errorf("op = %+v", op)
	}
	if left, _ := os.ReadDir(filepath.Join(svc.Files, "config")); len(left) != 0 {
		t.Errorf("password file left behind: %v", left)
	}

	svc.Fake.Handle = func(context.Context, protocol.Op, agent.Emitter) (any, error) {
		return nil, errors.New("CommandError: Error: That username is already taken.")
	}
	if err := r.CreateSuperuser(ctxT(t), "admin", "s3cret-pass"); !errors.Is(err, ErrUserExists) {
		t.Errorf("existing user: %v", err)
	}
}
