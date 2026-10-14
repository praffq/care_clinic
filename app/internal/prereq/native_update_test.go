package prereq

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ohcnetwork/care_desktop/app/internal/release"
	"github.com/ohcnetwork/care_desktop/app/internal/sys/proc"
)

func TestNativeUpdateHealthCommitAndRollback(t *testing.T) {
	for _, failure := range []string{"", "stop", "activate", "ready", "health", "commit"} {
		t.Run(failure, func(t *testing.T) {
			var calls []string
			step := func(name string) error {
				calls = append(calls, name)
				if failure == name {
					return errors.New(name + " failed")
				}
				return nil
			}
			ops := nativeUpdateOps{
				activate: func(string, string) error { return step("activate") },
				ready:    func() error { return step("ready") },
				commit:   func() error { return step("commit") },
				rollback: func() error { return step("rollback") },
			}
			err := applyNativeAgentUpdate("verified-agent", "version-record", func() error { return step("stop") }, func() error { return step("health") }, ops)
			expected := map[string]string{
				"":         "[stop activate ready health commit]",
				"stop":     "[stop]",
				"activate": "[stop activate rollback]",
				"ready":    "[stop activate ready rollback]",
				"health":   "[stop activate ready health rollback]",
				"commit":   "[stop activate ready health commit rollback]",
			}[failure]
			if fmt.Sprint(calls) != expected || (err != nil) != (failure != "") {
				t.Fatalf("calls=%v error=%v; expected %s", calls, err, expected)
			}
		})
	}
}

func TestNativeUpdateJournalsBeforeActivationAndKeepsRollbackRetryable(t *testing.T) {
	command := nativeActivateCommand("/download/agent", "/download/version")
	marker := strings.Index(command, "touch "+nativeUpdateMarker)
	stop := strings.Index(command, "systemctl stop")
	replace := strings.Index(command, "mv -f")
	if marker < 0 || stop <= marker || replace <= stop ||
		!strings.Contains(command[:marker], careAgentBinPath+".previous") ||
		!strings.Contains(command[:marker], careAgentVersionPath+".previous") {
		t.Fatalf("native update is not journaled before activation: %s", command)
	}
	command = nativeRollbackCommand()
	if strings.Index(command, "systemctl start") > strings.Index(command, "rm -f "+nativeUpdateMarker) {
		t.Fatalf("recovery marker removed before restart: %s", command)
	}
	cause := errors.New("health failed")
	err := rollbackNativeUpdate(cause, func() error { return errors.New("restart failed") })
	if !errors.Is(err, cause) || !strings.Contains(err.Error(), "recovery will retry") {
		t.Fatalf("recovery failure lost: %v", err)
	}
}

func TestNativeDownloadFailsClosedOnMissingOrUnpublishedAssets(t *testing.T) {
	engine := release.EngineUpdate{Version: "0.1.3", Assets: map[string]release.Asset{}}
	if _, err := linuxAgentDownload(engine, "amd64"); err == nil {
		t.Fatal("missing signed native asset accepted")
	}
	asset := release.Asset{URL: "https://example.test/care-engine-v0.1.3/care-engine-agent-linux-amd64", SHA256: strings.Repeat("a", 64), Size: 42}
	for _, invalid := range []release.Asset{
		{URL: asset.URL, SHA256: strings.Repeat("0", 64), Size: 42},
		{URL: strings.Replace(asset.URL, "v0.1.3", "v0.1.2", 1), SHA256: asset.SHA256, Size: 42},
		{URL: strings.Replace(asset.URL, "https:", "http:", 1), SHA256: asset.SHA256, Size: 42},
		{URL: asset.URL, SHA256: asset.SHA256},
	} {
		engine.Assets["native-linux-amd64"] = invalid
		if _, err := linuxAgentDownload(engine, "amd64"); err == nil {
			t.Errorf("invalid signed native asset accepted: %+v", invalid)
		}
	}
	pr := &Provisioner{}
	stopped := false
	err := pr.installEngineDownloads([]Download{{Name: "unpublished bundle", SHA256: strings.Repeat("0", 64)}}, func() error { stopped = true; return nil })
	if err == nil || !strings.Contains(err.Error(), "bootstrap is blocked") || stopped {
		t.Fatalf("unpublished VM bootstrap was not blocked before download/stop: %v", err)
	}
}

func TestNativeUpdateRequiresVerifiedManifest(t *testing.T) {
	pr := NewProvisioner(proc.Runner{Dir: t.TempDir()}, &release.Pins{CareVMVersion: "0.1.3", AppVersion: "0.1.19"}, nil)
	if _, err := pr.signedNativeEngine("0.1.3", false); err == nil || !strings.Contains(err.Error(), "signed engine manifest") {
		t.Fatalf("missing manifest trusted: %v", err)
	}
	client := release.NewClient(pr.ReleaseDir)
	if err := os.MkdirAll(filepath.Dir(client.StatePath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(client.StatePath, []byte(`{"highest_revision":1,"manifest":null,"signature":null}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := pr.signedNativeEngine("0.1.3", false); err == nil {
		t.Fatal("untrusted cached manifest accepted for native installation")
	}
}
