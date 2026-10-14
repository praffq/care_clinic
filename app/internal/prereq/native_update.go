package prereq

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/ohcnetwork/care_desktop/app/internal/sys/elevate"
	"github.com/ohcnetwork/care_desktop/app/internal/sys/proc"
)

const nativeUpdateMarker = "/usr/local/share/care-clinic/agent.update-pending"

type nativeUpdateOps struct {
	activate func(string, string) error
	ready    func() error
	rollback func() error
	commit   func() error
}

// UpdateNativeAgentWithHealth is called only by the approved update coordinator.
// The signed download is verified before the clinic stops, and the root-owned
// recovery marker survives desktop exits and power loss until health commits.
func (pr *Provisioner) UpdateNativeAgentWithHealth(version string, beforeStop, clinicHealth func() error) error {
	if runtime.GOOS != "linux" {
		return errors.New("native agent updates are only supported on Linux")
	}
	if err := pr.RecoverNativeAgentUpdate(); err != nil {
		return err
	}
	installed, _, err := pr.NativeAgentVersions()
	if err != nil {
		return err
	}
	if installed == "" {
		return errors.New("install the initial CARE agent through setup before requesting an update")
	}
	engine, err := pr.signedNativeEngine(version, false)
	if err != nil {
		return err
	}
	download, err := linuxAgentDownload(engine, runtime.GOARCH)
	if err != nil {
		return err
	}
	agent, err := pr.download(download)
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(agent) }()
	versionFile := filepath.Join(filepath.Dir(agent), filepath.Base(agent)+".version")
	if err := os.WriteFile(versionFile, []byte(version+"\n"), 0o600); err != nil {
		return err
	}
	defer func() { _ = os.Remove(versionFile) }()
	ops := nativeUpdateOps{
		activate: func(binary, record string) error {
			return elevate.Run(nativeActivateCommand(binary, record), true)
		},
		ready: func() error {
			return waitForDockerReady(careAgentReadyWait, func() bool {
				return DockerCheck(proc.Runner{}).OK
			}, pr.logln)
		},
		rollback: pr.RecoverNativeAgentUpdate,
		commit: func() error {
			return elevate.Run(nativeCommitCommand(), true)
		},
	}
	return applyNativeAgentUpdate(agent, versionFile, beforeStop, clinicHealth, ops)
}

func applyNativeAgentUpdate(binary, version string, beforeStop, health func() error, ops nativeUpdateOps) error {
	if beforeStop != nil {
		if err := beforeStop(); err != nil {
			return err
		}
	}
	if err := ops.activate(binary, version); err != nil {
		return rollbackNativeUpdate(err, ops.rollback)
	}
	if err := ops.ready(); err != nil {
		return rollbackNativeUpdate(err, ops.rollback)
	}
	if health != nil {
		if err := health(); err != nil {
			return rollbackNativeUpdate(err, ops.rollback)
		}
	}
	if err := ops.commit(); err != nil {
		return rollbackNativeUpdate(err, ops.rollback)
	}
	return nil
}

func rollbackNativeUpdate(cause error, rollback func() error) error {
	if err := rollback(); err != nil {
		return fmt.Errorf("%w; restoring the previous CARE agent failed: %v (recovery will retry before startup)", cause, err)
	}
	return fmt.Errorf("%w; restored the previous CARE agent", cause)
}

func nativeActivateCommand(binary, version string) string {
	return strings.Join([]string{
		"test ! -e " + nativeUpdateMarker,
		"install -o root -g root -m 0755 " + careAgentBinPath + " " + careAgentBinPath + ".previous",
		"install -o root -g root -m 0644 " + careAgentVersionPath + " " + careAgentVersionPath + ".previous",
		"install -o root -g root -m 0755 " + elevate.ShQuote(binary) + " " + careAgentBinPath + ".next",
		"install -o root -g root -m 0644 " + elevate.ShQuote(version) + " " + careAgentVersionPath + ".next",
		"touch " + nativeUpdateMarker,
		"sync",
		"systemctl stop care-engine-agent",
		"mv -f " + careAgentBinPath + ".next " + careAgentBinPath,
		"mv -f " + careAgentVersionPath + ".next " + careAgentVersionPath,
		"sync",
		"systemctl start care-engine-agent",
	}, " && ")
}

func nativeCommitCommand() string {
	// Removing and syncing the marker commits; stale backup files are harmless.
	return "rm -f " + nativeUpdateMarker + " && sync && rm -f " +
		careAgentBinPath + ".previous " + careAgentVersionPath + ".previous"
}

func nativeRollbackCommand() string {
	return strings.Join([]string{
		"test -f " + careAgentBinPath + ".previous",
		"test -f " + careAgentVersionPath + ".previous",
		"systemctl stop care-engine-agent",
		"install -o root -g root -m 0755 " + careAgentBinPath + ".previous " + careAgentBinPath + ".next",
		"install -o root -g root -m 0644 " + careAgentVersionPath + ".previous " + careAgentVersionPath + ".next",
		"mv -f " + careAgentBinPath + ".next " + careAgentBinPath,
		"mv -f " + careAgentVersionPath + ".next " + careAgentVersionPath,
		"sync",
		"systemctl start care-engine-agent",
		nativeCommitCommand(),
	}, " && ")
}

// RecoverNativeAgentUpdate restores, rather than resumes, an uncommitted update.
func (pr *Provisioner) RecoverNativeAgentUpdate() error {
	if runtime.GOOS != "linux" {
		return nil
	}
	if _, err := os.Lstat(nativeUpdateMarker); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return fmt.Errorf("inspect native agent recovery marker: %w", err)
	}
	pr.logln("Restoring the previous CARE agent after an interrupted update...")
	if err := elevate.Run(nativeRollbackCommand(), true); err != nil {
		return fmt.Errorf("restore interrupted native agent update: %w", err)
	}
	return nil
}
