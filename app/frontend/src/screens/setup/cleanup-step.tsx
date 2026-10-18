import { RefreshCw, Trash2 } from "lucide-react";

import { Callout, LogButton, StatusBadge } from "@/components/onboarding";
import { Spinner } from "@/components/spinner";
import { Button } from "@/components/ui/button";
import type { ResidueReport } from "@/types";

export type CleanupProblem = { kind: "check" | "remove"; message: string };

export function CleanupStep({ report, before, checking, removing, problem, busy, onRemove, onCheck }: {
  report: ResidueReport | null;
  before: ResidueReport | null;
  checking: boolean;
  removing: boolean;
  problem: CleanupProblem | null;
  busy: boolean;
  onRemove: () => void;
  onCheck: () => void;
}) {
  const ready = report?.clean === true && !problem;
  const current = report?.traces ?? [];

  if (checking && !report && !problem) {
    return <div className="on-card on-pad" role="status"><div className="on-card-head"><span className="on-tile on-large"><Spinner /></span><div className="on-grow"><h2>Checking for an earlier setup</h2><p>This takes a moment.</p></div><StatusBadge>Checking</StatusBadge></div></div>;
  }

  return (
    <>
      {report ? <div className="on-card on-pad">
        <div className="on-card-head"><span className="on-tile on-large"><Trash2 aria-hidden="true" /></span><div className="on-grow"><h2>{ready ? "Nothing from the earlier setup is left" : removing ? "Removing the earlier setup" : "Still on this computer"}</h2></div>{removing ? <Spinner /> : null}</div>
        <ul className="on-leftovers">{(before?.traces ?? current).map((trace) => {
          const removed = before && problem?.kind !== "check" && !current.some((item) => item.id === trace.id);
          return <li key={trace.id} title={trace.detail}>{trace.label}{removed ? <span className="on-success"> — Removed</span> : before && !removing ? " — Still here" : ""}</li>;
        })}</ul>
        {!ready ? <><p>This deletes the earlier clinic's database and files from this computer and can't be undone. Backups that clinic made are kept.</p>
          <p className="on-small" style={{ marginTop: 8 }}>The CARE Clinic service installed in the previous step stays. Your computer may ask for a password, fingerprint or PIN.</p>
          <div className="on-actions"><Button variant="destructive" disabled={busy} onClick={onRemove}>{removing ? <Spinner /> : <Trash2 aria-hidden="true" />}{removing ? "Removing…" : before ? "Try again" : "Remove it all"}</Button>{!removing ? <Button disabled={busy} onClick={onCheck}><RefreshCw aria-hidden="true" />Check again</Button> : null}</div>
          {removing ? <p className="on-small" role="status" style={{ marginTop: 8 }}>This usually takes 1 to 3 minutes.</p> : null}
        </> : <p className="on-success">The check found no leftovers. Your backups have been kept.</p>}
      </div> : null}
      {problem ? <Callout tone="danger" title="This step couldn't finish">{problem.message}<div className="on-actions"><Button disabled={busy} onClick={problem.kind === "remove" ? onRemove : onCheck}>Try again</Button><LogButton /></div></Callout> : null}
    </>
  );
}
