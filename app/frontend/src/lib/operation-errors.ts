import { errorText } from "@/lib/format";

export type OperationError = { action: string; title: string; message: string };

const TITLES: Record<string, string> = {
  start: "CARE couldn't start",
  stop: "CARE couldn't stop",
  restart: "CARE couldn't restart",
  uninstall: "Removal didn't finish",
  plugins: "The plugin change didn't finish",
  settings: "The settings change didn't finish",
  updates: "The clinic update didn't finish",
  "update-check": "Couldn't check for clinic updates",
  backup: "The backup didn't finish",
  restore: "The restore didn't finish",
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
            : action === "updates" ? updateMessage(detail)
              : action === "update-check"
                ? "Check the internet connection and try again. Nothing was changed."
                : action === "backup"
                  ? "Your earlier backups are unchanged. Check that the backup folder is connected and has space, then try again."
                  : action === "restore"
                    ? "Check the clinic on Overview. If CARE doesn't start, try the restore again or open the log file for support."
                    : "The operation didn't finish. Try again, or open the log file for support.";
  return { action, title: TITLES[action] ?? "CARE couldn't finish that", message };
}

function withBuildFailure(detail: string, message: string): string {
  const reason = buildFailure(detail);
  return reason ? `The build failed and won't succeed by retrying: ${reason}. ${message}` : message;
}

function updateMessage(detail: string): string {
  const desktop = /needs CARE Clinic ([^ ]+ )?first/.exec(detail);
  if (desktop) return `This update needs a newer CARE Clinic app${desktop[1] ? ` (${desktop[1].trim()})` : ""}. Update the app on this page first, then apply the clinic update again.`;
  return withBuildFailure(detail, "CARE Clinic put the previous version back where it could. See the update history on this page, or open the log file for support.");
}
