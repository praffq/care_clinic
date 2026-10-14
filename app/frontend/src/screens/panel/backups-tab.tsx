import { Archive, Check, Clock3, Database, FolderOpen, HardDrive, LoaderCircle, RefreshCw, ShieldCheck, TriangleAlert } from "lucide-react";
import { useCallback, useEffect, useRef, useState } from "react";

import { Button } from "@/components/ui/button";
import { bridge, onCareEvent } from "@/lib/bridge";
import { diskSize, errorText } from "@/lib/format";
import { useCare } from "@/state/care-store";
import type { BackupCopyStatus, BackupHealth } from "@/types";

import {
  backupGroups, BackupNotice, backupProblem, backupSpaceSummary, backupWhen,
  type BackupProblem,
} from "./backup-ui";
import { PanelPageHeader } from "./panel-ui";
import { backupFailureDetail } from "./panel-status";
import { usePanelUpdateLock } from "./panel-update-lock";
import { RestoreBackupFile } from "./restore-backup-dialog";
import { ExportBackupRecovery } from "./export-backup-recovery";
import "./backups.css";

export function BackupsTab() {
  const updateLock = usePanelUpdateLock();
  const {
    backups, backupsError, busy, busyLabel, restorePending, storage, storageError, log, tab, operationError,
    runAction, reloadBackups, recheckStorage, setTab,
  } = useCare();
  const [dir, setDir] = useState("");
  const [folderLoading, setFolderLoading] = useState(true);
  const [folderProblem, setFolderProblem] = useState<BackupProblem | null>(null);
  const [folderChanged, setFolderChanged] = useState(false);
  const [health, setHealth] = useState<BackupHealth | null>(null);
  const [healthProblem, setHealthProblem] = useState(false);
  const [copyProblem, setCopyProblem] = useState<BackupProblem | null>(null);
  const [working, setWorking] = useState<"folder" | "refresh" | "backup" | "copy" | null>(null);
  const [actionProblem, setActionProblem] = useState<BackupProblem | null>(null);
  const [backupComplete, setBackupComplete] = useState(false);
  const [now, setNow] = useState(() => new Date());
  const operation = useRef(false);
  const blocked = useRef(busy || restorePending);
  const mounted = useRef(true);
  const space = storage?.backup;
  const run = storage?.last_run;
  const automaticRunning = run?.state === "running" && run.at > 0 &&
    now.getTime() - run.at * 1000 < 6 * 60 * 60 * 1000;
  blocked.current = busy || restorePending || automaticRunning || folderLoading || updateLock.active;

  const readHealth = useCallback(async () => {
    try {
      const next = await bridge.GetBackupHealth();
      if (mounted.current) {
        setHealth(next);
        setHealthProblem(false);
      }
    } catch (error) {
      if (mounted.current) {
        log(`backup health: ${errorText(error)}`);
        setHealth(null);
        setHealthProblem(true);
      }
    }
  }, [log]);

  const readFolder = useCallback(async () => {
    try {
      const path = await bridge.GetBackupDir();
      if (mounted.current) {
        setDir(path);
        setFolderProblem(null);
      }
    } catch (error) {
      if (mounted.current) {
        log(`backup folder: ${errorText(error)}`);
        setDir("");
        setFolderProblem({
          title: "The backup folder couldn't be checked",
          detail: "Try Refresh again. Your existing backups have not been moved.",
        });
      }
    } finally {
      if (mounted.current) setFolderLoading(false);
    }
  }, [log]);

  useEffect(() => {
    mounted.current = true;
    const clock = window.setInterval(() => setNow(new Date()), 60_000);
    const off = onCareEvent("care-done", (code: number, label?: string) => {
      if (label !== "backup-now") return;
      setBackupComplete(code === 0);
      setActionProblem(code === 0 ? null : backupProblem("", "backup"));
    });
    const offCopy = onCareEvent("care-backup-copy", (copy: BackupCopyStatus) => {
      if (!mounted.current || !copy) return;
      setHealth((current) => current ? { ...current, copy } : current);
      void readHealth();
    });
    return () => {
      mounted.current = false;
      window.clearInterval(clock);
      off();
      offCopy();
    };
  }, [readHealth]);

  useEffect(() => {
    if (tab !== "backups") return;
    setFolderLoading(true);
    void readFolder();
    void readHealth();
  }, [tab, readFolder, readHealth]);

  useEffect(() => {
    if (storage?.health) setHealth(storage.health);
  }, [storage]);

  const refresh = async () => {
    if (operation.current || blocked.current || updateLock.isActive()) return;
    operation.current = true;
    setWorking("refresh");
    setActionProblem(null);
    try {
      await Promise.all([readFolder(), readHealth(), reloadBackups(), recheckStorage()]);
      if (mounted.current) setNow(new Date());
    } catch (error) {
      if (mounted.current) setActionProblem(backupProblem(error, "storage"));
    } finally {
      operation.current = false;
      if (mounted.current) setWorking(null);
    }
  };

  const changeFolder = async () => {
    if (operation.current || blocked.current || updateLock.isActive()) return;
    const revision = updateLock.revision();
    operation.current = true;
    setWorking("folder");
    try {
      const chosen = await bridge.ChooseFolder("Choose where backups should go");
      if (!chosen || !mounted.current) return;
      if (revision !== updateLock.revision() || updateLock.isActive()) {
        setFolderProblem({
          title: "Choose the backup folder again",
          detail: "A CARE Clinic update interrupted this change. Your backup folder has not changed.",
        });
        return;
      }
      if (blocked.current) {
        setFolderProblem(backupProblem(restorePending ? "a restore is unfinished" : "something else is still running", "folder"));
        return;
      }
      const target = await bridge.SetBackupDir(chosen);
      if (!mounted.current) return;
      setDir(target);
      setFolderProblem(null);
      setFolderChanged(target !== dir);
      setBackupComplete(false);
      try {
        await Promise.all([reloadBackups(), recheckStorage()]);
      } catch (error) {
        if (mounted.current) setActionProblem(backupProblem(error, "storage"));
      }
    } catch (error) {
      if (mounted.current) {
        log(`backup folder: ${errorText(error)}`);
        await readFolder();
        if (!mounted.current) return;
        setFolderProblem(backupProblem(error, "folder"));
      }
    } finally {
      operation.current = false;
      if (mounted.current) setWorking(null);
    }
  };

  const chooseCopy = async () => {
    if (operation.current || blocked.current || updateLock.isActive()) return;
    operation.current = true;
    setWorking("copy");
    setCopyProblem(null);
    try {
      const chosen = await bridge.ChooseFolder("Choose an external drive, second disk or network folder for a backup copy");
      if (!chosen || !mounted.current) return;
      await bridge.SetBackupCopyDir(chosen);
      await readHealth();
    } catch (error) {
      if (mounted.current) {
        log(`backup copy: ${errorText(error)}`);
        setCopyProblem({ title: "The backup copy folder wasn't changed", detail: errorText(error) });
      }
    } finally {
      operation.current = false;
      if (mounted.current) setWorking(null);
    }
  };

  const stopCopy = async () => {
    if (operation.current || blocked.current || updateLock.isActive()) return;
    operation.current = true;
    setWorking("copy");
    setCopyProblem(null);
    try {
      await bridge.ClearBackupCopyDir();
      await readHealth();
    } catch (error) {
      if (mounted.current) {
        log(`backup copy: ${errorText(error)}`);
        setCopyProblem({ title: "The backup copy couldn't be turned off", detail: errorText(error) });
      }
    } finally {
      operation.current = false;
      if (mounted.current) setWorking(null);
    }
  };

  const backUpNow = async () => {
    if (operation.current || blocked.current || updateLock.isActive()) return;
    operation.current = true;
    setWorking("backup");
    setActionProblem(null);
    setBackupComplete(false);
    try {
      await runAction("backup-now");
    } catch (error) {
      if (mounted.current) setActionProblem(backupProblem(error, "backup"));
    } finally {
      operation.current = false;
      if (mounted.current) setWorking(null);
    }
  };

  const backingUp = (busy && busyLabel === "Backing up") || working === "backup";
  const disabled = busy || restorePending || !!working || automaticRunning || folderLoading || updateLock.active;
  const measured = !storageError && !!space && space.total > 0 && space.level !== "unknown" &&
    !!dir && space.dir === dir;
  const tone = measured ? space.level : "unknown";
  const usedPercent = measured ? Math.min(100, Math.max(0, (1 - space.free / space.total) * 100)) : 0;
  const groups = backupGroups(backups, now);
  const lastBackup = storage?.newest_backup_at
    ? backupWhen(new Date(storage.newest_backup_at * 1000), now) : "";
  const lastRun = run?.at ? backupWhen(new Date(run.at * 1000), now) : "";
  const badge = tone === "critical" ? "Low space" : tone === "low" ? "Low space"
    : tone === "ok" ? "Healthy" : folderLoading ? "Checking" : "Not checked";
  const when = (seconds: number) => seconds > 0 ? backupWhen(new Date(seconds * 1000), now) : "";
  const copy = health?.copy;
  const copyOn = !!copy && copy.state !== "" && copy.state !== "off";
  const alerts = health?.alerts ?? [];
  const localActionProblem = operationError?.action === "backup-now" ? null : actionProblem;

  return (
    <>
    <PanelPageHeader title="Backups" subtitle="Safe copies of your patient data, made automatically." />
    <div className="care-backups">
      <section className={`care-backups-card care-backups-folder care-backups-folder--${tone}`} aria-label="Backup folder">
        <div className="care-backups-card-row">
          <span className={`care-backups-icon care-backups-icon--${tone}`}><FolderOpen aria-hidden="true" /></span>
          <div className="care-backups-grow">
            <div className="care-backups-eyebrow">Backups are saved to</div>
            <p className="care-backups-folder-path" title={dir}>{dir || (folderLoading ? "Checking backup folder…" : "Folder unavailable")}</p>
            {measured ? (
              <>
                <div className={`care-backups-meter care-backups-meter--${tone}`}
                  role="meter" aria-label="Backup folder space used" aria-valuemin={0} aria-valuemax={100} aria-valuenow={Math.round(usedPercent)}>
                  <span style={{ width: `${usedPercent}%` }} />
                </div>
                <div className="care-backups-space">
                  <span>{backupSpaceSummary(space)}</span>
                  <span>{diskSize(space.free)} free of {diskSize(space.total)}</span>
                </div>
              </>
            ) : <p className="care-backups-space-unavailable">Free space {folderLoading ? "is being checked" : "couldn't be checked"}</p>}
          </div>
          <span className={`care-backups-badge care-backups-badge--${tone}`}>
            {tone === "ok" ? <Check aria-hidden="true" /> : null}{badge}
          </span>
          <Button disabled={disabled} onClick={() => void changeFolder()}>
            {working === "folder" ? "Changing folder…" : "Change folder"}
          </Button>
        </div>
        {measured && space.need > 0 ? <p className="care-backups-folder-note">Each backup needs about {diskSize(space.need)}.</p> : null}
        {measured && space.shares_docker_drive ? (
          <p className="care-backups-folder-note">This folder shares the drive with the clinic's data. Keep another copy on a different drive to protect against drive failure.</p>
        ) : null}
      </section>

      {folderProblem ? <BackupNotice title={folderProblem.title}>{folderProblem.detail}</BackupNotice>
        : folderChanged ? (
          <BackupNotice title="The backup folder has changed" tone="success" log={false}>
            New backups will be saved here. Earlier backups stay in the previous folder.
          </BackupNotice>
        ) : null}

      {storageError ? (
        <BackupNotice title="Storage couldn't be checked">
          The last reading may be out of date. Try Refresh again before relying on the available space.
        </BackupNotice>
      ) : null}

      <div className="care-backups-toolbar">
        <Button variant="primary" disabled={disabled} onClick={() => void backUpNow()}>
          {backingUp || automaticRunning ? <LoaderCircle className="care-backups-spin" aria-hidden="true" /> : <Archive aria-hidden="true" />}
          {backingUp || automaticRunning ? "Backing up…" : "Back up now"}
        </Button>
        <div className="care-backups-schedule">
          <span>Full copy every night · changes saved every few minutes</span>
          <span className="care-backups-keep">Only the newest verified backup is kept</span>
        </div>
        {!backupsError ? <span className="care-backups-count">{backups.length} {backups.length === 1 ? "backup" : "backups"}</span> : null}
        <Button variant="ghost" size="sm" disabled={disabled} onClick={() => void refresh()}>
          <RefreshCw aria-hidden="true" className={working === "refresh" ? "care-backups-spin" : undefined} />
          {working === "refresh" ? "Refreshing…" : "Refresh"}
        </Button>
      </div>

      <section className="care-backups-card care-backups-health" aria-label="Backup health">
        <div className="care-backups-card-row">
          <span className={`care-backups-icon care-backups-icon--${health?.verified ? "ok" : health ? "low" : "unknown"}`}>
            {health?.verified ? <ShieldCheck aria-hidden="true" /> : <TriangleAlert aria-hidden="true" />}
          </span>
          <div className="care-backups-grow">
            <div className="care-backups-eyebrow">Latest backup</div>
            <p className="care-backups-health-when">{health?.last_backup_at ? when(health.last_backup_at)
              : health ? "No verified backup yet" : healthProblem ? "Backup status couldn't be checked" : "Checking backup status…"}</p>
            <dl className="care-backups-health-facts">
              <div><dt>Checks</dt><dd>{health?.verified ? "✓ Verified" : health?.last_backup_at ? "Not verified" : "—"}</dd></div>
              <div><dt>Changes saved through</dt><dd>{health?.saved_through ? when(health.saved_through) : "—"}</dd></div>
              <div><dt>Weekly trial restore</dt><dd>{health?.trial?.state === "passed" ? `Passed ${when(health.trial.at)}`
                : health?.trial?.state === "failed" ? "Failed" : health?.trial?.state === "skipped" ? "Skipped" : "Not run yet"}</dd></div>
            </dl>
          </div>
          {health?.verified ? <span className="care-backups-badge care-backups-badge--ok"><Check aria-hidden="true" />Verified</span> : null}
        </div>
      </section>

      {healthProblem ? (
        <BackupNotice title="Backup status couldn't be read">
          Refresh to check again. Automatic backups continue while CARE is running.
        </BackupNotice>
      ) : null}

      {alerts.map((alert) => (
        <BackupNotice key={alert.code} title={alert.message}
          tone={alert.level === "critical" ? undefined : "warning"} log={false}>
          {alertHelp(alert.code)}
        </BackupNotice>
      ))}

      <section className="care-backups-card care-backups-copy" aria-label="Backup copy">
        <div className="care-backups-card-row">
          <span className="care-backups-icon"><HardDrive aria-hidden="true" /></span>
          <div className="care-backups-grow">
            <div className="care-backups-eyebrow">Extra copy (optional)</div>
            <p className="care-backups-copy-path" title={copy?.dir}>{copyOn ? copy?.dir : "Not set up"}</p>
            <p className="care-backups-copy-state">{copyOn ? copyStateText(copy!, when) :
              "Keep a second copy on an external drive, second disk or network folder. Backups stay on this computer either way."}</p>
          </div>
          <Button disabled={disabled} onClick={() => void chooseCopy()}>
            {working === "copy" ? "Saving…" : copyOn ? "Change" : "Choose drive"}
          </Button>
          {copyOn ? <Button variant="ghost" disabled={disabled} onClick={() => void stopCopy()}>Stop copying</Button> : null}
        </div>
      </section>

      {copyProblem ? <BackupNotice title={copyProblem.title}>{copyProblem.detail}</BackupNotice> : null}

      {restorePending ? (
        <BackupNotice title="An earlier restore needs attention"
          actions={<Button onClick={() => setTab("overview")}>Open Overview</Button>}>
          Start CARE from Overview to recover safely before backing up, changing folders or restoring another file.
        </BackupNotice>
      ) : null}

      {localActionProblem ? <BackupNotice title={localActionProblem.title}>{localActionProblem.detail}</BackupNotice>
        : backingUp || automaticRunning ? (
          <BackupNotice title="A backup is running" tone="info" log={false}>
            Keep CARE Clinic open. The new copy will appear after the backup finishes.
          </BackupNotice>
        ) : backupComplete && run?.state === "ok" && !storage?.stale ? (
          <BackupNotice title="The backup finished" tone="success" log={false}>
            Refresh the list if the new copy hasn't appeared yet.
          </BackupNotice>
        ) : null}

      {run?.state === "failed" && !backingUp && !automaticRunning ? (
        <BackupNotice title={run.reason === "disk_full" ? "The last backup didn't finish — there wasn't enough space" : "The last backup didn't finish"}
          actions={<>
            {run.reason !== "database_unavailable" ? <Button disabled={disabled} onClick={() => void changeFolder()}>Choose another folder</Button> : null}
            <Button disabled={disabled} onClick={() => void backUpNow()}>Try again now</Button>
          </>}>
          {run.reason === "disk_full"
            ? `${run.need_bytes > 0 ? `It needed about ${diskSize(run.need_bytes)} and ${diskSize(run.free_bytes)} was free. ` : ""}Free up space or choose another folder.`
            : backupFailureDetail(run.reason)}
          {lastRun ? ` Last attempt: ${lastRun}.` : ""}
        </BackupNotice>
      ) : storage?.stale && !backingUp && !automaticRunning ? (
        <BackupNotice title={lastBackup ? `No backup since ${lastBackup}` : "No recent backup"} tone="warning"
          actions={<>
            <Button variant="primary" disabled={disabled} onClick={() => void backUpNow()}><Archive aria-hidden="true" />Back up now</Button>
            <Button disabled={disabled} onClick={() => void changeFolder()}>Choose another folder</Button>
          </>} log={false}>
          Check that the backup folder is available, then back up now. Automatic backups continue while CARE is running.
        </BackupNotice>
      ) : lastRun && run?.state === "ok" ? (
        <p className="care-backups-last-run"><Clock3 aria-hidden="true" />Last completed backup: {lastRun}</p>
      ) : lastBackup ? (
        <p className="care-backups-last-run"><Clock3 aria-hidden="true" />Latest backup: {lastBackup}</p>
      ) : null}

      {backupsError ? (
        <BackupNotice title="The backup list couldn't be read">
          Check that the backup folder is available, then try Refresh. Your existing backups have not been changed.
        </BackupNotice>
      ) : backups.length === 0 ? (
        <div className="care-backups-card care-backups-empty">
          <span className="care-backups-icon"><Archive aria-hidden="true" /></span>
          <h2>{measured ? "No backups yet" : "No backups listed"}</h2>
          <p>{measured
            ? "The first backup runs automatically after the clinic starts. Or choose Back up now."
            : "Check that the backup folder is available, or choose another folder."}</p>
        </div>
      ) : (
        <section className="care-backups-list" aria-label="Saved backups">
          {groups.map(([day, entries]) => (
            <div key={day}>
              <h2>{day}</h2>
              <ul>
                {entries.map(({ backup, date }) => (
                  <li key={backup.db_dump}>
                    <span className="care-backups-icon care-backups-icon--small"><Database aria-hidden="true" /></span>
                    <div className="care-backups-grow">
                      <h3 title={backup.db_dump}>{backupWhen(date, now)}</h3>
                      <p>{backup.size_bytes > 0 ? <>
                        {diskSize(backup.size_bytes)} database · {backup.files_archive ? "database and files" : "database only"}
                        {backup.encrypted ? " · encrypted" : " · not encrypted"}
                      </> : "Empty database file · not ready to restore"}</p>
                    </div>
                    <span className={`care-backups-kind${backup.manual ? " care-backups-kind--manual" : ""}`}>
                      {backup.manual ? "Manual" : "Automatic"}
                    </span>
                  </li>
                ))}
              </ul>
            </div>
          ))}
        </section>
      )}

      <ExportBackupRecovery disabled={disabled} />
      <RestoreBackupFile disabled={!!working || automaticRunning || folderLoading} health={health} />
    </div>
    </>
  );
}

function copyStateText(copy: BackupCopyStatus, when: (seconds: number) => string): string {
  const last = copy.last_copied_at ? `Last copied ${when(copy.last_copied_at)}.` : "Not copied yet.";
  switch (copy.state) {
    case "ok": return `Up to date. ${last}`;
    case "copying": return `Copying… ${last}`;
    case "waiting": return "Waiting for the first verified backup.";
    case "disconnected": return `Disconnected. ${last} It catches up when the drive is connected again.`;
    case "failed": return `Copy failed: ${copy.message || "unknown error"}. ${last}`;
    default: return last;
  }
}

function alertHelp(code: string): string {
  if (code.startsWith("copy_")) return "Your backups on this computer are not affected. Connect the drive or choose another folder.";
  if (code.startsWith("wal_")) return "Keep CARE running. If this continues, back up now and check the backup folder.";
  if (code === "backup_failed") return "The previous verified backup was kept. Try Back up now after fixing the cause.";
  if (code.startsWith("trial_")) return "The nightly backup is still verified. The trial restore runs again next week.";
  return "Try Back up now, then Refresh.";
}
