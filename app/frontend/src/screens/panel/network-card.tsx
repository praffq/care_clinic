import { RefreshCw } from "lucide-react";
import { useCallback, useEffect, useRef, useState } from "react";

import { Spinner } from "@/components/spinner";
import { Button } from "@/components/ui/button";
import { bridge } from "@/lib/bridge";
import { errorText } from "@/lib/format";
import { useCare } from "@/state/care-store";
import type { NetworkStatus } from "@/types";
import { PanelLogButton, usePanelTask } from "./panel-ui";
import { usePanelUpdateLock } from "./panel-update-lock";

/** Windows network profile check; renders nothing when it doesn't apply or is fine. */
export function NetworkCard({ locked }: { locked: boolean }) {
  const { log } = useCare();
  const updateLock = usePanelUpdateLock();
  const task = usePanelTask();
  const [network, setNetwork] = useState<NetworkStatus | null>(null);
  const [checking, setChecking] = useState(false);
  const mounted = useRef(true);
  const check = useCallback(async () => {
    setChecking(true);
    try {
      const next = await bridge.NetworkStatus();
      if (mounted.current) setNetwork(next);
    } catch (e) {
      log(`network check: ${errorText(e)}`);
    } finally { if (mounted.current) setChecking(false); }
  }, [log]);
  useEffect(() => {
    mounted.current = true;
    void check();
    return () => { mounted.current = false; };
  }, [check]);

  if (!network?.applicable || network.ok) return null;
  const fix = () => void task.run(async () => {
    await bridge.FixNetwork();
    await check();
  }, "Couldn't change the network profile. Try again, or change it in Windows settings.");
  return <section className="panel-card panel-card-pad panel-requirements" aria-label="Network profile">
    <div className="panel-row panel-between">
      <div>
        <h2 className="panel-eyebrow">Network profile</h2>
        <p className="panel-card-sub">{network.fixable ? "Set this network to Private so other devices can reach CARE."
          : "Check the clinic network in Windows settings, then check again."}</p>
      </div>
      <Button size="sm" disabled={checking || task.working} onClick={() => void check()}>
        {checking ? <Spinner /> : <RefreshCw aria-hidden="true" />}{checking ? "Checking…" : "Check now"}
      </Button>
    </div>
    {network.how ? <p className="panel-small">{network.how}</p> : null}
    <div className="panel-actions">
      {network.fixable ? <Button variant="primary" disabled={locked || updateLock.active || task.working} onClick={fix}>
        {task.working ? <Spinner /> : null}Set this network to Private
      </Button> : null}
      <PanelLogButton />
    </div>
    {task.error ? <p className="panel-inline-error" role="alert">{task.error}</p> : null}
  </section>;
}
