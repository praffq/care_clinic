import { Check, LockKeyhole, X } from "lucide-react";
import { useEffect, useRef, useState, type ReactNode } from "react";

import { Spinner } from "@/components/spinner";
import {
  AlertDialog, AlertDialogContent, AlertDialogDescription, AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Button } from "@/components/ui/button";
import { onCareEvent } from "@/lib/bridge";
import { errorText } from "@/lib/format";
import type { Journal, StepRecord, UpdateComponent } from "@/types";
import { AdvancedSecretInput, advancedProblem, type AdvancedProblem } from "./advanced-ui";

const STEP_LABELS: Record<string, string> = {
  preflight: "Checking everything is ready",
  backup: "Backing up the clinic",
  images: "Loading the new CARE",
  build: "Building CARE with the plugins",
  "build-frontend": "Building the CARE website",
  "stop-app": "Stopping CARE",
  "write-env": "Saving the new settings",
  migrate: "Updating the database",
  "start-stack": "Starting CARE",
  health: "Checking CARE answers",
  "stage-slot": "Installing the new engine",
  "stop-stack": "Stopping CARE",
  activate: "Switching to the new engine",
  boot: "Starting the new engine",
  "rollback-stop-app": "Stopping CARE",
  "rollback-restore-db": "Restoring the database",
  "rollback-env": "Putting the previous version back",
  "rollback-start-stack": "Starting the previous version",
  "rollback-health": "Checking CARE answers",
  "rollback-reactivate": "Switching back to the previous engine",
  "rollback-boot": "Starting the previous engine",
};

export const COMPONENT_LABELS: Record<UpdateComponent, string> = {
  care: "CARE", engine: "Engine", desktop: "CARE Clinic app", plugins: "Plugins", frontend: "CARE website settings",
};

export function stepLabel(name: string) {
  return STEP_LABELS[name] ?? name;
}

/** The unfinished operation the host last reported on "update-journal";
 *  undefined until the first event. */
export function useUpdateJournal() {
  const [journal, setJournal] = useState<Journal | null | undefined>(undefined);
  useEffect(() => onCareEvent("update-journal", (next: Journal | null) => setJournal(next ?? null)), []);
  return [journal, setJournal] as const;
}

function StepIcon({ step }: { step: StepRecord }) {
  if (step.status === "running") return <span className="panel-step-dot panel-tone-warning"><Spinner /></span>;
  if (step.status === "done") return <span className="panel-step-dot panel-tone-ok"><Check aria-hidden="true" /></span>;
  if (step.status === "failed") return <span className="panel-step-dot panel-tone-danger"><X aria-hidden="true" /></span>;
  return <span className="panel-step-dot" />;
}

function StepList({ steps, label }: { steps: StepRecord[]; label: string }) {
  return <ol className="panel-steps" aria-label={label}>
    {steps.map((step, index) => <li key={`${step.name}-${index}`} className={`panel-step panel-step-${step.status}`}
      aria-label={`${stepLabel(step.name)}: ${step.status}`}>
      <StepIcon step={step} />
      <span className="panel-grow">{stepLabel(step.name)}
        {step.attempts > 1 ? <span className="panel-small"> · try {step.attempts}</span> : null}
        {step.error ? <span className="panel-small panel-step-error">{step.error}</span> : null}
      </span>
    </li>)}
  </ol>;
}

/** The journal's steps, with its rollback steps once a rollback started. */
export function JournalSteps({ journal, label = "Update steps" }: { journal: Journal; label?: string }) {
  return <>
    <StepList steps={journal.steps} label={label} />
    {journal.rollback_steps?.length ? <>
      <p className="panel-eyebrow panel-steps-heading">Putting the previous version back</p>
      <StepList steps={journal.rollback_steps} label="Rollback steps" />
    </> : null}
  </>;
}

/** Asks for the CARE Clinic admin password before a risky change. onConfirm
 *  throws when the change didn't start; the dialog then shows why. */
export function AdminConfirmDialog({
  title, icon, description, children, confirmLabel, destructive = false, onConfirm, onClose,
}: {
  title: string;
  icon: ReactNode;
  description: ReactNode;
  children?: ReactNode;
  confirmLabel: string;
  destructive?: boolean;
  onConfirm: (password: string) => Promise<void>;
  onClose: () => void;
}) {
  const [password, setPassword] = useState("");
  const [working, setWorking] = useState(false);
  const [problem, setProblem] = useState<AdvancedProblem | null>(null);
  const live = useRef(true);
  useEffect(() => {
    live.current = true;
    return () => { live.current = false; };
  }, []);
  const submit = async () => {
    if (working || !password) return;
    setWorking(true);
    setProblem(null);
    try {
      await onConfirm(password);
    } catch (cause) {
      if (!live.current) return;
      const detail = errorText(cause);
      setProblem(/admin password does not match/i.test(detail)
        ? { title: "That password didn't work", message: "Enter the CARE Clinic admin password you chose during setup." }
        : advancedProblem(cause, "That didn't start", detail));
      setWorking(false);
    }
  };
  return <AlertDialog open onOpenChange={(open) => { if (!open && !working) onClose(); }}>
    <AlertDialogContent className="panel-dialog">
      <span className={`panel-tile ${destructive ? "panel-tile-danger" : ""}`}>{icon}</span>
      <AlertDialogTitle>{title}</AlertDialogTitle>
      <AlertDialogDescription asChild><div className="panel-dialog-description">{description}</div></AlertDialogDescription>
      {children}
      <form className="panel-dialog-form" noValidate onSubmit={(event) => { event.preventDefault(); void submit(); }}>
        <AdvancedSecretInput label="CARE Clinic admin password" value={password} disabled={working} autoFocus
          icon={<LockKeyhole aria-hidden="true" />} invalid={problem !== null}
          onChange={(value) => { setPassword(value); setProblem(null); }} />
        {problem ? <p className="panel-inline-error" role="alert"><strong>{problem.title}. </strong>{problem.message}</p> : null}
        <div className="panel-dialog-foot panel-actions">
          <Button type="button" disabled={working} onClick={onClose}>Cancel</Button>
          <Button type="submit" variant={destructive ? "destructive" : "primary"} disabled={working || !password}>
            {working ? <Spinner /> : null}{confirmLabel}
          </Button>
        </div>
      </form>
    </AlertDialogContent>
  </AlertDialog>;
}
