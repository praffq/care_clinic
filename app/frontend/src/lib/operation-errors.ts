import { errorText } from "@/lib/format";

export type OperationError = { action: string; title: string; message: string };

const TITLES: Record<string, string> = {
  start: "CARE couldn't start",
  stop: "CARE couldn't stop",
  restart: "CARE couldn't restart",
  uninstall: "Removal didn't finish",
  plugins: "The plugin change didn't finish",
  settings: "The settings change didn't finish",
};

/** The reason an image build failed for good (the engine's build_failed). */
export function buildFailure(detail: string): string | null {
  const match = /image build failed: (.*?)(?: \(rolled back\))?$/.exec(detail);
  return match ? match[1].trim() : null;
}

export function operationError(action: string, cause: unknown): OperationError {
  const detail = errorText(cause);
  const message = /CARE Clinic admin password does not match/.test(detail)
    ? "The CARE Clinic admin password wasn't accepted. Enter it again."
    : /something else is still running|CARE Clinic is closing/.test(detail)
      ? "Another operation is still running. Wait for it to finish before trying again."
      : /service isn't running|couldn't reach the CARE Clinic service/i.test(detail)
        ? "The CARE Clinic service isn't responding. Check its status on Overview, or open the log file for support."
        : action === "plugins"
          ? withBuildFailure(detail, "CARE Clinic put the previous plugins back. Check the plugin's package and settings, or open the log file for support.")
          : action === "settings"
            ? withBuildFailure(detail, "CARE Clinic put the previous settings back. Your changes are still saved: check them, then retry applying them, or open the log file for support.")
            : "The operation didn't finish. Try again, or open the log file for support.";
  return { action, title: TITLES[action] ?? "CARE couldn't finish that", message };
}

function withBuildFailure(detail: string, message: string): string {
  const reason = buildFailure(detail);
  return reason ? `The build failed and won't succeed by retrying: ${reason}. ${message}` : message;
}
