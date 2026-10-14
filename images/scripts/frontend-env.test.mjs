import assert from "node:assert/strict";
import { chmodSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { spawnSync } from "node:child_process";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";
import { API_PLACEHOLDER, frontendBuild } from "./frontend-env.mjs";

test("every setting except the hard-coded API origin is compiled, including empty values", () => {
  const env = { REACT_APP_TITLE: "Clinic", REACT_EMPTY: "", REACT_CARE_API_URL: "https://north.local" };
  const build = frontendBuild(env);
  assert.deepEqual(build.build_env, { REACT_APP_TITLE: "Clinic", REACT_EMPTY: "" });
  assert.deepEqual(build.compiled, { ...build.build_env, REACT_CARE_API_URL: API_PLACEHOLDER });
  assert.equal(Object.hasOwn(build, "runtime_keys"), false);
  assert.deepEqual(env, { REACT_APP_TITLE: "Clinic", REACT_EMPTY: "", REACT_CARE_API_URL: "https://north.local" });
});

test("build inputs preserve literal dollars, quotes, Unicode and explicit empties", () => {
  const input = { REACT_TITLE: "it's \"quoted\" `value` $HOME ${X} \\ \n हिन्दी", REACT_BLANK: "" };
  assert.deepEqual(JSON.parse(JSON.stringify(frontendBuild(input).compiled)), { ...input, REACT_CARE_API_URL: API_PLACEHOLDER });
  for (const invalid of [{ SECRET: "bad" }, { REACT_VALUE: false }, { "REACT_A;B": "bad" }]) {
    assert.throws(() => frontendBuild(invalid), /Invalid compiled frontend setting/);
  }
});

test("the startup script owns exactly the one API URL placeholder", () => {
  const script = readFileSync(new URL("../frontend/40-care-api-url.sh", import.meta.url), "utf8");
  assert.ok(script.includes(API_PLACEHOLDER));
  assert.equal((script.match(/__CARE_CLINIC_/g) ?? []).length, 1);
});

test("startup substitutes only the API URL from a pristine bundle and rejects unsafe origins", () => {
  const root = mkdtempSync(join(tmpdir(), "care-api-url-"));
  try {
    const source = join(root, "source");
    const target = join(root, "target");
    const bin = join(root, "bin");
    for (const dir of [source, target, bin]) mkdirSync(dir);
    const bundle = `const api="${API_PLACEHOLDER}"; const title="Clinic";`;
    writeFileSync(join(source, "app.js"), bundle);
    writeFileSync(join(source, "app.js.gz"), "stale compressed bundle");
    let script = readFileSync(new URL("../frontend/40-care-api-url.sh", import.meta.url), "utf8")
      .replaceAll("/care-clinic/html", source).replaceAll("/usr/share/nginx/html", target);
    // macOS's sed needs an explicit empty backup suffix; the image uses BusyBox.
    if (process.platform === "darwin") {
      writeFileSync(join(bin, "sed"), '#!/bin/sh\nshift\nexec /usr/bin/sed -i "" "$@"\n');
      chmodSync(join(bin, "sed"), 0o700);
    }
    const startup = url => spawnSync("sh", ["-c", script], {
      env: { ...process.env, PATH: `${bin}:${process.env.PATH}`, REACT_CARE_API_URL: url }, encoding: "utf8",
    });
    for (const host of ["north.local", "south.local"]) {
      const result = startup(`https://${host}`);
      assert.equal(result.status, 0, result.stderr);
      assert.equal(readFileSync(join(target, "app.js"), "utf8"), bundle.replace(API_PLACEHOLDER, `https://${host}`));
      assert.throws(() => readFileSync(join(target, "app.js.gz")));
      assert.equal(readFileSync(join(source, "app.js"), "utf8"), bundle);
    }
    for (const invalid of ["", "http://north.local", "https://north.local/other", "https://a&b", "https://$(id)"]) {
      assert.notEqual(startup(invalid).status, 0, invalid);
    }
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});
