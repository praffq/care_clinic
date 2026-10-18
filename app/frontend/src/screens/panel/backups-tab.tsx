import { DatabaseBackup, FolderOpen, HardDriveDownload, History, KeyRound, MonitorSmartphone, RefreshCw, RotateCcw, Usb } from "lucide-react";
import { useCallback, useEffect, useRef, useState } from "react";

import { Spinner } from "@/components/spinner";
import { Button } from "@/components/ui/button";
import { Progress } from "@/components/ui/progress";
import { toast } from "@/components/ui/sonner";
import { bridge, onCareEvent } from "@/lib/bridge";
import { dateTime, diskSize, errorText } from "@/lib/format";
import { useCare } from "@/state/care-store";
import type { BackupProgress, BackupSet, BackupsState } from "@/types";
import { AdminConfirmDialog } from "./panel-journal";
import { PanelBadge, PanelLogButton, PanelNotice, PanelPageHeader, usePanelTask } from "./panel-ui";
import { usePanelUpdateLock } from "./panel-update-lock";

const BACKUP_JOB = "backup";
const RESTORE_JOB = "restore";
export const BACKING_UP = "Backing up the clinic";
export const RESTORING = "Restoring a backup";

/** Backups copied out of the engine to a folder on this computer, and
 *  restoring one of them. */
export function BackupsTab({ locked: panelLocked }: { locked: boolean }) {
  const care = useCare();
  const updateLock = usePanelUpdateLock();
  const active = care.flow === "panel" && care.tab === "backups";
  const [state, setState] = useState<BackupsState | null>(null);
  const [loadError, setLoadError] = useState("");
  const [progress, setProgress] = useState<BackupProgress | null>(null);
  const [restoring, setRestoring] = useState<BackupSet | null>(null);
  const [choosing, setChoosing] = useState(false);
  const refresh = usePanelTask();
  const start = usePanelTask();
  const generation = useRef(0);
  const wasBusy = useRef(care.busy);

  const reload = useCallback(async () => {
    const mine = ++generation.current;
    try {
      const next = await bridge.ListBackups();
      if (mine !== generation.current) return;
      setState(next);
      setLoadError("");
    } catch (cause) {
      if (mine !== generation.current) return;
      setLoadError(errorText(cause));
      throw cause;
    }
  }, []);
  useEffect(() => { if (active) void reload().catch(() => undefined); }, [active, reload]);
  useEffect(() => {
    if (wasBusy.current && !care.busy) {
      setProgress(null);
      if (active) void reload().catch(() => undefined);
    }
    wasBusy.current = care.busy;
  }, [active, care.busy, reload]);
  useEffect(() => onCareEvent("backup-progress", (next: BackupProgress) => setProgress(next)), []);

  const backingUp = care.busy && care.busyLabel === BACKING_UP;
  const restoringNow = care.busy && care.busyLabel === RESTORING;
  const locked = panelLocked || updateLock.active || start.working;
  const status = state?.status;
  const latest = state?.sets.find((set) => set.latest);
  const copiedAt = status?.verified_at;
  const copy = status?.copy;
  const advanced = <Button size="sm" variant="ghost" onClick={() => care.setTab("advanced")}>Open Advanced</Button>;

  const backUp = () => {
    if (locked || updateLock.isActive()) return;
    setProgress(null);
    void start.run(() => care.runJob(BACKUP_JOB, BACKING_UP, () => bridge.BackUpNow()),
      "The backup didn't start. Try again, or open the log file for support.");
  };
  const restore = async (set: BackupSet, password: string) => {
    if (care.busy || updateLock.isActive()) throw new Error("something else is still running");
    setProgress(null);
    if (!set.dir) throw new Error("only backups in the backup folder can be restored");
    const dir = set.dir;
    await care.runJob(RESTORE_JOB, RESTORING, () => bridge.RestoreBackup(dir, "", password));
    setRestoring(null);
  };
  const chooseFolder = async (password: string) => {
    const dir = await bridge.ChooseBackupFolder(password);
    setChoosing(false);
    if (dir) {
      toast(`Backups now go to ${dir}`);
      await reload().catch(() => undefined);
    }
  };

  const healthy = status && !status.critical && !status.last_error && !status.message && !!copiedAt;
  return <div className="panel-page" aria-label="Backups">
    <PanelPageHeader title="Backups" subtitle="Copies of your clinic's records, kept in a folder on this computer." />
    {loadError && !state ? <PanelNotice tone="warning" title="Couldn't read the backups"
      actions={<Button disabled={refresh.working} onClick={() => void refresh.run(reload, "Still couldn't read the backups. Check the clinic on Overview.")}>Try again</Button>}>
      Check that the clinic is running on Overview, then try again.
    </PanelNotice> : null}
    {status?.critical ? <PanelNotice tone="danger" title="Backups aren't being copied out">
      {status.message || `${status.pending} backups are waiting inside the engine.`} Check that the backup folder is
      connected and has space, then back up now.
    </PanelNotice> : status?.message ? <PanelNotice tone="warning" title="Backups need attention">{status.message}</PanelNotice> : null}
    {status?.last_error ? <PanelNotice tone="danger" title="The last copy to the backup folder failed">
      {status.last_error}{status.last_error_at ? ` (${dateTime(status.last_error_at)})` : ""}
    </PanelNotice> : null}
    {copy?.message ? <PanelNotice tone="warning" title="The extra copy needs attention" actions={advanced}>{copy.message}</PanelNotice> : null}
    {state?.engine_error ? <PanelNotice tone="warning" title="Couldn't list the backups inside the engine">
      {state.engine_error} Backups already in the folder are shown below.
    </PanelNotice> : null}

    <section className="panel-card panel-card-pad panel-update-card" aria-label="Backup status">
      <div className="panel-update-head">
        <span className="panel-tile"><DatabaseBackup aria-hidden="true" /></span>
        <div className="panel-grow">
          <h2 className="panel-title">Daily backups</h2>
          <p className="panel-card-sub">{copiedAt ? `Last copied out ${dateTime(copiedAt)}` : state ? "Not copied out yet" : "Reading backups…"}
            {status?.checked_at ? ` · Checked ${dateTime(status.checked_at)}` : ""}</p>
        </div>
        <PanelBadge tone={backingUp || restoringNow ? "neutral" : healthy ? "ok" : state ? "warning" : "neutral"} working={backingUp || restoringNow || !state}>
          {backingUp ? "Backing up" : restoringNow ? "Restoring" : !state ? "Reading" : healthy ? "Copied out" : "Needs attention"}
        </PanelBadge>
        <Button variant="ghost" aria-label="Check backups again" disabled={refresh.working || !state}
          onClick={() => void refresh.run(reload, "Couldn't read the backups. Try again.")}>
          {refresh.working ? <Spinner /> : <RefreshCw aria-hidden="true" />}Check again
        </Button>
        <Button variant="primary" disabled={locked || !state} onClick={backUp}>
          <HardDriveDownload aria-hidden="true" />Back up now
        </Button>
      </div>
      {(backingUp || restoringNow) ? <BackupProgressView progress={progress} restoring={restoringNow} /> : null}
      <div className="panel-backup-details">
        <div className="panel-row panel-between panel-detail-row">
          <span className="panel-small">Backup folder</span>
          <span className="panel-row panel-folder">
            <span className="panel-mono" title={state?.dir}>{state?.dir || "Not set"}</span>
            <Button size="sm" disabled={locked || !state} onClick={() => setChoosing(true)}>
              <FolderOpen aria-hidden="true" />Change folder…
            </Button>
          </span>
        </div>
        <div className="panel-row panel-between panel-detail-row">
          <span className="panel-small">Latest copy</span>
          <span>{latest ? <>{dateTime(latest.created_at)} <span className="panel-small">· {diskSize(latest.size)}</span></> : "None yet"}</span>
        </div>
        <div className="panel-row panel-between panel-detail-row">
          <span className="panel-small">Waiting to be copied</span>
          <span>{status ? status.pending === 0 ? "None" : `${status.pending}${status.oldest_pending_at ? ` · oldest ${dateTime(status.oldest_pending_at)}` : ""}` : "—"}</span>
        </div>
        <div className="panel-row panel-between panel-detail-row" data-hook="extra-copy">
          <span className="panel-small"><Usb aria-hidden="true" className="panel-inline-icon" />Extra copy (USB drive or network share)</span>
          <span className="panel-row panel-folder">
            {copy ? <>
              <span className="panel-mono" title={copy.dir}>{copy.dir}</span>
              {copy.connected && copy.copied_at ? <PanelBadge tone="ok">Copied {dateTime(copy.copied_at)}</PanelBadge>
                : <PanelBadge tone="warning">{copy.connected ? "Not copied yet" : "Not connected"}</PanelBadge>}
            </> : <span>Not set up</span>}
            {advanced}
          </span>
        </div>
      </div>
      {start.error ? <p className="panel-inline-error" role="alert">{start.error}</p> : null}
      {refresh.error ? <p className="panel-inline-error" role="alert">{refresh.error}</p> : null}
    </section>

    <section className="panel-card panel-card-pad" aria-label="Backup sets">
      <h2 className="panel-eyebrow panel-history-title"><History aria-hidden="true" />Backups on this computer</h2>
      {state && state.sets.length === 0 ? <p className="panel-small">No backups yet. CARE backs up every day; use Back up now to take one straight away.</p> : null}
      {state && state.sets.length > 0 ? <ul className="panel-backup-sets" aria-label="Backups">
        {state.sets.map((set) => <li key={set.id} className="panel-history-row">
          <div className="panel-grow">
            <div>{dateTime(set.created_at) || set.id}</div>
            <p className="panel-small"><span className="panel-mono">{set.id}</span> · {diskSize(set.size)}</p>
          </div>
          {set.latest && set.verified_at ? <PanelBadge tone="ok">Verified</PanelBadge>
            : set.copied ? <PanelBadge>Copied</PanelBadge> : <PanelBadge tone="warning">Not copied out</PanelBadge>}
          <Button size="sm" disabled={locked || !set.copied || !set.dir} aria-label={`Restore the backup from ${dateTime(set.created_at) || set.id}`}
            title={set.copied ? undefined : "Only backups copied to the backup folder can be restored."}
            onClick={() => setRestoring(set)}><RotateCcw aria-hidden="true" />Restore…</Button>
        </li>)}
      </ul> : null}
      <div className="panel-row panel-between panel-backup-foot" data-hook="restore-new-computer">
        <p className="panel-small panel-grow"><MonitorSmartphone aria-hidden="true" className="panel-inline-icon" />Restoring from the
          extra copy, or a backup brought from another computer? Use Restore from backup in Advanced, under Backup recovery.</p>
        {advanced}
      </div>
    </section>
    <div className="panel-update-foot">
      <p className="panel-small panel-grow"><KeyRound aria-hidden="true" className="panel-inline-icon" />Backups are encrypted. Save the
        recovery key from Advanced and keep it away from this computer: it's needed to restore on a new one.</p>
      <PanelLogButton />
    </div>

    {restoring ? <AdminConfirmDialog destructive title={`Restore the backup from ${dateTime(restoring.created_at) || restoring.id}?`}
      icon={<RotateCcw aria-hidden="true" />} confirmLabel="Restore this backup"
      description={<>
        <PanelNotice tone="danger" title="Everything recorded after this backup will be lost">
          The clinic's records and files are replaced with the copy from {dateTime(restoring.created_at) || restoring.id}.
        </PanelNotice>
        <ul className="panel-plan-list">
          <li>CARE stops and staff are signed out until the restore finishes.</li>
          <li>If you might need today's records, cancel and use Back up now first.</li>
        </ul>
      </>}
      onConfirm={(password) => restore(restoring, password)} onClose={() => setRestoring(null)} /> : null}
    {choosing ? <AdminConfirmDialog title="Change the backup folder?" icon={<FolderOpen aria-hidden="true" />}
      confirmLabel="Choose folder…"
      description={<p>New backups are copied to the folder you choose. Pick a folder on a disk with plenty of space;
        backups already copied stay where they are.</p>}
      onConfirm={chooseFolder} onClose={() => setChoosing(false)} /> : null}
  </div>;
}

function BackupProgressView({ progress, restoring }: { progress: BackupProgress | null; restoring: boolean }) {
  const percent = progress?.total ? Math.max(0, Math.min(100, Math.round((progress.bytes ?? 0) / progress.total * 100))) : undefined;
  return <div className="panel-update-progress" role="status" aria-live="polite">
    <h3>{restoring ? "Restoring the backup…" : "Backing up the clinic…"}</h3>
    <Progress value={percent} aria-label={restoring ? "Restore progress" : "Backup progress"} />
    <p className="panel-small">{progress?.message || (restoring ? "Starting the restore" : "Starting the backup")}
      {progress?.total ? ` · ${diskSize(progress.bytes ?? 0)} of ${diskSize(progress.total)}` : ""}</p>
    <p className="panel-small">{restoring ? "CARE is unavailable until the restore finishes. Keep this computer on."
      : "CARE keeps running while it backs up."}</p>
  </div>;
}
