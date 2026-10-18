# Update coordinator

`app/internal/updates` is the only code that changes the installed desktop,
engine and CARE versions. It checks the signed feed ([updates-feed.md](updates-feed.md)),
plans an update with `feed.ResolvePlan`, pre-downloads it, and applies it one
component at a time through a crash-safe journal, rolling back on failure.
Nothing is applied automatically: the caller (app UI, or
`care-clinic-service ctl update`) decides when to `Check`, `Apply`, `Resume`
or `Rollback`.

It talks to the outside world through three interfaces, so every step and
failure point is tested with fakes (`updates/updatestest`):

| Interface | Real implementation | Used for |
| --- | --- | --- |
| `Service` | `*control.Client` | engine slots: list, install (inactive), activate |
| `Agent` | `ClientAgent` (control client: agent relay and backups) | status, `stack.stop/up`, `migrations.run`, backup, restore |
| `Stack` | `RuntimeStack` (`stack.Runtime` + deployment kit) | load/pull images, write the env, health, DB size |

## State

All in `Options.Dir` (the CLI uses `<user config>/care-clinic/updates`),
written atomically (temp file, fsync, rename):

| File | Contents |
| --- | --- |
| `verified.json` | the last feed that verified, byte for byte with its signature (re-verified on every use), and the highest revision seen |
| `installed.json` | what the coordinator last committed: engine version/data_schema, CARE version and every pinned image digest |
| `journal.json` | the operation in flight, its steps and rollback data; absent when idle |
| `history.json` | finished operations: `applied`, `rolled_back` or `rollback_failed` (last 200) |
| `downloads/` | verified installers, engine bundles and image tarballs |
| `lock` | held while an operation runs (one at a time, across processes) |

## Check

1. Fetch `updates.json` and `updates.json.sig` (a 404 for the signature means
   unsigned).
2. Verify: bad signature, unknown key, a missing signature (unless the build
   has no production key; see [unsigned feeds](updates-feed.md#trust-model)), revision lower than `highest_revision`,
   expired or invalid feeds are rejected and `verified.json` is left alone.
3. Plan (desktop → engine → CARE). No path yields `Available: false` with the
   reason; nothing newer yields "everything is up to date".
4. Pre-fetch: download and verify the desktop installer, download the engine
   bundle and install it into an inactive slot, download CARE image tarballs
   and load/pull them into the running engine. A tarball must have been saved
   under the repository its `ref` names (docker resolves the stack's
   `repo@digest` by repository), else `ErrImageRepo`. Failures are reported in
   `PrefetchError` and retried by `Apply`.

## Apply

`Apply` re-verifies `verified.json`, re-plans, and runs each step of the plan
as its own journaled operation. Each step of an operation runs at most 3 times
(`MaxAttempts`), then the operation is rolled back.

### CARE (F6)

| Step | Does | Journal |
| --- | --- | --- |
| `preflight` | clock not behind the feed, free disk ≥ 2 × DB + image reserve, stack healthy (updates only) | |
| `backup` | `backup.run`, then export the set to the host's `backup_dir` (skipped on a fresh install) | `backup_id` |
| `images` | download tarballs, load/pull every image | |
| `stop-app` | `stack.stop` backend, celery-worker, celery-beat; db stays up | `stack_stopped` |
| `write-env` | render the kit pinned to the new digests and upload it | `env_written` |
| `migrate` | `migrations.run` | `db_migrated` (set before it starts) |
| `start-stack` | `stack.up` and wait | |
| `health` | services running and `https://localhost/ping/` | |

Rollback undoes exactly what the journal says happened:

- failure before `stop-app`: nothing to undo;
- after `stop-app`: previous env if it was written, then `stack.up` and health;
- after `migrate` started: stop the app, previous env, restore `backup_id`
  (upload the host set and recovery key, `db.restore`), `stack.up`, health.
  The env goes back first because `db.restore` migrates and starts the stack
  with whatever env is written;
- a failed fresh install: stop the app.

### Engine (F7)

| Step | Does | Journal |
| --- | --- | --- |
| `stage-slot` | install the bundle into a new slot if Check did not | |
| `stop-stack` | `stack.stop` (when CARE is installed) | `stack_stopped` |
| `activate` | switch the slot pointer and boot | `engine_activated`, `previous_engine` |
| `boot` | wait for `/v1/status` to report the new version with docker ready, and the feed's `data_schema` (≤ the app's supported maximum) | |
| `start-stack`, `health` | when CARE is installed | |

Rollback re-activates the previous slot and waits for it, then restarts the
stack. `data.img` is never rolled back: engines must keep data compatible
within a `data_schema`.

### Desktop (F8)

Only planned and downloaded: `Apply` stops at a desktop step with
`ErrDesktopUpdate` and the verified installer's path. Installing it (and
swapping the running app) is the caller's job.

## Crashes and failures

- Every step and every rollback flag is saved before it acts, so after a
  crash the journal says what may have happened. Steps are idempotent.
- On start (`Open`) or before any operation, a journal left `running` becomes
  `uncertain` (`rolling_back` becomes `rollback_uncertain`). `Apply` refuses
  to run (`ErrInFlight`) until `Resume` (continue forward, or continue the
  rollback) or `Rollback` (undo) is called.
- A rollback that fails 3 times leaves the journal `failed` and records
  `rollback_failed`; `Rollback` retries it.

## Command line

```sh
care-clinic-service ctl [--dev] update check  --desktop-version 0.2.0 --feed http://localhost:8787/current/updates.json
care-clinic-service ctl [--dev] update apply  --desktop-version 0.2.0 --deployments deployments
care-clinic-service ctl [--dev] update status|history|resume|rollback --desktop-version 0.2.0
```

Release builds trust only the release keys; build with `-tags devfeed` to use
the [demo feed](../demo/README.md).

## Tests

- `app/internal/updates`: every failure point of both operations (rolled back
  to a healthy clinic), a crash before and after every step followed by
  `Resume` or `Rollback`, crashes during rollback, rollback failure and
  retry, retries, feed rejection, locking.
- `app/cmd/release` `TestDemoWithCoordinator`: demo revisions 1–7 in order,
  served over HTTP: fresh install, CARE update, engine slot swap, broken CARE
  rolled back via the database restore, a replayed revision and a tampered
  signature rejected, and "update not available" with its reason.

## Adoption

A clinic installed without the coordinator (e.g. `ctl clinic install`) has an
empty `installed.json`. On `Check` and `Apply` the coordinator adopts it: the
engine from the service's current slot, and CARE by matching the running
backend and frontend image digests (`stack.status`) against the feed's CARE
releases. If the running images match no release, the update is refused
(`ErrUnknownCare`, reported as "not available") instead of overwriting an
unknown stack. A stack with no CARE services is a fresh install.

## Known gaps

- The database size is estimated from the newest complete backup set.
- Support-image-only changes (postgres, redis, silo) are applied with the next
  CARE update, not on their own.
