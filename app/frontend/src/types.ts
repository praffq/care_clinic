// The shapes the Go bridge hands back. Mirrors app/internal/care — keep in step
// with wailsjs/go/models.ts, which Wails regenerates from the Go structs.

export type NameStatus = { ok: boolean; message: string };
export type NetworkStatus = {
  applicable: boolean;
  ok: boolean;
  message: string;
  how: string;
  fixable: boolean;
};
export type HomeAccess = {
  installed: boolean;
  running: boolean;
  https: boolean;
  host: string;
  url: string;
  ip: string;
  enabled: boolean;
  conflict: boolean;
  unknown: boolean;
  detail: string;
  clinic_ready: boolean;
};

export type AppState = {
  role: "" | "server" | "client";
  client_url: string;
  version: string;
  platform: string;
  setup_done: boolean;
  setup_started: boolean;
  mdns_name: string;
};

export type ClientConnectPhase = "finding" | "connecting" | "checking" | "opening";

export type SetupPage = "computer" | "address" | "admin" | "review" | "install";
export type SetupFailure = { can_retry: boolean };

/** One row of HostChecks: what this computer needs before setup. */
export type HostCheck = {
  id: "virtualization" | "memory" | "disk" | "service";
  title: string;
  level: "ok" | "warn" | "fail";
  detail: string;
  action?: "install-service";
};
export type HostReport = { checks: HostCheck[]; ready: boolean };

export type SummaryLevel = "ok" | "busy" | "warn" | "error";
export type Summary = {
  level: SummaryLevel;
  title: string;
  detail?: string;
  running: boolean;
  notes?: string[];
};

export type EngineState =
  | "no_engine" | "stopped" | "starting" | "ready" | "not_ready" | "stopping" | "crashed" | "backoff";
export type StackState = "not_installed" | "starting" | "running" | "failed" | "stopping" | "stopped";

export type EngineStatus = {
  state: EngineState;
  engine_version?: string;
  started_at?: string;
  uptime_seconds?: number;
  restarts: number;
  last_error?: string;
  last_error_at?: string;
  next_retry_at?: string;
  stack?: { state: StackState; error?: string; since: string };
};

export type ForwardListener = {
  name: string;
  listen: string;
  vsock_port: number;
  listening: boolean;
  error?: string;
};

export type MDNSStatus = {
  enabled: boolean;
  name: string;
  advertising: boolean;
  since?: string;
  conflict?: { host: string; address: string };
  last_error?: string;
};

export type ServiceStatus = {
  engine: EngineStatus;
  slots: { current: string; previous?: string };
  forward?: { proxy_protocol: boolean; listeners: ForwardListener[] };
  mdns?: MDNSStatus;
};

/** What ServiceStatus and the service-status event report. */
export type ServiceState = {
  installed: boolean;
  reachable: boolean;
  error?: string;
  status?: ServiceStatus;
  summary: Summary;
};

/**
 * What FindClinic found. Reading this changes nothing on the computer;
 * `already_trusted` means connecting needs no certificate installation.
 */
export type ClinicInfo = {
  url: string;
  host: string;
  fingerprint: string;
  already_trusted: boolean;
};

/**
 * Whether the clinic is answering. A clinic that is switched off is
 * `reachable: false` with a `detail`, not an error.
 */
export type ClientReachability = {
  reachable: boolean;
  checked_at: number;
  detail: string;
};

/** What connecting would clean up. Read-only; no administrator approval. */
export type ClientPreflight = {
  hosts_entry: boolean;
  old_certificate: boolean;
  unfinished_server_setup: boolean;
};

/** Which of the two .env files an editor is pointed at. */
export type Section = "backend" | "frontend";

export type UpdateChecks = { app: boolean };

export type QuitRequest = { id: number; title: string; message: string };
export type ConfirmationRequest = { id: number; title: string; message: string };

export type AppUpdatePhase = "downloading" | "verifying" | "installing" | "restarting" | "installer";

export type AppUpdateProgress = { phase: AppUpdatePhase; done: number; total: number };

export type AppUpdate = {
  current: string;
  version: string;
  available: boolean;
  notes_url: string;
  asset: string;
  size: number;
};
