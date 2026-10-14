import { readFileSync, writeFileSync } from "node:fs";
import { frontendBuild } from "../../images/scripts/frontend-env.mjs";

const plan = JSON.parse(readFileSync("plan.json", "utf8"));
const build = frontendBuild(plan.build_env);
writeFileSync("images/frontend/clinic.json", JSON.stringify(build.compiled, null, 2) + "\n");
writeFileSync("ci/frontend-build.json", JSON.stringify({
  build_env: build.build_env,
}, null, 2) + "\n");
