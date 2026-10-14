import { readFileSync } from "node:fs";
import { spawnSync } from "node:child_process";

// Environment variables take priority over Vite's dotenv files. JSON avoids
// dotenv interpolation changing literal dollars, quotes or explicit empties.
const env = JSON.parse(readFileSync("/app/clinic.json", "utf8"));
for (const [key, value] of Object.entries(env)) {
  if (!/^REACT_[A-Z0-9_]+$/.test(key) || typeof value !== "string") {
    throw new Error(`Invalid compiled frontend setting: ${key}`);
  }
}
const result = spawnSync("npm", ["run", "build"], {
  env: { ...process.env, ...env }, stdio: "inherit",
});
if (result.error) throw result.error;
process.exit(result.status ?? 1);
