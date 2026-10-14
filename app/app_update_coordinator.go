package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"time"

	"github.com/ohcnetwork/care_desktop/app/internal/compose"
	"github.com/ohcnetwork/care_desktop/app/internal/health"
	"github.com/ohcnetwork/care_desktop/app/internal/prereq"
	"github.com/ohcnetwork/care_desktop/app/internal/release"
	"github.com/ohcnetwork/care_desktop/app/internal/sys/diskspace"
	"github.com/ohcnetwork/care_desktop/app/internal/sys/power"
	"github.com/ohcnetwork/care_desktop/app/internal/updates"
	"github.com/ohcnetwork/care_desktop/app/internal/vm"
)

const updateStagingReserve = uint64(1 << 30)

func (a *App) runCoordinatedUpdate(request updates.UpdateType, desktopApply func(*updates.Journal, updates.Checkpoint) error) error {
	if a.updateCoordinator == nil {
		return errors.New("the update coordinator is unavailable")
	}
	client := release.NewClient(a.installDir())
	manifest, raw, err := client.ReadVerified()
	if err != nil {
		return fmt.Errorf("check signed updates before applying: %w", err)
	}
	if err := a.updateCoordinator.SaveVerifiedManifest(manifest, raw); err != nil {
		return err
	}
	engine := a.EngineStatus()
	currentEngine := engine.InstalledVersion
	if currentEngine == "" {
		currentEngine = a.pins.CareVMVersion
	}
	order, err := updates.DependencyOrder(a.pins.AppVersion, currentEngine, manifest)
	if err != nil {
		return err
	}
	steps := make([]updates.UpdateType, 0, 3)
	switch request {
	case updates.Desktop, updates.Engine:
		changed := manifest.Desktop.Version != a.pins.AppVersion
		if request == updates.Engine {
			changed = manifest.Engine.Version != currentEngine || engine.UpdateNeeded
		}
		if !changed {
			return fmt.Errorf("there is no prepared %s update to apply", request)
		}
		steps = append(steps, order...)
		if len(steps) == 0 {
			steps = append(steps, request)
		}
	case updates.CAREImages:
		if !updates.Compatible(manifest.Care.RequiresDesktop, manifest.Desktop.Version) ||
			!updates.Compatible(manifest.Care.RequiresEngine, manifest.Engine.Version) {
			return errors.New("the signed CARE image release is incompatible with its desktop or engine requirements")
		}
		steps = append(steps, order...)
		steps = append(steps, updates.CAREImages)
	default:
		return fmt.Errorf("unsupported update request %q", request)
	}
	from, err := a.updateSnapshot()
	if err != nil {
		return err
	}
	to := from
	to.Files = append([]updates.FileDigest(nil), from.Files...)
	for _, step := range steps {
		switch step {
		case updates.Desktop:
			to.DesktopVersion = manifest.Desktop.Version
		case updates.Engine:
			to.EngineVersion = manifest.Engine.Version
		case updates.CAREImages:
			to.BackendDigest = manifest.Care.Backend.Image
			to.FrontendDigest = manifest.Care.Frontend.Image
		}
	}
	journal, err := a.updateCoordinator.Create(manifest.Revision, from, to, updateStepNames(steps))
	if err != nil {
		return err
	}
	operator, err := user.Current()
	if err != nil {
		return fmt.Errorf("identify the local staff member approving the update: %w", err)
	}
	if operator.Username == "" {
		return errors.New("identify the local staff member approving the update: operating system username is empty")
	}
	if _, err := a.updateCoordinator.Approve(journal.PlanID, "local staff ("+operator.Username+")"); err != nil {
		return err
	}
	if err := a.updateCoordinator.Execute(context.Background(), journal.PlanID, a.updateHooks(manifest, desktopApply)); err != nil {
		return err
	}
	active, err := a.updateCoordinator.Journal()
	if err != nil {
		return err
	}
	if active == nil {
		return a.updateCoordinator.SaveInstalled(to, from.Files...)
	}
	return nil
}

func (a *App) updateHooks(manifest release.Manifest, desktopApply func(*updates.Journal, updates.Checkpoint) error) updates.Hooks {
	if desktopApply == nil {
		desktopApply = a.installStagedDesktopUpdate
	}
	return updates.Hooks{
		Preflight: func(ctx context.Context, plan *updates.Journal) error {
			return a.updatePreflight(ctx, plan, manifest)
		},
		Apply: func(_ context.Context, name string, plan *updates.Journal, checkpoint updates.Checkpoint) (string, error) {
			switch updates.UpdateType(name) {
			case updates.CAREImages:
				return "restore previous CARE images and pre-update backup", a.engine().ApplyUpdateWithJournal(plan, checkpoint)
			case updates.Engine:
				plan.Rollback.EnginePrevious = plan.From.EngineVersion
				if err := checkpoint(); err != nil {
					return "", err
				}
				return "restore previous CARE engine", a.updateEngine()
			case updates.Desktop:
				if err := desktopApply(plan, checkpoint); err != nil {
					return "", err
				}
				return "previous desktop application retained beside the installed application", updates.ErrHandoffPending
			default:
				return "", fmt.Errorf("unsupported update step %q", name)
			}
		},
		Rollback: func(_ context.Context, name string, plan updates.Journal) error {
			switch updates.UpdateType(name) {
			case updates.CAREImages:
				return a.engine().RollbackUpdate(plan.Rollback.BackupID, plan.Rollback.DBMigrated)
			case updates.Engine:
				return a.provisioner().RecoverEngineUpdate()
			case updates.Desktop:
				return errors.New("the desktop update helper did not finish; use its retained previous app recovery copy")
			default:
				return fmt.Errorf("unsupported update rollback step %q", name)
			}
		},
	}
}

func (a *App) recoverUpdatePlan(ctx context.Context) error {
	if a.updateCoordinator == nil {
		return errors.New("the update coordinator is unavailable")
	}
	journal, err := a.updateCoordinator.Journal()
	if err != nil || journal == nil {
		return err
	}
	desktopStep := -1
	for i, step := range journal.Steps {
		if step.Name == string(updates.Desktop) {
			desktopStep = i
			break
		}
	}
	if journal.State == updates.Running && desktopStep >= 0 && journal.To.DesktopVersion == a.pins.AppVersion {
		if journal.Steps[desktopStep].Status == updates.StepRunning {
			if err := a.updateCoordinator.CompleteHandoff(journal.PlanID, a.pins.AppVersion); err != nil {
				return err
			}
		} else if journal.Steps[desktopStep].Status != updates.StepDone {
			return fmt.Errorf("desktop update plan is interrupted before its handoff: step status %s", journal.Steps[desktopStep].Status)
		}
		manifest, _, err := release.NewClient(a.installDir()).ReadVerified()
		if err != nil {
			return fmt.Errorf("resume approved update plan: %w", err)
		}
		if manifest.Revision != journal.ManifestRevision {
			return fmt.Errorf("resume approved update plan: signed manifest revision changed from %d to %d", journal.ManifestRevision, manifest.Revision)
		}
		if err := a.updateCoordinator.Execute(ctx, journal.PlanID, a.updateHooks(manifest, a.installStagedDesktopUpdate)); err != nil {
			return err
		}
		if remaining, err := a.updateCoordinator.Journal(); err != nil {
			return err
		} else if remaining == nil {
			return a.updateCoordinator.SaveInstalled(journal.To, journal.From.Files...)
		}
		return nil
	}
	return a.updateCoordinator.Recover(ctx, updates.Hooks{
		Rollback: func(_ context.Context, name string, plan updates.Journal) error {
			switch updates.UpdateType(name) {
			case updates.CAREImages:
				return a.engine().RollbackUpdate(plan.Rollback.BackupID, plan.Rollback.DBMigrated)
			case updates.Engine:
				return a.provisioner().RecoverEngineUpdate()
			case updates.Desktop:
				if a.pins.AppVersion != plan.To.DesktopVersion {
					return nil
				}
				return errors.New("the updated desktop is running but this plan needs rollback; the retained previous application must be restored")
			default:
				return fmt.Errorf("unsupported update recovery step %q", name)
			}
		},
	})
}

func updateStepNames(steps []updates.UpdateType) []string {
	names := make([]string, len(steps))
	for i, step := range steps {
		names[i] = string(step)
	}
	return names
}

func (a *App) updateSnapshot() (updates.Snapshot, error) {
	status := a.EngineStatus()
	engineVersion := status.InstalledVersion
	if engineVersion == "" {
		engineVersion = a.pins.CareVMVersion
	}
	lock := compose.ReadLock(a.installDir())
	s := updates.Snapshot{
		DesktopVersion: a.pins.AppVersion,
		EngineVersion:  engineVersion,
		BackendDigest:  lock.Backend.Current.Image,
		FrontendDigest: lock.Frontend.Current.Image,
	}
	executable, err := os.Executable()
	if err != nil {
		return updates.Snapshot{}, fmt.Errorf("locate installed CARE Clinic executable: %w", err)
	}
	digest, err := fileSHA256(executable)
	if err != nil {
		return updates.Snapshot{}, fmt.Errorf("fingerprint installed CARE Clinic executable: %w", err)
	}
	s.Files = append(s.Files, updates.FileDigest{Path: executable, SHA256: digest})
	settings, hash, err := updateSettingsDigests(a.installDir())
	if err != nil {
		return updates.Snapshot{}, err
	}
	s.Files = append(s.Files, settings...)
	s.SettingsHash = hash
	return s, nil
}

func updateSettingsDigests(dir string) ([]updates.FileDigest, string, error) {
	h := sha256.New()
	var files []updates.FileDigest
	for _, name := range []string{"backend.env", "frontend.env", "backend.overrides.env", "frontend.overrides.env", "secrets.env", "env-defaults.yml", "plugins.json"} {
		path := filepath.Join(dir, name)
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, "", fmt.Errorf("inspect settings file for update fingerprint %s: %w", name, err)
		}
		if !info.Mode().IsRegular() {
			return nil, "", fmt.Errorf("settings file for update fingerprint %s is not a regular file", name)
		}
		digest, err := fileSHA256(path)
		if err != nil {
			return nil, "", fmt.Errorf("fingerprint settings file %s: %w", name, err)
		}
		files = append(files, updates.FileDigest{Path: path, SHA256: digest})
		_, _ = fmt.Fprintf(h, "%s\x00%s\x00", name, digest)
	}
	return files, hex.EncodeToString(h.Sum(nil)), nil
}

func (a *App) updatePreflight(ctx context.Context, plan *updates.Journal, manifest release.Manifest) error {
	now := time.Now()
	if now.Year() < 2020 || now.Year() > 2100 {
		return errors.New("the computer clock is not set to a sane date; correct it before updating")
	}
	published, err := time.Parse(time.RFC3339, manifest.PublishedAt)
	if err != nil || published.After(now.Add(5*time.Minute)) {
		return errors.New("the computer clock is behind the signed update manifest; correct it before updating")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	onAC, detectable, err := power.OnACPower()
	if err != nil {
		return err
	}
	if detectable && !onAC {
		return errors.New("connect this computer to mains power before updating")
	}
	required := updateStagingReserve
	needsContainers := true
	for _, step := range plan.Steps {
		switch updates.UpdateType(step.Name) {
		case updates.CAREImages:
			needsContainers = true
			e := a.engine()
			if e.Builder().Waiting().Backend != "" {
				if !e.Backups().BackupEncryptionOn() {
					return errors.New("encrypted backup recovery is unavailable; CARE updates are blocked until backup protection is repaired")
				}
				required += e.UpdateBackupNeed()
			}
		case updates.Engine:
			needsContainers = true
			info, err := a.provisioner().EngineDownloadInfo()
			if err != nil {
				return fmt.Errorf("estimate CARE engine update space: %w", err)
			}
			required += uint64(max(info.Size, 0))
		case updates.Desktop:
			if asset, ok := manifest.Desktop.Assets[desktopPlatformKey()]; ok {
				required += uint64(asset.Size)
			}
		}
	}
	appSpace, err := diskspace.Of(a.installDir())
	if err != nil {
		return fmt.Errorf("check free space for update staging: %w", err)
	}
	if appSpace.Free < required {
		return fmt.Errorf("not enough free disk space for the update and rollback artifacts: need %d bytes, have %d", required, appSpace.Free)
	}
	if a.loadConfig().Role == roleServer && a.loadConfig().SetupDone {
		backupSpace, err := diskspace.Of(a.engine().BackupDirPath())
		if err != nil {
			return fmt.Errorf("check backup free space: %w", err)
		}
		need := a.engine().UpdateBackupNeed()
		if backupSpace.Free < need {
			return fmt.Errorf("not enough free space for the pre-update backup: need %d bytes, have %d", need, backupSpace.Free)
		}
		if needsContainers {
			status := prereq.DockerCheck(a.engine().Runner())
			if !status.OK {
				return fmt.Errorf("the container engine is not healthy: %s", status.Message)
			}
			if !health.Ping().Active {
				return errors.New("CARE containers are not healthy; start CARE and retry the update")
			}
			if vm.Supported() {
				engineStatus := vm.Status(ctx)
				if !engineStatus.Running {
					return errors.New("the CARE engine is not healthy; start it and retry the update")
				}
			}
			plan.From.LocalImageIDs = nil
			for _, image := range []string{plan.From.BackendDigest, plan.From.FrontendDigest} {
				if image == "" {
					continue
				}
				id, err := a.engine().Runner().Capture("docker", "image", "inspect", "--format", "{{.Id}}", image)
				if err != nil {
					return fmt.Errorf("read installed image ID for %s: %w", image, err)
				}
				plan.From.LocalImageIDs = append(plan.From.LocalImageIDs, id)
			}
		}
	}
	return nil
}

func desktopPlatformKey() string {
	if runtime.GOOS == "darwin" {
		return "darwin-universal"
	}
	if runtime.GOOS == "windows" {
		return "windows-amd64"
	}
	return ""
}
