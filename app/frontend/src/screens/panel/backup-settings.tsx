import { Archive, DatabaseBackup, FolderInput, FolderX, KeyRound } from "lucide-react";
import { useRef, useState } from "react";

import { Spinner } from "@/components/spinner";
import {
  AlertDialog, AlertDialogContent, AlertDialogDescription, AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { toast } from "@/components/ui/sonner";
import { bridge } from "@/lib/bridge";
import { when } from "@/lib/format";
import { useCare } from "@/state/care-store";
import type { RestoreChoice } from "@/types";
import { AdvancedError, advancedProblem, useAdvancedLock, type AdvancedProblem } from "./advanced-ui";

type Task = "key" | "copy" | "clear" | "choose" | "restore";

/** Backup recovery key, extra copy and restore (Advanced). */
export function BackupSettings({ adminPassword }: { adminPassword: string }) {
  const { busy, service, runJob } = useCare();
  const lock = useAdvancedLock();
  const [task, setTask] = useState<Task | null>(null);
  const [problem, setProblem] = useState<AdvancedProblem | null>(null);
  const [otherKey, setOtherKey] = useState(false);
  const [choice, setChoice] = useState<RestoreChoice | null>(null);
  const pending = useRef(false);
  const copy = service?.status?.backups?.copy;
  const blocked = busy || lock.disabled || task !== null;

  const act = async (name: Task, fn: () => Promise<void>, title: string, message: string) => {
    if (pending.current || busy || lock.isLocked()) return;
    pending.current = true;
    setTask(name);
    setProblem(null);
    try {
      await fn();
    } catch (cause) {
      setProblem(advancedProblem(cause, title, message));
    } finally {
      pending.current = false;
      setTask(null);
    }
  };
  const saveKey = () => act("key", async () => {
    const path = await bridge.ExportBackupRecoveryKey(adminPassword);
    if (path) toast(`Recovery key saved to ${path}`);
  }, "The recovery key wasn't saved", "Choose another folder, for example a USB drive kept away from this computer.");
  const setCopy = () => act("copy", async () => {
    const dir = await bridge.SetBackupCopyDir(adminPassword);
    if (dir) toast(`Backups will also be copied to ${dir}`);
  }, "The extra copy wasn't set up", "Choose a folder on a connected USB drive or network share, then try again.");
  const clearCopy = () => act("clear", async () => {
    await bridge.ClearBackupCopyDir(adminPassword);
    toast("Backups are no longer copied a second time");
  }, "The extra copy wasn't turned off", "Try again when CARE Clinic is ready.");
  const choose = () => act("choose", async () => {
    const next = await bridge.ChooseBackupToRestore(otherKey);
    if (next) setChoice(next);
  }, "That backup can't be restored", "Choose the backup folder (or one backup inside it) and, from another computer, its recovery key.");
  const restore = () => {
    const picked = choice;
    if (!picked) return;
    setChoice(null);
    return act("restore", async () => {
      await runJob("restore", "Restoring a backup", () => bridge.RestoreBackup(picked.set_dir, picked.key_path ?? "", adminPassword));
    }, "The restore didn't start", "Nothing was changed. Try again, or open the log file for support.");
  };

  return <section className="advanced-card advanced-card-pad" aria-labelledby="advanced-backups-title">
    <div className="advanced-card-row">
      <span className="advanced-icon"><Archive aria-hidden="true" /></span>
      <div className="advanced-grow">
        <h2 className="advanced-card-title" id="advanced-backups-title">Backup recovery</h2>
        <p className="advanced-card-description">CARE backs up the clinic every day into the backup folder. Backups are encrypted:
          keep the recovery key somewhere safe, away from this computer, so the clinic can be restored even if this computer is lost.</p>
      </div>
    </div>
    <div className="advanced-setting">
      <div>
        <div className="advanced-setting-label"><KeyRound aria-hidden="true" className="size-4" />Recovery key</div>
        <p className="advanced-setting-help">Needed to restore any backup, here or on a new computer. Save it on a USB drive or print it, and keep it apart from the backups.</p>
      </div>
      <div className="advanced-setting-control">
        <Button type="button" disabled={blocked} onClick={() => void saveKey()}>
          {task === "key" ? <Spinner /> : <KeyRound aria-hidden="true" className="size-4" />}Save recovery key…
        </Button>
      </div>
    </div>
    <div className="advanced-setting">
      <div>
        <div className="advanced-setting-label"><FolderInput aria-hidden="true" className="size-4" />Extra copy</div>
        <p className="advanced-setting-help">{copy
          ? <>Also copied to <code>{copy.dir}</code>. Last copied: {when(copy.copied_at)}.</>
          : "Keep a second copy of the newest backup on a USB drive or network share. When it isn't connected, CARE copies the next time it is."}</p>
        {copy?.message ? <p className="advanced-setting-help" role="status">{copy.message}</p> : null}
      </div>
      <div className="advanced-setting-control">
        <Button type="button" disabled={blocked} onClick={() => void setCopy()}>
          {task === "copy" ? <Spinner /> : <FolderInput aria-hidden="true" className="size-4" />}{copy ? "Change folder…" : "Choose folder…"}
        </Button>
        {copy ? <Button type="button" variant="ghost" disabled={blocked} onClick={() => void clearCopy()}>
          {task === "clear" ? <Spinner /> : <FolderX aria-hidden="true" className="size-4" />}Stop extra copy
        </Button> : null}
      </div>
    </div>
    <div className="advanced-setting">
      <div>
        <div className="advanced-setting-label"><DatabaseBackup aria-hidden="true" className="size-4" />Restore</div>
        <p className="advanced-setting-help">Replaces the clinic's records and files with a backup. Choose the backup folder, a copy of it,
          or one backup inside it; the newest backup there is used.</p>
        <label className="advanced-check">
          <Checkbox checked={otherKey} disabled={blocked} onCheckedChange={(checked) => setOtherKey(checked === true)} />
          <span>The backup came from another computer<small>You'll also choose the recovery key saved there.</small></span>
        </label>
      </div>
      <div className="advanced-setting-control">
        <Button type="button" className="advanced-outline-danger" disabled={blocked} onClick={() => void choose()}>
          {task === "choose" || task === "restore" ? <Spinner /> : <DatabaseBackup aria-hidden="true" className="size-4" />}Restore from backup…
        </Button>
      </div>
    </div>
    <AdvancedError problem={problem} />
    <AlertDialog open={choice !== null} onOpenChange={(open) => { if (!open) setChoice(null); }}>
      <AlertDialogContent className="advanced-dialog">
        <span className="advanced-icon advanced-icon-danger"><DatabaseBackup aria-hidden="true" /></span>
        <AlertDialogTitle className="advanced-dialog-title">Restore the backup from {when(choice?.created_at)}?</AlertDialogTitle>
        <AlertDialogDescription>
          The clinic stops while it restores. Everything recorded after this backup is replaced by it and can't be recovered.
        </AlertDialogDescription>
        <div className="advanced-dialog-body">
          <p className="advanced-setting-help">Backup <code>{choice?.id}</code> in <code>{choice?.set_dir}</code>
            {choice?.key_path ? <>, decrypted with <code>{choice.key_path}</code></> : null}.</p>
        </div>
        <div className="advanced-dialog-foot">
          <Button type="button" onClick={() => setChoice(null)}>Cancel</Button>
          <Button type="button" variant="destructive" onClick={() => void restore()}>
            <DatabaseBackup aria-hidden="true" className="size-4" />Restore
          </Button>
        </div>
      </AlertDialogContent>
    </AlertDialog>
  </section>;
}
