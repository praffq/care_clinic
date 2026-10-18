import { Download, Monitor, RotateCw, ScrollText } from "lucide-react";
import { useEffect, useState } from "react";

import { Button } from "@/components/ui/button";
import { Progress } from "@/components/ui/progress";
import type { AppUpdateController } from "@/hooks/use-app-update";
import { bridge } from "@/lib/bridge";
import { megabytes } from "@/lib/format";
import { useCare } from "@/state/care-store";
import { PanelBadge, PanelLogButton, PanelNotice, PanelPageHeader, usePanelTask } from "./panel-ui";

function checkedTime(timestamp: number) {
  return new Date(timestamp).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
}

export function UpdatePanel({ appUpdate }: { appUpdate: AppUpdateController }) {
  const { tab, reloadUpdateChecks } = useCare();
  useEffect(() => { if (tab === "updates") void reloadUpdateChecks(); }, [tab, reloadUpdateChecks]);
  return <div className="panel-page" aria-label="Updates">
    <PanelPageHeader title="Updates" subtitle="Keep CARE Clinic up to date." />
    <UpdateChecksNotice />
    <ClinicApplicationCard controller={appUpdate} />
    <div className="panel-update-foot">
      <p className="panel-small panel-grow">Updates need internet. Your clinic records are kept.</p>
      <PanelLogButton />
    </div>
  </div>;
}

function UpdateChecksNotice() {
  const { updateChecks, setTab } = useCare();
  if (!updateChecks) return null;
  const open = <Button onClick={() => setTab("advanced")}>Open Advanced</Button>;
  if (updateChecks.app) return <PanelNotice tone="ok" title="Automatic update checks are on" actions={open} dismissKey="checks-on">
    CARE Clinic looks for new versions of this app. An administrator can turn this off in Advanced.
  </PanelNotice>;
  return <PanelNotice tone="warning" title="Update checks are turned off" actions={open} dismissKey="checks-off">
    CARE Clinic won't look for new versions of this app. An administrator can turn them back on in Advanced.
  </PanelNotice>;
}

function ClinicApplicationCard({ controller }: { controller: AppUpdateController }) {
  const { service } = useCare();
  const running = service?.summary.running === true;
  const { update, version, checking, problem, progress, active, disabled, check, install, off, ready, unsigned, outcome } = controller;
  const action = usePanelTask();
  const [checkedAt, setCheckedAt] = useState(0);
  useEffect(() => { if (update) setCheckedAt(Date.now()); }, [update]);
  const phase = progress?.phase;
  const downloading = phase === "downloading" ? progress : null;
  const percent = downloading && downloading.total > 0
    ? Math.max(0, Math.min(100, Math.round(downloading.done / downloading.total * 100))) : undefined;
  const target = update?.version ? ` ${update.version}` : "";
  const phaseTitle = phase === "downloading" ? `Downloading CARE Clinic${target}…`
    : phase === "verifying" || phase === "ready" ? "Checking the downloaded update…"
      : phase === "installing" ? `Installing CARE Clinic${target}…`
        : phase === "restarting" ? "Installed — restarting CARE Clinic…"
          : phase === "installer" ? "The installer has opened"
            : "Preparing the CARE Clinic update…";
  const problemTitle = problem?.kind === "download" ? "The download didn't come through properly"
    : problem?.kind === "install" ? "CARE Clinic couldn't finish updating"
      : problem?.kind === "unavailable" ? "New version, but not for this computer yet"
        : "Couldn't check for updates";
  const problemDetail = problem?.kind === "offline" ? "Couldn't connect to the update service. Check the internet connection and try again."
    : problem?.kind === "download" ? "Nothing was installed. Try again on a steadier connection."
      : problem?.kind === "install" ? "Try again. If it keeps failing, share the log file with your support contact."
        : problem?.kind === "unavailable" ? `${problem.version} is available for other systems. Check again another day.`
          : "Updates couldn't be checked right now. Try again. If it keeps failing, share the log file with support.";

  return <section className="panel-card panel-card-pad panel-update-card" aria-label="CARE Clinic application">
    <div className="panel-update-head">
      <span className="panel-tile"><Monitor aria-hidden="true" /></span>
      <div className="panel-grow">
        <h2 className="panel-title">CARE Clinic application</h2>
        <p className="panel-card-sub">{version ? `Version ${version}` : "Checking the application version…"}
          {off ? " · Update checks are turned off in Advanced." : checkedAt ? ` · Checked at ${checkedTime(checkedAt)}` : ""}</p>
      </div>
      {off && !active ? <PanelBadge>Checks off</PanelBadge>
        : <PanelBadge tone={problem ? "warning" : update ? "ok" : "neutral"} working={checking || active}>
          {active ? "Updating" : checking ? "Checking…" : problem ? "Needs attention"
            : update?.available ? `${update.version} ${ready ? "ready" : "available"}` : update ? "Up to date" : "Not checked yet"}
        </PanelBadge>}
      {update?.available && unsigned ? <PanelBadge tone="warning">Unsigned</PanelBadge> : null}
      {!active && !off ? <Button aria-label="Check CARE Clinic updates" disabled={checking || disabled} onClick={() => void check()}>Check now</Button> : null}
    </div>
    {outcome?.state ? <AppUpdateOutcomeNotice controller={controller} /> : null}
    {active ? <div className="panel-update-progress" role="status" aria-live="polite">
      <h3>{phaseTitle}</h3>
      {phase !== "installer" && phase !== "restarting" ? <Progress value={percent} aria-label="CARE Clinic update download" /> : null}
      {downloading ? <p className="panel-small">
        {downloading.total > 0 ? `${megabytes(downloading.done) || "0 MB"} of ${megabytes(downloading.total)}`
          : downloading.done > 0 ? `${megabytes(downloading.done)} downloaded` : "Starting download…"}
        {percent !== undefined ? ` · ${percent}%` : ""}
      </p> : null}
      <p className="panel-small">{phase === "installer" ? "Follow the installer to finish, then reopen CARE Clinic."
        : phase === "restarting" ? "CARE Clinic is reopening."
          : "Keep CARE Clinic open. Your computer may ask for permission to replace the app."}</p>
      {running ? <p className="panel-small">The clinic keeps running while CARE Clinic updates.</p> : null}
      {phase === "installer" ? <Button disabled={controller.updating} onClick={controller.dismiss}>Done</Button> : null}
    </div> : off ? null : problem ? <PanelNotice title={problemTitle} tone={problem.kind === "download" || problem.kind === "install" ? "danger" : "warning"}
      actions={<>
        {problem.kind !== "unavailable" ? <Button disabled={disabled || checking}
          onClick={() => void (problem.kind === "download" || problem.kind === "install" ? install() : check())}>Try again</Button> : null}
        <PanelLogButton />
      </>}>
      {problemDetail}
    </PanelNotice> : update?.available ? <PanelNotice tone="ok" title={ready ? `CARE Clinic ${update.version} is ready to install` : `CARE Clinic ${update.version} is available`}>
      {ready ? "Downloaded and verified. CARE Clinic closes briefly, installs it and reopens. Your computer may ask for permission."
        : "Downloads and verifies the new version, then asks you to restart CARE Clinic to install it."}
      {running ? " Clinic data is kept; if the update includes a new CARE Clinic service, the clinic restarts once." : ""}
      {unsigned ? " This version is not signed; it is offered only because this copy isn't signed either." : ""}
      <div className="panel-actions">
        {update.notes_url ? <Button disabled={action.working} onClick={() => void action.run(() => bridge.OpenURL(update.notes_url),
          "Couldn't open what's new. Try again when this computer is connected to the internet.")}>
          <ScrollText aria-hidden="true" />What&apos;s new
        </Button> : null}
        <Button variant="primary" disabled={disabled || checking} onClick={() => void install()}>
          {ready ? <><RotateCw aria-hidden="true" />Restart to update</> : <><Download aria-hidden="true" />Update CARE Clinic</>}
        </Button>
      </div>
    </PanelNotice> : null}
    {action.error ? <p className="panel-inline-error" role="alert">{action.error}</p> : null}
  </section>;
}

/** What the relaunched app reports about the self-update it just finished. */
function AppUpdateOutcomeNotice({ controller }: { controller: AppUpdateController }) {
  const { outcome, finish, dismissOutcome, disabled } = controller;
  if (!outcome?.state) return null;
  if (outcome.state === "finishing") {
    return <div className="panel-update-progress" role="status" aria-live="polite">
      <h3>Finishing the update to CARE Clinic {outcome.version}…</h3>
      <Progress aria-label="Finishing the CARE Clinic update" />
      <p className="panel-small">Checking the CARE Clinic service and bringing the clinic back. Clinic data is not changed.</p>
    </div>;
  }
  if (outcome.state === "done") {
    return <PanelNotice tone="ok" title={`CARE Clinic ${outcome.version} is installed`}
      actions={<Button onClick={dismissOutcome}>Dismiss</Button>}>
      The update finished and the clinic is running.
    </PanelNotice>;
  }
  return <PanelNotice tone="danger" title={`The update to CARE Clinic ${outcome.version} didn't finish`}
    actions={<>
      {outcome.can_retry ? <Button disabled={disabled} onClick={() => void finish()}>Try again</Button> : null}
      <Button onClick={dismissOutcome}>Dismiss</Button>
      <PanelLogButton />
    </>}>
    {outcome.error || "See the log file for details."}
    {outcome.can_retry ? " The new version is installed; trying again finishes the remaining steps." : " The previous version was kept."}
  </PanelNotice>;
}
