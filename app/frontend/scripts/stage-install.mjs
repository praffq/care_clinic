// Stage deployments/ into ../install so Go can embed it (//go:embed all:install).
// Runs from the Wails pre-build hook (including dev) and standalone frontend builds.
// deployments/ is the single source of truth; nothing is duplicated in git.
// The staged .env also gets CARE_CLINIC_VERSION (versions.yml desktop.version).
//
// The list is read from the directory rather than hardcoded: a hand-maintained
// array silently drifts, and a file added to the stack but missed here goes
// missing at runtime on the clinic's machine, not at build time here.
import { cpSync, mkdirSync, readFileSync, readdirSync, rmSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url)); // app/frontend/scripts
const root = join(here, "..", "..", "..");
const source = join(root, "deployments");
const install = join(here, "..", "..", "install"); // app/install

// The desktop version is versions.yml desktop.version (docs/versions.md). It
// is written into wails.json (installer and bundle metadata) and, as
// CARE_CLINIC_VERSION, at the top of the staged .env, where the app reads it.
const block = readFileSync(join(root, "versions.yml"), "utf8").match(/^desktop:[^\S\r\n]*\r?\n((?:(?:[ \t]+[^\r\n]*|[ \t]*)\r?\n)*)/m);
const versions = block ? [...block[1].matchAll(/^  version:[ \t]*"?([^"\s#]*)"?[ \t]*(?:#.*)?$/gm)] : [];
if (versions.length !== 1 || !/^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-dev)?$/.test(versions[0][1])) {
  throw new Error("versions.yml must contain one desktop.version X.Y.Z or X.Y.Z-dev");
}
const version = versions[0][1];
const env = readFileSync(join(source, ".env"), "utf8");
if (/^CARE_CLINIC_VERSION=/m.test(env)) {
  throw new Error("deployments/.env must not set CARE_CLINIC_VERSION; the desktop version is versions.yml desktop.version");
}
const metadataPath = join(here, "..", "..", "wails.json");
const metadata = JSON.parse(readFileSync(metadataPath, "utf8"));
const productVersion = version.replace(/-dev$/, "");
if (metadata.info.productVersion !== productVersion) {
  metadata.info.productVersion = productVersion;
  writeFileSync(metadataPath, JSON.stringify(metadata, null, 2) + "\n");
}

// .env must be staged: Compose auto-loads it from the project dir, and it is
// what pins every image. Only OS/editor junk is skipped.
const SKIP = new Set([".DS_Store", "Thumbs.db", ".gitkeep"]);
const items = readdirSync(source).filter((name) => !SKIP.has(name));
if (items.length === 0) throw new Error(`nothing to stage: ${source} is empty`);

// Clear first, so a file deleted from deployments/ cannot linger in an install
// dir staged by an earlier build. .gitkeep is tracked and must survive.
mkdirSync(install, { recursive: true });
for (const stale of readdirSync(install)) {
  if (stale !== ".gitkeep") rmSync(join(install, stale), { recursive: true, force: true });
}
for (const item of items) {
  cpSync(join(source, item), join(install, item), { recursive: true });
}
writeFileSync(join(install, ".env"), `CARE_CLINIC_VERSION=${version}\n` + env);
console.log(`staged ${items.length} entries → app/install`);
