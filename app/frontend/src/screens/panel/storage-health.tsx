import { Activity, HardDrive, RefreshCw } from "lucide-react";
import { useCallback, useEffect, useRef, useState } from "react";

import { Spinner } from "@/components/spinner";
import { Button } from "@/components/ui/button";
import { bridge } from "@/lib/bridge";
import { diskSize, errorText } from "@/lib/format";
import { useCare } from "@/state/care-store";
import type { CareHealth, DiskUsage, StorageLevel, StorageState } from "@/types";
import type { PanelTone } from "./panel-status";
import { PanelBadge } from "./panel-ui";

import "./storage-health.css";

const REFRESH_MS = 60_000;

const LEVEL: Record<StorageLevel, [string, PanelTone]> = {
  ok: ["Enough room", "ok"],
  low: ["Getting full", "warning"],
  critical: ["Almost full", "danger"],
  unknown: ["Unknown", "neutral"],
};

const SERVICE_LABELS: Record<string, string> = {
  backend: "CARE server", celery: "Background jobs", frontend: "CARE website", db: "Database",
  redis: "Cache", minio: "File storage", caddy: "Web gateway",
};

function serviceTone(state: string): PanelTone {
  if (state === "running") return "ok";
  if (state === "restarting" || state === "created" || state === "starting") return "warning";
  return "danger";
}

/** Disk space and CARE's health, refreshed while Overview is showing. */
export function StorageHealthCard() {
  const { tab, flow, service } = useCare();
  const active = flow === "panel" && tab === "overview";
  const [storage, setStorage] = useState<StorageState | null>(null);
  const [health, setHealth] = useState<CareHealth | null>(null);
  const [error, setError] = useState("");
  const [checking, setChecking] = useState(false);
  const generation = useRef(0);
  const running = service?.summary.running === true;

  const load = useCallback(async () => {
    const request = ++generation.current;
    setChecking(true);
    try {
      const [nextStorage, nextHealth] = await Promise.all([bridge.StorageStatus(), bridge.CheckCareHealth()]);
      if (request !== generation.current) return;
      setStorage(nextStorage);
      setHealth(nextHealth);
      setError("");
    } catch (cause) {
      if (request === generation.current) setError(errorText(cause));
    } finally {
      if (request === generation.current) setChecking(false);
    }
  }, []);

  useEffect(() => {
    if (!active) return;
    void load();
    const timer = window.setInterval(() => void load(), REFRESH_MS);
    return () => { window.clearInterval(timer); generation.current++; };
  }, [active, running, load]);

  return <section className="panel-card panel-card-pad panel-storage-health" aria-label="Storage and health">
    <div className="panel-row panel-between">
      <h2 className="panel-eyebrow">Storage and health</h2>
      <Button variant="ghost" size="sm" disabled={checking} onClick={() => void load()}>
        {checking ? <Spinner /> : <RefreshCw aria-hidden="true" />}Check again
      </Button>
    </div>
    {error ? <p className="panel-inline-error" role="alert">Couldn't check storage and health: {error}</p> : null}
    {storage ? <div className="panel-storage" aria-label="Storage">
      {storage.level !== "ok" ? <p className={`panel-storage-title panel-state-${LEVEL[storage.level][1]}`}
        role={storage.level === "critical" ? "alert" : "status"}>{storage.title}</p> : null}
      {storage.disks.map((disk) => <DiskRow key={disk.kind} disk={disk} />)}
    </div> : !error ? <p className="panel-small">Checking…</p> : null}
    {health ? <HealthRows health={health} /> : null}
  </section>;
}

function DiskRow({ disk }: { disk: DiskUsage }) {
  const [label, tone] = LEVEL[disk.level] ?? LEVEL.unknown;
  const used = disk.total > 0 ? Math.min(100, Math.max(0, ((disk.total - disk.free) / disk.total) * 100)) : 0;
  return <div className="panel-disk" aria-label={disk.name}>
    <div className="panel-row panel-between">
      <span className="panel-disk-name"><HardDrive aria-hidden="true" />{disk.name}</span>
      <PanelBadge tone={tone}>{label}</PanelBadge>
    </div>
    {disk.path ? <div className="panel-disk-path" title={disk.path}>{disk.path}</div> : null}
    {disk.total > 0 ? <>
      <div className="panel-meter" role="meter" aria-valuemin={0} aria-valuemax={100}
        aria-valuenow={Math.round(used)} aria-label={`${disk.name}: ${Math.round(used)}% used`}>
        <div className={`panel-meter-fill panel-tone-${tone}`} style={{ width: `${used}%` }} />
      </div>
      <p className="panel-small">{diskSize(disk.free)} free of {diskSize(disk.total)}</p>
    </> : null}
    {disk.detail ? <p className="panel-small">{disk.detail}</p> : null}
  </div>;
}

function HealthRows({ health }: { health: CareHealth }) {
  const services = health.services ?? [];
  return <div className="panel-health" aria-label="CARE health">
    <div className="panel-row panel-between">
      <span className="panel-disk-name"><Activity aria-hidden="true" />CARE</span>
      <PanelBadge tone={health.healthy ? "ok" : "danger"}>{health.healthy ? "Healthy" : "Not answering"}</PanelBadge>
    </div>
    {!health.healthy ? <p className="panel-small">{health.detail}</p> : null}
    {services.length ? <ul className="panel-health-services">
      {services.map((svc) => <li key={svc.name} className="panel-row panel-between">
        <span className="panel-small">{SERVICE_LABELS[svc.name] ?? svc.name}</span>
        <PanelBadge tone={serviceTone(svc.state)}>{svc.state}</PanelBadge>
      </li>)}
    </ul> : null}
    {health.servicesError ? <p className="panel-small">Couldn't list CARE's parts: {health.servicesError}</p> : null}
  </div>;
}
