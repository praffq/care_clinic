import { Check, KeyRound } from "lucide-react";
import { useCallback, useEffect, useId, useRef, useState } from "react";

import { Spinner } from "@/components/spinner";
import {
  AlertDialog, AlertDialogContent, AlertDialogDescription, AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Button } from "@/components/ui/button";
import { usePasswordStrength } from "@/hooks/use-password-strength";
import { bridge } from "@/lib/bridge";
import { useCare } from "@/state/care-store";
import {
  AdvancedError, AdvancedNotice, AdvancedSecretInput, advancedProblem, useAdvancedLock, type AdvancedProblem,
} from "./advanced-ui";

function AdminPasswordForm({
  currentPassword, onSuccess, onCancel, onWorkingChange,
}: {
  currentPassword: string;
  onSuccess: (password: string) => void;
  onCancel: () => void;
  onWorkingChange?: (working: boolean) => void;
}) {
  const { busy } = useCare();
  const lock = useAdvancedLock();
  const id = useId();
  const live = useRef(true);
  const pending = useRef(false);
  const [password, setPassword] = useState("");
  const [confirm, setConfirm] = useState("");
  const [working, setWorking] = useState(false);
  const [problem, setProblem] = useState<AdvancedProblem | null>(null);
  const [retryAt, setRetryAt] = useState(0);
  const [now, setNow] = useState(Date.now());
  const strength = usePasswordStrength(password);
  const waitSeconds = Math.max(0, Math.ceil((retryAt - now) / 1000));
  const locked = busy || lock.disabled || working;
  const canSubmit = !locked && waitSeconds === 0 && strength.strong && password === confirm;

  useEffect(() => {
    live.current = true;
    return () => { live.current = false; };
  }, []);
  useEffect(() => {
    if (!retryAt) return;
    const timer = window.setInterval(() => setNow(Date.now()), 1000);
    return () => window.clearInterval(timer);
  }, [retryAt]);

  const clear = () => { setPassword(""); setConfirm(""); };
  const cancel = () => {
    if (pending.current) return;
    clear();
    setProblem(null);
    onCancel();
  };
  const submit = async () => {
    if (!canSubmit || lock.isLocked() || pending.current) return;
    pending.current = true;
    setWorking(true);
    onWorkingChange?.(true);
    setProblem(null);
    const nextPassword = password;
    try {
      await bridge.ChangeAdminPassword(currentPassword, nextPassword);
      if (!live.current) return;
      clear();
      onSuccess(nextPassword);
    } catch (cause) {
      if (!live.current) return;
      const next = advancedProblem(cause, "The password couldn't be changed",
        "Nothing was confirmed. Try again, or open the log file for support.");
      setProblem(next);
      if (next.retryAfter) {
        const time = Date.now();
        setNow(time);
        setRetryAt(time + next.retryAfter * 1000);
      }
      clear();
    } finally {
      pending.current = false;
      if (live.current) { setWorking(false); onWorkingChange?.(false); }
    }
  };
  const rules = [
    { label: "8–20 characters", met: [...password].length >= 8 && [...password].length <= 20 },
    { label: "Uppercase", met: /\p{Lu}/u.test(password) },
    { label: "Lowercase", met: /\p{Ll}/u.test(password) },
    { label: "A number", met: /\p{Nd}/u.test(password) },
  ];

  return <form className="advanced-admin-form" noValidate onSubmit={(event) => { event.preventDefault(); void submit(); }}>
    <p>Your CARE web login won't change.</p>
    <fieldset disabled={locked}>
      <AdvancedSecretInput id={`${id}-password`} label="New CARE Clinic admin password" value={password}
        onChange={setPassword} disabled={locked} autoComplete="new-password" autoFocus
        hint={<div className="advanced-password-rules">
          {rules.map((rule) => <span key={rule.label} className={`advanced-password-rule${rule.met ? " is-met" : ""}`}>
            <Check aria-hidden="true" />{rule.label}
          </span>)}
        </div>} />
      {password && !strength.strong ? <p className="advanced-field-hint" role="status">
        {strength.message}
      </p> : null}
      <AdvancedSecretInput id={`${id}-confirm`} label="Confirm new CARE Clinic admin password" value={confirm}
        onChange={setConfirm} disabled={locked} autoComplete="new-password"
        invalid={!!confirm && password !== confirm}
        hint={confirm && password !== confirm ? <span className="advanced-field-error">The passwords don't match. Type the same password again.</span> : undefined} />
    </fieldset>
    <AdvancedError problem={problem} />
    {waitSeconds > 0 ? <p className="advanced-field-hint" role="status">
      Try again in {waitSeconds} {waitSeconds === 1 ? "second" : "seconds"}.
    </p> : null}
    <div className="advanced-form-footer">
      <Button type="button" disabled={working} onClick={cancel}>Cancel</Button>
      <Button type="submit" variant="primary" disabled={!canSubmit}>
        {working ? <Spinner /> : null}
        {working ? "Saving…" : "Change CARE Clinic password"}
      </Button>
    </div>
  </form>;
}

export function AdminPasswordSettings({
  adminPassword, onPasswordChanged,
}: {
  adminPassword: string;
  onPasswordChanged: (password: string) => void;
}) {
  const { busy } = useCare();
  const lock = useAdvancedLock();
  const blocked = busy || lock.disabled;
  const [open, setOpen] = useState(false);
  const [working, setWorking] = useState(false);
  const [notice, setNotice] = useState("");
  const pending = useRef(false);
  const triggerRef = useRef<HTMLButtonElement>(null);
  const passwordWorking = useCallback((next: boolean) => {
    pending.current = next;
    setWorking(next);
  }, []);
  const close = () => {
    if (working || pending.current) return;
    setOpen(false);
  };

  return <>
    <section className="advanced-card advanced-card-pad" aria-labelledby="advanced-password-title">
      <div className="advanced-card-row">
        <span className="advanced-icon advanced-icon-brand"><KeyRound aria-hidden="true" /></span>
        <div className="advanced-grow">
          <h2 className="advanced-card-title" id="advanced-password-title">Admin password</h2>
          <p className="advanced-card-description">Change the CARE Clinic admin password. CARE web passwords are separate.</p>
        </div>
        <div className="advanced-actions">
          <Button type="button" ref={triggerRef} disabled={blocked || working}
            onClick={() => { if (!busy && !lock.isLocked() && !pending.current) { setNotice(""); setOpen(true); } }}>Change password</Button>
        </div>
      </div>
    </section>
    {notice ? <AdvancedNotice title={notice} tone="success" /> : null}
    <AlertDialog open={open} onOpenChange={(next) => { if (!next) close(); }}>
      <AlertDialogContent className="advanced-dialog advanced-dialog-narrow"
        onCloseAutoFocus={(event) => { event.preventDefault(); triggerRef.current?.focus(); }}
        onEscapeKeyDown={(event) => { if (working || pending.current) event.preventDefault(); }}>
        <AlertDialogTitle>Change the CARE Clinic admin password</AlertDialogTitle>
        <AlertDialogDescription>Staff sign-in to the CARE website stays the same.</AlertDialogDescription>
        <div className="advanced-dialog-body">
          <AdminPasswordForm currentPassword={adminPassword}
            onWorkingChange={passwordWorking} onCancel={close} onSuccess={(password) => {
              pending.current = false;
              setWorking(false);
              onPasswordChanged(password);
              setOpen(false);
              setNotice("CARE Clinic password changed. Your CARE web login is unchanged.");
            }} />
        </div>
      </AlertDialogContent>
    </AlertDialog>
  </>;
}
