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
export type SetupRecoveryStatus = { codes_saved: boolean; codes_path: string; codes_problem: "" | "missing" | "unreadable" | "mismatch" };

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

/** The extra backup copy to a USB drive or network share. */
export type BackupCopyStatus = {
  dir: string;
  last_copied?: string;
  copied_at?: string;
  connected: boolean;
  last_error?: string;
  last_error_at?: string;
  message?: string;
};

/** The service's backup export: sets copied out of the engine into dir. */
export type BackupStatus = {
  dir: string;
  last_exported?: string;
  verified_at?: string;
  last_error?: string;
  last_error_at?: string;
  checked_at?: string;
  /** Complete sets still in the engine; at 2 the status is critical. */
  pending: number;
  oldest_pending_at?: string;
  critical?: boolean;
  message?: string;
  copy?: BackupCopyStatus;
};

/** A backup picked to restore (ChooseBackupToRestore). */
export type RestoreChoice = {
  set_dir: string;
  id: string;
  created_at: string;
  /** The chosen recovery key file; absent means this computer's. */
  key_path?: string;
};

export type ServiceStatus = {
  engine: EngineStatus;
  slots: { current: string; previous?: string };
  forward?: { proxy_protocol: boolean; listeners: ForwardListener[] };
  mdns?: MDNSStatus;
  backups?: BackupStatus;
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

export type AppUpdatePhase = "downloading" | "verifying" | "ready" | "installing" | "restarting" | "installer";

export type AppUpdateProgress = { phase: AppUpdatePhase; done: number; total: number };

export type AppUpdate = {
  current: string;
  version: string;
  available: boolean;
  notes_url: string;
  asset: string;
  size: number;
  /** Not signed (ad-hoc on macOS), or from an update feed accepted as unsigned. */
  unsigned?: boolean;
  source?: "feed" | "release";
  /** Downloaded and verified; RestartToUpdate installs it. */
  ready?: boolean;
};

/** How the last self-update ended, reported by the relaunched app. */
export type AppUpdateOutcome = {
  state: "" | "finishing" | "done" | "failed";
  version: string;
  error?: string;
  can_retry?: boolean;
};

export type StorageLevel = "ok" | "low" | "critical" | "unknown";

/** One disk the clinic depends on (StorageStatus). */
export type DiskUsage = {
  kind: "engine" | "backups";
  name: string;
  path?: string;
  free: number;
  total: number;
  level: StorageLevel;
  detail?: string;
};

export type StorageState = { level: StorageLevel; title: string; disks: DiskUsage[] };

/** CheckCareHealth: CARE's ping and its containers. */
export type CareHealth = {
  healthy: boolean;
  code: number;
  detail: string;
  services: { name: string; state: string }[] | null;
  servicesError?: string;
  checkedAt: string;
};

/** A CARE backend plugin (internal/plugins.Plugin). Secret configs read back as REDACTED. */
export type Plugin = {
  id: string;
  package: string;
  version?: string;
  configs?: Record<string, unknown>;
  bundled?: boolean;
};

/** A catalog frontend plugin (internal/plugins.FrontendPlugin), served at /plugins/<slug>/. */
export type FrontendPlugin = {
  slug: string;
  label: string;
  description: string;
  required?: boolean;
  config?: Record<string, unknown>;
};

/** A catalog preset (internal/plugins.Preset). */
export type PluginPreset = Plugin & {
  label: string;
  description: string;
  default?: boolean;
  required_configs?: string[];
};

/** An image the clinic runs (internal/updates.Image). */
export type UpdateImage = { role: string; repo: string; digest: string };

/** What the update coordinator recorded as installed (internal/updates.Installed). */
export type InstalledVersions = {
  desktop?: string;
  engine?: { version: string; data_schema: number };
  care?: { version: string; released_at: string; images: UpdateImage[] };
  updated_at?: string;
};

export type UpdateComponent = "care" | "engine" | "desktop" | "plugins" | "frontend";

export type PlannedStep = { component: UpdateComponent; from?: string; to: string };

/** internal/updates.CheckResult. */
export type CheckResult = {
  revision: number;
  unsigned?: boolean;
  available: boolean;
  reason?: string;
  urgent?: boolean;
  steps?: PlannedStep[];
  held?: string[];
  prefetched: boolean;
  prefetch_error?: string;
};

export type ReleaseNote = {
  component: UpdateComponent;
  version: string;
  released_at?: string;
  migrations?: boolean;
  data_schema?: number;
  links?: { label: string; url: string }[];
};

export type JournalState = "running" | "uncertain" | "rolling_back" | "rollback_uncertain" | "failed";
export type StepRecord = {
  name: string;
  status: "pending" | "running" | "done" | "failed";
  attempts: number;
  error?: string;
  at?: string;
};

/** An unfinished update, plugin or frontend operation (internal/updates.Journal). */
export type Journal = {
  id: string;
  component: UpdateComponent;
  from?: string;
  to: string;
  revision: number;
  state: JournalState;
  started_at: string;
  updated_at: string;
  steps: StepRecord[];
  rollback_steps?: StepRecord[];
  error?: string;
  failed_step?: string;
};

export type HistoryEntry = {
  id: string;
  component: UpdateComponent;
  from?: string;
  to: string;
  revision: number;
  result: "applied" | "rolled_back" | "rollback_failed";
  error?: string;
  failed_step?: string;
  backup_id?: string;
  started_at: string;
  finished_at: string;
};

export type UpdateBlocker = "up_to_date" | "needs_desktop" | "blocked" | "unknown_care" | "no_path";

/** ClinicUpdateStatus: the Updates screen. */
export type ClinicUpdates = {
  installed: InstalledVersions;
  verified_at?: string;
  unsigned?: boolean;
  check?: CheckResult;
  check_error?: string;
  checked_at?: string;
  blocker?: UpdateBlocker;
  notes?: ReleaseNote[];
  journal?: Journal;
  history: HistoryEntry[];
};

/** One backup set; copied sets are in the backup folder on this computer. */
export type BackupSet = {
  id: string;
  created_at: string;
  size: number;
  /** A copied set's folder, for RestoreBackup. */
  dir?: string;
  copied: boolean;
  latest?: boolean;
  verified_at?: string;
};

export type BackupsState = { dir: string; sets: BackupSet[]; status: BackupStatus; engine_error?: string };

/** The backup-progress event. */
export type BackupProgress = { job: "backup" | "restore"; state?: string; bytes?: number; total?: number; message?: string };

export type EngineSlot = { version: string; arch?: string; data_schema: number; current?: boolean; fallback?: boolean };

/** SystemInfo: the versions and engine slots on this computer. */
export type SystemInfo = {
  desktop_version: string;
  service_version?: string;
  engine?: EngineStatus;
  current?: string;
  fallback?: string;
  slots: EngineSlot[];
  error?: string;
};
