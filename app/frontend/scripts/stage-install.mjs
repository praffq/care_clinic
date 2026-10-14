// Stage deployments/ into ../install so Go can embed it (//go:embed all:install).
// Runs from the Wails pre-build hook (including dev) and standalone frontend builds.
// deployments/ and the root versions.toml are the single source of truth;
// nothing is duplicated in git.
//
// install/.env is generated: deployments/.env (non-release defaults) plus the
// release pins from versions.toml, rendered and validated by the Go command
// app/cmd/stageconfig. TOML is never parsed here.
//
// The list is read from the directory rather than hardcoded: a hand-maintained
// array silently drifts, and a file added to the stack but missed here goes
// missing at runtime on the clinic's machine, not at build time here.
import { execFileSync } from "node:child_process";
import { cpSync, mkdirSync, readFileSync, readdirSync, rmSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url)); // app/frontend/scripts
const app = join(here, "..", ".."); // app
const root = join(app, "..");
const source = join(root, "deployments");
const install = join(app, "install");

// Validate and render before touching install/, so a bad versions.toml leaves
// the previously staged kit intact.
let staged;
try {
  const output = execFileSync(
    "go",
    ["run", "./cmd/stageconfig", "-config", join(root, "versions.toml"), "-env", join(source, ".env")],
    { cwd: app, encoding: "utf8", stdio: ["ignore", "pipe", "pipe"] },
  );
  staged = JSON.parse(output);
} catch (err) {
  throw new Error(`versions.toml could not be staged:\n${err.stderr || err.message}`);
}
if (typeof staged.desktop_version !== "string" || typeof staged.env !== "string") {
  throw new Error("stageconfig returned an unexpected result");
}
const version = staged.desktop_version.replace(/-dev$/, "");
const metadataPath = join(app, "wails.json");
const metadata = JSON.parse(readFileSync(metadataPath, "utf8"));
if (metadata.info.productVersion !== version) {
  metadata.info.productVersion = version;
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
writeFileSync(join(install, ".env"), staged.env);
console.log(`staged ${items.length} entries (desktop ${staged.desktop_version}) → app/install`);
