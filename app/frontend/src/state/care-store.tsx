// One store for everything that outlives a single screen: which view is up, how
// the install is progressing, and the panel's view of the background service.
// Screens own their own form state; this owns the parts the rail and the Wails
// events touch.
import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from "react";

import { toast } from "@/components/ui/sonner";
import { bridge, logToHost, onCareEvent } from "@/lib/bridge";
import { errorText } from "@/lib/format";
import { operationError as describeOperationError, type OperationError } from "@/lib/operation-errors";
import { applyProgress, failActiveStep, type SetupProgress, type SetupSteps, stepsForRetry } from "@/lib/run-steps";
import type { AppUpdateOutcome, AppUpdateProgress, ServiceState, SetupFailure, SetupPage, UpdateChecks } from "@/types";

export type Flow = "role" | "client" | "setup" | "installing" | "failed" | "panel" | "remove";
export type PanelTab = "overview" | "home-access" | "updates" | "advanced";
export type ClinicAction = "start" | "stop" | "restart";

export type InstallParams = {
  host: string;
  adminPassword: string;
  pages?: SetupPage[];
};

export type RunState = {
  steps: SetupSteps;
  startedAt: number;
  finished: boolean;
  failMessage: string;
  failure: SetupFailure | null;
  pages: SetupPage[];
};

const IDLE_RUN: RunState = {
  steps: {},
  startedAt: 0,
  finished: false,
  failMessage: "",
  failure: null,
  pages: ["computer", "address", "admin", "review", "install"],
};

const ACTION_LABELS: Record<ClinicAction, string> = {
  start: "Starting",
  stop: "Stopping",
  restart: "Restarting",
};

type CareStore = {
  ready: boolean;
  flow: Flow;
  mdnsName: string;
  clientURL: string;
  selectRole: (role: "server" | "client") => void;
  clearRole: () => Promise<boolean>;

  run: RunState;
  setupReset: number;
  startInstall: (params: InstallParams) => Promise<void>;
  resumeInstall: () => Promise<void>;
  retryInstall: () => Promise<void>;
  restartSetup: () => void;
  openPanel: () => void;

  tab: PanelTab;
  setTab: (tab: PanelTab) => void;
  busy: boolean;
  busyLabel: string;
  operationError: OperationError | null;
  clearOperationError: () => void;
  /** The background service's latest status; null until the first reading. */
  service: ServiceState | null;
  refreshService: () => Promise<void>;
  updateChecks: UpdateChecks | null;
  reloadUpdateChecks: () => Promise<void>;
  saveUpdateChecks: (next: UpdateChecks, adminPassword: string) => Promise<void>;
  /** Downloads and verifies the update ("download"), or installs the downloaded one and restarts ("restart"). */
  runAppUpdate: (step: "download" | "restart") => Promise<void>;
  acknowledgeAppUpdate: () => boolean;
  version: string;
  platform: string;
  /** Resolves true when the native job is accepted; care-done reports completion. */
  runAction: (action: ClinicAction) => Promise<boolean>;
  /** Starts a native job labelled label; like runAction, care-done reports completion. */
  runJob: (label: string, busyLabel: string, start: () => Promise<void>) => Promise<boolean>;
  uninstall: (removeData: boolean, adminPassword: string, removeApp?: boolean) => Promise<boolean>;
  log: (line: string) => void;
};

const CareContext = createContext<CareStore | null>(null);

export function useCare(): CareStore {
  const store = useContext(CareContext);
  if (!store) throw new Error("useCare must be used inside <CareProvider>");
  return store;
}

export function CareProvider({ children }: { children: ReactNode }) {
  const [ready, setReady] = useState(false);
  const [flow, setFlowState] = useState<Flow>("role");
  const [clientURL, setClientURL] = useState("");
  const [mdnsName, setMdnsName] = useState("care.local");
  const [run, setRunState] = useState<RunState>(IDLE_RUN);
  const [setupReset, setSetupReset] = useState(0);
  const [tab, setTab] = useState<PanelTab>("overview");
  const [busy, setBusyState] = useState(false);
  const [busyLabel, setBusyLabel] = useState("");
  const [operationError, setOperationError] = useState<OperationError | null>(null);
  const [service, setService] = useState<ServiceState | null>(null);
  const [version, setVersion] = useState("");
  const [platform, setPlatform] = useState("");
  const [updateChecks, setUpdateChecks] = useState<UpdateChecks | null>(null);
  const [bootError, setBootError] = useState<Error | null>(null);

  // Refs shadow the state the event handlers read, so they never work from a
  // stale closure and never need to re-subscribe.
  const flowRef = useRef<Flow>("role");
  const runRef = useRef<RunState>(IDLE_RUN);
  const busyRef = useRef(false);
  // Buffered so the fail screen can show the real error rather than just
  // "exit status 1". Kept out of React state: only a step change repaints.
  const logRef = useRef<string[]>([]);
  const lastErrorRef = useRef("");
  const removeAppRef = useRef(false);
  const uninstalledRef = useRef(false);
  const uninstallEndedRef = useRef(false);
  const activeActionRef = useRef("");
  const setupEndedRef = useRef(false);
  const setupFailureReceivedRef = useRef(false);
  const bootStartedRef = useRef(false);
  // A pushed service-status is newer than any ServiceStatus() still in flight.
  const serviceEpochRef = useRef(0);
  const appUpdatePhaseRef = useRef<AppUpdateProgress["phase"] | null>(null);
  const appUpdateHandoffRef = useRef(false);
  // The relaunched app is finishing a self-update (service, clinic health).
  const appUpdateFinishingRef = useRef(false);

  const setFlow = useCallback((next: Flow) => {
    flowRef.current = next;
    setFlowState(next);
  }, []);

  const selectRole = useCallback((role: "server" | "client") => {
    if (busyRef.current || flowRef.current !== "role") return;
    setFlow(role === "client" ? "client" : "setup");
  }, [setFlow]);

  const setRun = useCallback((next: RunState) => {
    runRef.current = next;
    setRunState(next);
  }, []);

  const setBusy = useCallback((next: boolean, label = "") => {
    busyRef.current = next;
    setBusyState(next);
    setBusyLabel(label);
  }, []);

  const clearOperationError = useCallback(() => setOperationError(null), []);

  const pushLine = useCallback(
    (line: string) => {
      logRef.current.push(line);
      if (logRef.current.length > 300) logRef.current.shift();
    },
    [],
  );

  // Lines raised HERE (a failed save, a caught render error) have never been near
  // Go, so they reach the log file only if we send them. Lines arriving on
  // care-log are already in it — see logFromHost below.
  const log = useCallback((line: string) => {
    logToHost(line);
    if (flowRef.current === "panel") return;
    pushLine(line);
  }, [pushLine]);

  const logFromHost = useCallback(
    (line: string) => {
      if (flowRef.current === "panel") return;
      pushLine(line);
    },
    [pushLine],
  );

  const clearRole = useCallback(async () => {
    try {
      await bridge.ClearRole();
    } catch (e) {
      log(`role: ${errorText(e)}`);
      toast(/already set up|uninstall/i.test(errorText(e))
        ? "This computer already has a saved setup. Disconnect or remove it before going back."
        : "Couldn't go back. Try again, or share the log file with your support contact.");
      return false;
    }
    setClientURL("");
    setFlow("role");
    return true;
  }, [log, setFlow]);

  const failInstall = useCallback(() => {
    const tail = logRef.current.slice(-40).join("\n").trim();
    const headline = (lastErrorRef.current || "Setup did not complete.").trim();
    setRun({
      ...runRef.current,
      steps: failActiveStep(runRef.current.steps),
      failMessage: tail ? `${headline}\n\n---- last output ----\n${tail}` : headline,
    });
    setFlow("failed");
  }, [setFlow, setRun]);

  // --- panel ------------------------------------------------------------
  const refreshService = useCallback(async () => {
    const epoch = ++serviceEpochRef.current;
    try {
      const next = await bridge.ServiceStatus();
      if (epoch === serviceEpochRef.current) setService(next);
    } catch (e) {
      log(`service status: ${errorText(e)}`);
    }
  }, [log]);

  const runAction = useCallback(
    async (action: ClinicAction) => {
      if (busyRef.current) {
        setOperationError(describeOperationError(action, "something else is still running"));
        return false;
      }
      setBusy(true, ACTION_LABELS[action]);
      activeActionRef.current = action;
      lastErrorRef.current = "";
      setOperationError(null);
      log(`\n$ care ${action}`);
      try {
        await bridge.ClinicAction(action);
        return true;
      } catch (e) {
        log(`error: ${errorText(e)}`);
        setOperationError(describeOperationError(action, e));
        activeActionRef.current = "";
        setBusy(false);
        return false;
      }
    },
    [log, setBusy],
  );

  const runJob = useCallback(
    async (label: string, jobBusyLabel: string, start: () => Promise<void>) => {
      if (busyRef.current) throw new Error("something else is still running");
      setBusy(true, jobBusyLabel);
      activeActionRef.current = label;
      lastErrorRef.current = "";
      setOperationError(null);
      log(`\n$ care ${label}`);
      try {
        await start();
        return true;
      } catch (e) {
        log(`error: ${errorText(e)}`);
        activeActionRef.current = "";
        setBusy(false);
        throw e;
      }
    },
    [log, setBusy],
  );

  const runAppUpdate = useCallback(async (step: "download" | "restart") => {
    if (busyRef.current) throw new Error("Something else is still running.");
    appUpdatePhaseRef.current = null;
    appUpdateHandoffRef.current = false;
    lastErrorRef.current = "";
    setBusy(true, "Updating CARE Clinic");
    activeActionRef.current = "app-update";
    log(step === "download" ? "\n$ care-clinic update --download" : "\n$ care-clinic update --restart");
    try {
      await (step === "download" ? bridge.DownloadAppUpdate() : bridge.RestartToUpdate());
    } catch (e) {
      log(`error: ${errorText(e)}`);
      if (flowRef.current !== "role") {
        toast.error("CARE Clinic couldn't finish updating. Your current version was kept. Try again.");
      }
      setBusy(false);
      activeActionRef.current = "";
      throw e;
    }
  }, [log, setBusy]);

  const acknowledgeAppUpdate = useCallback(() => {
    if (!appUpdateHandoffRef.current || appUpdatePhaseRef.current !== "installer") {
      return !busyRef.current;
    }
    appUpdateHandoffRef.current = false;
    appUpdatePhaseRef.current = null;
    activeActionRef.current = "";
    setBusy(false);
    return true;
  }, [setBusy]);

  const reloadUpdateChecks = useCallback(async () => {
    try {
      setUpdateChecks(await bridge.GetUpdateChecks());
    } catch (e) {
      log(`read update checks: ${errorText(e)}`);
      setUpdateChecks((current) => current ?? { app: true });
    }
  }, [log]);

  const saveUpdateChecks = useCallback(async (next: UpdateChecks, adminPassword: string) => {
    await bridge.SetUpdateChecks(next.app, adminPassword);
    setUpdateChecks(next);
  }, []);

  useEffect(() => { void reloadUpdateChecks(); }, [reloadUpdateChecks]);

  const uninstall = useCallback(
    async (removeData: boolean, adminPassword: string, removeApp = false) => {
      if (busyRef.current) {
        setOperationError(describeOperationError("uninstall", "something else is still running"));
        return false;
      }
      removeAppRef.current = removeApp;
      uninstalledRef.current = false;
      uninstallEndedRef.current = false;
      setBusy(true, "Uninstalling");
      activeActionRef.current = "uninstall";
      lastErrorRef.current = "";
      setOperationError(null);
      log(`\n$ care uninstall${removeData ? " --delete-data" : ""} --yes`);
      try {
        await bridge.RunUninstall(removeData, adminPassword);
        return true;
      } catch (e) {
        log(`error: ${errorText(e)}`);
        setBusy(false);
        activeActionRef.current = "";
        setOperationError(describeOperationError("uninstall", e));
        removeAppRef.current = false;
        return false;
      }
    },
    [log, setBusy],
  );

  const bootPanel = useCallback(async () => {
    try {
      const state = await bridge.GetState();
      if (state.role !== "server") {
        setFlow(state.role === "client" ? "client" : "role");
        return;
      }
      setMdnsName(state.mdns_name || "care.local");
    } catch (e) {
      setBootError(new Error(errorText(e)));
      return;
    }
    setTab("overview");
    await refreshService();
  }, [refreshService, setFlow]);

  const openPanel = useCallback(() => {
    setFlow("panel");
    void bootPanel();
  }, [bootPanel, setFlow]);

  // --- setup flow -------------------------------------------------------
  const startInstall = useCallback(
    async (params: InstallParams) => {
      if (busyRef.current || flowRef.current !== "setup") {
        throw new Error("Something else is still running. Wait for it to finish.");
      }
      setBusy(true, "Checking setup");
      activeActionRef.current = "setup";
      setupEndedRef.current = false;
      setupFailureReceivedRef.current = false;
      logRef.current = [];
      lastErrorRef.current = "";
      setMdnsName(params.host);
      setRun({
        ...IDLE_RUN,
        startedAt: Date.now(),
        pages: params.pages ?? IDLE_RUN.pages,
      });
      try {
        await bridge.RunSetup(params.host, params.adminPassword);
        if (flowRef.current === "setup" && !setupEndedRef.current) {
          setFlow("installing");
          setBusy(true, "Installing CARE");
        }
        log("Starting one-time setup...");
      } catch (e) {
        log(`setup validation: ${errorText(e)}`);
        if (activeActionRef.current === "setup") {
          activeActionRef.current = "";
          setBusy(false);
        }
        throw e;
      }
    },
    [log, setBusy, setFlow, setRun],
  );

  const resumeInstall = useCallback(async () => {
    if (busyRef.current || flowRef.current !== "failed" || !runRef.current.failure?.can_retry) {
      throw new Error("This installation isn't ready to retry.");
    }
    const previous = runRef.current;
    setBusy(true, "Retrying installation");
    activeActionRef.current = "setup";
    setupEndedRef.current = false;
    setupFailureReceivedRef.current = false;
    logRef.current = [];
    lastErrorRef.current = "";
    setRun({
      ...IDLE_RUN,
      steps: stepsForRetry(previous.steps),
      pages: previous.pages,
      failure: previous.failure,
      startedAt: Date.now(),
    });
    try {
      await bridge.RetrySetup();
      if (!setupEndedRef.current || activeActionRef.current === "setup") {
        if (!setupFailureReceivedRef.current) setRun({ ...runRef.current, failure: null });
        setFlow("installing");
        setBusy(true, "Installing CARE");
      }
      log("Retrying the unfinished installation...");
    } catch (error) {
      log(`retry installation: ${errorText(error)}`);
      if (!setupEndedRef.current) {
        setRun(previous);
        activeActionRef.current = "";
        setBusy(false);
      }
      throw error;
    }
  }, [log, setBusy, setFlow, setRun]);

  const restartSetup = useCallback(() => {
    lastErrorRef.current = "";
    logRef.current = [];
    setRun(IDLE_RUN);
    setFlow("setup");
  }, [setFlow, setRun]);

  const retryInstall = useCallback(async () => {
    if (busyRef.current) throw new Error("something else is still running");
    setBusy(true, "Clearing the unfinished installation");
    activeActionRef.current = "cleanup-failed";
    try {
      await bridge.CleanupFailedInstall();
      setSetupReset((value) => value + 1);
      restartSetup();
    } catch (e) {
      log(`cleanup: ${errorText(e)}`);
      throw e;
    } finally {
      activeActionRef.current = "";
      setBusy(false);
    }
  }, [log, restartSetup, setBusy]);

  const finishUninstall = useCallback(() => {
    if (!uninstalledRef.current || !uninstallEndedRef.current) return;
    uninstalledRef.current = false;
    uninstallEndedRef.current = false;
    activeActionRef.current = "";
    if (flowRef.current === "remove") {
      void bridge.ExitUninstall().catch((e) => {
        log(`finish uninstall: ${errorText(e)}`);
        setBusy(false);
        toast.error("The clinic was removed, but the uninstaller couldn't close. Close this window to finish.");
      });
      return;
    }
    const removeApp = removeAppRef.current;
    removeAppRef.current = false;
    setMdnsName("care.local");
    setClientURL("");
    setService(null);
    setUpdateChecks({ app: true });
    void reloadUpdateChecks();
    setOperationError(null);
    setRun(IDLE_RUN);
    setSetupReset((value) => value + 1);
    setFlow("role");
    setBusy(removeApp, removeApp ? "Removing CARE Clinic" : "");
    if (removeApp) {
      void bridge.RemoveApp().catch((e) => {
        log(`remove app: ${errorText(e)}`);
        setBusy(false);
        toast.error("The clinic was removed, but CARE Clinic couldn't remove itself. Close it and remove the application using this computer's settings.");
      });
    } else {
      toast("The clinic was removed from this computer.");
    }
  }, [log, reloadUpdateChecks, setBusy, setFlow, setRun]);

  // --- host events ------------------------------------------------------
  useEffect(() => {
    const unsubscribes = [
      onCareEvent("care-log", (line: string) => {
        if (line.startsWith("error:")) {
          lastErrorRef.current = line.slice("error:".length).trim();
        }
        logFromHost(line);
      }),
      onCareEvent("care-error", (title: string, detail: string) => {
        if (title === "The CARE Clinic update didn't finish") return;
        lastErrorRef.current = detail;
        if (flowRef.current === "panel") {
          const error = describeOperationError(activeActionRef.current || "operation", detail);
          setOperationError({ ...error, title });
        }
      }),
      onCareEvent("service-status", (next: ServiceState) => {
        serviceEpochRef.current++;
        setService(next);
      }),
      onCareEvent("setup-progress", (progress: SetupProgress) => {
        if (activeActionRef.current !== "setup" || setupEndedRef.current) return;
        setRun({ ...runRef.current, steps: applyProgress(runRef.current.steps, progress) });
      }),
      onCareEvent("setup-failed", (failure: SetupFailure) => {
        if (activeActionRef.current !== "setup" || setupEndedRef.current) return;
        setupFailureReceivedRef.current = true;
        setRun({ ...runRef.current, failure });
      }),
      onCareEvent("app-update-progress", (progress: AppUpdateProgress) => {
        if (activeActionRef.current && activeActionRef.current !== "app-update") return;
        appUpdatePhaseRef.current = progress.phase;
        // A reloaded UI may attach while the native updater is already running.
        if (!activeActionRef.current) {
          activeActionRef.current = "app-update";
          setBusy(true, "Updating CARE Clinic");
        }
      }),
      onCareEvent("app-update-outcome", (outcome: AppUpdateOutcome) => {
        if (outcome.state !== "finishing" || (activeActionRef.current && activeActionRef.current !== "app-update")) return;
        appUpdateFinishingRef.current = true;
        activeActionRef.current = "app-update";
        setBusy(true, "Finishing the CARE Clinic update");
      }),
      onCareEvent("care-done", (code: number, label?: string) => {
        if (label === "app-update") {
          if (activeActionRef.current && activeActionRef.current !== label) return;
          if (appUpdateFinishingRef.current && appUpdatePhaseRef.current !== "restarting") {
            // The outcome notice reports the result; the old version was already replaced.
            appUpdateFinishingRef.current = false;
            appUpdatePhaseRef.current = null;
            activeActionRef.current = "";
            setBusy(false);
            return;
          }
          appUpdateFinishingRef.current = false;
          if (code === 0 && (appUpdatePhaseRef.current === "installer" || appUpdatePhaseRef.current === "restarting")) {
            appUpdateHandoffRef.current = true;
            activeActionRef.current = "app-update";
            setBusy(true, "Finish the CARE Clinic update");
            return;
          }
          appUpdatePhaseRef.current = null;
          appUpdateHandoffRef.current = false;
          activeActionRef.current = "";
          setBusy(false);
          if (code !== 0 && flowRef.current !== "role") {
            toast.error("CARE Clinic couldn't finish updating. Your current version was kept. Try again.");
          }
          return;
        }
        if (label === "uninstall" && (flowRef.current === "panel" || flowRef.current === "remove")) {
          if (code === 0) {
            uninstallEndedRef.current = true;
            finishUninstall();
          } else {
            uninstalledRef.current = false;
            uninstallEndedRef.current = false;
            removeAppRef.current = false;
            activeActionRef.current = "";
            setBusy(false);
            const error = describeOperationError("uninstall", lastErrorRef.current);
            setOperationError(error);
            if (flowRef.current === "remove") toast.error(error.title);
            void refreshService();
          }
          return;
        }
        if (flowRef.current === "role" || flowRef.current === "client") return;
        if (flowRef.current === "remove") {
          setBusy(false);
          if (code !== 0) {
            toast.error(describeOperationError("uninstall", lastErrorRef.current).message);
          }
          return;
        }
        if (flowRef.current !== "panel") {
          if (label === "setup" && activeActionRef.current === "setup") {
            setupEndedRef.current = true;
            if (code !== 0) {
              activeActionRef.current = "";
              setBusy(false);
              log(`\n× Setup failed (exit ${code}).`);
              failInstall();
            } else if (runRef.current.finished) {
              activeActionRef.current = "";
              setBusy(false);
              openPanel();
            }
          }
          return;
        }
        if (activeActionRef.current && label !== activeActionRef.current) return;
        log(`— done (exit ${code}) —`);
        if (code !== 0) {
          setOperationError(describeOperationError(label ?? "operation", lastErrorRef.current));
        }
        activeActionRef.current = "";
        setBusy(false);
        void refreshService();
      }),
      onCareEvent("setup-done", () => {
        if (activeActionRef.current !== "setup") return;
        const current = runRef.current;
        setRun({ ...current, finished: true });
        if (setupEndedRef.current) {
          activeActionRef.current = "";
          setBusy(false);
          openPanel();
        }
      }),
      onCareEvent("uninstalled", () => {
        if (flowRef.current !== "panel" && flowRef.current !== "remove") return;
        uninstalledRef.current = true;
        finishUninstall();
      }),
    ];
    return () => unsubscribes.forEach((off) => off?.());
  }, [failInstall, finishUninstall, log, logFromHost, openPanel, refreshService, setBusy, setRun]);

  // --- boot -------------------------------------------------------------
  useEffect(() => {
    if (bootStartedRef.current) return;
    bootStartedRef.current = true;
    void (async () => {
      try {
        const state = await bridge.GetState();
        setVersion(state.version);
        setPlatform(state.platform);
        setMdnsName(state.mdns_name || "care.local");
        setClientURL(state.client_url || "");
        if (await bridge.UninstallRequested()) {
          setFlow("remove");
          return;
        }
        if (state.role === "client") {
          setFlow("client");
        } else if (state.role === "server" && state.setup_done) {
          setFlow("panel");
          await bootPanel();
        } else if (state.role === "server") {
          setFlow("setup");
        }
      } catch (e) {
        setBootError(new Error(errorText(e)));
      } finally {
        setReady(true);
      }
    })();
    // Boot runs once; bootPanel is stable for the life of the provider.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const value = useMemo<CareStore>(
    () => ({
      ready,
      flow,
      mdnsName,
      clientURL,
      selectRole,
      clearRole,
      run,
      setupReset,
      startInstall,
      resumeInstall,
      retryInstall,
      restartSetup,
      openPanel,
      tab,
      setTab,
      busy,
      busyLabel,
      operationError,
      clearOperationError,
      service,
      refreshService,
      updateChecks,
      reloadUpdateChecks,
      saveUpdateChecks,
      runAppUpdate,
      acknowledgeAppUpdate,
      version,
      platform,
      runAction,
      runJob,
      uninstall,
      log,
    }),
    [
      ready, flow, mdnsName, clientURL, selectRole, clearRole,
      run, setupReset, startInstall, resumeInstall, retryInstall, restartSetup, openPanel,
      tab, busy, busyLabel, operationError, clearOperationError, service, refreshService,
      updateChecks, reloadUpdateChecks, saveUpdateChecks, runAppUpdate, acknowledgeAppUpdate, version, platform,
      runAction, runJob, uninstall, log,
    ],
  );

  if (bootError) throw bootError;
  return <CareContext.Provider value={value}>{children}</CareContext.Provider>;
}
