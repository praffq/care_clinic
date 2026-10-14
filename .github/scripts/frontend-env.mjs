import { readFileSync, writeFileSync } from "node:fs";
import { frontendBuild } from "../../images/scripts/frontend-env.mjs";

const plan = JSON.parse(readFileSync("plan.json", "utf8"));
const runtimeKeys = JSON.parse(readFileSync("images/frontend/runtime-keys.json", "utf8"));
const build = frontendBuild(plan.build_env, runtimeKeys);
writeFileSync("images/frontend/clinic.env", build.dotenv);
writeFileSync("ci/frontend-build.json", JSON.stringify({
  runtime_keys: build.runtime_keys,
  build_env: build.build_env,
}, null, 2) + "\n");
