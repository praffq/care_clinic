// Model for the Plugins card: configs as editable text, mirroring
// internal/plugins (ParseConfig, IsSecret, RedactedValue).
import type { Plugin, PluginPreset } from "@/types";

export const REDACTED = "********";

const SECRET = /(KEY|SECRET|TOKEN|PASSWORD|PASSWD|CREDENTIAL)/i;
const CONFIG_KEY = /^[A-Za-z_][A-Za-z0-9_]{0,99}$/;
const PLUGIN_ID = /^[a-z][a-z0-9_]{0,63}$/;

export function isSecret(key: string) {
  return SECRET.test(key);
}

/** A config value as the user types it: strings stay bare, the rest is JSON. */
export function showValue(value: unknown): string {
  return typeof value === "string" ? value : JSON.stringify(value);
}

/** Reads a typed value back: JSON when it parses, else the string. */
export function parseValue(text: string): unknown {
  try {
    return JSON.parse(text);
  } catch {
    return text;
  }
}

/** KEY=value lines for the configs not in skip. */
export function configLines(configs: Record<string, unknown> | undefined, skip: string[] = []): string {
  return Object.entries(configs ?? {})
    .filter(([key]) => !skip.includes(key))
    .map(([key, value]) => `${key}=${showValue(value)}`)
    .join("\n");
}

/** Parses KEY=value lines; returns an error message for a bad line. */
export function parseLines(text: string): { configs: Record<string, unknown>; error: string } {
  const configs: Record<string, unknown> = {};
  for (const raw of text.split("\n")) {
    const line = raw.trim();
    if (!line || line.startsWith("#")) continue;
    const at = line.indexOf("=");
    const key = at < 0 ? "" : line.slice(0, at).trim();
    if (!CONFIG_KEY.test(key)) return { configs, error: `"${line}" must be KEY=value.` };
    configs[key] = parseValue(line.slice(at + 1).trim());
  }
  return { configs, error: "" };
}

export type PluginDraft = {
  /** The preset this draft starts from, if any. */
  preset?: PluginPreset;
  /** True when the plugin is already enabled (the ID is fixed). */
  enabled: boolean;
  id: string;
  package: string;
  version: string;
  /** Values of the preset's required configs; blank keeps a saved secret. */
  required: Record<string, string>;
  /** Saved secrets among the required configs (shown blank). */
  saved: string[];
  extra: string;
};

export function draftFor(preset: PluginPreset | undefined, enabled: Plugin | undefined): PluginDraft {
  const source: Plugin | undefined = enabled ?? preset;
  const requiredKeys = preset?.required_configs ?? [];
  const required: Record<string, string> = {};
  const saved: string[] = [];
  for (const key of requiredKeys) {
    const value = source?.configs?.[key];
    if (value === REDACTED) saved.push(key);
    required[key] = value === undefined || value === REDACTED ? "" : showValue(value);
  }
  return {
    preset, enabled: !!enabled,
    id: source?.id ?? "", package: source?.package ?? "", version: source?.version ?? "",
    required, saved, extra: configLines(source?.configs, requiredKeys),
  };
}

/** Builds the plugin to send, or an error message. */
export function pluginFrom(draft: PluginDraft): { plugin?: Plugin; error: string } {
  const id = draft.id.trim();
  if (!PLUGIN_ID.test(id)) {
    return { error: "The name must be the plugin's Python app name: lowercase letters, digits and _, e.g. care_example." };
  }
  const pkg = draft.package.trim();
  if (!pkg) return { error: "Enter the plugin's package: a pip name or a git+https:// URL." };
  const { configs, error } = parseLines(draft.extra);
  if (error) return { error };
  for (const [key, text] of Object.entries(draft.required)) {
    if (text.trim()) configs[key] = parseValue(text.trim());
    else if (draft.saved.includes(key)) configs[key] = REDACTED;
    else return { error: `Enter ${key}.` };
  }
  return { plugin: { id, package: pkg, version: draft.version.trim(), configs }, error: "" };
}

export function presetFor(catalog: PluginPreset[], id: string) {
  return catalog.find((preset) => preset.id === id);
}

/** Whether an enabled plugin is the preset's own source (bundled in CARE). */
export function isPresetSource(preset: PluginPreset | undefined, plugin: Plugin) {
  return !!preset && preset.package === plugin.package && (preset.version ?? "") === (plugin.version ?? "");
}
