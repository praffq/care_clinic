import { useCallback, useEffect, useRef, useState } from "react";

import { Callout, isClinicName, LogButton } from "@/components/onboarding";
import { Spinner } from "@/components/spinner";
import { Button } from "@/components/ui/button";
import { useAppUpdate, type AppUpdateController } from "@/hooks/use-app-update";
import { usePasswordStrength } from "@/hooks/use-password-strength";
import { bridge } from "@/lib/bridge";
import { errorText, normaliseHost } from "@/lib/format";
import { useCare } from "@/state/care-store";
import type { SetupForm } from "@/state/forms";
import type { BackupSpace, HostReport, SetupPage } from "@/types";
import { AddressStep, AdminStep, BackupStep, ComputerStep, ReviewStep, type AddressResult, type SetupIssue } from "./configuration-steps";
import { SetupLayout } from "./setup-layout";
import { backupProblem, EMPTY_BACKUP_RECOVERY, installServiceProblem, recoveryProblem } from "./setup-model";

const STEPS: SetupPage[] = ["computer", "address", "backup", "admin", "review", "install"];

export function SetupScreen({ form, patch }: { form: SetupForm; patch: (values: Partial<SetupForm>) => void }) {
  const { clearRole, log } = useCare();
  const [status, setStatus] = useState<"starting" | "ready" | "failed">("starting");
  const [attempt, setAttempt] = useState(0);
  const [localBusy, setLocalBusy] = useState(false);
  const busyRef = useRef(true);
  const pending = useRef<Promise<void> | null>(null);
  const update = useAppUpdate(localBusy || status !== "ready", status === "ready", () => busyRef.current);
  const onBusy = useCallback((value: boolean) => { busyRef.current = value; setLocalBusy(value); }, []);
  useEffect(() => {
    let live = true;
    pending.current ??= bridge.BeginServerSetup();
    void pending.current.then(
      () => { if (live) setStatus("ready"); },
      (e) => { log(`begin setup: ${errorText(e)}`); if (live) setStatus("failed"); },
    );
    return () => { live = false; };
  }, [attempt, log]);
  if (status === "ready") return <SetupWizard form={form} patch={patch} update={update} onBusy={onBusy} />;
  return <SetupLayout steps={STEPS} page="computer" done={{}} working={status === "starting" || localBusy}
    title="Checking this computer" subtitle="Preparing this computer for CARE." note={status === "starting" ? "Preparing setup…" : "Try again before continuing."}
    back={() => { setLocalBusy(true); void clearRole().finally(() => setLocalBusy(false)); }} update={update}>
    {status === "starting" ? <div className="on-card on-pad on-row" role="status"><Spinner />Preparing setup…</div>
      : <Callout tone="danger" title="Couldn't start setup">CARE Clinic couldn't prepare setup on this computer. Try again. If it keeps happening, share the log file.
        <div className="on-actions"><Button disabled={localBusy} onClick={() => { pending.current = null; setStatus("starting"); setAttempt((n) => n + 1); }}>Try again</Button><LogButton /></div>
      </Callout>}
  </SetupLayout>;
}

function SetupWizard({ form, patch, update, onBusy }: {
  form: SetupForm; patch: (values: Partial<SetupForm>) => void; update: AppUpdateController; onBusy: (busy: boolean) => void;
}) {
  const care = useCare();
  const [page, setPage] = useState<SetupPage>("computer");
  const pageRef = useRef<SetupPage>("computer");
  const [editing, setEditing] = useState(false);
  const editingRef = useRef(false);
  const [done, setDone] = useState<Partial<Record<SetupPage, boolean>>>({});
  const [operation, setOperation] = useState("preparing");
  const workRef = useRef(false);
  const [problem, setProblem] = useState("");
  const [report, setReport] = useState<HostReport | null>(null);
  const reportRef = useRef<HostReport | null>(null);
  const [serviceProblem, setServiceProblem] = useState("");
  const [passwordError, setPasswordError] = useState("");
  const [address, setAddress] = useState<AddressResult>({ name: "", state: "waiting", message: "" });
  const [addressPending, setAddressPending] = useState(0);
  const [addressQueued, setAddressQueued] = useState(false);
  const addressQueuedRef = useRef(false);
  const addressSequence = useRef(0);
  const addressTimer = useRef(0);
  const addressTasks = useRef(new Set<Promise<boolean>>());
  const addressWrite = useRef(Promise.resolve());
  const [recovery, setRecovery] = useState(EMPTY_BACKUP_RECOVERY);
  const [recoveryError, setRecoveryError] = useState("");
  const [space, setSpace] = useState<BackupSpace | null>(null);
  const [folderError, setFolderError] = useState("");
  const [issues, setIssues] = useState<SetupIssue[]>([]);
  const [verified, setVerified] = useState(false);
  const formRef = useRef(form);
  formRef.current = form;
  const mounted = useRef(false);
  const initialise = useRef<Promise<void> | null>(null);
  const strength = usePasswordStrength(form.adminPassword);
  const busy = operation !== "" || addressQueued || addressPending > 0 || care.busy;
  const locked = busy || update.active;
  const computerReady = report?.ready === true;
  const adminReady = strength.strong && !!form.adminConfirm && form.adminPassword === form.adminConfirm && !passwordError;
  const backupReady = !!space && !folderError && recovery.backup_verified && !recovery.backup_problem && !recoveryError;
  const addressReady = address.state === "ready" && address.name === normaliseHost(form.hostInput) && isClinicName(form.hostInput);
  const syncBusy = useCallback(() => {
    onBusy(workRef.current || addressQueuedRef.current || addressTasks.current.size > 0);
  }, [onBusy]);

  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
      addressSequence.current++;
      window.clearTimeout(addressTimer.current);
    };
  }, []);

  const markDone = (step: SetupPage, value: boolean) => setDone((prev) => ({ ...prev, [step]: value }));
  const changeForm = (values: Partial<SetupForm>) => {
    formRef.current = { ...formRef.current, ...values };
    patch(values);
    setVerified(false);
    if (values.adminPassword !== undefined || values.adminConfirm !== undefined) setPasswordError("");
  };

  const loadRecovery = async () => {
    const status = await bridge.GetSetupBackupStatus();
    if (mounted.current) { setRecovery(status); setRecoveryError(""); }
    return status;
  };

  const checkFolder = async (dir: string) => {
    try {
      const [raw, result] = await Promise.all([bridge.ValidateBackupDir(dir), bridge.BackupDirSpace(dir)]);
      if (raw) care.log(`backup location: ${raw}`);
      if (mounted.current) { setFolderError(backupProblem(raw)); setSpace(result); }
      return raw === "";
    } catch (e) {
      if (mounted.current) { setSpace(null); setFolderError("That location couldn't be checked. Check it again, or choose a different location."); }
      throw e;
    }
  };

  const checkComputer = async () => {
    const next = await bridge.HostChecks();
    reportRef.current = next;
    if (mounted.current) setReport(next);
    markDone("computer", next.ready);
    return next.ready;
  };

  const checkAddress = useCallback((raw: string): Promise<boolean> => {
    const sequence = ++addressSequence.current;
    const name = normaliseHost(raw);
    onBusy(true);
    setAddress({ name, state: "checking", message: "Checking whether this name is free on your network…" });
    setAddressPending((n) => n + 1);
    const task = (async () => {
      const current = () => mounted.current && sequence === addressSequence.current;
      try {
        if (!isClinicName(raw)) {
          if (current()) setAddress({ name, state: "bad", message: "Use a name of 1 to 63 letters, numbers or hyphens. Start and end with a letter or number." });
          return false;
        }
        const invalid = await bridge.ValidateDomain(raw.trim());
        if (!current()) return false;
        if (invalid) {
          care.log(`clinic address: ${invalid}`);
          setAddress({ name, state: "bad", message: "Choose a name with letters, numbers or hyphens. Start and end with a letter or number." });
          return false;
        }
        const result = await bridge.MDNSStatus(name);
        if (!current()) return false;
        if (!result.ok) {
          care.log(`clinic address: ${result.message}`);
          const taken = /already in use|already taken/i.test(result.message);
          setAddress({ name, state: taken ? "bad" : "failed", message: taken
            ? `${name} is already taken on this network — type a different name.`
            : "Couldn't check this name on the network. Keep this computer connected and check again." });
          return false;
        }
        const saved = addressWrite.current.then(async () => {
          if (!current()) return false;
          await bridge.SetMDNSName(name);
          return current();
        });
        addressWrite.current = saved.then(() => {}, () => {});
        if (!(await saved)) return false;
        setAddress({ name, state: "ready", message: `${name} is free to use on this network.` });
        return true;
      } catch (e) {
        care.log(`clinic address: ${errorText(e)}`);
        if (current()) setAddress({ name, state: "failed", message: "Couldn't check or save this clinic address. Try again, or share the log file with your support contact." });
        return false;
      } finally { if (mounted.current) setAddressPending((n) => n - 1); }
    })();
    addressTasks.current.add(task);
    void task.then(() => { addressTasks.current.delete(task); syncBusy(); });
    return task;
  }, [care.log, onBusy, syncBusy]);

  const validateReview = async () => {
    setVerified(false);
    const latest = formRef.current;
    const result: SetupIssue[] = [];
    if (!(await checkComputer())) result.push({ step: "computer", message: "This computer isn't ready yet. Check the items marked as needing attention." });
    if (!isClinicName(latest.hostInput) || (await bridge.ValidateDomain(latest.hostInput.trim()))) {
      result.push({ step: "address", message: "Choose a valid clinic name before installing." });
    }
    const folderOk = await checkFolder(latest.backupDir);
    const status = await loadRecovery();
    if (!folderOk) result.push({ step: "backup", message: "Choose a backup location that is available and has room." });
    else if (!status.backup_verified || status.backup_problem) result.push({ step: "backup", message: "Save and check the backup recovery file before installing." });
    const invalid = await bridge.ValidatePassword(latest.adminPassword);
    if (invalid) {
      care.log(`admin password: ${invalid}`);
      result.push({ step: "admin", message: "Choose a stronger admin password before installing." });
    } else if (latest.adminPassword !== latest.adminConfirm) {
      result.push({ step: "admin", message: "Both passwords must match before installing." });
    }
    setIssues(result);
    setVerified(true);
    for (const issue of result) markDone(issue.step, false);
    return result;
  };

  const nextAfter = (from: SetupPage) => STEPS[STEPS.indexOf(from) + 1] ?? "review";

  const visit = async (target: SetupPage, mode: "forward" | "back" | "edit" = "forward") => {
    setProblem("");
    if (!mounted.current) return;
    pageRef.current = target;
    editingRef.current = mode === "edit" && target !== "review";
    setPage(target);
    setEditing(editingRef.current);
    if (target === "computer") { await checkComputer(); return; }
    if (target === "address") { await checkAddress(formRef.current.hostInput); return; }
    if (target === "backup") { await loadRecovery(); await checkFolder(formRef.current.backupDir); return; }
    if (target === "review") { await validateReview(); return; }
  };

  const execute = async (name: string, fn: () => Promise<void>, recoveryAction = false) => {
    if (workRef.current || addressQueuedRef.current || addressTasks.current.size || update.isActive() || care.busy) return;
    workRef.current = true;
    onBusy(true);
    setOperation(name);
    setProblem("");
    if (recoveryAction) setRecoveryError("");
    try { await fn(); } catch (e) {
      care.log(`setup ${name}: ${errorText(e)}`);
      if (name === "verify-backup") setRecovery((previous) => ({ ...previous, backup_verified: false }));
      if (recoveryAction) setRecoveryError(recoveryProblem(errorText(e)));
      else setProblem("This step couldn't finish. Try again. If it keeps happening, share the log file with your support contact.");
    } finally {
      workRef.current = false;
      syncBusy();
      if (mounted.current) setOperation("");
    }
  };

  useEffect(() => {
    // One bootstrap across StrictMode effect replay.
    initialise.current ??= (async () => {
      workRef.current = true;
      try {
        await visit("computer");
      } catch (e) {
        care.log(`prepare setup: ${errorText(e)}`);
        setProblem("Couldn't check this computer. Check again before continuing.");
      } finally { workRef.current = false; syncBusy(); if (mounted.current) setOperation(""); }
    })();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const recheck = () => void execute("checking", async () => {
    setServiceProblem("");
    await checkComputer();
  });

  const installService = () => void execute("install-service", async () => {
    setServiceProblem("");
    try {
      await bridge.InstallService();
    } catch (e) {
      const text = errorText(e);
      care.log(`install service: ${text}`);
      setServiceProblem(installServiceProblem(text));
    }
    await checkComputer();
  });

  const changeHost = (value: string) => {
    changeForm({ hostInput: value });
    addressQueuedRef.current = true;
    onBusy(true);
    setAddressQueued(true);
    addressSequence.current++;
    window.clearTimeout(addressTimer.current);
    setAddress({ name: normaliseHost(value), state: "checking", message: "Checking whether this name is free on your network…" });
    markDone("address", false);
    addressTimer.current = window.setTimeout(() => {
      addressQueuedRef.current = false;
      setAddressQueued(false);
      void checkAddress(value);
    }, 350);
  };

  const goBack = () => void execute("back", async () => {
    window.clearTimeout(addressTimer.current);
    await Promise.all(addressTasks.current);
    if (editingRef.current) { await visit("review"); return; }
    const index = STEPS.indexOf(pageRef.current);
    if (index <= 0) await care.clearRole();
    else await visit(STEPS[index - 1], "back");
  });

  const continueSetup = () => void execute("continue", async () => {
    window.clearTimeout(addressTimer.current);
    const target = pageRef.current;
    const latest = formRef.current;
    if (target === "review") {
      if ((await validateReview()).length) return;
      try {
        await care.startInstall({
          host: normaliseHost(latest.hostInput),
          adminPassword: latest.adminPassword,
          backupDir: latest.backupDir.trim(),
          pages: STEPS,
        });
      } catch (e) {
        care.log(`start installation: ${errorText(e)}`);
        const failures = await validateReview();
        if (!failures.length) setProblem("Installation hasn't started. CARE couldn't accept the request. Wait a moment and try again, or share the log file.");
      }
      return;
    }
    let ok = false;
    if (target === "computer") ok = await checkComputer();
    else if (target === "address") ok = await checkAddress(latest.hostInput);
    else if (target === "backup") {
      const folderOk = await checkFolder(latest.backupDir);
      const status = await loadRecovery();
      ok = folderOk && status.backup_verified && !status.backup_problem;
      if (!ok && folderOk) setRecoveryError("Save and check the correct recovery file before continuing.");
    }
    else if (target === "admin") {
      const invalid = await bridge.ValidatePassword(latest.adminPassword);
      ok = !invalid && latest.adminPassword === latest.adminConfirm;
      if (!ok) setPasswordError("Choose and confirm a valid password before continuing.");
    }
    markDone(target, ok);
    if (ok) await visit(editingRef.current ? "review" : nextAfter(target));
  });

  const edit = (target: SetupPage) => void execute("edit", () => visit(target, "edit"));

  const chooseFolder = () => void execute("choose-folder", async () => {
    const chosen = await bridge.ChooseFolder("Choose backup folder");
    if (!chosen) return;
    changeForm({ backupDir: chosen });
    setSpace(null);
    markDone("backup", false);
    await checkFolder(chosen);
  });

  const saveRecovery = (kind: "save-backup" | "verify-backup" | "replace-backup") => void execute(kind, async () => {
    const latest = formRef.current;
    if (!(await checkFolder(latest.backupDir))) return;
    const changed = kind === "save-backup" ? await bridge.SaveSetupBackupRecovery(latest.backupDir)
      : kind === "replace-backup" ? await bridge.ReplaceSetupBackupRecovery(latest.backupDir)
      : await bridge.VerifySetupBackupRecovery(latest.backupDir);
    if (changed) { setVerified(false); markDone("backup", false); await loadRecovery(); }
  }, true);

  const reloadRecovery = () => void execute("read-recovery", async () => { await loadRecovery(); }, true);

  const titles: Record<SetupPage, [string, string]> = {
    computer: ["Checking this computer", "CARE runs in the background on this computer. Make sure it has what the clinic needs."],
    address: ["Choosing the clinic address", "Staff type this into their browser to open CARE. Short and easy to say out loud works best."],
    backup: ["Setting up backups", "CARE makes an encrypted backup automatically, once every 24 hours. Choose where the backups go, then save the file that unlocks them."],
    admin: ["Creating the admin password", "This password protects CARE Clinic on this computer and is the first sign-in for CARE itself."],
    review: ["Review before installing", "Here's what CARE will set up."],
    install: ["Installing CARE", ""],
  };
  const ready = page === "computer" ? computerReady
    : page === "address" ? addressReady : page === "backup" ? backupReady : page === "admin" ? adminReady
    : verified && issues.length === 0;
  const note = busy ? operation === "install-service" ? "Installing the CARE Clinic service…" : page === "review" ? "Checking everything before installation…" : "Checking…"
    : problem ? "Check this step again before continuing."
    : editing ? "Choose Continue to return to Review when this step is ready."
    : page === "computer" ? computerReady ? "This computer is ready." : "Fix the computer checks to continue."
    : page === "address" ? addressReady ? "Next: backups." : "Pick an address that is free to continue."
    : page === "backup" ? backupReady ? "Next: the admin password." : "Save and check your recovery file to continue."
    : page === "admin" ? adminReady ? "Almost done — one last look before installing." : "Choose and confirm a password to continue."
    : issues.length ? "Fix the highlighted step; you come back to this screen." : "Nothing has been installed yet.";
  const blocked = issues.map((issue) => issue.step);
  if (page === "computer" && report && !report.ready) blocked.push("computer");

  return (
    <SetupLayout steps={STEPS} page={page} done={done} blocked={blocked} working={busy}
      title={titles[page][0]} subtitle={titles[page][1]} note={note} back={page === "review" ? undefined : goBack}
      next={continueSetup} nextDisabled={!ready} editing={editing} update={update}>
      {page === "computer" ? <ComputerStep report={report} problem={serviceProblem} busy={locked} installing={operation === "install-service"}
        onInstallService={installService} onCheck={recheck} />
        : page === "address" ? <AddressStep value={form.hostInput} result={address} disabled={operation !== "" || update.active || care.busy} onChange={changeHost} onCheck={() => void checkAddress(formRef.current.hostInput)} />
        : page === "backup" ? <BackupStep form={form} space={space} folderProblem={folderError} recovery={recovery} recoveryError={recoveryError} busy={locked} action={operation}
          onOpenFolder={() => void execute("open-backup-folder", () => bridge.OpenSetupBackupRecoveryFolder(), true)}
          onChoose={chooseFolder} onCheck={() => void execute("check-folder", async () => { await checkFolder(formRef.current.backupDir); })}
          onSave={() => saveRecovery("save-backup")} onVerify={() => saveRecovery("verify-backup")} onReplace={() => saveRecovery("replace-backup")} onReload={reloadRecovery} />
        : page === "admin" ? <AdminStep form={form} patch={changeForm} strength={strength} passwordError={passwordError} busy={locked} />
        : <ReviewStep steps={STEPS} form={form} report={report} backupPath={space?.dir || form.backupDir} issues={issues} verified={verified} busy={locked} onEdit={edit} />}
      {problem ? <Callout tone="danger" title={page === "review" ? "Nothing has been installed yet" : "This step couldn't finish"}>{problem}
        <div className="on-actions"><Button disabled={locked} onClick={() => void execute("checking", () => visit(pageRef.current, editingRef.current ? "edit" : "back"))}>Try again</Button><LogButton /></div>
      </Callout> : null}
    </SetupLayout>
  );
}
