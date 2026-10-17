# Release pins: `versions.yml`

`versions.yml` at the repository root is the single human-edited file that says
what a CARE Clinic release is made of: the desktop app version, the engine (the
CARE Clinic VM) version, the CARE backend and frontend sources, the third-party
container images, and any host-side tools the desktop downloads. A later step of
the release pipeline will turn it into a signed `updates.json`; this file is the
source of truth that step reads.

```yaml
schema: 1
desktop:
  version: "0.2.0"
  requires: { engine: ">=0.1.0 <0.2.0" }
engine:
  version: "0.1.0"
  requires: { desktop: ">=0.2.0" }
care:
  version: "3.1.0"
  requires: { desktop: ">=0.2.0", engine: ">=0.1.0" }
  backend:  { repo: "ohcnetwork/care",    ref: "develop" }
  frontend: { repo: "ohcnetwork/care_fe", ref: "develop" }
images:
  postgres: "postgres:17.10-alpine"
  ...
tools:
  qemu_windows_x86_64: { version: "10.1.0", url: "https://...", sha256: "..." }
```

Every `version` is a [semantic version](https://semver.org) without a leading
`v` (`+build` metadata is allowed) and **must be quoted**: YAML reads `0.2` as a
float, so unquoted versions are rejected. `requires` entries are ranges made of
space-separated comparators that must all hold, using `=`, `>`, `>=`, `<` and
`<=` (for example `">=0.1.0 <0.2.0"`). `desktop.requires.engine`,
`engine.requires.desktop` and both `care.requires` entries are mandatory; a
component cannot require itself.

The remaining fields are checked as well: `schema` must be `1`; `repo` values
are GitHub `owner/name`; `ref` must not be empty; every image must pin a tag or
digest and may not use the floating `latest` or `edge` tags; tool URLs must be
`https` and `sha256` must be 64 lowercase hex characters. Unknown keys anywhere
in the file are errors, so typos fail loudly rather than being ignored.

The file is parsed by `app/internal/versions` (`versions.Load` / `versions.Parse`),
which reports every problem at once with its field path, for example
`care.backend.ref: required`. Its tests also load the real `versions.yml`, so
`cd app && go test ./internal/versions/...` validates an edit before it is
committed.
