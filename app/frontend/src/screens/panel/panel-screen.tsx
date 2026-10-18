import { ArrowDownToLine, TriangleAlert, X } from "lucide-react";
import { useLayoutEffect, useRef } from "react";

import { Rail } from "@/components/rail";
import { Screen, ScreenBody } from "@/components/screen";
import { Button } from "@/components/ui/button";
import { useAppUpdate, type AppUpdateController } from "@/hooks/use-app-update";
import { useCare } from "@/state/care-store";
import { AdvancedTab } from "./advanced-tab";
import { HomeAccessTab } from "./home-access";
import { OverviewTab } from "./overview-tab";
import { DismissButton, PanelLogButton, useDismissed, usePanelTask } from "./panel-ui";
import { PanelUpdateLockProvider } from "./panel-update-lock";
import { RecoveryCodesBanner } from "./recovery-codes-banner";
import { UpdatePanel } from "./update-panel";

import "./panel.css";

export function PanelScreen() {
  const appUpdate = useAppUpdate(false);
  return <PanelUpdateLockProvider active={appUpdate.active} isActive={appUpdate.isActive}>
    <PanelContent appUpdate={appUpdate} />
  </PanelUpdateLockProvider>;
}

function PanelContent({ appUpdate }: { appUpdate: AppUpdateController }) {
  const care = useCare();
  const task = usePanelTask();
  const panel = useRef<HTMLDivElement>(null);
  const locked = care.busy || appUpdate.active || task.working;
  const [taskErrorHidden, hideTaskError] = useDismissed(task.error ?? "");
  const updateCount = appUpdate.update?.available && !appUpdate.active ? 1 : 0;

  useLayoutEffect(() => {
    panel.current?.querySelector(".panel-tab-body")?.scrollTo({ top: 0 });
  }, [care.tab]);

  return <div className="care-panel" ref={panel}>
    <Rail updateCount={updateCount} />
    <Screen className="care-panel-main">
      {care.operationError ? <div className="panel-banner panel-tone-danger" role="alert">
        <TriangleAlert aria-hidden="true" />
        <div className="panel-grow"><strong>{care.operationError.title}. </strong>{care.operationError.message}</div>
        <PanelLogButton />
        <Button variant="ghost" size="icon" aria-label="Dismiss operation message" onClick={care.clearOperationError}><X aria-hidden="true" /></Button>
      </div> : appUpdate.active && care.tab !== "updates" ? <div className="panel-banner" role="status">
        <ArrowDownToLine aria-hidden="true" />
        <div className="panel-grow"><strong>CARE Clinic is updating. </strong>
          {appUpdate.progress?.phase === "installer" ? "Finish the installer, then acknowledge it in Updates."
            : "Wait until it finishes before making other changes."}</div>
        <Button size="sm" onClick={() => care.setTab("updates")}>View update</Button>
      </div> : null}
      {task.error && !care.operationError && !taskErrorHidden ? <div className="panel-banner panel-tone-danger" role="alert">
        <span className="panel-grow">{task.error}</span><PanelLogButton />
        <DismissButton onClick={hideTaskError} />
      </div> : null}
      <RecoveryCodesBanner disabled={locked} />
      <ScreenBody className="panel-tab-body care-panel-content">
        {/* Keep component instances stable; each tab controls its own deactivation. */}
        <div hidden={care.tab !== "overview"}><OverviewTab /></div>
        <div hidden={care.tab !== "home-access"}><HomeAccessTab locked={locked} /></div>
        <div hidden={care.tab !== "updates"}><UpdatePanel appUpdate={appUpdate} /></div>
        <div data-panel-tab="advanced" hidden={care.tab !== "advanced"}><AdvancedTab disabled={locked} /></div>
      </ScreenBody>
    </Screen>
  </div>;
}
