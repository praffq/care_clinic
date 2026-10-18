import { useCallback, useEffect, useRef, useState } from "react";

import { bridge, onCareEvent } from "@/lib/bridge";
import { errorText } from "@/lib/format";
import { useCare } from "@/state/care-store";
import type { AppUpdate, AppUpdateOutcome, AppUpdateProgress } from "@/types";

type UpdateProblem =
  | { kind: "offline" | "check" | "download" | "install" | "location" }
  | { kind: "unavailable"; version: string };

function updateProblem(error: unknown, checking: boolean): UpdateProblem {
  const text = errorText(error);
  if (text.includes("update location unavailable:")) return { kind: "location" };
  const unavailable = /^release (\S+) has no installer for this computer$/.exec(text);
  if (unavailable) return { kind: "unavailable", version: unavailable[1] };
  if (checking) {
    return { kind: /^couldn't reach GitHub to check for updates:/i.test(text) ? "offline" : "check" };
  }
  return {
    kind: /update didn't download properly/i.test(text)
      ? "download"
      : "install",
  };
}

export function useAppUpdate(disabled = false, enabled = true, isBlocked?: () => boolean) {
  const { busy, busyLabel, version, runAppUpdate, acknowledgeAppUpdate, log, updateChecks, reloadUpdateChecks } = useCare();
  const checksLoaded = updateChecks !== null;
  const off = updateChecks?.app === false;
  const offRef = useRef(off);
  offRef.current = off;
  const [update, setUpdate] = useState<AppUpdate | null>(null);
  const [checking, setChecking] = useState(false);
  const [problem, setProblem] = useState<UpdateProblem | null>(null);
  const [progress, setProgress] = useState<AppUpdateProgress | null>(null);
  const [starting, setStarting] = useState(false);
  // Downloaded and verified this session: the next step is "Restart to update".
  const [downloaded, setDownloaded] = useState(false);
  const [outcome, setOutcome] = useState<AppUpdateOutcome | null>(null);
  const mounted = useRef(false);
  const checkPending = useRef(false);
  const installPending = useRef(false);
  const progressRef = useRef<AppUpdateProgress | null>(null);
  const failure = useRef<unknown>(null);
  const finishing = useRef(false);
  const updating = busy && busyLabel === "Updating CARE Clinic";
  const active = starting || updating || progress !== null;
  const isActive = () => installPending.current || progressRef.current !== null || updating;

  const check = useCallback(async () => {
    if (offRef.current || checkPending.current || installPending.current || progressRef.current) return;
    checkPending.current = true;
    setChecking(true);
    setProblem(null);
    setUpdate(null);
    try {
      const result = await bridge.CheckAppUpdate();
      if (mounted.current && !offRef.current) {
        setUpdate(result);
        setDownloaded(result.ready === true);
      }
    } catch (error) {
      log(`check CARE Clinic update: ${errorText(error)}`);
      if (/update checks are turned off/i.test(errorText(error))) void reloadUpdateChecks();
      else if (mounted.current) setProblem(updateProblem(error, true));
    } finally {
      checkPending.current = false;
      if (mounted.current) setChecking(false);
    }
  }, [log, reloadUpdateChecks]);

  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
    };
  }, []);

  useEffect(() => {
    const offProgress = onCareEvent("app-update-progress", (next: AppUpdateProgress) => {
      progressRef.current = next;
      setProgress(next);
      setStarting(false);
    });
    const offError = onCareEvent("care-error", (label: string, detail: string) => {
      if (label === "The CARE Clinic update didn't finish") failure.current = detail;
    });
    const offOutcome = onCareEvent("app-update-outcome", (next: AppUpdateOutcome) => {
      // Stays set until that job's care-done, which follows its final outcome.
      if (next.state === "finishing") finishing.current = true;
      if (mounted.current) setOutcome(next);
    });
    void bridge.GetAppUpdateOutcome().then((next) => {
      if (mounted.current && next.state) setOutcome((seen) => seen ?? next);
    }).catch(() => {});
    const offDone = onCareEvent("care-done", (code: number, label?: string) => {
      if (label !== "app-update") return;
      // A resumed update reports through its outcome, not as a failed install.
      if (finishing.current && !installPending.current) {
        finishing.current = false;
        return;
      }
      installPending.current = false;
      setStarting(false);
      if (code === 0 && progressRef.current?.phase === "ready") {
        progressRef.current = null;
        setProgress(null);
        setDownloaded(true);
        return;
      }
      if (code !== 0) {
        progressRef.current = null;
        setProgress(null);
        setProblem(updateProblem(failure.current, false));
      }
    });
    return () => {
      offProgress();
      offError();
      offOutcome();
      offDone();
    };
  }, []);

  useEffect(() => {
    if (enabled && checksLoaded && !off) void check();
    // Check once on entry, not after every update/busy transition.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [enabled, checksLoaded, off, check]);

  useEffect(() => {
    if (!off || installPending.current || progressRef.current) return;
    setUpdate(null);
    setProblem(null);
  }, [off]);

  const install = async () => {
    if (off || disabled || busy || isBlocked?.() || checkPending.current || installPending.current || progressRef.current) return;
    installPending.current = true;
    failure.current = null;
    setProblem(null);
    setStarting(true);
    try {
      await runAppUpdate(ready ? "restart" : "download");
      // The promise only accepts the job. Events, not its resolution, finish it.
    } catch (error) {
      installPending.current = false;
      if (mounted.current) {
        setStarting(false);
        setProblem(updateProblem(error, false));
      }
    }
  };

  const dismiss = () => {
    if (installPending.current ||
      (progressRef.current && progressRef.current.phase !== "installer") ||
      !acknowledgeAppUpdate()) return;
    progressRef.current = null;
    setProgress(null);
    setProblem(null);
    setUpdate(null);
  };

  const ready = downloaded && update?.available === true;

  /** Retries the steps the relaunched app could not finish (service, clinic health). */
  const finish = async () => {
    try {
      await bridge.FinishAppUpdate();
    } catch (error) {
      log(`finish CARE Clinic update: ${errorText(error)}`);
    }
  };

  return {
    update,
    version: update?.current || version,
    checking,
    problem,
    progress,
    active,
    isActive,
    updating,
    off,
    disabled: disabled || busy,
    ready,
    unsigned: update?.unsigned === true,
    outcome,
    check,
    install,
    finish,
    dismiss,
    dismissOutcome: () => setOutcome(null),
  };
}

export type AppUpdateController = ReturnType<typeof useAppUpdate>;
