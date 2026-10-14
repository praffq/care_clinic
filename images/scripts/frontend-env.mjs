#!/usr/bin/env node
// CI gets these values from releasectl's validated env-defaults.yml snapshot.
import { execFileSync } from "node:child_process";
import { writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

export const API_PLACEHOLDER = "https://care-clinic.invalid/__CARE_CLINIC_REACT_CARE_API_URL__";

export function frontendBuild(clinicEnv) {
  const buildEnv = {};
  for (const key of Object.keys(clinicEnv).sort()) {
    if (!/^REACT_[A-Z0-9_]+$/.test(key) || typeof clinicEnv[key] !== "string") {
      throw new Error(`Invalid compiled frontend setting: ${key}`);
    }
    if (key !== "REACT_CARE_API_URL") buildEnv[key] = clinicEnv[key];
  }
  return { build_env: buildEnv, compiled: { ...buildEnv, REACT_CARE_API_URL: API_PLACEHOLDER } };
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  const root = join(dirname(fileURLToPath(import.meta.url)), "..", "..");
  const args = process.argv.slice(2);
  const opt = name => { const i = args.indexOf(name); return i < 0 ? undefined : args[i + 1]; };
  const staged = JSON.parse(execFileSync("go", ["run", "./cmd/stageconfig"], { cwd: join(root, "app"), encoding: "utf8" }));
  const build = frontendBuild(staged.build_env);
  writeFileSync(opt("--out") ?? join(root, "images/frontend/clinic.json"), JSON.stringify(build.compiled, null, 2) + "\n");
  if (opt("--json")) writeFileSync(opt("--json"), JSON.stringify({ build_env: build.build_env }, null, 2) + "\n");
}
