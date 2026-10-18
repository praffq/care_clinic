import { ArrowRight, Download, ExternalLink, History, Play, RefreshCw, Server, ShieldAlert, Undo2 } from "lucide-react";
import { useCallback, useEffect, useRef, useState, type ReactNode } from "react";

import { Spinner } from "@/components/spinner";
import { Button } from "@/components/ui/button";
import { bridge } from "@/lib/bridge";
import { dateTime, errorText } from "@/lib/format";
import { useCare } from "@/state/care-store";
import type { ClinicUpdates, HistoryEntry, Journal, PlannedStep, ReleaseNote, UpdateBlocker } from "@/types";
import type { PanelTone } from "./panel-status";
import { AdminConfirmDialog, COMPONENT_LABELS, JournalSteps, stepLabel, useUpdateJournal } from "./panel-journal";
import { PanelBadge, PanelNotice, usePanelTask } from "./panel-ui";
import { usePanelUpdateLock } from "./panel-update-lock";

const CHECK_JOB = "update-check";
const UPDATE_JOB = "updates";
export const CHECKING = "Checking for clinic updates";
export const UPDATING = "Updating the clinic";
export const ROLLING_BACK = "Rolling back the clinic update";

/** ClinicUpdateStatus while `active`, again after every job, and on demand. */
export function useClinicUpdates(active: boolean) {
  const { busy, log } = useCare();
  const [data, setData] = useState<ClinicUpdates | null>(null);
  const [error, setError] = useState("");
  const generation = useRef(0);
  const wasBusy = useRef(busy);
  const reload = useCallback(async () => {
    const mine = ++generation.current;
    try {
      const next = await bridge.ClinicUpdateStatus();
      if (mine !== generation.current) return;
      setData(next);
      setError("");
    } catch (cause) {
      if (mine !== generation.current) return;
      log(`panel: ${errorText(cause)}`);
      setError(errorText(cause));
    }
  }, [log]);
  useEffect(() => { if (active) void reload(); }, [active, reload]);
  useEffect(() => {
    if (wasBusy.current && !busy && active) void reload();
    wasBusy.current = busy;
  }, [active, busy, reload]);
  return { data, error, reload };
}

const RESULT: Record<HistoryEntry["result"], [string, PanelTone]> = {
  applied: ["Applied", "ok"],
  rolled_back: ["Rolled back", "warning"],
  rollback_failed: ["Rollback failed", "danger"],
};

const BLOCKERS: Record<Exclude<UpdateBlocker, "up_to_date">, [string, string]> = {
  needs_desktop: ["A newer CARE Clinic app is needed first",
    "The next clinic update only runs on a newer version of this app. Update the CARE Clinic application above, then check again."],
  blocked: ["The next update is on hold",
    "The CARE team paused this release, so nothing will be installed. Check again later."],
  unknown_care: ["This CARE version isn't in the update feed",
    "CARE Clinic can't plan an update from the version installed here. Ask your support contact before changing anything."],
  no_path: ["No update can be applied yet",
    "There's no supported way to update this clinic from its current versions. Check again later, or ask your support contact."],
};

function change(step: { component: string; from?: string; to: string }) {
  const name = COMPONENT_LABELS[step.component as PlannedStep["component"]] ?? step.component;
  return step.from ? `${name} ${step.from} → ${step.to}` : `${name} ${step.to}`;
}

/** The clinic's own software: CARE and the engine, from the signed update
 *  feed. The CARE Clinic application card above updates the app itself. */
export function ClinicUpdatesCard() {
  const care = useCare();
  const updateLock = usePanelUpdateLock();
  const active = care.flow === "panel" && care.tab === "updates";
  const { data, error, reload } = useClinicUpdates(active);
  const [live, setLive] = useUpdateJournal();
  const [confirm, setConfirm] = useState<"apply" | "resume" | "rollback" | null>(null);
  const start = usePanelTask();
  const links = usePanelTask();

  const checking = care.busy && care.busyLabel === CHECKING;
  const applying = care.busy && (care.busyLabel === UPDATING || care.busyLabel === ROLLING_BACK);
  const locked = care.busy || updateLock.active || start.working;
  // The live journal while an operation runs; the stored one otherwise.
  const journal: Journal | null = applying && live !== undefined ? live : (data?.journal ?? null);
  useEffect(() => { if (!care.busy) setLive(undefined); }, [care.busy, setLive]);

  const check = () => {
    if (locked || updateLock.isActive()) return;
    void start.run(() => care.runJob(CHECK_JOB, CHECKING, () => bridge.CheckClinicUpdates()),
      "The check didn't start. Try again, or open the log file for support.");
  };
  const run = async (kind: "apply" | "resume" | "rollback", password: string) => {
    if (care.busy || updateLock.isActive()) throw new Error("something else is still running");
    const call = kind === "apply" ? bridge.ApplyClinicUpdates : kind === "resume" ? bridge.ResumeClinicUpdate : bridge.RollBackClinicUpdate;
    setLive(undefined);
    await care.runJob(UPDATE_JOB, kind === "rollback" ? ROLLING_BACK : UPDATING, () => call(password));
    setConfirm(null);
  };
  const open = (url: string) => void links.run(() => bridge.OpenURL(url),
    "Couldn't open the release notes. Try again when this computer is connected to the internet.");

  const installed = data?.installed;
  const result = data?.check;
  const interrupted = !applying && journal !== null;
  const badge: [string, PanelTone, boolean] = applying ? [care.busyLabel === ROLLING_BACK ? "Rolling back" : "Updating", "warning", true]
    : checking ? ["Checking…", "neutral", true]
      : interrupted ? ["Needs attention", "danger", false]
        : data?.check_error ? ["Couldn't check", "warning", false]
          : result?.available ? ["Update available", "ok", false]
            : result ? (data?.blocker === "up_to_date" ? ["Up to date", "ok", false] : ["Can't update yet", "warning", false])
              : ["Not checked yet", "neutral", false];

  return <section className="panel-card panel-card-pad panel-update-card" aria-label="Clinic software">
    <div className="panel-update-head">
      <span className="panel-tile"><Server aria-hidden="true" /></span>
      <div className="panel-grow">
        <h2 className="panel-title">Clinic software</h2>
        <p className="panel-card-sub">CARE and the engine that runs it.
          {data?.checked_at ? ` Checked ${dateTime(data.checked_at)}.` : data?.verified_at ? ` Last verified ${dateTime(data.verified_at)}.` : ""}</p>
      </div>
      {data?.unsigned ? <span title="This build accepts an update feed without a signature. Use it only for testing.">
        <PanelBadge tone="warning"><ShieldAlert aria-hidden="true" />Unsigned feed</PanelBadge>
      </span> : null}
      <PanelBadge tone={badge[1]} working={badge[2]}>{badge[0]}</PanelBadge>
      <Button aria-label="Check clinic updates" disabled={locked || !data} onClick={check}>
        {checking ? <Spinner /> : <RefreshCw aria-hidden="true" />}Check now
      </Button>
    </div>
    <dl className="panel-versions" aria-label="Installed versions">
      <Version label="CARE" value={installed?.care?.version} hint={installed?.care?.released_at ? `Released ${dateTime(installed.care.released_at)}` : undefined} />
      <Version label="Engine" value={installed?.engine?.version} hint={installed?.engine ? `Data format ${installed.engine.data_schema}` : undefined} />
      <Version label="CARE Clinic app" value={installed?.desktop || care.version} />
    </dl>
    {data?.unsigned ? <p className="panel-small panel-unsigned">This build accepts an unsigned update feed, so updates
      aren't checked against the CARE team's signature. Use it only for testing.</p> : null}
    {error && !data ? <PanelNotice tone="warning" title="Couldn't read the clinic's update status"
      actions={<Button onClick={() => void reload()}>Try again</Button>}>
      Check that the clinic is running on Overview, then try again.
    </PanelNotice> : null}
    {applying ? <div className="panel-update-progress" role="status" aria-live="polite">
      <h3>{care.busyLabel === ROLLING_BACK ? `Rolling back${journal ? ` ${change(journal)}` : "…"}`
        : journal ? `Updating ${change(journal)}` : "Starting the update…"}</h3>
      <p className="panel-small">CARE restarts during the update and staff are signed out briefly. Keep this computer on.</p>
      {journal ? <JournalSteps journal={journal} /> : null}
    </div> : interrupted && journal ? <InterruptedNotice journal={journal} disabled={locked}
      onResume={() => setConfirm("resume")} onRollback={() => setConfirm("rollback")} /> : null}
    {!applying && data?.check_error ? <PanelNotice tone="warning" title="Couldn't check for clinic updates"
      actions={<Button disabled={locked} onClick={check}>Try again</Button>}>
      {data.check_error}
    </PanelNotice> : null}
    {!applying && !interrupted && result?.available ? <PanelNotice tone="ok" title={result.urgent ? "An important clinic update is available" : "A clinic update is available"}
      actions={<Button variant="primary" disabled={locked} onClick={() => setConfirm("apply")}>
        <Download aria-hidden="true" />Apply update
      </Button>}>
      <Plan steps={result.steps ?? []} notes={data?.notes ?? []} onOpen={open} linksDisabled={links.working} />
      {result.held?.length ? <p className="panel-small">Held back for now: {result.held.join(", ")}.</p> : null}
      <p className="panel-small">{result.prefetched ? "Downloaded and ready to install."
        : result.prefetch_error ? `Not downloaded yet (${result.prefetch_error}); applying downloads it first.`
          : "Applying downloads it first."}</p>
    </PanelNotice> : null}
    {!applying && result && !result.available && data?.blocker && data.blocker !== "up_to_date" ? <PanelNotice tone="warning"
      title={BLOCKERS[data.blocker][0]}>
      {BLOCKERS[data.blocker][1]}
      {result.reason ? <p className="panel-small panel-reason">{result.reason}</p> : null}
    </PanelNotice> : null}
    {!applying && result && !result.available && data?.blocker === "up_to_date" ? <p className="panel-small">CARE and the engine are up to date.</p> : null}
    {links.error || start.error ? <p className="panel-inline-error" role="alert">{links.error || start.error}</p> : null}
    {data ? <UpdateHistory history={data.history} /> : null}
    {confirm ? <AdminConfirmDialog
      title={confirm === "apply" ? "Apply the clinic update?" : confirm === "resume" ? "Resume the interrupted update?" : "Roll back the interrupted update?"}
      icon={confirm === "rollback" ? <Undo2 aria-hidden="true" /> : <Download aria-hidden="true" />}
      destructive={confirm === "rollback"}
      confirmLabel={confirm === "apply" ? "Apply update" : confirm === "resume" ? "Resume update" : "Roll back"}
      description={confirm === "apply" ? <>
        <p>CARE stops for a few minutes and staff are signed out. The clinic is backed up first, and the previous
          version is put back automatically if anything fails.</p>
        {result?.steps?.length ? <ul className="panel-plan-list">{result.steps.map((step) => <li key={`${step.component}-${step.to}`}>{change(step)}</li>)}</ul> : null}
      </> : confirm === "resume" ? <p>CARE Clinic carries on from the step that was interrupted. CARE may restart.</p>
        : <p>CARE Clinic puts the previous version back, restoring the database from the backup taken before the update
          if it had changed. Records added since then are lost.</p>}
      onConfirm={(password) => run(confirm, password)} onClose={() => setConfirm(null)} /> : null}
  </section>;
}

function Version({ label, value, hint }: { label: string; value?: string; hint?: string }) {
  return <div className="panel-version">
    <dt className="panel-eyebrow">{label}</dt>
    <dd><span className="panel-version-value">{value || "Unknown"}</span>{hint ? <span className="panel-small">{hint}</span> : null}</dd>
  </div>;
}

function Plan({ steps, notes, onOpen, linksDisabled }: {
  steps: PlannedStep[];
  notes: ReleaseNote[];
  onOpen: (url: string) => void;
  linksDisabled: boolean;
}) {
  return <ul className="panel-plan" aria-label="Planned update">
    {steps.map((step) => {
      const note = notes.find((candidate) => candidate.component === step.component && candidate.version === step.to);
      return <li key={`${step.component}-${step.to}`}>
        <div className="panel-plan-head">
          <strong>{COMPONENT_LABELS[step.component] ?? step.component}</strong>
          <span className="panel-mono">{step.from ?? "—"}</span><ArrowRight aria-hidden="true" /><span className="panel-mono">{step.to}</span>
          {note?.migrations ? <PanelBadge tone="warning">Database changes</PanelBadge> : null}
        </div>
        {step.component === "desktop" ? <p className="panel-small">Install this with the CARE Clinic application card above first.</p> : null}
        {note?.released_at ? <p className="panel-small">Released {dateTime(note.released_at)}</p> : null}
        {note?.links?.length ? <div className="panel-actions panel-plan-links">
          {note.links.map((link) => <Button key={link.url} variant="ghost" size="sm" disabled={linksDisabled}
            aria-label={`${link.label} for ${change(step)}`} onClick={() => onOpen(link.url)}>
            <ExternalLink aria-hidden="true" />{link.label}
          </Button>)}
        </div> : null}
      </li>;
    })}
  </ul>;
}

function InterruptedNotice({ journal, disabled, onResume, onRollback }: {
  journal: Journal;
  disabled: boolean;
  onResume: () => void;
  onRollback: () => void;
}) {
  const failed = journal.state === "failed";
  const rollingBack = journal.state === "rolling_back" || journal.state === "rollback_uncertain";
  const title = failed ? "Putting the previous version back failed"
    : rollingBack ? "Putting the previous version back was interrupted" : "An update was interrupted";
  const body: ReactNode = failed
    ? <>CARE Clinic couldn't undo the update of {change(journal)}. Try the rollback again; if it keeps failing, contact support with the log file.</>
    : rollingBack ? <>CARE Clinic stopped while undoing the update of {change(journal)}. Resume to finish putting the previous version back.</>
      : <>CARE Clinic stopped while updating {change(journal)}. Resume to finish it, or roll back to {journal.from ?? "the previous version"}.</>;
  return <PanelNotice tone="danger" title={title} actions={<>
    {!failed ? <Button variant="primary" disabled={disabled} onClick={onResume}><Play aria-hidden="true" />Resume</Button> : null}
    <Button variant={failed ? "primary" : "default"} disabled={disabled} onClick={onRollback}>
      <Undo2 aria-hidden="true" />{failed ? "Try the rollback again" : "Roll back"}
    </Button>
  </>}>
    {body}
    {journal.error ? <p className="panel-small panel-reason">{journal.failed_step ? `${stepLabel(journal.failed_step)}: ` : ""}{journal.error}</p> : null}
    <JournalSteps journal={journal} label="Interrupted update steps" />
  </PanelNotice>;
}

function UpdateHistory({ history }: { history: HistoryEntry[] }) {
  return <div className="panel-history">
    <h3 className="panel-eyebrow panel-history-title"><History aria-hidden="true" />Recent changes</h3>
    {history.length === 0 ? <p className="panel-small">No clinic updates or plugin changes yet.</p>
      : <ul aria-label="Update history">
        {history.map((entry) => {
          const [label, tone] = RESULT[entry.result] ?? [entry.result, "neutral"];
          return <li key={entry.id} className="panel-history-row">
            <div className="panel-grow">
              <div>{change(entry)}</div>
              <p className="panel-small">{dateTime(entry.finished_at || entry.started_at)}</p>
              {entry.error ? <p className="panel-small panel-reason">
                {entry.failed_step ? `${stepLabel(entry.failed_step)}: ` : ""}{entry.error}
              </p> : null}
            </div>
            <PanelBadge tone={tone}>{label}</PanelBadge>
          </li>;
        })}
      </ul>}
  </div>;
}
