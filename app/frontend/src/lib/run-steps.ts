// Setup steps in the order the host runs them. The ids match the "step" the
// host sends in setup-progress events (app_onboarding.go, internal/clinic).
export const SETUP_STEPS = [
  { id: "service", label: "Connecting to the CARE Clinic service" },
  { id: "engine", label: "Installing the clinic engine" },
  { id: "engine-ready", label: "Starting the clinic engine" },
  { id: "address", label: "Publishing the clinic address" },
  { id: "images", label: "Downloading CARE" },
  { id: "configure", label: "Saving the clinic settings" },
  { id: "database", label: "Preparing the database" },
  { id: "start", label: "Starting CARE" },
  { id: "admin", label: "Creating the admin login" },
  { id: "computer", label: "Setting up this computer" },
] as const;

export type SetupStepId = (typeof SETUP_STEPS)[number]["id"];

/** One setup-progress event from the host. */
export type SetupProgress = {
  step: string;
  state: "active" | "done" | "skipped";
  detail?: string;
  done?: number;
  total?: number;
};

export type StepStatus = "pending" | "active" | "done" | "skipped" | "failed";

export type StepProgress = {
  status: StepStatus;
  detail: string;
  done: number;
  total: number;
};

export type SetupSteps = Partial<Record<SetupStepId, StepProgress>>;

const KNOWN = new Set<string>(SETUP_STEPS.map((s) => s.id));

/** Applies one event. Unknown steps are ignored; a started step marks any
 * earlier step that is still active as done, since the host runs them in order. */
export function applyProgress(steps: SetupSteps, ev: SetupProgress): SetupSteps {
  if (!KNOWN.has(ev.step)) return steps;
  const id = ev.step as SetupStepId;
  const prev = steps[id];
  const next: SetupSteps = { ...steps };
  if (ev.state === "active") {
    const index = SETUP_STEPS.findIndex((s) => s.id === id);
    for (const earlier of SETUP_STEPS.slice(0, index)) {
      if (next[earlier.id]?.status === "active") next[earlier.id] = { ...next[earlier.id]!, status: "done" };
    }
  }
  const sameStep = prev?.status === ev.state;
  next[id] = {
    status: ev.state,
    detail: ev.detail || (sameStep ? prev.detail : ""),
    done: ev.done ?? 0,
    total: ev.total ?? 0,
  };
  return next;
}

/** Marks the running step as failed. */
export function failActiveStep(steps: SetupSteps): SetupSteps {
  const next: SetupSteps = { ...steps };
  for (const s of SETUP_STEPS) {
    if (next[s.id]?.status === "active") next[s.id] = { ...next[s.id]!, status: "failed" };
  }
  return next;
}

/** Keeps finished steps for a retry; the host repeats only what's left. */
export function stepsForRetry(steps: SetupSteps): SetupSteps {
  const next: SetupSteps = {};
  for (const s of SETUP_STEPS) {
    const step = steps[s.id];
    if (step && (step.status === "done" || step.status === "skipped")) next[s.id] = step;
  }
  return next;
}

/** The step to headline: the running one, else the first not yet finished. */
export function currentStep(steps: SetupSteps): number {
  const active = SETUP_STEPS.findIndex((s) => steps[s.id]?.status === "active" || steps[s.id]?.status === "failed");
  if (active >= 0) return active;
  const pending = SETUP_STEPS.findIndex((s) => !steps[s.id]);
  return pending >= 0 ? pending : SETUP_STEPS.length - 1;
}
