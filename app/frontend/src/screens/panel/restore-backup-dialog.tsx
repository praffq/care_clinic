import { Clock3, Database, Eye, EyeOff, FileKey, FolderOpen, LoaderCircle, LockKeyhole, RotateCcw } from "lucide-react";
import { useEffect, useRef, useState, type FormEvent } from "react";

import {
  AlertDialog, AlertDialogCancel, AlertDialogContent, AlertDialogDescription,
  AlertDialogFooter, AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Button } from "@/components/ui/button";
import { bridge, onCareEvent } from "@/lib/bridge";
import { errorText } from "@/lib/format";
import { useCare } from "@/state/care-store";
import type { BackupHealth, ImportedBackup } from "@/types";

import { backupDate, BackupNotice, backupProblem, backupWhen, type BackupProblem } from "./backup-ui";
import { usePanelUpdateLock } from "./panel-update-lock";

type RestoreOutcome = "idle" | "running" | "complete" | "failed";

/** Local datetime-local value for a Unix time, to the minute. */
function localInput(seconds: number): string {
  const d = new Date(seconds * 1000);
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

/** The newest moment replay can reach: the archived "saved through" time, never before the full copy finished. */
export function latestRestorable(health?: BackupHealth | null): number {
  return Math.max(health?.saved_through ?? 0, health?.earliest_restore_at ?? 0);
}

/** The RFC 3339 target sent to RestorePointInTime, or "" if it is outside what the backup can reach. */
export function pointInTimeTarget(kind: "latest" | "time", value: string, earliest: number, latest: number): string {
  if (kind === "latest") return "latest";
  const at = new Date(value);
  if (!value || Number.isNaN(at.getTime())) return "";
  if (at.getTime() < earliest * 1000 || at.getTime() > latest * 1000) return "";
  return at.toISOString();
}

export function RestoreBackupFile({ disabled, health }: { disabled: boolean; health?: BackupHealth | null }) {
  const updateLock = usePanelUpdateLock();
  const { busy, busyLabel, restorePending, restoreFile, restorePointInTime, log, setTab } = useCare();
  const [mode, setMode] = useState<"file" | "time">("file");
  const [timeKind, setTimeKind] = useState<"latest" | "time">("latest");
  const [timeValue, setTimeValue] = useState("");
  const [found, setFound] = useState<ImportedBackup | null>(null);
  const [open, setOpen] = useState(false);
  const [choosing, setChoosing] = useState<"backup" | "recovery" | null>(null);
  const [checking, setChecking] = useState(false);
  const [problem, setProblem] = useState<BackupProblem | null>(null);
  const [dialogProblem, setDialogProblem] = useState<BackupProblem | null>(null);
  const [recoveryFile, setRecoveryFile] = useState("");
  const [adminPassword, setAdminPassword] = useState("");
  const [showPassword, setShowPassword] = useState(false);
  const [acknowledged, setAcknowledged] = useState(false);
  const [outcome, setOutcome] = useState<RestoreOutcome>("idle");
  const operation = useRef(false);
  const operationVersion = useRef(0);
  const requested = useRef(false);
  const lastFailure = useRef<BackupProblem | null>(null);
  const blocked = useRef(disabled || busy || restorePending);
  const mounted = useRef(true);
  const passwordInput = useRef<HTMLInputElement>(null);
  const heading = useRef<HTMLHeadingElement>(null);
  blocked.current = disabled || busy || restorePending || updateLock.active;

  const cancelPreflight = () => {
    operationVersion.current++;
    operation.current = false;
    setChoosing(null);
    setChecking(false);
  };

  useEffect(() => {
    mounted.current = true;
    const offUpdate = onCareEvent("app-update-progress", () => {
      if (requested.current) return;
      cancelPreflight();
      setAdminPassword("");
      setShowPassword(false);
      setAcknowledged(false);
    });
    const offError = onCareEvent("care-error", (title: string, detail: string) => {
      if (requested.current && /restore/i.test(title)) {
        lastFailure.current = backupProblem(detail, "restore");
      }
    });
    const offDone = onCareEvent("care-done", (code: number, label?: string) => {
      if (label !== "restore") return;
      requested.current = false;
      setOutcome(code === 0 ? "complete" : "failed");
      setAdminPassword("");
      setShowPassword(false);
      setAcknowledged(false);
      if (code !== 0) {
        setProblem(lastFailure.current ?? backupProblem("", "restore"));
      }
    });
    return () => {
      mounted.current = false;
      operationVersion.current++;
      offUpdate();
      offError();
      offDone();
    };
  }, []);

  const chooseBackup = async () => {
    if (operation.current || blocked.current || updateLock.isActive()) return;
    const version = ++operationVersion.current;
    const revision = updateLock.revision();
    const current = () => mounted.current && version === operationVersion.current &&
      revision === updateLock.revision() && !updateLock.isActive();
    operation.current = true;
    setChoosing("backup");
    try {
      const path = await bridge.ChooseBackupFile();
      if (!path || !current()) return;
      const inspected = await bridge.InspectBackupFile(path);
      if (!current()) return;
      setMode("file");
      setFound(inspected);
      setRecoveryFile("");
      setAdminPassword("");
      setShowPassword(false);
      setAcknowledged(false);
      setProblem(null);
      setDialogProblem(null);
      setOutcome("idle");
      setOpen(true);
    } catch (error) {
      if (current()) {
        log(`backup selection: ${errorText(error)}`);
        setOutcome("idle");
        setProblem(backupProblem(error, "inspect"));
      }
    } finally {
      if (version === operationVersion.current) {
        operation.current = false;
        if (mounted.current) setChoosing(null);
      }
    }
  };

  const chooseRecovery = async () => {
    if (operation.current || blocked.current || updateLock.isActive()) return;
    const version = ++operationVersion.current;
    const revision = updateLock.revision();
    const current = () => mounted.current && version === operationVersion.current &&
      revision === updateLock.revision() && !updateLock.isActive();
    operation.current = true;
    setChoosing("recovery");
    try {
      const path = await bridge.ChooseRecoveryFile();
      if (!path || !current()) return;
      setRecoveryFile(path);
      setDialogProblem(null);
    } catch (error) {
      if (current()) {
        log(`backup recovery selection: ${errorText(error)}`);
        setDialogProblem(backupProblem(error, "recovery"));
      }
    } finally {
      if (version === operationVersion.current) {
        operation.current = false;
        if (mounted.current) setChoosing(null);
      }
    }
  };

  const openPointInTime = () => {
    if (operation.current || blocked.current || updateLock.isActive()) return;
    setMode("time");
    setTimeKind("latest");
    setTimeValue("");
    setRecoveryFile("");
    setAdminPassword("");
    setShowPassword(false);
    setAcknowledged(false);
    setDialogProblem(null);
    setOutcome("idle");
    setOpen(true);
  };

  const close = (next: boolean) => {
    if (requested.current) return;
    setOpen(next);
    if (!next) {
      cancelPreflight();
      setAdminPassword("");
      setShowPassword(false);
      setAcknowledged(false);
      setDialogProblem(null);
    }
  };

  const submitPointInTime = async () => {
    const target = pointInTimeTarget(timeKind, timeValue, health?.earliest_restore_at ?? 0, latestRestorable(health));
    if (operation.current || blocked.current || updateLock.isActive() || !health?.verified || !target ||
      !acknowledged || !adminPassword || !recoveryFile) return;
    const version = ++operationVersion.current;
    const currentOperation = () => mounted.current && version === operationVersion.current;
    operation.current = true;
    setChecking(true);
    setDialogProblem(null);
    try {
      const verified = await bridge.VerifyAdminPassword(adminPassword);
      if (!currentOperation() || updateLock.isActive()) return;
      if (!verified) {
        setDialogProblem(backupProblem("the CARE Clinic admin password does not match", "restore"));
        passwordInput.current?.focus();
        return;
      }
      if (blocked.current) {
        setDialogProblem(backupProblem(restorePending ? "a restore is unfinished" : "something else is still running", "restore"));
        return;
      }
      requested.current = true;
      lastFailure.current = null;
      setProblem(null);
      setOutcome("running");
      await restorePointInTime(target, recoveryFile, adminPassword);
      if (!currentOperation()) return;
      setAdminPassword("");
      setShowPassword(false);
      setAcknowledged(false);
      setOpen(false);
    } catch (error) {
      if (currentOperation()) {
        requested.current = false;
        setOutcome("idle");
        setDialogProblem(backupProblem(error, "restore"));
        log("The point-in-time restore request was not accepted. Check the time, recovery file and CARE Clinic password.");
      }
    } finally {
      if (version === operationVersion.current) {
        operation.current = false;
        if (mounted.current) setChecking(false);
      }
    }
  };

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    if (mode === "time") {
      await submitPointInTime();
      return;
    }
    if (operation.current || blocked.current || updateLock.isActive() || !found || !acknowledged ||
      !adminPassword || (found.encrypted && !recoveryFile)) return;
    const version = ++operationVersion.current;
    const revision = updateLock.revision();
    const currentOperation = () => mounted.current && version === operationVersion.current;
    const currentPreflight = () => currentOperation() &&
      revision === updateLock.revision() && !updateLock.isActive();
    operation.current = true;
    setChecking(true);
    setDialogProblem(null);
    try {
      const current = await bridge.InspectBackupFile(found.path);
      if (!currentPreflight()) return;
      if (current.db_dump !== found.db_dump || current.path !== found.path ||
        current.files_archive !== found.files_archive || current.encrypted !== found.encrypted) {
        setFound(current);
        setAcknowledged(false);
        setDialogProblem({
          title: "The selected backup has changed",
          detail: "Check the backup and its uploaded files below, then confirm the replacement again.",
        });
        return;
      }
      const verified = await bridge.VerifyAdminPassword(adminPassword);
      if (!currentPreflight()) return;
      if (!verified) {
        setDialogProblem(backupProblem("the CARE Clinic admin password does not match", "restore"));
        passwordInput.current?.focus();
        return;
      }
      if (blocked.current) {
        setDialogProblem(backupProblem(restorePending ? "a restore is unfinished" : "something else is still running", "restore"));
        return;
      }
      requested.current = true;
      lastFailure.current = null;
      setProblem(null);
      setOutcome("running");
      await restoreFile(found.path, found.encrypted ? recoveryFile : "", adminPassword);
      if (!currentOperation()) return;
      setAdminPassword("");
      setShowPassword(false);
      setAcknowledged(false);
      setOpen(false);
    } catch (error) {
      if (currentOperation()) {
        requested.current = false;
        setOutcome("idle");
        setDialogProblem(backupProblem(error, "restore"));
        // Native methods record their detailed failures; never log credentials.
        log("The restore request was not accepted. Check the backup, recovery file and CARE Clinic password.");
      }
    } finally {
      if (version === operationVersion.current) {
        operation.current = false;
        if (mounted.current) setChecking(false);
      }
    }
  };

  const restoring = (busy && busyLabel === "Restoring") || outcome === "running";
  const waiting = disabled || busy || restorePending || !!choosing || checking || restoring || updateLock.active;
  const earliest = health?.earliest_restore_at ?? 0;
  const latest = latestRestorable(health);
  const pitrTarget = pointInTimeTarget(timeKind, timeValue, earliest, latest);
  const canPointInTime = !!health?.verified && earliest > 0 && !health?.wal?.gap;
  const canSubmit = mode === "time"
    ? !waiting && canPointInTime && !!pitrTarget && acknowledged && !!adminPassword && !!recoveryFile
    : !waiting && !!found && acknowledged && !!adminPassword && (!found.encrypted || !!recoveryFile);
  const needsRecovery = mode === "time" || !!found?.encrypted;
  const made = found ? backupWhen(backupDate(found.db_dump)) : "";
  const madeInSentence = made.replace(/^(Today|Yesterday)\b/, (day) => day.toLowerCase());

  return (
    <section className="care-backups-restore" aria-labelledby="care-backups-restore-title">
      <div className="care-backups-card care-backups-restore-card">
        <div className="care-backups-card-row">
          <span className="care-backups-icon"><FolderOpen aria-hidden="true" /></span>
          <div className="care-backups-grow">
            <h2 id="care-backups-restore-title">Restore from a backup file</h2>
            <p>Choose a backup from this computer or another clinic computer. Restoring replaces the clinic's current data.</p>
          </div>
          <Button disabled={waiting} onClick={() => void chooseBackup()}>
            {choosing === "backup" ? <><LoaderCircle className="care-backups-spin" aria-hidden="true" />Checking file…</>
              : found ? "Choose a different file" : "Choose file"}
          </Button>
        </div>
        {found ? (
          <div className="care-backups-selected">
            <Database aria-hidden="true" />
            <div className="care-backups-grow">
              <strong title={found.path}>{found.db_dump}</strong>
              <p>{found.files_archive ? "Database and uploaded files" : "Database only"}{found.encrypted ? " · encrypted" : " · not encrypted"} · {made}</p>
            </div>
            <Button
              className="care-backups-outline-danger"
              disabled={waiting}
              onClick={() => {
                if (operation.current || blocked.current || updateLock.isActive()) return;
                setDialogProblem(null);
                setOpen(true);
              }}
            >
              <RotateCcw aria-hidden="true" />Restore this file
            </Button>
          </div>
        ) : null}
      </div>

      <div className="care-backups-card care-backups-restore-card care-backups-pitr-card">
        <div className="care-backups-card-row">
          <span className="care-backups-icon"><Clock3 aria-hidden="true" /></span>
          <div className="care-backups-grow">
            <h2>Restore to a point in time</h2>
            <p>{canPointInTime
              ? `Rebuild the database as it was at any time from ${backupWhen(new Date(earliest * 1000))} until the latest saved change. Uploaded files are not changed.`
              : health?.wal?.gap
                ? "Unavailable while a saved change file is missing. The next verified full backup starts a new unbroken chain."
                : "Available after the first verified backup on this computer."}</p>
          </div>
          <Button disabled={waiting || !canPointInTime} onClick={openPointInTime}>Choose time</Button>
        </div>
      </div>

      {restoring ? (
        <BackupNotice title="Restoring the selected backup…" tone="info" log={false}>
          CARE is checking and restoring the backup. Keep CARE Clinic open and don't turn this computer off.
        </BackupNotice>
      ) : outcome === "complete" && !restorePending ? (
        <BackupNotice title="The backup was restored" tone="success" log={false}>
          CARE is running again with the restored data.
        </BackupNotice>
      ) : problem ? (
        <BackupNotice title={problem.title}>{problem.detail}</BackupNotice>
      ) : null}

      <AlertDialog open={open} onOpenChange={close}>
        <AlertDialogContent
          className="care-restore-dialog"
          onOpenAutoFocus={(event) => { event.preventDefault(); heading.current?.focus(); }}
          onEscapeKeyDown={(event) => { if (requested.current) event.preventDefault(); }}
        >
          <form onSubmit={(event) => void submit(event)}>
            <div className="care-restore-heading">
              <span className="care-backups-icon care-backups-icon--danger"><RotateCcw aria-hidden="true" /></span>
              <div>
                <AlertDialogTitle ref={heading} tabIndex={-1}>
                  {mode === "time" ? "Restore the database to a point in time?"
                    : `Restore the backup ${made === "Date unavailable" ? "you selected" : `from ${madeInSentence}`}?`}
                </AlertDialogTitle>
                <AlertDialogDescription>
                  {mode === "time"
                    ? "CARE rebuilds the database from the newest verified backup and saved changes in a separate copy, and only replaces today's records after it checks out. Changes after the chosen time will be lost. Uploaded files are not changed."
                    : "Today's clinic records will be replaced with this copy. Changes made since this backup will be lost."}
                  {" "}CARE pauses while it restores. This can't be undone.
                </AlertDialogDescription>
              </div>
            </div>

            {mode === "time" ? (
              <fieldset className="care-restore-file care-restore-time" disabled={waiting}>
                <legend>Restore to</legend>
                <label>
                  <input type="radio" name="care-restore-time" checked={timeKind === "latest"} onChange={() => setTimeKind("latest")} />
                  <span>The latest saved change{health?.saved_through ? ` (${backupWhen(new Date(health.saved_through * 1000))})` : ""}</span>
                </label>
                <label>
                  <input type="radio" name="care-restore-time" checked={timeKind === "time"} onChange={() => setTimeKind("time")} />
                  <span>A specific time</span>
                </label>
                {timeKind === "time" ? (
                  <input
                    type="datetime-local" aria-label="Restore time"
                    min={earliest ? localInput(earliest) : undefined} max={localInput(latest)}
                    value={timeValue} onChange={(event) => setTimeValue(event.target.value)}
                  />
                ) : null}
                {timeKind === "time" && timeValue && !pitrTarget ? (
                  <p>Choose a time between {backupWhen(new Date(earliest * 1000))} and {backupWhen(new Date(latest * 1000))}, the latest saved change.</p>
                ) : null}
              </fieldset>
            ) : found ? (
              <div className="care-restore-file">
                <strong title={found.path}>{found.db_dump}</strong>
                <span>{found.files_archive ? "Database and uploaded files" : "Database only"}{found.encrypted ? " · encrypted" : " · not encrypted"}</span>
                {!found.files_archive ? (
                  <p>Uploaded files are not included and will not be restored. If you have the matching files archive, put it beside this backup before continuing.</p>
                ) : null}
              </div>
            ) : null}

            <div className="care-restore-fields">
              {needsRecovery ? (
                <div className="care-restore-field">
                  <label id="care-restore-recovery-label">Backup recovery file</label>
                  <div className="care-restore-input">
                    <FileKey aria-hidden="true" />
                    <span aria-labelledby="care-restore-recovery-label" title={recoveryFile}>
                      {recoveryFile || "Select the file saved during setup"}
                    </span>
                    <Button type="button" size="sm" disabled={waiting} onClick={() => void chooseRecovery()}>
                      {choosing === "recovery" ? "Checking…" : "Choose recovery file"}
                    </Button>
                  </div>
                  <p>Keep it safe and don't share it. Use the file from this backup's clinic, not CARE Clinic recovery codes. Without it, encrypted backups cannot be unlocked.</p>
                </div>
              ) : null}

              <div className="care-restore-field">
                <label htmlFor="care-restore-password">CARE Clinic admin password</label>
                <div className="care-restore-input">
                  <LockKeyhole aria-hidden="true" />
                  <input
                    id="care-restore-password"
                    ref={passwordInput}
                    type={showPassword ? "text" : "password"}
                    autoComplete="current-password"
                    spellCheck={false}
                    value={adminPassword}
                    aria-describedby="care-restore-password-hint"
                    disabled={waiting}
                    onChange={(event) => setAdminPassword(event.target.value)}
                    placeholder="Your CARE Clinic admin password"
                  />
                  <Button
                    type="button" variant="ghost" size="sm" disabled={waiting}
                    aria-label={showPassword ? "Hide admin password" : "Show admin password"}
                    aria-pressed={showPassword}
                    onClick={() => setShowPassword((value) => !value)}
                  >
                    {showPassword ? <EyeOff aria-hidden="true" /> : <Eye aria-hidden="true" />}
                  </Button>
                </div>
                <div id="care-restore-password-hint" className="care-restore-password-hint">
                  <span>For this CARE Clinic installation, not the CARE web login.</span>
                  <Button
                    type="button" variant="ghost" size="bare" disabled={requested.current}
                    onClick={() => { close(false); setTab("advanced"); }}
                  >Forgot CARE Clinic password?</Button>
                </div>
              </div>

              <label className="care-restore-ack">
                <input
                  type="checkbox" checked={acknowledged} disabled={waiting}
                  onChange={(event) => setAcknowledged(event.target.checked)}
                />
                <span>I understand today's data will be replaced</span>
              </label>
            </div>

            {updateLock.active && !requested.current ? (
              <BackupNotice title="Finish the CARE Clinic update first" tone="info" log={false}>
                Your backup selection is kept. Finish the update in Updates before restoring.
              </BackupNotice>
            ) : restorePending ? (
              <BackupNotice title="An earlier restore needs attention" log={false}>
                Open Overview and start CARE to recover safely. A second restore cannot start yet.
              </BackupNotice>
            ) : busy && !requested.current ? (
              <BackupNotice title="Another task is running" tone="info" log={false}>
                Your selection is kept. Wait for the current task to finish before restoring.
              </BackupNotice>
            ) : null}
            {dialogProblem ? <BackupNotice title={dialogProblem.title}>{dialogProblem.detail}</BackupNotice> : null}

            <AlertDialogFooter className="care-restore-footer">
              <AlertDialogCancel disabled={requested.current}>Cancel</AlertDialogCancel>
              <Button type="submit" variant="destructive" disabled={!canSubmit}>
                {checking ? <><LoaderCircle className="care-backups-spin" aria-hidden="true" />Checking…</> : "Replace current data"}
              </Button>
            </AlertDialogFooter>
          </form>
        </AlertDialogContent>
      </AlertDialog>
    </section>
  );
}
