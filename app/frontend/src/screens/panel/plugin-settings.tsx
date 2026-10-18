import { Package, Plus, Puzzle, Settings2, Trash2 } from "lucide-react";
import { useCallback, useEffect, useRef, useState } from "react";

import { Spinner } from "@/components/spinner";
import {
  AlertDialog, AlertDialogContent, AlertDialogDescription, AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { bridge } from "@/lib/bridge";
import { useCare } from "@/state/care-store";
import type { Plugin, PluginPreset } from "@/types";
import { AdvancedError, AdvancedSecretInput, advancedProblem, useAdvancedLock, type AdvancedProblem } from "./advanced-ui";
import { draftFor, isPresetSource, isSecret, pluginFrom, presetFor, type PluginDraft } from "./plugin-model";

const JOB = "plugins";

/** Backend plugins: presets plus any pip or git plugin. Each change is one
 *  journaled operation in the service (backup, build, migrate, health check,
 *  rollback on failure); care-done "plugins" reports its end. */
export function PluginSettings({ adminPassword }: { adminPassword: string }) {
  const { busy, runJob } = useCare();
  const lock = useAdvancedLock();
  const [catalog, setCatalog] = useState<PluginPreset[]>([]);
  const [enabled, setEnabled] = useState<Plugin[] | null>(null);
  const [problem, setProblem] = useState<AdvancedProblem | null>(null);
  const [draft, setDraft] = useState<PluginDraft | null>(null);
  const [removing, setRemoving] = useState<Plugin | null>(null);
  const [starting, setStarting] = useState(false);
  const live = useRef(true);
  const wasBusy = useRef(busy);

  const reload = useCallback(async () => {
    try {
      const [presets, list] = await Promise.all([bridge.PluginCatalog(), bridge.ListPlugins()]);
      if (!live.current) return;
      setCatalog(presets ?? []);
      setEnabled(list ?? []);
      setProblem(null);
    } catch (cause) {
      if (live.current) setProblem(advancedProblem(cause, "The plugins couldn't be read", "Try again when CARE Clinic is running."));
    }
  }, []);
  useEffect(() => {
    live.current = true;
    void reload();
    return () => { live.current = false; };
  }, [reload]);
  useEffect(() => {
    if (wasBusy.current && !busy) void reload();
    wasBusy.current = busy;
  }, [busy, reload]);

  const blocked = busy || lock.disabled || starting || enabled === null;
  const start = async (label: string, call: () => Promise<void>) => {
    if (blocked || lock.isLocked()) return false;
    setStarting(true);
    setProblem(null);
    try {
      return await runJob(JOB, label, call);
    } catch (cause) {
      setProblem(advancedProblem(cause, "The plugin change didn't start", "Nothing was changed. Check the plugin and try again."));
      return false;
    } finally {
      if (live.current) setStarting(false);
    }
  };
  const enable = async (plugin: Plugin) => {
    if (await start(`Setting up ${plugin.id}`, () => bridge.EnablePlugin(plugin, adminPassword))) setDraft(null);
  };
  const disable = async (plugin: Plugin) => {
    setRemoving(null);
    await start(`Removing ${plugin.id}`, () => bridge.DisablePlugin(plugin.id, adminPassword));
  };

  const enabledIds = new Set((enabled ?? []).map((plugin) => plugin.id));
  const available = catalog.filter((preset) => !enabledIds.has(preset.id));
  return <section className="advanced-card advanced-card-pad" aria-labelledby="advanced-plugins-title">
    <div className="advanced-card-row">
      <span className="advanced-icon"><Puzzle aria-hidden="true" /></span>
      <div className="advanced-grow">
        <h2 className="advanced-card-title" id="advanced-plugins-title">Plugins</h2>
        <p className="advanced-card-description">Add features to CARE. Each change backs up the clinic first, restarts CARE,
          and puts everything back if the plugin fails. A plugin's screens are turned on in CARE's admin dashboard.</p>
      </div>
      <Button type="button" disabled={blocked} onClick={() => setDraft(draftFor(undefined, undefined))}>
        <Plus aria-hidden="true" className="size-4" />Add plugin…
      </Button>
    </div>
    {enabled === null && !problem ? <div className="advanced-busy" role="status"><Spinner />Reading plugins…</div> : null}
    <AdvancedError problem={problem} />
    {enabled !== null ? <ul className="plugin-list" aria-label="Enabled plugins">
      {enabled.length === 0 ? <li className="plugin-empty">No plugins are enabled.</li> : null}
      {enabled.map((plugin) => {
        const preset = presetFor(catalog, plugin.id);
        return <li key={plugin.id} className="plugin-row">
          <div className="advanced-grow">
            <div className="advanced-setting-label">{preset?.label ?? plugin.id}
              <span className="plugin-tag">{isPresetSource(preset, plugin) && preset?.bundled ? "Included" : "Custom build"}</span>
            </div>
            <p className="plugin-source">{plugin.package}{plugin.version ? ` ${plugin.version}` : ""}</p>
          </div>
          <Button type="button" variant="ghost" size="sm" disabled={blocked} aria-label={`Settings for ${plugin.id}`}
            onClick={() => setDraft(draftFor(preset, plugin))}><Settings2 aria-hidden="true" className="size-4" />Settings</Button>
          <Button type="button" variant="ghost" size="sm" disabled={blocked} aria-label={`Remove ${plugin.id}`}
            onClick={() => setRemoving(plugin)}><Trash2 aria-hidden="true" className="size-4" />Remove</Button>
        </li>;
      })}
    </ul> : null}
    {enabled !== null && available.length > 0 ? <>
      <h3 className="plugin-heading">Available</h3>
      <ul className="plugin-list" aria-label="Available plugins">
        {available.map((preset) => <li key={preset.id} className="plugin-row">
          <div className="advanced-grow">
            <div className="advanced-setting-label">{preset.label}</div>
            <p className="advanced-setting-help">{preset.description}</p>
          </div>
          <Button type="button" variant="soft" size="sm" disabled={blocked} aria-label={`Add ${preset.label}`}
            onClick={() => setDraft(draftFor(preset, undefined))}><Plus aria-hidden="true" className="size-4" />Add</Button>
        </li>)}
      </ul>
    </> : null}
    {draft ? <PluginDialog draft={draft} working={starting} disabled={busy || lock.disabled}
      onCancel={() => setDraft(null)} onSave={(plugin) => void enable(plugin)} /> : null}
    <AlertDialog open={removing !== null} onOpenChange={(open) => { if (!open) setRemoving(null); }}>
      <AlertDialogContent className="advanced-dialog advanced-dialog-narrow">
        <span className="advanced-icon advanced-icon-danger"><Trash2 aria-hidden="true" /></span>
        <AlertDialogTitle className="advanced-dialog-title">Remove {removing?.id}?</AlertDialogTitle>
        <AlertDialogDescription>CARE restarts without it. Records the plugin saved stay in the database.</AlertDialogDescription>
        <div className="advanced-dialog-foot">
          <Button type="button" onClick={() => setRemoving(null)}>Cancel</Button>
          <Button type="button" variant="destructive" disabled={blocked} onClick={() => { if (removing) void disable(removing); }}>
            <Trash2 aria-hidden="true" className="size-4" />Remove plugin
          </Button>
        </div>
      </AlertDialogContent>
    </AlertDialog>
  </section>;
}

function PluginDialog({ draft: initial, working, disabled, onCancel, onSave }: {
  draft: PluginDraft;
  working: boolean;
  disabled: boolean;
  onCancel: () => void;
  onSave: (plugin: Plugin) => void;
}) {
  const [draft, setDraft] = useState(initial);
  const [error, setError] = useState("");
  const custom = !draft.preset;
  const set = (patch: Partial<PluginDraft>) => { setDraft((current) => ({ ...current, ...patch })); setError(""); };
  const save = () => {
    const { plugin, error: problem } = pluginFrom(draft);
    if (!plugin) { setError(problem); return; }
    onSave(plugin);
  };
  const title = draft.enabled ? `${draft.preset?.label ?? draft.id} settings` : draft.preset ? `Add ${draft.preset.label}` : "Add a plugin";
  return <AlertDialog open onOpenChange={(open) => { if (!open && !working) onCancel(); }}>
    <AlertDialogContent className="advanced-dialog">
      <span className="advanced-icon"><Package aria-hidden="true" /></span>
      <AlertDialogTitle className="advanced-dialog-title">{title}</AlertDialogTitle>
      <AlertDialogDescription>{draft.preset?.description
        ?? "Any CARE backend plugin from PyPI or a git repository. It is built into CARE on this computer, which needs the internet."}</AlertDialogDescription>
      <form className="advanced-dialog-body" noValidate onSubmit={(event) => { event.preventDefault(); save(); }}>
        {custom ? <>
          <Field id="plugin-id" label="Name" hint="The plugin's Python app name, e.g. care_example."
            value={draft.id} disabled={disabled || draft.enabled} onChange={(id) => set({ id })} />
          <Field id="plugin-package" label="Package" hint="A pip package name or git+https://github.com/owner/repo.git"
            value={draft.package} disabled={disabled} onChange={(value) => set({ package: value })} />
          <Field id="plugin-version" label="Version" hint="Optional: ==1.2.0 for pip, @main or @v1.2.0 for git."
            value={draft.version} disabled={disabled} onChange={(version) => set({ version })} />
        </> : null}
        {Object.keys(draft.required).map((key) => isSecret(key)
          ? <AdvancedSecretInput key={key} label={key} value={draft.required[key]} disabled={disabled}
            placeholder={draft.saved.includes(key) ? "Saved - leave blank to keep" : ""}
            onChange={(value) => set({ required: { ...draft.required, [key]: value } })} />
          : <Field key={key} id={`plugin-config-${key}`} label={key} value={draft.required[key]} disabled={disabled}
            onChange={(value) => set({ required: { ...draft.required, [key]: value } })} />)}
        <div className="advanced-field">
          <label htmlFor="plugin-configs">{Object.keys(draft.required).length ? "Other settings" : "Settings"}</label>
          <textarea id="plugin-configs" className="plugin-configs" rows={4} spellCheck={false} disabled={disabled}
            value={draft.extra} placeholder="KEY=value, one per line" onChange={(event) => set({ extra: event.target.value })} />
          <div className="advanced-field-hint">Values are text, or JSON such as ["*.local"] or true. Secrets show as ********; leave them to keep them.</div>
        </div>
        {error ? <p className="advanced-field-error" role="alert">{error}</p> : null}
        <div className="advanced-dialog-foot">
          <Button type="button" disabled={working} onClick={onCancel}>Cancel</Button>
          <Button type="submit" variant="primary" disabled={disabled || working}>
            {working ? <Spinner /> : null}{working ? "Starting…" : draft.enabled ? "Save and restart CARE" : "Add and restart CARE"}
          </Button>
        </div>
      </form>
    </AlertDialogContent>
  </AlertDialog>;
}

function Field({ id, label, hint, value, disabled, onChange }: {
  id: string;
  label: string;
  hint?: string;
  value: string;
  disabled: boolean;
  onChange: (value: string) => void;
}) {
  return <div className="advanced-field">
    <label htmlFor={id}>{label}</label>
    <Input id={id} value={value} disabled={disabled} autoComplete="off" spellCheck={false}
      aria-describedby={hint ? `${id}-hint` : undefined} onChange={(event) => onChange(event.target.value)} />
    {hint ? <div id={`${id}-hint`} className="advanced-field-hint">{hint}</div> : null}
  </div>;
}
