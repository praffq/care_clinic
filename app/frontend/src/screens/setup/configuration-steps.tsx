import { AlertTriangle, Check, CheckCircle2, Cpu, Download, Globe, KeyRound, Lock, Minus, RefreshCw, X } from "lucide-react";
import { useState } from "react";

import { Callout, ClinicAddressInput, LogButton, StatusBadge } from "@/components/onboarding";
import { Spinner } from "@/components/spinner";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import type { PasswordStrength } from "@/hooks/use-password-strength";
import { cn } from "@/lib/utils";
import type { SetupForm } from "@/state/forms";
import type { HostCheck, HostReport, SetupPage } from "@/types";

export type AddressResult = { name: string; state: "waiting" | "checking" | "ready" | "bad" | "failed"; message: string };

export function AddressStep({ value, result, disabled, onChange, onCheck }: {
  value: string; result: AddressResult; disabled: boolean;
  onChange: (value: string) => void; onCheck: () => void;
}) {
  const invalid = result.state === "bad" || result.state === "failed";
  return (
    <>
      <section className="on-card on-pad">
        <div className="on-field">
          <label htmlFor="mdnsname">Clinic address</label>
          <ClinicAddressInput id="mdnsname" prefix value={value} invalid={invalid} disabled={disabled}
            aria-describedby="setup-address-hint" onValueChange={onChange} />
          <p id="setup-address-hint" className={cn("on-hint", result.state === "ready" ? "on-success" : invalid ? "on-error" : "")} role="status">
            {result.state === "ready" ? <CheckCircle2 aria-hidden="true" /> : result.state === "checking" ? <Spinner /> : null}
            {result.message || "Checking whether this name is free on your network…"}
          </p>
        </div>
        {invalid ? <div className="on-actions"><Button disabled={disabled} onClick={onCheck}><RefreshCw aria-hidden="true" />Check again</Button>{result.state === "failed" ? <LogButton /> : null}</div> : null}
      </section>
      {result.state === "ready" ? <Callout title="Staff will open"><span className="on-mono">https://{result.name}</span></Callout> : null}
    </>
  );
}

export type SetupIssue = { step: SetupPage; message: string };

function checkTone(level: HostCheck["level"]): "ok" | "warn" | "bad" {
  return level === "ok" ? "ok" : level === "warn" ? "warn" : "bad";
}

export function ComputerStep({ report, problem, busy, installing, onInstallService, onCheck }: {
  report: HostReport | null; problem: string; busy: boolean; installing: boolean;
  onInstallService: () => void; onCheck: () => void;
}) {
  return (
    <>
      <section className="on-card" aria-label="Computer checks">
        {report === null ? <div className="on-data-row" role="status"><Spinner />Checking this computer…</div>
          : report.checks.map((check) => <div key={check.id} className={cn("on-data-row", check.level === "fail" && "on-invalid")} data-check={check.id}>
            <span className={cn("on-tile", check.level === "ok" && "on-solid", check.level === "fail" && "on-bad")} aria-hidden="true">
              {check.level === "ok" ? <Check /> : check.level === "warn" ? <AlertTriangle /> : <X />}
            </span>
            <div className="on-grow"><strong>{check.title}</strong>{check.detail ? <p>{check.detail}</p> : null}</div>
            <StatusBadge tone={checkTone(check.level)}>{check.level === "ok" ? "Ready" : check.level === "warn" ? "Check" : "Needs attention"}</StatusBadge>
            {check.action === "install-service" ? <Button variant="primary" disabled={busy} onClick={onInstallService}>
              {installing ? <Spinner /> : <Download aria-hidden="true" />}{installing ? "Installing service…" : "Install service"}
            </Button> : null}
          </div>)}
      </section>
      {installing ? <Callout title="Installing the CARE Clinic service">Approve the request if your computer asks for permission. This can take a minute.</Callout> : null}
      {problem ? <Callout tone="danger" title="That didn't finish">{problem}<div className="on-actions"><LogButton /></div></Callout> : null}
      <div className="on-actions"><Button disabled={busy} onClick={onCheck}><RefreshCw aria-hidden="true" />Re-check</Button></div>
      {report && !report.ready && !problem ? <p className="on-small" role="status">Fix the items marked as needing attention, then choose Re-check.</p> : null}
    </>
  );
}

export function AdminStep({ form, patch, strength, passwordError, busy }: {
  form: SetupForm; patch: (form: Partial<SetupForm>) => void; strength: PasswordStrength;
  passwordError: string; busy: boolean;
}) {
  const [show, setShow] = useState(false);
  const length = [...form.adminPassword].length;
  const rules = [
    { label: "8 to 20 characters", ok: length >= 8 && length <= 20 },
    { label: "Upper and lowercase letter", ok: /\p{Lu}/u.test(form.adminPassword) && /\p{Ll}/u.test(form.adminPassword) },
    { label: "A number", ok: /\p{Nd}/u.test(form.adminPassword) },
  ];
  const matching = !!form.adminConfirm && form.adminConfirm === form.adminPassword;
  return (
    <section className="on-card on-pad on-password-card">
      <div className="on-row" style={{ flexWrap: "wrap" }}><span className="on-eyebrow">Username</span><StatusBadge><span className="on-mono">admin</span></StatusBadge><span className="on-small">You can add more staff logins inside CARE later.</span></div>
      <div className="on-field">
        <label htmlFor="adminpw">Password</label>
        <div className="on-input"><Lock aria-hidden="true" /><Input className="on-text-input" id="adminpw" type={show ? "text" : "password"} autoComplete="new-password" placeholder="Choose a password" value={form.adminPassword} disabled={busy} aria-describedby="password-rules password-verdict" onChange={(e) => patch({ adminPassword: e.target.value })} />
          <Button variant="ghost" aria-label={show ? "Hide passwords" : "Show passwords"} aria-pressed={show} disabled={busy} onClick={() => setShow((v) => !v)}>{show ? "Hide" : "Show"}</Button>
        </div>
        <div id="password-rules">
          <div className="on-strength" aria-hidden="true">{rules.map((rule) => <span key={rule.label} className={rule.ok ? "on-filled" : ""} />)}</div>
          <div className="on-chips">{rules.map((rule) => <span key={rule.label} className={cn("on-chip", rule.ok && "on-ok")}>{rule.ok ? <Check aria-hidden="true" /> : <Minus aria-hidden="true" />}{rule.label}<span className="sr-only">{rule.ok ? ": met" : ": not met"}</span></span>)}</div>
        </div>
        <p id="password-verdict" className={cn("on-hint", passwordError && "on-error")} role="status">{passwordError || (form.adminPassword && !strength.strong ? strength.message : null)}</p>
      </div>
      <div className="on-field">
        <label htmlFor="adminpw-confirm">Confirm password</label>
        <div className={cn("on-input", form.adminConfirm && !matching && "on-invalid")}><Lock aria-hidden="true" /><Input className="on-text-input" id="adminpw-confirm" type={show ? "text" : "password"} autoComplete="new-password" placeholder="Type it again" value={form.adminConfirm} disabled={busy} aria-invalid={!!form.adminConfirm && !matching} aria-describedby="confirm-verdict" onChange={(e) => patch({ adminConfirm: e.target.value })} /></div>
        <p id="confirm-verdict" role="status" className={cn("on-hint", matching ? "on-success" : "on-error")}>{matching ? <><CheckCircle2 aria-hidden="true" />Both passwords match.</> : form.adminConfirm ? "These don't match. Type the same password again, including capital letters." : null}</p>
      </div>
      <p className="on-small">Save this password in your password manager or write it down somewhere secure before starting installation. It can't be recovered if it's lost.</p>
    </section>
  );
}

export function ReviewStep({ steps, form, report, issues, verified, busy, onEdit }: {
  steps: SetupPage[]; form: SetupForm; report: HostReport | null; issues: SetupIssue[];
  verified: boolean; busy: boolean; onEdit: (page: SetupPage) => void;
}) {
  const passed = report?.checks.filter((check) => check.level !== "fail").length ?? 0;
  const rows = [
    { id: "computer" as const, icon: Cpu, label: "This computer", value: report ? `${passed} of ${report.checks.length} computer checks passed` : "", detail: verified ? "Checked just now" : "Checking before installation" },
    { id: "address" as const, icon: Globe, label: "Clinic address", value: `https://${form.hostInput.trim().toLowerCase()}.local`, detail: "What staff type in their browser" },
    { id: "admin" as const, icon: KeyRound, label: "Admin login", value: "admin", detail: "Password set" },
  ];
  return (
    <>
      {issues.length ? <Callout tone="danger" title={issues.length === 1 ? "One step needs another look" : "Some steps need another look"}>Something changed since you checked. Fix it takes you to that step and brings you straight back here — without walking through the other steps again.</Callout> : null}
      <div className="on-card">
        {rows.map((row) => {
          const failures = issues.filter((issue) => issue.step === row.id);
          return <section key={row.id} className={cn("on-review-row", failures.length > 0 && "on-invalid")} aria-label={row.label}>
            <span className="on-tile"><row.icon aria-hidden="true" /></span><span className="on-review-label">{row.label}</span>
            <div className="on-grow on-review-value"><strong className={row.id !== "computer" ? "on-mono" : ""}>{row.value || "Not yet confirmed"}</strong>
              {failures.length ? failures.map((issue) => <p key={issue.message}>Step {steps.indexOf(issue.step) + 1} · {issue.message}</p>) : <p>{row.detail}</p>}
            </div>
            <Button variant="ghost" disabled={busy} aria-label={`${failures.length ? "Fix" : "Edit"} ${row.label}`} onClick={() => onEdit(row.id)}>{failures.length ? "Fix it" : "Edit"}</Button>
          </section>;
        })}
      </div>
      {verified && !issues.length ? <Callout tone="ok" title="Everything is ready">Installing takes 5 to 20 minutes. Don't quit CARE Clinic or turn the computer off while it runs.</Callout> : null}
    </>
  );
}
