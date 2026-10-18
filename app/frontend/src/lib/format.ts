// Small presentation helpers shared across the screens.

/** Go errors arrive as multi-line strings; a toast only has room for the first. */
export function firstLine(s: string): string {
  return s.split("\n")[0].trim();
}

export function errorText(e: unknown): string {
  return e instanceof Error ? e.message : String(e);
}

/** "2026-09-09 04:30 - daily" -> "09/09 04:30" */
export function shortDate(label: string): string {
  const m = label.match(/^(\d{4})-(\d{2})-(\d{2})\s+(\d{2}:\d{2})/);
  return m ? `${m[3]}/${m[2]} ${m[4]}` : label.split(" - ")[0] || label;
}

export function mmss(seconds: number): string {
  return `${Math.floor(seconds / 60)}:${String(seconds % 60).padStart(2, "0")}`;
}

export function diskSize(bytes: number): string {
  const gb = bytes / 2 ** 30;
  if (gb >= 10) return `${Math.round(gb)} GB`;
  if (gb >= 1) return `${gb.toFixed(1)} GB`;
  return `${Math.round(bytes / 2 ** 20)} MB`;
}

export function megabytes(bytes: number): string {
  return bytes ? `${(bytes / 1e6).toFixed(1)} MB` : "";
}

/** "care" / "CARE.local" -> "care.local" */
export function normaliseHost(raw: string): string {
  // Mirrors mdns.Label on the host: lowercase, trim spaces, then strip surrounding
  // dots BEFORE and after removing a trailing ".local". Without the dot trimming,
  // "care." became "care..local" here and in the stored config.
  const label = raw
    .trim()
    .toLowerCase()
    .replace(/^\.+|\.+$/g, "")
    .replace(/\.local$/i, "")
    .replace(/^\.+|\.+$/g, "");
  return `${label}.local`;
}

/** A status timestamp for people: "never" when unset. */
export function when(at?: string): string {
  if (!at) return "never";
  const date = new Date(at);
  return Number.isNaN(date.getTime()) || date.getTime() <= 0 ? "never" : date.toLocaleString();
}

/** An ISO time as "12 Mar 2026, 14:05" in the computer's locale; "" for none. */
export function dateTime(iso?: string): string {
  if (!iso) return "";
  const at = new Date(iso);
  if (Number.isNaN(at.getTime()) || at.getTime() <= 0) return "";
  return at.toLocaleString([], { day: "numeric", month: "short", year: "numeric", hour: "2-digit", minute: "2-digit" });
}
