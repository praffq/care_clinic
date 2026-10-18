import { Cpu } from "lucide-react";
import { useCallback, useEffect, useRef, useState } from "react";

import { bridge } from "@/lib/bridge";
import { useCare } from "@/state/care-store";
import type { EngineState, SystemInfo } from "@/types";
import type { PanelTone } from "./panel-status";
import { PanelBadge } from "./panel-ui";

const VM: Record<EngineState, [string, PanelTone]> = {
  no_engine: ["Not installed", "danger"],
  stopped: ["Stopped", "neutral"],
  starting: ["Starting", "warning"],
  ready: ["Running", "ok"],
  not_ready: ["Not ready", "warning"],
  stopping: ["Stopping", "warning"],
  crashed: ["Stopped unexpectedly", "danger"],
  backoff: ["Waiting to retry", "warning"],
};

function uptime(seconds?: number) {
  if (!seconds) return "";
  const hours = Math.floor(seconds / 3600);
  return hours >= 24 ? `${Math.floor(hours / 24)} d ${hours % 24} h` : hours ? `${hours} h ${Math.floor(seconds % 3600 / 60)} min` : `${Math.floor(seconds / 60)} min`;
}

/** Versions of the app, the service and the engine, and the engine slots
 *  (the running one and the one CARE Clinic falls back to). */
export function SystemCard() {
  const { flow, tab, service, version } = useCare();
  const active = flow === "panel" && tab === "overview";
  const [info, setInfo] = useState<SystemInfo | null>(null);
  const generation = useRef(0);
  const load = useCallback(async () => {
    const mine = ++generation.current;
    try {
      const next = await bridge.SystemInfo();
      if (mine === generation.current) setInfo(next);
    } catch {
      if (mine === generation.current) setInfo(null);
    }
  }, []);
  const engineState = service?.status?.engine.state;
  const current = service?.status?.slots.current;
  useEffect(() => { if (active) void load(); }, [active, load, engineState, current]);

  const engine = info?.engine;
  const vm = engine ? VM[engine.state] ?? [engine.state, "neutral"] : null;
  return <section className="panel-card panel-card-pad panel-system-card" aria-label="Engine and system">
    <div className="panel-update-head">
      <span className="panel-tile panel-tile-neutral"><Cpu aria-hidden="true" /></span>
      <div className="panel-grow">
        <h2 className="panel-title">Engine and system</h2>
        <p className="panel-card-sub">What runs CARE on this computer. Share this with support when asked.</p>
      </div>
      {vm ? <PanelBadge tone={vm[1]}>Engine {vm[0].toLowerCase()}</PanelBadge> : null}
    </div>
    <dl className="panel-versions" aria-label="System versions">
      <div className="panel-version"><dt className="panel-eyebrow">CARE Clinic app</dt>
        <dd><span className="panel-version-value">{info?.desktop_version || version || "Unknown"}</span></dd></div>
      <div className="panel-version"><dt className="panel-eyebrow">Background service</dt>
        <dd><span className="panel-version-value">{info?.service_version || "Unknown"}</span></dd></div>
      <div className="panel-version"><dt className="panel-eyebrow">Engine</dt>
        <dd><span className="panel-version-value">{engine?.engine_version || info?.current || "Unknown"}</span>
          {engine?.uptime_seconds ? <span className="panel-small">Up {uptime(engine.uptime_seconds)}{engine.restarts ? ` · ${engine.restarts} restarts` : ""}</span> : null}</dd></div>
    </dl>
    {info?.error ? <p className="panel-small panel-inline-error">{info.error}</p> : null}
    {engine?.last_error ? <p className="panel-small panel-inline-error">{engine.last_error}</p> : null}
    {info?.slots.length ? <h3 className="panel-eyebrow panel-history-title panel-slots">Engine versions installed</h3> : null}
    {info?.slots.length ? <ul aria-label="Engine versions installed">
      {info.slots.map((slot) => <li key={slot.version} className="panel-history-row">
        <div className="panel-grow">
          <div className="panel-mono">{slot.version}</div>
          <p className="panel-small">{[slot.arch, `data format ${slot.data_schema}`].filter(Boolean).join(" · ")}</p>
        </div>
        {slot.current ? <PanelBadge tone="ok">In use</PanelBadge> : slot.fallback ? <PanelBadge>Fallback</PanelBadge> : null}
      </li>)}
    </ul> : null}
    {info && !info.error && !info.fallback ? <p className="panel-small">No fallback engine yet. The previous engine is kept as a fallback after an engine update.</p> : null}
  </section>;
}
