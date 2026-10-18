import type {
  AppState, AppUpdate, AppUpdateOutcome, AppUpdateProgress, BackupSet, BackupStatus, CareHealth, CheckResult, ClientPreflight,
  ClientReachability, ClinicUpdates, ConfirmationRequest, HistoryEntry, HomeAccess, HostCheck, HostReport, Journal, NetworkStatus,
  Plugin, PluginPreset, QuitRequest, RestoreChoice, Section, ServiceState, StepRecord, StorageState, SystemInfo,
  UpdateChecks,
} from "../../src/types";

type Host = Window["go"]["main"]["App"];
type Method = keyof Host;
type Call = { method: Method; args: unknown[] };
type Handler = (...data: unknown[]) => void;
type Results = { [K in Method]: Awaited<ReturnType<Host[K]>> };
type Fixtures = {
  checks: HostCheck[];
  serviceInstalled: boolean;
  network: NetworkStatus;
  homeAccess: HomeAccess;
  preflight: ClientPreflight;
  reachability: ClientReachability;
  addressTaken: boolean;
  running: boolean;
  reachable: boolean;
  clinicStatus: string;
  adminPassword: string;
  finishJobs: boolean;
  quitAccepted: boolean;
  env: Record<Section, string>;
  canRemoveApp: boolean;
  appRemoved: boolean;
  setupStarted: boolean;
  updateChecks: UpdateChecks;
  recoveryCodes: string[];
  recoveryGeneration: number;
  recoveryFailures: number;
  recoveryRetryAfter: number;
  webUsers: Record<string, string>;
  storage: StorageState;
  health: CareHealth;
  catalog: PluginPreset[];
  plugins: Plugin[];
  backups: BackupStatus;
  restoreChoice: RestoreChoice | null;
  copyFolder: string;
  clinicUpdates: ClinicUpdates;
  nextCheck: CheckResult;
  /** The sets ListBackups returns; its dir and status come from backups. */
  backupSets: BackupSet[];
  backupFolder: string;
  system: SystemInfo;
};

declare global {
  interface Window {
    careTest: {
      calls: Call[];
      logs: string[];
      state: AppState;
      fixtures: Fixtures;
      respond: <K extends Method>(method: K, value: Results[K]) => void;
      emit: (event: string, ...data: unknown[]) => void;
      emitService: () => void;
      failNext: (method: Method, error: string) => void;
      hold: (method: Method) => void;
      release: (method: Method) => void;
      progress: (value: AppUpdateProgress) => void;
      finishUpdate: (error?: string) => void;
      /** The download finished and verified: emits "ready" and ends the job. */
      downloaded: () => void;
      setOutcome: (value: AppUpdateOutcome) => void;
      setUpdate: (value: AppUpdate) => void;
      finishJob: (action: string, error?: string) => void;
      journal: (journal: Journal | null) => void;
      requestQuit: (action: string) => void;
      requestConfirmation: (title: string, message: string) => void;
      cancelConfirmation: () => void;
    };
  }
}

export function installTestHost() {
  if (!import.meta.env.DEV || import.meta.env.MODE !== "test") {
    throw new Error("The simulated host is only available to automated tests.");
  }
  const params = new URLSearchParams(location.search);
  const scenario = params.get("scenario") || "current";
  const savedRole = params.get("role");
  const state: AppState = {
    role: savedRole === "client" || savedRole === "server" ? savedRole : "",
    client_url: "",
    version: "0.1.5",
    platform: params.get("platform") || "darwin",
    setup_done: false,
    setup_started: false,
    mdns_name: "",
  };
  const calls: Call[] = [];
  const logs: string[] = [];
  const listeners = new Map<string, Set<Handler>>();
  const failures = new Map<Method, string>();
  const responses = new Map<Method, unknown>();
  const holds = new Map<Method, { wait: Promise<void>; release: () => void }>();
  let quitRequest: QuitRequest | null = null;
  let quitSequence = 0;
  let confirmation: ConfirmationRequest | null = null;
  let confirmationSequence = 0;
  const sampleCodes = (generation: number) => Array.from({ length: 6 }, (_, index) =>
    `AAAA-BBBB-CCCC-DDDD-EEEE-FFFF-${generation.toString(16).padStart(4, "0")}-${(index + 1).toString(16).padStart(4, "0")}`.toUpperCase());
  const GB = 2 ** 30;
  const at = (day: number, hour = 2) => new Date(Date.UTC(2026, 2, day, hour, 30)).toISOString();
  const clinicUpdates: ClinicUpdates = {
    installed: {
      desktop: "0.1.5",
      engine: { version: "1.2.3", data_schema: 1 },
      care: { version: "2026.3.0", released_at: at(2), images: [] },
      updated_at: at(4),
    },
    verified_at: at(10, 9),
    history: [
      { id: "op-2", component: "engine", from: "1.2.2", to: "1.2.3", revision: 7, result: "applied", started_at: at(4), finished_at: at(4, 3) },
      { id: "op-1", component: "care", from: "2026.2.1", to: "2026.3.0", revision: 6, result: "rolled_back",
        error: "CARE didn't answer its health check within 5 minutes", failed_step: "health", started_at: at(3), finished_at: at(3, 3) },
    ],
  };
  const nextCheck: CheckResult = { revision: 8, available: false, reason: "everything is up to date", prefetched: false };
  const backupFolder = "/Volumes/ClinicBackup/CARE";
  const backupDir = "/Library/Application Support/CARE Clinic/backups";
  const backupSets: BackupSet[] = [
    { id: "daily-20261018T000000Z", created_at: "2026-10-18T00:00:00Z", size: 412 * 2 ** 20, dir: `${backupDir}/daily-20261018T000000Z`,
      copied: true, latest: true, verified_at: "2026-10-18T00:05:00Z" },
    { id: "daily-20261017T000000Z", created_at: "2026-10-17T00:00:00Z", size: 405 * 2 ** 20, dir: `${backupDir}/daily-20261017T000000Z`, copied: true },
  ];
  const system: SystemInfo = {
    desktop_version: "0.1.5",
    service_version: "0.1.5",
    engine: { state: "ready", engine_version: "1.2.3", restarts: 0, uptime_seconds: 3 * 86400 + 4 * 3600 },
    current: "1.2.3",
    fallback: "1.2.2",
    slots: [
      { version: "1.2.3", arch: "arm64", data_schema: 1, current: true },
      { version: "1.2.2", arch: "arm64", data_schema: 1, fallback: true },
    ],
  };
  const fixtures: Fixtures = {
    checks: [
      { id: "virtualization", title: "Virtualization", level: "ok", detail: "This computer can run the CARE engine." },
      { id: "memory", title: "Memory", level: "ok", detail: "16 GB installed." },
      { id: "disk", title: "Disk space", level: "ok", detail: "212 GB free." },
    ],
    serviceInstalled: true,
    network: { applicable: state.platform === "windows", ok: true, message: "", how: "", fixable: true },
    homeAccess: {
      installed: false, running: false, https: false, host: "", url: "", ip: "", enabled: false, conflict: false, unknown: false,
      detail: "Tailscale is not installed on this computer.", clinic_ready: true,
    },
    preflight: { hosts_entry: false, old_certificate: false, unfinished_server_setup: false },
    reachability: { reachable: true, checked_at: 0, detail: "" },
    addressTaken: false,
    running: true,
    reachable: true,
    clinicStatus: "backend\nfrontend\ndb",
    adminPassword: "ClinicTest123",
    finishJobs: true,
    quitAccepted: false,
    env: {
      backend: "DB_BACKUP_RETENTION_PERIOD=0\nTIME_ZONE=Asia/Kolkata\nCORAZA_MODE=Off\nDEFAULT_FROM_EMAIL=clinic@example.invalid\nENABLE_OTP_LOGIN=False\nALLOW_PUBLIC_READ=False\n",
      frontend: "REACT_CARE_API_URL=https://care.local\nREACT_CARE_ENABLE_DASHBOARD=true\n",
    },
    canRemoveApp: params.get("removable") === "1",
    appRemoved: false,
    setupStarted: false,
    updateChecks: { app: !(params.get("checksOff") ?? "").split(",").includes("app") },
    recoveryCodes: sampleCodes(1),
    recoveryGeneration: 1,
    recoveryFailures: 0,
    recoveryRetryAfter: 0,
    webUsers: { admin: "ClinicTest123", nurse: "NurseTest123" },
    storage: {
      level: "ok", title: "Enough free space.",
      disks: [
        { kind: "engine", name: "Clinic data (engine disk)", free: 48 * GB, total: 64 * GB, level: "ok" },
        { kind: "backups", name: "Backup folder", path: "/Library/Application Support/CARE Clinic/backups", free: 418 * GB, total: 500 * GB, level: "ok" },
      ],
    },
    health: {
      healthy: true, code: 200, detail: "CARE is answering", checkedAt: new Date(0).toISOString(),
      services: [{ name: "backend", state: "running" }, { name: "db", state: "running" }, { name: "frontend", state: "running" }],
    },
    backups: {
      dir: "/Library/Application Support/CARE Clinic/backups", pending: 0,
      last_exported: "daily-20261018T000000Z", verified_at: "2026-10-18T00:05:00Z",
    },
    restoreChoice: {
      set_dir: "/Volumes/CARE USB/backups/daily-20261018T000000Z", id: "daily-20261018T000000Z", created_at: "2026-10-18T00:00:00Z",
    },
    copyFolder: "/Volumes/CARE USB/backups",
    catalog: [
      {
        id: "care_multihost_files_be", package: "git+https://github.com/ohcnetwork/care_multihost_files_be.git", version: "@main",
        configs: { ALLOWED_HOSTS: ["*.local", "*.ts.net"] }, bundled: true, default: true,
        label: "Files on every address", description: "Lets files open from more than one address.",
      },
      {
        id: "care_filly", package: "git+https://github.com/ohcnetwork/care_filly.git", version: "@main", bundled: true,
        label: "Filly", description: "AI scribe.", required_configs: ["MEDISPEAK_API_KEY", "MEDISPEAK_BASE_URL"],
      },
    ],
    plugins: [{
      id: "care_multihost_files_be", package: "git+https://github.com/ohcnetwork/care_multihost_files_be.git", version: "@main",
      configs: { ALLOWED_HOSTS: ["*.local", "*.ts.net"] }, bundled: true,
    }],
    clinicUpdates, nextCheck, backupSets, backupFolder, system,
  };
  const careSteps = ["preflight", "backup", "images", "stop-app", "write-env", "migrate", "start-stack", "health"];
  const steps = (names: string[], done: number, failed = -1): StepRecord[] => names.map((name, index) => ({
    name, attempts: index <= done ? 1 : 0,
    status: index === failed ? "failed" : index < done ? "done" : index === done ? "running" : "pending",
    ...(index === failed ? { error: "migration 0142 failed: column already exists" } : {}),
  }));
  const journalFor = (state: Journal["state"], done: number, failed = -1): Journal => ({
    id: "op-3", component: "care", from: "2026.3.0", to: "2026.4.0", revision: 8, state, started_at: at(11), updated_at: at(11),
    steps: steps(careSteps, done, failed),
  });
  if (scenario === "panel-update-available") {
    fixtures.clinicUpdates.unsigned = true;
    fixtures.nextCheck = {
      revision: 8, unsigned: true, available: true, prefetched: true,
      steps: [{ component: "engine", from: "1.2.3", to: "1.3.0" }, { component: "care", from: "2026.3.0", to: "2026.4.0" }],
    };
  }
  if (scenario === "panel-update-needs-desktop") {
    fixtures.nextCheck = { revision: 8, available: false, prefetched: false,
      reason: "no compatible update path: CARE 2026.4.0 requires desktop 0.3.0 (installed 0.1.5)" };
  }
  if (scenario === "panel-update-interrupted") fixtures.clinicUpdates.journal = journalFor("uncertain", 5);
  if (scenario === "panel-update-rollback-failed") {
    fixtures.clinicUpdates.journal = {
      ...journalFor("failed", 5, 5), error: "migration 0142 failed: column already exists", failed_step: "migrate",
      rollback_steps: [
        { name: "rollback-stop-app", status: "done", attempts: 1 },
        { name: "rollback-restore-db", status: "failed", attempts: 2, error: "the backup couldn't be read: disk I/O error" },
      ],
    };
  }
  if (scenario === "panel-plugin-rolled-back") {
    fixtures.clinicUpdates.history.unshift({
      id: "op-p", component: "plugins", from: "2026.3.0", to: "2026.3.0", revision: 7, result: "rolled_back", failed_step: "build",
      error: "image build failed: pip couldn't find care_filly@v9 (rolled back)", started_at: at(10, 4), finished_at: at(10, 5),
    });
  }
  if (scenario === "panel-backups-alert") {
    fixtures.backups = {
      ...fixtures.backups, critical: true, pending: 2, oldest_pending_at: "2026-10-18T00:00:00Z",
      message: "Backups haven't been copied out since 17 Oct.", last_error: "copy to the backup folder: no space left on device",
      last_error_at: "2026-10-18T00:05:00Z", verified_at: "2026-10-17T00:05:00Z", last_exported: "daily-20261017T000000Z",
    };
    fixtures.backupSets = [
      { id: "daily-20261018T000000Z", created_at: "2026-10-18T00:00:00Z", size: 412 * 2 ** 20, copied: false },
      { ...fixtures.backupSets[1], latest: true, verified_at: "2026-10-17T00:05:00Z" },
    ];
  }
  const recoveryCodeCount = params.get("recoveryCodes");
  if (recoveryCodeCount !== null) {
    fixtures.recoveryCodes = fixtures.recoveryCodes.map((code, index) => index < Number(recoveryCodeCount) ? code : "");
  }
  if (scenario === "panel-disk-low") {
    fixtures.storage = {
      level: "critical",
      title: "Clinic data (engine disk) is almost full (3.0 GB free). Free up space now: CARE stops saving records when it runs out.",
      disks: [
        { kind: "engine", name: "Clinic data (engine disk)", free: 3 * GB, total: 64 * GB, level: "critical" },
        { kind: "backups", name: "Backup folder", path: "/Volumes/Backup/care", free: 8 * GB, total: 500 * GB, level: "low" },
      ],
    };
    fixtures.health = {
      healthy: false, code: 502, detail: "CARE answered HTTP 502 Bad Gateway", checkedAt: new Date(0).toISOString(),
      services: [{ name: "backend", state: "restarting" }, { name: "db", state: "running" }],
    };
  }
  if (scenario === "panel-backups-critical") {
    fixtures.backups = {
      ...fixtures.backups, pending: 2, critical: true, oldest_pending_at: "2026-10-17T00:00:00Z",
      last_error: "the backup folder is not available (is its drive connected?)",
      message: "Backup not copied out since 18/10/2026, 00:05:00: the backup folder is not available (is its drive connected?). 2 backups are waiting in the engine, which keeps no more: the next one replaces the oldest.",
      copy: { dir: "/Volumes/CARE USB/backups", connected: false, message: "The extra backup copy (/Volumes/CARE USB/backups) isn't connected; last copied never" },
    };
  }
  if (scenario.startsWith("setup-")) state.role = "server";
  if (scenario === "setup-service") fixtures.serviceInstalled = false;
  if (scenario === "setup-memory") fixtures.checks[1] = { ...fixtures.checks[1], level: "fail", detail: "CARE needs at least 8 GB of memory." };
  if (scenario === "setup-windows") {
    state.platform = "windows";
    fixtures.network.applicable = true;
    fixtures.network.ok = false;
  }
  if (scenario === "setup-address") fixtures.addressTaken = true;
  if (scenario.startsWith("client-")) state.role = "client";
  if (scenario === "client-saved" || scenario === "client-offline") {
    state.client_url = "https://care.local";
    if (scenario === "client-offline") { fixtures.reachability.reachable = false; fixtures.reachability.detail = "the server did not answer"; }
  }
  if (scenario.startsWith("panel-") || params.get("screen") === "panel") {
    state.role = "server";
    state.setup_done = true;
    state.mdns_name = "care.local";
  }
  if (params.get("screen") === "remove") {
    state.role = "server";
    state.setup_done = true;
    state.mdns_name = "care.local";
  }
  if (scenario === "panel-stopped") fixtures.running = false;
  if (scenario === "panel-unreachable") fixtures.reachable = false;
  if (scenario === "panel-network") {
    state.platform = "windows";
    fixtures.network.applicable = true;
    fixtures.network.ok = false;
  }
  const available = ["available", "unsigned", "downloading", "installing", "verifying", "restarting", "installer", "failed", "download-failed", "unavailable"].includes(scenario);
  let update: AppUpdate = {
    current: "0.1.5",
    version: available ? "0.1.6" : "0.1.5",
    available,
    notes_url: "https://github.com/ohcnetwork/care_desktop/releases",
    asset: "CARE-Clinic-preview.dmg",
    size: 50_000_000,
    unsigned: scenario === "unsigned",
  };
  let updateRunning = false;
  let outcome: AppUpdateOutcome = { state: "", version: "" };

  const emit = (event: string, ...data: unknown[]) => {
    listeners.get(event)?.forEach((handler) => handler(...data));
  };
  const hostReport = (): HostReport => {
    const checks: HostCheck[] = [...fixtures.checks, fixtures.serviceInstalled
      ? { id: "service", title: "CARE Clinic service", level: "ok", detail: "Installed and running." }
      : { id: "service", title: "CARE Clinic service", level: "fail", detail: "The background service isn't installed yet.", action: "install-service" }];
    return { checks, ready: checks.every((check) => check.level !== "fail") };
  };
  const serviceState = (): ServiceState => {
    if (!fixtures.reachable) return {
      installed: true, reachable: false, error: "dial unix /var/run/care-clinic.sock: connection refused",
      summary: { level: "error", title: "Service not responding", detail: "The CARE Clinic service isn't answering.", running: false },
    };
    const running = fixtures.running;
    return {
      installed: true, reachable: true,
      status: {
        engine: { state: running ? "ready" : "stopped", engine_version: "1.2.3", restarts: 0,
          stack: { state: running ? "running" : "stopped", since: new Date().toISOString() } },
        slots: { current: "v1" },
        forward: { proxy_protocol: true, listeners: [
          { name: "https", listen: ":443", vsock_port: 8443, listening: running },
          { name: "http", listen: ":80", vsock_port: 8080, listening: running },
        ] },
        mdns: { enabled: true, name: (state.mdns_name || "care.local").replace(/\.local$/, ""), advertising: running },
        backups: structuredClone(fixtures.backups),
      },
      summary: running
        ? { level: "ok", title: "Running", detail: "Staff can open CARE on the clinic Wi-Fi.", running: true }
        : { level: "warn", title: "Stopped", detail: "Staff can't use CARE until it's started.", running: false },
    };
  };
  const emitService = () => emit("service-status", serviceState());
  const finishJob = (action: string, error?: string) => {
    if (error) {
      emit("care-log", `error: ${error}`);
      if (action !== "setup") emit("care-error", "CARE couldn't finish that", error);
      if (action === "setup") emit("setup-failed", { can_retry: true });
    } else {
      if (["start", "restart", "settings"].includes(action)) { fixtures.running = true; fixtures.clinicStatus = "backend\nfrontend\ndb"; }
      if (action === "stop") { fixtures.running = false; fixtures.clinicStatus = ""; }
      if (action === "uninstall") {
        state.role = "";
        state.setup_done = false;
        state.setup_started = false;
        state.mdns_name = "";
        state.client_url = "";
        fixtures.setupStarted = false;
        emit("uninstalled", true);
      }
      if (action === "setup") {
        for (const step of ["service", "engine", "engine-ready", "address", "images", "configure", "database", "start", "admin", "computer"]) {
          emit("setup-progress", { step, state: "active" });
          emit("setup-progress", { step, state: "done" });
        }
        state.setup_done = true;
        fixtures.running = true;
        emit("setup-done", true);
      }
      emitService();
    }
    emit("care-done", error ? 1 : 0, action);
  };
  const acceptJob = (action: string) => {
    if (fixtures.finishJobs) window.setTimeout(() => finishJob(action), 350);
  };
  const blockerFor = (check: CheckResult): ClinicUpdates["blocker"] => check.available ? undefined
    : check.reason === "everything is up to date" ? "up_to_date"
      : /is not a release in the update feed/.test(check.reason ?? "") ? "unknown_care"
        : /is blocked/.test(check.reason ?? "") ? "blocked" : /requires desktop/.test(check.reason ?? "") ? "needs_desktop" : "no_path";
  const notesFor = (check: CheckResult) => (check.steps ?? []).map((step) => ({
    component: step.component, version: step.to, released_at: at(10),
    migrations: step.component === "care",
    links: step.component === "care"
      ? [{ label: "CARE backend", url: `https://github.com/ohcnetwork/care/tree/v${step.to}` }]
      : [{ label: "Release notes", url: `https://github.com/praffq/care_clinic/releases/tag/${step.component}-v${step.to}` }],
  }));
  const record = (entry: Omit<HistoryEntry, "id" | "revision" | "started_at" | "finished_at">) => {
    const now = new Date().toISOString();
    fixtures.clinicUpdates.history = [
      { id: `op-${Date.now()}`, revision: 8, started_at: now, finished_at: now, ...entry },
      ...fixtures.clinicUpdates.history,
    ];
  };
  const runUpdate = (journal: Journal) => {
    if (!fixtures.finishJobs) return;
    void (async () => {
      for (let done = 0; done <= journal.steps.length; done++) {
        emit("update-journal", { ...journal, steps: journal.steps.map((step, index) => ({
          ...step, status: index < done ? "done" : index === done ? "running" : "pending", attempts: index <= done ? 1 : 0,
        })) });
        await wait(60);
      }
      emit("update-journal", null);
      fixtures.clinicUpdates.journal = undefined;
      fixtures.clinicUpdates.check = undefined;
      fixtures.clinicUpdates.blocker = undefined;
      fixtures.clinicUpdates.notes = undefined;
      record({ component: journal.component, from: journal.from, to: journal.to, result: "applied" });
      if (journal.component === "care") fixtures.clinicUpdates.installed.care = { ...fixtures.clinicUpdates.installed.care!, version: journal.to };
      finishJob("updates");
    })();
  };
  const pluginJournal = () => {
    if (!fixtures.finishJobs) return;
    const names = ["preflight", "backup", "build", "stop-app", "write-env", "migrate", "start-stack", "health"];
    [0, 2, 5].forEach((done, index) => window.setTimeout(() => emit("update-journal", {
      id: "op-plugins", component: "plugins", from: "2026.3.0", to: "2026.3.0", revision: 8, state: "running",
      started_at: at(11), updated_at: at(11), steps: steps(names, done),
    }), index * 90));
    window.setTimeout(() => emit("update-journal", null), 340);
  };
  const finishUpdate = (error?: string) => {
    if (error) {
      emit("care-log", `error: ${error}`);
      emit("care-error", "The CARE Clinic update didn't finish", error);
    }
    updateRunning = false;
    emit("care-done", error ? 1 : 0, "app-update");
  };
  const requireServer = () => {
    if (state.role !== "server") throw new Error("This operation requires the server role.");
  };
  const requireAdmin = (password: string) => {
    requireServer();
    if (!state.setup_done || password !== fixtures.adminPassword) throw new Error("the CARE Clinic admin password does not match this installation");
  };
  const wait = (ms: number) => new Promise<void>((resolve) => window.setTimeout(resolve, ms));
  const methods = {
    GetState: async () => ({ ...state }),
    UninstallRequested: async () => params.get("screen") === "remove",
    BeginServerSetup: async () => {
      if (state.role === "client") throw new Error("this computer is already a client; its role cannot be changed");
      state.role = "server";
    },
    ClearRole: async () => {
      if (state.setup_done || state.client_url || fixtures.setupStarted) throw new Error("Uninstall the current setup first.");
      state.role = "";
      state.mdns_name = "";
    },
    CanRemoveApp: async () => fixtures.canRemoveApp,
    ClientPreflight: async () => ({ ...fixtures.preflight }),
    FindClinic: async (address: string) => {
      if (fixtures.preflight.unfinished_server_setup) throw new Error("this computer has an unfinished clinic setup; remove it in Setup before connecting");
      if (scenario === "client-not-found") throw new Error("could not reach the clinic; check its address, network and main computer");
      const name = address.replace(/^https?:\/\//, "").replace(/\/$/, "");
      return { url: `https://${name}`, host: name, fingerprint: "PREVIEW", already_trusted: false };
    },
    ConnectClient: async (address: string) => {
      if (fixtures.preflight.unfinished_server_setup) throw new Error("this computer has an unfinished clinic setup; remove it in Setup before connecting");
      state.role = "client";
      state.client_url = address.startsWith("https://") ? address : `https://${address}`;
      for (const phase of ["finding", "connecting", "checking", "opening"]) {
        emit("client-connect-progress", phase);
        await wait(150);
      }
      logs.push("Test only: the clinic browser would open.");
    },
    ClientReachable: async () => ({ ...fixtures.reachability, checked_at: Math.floor(Date.now() / 1000) }),
    DisconnectClient: async () => { state.role = ""; state.client_url = ""; },
    SetMDNSName: async (name: string) => {
      requireServer();
      state.mdns_name = name;
    },
    HostChecks: async () => { requireServer(); return hostReport(); },
    InstallService: async () => {
      requireServer();
      await wait(150);
      fixtures.serviceInstalled = true;
    },
    ServiceStatus: async () => serviceState(),
    NetworkStatus: async () => ({ ...fixtures.network }),
    FixNetwork: async () => { requireServer(); fixtures.network.ok = true; },
    HomeAccessStatus: async () => { requireServer(); return { ...fixtures.homeAccess }; },
    EnableHomeAccess: async () => {
      requireServer();
      if (!fixtures.homeAccess.running || !fixtures.homeAccess.https) throw new Error(fixtures.homeAccess.detail);
      fixtures.homeAccess.enabled = true;
      return !fixtures.homeAccess.clinic_ready;
    },
    DisableHomeAccess: async () => { requireServer(); fixtures.homeAccess.enabled = false; },
    MDNSStatus: async () => ({ ok: !fixtures.addressTaken, message: fixtures.addressTaken ? "care.local is already in use on this network (192.0.2.1)" : "" }),
    ValidateDomain: async (name: string) => /^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.local)?$/i.test(name) ? "" : "Invalid clinic name",
    ValidatePassword: async (password: string) => [...password].length < 8 ? "Password must be at least 8 characters."
      : [...password].length > 20 ? "Password must be 20 characters or fewer."
      : !/\p{Lu}/u.test(password) || !/\p{Ll}/u.test(password) || !/\p{Nd}/u.test(password) ? "Password must include an uppercase letter, a lowercase letter, and a number." : "",
    ClinicStatus: async () => fixtures.clinicStatus,
    GetUpdateChecks: async () => ({ ...fixtures.updateChecks }),
    SetUpdateChecks: async (app: boolean, password: string) => {
      requireAdmin(password);
      fixtures.updateChecks = { app };
    },
    PluginCatalog: async () => fixtures.catalog.map((preset) => ({ ...preset })),
    FrontendPlugins: async () => [{
      slug: "care_onboarding_fe", label: "CARE Onboarding", required: true, config: { redirect_after_login: true },
      description: "Guided first-time clinic setup; opens after the first admin login while CARE has no facility.",
    }],
    ListPlugins: async () => {
      requireServer();
      return fixtures.plugins.map((plugin) => ({
        ...plugin,
        configs: Object.fromEntries(Object.entries(plugin.configs ?? {}).map(([key, value]) =>
          [key, /KEY|SECRET|TOKEN|PASSWORD/i.test(key) ? "********" : value])),
      }));
    },
    EnablePlugin: async (plugin: Plugin, password: string) => {
      requireAdmin(password);
      const old = fixtures.plugins.find((current) => current.id === plugin.id);
      const configs = Object.fromEntries(Object.entries(plugin.configs ?? {}).map(([key, value]) =>
        [key, value === "********" ? old?.configs?.[key] : value]));
      const next = { ...plugin, configs };
      fixtures.plugins = old ? fixtures.plugins.map((current) => current.id === plugin.id ? next : current) : [...fixtures.plugins, next];
      pluginJournal();
      acceptJob("plugins");
    },
    DisablePlugin: async (id: string, password: string) => {
      requireAdmin(password);
      if (!fixtures.plugins.some((plugin) => plugin.id === id)) throw new Error(`plugin ${id} is not enabled`);
      fixtures.plugins = fixtures.plugins.filter((plugin) => plugin.id !== id);
      pluginJournal();
      acceptJob("plugins");
    },
    ClinicAction: async (action: "start" | "stop" | "restart") => {
      requireServer();
      if (!fixtures.reachable) throw new Error("the CARE Clinic service isn't running");
      acceptJob(action);
    },
    ApplySettings: async (files: string[], password: string) => {
      requireAdmin(password);
      if (!fixtures.reachable) throw new Error("the CARE Clinic service isn't running");
      if (files.some((file) => file !== "backend" && file !== "frontend")) throw new Error("unknown env file");
      acceptJob("settings");
    },
    VerifyAdminPassword: async (password: string) => password === fixtures.adminPassword,
    ReadEnv: async (section: string, password: string) => {
      requireAdmin(password);
      if (section !== "backend" && section !== "frontend") throw new Error("unknown env file");
      return fixtures.env[section];
    },
    WriteEnv: async (section: string, content: string, password: string) => {
      requireAdmin(password);
      if (section !== "backend" && section !== "frontend") throw new Error("unknown env file");
      fixtures.env[section] = content;
    },
    ChangeAdminPassword: async (current: string, next: string) => {
      requireServer();
      if (current !== fixtures.adminPassword) throw new Error("the CARE Clinic admin password does not match this installation");
      const problem = await methods.ValidatePassword(next);
      if (problem) throw new Error(problem);
      fixtures.adminPassword = next;
    },
    GetAdminRecoveryCodeCount: async () => {
      requireServer();
      return fixtures.recoveryCodes.filter(Boolean).length;
    },
    SaveAdminRecoveryCodes: async (password: string) => {
      requireAdmin(password);
      fixtures.recoveryCodes = sampleCodes(++fixtures.recoveryGeneration);
      fixtures.recoveryFailures = 0;
      fixtures.recoveryRetryAfter = 0;
      emit("admin-recovery-codes-changed", fixtures.recoveryCodes.filter(Boolean).length);
      return "/Users/test/Desktop/CARE-Clinic-admin-recovery-codes.txt";
    },
    ResetAdminPassword: async (code: string, next: string) => {
      requireServer();
      const now = Math.floor(Date.now() / 1000);
      if (fixtures.recoveryRetryAfter > now) throw new Error(`too many recovery attempts; try again in ${fixtures.recoveryRetryAfter - now} seconds`);
      const problem = await methods.ValidatePassword(next);
      if (problem) throw new Error(problem);
      const normalize = (value: string) => value.replace(/[\s-]/g, "").toUpperCase();
      const index = fixtures.recoveryCodes.findIndex((candidate) => candidate && normalize(candidate) === normalize(code));
      if (index < 0) {
        fixtures.recoveryFailures++;
        if (fixtures.recoveryFailures >= 5) fixtures.recoveryRetryAfter = now + (fixtures.recoveryFailures - 4) * 60;
        throw new Error("that recovery code is invalid or already used; use an unused code from the latest set");
      }
      fixtures.recoveryCodes[index] = "";
      fixtures.adminPassword = next;
      fixtures.recoveryFailures = 0;
      fixtures.recoveryRetryAfter = 0;
      emit("admin-recovery-codes-changed", fixtures.recoveryCodes.filter(Boolean).length);
    },
    ResetWebPassword: async (username: string, next: string, password: string) => {
      requireAdmin(password);
      if (!fixtures.running) throw new Error("CARE isn't running; start CARE from Overview, then try again");
      if (!Object.prototype.hasOwnProperty.call(fixtures.webUsers, username)) throw new Error(`no CARE user named "${username}"; usernames are case-sensitive`);
      const problems = [
        ...(next.length < 8 ? ["This password is too short. It must contain at least 8 characters."] : []),
        ...(/^\d+$/.test(next) ? ["This password is entirely numeric."] : []),
      ];
      if (problems.length) throw new Error(`CARE didn't accept that password: ${problems.join(" ")}`);
      fixtures.webUsers[username] = next;
    },
    StorageStatus: async () => structuredClone(fixtures.storage),
    ExportBackupRecoveryKey: async (password: string) => {
      requireAdmin(password);
      return "/Volumes/KEYS/CARE-care-backup-recovery-key.pem";
    },
    ChooseBackupToRestore: async (otherKey: boolean) => {
      requireServer();
      if (!fixtures.restoreChoice) return null;
      const choice = structuredClone(fixtures.restoreChoice);
      return otherKey ? { ...choice, key_path: "/Volumes/KEYS/old-clinic-key.pem" } : choice;
    },
    RestoreBackup: async (setDir: string, _keyPath: string, password: string) => {
      requireAdmin(password);
      if (!fixtures.finishJobs) return;
      void (async () => {
        emit("backup-progress", { job: "restore", state: "restoring", message: `Restoring ${setDir.split("/").pop()}` });
        await wait(200);
        finishJob("restore");
      })();
    },
    SetBackupCopyDir: async (password: string) => {
      requireAdmin(password);
      if (!fixtures.copyFolder) return "";
      fixtures.backups.copy = { dir: fixtures.copyFolder, connected: true, last_copied: fixtures.backups.last_exported, copied_at: "2026-10-18T00:06:00Z" };
      emitService();
      return fixtures.copyFolder;
    },
    ClearBackupCopyDir: async (password: string) => {
      requireAdmin(password);
      delete fixtures.backups.copy;
      emitService();
    },
    CopyBackupsNow: async () => {
      const copy = fixtures.backups.copy;
      if (copy && !copy.connected) throw new Error(copy.message ?? "not connected");
    },
    PruneEngineImages: async () => {
      requireServer();
      acceptJob("prune-images");
    },
    ClinicUpdateStatus: async () => {
      requireServer();
      return structuredClone(fixtures.clinicUpdates);
    },
    CheckClinicUpdates: async () => {
      requireServer();
      if (!fixtures.finishJobs) return;
      window.setTimeout(() => {
        const check = structuredClone(fixtures.nextCheck);
        Object.assign(fixtures.clinicUpdates, {
          check, check_error: undefined, checked_at: new Date().toISOString(), blocker: blockerFor(check),
          notes: check.available ? notesFor(check) : undefined, verified_at: new Date().toISOString(),
        });
        finishJob("update-check");
      }, 250);
    },
    ApplyClinicUpdates: async (password: string) => {
      requireAdmin(password);
      const step = fixtures.clinicUpdates.check?.steps?.find((candidate) => candidate.component === "care")
        ?? fixtures.clinicUpdates.check?.steps?.[0];
      if (!step) throw new Error("check for updates first");
      runUpdate({ ...journalFor("running", 0), component: step.component, from: step.from, to: step.to });
    },
    ResumeClinicUpdate: async (password: string) => {
      requireAdmin(password);
      const journal = fixtures.clinicUpdates.journal;
      if (!journal) throw new Error("no unfinished update operation");
      if (journal.state === "failed") throw new Error("the rollback failed; use Rollback to retry it");
      runUpdate({ ...journal, state: "running" });
    },
    RollBackClinicUpdate: async (password: string) => {
      requireAdmin(password);
      const journal = fixtures.clinicUpdates.journal;
      if (!journal) throw new Error("no unfinished update operation");
      if (!fixtures.finishJobs) return;
      window.setTimeout(() => {
        fixtures.clinicUpdates.journal = undefined;
        emit("update-journal", null);
        record({ component: journal.component, from: journal.from, to: journal.to, result: "rolled_back",
          error: journal.error ?? "rolled back on request", failed_step: journal.failed_step });
        finishJob("updates");
      }, 250);
    },
    ListBackups: async () => {
      requireServer();
      return structuredClone({ dir: fixtures.backups.dir, sets: fixtures.backupSets, status: fixtures.backups });
    },
    BackUpNow: async () => {
      requireServer();
      if (!fixtures.finishJobs) return;
      void (async () => {
        const total = 420 * 2 ** 20;
        for (const bytes of [0, total / 2, total]) {
          emit("backup-progress", { job: "backup", state: "copying", bytes, total, message: "Copying to the backup folder" });
          await wait(80);
        }
        const now = new Date().toISOString();
        const id = `daily-${now.replace(/[-:]/g, "").replace(/\.\d+/, "")}`;
        fixtures.backupSets = [{ id, created_at: now, size: total, dir: `${fixtures.backups.dir}/${id}`, copied: true, latest: true, verified_at: now },
          ...fixtures.backupSets.map((set) => ({ ...set, latest: false }))];
        fixtures.backups = { ...fixtures.backups, last_exported: id, verified_at: now, checked_at: now,
          pending: 0, critical: false, message: undefined, last_error: undefined, last_error_at: undefined };
        emitService();
        finishJob("backup");
      })();
    },
    ChooseBackupFolder: async (password: string) => {
      requireAdmin(password);
      if (!fixtures.backupFolder) return "";
      fixtures.backups.dir = fixtures.backupFolder;
      emitService();
      return fixtures.backupFolder;
    },
    SystemInfo: async () => structuredClone(fixtures.system),
    CheckCareHealth: async () => structuredClone(fixtures.health),
    RunUninstall: async (_removeData: boolean, password: string) => {
      requireAdmin(password);
      acceptJob("uninstall");
    },
    RemoveApp: async () => {
      if (state.setup_done || state.client_url) throw new Error("uninstall the clinic setup or disconnect this computer before removing the app");
      if (!fixtures.canRemoveApp) throw new Error("This preview has no removable application bundle.");
      fixtures.appRemoved = true;
      logs.push("Test only: the desktop application would close and be removed.");
    },
    ExitUninstall: async () => { logs.push("Test only: the uninstaller would finish."); },
    LogPath: async () => "/test-fixtures/logs/care-clinic.log",
    RetrySetup: async () => {
      requireServer();
      if (state.setup_done || !fixtures.setupStarted) throw new Error("there is no unfinished installation that can be retried in this session");
      if (fixtures.finishJobs) window.setTimeout(() => finishJob("setup"), 350);
    },
    CleanupFailedInstall: async () => {
      requireServer();
      if (state.setup_done) throw new Error("this clinic is installed; use Uninstall instead");
      fixtures.setupStarted = false;
      state.setup_started = false;
    },
    RunSetup: async (name: string, password: string): Promise<void> => {
      if (params.get("simulateInstall") !== "1") throw new Error("This action is disabled in the automated test fixture.");
      requireServer();
      if (!hostReport().ready) throw new Error("this computer isn't ready");
      if (await methods.ValidateDomain(name)) throw new Error("invalid clinic name");
      const problem = await methods.ValidatePassword(password);
      if (problem) throw new Error(problem);
      state.mdns_name = name.endsWith(".local") ? name : `${name}.local`;
      fixtures.adminPassword = password;
      fixtures.setupStarted = true;
      state.setup_started = true;
      if (!fixtures.finishJobs) return;
      void (async () => {
        for (const line of [
          "Using the installed engine 1.2.3",
          "Starting the engine...",
          "Using the CARE images already in the engine",
          "Uploading the clinic configuration",
          "Generated new clinic secrets",
          "Applying database migrations",
          "Starting CARE",
          `CARE is up -> https://${state.mdns_name}`,
        ]) {
          emit("care-log", line);
          await wait(700);
        }
        finishJob("setup", scenario === "installation-failed" ? "Simulated installation failure for preview." : undefined);
      })();
    },
    CheckAppUpdate: async () => {
      if (!fixtures.updateChecks.app) throw new Error("CARE Clinic app update checks are turned off; an administrator can turn them back on in Advanced");
      if (scenario === "offline") throw new Error("couldn't reach GitHub to check for updates: network is unreachable");
      if (scenario === "check-error") throw new Error("GitHub answered HTTP 403 for https://api.github.com");
      if (scenario === "unavailable") throw new Error("release 0.1.6 has no installer for this computer");
      return { ...update };
    },
    DownloadAppUpdate: async () => {
      if (!fixtures.updateChecks.app) throw new Error("CARE Clinic app update checks are turned off; an administrator can turn them back on in Advanced");
      if (updateRunning) throw new Error("something else is still running - wait for it to finish");
      updateRunning = true;
      // Fixture events only. No native updater, filesystem, or network is used.
      queueMicrotask(() => {
        if (scenario === "failed" || scenario === "download-failed") {
          finishUpdate(scenario === "failed"
            ? "couldn't replace CARE Clinic, so the current version was kept: exit status 1"
            : "the CARE Clinic 0.1.6 update didn't download properly and was deleted without being installed (SHA-256 mismatch)");
          return;
        }
        const phase: AppUpdateProgress["phase"] =
          scenario === "installing" || scenario === "verifying" ||
          scenario === "restarting" || scenario === "installer"
            ? scenario : "downloading";
        emit("app-update-progress", { phase, done: 31_000_000, total: 50_000_000 });
        if (phase === "installer" || phase === "restarting") finishUpdate();
      });
    },
    RestartToUpdate: async () => {
      if (updateRunning) throw new Error("something else is still running - wait for it to finish");
      if (!update.ready) throw new Error("download the update first");
      updateRunning = true;
      queueMicrotask(() => {
        emit("app-update-progress", { phase: "installing", done: 0, total: 0 });
        emit("app-update-progress", { phase: "restarting", done: 0, total: 0 });
        finishUpdate();
      });
    },
    GetAppUpdateOutcome: async () => ({ ...outcome }),
    FinishAppUpdate: async () => {
      if (outcome.state !== "failed" || !outcome.can_retry) throw new Error("no unfinished CARE Clinic update");
      outcome = { state: "finishing", version: outcome.version };
      emit("app-update-outcome", { ...outcome });
      queueMicrotask(() => {
        outcome = { state: "done", version: outcome.version };
        emit("app-update-outcome", { ...outcome });
        emit("care-done", 0, "app-update");
      });
    },
    OpenURL: async () => { logs.push("Test only: release notes requested."); },
    OpenLogFolder: async () => { logs.push("Test only: log file requested."); },
    SetQuitDialogReady: async (ready: boolean) => {
      if (!ready) quitRequest = null;
      return quitRequest ? { ...quitRequest } : null;
    },
    RespondToQuit: async (id: number, quit: boolean) => {
      if (!quitRequest || quitRequest.id !== id) throw new Error("this quit request is no longer active");
      quitRequest = null;
      fixtures.quitAccepted = quit;
      if (quit) logs.push("Test only: CARE Clinic would close after confirmation.");
    },
    SetConfirmationDialogReady: async (ready: boolean) => {
      if (!ready) confirmation = null;
      return confirmation ? { ...confirmation } : null;
    },
    RespondToConfirmation: async (id: number, approved: boolean) => {
      if (!confirmation || confirmation.id !== id) throw new Error("this permission request is no longer active");
      confirmation = null;
      logs.push(`Test only: permission ${approved ? "approved" : "declined"}.`);
    },
  } satisfies Partial<Host>;

  // Every omitted action fails closed. Keeping the full type also catches new
  // bindings so a preview cannot accidentally inherit a real native method.
  const host: Host = new Proxy({
    ...methods,
  }, {
    get(target, key) {
      if (typeof key !== "string" || !Object.prototype.hasOwnProperty.call(target, key)) return undefined;
      return async (...args: unknown[]) => {
        const method = key as Method;
        calls.push({ method, args });
        await holds.get(method)?.wait;
        if (failures.has(method)) {
          const error = failures.get(method);
          failures.delete(method);
          throw new Error(error);
        }
        if (responses.has(method)) return responses.get(method);
        const handler: unknown = Reflect.get(target, key);
        if (typeof handler !== "function") throw new Error(`${key} is disabled in the automated test fixture.`);
        return Reflect.apply(handler, target, args);
      };
    },
  });

  window.go = { main: { App: host } };
  window.runtime = {
    EventsOn(event, handler) {
      const handlers = listeners.get(event) ?? new Set<Handler>();
      handlers.add(handler);
      listeners.set(event, handlers);
      return () => { handlers.delete(handler); };
    },
    EventsEmit: emit,
    LogPrint: (line) => { logs.push(line); },
  };
  window.careTest = {
    calls, logs, state, emit, emitService, fixtures, finishJob,
    journal: (journal) => emit("update-journal", journal),
    requestQuit: (action) => {
      quitRequest = { id: ++quitSequence, title: "CARE is still working", message: `CARE is still ${action}. Quitting now stops it before it finishes. Commands that already started may keep running in the background for a few minutes.` };
      emit("quit-requested", { ...quitRequest });
    },
    requestConfirmation: (title, message) => {
      confirmation = { id: ++confirmationSequence, title, message };
      emit("confirmation-requested", { ...confirmation });
    },
    cancelConfirmation: () => {
      if (!confirmation) return;
      const id = confirmation.id;
      confirmation = null;
      emit("confirmation-cancelled", id);
    },
    respond: (method, value) => { responses.set(method, value); },
    failNext: (method, error) => { failures.set(method, error); },
    hold(method) {
      let release = () => {};
      const wait = new Promise<void>((resolve) => { release = resolve; });
      holds.set(method, { wait, release });
    },
    release(method) {
      holds.get(method)?.release();
      holds.delete(method);
    },
    progress: (value) => emit("app-update-progress", value),
    finishUpdate,
    downloaded: () => {
      update = { ...update, ready: true };
      emit("app-update-progress", { phase: "ready", done: 0, total: 0 });
      finishUpdate();
    },
    setOutcome: (value) => { outcome = value; emit("app-update-outcome", { ...value }); },
    setUpdate: (value) => { update = value; },
  };
}
