import { Copy, ExternalLink, Play, RefreshCw, RotateCcw, Smartphone, Square, TriangleAlert } from "lucide-react";
import { useState, type ReactNode } from "react";

import { Spinner } from "@/components/spinner";
import { Button } from "@/components/ui/button";
import { toast } from "@/components/ui/sonner";
import { bridge } from "@/lib/bridge";
import { useCare, type ClinicAction } from "@/state/care-store";
import type { EngineState, ServiceState, StackState } from "@/types";
import { NetworkCard } from "./network-card";
import { PhoneDialog } from "./phone-dialog";
import { panelStatus, type PanelTone } from "./panel-status";
import { PanelBadge, PanelLogButton, PanelPageHeader, usePanelTask } from "./panel-ui";
import { usePanelUpdateLock } from "./panel-update-lock";
import { BackupStatusCard } from "./backup-status";
import { StorageHealthCard } from "./storage-health";

const ENGINE_LABELS: Record<EngineState, [string, PanelTone]> = {
  no_engine: ["Not installed", "danger"],
  stopped: ["Stopped", "neutral"],
  starting: ["Starting", "warning"],
  ready: ["Running", "ok"],
  not_ready: ["Not ready", "warning"],
  stopping: ["Stopping", "warning"],
  crashed: ["Stopped unexpectedly", "danger"],
  backoff: ["Waiting to retry", "warning"],
};

const STACK_LABELS: Record<StackState, [string, PanelTone]> = {
  not_installed: ["Not installed", "neutral"],
  starting: ["Starting", "warning"],
  running: ["Running", "ok"],
  failed: ["Failed", "danger"],
  stopping: ["Stopping", "warning"],
  stopped: ["Stopped", "neutral"],
};

export function OverviewTab() {
  const care = useCare();
  const updateLock = usePanelUpdateLock();
  const [phoneOpen, setPhoneOpen] = useState(false);
  const action = usePanelTask();
  const address = usePanelTask();
  const refresh = usePanelTask();
  const status = panelStatus(care);
  const service = care.service;
  const locked = care.busy || action.working;
  const mutationLocked = locked || updateLock.active || !service?.reachable;
  const running = service?.summary.running === true;
  const run = (name: ClinicAction) => {
    if (mutationLocked || updateLock.isActive()) return;
    void action.run(() => care.runAction(name),
      "That didn't start. Try again, or open the log file for support.");
  };
  const copy = async () => {
    if (await address.run(() => navigator.clipboard.writeText(care.mdnsName),
      "Couldn't copy the address. Select the address above and copy it instead.")) toast("Address copied");
  };

  return <div className="panel-page" aria-label="Overview">
    <PanelPageHeader title="Overview" subtitle="Your clinic at a glance." />
    <section className={`panel-card panel-hero ${status.tone === "danger" ? "panel-hero-danger" : ""}`} aria-label="Clinic status">
      <div className={`panel-orb panel-tone-${status.tone}`} aria-hidden="true">
        {status.working ? <Spinner /> : status.tone === "danger" ? <TriangleAlert />
          : <span className={`panel-orb-dot ${!running ? "panel-orb-stop" : ""}`} />}
      </div>
      <div className="panel-hero-copy" role="status" aria-live="polite">
        <h2 className={`panel-state-${status.tone}`}>{status.label}</h2>
        {status.detail ? <p>{status.detail}</p> : null}
        {service && !service.reachable && service.error ? <p className="panel-small">{service.error}</p> : null}
        {service?.summary.notes?.length ? <ul className="panel-small">
          {service.summary.notes.map((note) => <li key={note}>{note}</li>)}
        </ul> : null}
      </div>
      <div className="panel-hero-actions">
        {running ? <>
          <Button disabled={mutationLocked} onClick={() => run("stop")}><Square aria-hidden="true" />Stop</Button>
          <Button variant="primary" disabled={mutationLocked} onClick={() => run("restart")}><RotateCcw aria-hidden="true" />Restart</Button>
        </> : <>
          <Button variant="primary" disabled={mutationLocked} onClick={() => run("start")}><Play aria-hidden="true" />Start clinic</Button>
          <Button disabled={mutationLocked} onClick={() => run("restart")}><RotateCcw aria-hidden="true" />Restart</Button>
        </>}
        <Button variant="ghost" disabled={refresh.working} onClick={() => void refresh.run(care.refreshService,
          "Couldn't check the clinic. Try again, or open the log file for support.")}>
          {refresh.working ? <Spinner /> : <RefreshCw aria-hidden="true" />}Check again
        </Button>
      </div>
      {action.error || refresh.error ? <div className="panel-hero-error">
        <p className="panel-inline-error" role="alert">{action.error || refresh.error}</p>
        <div className="panel-actions"><PanelLogButton /></div>
      </div> : null}
    </section>
    <NetworkCard locked={locked} />
    <div className="panel-overview-grid">
      <section className="panel-address-card" aria-label="Clinic address">
        <h2 className="panel-eyebrow">Clinic address</h2>
        <div className="panel-address">{care.mdnsName}</div>
        <p>{status.available ? "Open on any computer, phone or tablet on the clinic Wi-Fi."
          : !running ? "Works again as soon as the clinic is started." : "Not reachable right now."}</p>
        <div className="panel-card-spacer" />
        <div className="panel-actions">
          <Button variant="white" disabled={!status.available || locked || address.working}
            onClick={() => void address.run(() => bridge.OpenURL(`https://${care.mdnsName}/`),
              "Couldn't open CARE. Try again, or type the clinic address into your browser.")}>
            <ExternalLink aria-hidden="true" />Open CARE
          </Button>
          <Button variant="glass" disabled={locked || address.working} onClick={() => void copy()}>
            <Copy aria-hidden="true" />Copy
          </Button>
          <Button variant="glass" disabled={!status.available || locked} onClick={() => setPhoneOpen(true)}>
            <Smartphone aria-hidden="true" />Connect a phone or tablet
          </Button>
        </div>
        {address.error ? <p className="panel-inline-error" role="alert">{address.error}</p> : null}
      </section>
      <ServiceDetails service={service} />
    </div>
    <StorageHealthCard />
    <BackupStatusCard />
    {phoneOpen ? <PhoneDialog onClose={() => setPhoneOpen(false)} /> : null}
  </div>;
}

function Row({ label, children }: { label: string; children: ReactNode }) {
  return <div className="panel-row panel-between panel-detail-row">
    <span className="panel-small">{label}</span><span>{children}</span>
  </div>;
}

function ServiceDetails({ service }: { service: ServiceState | null }) {
  const status = service?.status;
  if (!service || !status) return <section className="panel-card panel-card-pad" aria-label="Clinic details">
    <h2 className="panel-eyebrow">Details</h2>
    <p className="panel-small">{!service ? "Checking the CARE Clinic service…"
      : !service.installed ? "The CARE Clinic service isn't installed on this computer."
        : "The CARE Clinic service isn't answering. Details will appear when it responds."}</p>
  </section>;
  const engine = ENGINE_LABELS[status.engine.state] ?? [status.engine.state, "neutral"];
  const stack = status.engine.stack ? STACK_LABELS[status.engine.stack.state] ?? [status.engine.stack.state, "neutral"] : null;
  const version = status.engine.engine_version || status.slots.current;
  const mdns = status.mdns;
  const listeners = status.forward?.listeners ?? [];
  return <section className="panel-card panel-card-pad" aria-label="Clinic details">
    <h2 className="panel-eyebrow">Details</h2>
    <Row label="Engine"><PanelBadge tone={engine[1]}>{engine[0]}</PanelBadge>{version ? <span className="panel-small"> {version}</span> : null}</Row>
    {status.engine.last_error ? <p className="panel-small panel-inline-error">{status.engine.last_error}</p> : null}
    {stack ? <Row label="CARE"><PanelBadge tone={stack[1]}>{stack[0]}</PanelBadge></Row> : null}
    {status.engine.stack?.error ? <p className="panel-small panel-inline-error">{status.engine.stack.error}</p> : null}
    {mdns ? <Row label="Network name">
      <span className="panel-small">{mdns.name ? `${mdns.name}.local` : "Not set"} </span>
      <PanelBadge tone={mdns.conflict ? "danger" : mdns.advertising ? "ok" : "neutral"}>
        {mdns.conflict ? "Name conflict" : mdns.advertising ? "Advertising" : mdns.enabled ? "Not advertising" : "Off"}
      </PanelBadge>
    </Row> : null}
    {mdns?.conflict ? <p className="panel-small panel-inline-error">Another device ({mdns.conflict.host}, {mdns.conflict.address}) is using this name.</p> : null}
    {mdns?.last_error ? <p className="panel-small panel-inline-error">{mdns.last_error}</p> : null}
    {listeners.map((listener) => <div key={listener.name}>
      <Row label={`Port ${listener.listen}`}>
        <PanelBadge tone={listener.listening ? "ok" : "danger"}>{listener.listening ? "Listening" : "Not listening"}</PanelBadge>
      </Row>
      {listener.error ? <p className="panel-small panel-inline-error">{listener.error}</p> : null}
    </div>)}
  </section>;
}
