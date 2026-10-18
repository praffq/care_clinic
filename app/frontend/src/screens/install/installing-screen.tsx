import { Check, Coffee, Minus, X } from "lucide-react";
import { useEffect, useRef } from "react";

import { Callout, LogButton } from "@/components/onboarding";
import { Spinner } from "@/components/spinner";
import { Progress } from "@/components/ui/progress";
import { useElapsed } from "@/hooks/use-elapsed";
import { onCareEvent } from "@/lib/bridge";
import { megabytes, mmss } from "@/lib/format";
import { currentStep, SETUP_STEPS, type StepStatus } from "@/lib/run-steps";
import { cn } from "@/lib/utils";
import { useCare } from "@/state/care-store";

import { InstallLayout } from "./install-layout";

const QUIET_WARNING_MS = 15 * 60 * 1000;

const STATUS_TEXT: Record<StepStatus, string> = {
  pending: "Not started",
  active: "In progress",
  done: "Done",
  skipped: "Not needed",
  failed: "Failed",
};

/** Byte progress for a download or upload; a moving bar while the size is unknown. */
function StepBar({ done, total, label }: { done: number; total: number; label: string }) {
  if (total <= 0) {
    return <Progress className="install-progress install-progress-busy" aria-label={label} />;
  }
  const pct = Math.min(100, Math.floor((done / total) * 100));
  return (
    <>
      <Progress className="install-progress" value={pct} aria-label={label} aria-valuetext={`${pct}%`} />
      <span className="install-stage-bytes">{megabytes(done) || "0.0 MB"} of {megabytes(total)} · {pct}%</span>
    </>
  );
}

export function InstallingScreen() {
  const { run } = useCare();
  const elapsed = useElapsed(run.startedAt, true);
  const elapsedTime = mmss(elapsed).padStart(5, "0");
  const lastLogAt = useRef(run.startedAt || Date.now());

  useEffect(() => {
    lastLogAt.current = run.startedAt || Date.now();
    // Only actual host log events reset silence, not UI logs or phase weights.
    return onCareEvent("care-log", (line: unknown) => {
      if (typeof line === "string") lastLogAt.current = Date.now();
    });
  }, [run.startedAt]);

  const quiet = Date.now() - lastLogAt.current >= QUIET_WARNING_MS;
  const index = currentStep(run.steps);
  const current = SETUP_STEPS[index];
  const stage = run.finished ? "Finishing installation" : current.label;
  const activeRow = useRef<HTMLLIElement>(null);

  useEffect(() => {
    activeRow.current?.scrollIntoView?.({ block: "nearest" });
  }, [index]);

  return (
    <InstallLayout
      title="Installing CARE"
      subtitle="This takes 5 to 20 minutes. You can leave this window open and do something else."
      headerAside={<time className="install-elapsed" role="timer" aria-live="off" dateTime={`PT${elapsed}S`} aria-label={`Elapsed installation time: ${elapsedTime}`}>{elapsedTime}</time>}
      footer={<p className="on-foot-note">Installing — please don't close this window.</p>}
    >
      <section className="on-card install-progress-card" aria-label="Installation progress">
        <div role="status" aria-live="polite" aria-atomic="true">
          <div className="on-eyebrow">
            {quiet ? "Last reported step" : `Step ${Math.min(index + 1, SETUP_STEPS.length)} of ${SETUP_STEPS.length}`}
          </div>
          <h2 className="install-current-label">{stage}</h2>
        </div>
        <ol className="install-stages" aria-label="Installation steps">
          {SETUP_STEPS.map((step) => {
            const p = run.steps[step.id];
            const status = p?.status ?? "pending";
            return (
              <li key={step.id} ref={step.id === current.id ? activeRow : undefined} className={cn("install-stage", `install-stage-${status}`)}
                aria-current={status === "active" ? "step" : undefined}>
                <span className="install-stage-mark" aria-hidden="true">
                  {status === "done" ? <Check /> : status === "skipped" ? <Minus />
                    : status === "failed" ? <X /> : status === "active" ? <Spinner /> : null}
                </span>
                <div className="install-stage-body">
                  <span className="install-stage-name">{step.label}</span>
                  <span className="sr-only">: {STATUS_TEXT[status]}</span>
                  {p?.detail ? <span className="install-stage-detail">{p.detail}</span> : null}
                  {status === "active" ? <StepBar done={p?.done ?? 0} total={p?.total ?? 0} label={step.label} /> : null}
                </div>
              </li>
            );
          })}
        </ol>
      </section>
      {quiet ? (
        <div role="status">
          <Callout tone="warn" title="This is taking longer than usual">
            No new installation messages have arrived for at least 15 minutes. Open the log file and share it with your support contact before closing CARE Clinic.
          </Callout>
        </div>
      ) : (
        <div className="install-reassurance">
          <Coffee aria-hidden="true" />
          <div><strong>A good time for a break</strong>Keep the computer on, awake and connected to the internet. The control panel will open when your clinic is ready.</div>
        </div>
      )}
      <div className="install-support">
        <LogButton />
        <p className="on-small">For technical support only — you don't need to read it.</p>
      </div>
    </InstallLayout>
  );
}
