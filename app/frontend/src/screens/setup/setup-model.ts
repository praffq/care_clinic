import type { SetupPage, SetupRecoveryStatus } from "@/types";

export const SETUP_LABELS: Record<SetupPage, string> = {
  computer: "Computer checks",
  cleanup: "Cleanup stale files",
  address: "Clinic address",
  admin: "Admin password",
  review: "Review",
  install: "Install",
};

/** Turns an InstallService failure into something an operator can act on. */
export function installServiceProblem(raw: string): string {
  if (/\b(user )?cancel(l)?ed\b|declined|-128/i.test(raw)) {
    return "Your computer didn't approve installing the CARE Clinic service. Try again and approve the request when your computer asks.";
  }
  return raw || "The CARE Clinic service couldn't be installed. Try again, or share the log file with your support contact.";
}

export const EMPTY_RECOVERY: SetupRecoveryStatus = { codes_saved: false, codes_path: "", codes_problem: "" };

export function recoveryProblem(raw: string): string {
  if (/outside CARE/i.test(raw)) return "That folder belongs to CARE. Keep recovery codes outside CARE's settings, installation, logs and backup folders. Choose somewhere else.";
  if (/file exists|new filename/i.test(raw)) return "There's already a file with that name, or it couldn't be saved there. CARE never overwrites a file. Choose a new filename or location.";
  if (/locked|already installed/i.test(raw)) return "Recovery setup is locked because installation has already started. Keep the recovery codes you saved and return to the existing installation.";
  if (/unavailable|couldn't open|couldn't find/i.test(raw)) return "The recovery codes file couldn't be opened. It may have been moved or changed. Save a fresh set before continuing.";
  return "The recovery codes couldn't be saved or checked. Try again. If it keeps happening, share the log file with your support contact.";
}
