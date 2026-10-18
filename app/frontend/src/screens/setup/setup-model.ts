import type { SetupPage } from "@/types";

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
