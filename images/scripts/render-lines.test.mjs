import assert from "node:assert/strict";
import test from "node:test";

import { render } from "./render-lines.mjs";

const digest = "a".repeat(64);
const built = {
  backend: {
    commit: "b".repeat(40),
    image: `ghcr.io/ohc/care-backend@sha256:${digest}`,
    builder: `ghcr.io/ohc/care-builder@sha256:${digest}`,
  },
  frontend: {
    commit: "c".repeat(40),
    image: `ghcr.io/ohc/care-frontend@sha256:${digest}`,
    builder: `ghcr.io/ohc/care-builder@sha256:${digest}`,
    runtime_keys: ["CARE_API_URL"],
  },
};

function renderDate(supported_until) {
  return render({ lines: [{ id: "3.1", supported_until }] }, { "3.1": built });
}

test("render rejects impossible release-line dates", () => {
  for (const date of ["2026-02-30", "2025-02-29", "2026-13-01"]) {
    assert.throws(() => renderDate(date), /supported_until must be a valid/);
  }
});

test("render accepts real dates, including leap day", () => {
  assert.equal(renderDate("2024-02-29").lines[0].supported_until, "2024-02-29");
});
