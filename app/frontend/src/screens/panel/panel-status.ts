import type { ServiceState } from "@/types";

export type PanelTone = "ok" | "warning" | "danger" | "neutral";

type StatusInput = {
  service: ServiceState | null;
  busy: boolean;
  busyLabel: string;
};

const OPERATIONS: Record<string, [string, string]> = {
  Starting: ["Starting", "CARE is starting. Keep this computer on."],
  Stopping: ["Stopping", "Staff won't be able to use CARE until it is started again."],
  Restarting: ["Restarting", "Staff will be signed out briefly. Keep this computer on."],
  Uninstalling: ["Removing", "CARE is being removed from this computer."],
  "Updating the clinic": ["Updating", "CARE restarts during the update. Keep this computer on."],
  "Rolling back the clinic update": ["Rolling back", "CARE restarts on the previous version. Keep this computer on."],
  "Restoring a backup": ["Restoring", "Staff can't use CARE until the restore finishes. Keep this computer on."],
};

/** Panel status, driven by the background service's summary. */
export function panelStatus({ service, busy, busyLabel }: StatusInput) {
  const operation = busy ? OPERATIONS[busyLabel] : undefined;
  if (operation) return {
    label: operation[0], detail: operation[1], tone: "warning" as PanelTone,
    working: true, available: false, running: service?.summary.running ?? false,
  };
  if (!service) return {
    label: "Checking", detail: "Checking whether CARE is running on this computer.",
    tone: "neutral" as PanelTone, working: true, available: false, running: false,
  };
  const { summary } = service;
  const tone: PanelTone = summary.level === "ok" ? "ok" : summary.level === "busy" ? "warning"
    : summary.level === "warn" ? "warning" : "danger";
  return {
    label: summary.title,
    detail: summary.detail ?? "",
    tone,
    working: summary.level === "busy",
    available: service.reachable && summary.running && summary.level === "ok",
    running: summary.running,
  };
}
