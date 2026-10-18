import { Archive, HardDrive, RefreshCw } from "lucide-react";

import { Spinner } from "@/components/spinner";
import { Button } from "@/components/ui/button";
import { toast } from "@/components/ui/sonner";
import { bridge } from "@/lib/bridge";
import { when } from "@/lib/format";
import { useCare } from "@/state/care-store";
import type { BackupStatus } from "@/types";
import type { PanelTone } from "./panel-status";
import { PanelBadge, usePanelTask } from "./panel-ui";

function exportState(b: BackupStatus): [string, PanelTone] {
  if (b.critical) return ["Not copied out", "danger"];
  if (b.message) return ["Waiting", "warning"];
  if (!b.last_exported) return ["No backup yet", "neutral"];
  return ["Copied out", "ok"];
}

/** Where the clinic's backups are, and whether they got there (Overview). */
export function BackupStatusCard() {
  const { service, busy } = useCare();
  const copyNow = usePanelTask();
  const backups = service?.status?.backups;
  if (!backups) return null;
  const [label, tone] = exportState(backups);
  const extra = backups.copy;
  const copy = async () => {
    if (await copyNow.run(() => bridge.CopyBackupsNow(),
      "The extra copy didn't finish. Check the drive or share is connected, then try again.")) toast("Backup copied");
  };
  return <section className="panel-card panel-card-pad panel-storage-health" aria-label="Backups">
    <h2 className="panel-eyebrow">Backups</h2>
    <div className="panel-disk" aria-label="Backup folder status">
      <div className="panel-row panel-between">
        <span className="panel-disk-name"><Archive aria-hidden="true" />Backup folder</span>
        <PanelBadge tone={tone}>{label}</PanelBadge>
      </div>
      {backups.dir ? <div className="panel-disk-path" title={backups.dir}>{backups.dir}</div> : null}
      <p className="panel-small">Last copied out: {when(backups.verified_at)}</p>
      {backups.message ? <p className={`panel-small panel-state-${tone}`} role={backups.critical ? "alert" : "status"}>
        {backups.message}</p> : null}
    </div>
    {extra ? <div className="panel-disk" aria-label="Extra copy status">
      <div className="panel-row panel-between">
        <span className="panel-disk-name"><HardDrive aria-hidden="true" />Extra copy</span>
        <PanelBadge tone={extra.message ? "warning" : extra.last_copied ? "ok" : "neutral"}>
          {!extra.connected && extra.message ? "Not connected" : extra.message ? "Not copied" : extra.last_copied ? "Copied" : "Not copied yet"}
        </PanelBadge>
      </div>
      <div className="panel-disk-path" title={extra.dir}>{extra.dir}</div>
      <p className="panel-small">Last copied: {when(extra.copied_at)}</p>
      {extra.message ? <p className="panel-small panel-state-warning" role="status">{extra.message}</p> : null}
      <div className="panel-actions">
        <Button variant="ghost" size="sm" disabled={busy || copyNow.working || !backups.last_exported} onClick={() => void copy()}>
          {copyNow.working ? <Spinner /> : <RefreshCw aria-hidden="true" />}Copy now
        </Button>
      </div>
      {copyNow.error ? <p className="panel-inline-error" role="alert">{copyNow.error}</p> : null}
    </div> : <p className="panel-small">Add an extra copy on a USB drive or network share in Advanced.</p>}
  </section>;
}
