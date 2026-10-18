import type { SetupBackupStatus, SetupPage, SetupRecoveryStatus } from "@/types";

export const SETUP_LABELS: Record<SetupPage, string> = {
  computer: "Computer checks",
  cleanup: "Cleanup stale files",
  address: "Clinic address",
  backup: "Backups",
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

export function codesProblem(raw: string): string {
  if (/outside CARE/i.test(raw)) return "That folder belongs to CARE. Keep recovery codes outside CARE's settings, installation, logs and backup folders. Choose somewhere else.";
  if (/file exists|new filename/i.test(raw)) return "There's already a file with that name, or it couldn't be saved there. CARE never overwrites a file. Choose a new filename or location.";
  if (/locked|already installed/i.test(raw)) return "Recovery setup is locked because installation has already started. Keep the recovery codes you saved and return to the existing installation.";
  if (/unavailable|couldn't open|couldn't find/i.test(raw)) return "The recovery codes file couldn't be opened. It may have been moved or changed. Save a fresh set before continuing.";
  return "The recovery codes couldn't be saved or checked. Try again. If it keeps happening, share the log file with your support contact.";
}

export const EMPTY_BACKUP_RECOVERY: SetupBackupStatus = { backup_saved: false, backup_verified: false, backup_path: "", backup_problem: "" };

export function backupProblem(raw: string): string {
  if (!raw) return "";
  if (/read.only/i.test(raw)) return "That location is read-only, so backups can't be written to it. Choose a different location.";
  if (/not a folder|file, not a folder/i.test(raw)) return "That's a file, not a folder. Choose a different location.";
  if (/isn't there|not available|no such file/i.test(raw)) return "The backup location isn't available any more. Reconnect it and check again, or choose a different location.";
  if (/Desktop, Documents or Downloads|iCloud/i.test(raw)) return "macOS doesn't let CARE's background service write to Desktop, Documents, Downloads or iCloud Drive. Choose a different location, such as a folder on an external drive.";
  if (/service's backup folder|service is running/i.test(raw)) return "CARE couldn't read where backups go. Check that the CARE Clinic service is running, then check again.";
  if (/not.*allowed|isn['’]t allowed|permission|access denied/i.test(raw)) return "This computer isn't allowed to write there. Ask whoever looks after this computer for access, or choose a different location.";
  if (/another CARE|another clinic|different clinic|foreign/i.test(raw)) return "That folder already holds backups from a different clinic. Leave those backups intact and choose a different location.";
  if (/free space|not enough|needs .*GB|too small/i.test(raw)) return "There isn't enough room for the first backup. Choose a different location with more room.";
  if (/recovery.*separate|recovery file/i.test(raw)) return "Keep the backup recovery file separate from the backup folder. Choose a different location.";
  if (/top of a drive/i.test(raw)) return "Backups can't go at the top of a drive. Choose or create a folder on it.";
  if (/inside|outside|overlap|CARE|absolute/i.test(raw)) return "Backups can't be kept in that location. Keep them outside CARE's own folders and choose a different location.";
  return "That location couldn't be checked or written to. Check it again, or choose a different location.";
}

export function recoveryProblem(raw: string): string {
  if (/outside CARE|settings, installation|overlap/i.test(raw)) return "That folder belongs to CARE. Keep recovery materials outside CARE's settings, installation, logs and backup folder. Choose somewhere else.";
  if (/file exists|new filename/i.test(raw)) return "There's already a file with that name, or it couldn't be saved there. CARE never overwrites a file. Choose a new filename or location.";
  if (/match|different|decrypt/i.test(raw)) return "That file doesn't match this clinic. It may be from an earlier setup or a different clinic. Select the file you just saved.";
  if (/locked|already installed/i.test(raw)) return "Recovery setup is locked because installation has already started. Keep the recovery material you saved and return to the existing installation.";
  if (/read|open|invalid|private key|PEM|recovery file/i.test(raw)) return "That file couldn't be opened. It may be damaged or incomplete. Select the correct file, or save a new recovery file before installing.";
  return "The recovery file couldn't be saved or checked. Try again. If it keeps happening, share the log file with your support contact.";
}
