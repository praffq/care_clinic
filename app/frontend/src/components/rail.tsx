import { memo } from "react";
import { ArrowDownToLine, ArrowUpRight, DatabaseBackup, House, LayoutDashboard, ScrollText, Settings } from "lucide-react";

import logoMark from "@/assets/care-logo-mark.svg";
import { bridge } from "@/lib/bridge";
import { panelStatus } from "@/screens/panel/panel-status";
import { usePanelTask } from "@/screens/panel/panel-ui";
import { useCare, type PanelTab } from "@/state/care-store";

const CLINIC_TABS: { id: PanelTab; label: string; icon: typeof LayoutDashboard }[] = [
  { id: "overview", label: "Overview", icon: LayoutDashboard },
  { id: "home-access", label: "Home access", icon: House },
  { id: "updates", label: "Updates", icon: ArrowDownToLine },
  { id: "backups", label: "Backups", icon: DatabaseBackup },
  { id: "advanced", label: "Advanced", icon: Settings },
];

export const Rail = memo(function Rail({ locked = false, updateCount = 0 }: {
  locked?: boolean;
  updateCount?: number;
}) {
  const care = useCare();
  const task = usePanelTask();
  const status = panelStatus(care);
  return <aside className="care-panel-rail" aria-label="CARE Clinic control panel">
    <div className="care-panel-rail-brand">
      <img src={logoMark} alt="" />
      <div>CARE Clinic<p>Control panel</p></div>
    </div>
    <div className="care-panel-rail-sep" />
    <div className="care-panel-rail-kicker">Clinic</div>
    <nav className="care-panel-rail-nav" aria-label="Clinic sections">
      {CLINIC_TABS.map(({ id, label, icon: Icon }) => <button key={id} type="button"
        className="care-panel-rail-link" aria-current={care.tab === id ? "page" : undefined}
        disabled={locked} onClick={() => care.setTab(id)}>
        <Icon aria-hidden="true" /><span>{label}</span>
        {id === "updates" && updateCount > 0 ? <span className="care-panel-rail-count"
          aria-label={`${updateCount} ${updateCount === 1 ? "update" : "updates"} available`}>{updateCount}</span> : null}
      </button>)}
    </nav>
    <div className="care-panel-rail-spacer" />
    <button type="button" className="care-panel-rail-link care-panel-rail-docs" disabled={task.working}
      onClick={() => void task.run(() => bridge.OpenURL("https://docs.ohc.network/"),
        "Couldn't open the documentation. Try again when this computer is connected to the internet.")}>
      <ScrollText aria-hidden="true" /><span>Docs</span><ArrowUpRight aria-hidden="true" />
    </button>
    {task.error ? <p className="care-panel-rail-error" role="alert">{task.error}</p> : null}
    <div className="care-panel-rail-status" role="status" aria-label={`Clinic status: ${status.label}`}>
      <span className={`care-panel-rail-dot panel-tone-${status.tone}`} />
      <div>{status.label}
        {care.busy && !status.working ? <p>{care.busyLabel}…</p> : null}
      </div>
    </div>
    {care.version ? <div className="care-panel-rail-version">v{care.version.replace(/^v/, "")}</div> : null}
  </aside>;
});
