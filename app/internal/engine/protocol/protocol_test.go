package protocol

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

var (
	dgA = "sha256:" + strings.Repeat("a", 64)
	dgB = "sha256:" + strings.Repeat("b", 64)
)

func TestDecodeOp(t *testing.T) {
	tests := []struct {
		name    string
		op      string
		body    string
		wantErr string // "" ok, else "invalid_params:<field>", "bad_request", "unknown_op"
	}{
		// stack.up
		{"up empty body", OpStackUp, ``, ""},
		{"up full", OpStackUp, `{"services":["db","backend"],"wait":true,"timeout_sec":120}`, ""},
		{"up bad service", OpStackUp, `{"services":["postgres"]}`, "invalid_params:services[0]"},
		{"up dup service", OpStackUp, `{"services":["db","db"]}`, "invalid_params:services[1]"},
		{"up timeout low", OpStackUp, `{"timeout_sec":5}`, "invalid_params:timeout_sec"},
		{"up timeout high", OpStackUp, `{"timeout_sec":901}`, "invalid_params:timeout_sec"},
		{"up service shell", OpStackUp, `{"services":["db; rm -rf /"]}`, "invalid_params:services[0]"},
		// stack.stop
		{"stop ok", OpStackStop, `{"services":["caddy"],"timeout_sec":30}`, ""},
		{"stop bad", OpStackStop, `{"services":[""]}`, "invalid_params:services[0]"},
		// stack.down
		{"down plain", OpStackDown, `{}`, ""},
		{"down purge ok", OpStackDown, `{"remove_volumes":true,"confirm_purge":"PURGE-ALL-CLINIC-DATA"}`, ""},
		{"down purge no token", OpStackDown, `{"remove_volumes":true}`, "invalid_params:confirm_purge"},
		{"down purge wrong token", OpStackDown, `{"remove_volumes":true,"confirm_purge":"yes"}`, "invalid_params:confirm_purge"},
		{"down stray token", OpStackDown, `{"confirm_purge":"PURGE-ALL-CLINIC-DATA"}`, "invalid_params:confirm_purge"},
		// stack.status / migrations.run / backup.run
		{"status", OpStackStatus, `{}`, ""},
		{"status unknown field", OpStackStatus, `{"x":1}`, "bad_request"},
		{"migrations", OpRunMigrations, ` `, ""},
		{"backup", OpBackupRun, `{}`, ""},
		// stack.logs
		{"logs ok", OpStackLogs, `{"service":"backend","tail_lines":100,"since":"2026-01-01T00:00:00Z"}`, ""},
		{"logs tail 0", OpStackLogs, `{"service":"backend"}`, "invalid_params:tail_lines"},
		{"logs tail high", OpStackLogs, `{"service":"backend","tail_lines":5001}`, "invalid_params:tail_lines"},
		{"logs no service", OpStackLogs, `{"tail_lines":10}`, "invalid_params:service"},
		{"logs bad since", OpStackLogs, `{"service":"db","tail_lines":10,"since":"yesterday"}`, "bad_request"},
		// image.pull
		{"pull ok", OpImagePull, `{"role":"backend","ref":"ghcr.io/ohcnetwork/care","digest":"` + dgA + `"}`, ""},
		{"pull ref with digest", OpImagePull, `{"role":"backend","ref":"ghcr.io/ohcnetwork/care@` + dgA + `","digest":"` + dgA + `"}`, ""},
		{"pull ref port", OpImagePull, `{"role":"redis","ref":"localhost:5000/redis","digest":"` + dgA + `"}`, ""},
		{"pull ref digest mismatch", OpImagePull, `{"role":"backend","ref":"ghcr.io/x/care@` + dgB + `","digest":"` + dgA + `"}`, "invalid_params:ref"},
		{"pull ref tag", OpImagePull, `{"role":"backend","ref":"ghcr.io/ohcnetwork/care:latest","digest":"` + dgA + `"}`, "invalid_params:ref"},
		{"pull ref flag", OpImagePull, `{"role":"backend","ref":"--help","digest":"` + dgA + `"}`, "invalid_params:ref"},
		{"pull ref traversal", OpImagePull, `{"role":"backend","ref":"../x","digest":"` + dgA + `"}`, "invalid_params:ref"},
		{"pull bad digest", OpImagePull, `{"role":"backend","ref":"ghcr.io/x","digest":"sha256:ABC"}`, "invalid_params:digest"},
		{"pull bad role", OpImagePull, `{"role":"db","ref":"ghcr.io/x","digest":"` + dgA + `"}`, "invalid_params:role"},
		// image.load
		{"load ok", OpImageLoad, `{"role":"silo","digest":"` + dgA + `"}`, ""},
		{"load digest no prefix", OpImageLoad, `{"role":"silo","digest":"` + strings.Repeat("a", 64) + `"}`, "invalid_params:digest"},
		{"load digest short", OpImageLoad, `{"role":"silo","digest":"sha256:abc"}`, "invalid_params:digest"},
		// image.build
		{"build ok", OpImageBuild, `{"kind":"backend-plugins","base":"` + dgA + `","plugins":[{"name":"care_scribe","package":"git+https://github.com/ohcnetwork/care_scribe.git","version":"v1.2.0"},{"name":"abdm","package":"care-abdm"}]}`, ""},
		{"build frontend env", OpImageBuild, `{"kind":"frontend-rebuild","base":"` + dgA + `","build_env":{"REACT_CARE_API_URL":"https://x","CARE_FOO":"1"}}`, ""},
		{"build env key not allowed", OpImageBuild, `{"kind":"frontend-rebuild","base":"` + dgA + `","build_env":{"PATH":"/tmp"}}`, "invalid_params:build_env.PATH"},
		{"build env lowercase", OpImageBuild, `{"kind":"frontend-rebuild","base":"` + dgA + `","build_env":{"react_x":"1"}}`, "invalid_params:build_env.react_x"},
		{"build env newline", OpImageBuild, `{"kind":"frontend-rebuild","base":"` + dgA + `","build_env":{"REACT_X":"a\nRUN evil"}}`, "invalid_params:build_env.REACT_X"},
		{"build bad kind", OpImageBuild, `{"kind":"anything","base":"` + dgA + `"}`, "invalid_params:kind"},
		{"build bad base", OpImageBuild, `{"kind":"backend-plugins","base":"latest"}`, "invalid_params:base"},
		{"build plugin traversal", OpImageBuild, `{"kind":"backend-plugins","base":"` + dgA + `","plugins":[{"name":"../x","package":"p"}]}`, "invalid_params:plugins[0].name"},
		{"build plugin dup", OpImageBuild, `{"kind":"backend-plugins","base":"` + dgA + `","plugins":[{"name":"a","package":"p"},{"name":"a","package":"p"}]}`, "invalid_params:plugins[1].name"},
		{"build package traversal", OpImageBuild, `{"kind":"backend-plugins","base":"` + dgA + `","plugins":[{"name":"a","package":"../x"}]}`, "invalid_params:plugins[0].package"},
		{"build package file url", OpImageBuild, `{"kind":"backend-plugins","base":"` + dgA + `","plugins":[{"name":"a","package":"file:///etc/passwd"}]}`, "invalid_params:plugins[0].package"},
		{"build package flag", OpImageBuild, `{"kind":"backend-plugins","base":"` + dgA + `","plugins":[{"name":"a","package":"-e ."}]}`, "invalid_params:plugins[0].package"},
		{"build version shell", OpImageBuild, `{"kind":"backend-plugins","base":"` + dgA + `","plugins":[{"name":"a","package":"p","version":"1.0 && id"}]}`, "invalid_params:plugins[0].version"},
		// image.prune
		{"prune ok", OpImagePrune, `{"keep":["` + dgA + `","` + dgB + `"]}`, ""},
		{"prune bad", OpImagePrune, `{"keep":["` + dgA + `","nope"]}`, "invalid_params:keep[1]"},
		// db.dump
		{"dump ok", OpDBDump, `{"label":"pre-upgrade-2"}`, ""},
		{"dump empty", OpDBDump, `{}`, "invalid_params:label"},
		{"dump traversal", OpDBDump, `{"label":"../x"}`, "invalid_params:label"},
		{"dump too long", OpDBDump, `{"label":"` + strings.Repeat("a", 41) + `"}`, "invalid_params:label"},
		// db.restore
		{"restore ok", OpDBRestore, `{"backup_id":"20260101T120000Z"}`, ""},
		{"restore traversal", OpDBRestore, `{"backup_id":"../x"}`, "invalid_params:backup_id"},
		{"restore slash", OpDBRestore, `{"backup_id":"a/b"}`, "invalid_params:backup_id"},
		{"restore empty", OpDBRestore, `{}`, "invalid_params:backup_id"},
		// manage
		{"manage super ok", OpManage, `{"command":"createsuperuser","username":"admin","password_file":"admin.pw"}`, ""},
		{"manage sync ok", OpManage, `{"command":"sync_valueset"}`, ""},
		{"manage unknown", OpManage, `{"command":"shell"}`, "invalid_params:command"},
		{"manage pw traversal", OpManage, `{"command":"changepassword","username":"admin","password_file":"../x"}`, "invalid_params:password_file"},
		{"manage pw dotdot", OpManage, `{"command":"changepassword","username":"admin","password_file":".."}`, "invalid_params:password_file"},
		{"manage pw missing", OpManage, `{"command":"changepassword","username":"admin"}`, "invalid_params:password_file"},
		{"manage bad username", OpManage, `{"command":"createsuperuser","username":"a b","password_file":"p"}`, "invalid_params:username"},
		{"manage sync with user", OpManage, `{"command":"sync_permissions_roles","username":"admin"}`, "invalid_params:username"},
		{"manage inline password", OpManage, `{"command":"createsuperuser","username":"a","password":"x"}`, "bad_request"},
		// generic decoding
		{"unknown op", "docker.exec", `{}`, "unknown_op"},
		{"unknown field", OpStackUp, `{"services":["db"],"args":["--privileged"]}`, "bad_request"},
		{"trailing data", OpStackUp, `{} {}`, "bad_request"},
		{"trailing garbage", OpStackUp, `{}x`, "bad_request"},
		{"not object", OpStackUp, `[]`, "bad_request"},
		{"null", OpStackUp, `null`, "bad_request"},
		{"malformed", OpStackUp, `{`, "bad_request"},
		{"wrong type", OpStackUp, `{"wait":"yes"}`, "bad_request"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			op, err := DecodeOp(tc.op, strings.NewReader(tc.body))
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if op.OpName() != tc.op {
					t.Fatalf("OpName = %q, want %q", op.OpName(), tc.op)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error %q, got op %+v", tc.wantErr, op)
			}
			if op != nil {
				t.Fatalf("op must be nil on error, got %+v", op)
			}
			code, field, _ := strings.Cut(tc.wantErr, ":")
			switch code {
			case CodeUnknownOp:
				var ue *UnknownOpError
				if !errors.As(err, &ue) || ue.Op != tc.op {
					t.Fatalf("want UnknownOpError, got %T %v", err, err)
				}
			case CodeBadRequest:
				var de *DecodeError
				if !errors.As(err, &de) {
					t.Fatalf("want DecodeError, got %T %v", err, err)
				}
			case CodeInvalidParams:
				var ve *ValidationError
				if !errors.As(err, &ve) {
					t.Fatalf("want ValidationError, got %T %v", err, err)
				}
				for _, f := range ve.Errors {
					if f.Field == field {
						return
					}
				}
				t.Fatalf("missing field error %q in %v", field, ve.Errors)
			}
		})
	}
}

func TestRegistryComplete(t *testing.T) {
	want := []string{"backup.run", "db.dump", "db.restore", "image.build", "image.load", "image.prune",
		"image.pull", "manage", "migrations.run", "stack.down", "stack.logs", "stack.status", "stack.stop", "stack.up"}
	got := OpNames()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("OpNames = %v", got)
	}
	for _, n := range got {
		if registry[n]().OpName() != n {
			t.Errorf("op %q reports name %q", n, registry[n]().OpName())
		}
	}
}

func TestEnums(t *testing.T) {
	for _, s := range []string{"db", "redis", "minio", "backend", "celery-worker", "celery-beat", "frontend", "caddy", "backup", "migrate"} {
		if !Service(s).Valid() {
			t.Errorf("service %q invalid", s)
		}
	}
	for _, s := range []string{"backend", "frontend", "caddy", "backup", "postgres", "redis", "silo"} {
		if !ImageRole(s).Valid() {
			t.Errorf("role %q invalid", s)
		}
	}
	for _, s := range []string{"config", "build", "restore-input"} {
		if !FileArea(s).Valid() {
			t.Errorf("area %q invalid", s)
		}
	}
	if Service("Backend").Valid() || ImageRole("").Valid() || FileArea("../config").Valid() ||
		BuildKind("x").Valid() || ManageCommand("shell").Valid() {
		t.Error("invalid enum accepted")
	}
	for d, ok := range map[string]bool{
		dgA: true, "sha256:" + strings.Repeat("A", 64): false, "sha512:" + strings.Repeat("a", 64): false,
		dgA + "\n": false, " " + dgA: false, "": false,
	} {
		if Digest(d).Valid() != ok {
			t.Errorf("Digest(%q).Valid() != %v", d, ok)
		}
	}
}

func TestValidFileName(t *testing.T) {
	for n, ok := range map[string]bool{
		"admin.pw": true, "a-b_c.1": true, "": false, ".": false, "..": false, "../x": false,
		"a/b": false, `a\b`: false, ".hidden": false, strings.Repeat("a", 65): false,
	} {
		if ValidFileName(n) != ok {
			t.Errorf("ValidFileName(%q) != %v", n, ok)
		}
	}
}

func TestErrorJSON(t *testing.T) {
	_, err := DecodeOp(OpStackDown, strings.NewReader(`{"remove_volumes":true}`))
	b, _ := json.Marshal(err)
	want := `{"error":"invalid_params","fields":[{"field":"confirm_purge","msg":"must be \"PURGE-ALL-CLINIC-DATA\" when remove_volumes is set"}]}`
	if string(b) != want {
		t.Fatalf("got %s", b)
	}
	b, _ = json.Marshal(&UnknownOpError{Op: "x"})
	if string(b) != `{"error":"unknown_op","op":"x"}` {
		t.Fatalf("got %s", b)
	}
	b, _ = json.Marshal(&ValidationError{})
	if string(b) != `{"error":"invalid_params","fields":[]}` {
		t.Fatalf("got %s", b)
	}
}

func TestBuildEnvErrorsDeterministic(t *testing.T) {
	o := ImageBuild{Kind: BuildFrontendRebuild, Base: Digest(dgA), BuildEnv: map[string]string{"Z": "", "A": "", "M": ""}}
	for range 20 {
		var ve *ValidationError
		if !errors.As(o.Validate(), &ve) || len(ve.Errors) != 3 || ve.Errors[0].Field != "build_env.A" || ve.Errors[2].Field != "build_env.Z" {
			t.Fatalf("unexpected errors %v", ve)
		}
	}
}

func TestFrameRoundTrip(t *testing.T) {
	res, _ := json.Marshal(DBDumpResult{BackupID: "20260101T000000Z"})
	frames := []Frame{
		{Progress: &Progress{Service: ServiceBackend, State: "pulling", Bytes: 10, Total: 100}},
		{Log: &LogLine{Service: ServiceDB, Line: "ready"}},
		{Result: res},
		{Error: &OpError{Code: "failed", Message: "boom"}},
	}
	for i, f := range frames {
		b, err := json.Marshal(f)
		if err != nil {
			t.Fatal(err)
		}
		var g Frame
		if err := json.Unmarshal(b, &g); err != nil {
			t.Fatal(err)
		}
		if g.Terminal() != (i >= 2) {
			t.Errorf("frame %d (%s) Terminal = %v", i, b, g.Terminal())
		}
	}
	b, _ := json.Marshal(frames[2])
	if string(b) != `{"result":{"backup_id":"20260101T000000Z"}}` {
		t.Fatalf("got %s", b)
	}
}
