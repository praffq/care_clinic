# care-clinic-service

The root host service that keeps CARE running without the desktop app. It
owns the engine VM (installs engine slots, restarts the VM after crashes or
an unresponsive agent), forwards the host's ports into the VM, advertises
`<name>.local`, and serves the control API on
`/var/run/care-clinic/control.sock` (members of the `care-clinic` group).

| Host listener      | Guest (vsock) | Used for                |
|--------------------|---------------|-------------------------|
| `0.0.0.0:80`       | 8080          | HTTP (redirect to HTTPS)|
| `0.0.0.0:443`      | 8443          | HTTPS                   |
| `127.0.0.1:8480`   | 8480          | `tailscale serve` target|

If a port is taken, the service keeps running, logs the owning process and
reports it under `forward` in `ctl status`, and retries every 10 s.
`proxy_protocol` in `config.json` prefixes every forwarded connection with a
PROXY v2 header (off until Caddy is configured for it; installing a clinic
turns it on). Changing it through the control API applies to new connections
at once; open ones are unaffected.

## Clinic stack

Once the agent is ready after a boot, the service runs `stack.up` (waiting
for healthy) when a clinic is installed, so CARE comes back without the app.
It never migrates or updates. Before shutting the VM down it runs
`stack.stop`. `ctl status` reports the outcome under `engine.stack.state`:
`not_installed`, `starting`, `running`, `failed` (with `error`), `stopping`
or `stopped`; per-container state is `ctl clinic status`.

Install a clinic without the app from dev images (`scripts/dev-images.sh`),
run from the repo root so `deployments/` is found:

```sh
B='/Library/Application Support/care-clinic/bin/care-clinic-service'
"$B" ctl clinic install --images dist/images --admin admin --admin-password '...'
"$B" ctl clinic status
"$B" ctl clinic logs caddy 50
"$B" ctl restart                       # the stack comes back on its own
```

`install` renders `deployments/` for `<mdns_name>.local`, loads the images
(digest-verified by the agent), uploads the config, runs migrations and
starts the stack. Secrets are generated once and kept in
`~/Library/Application Support/care-clinic/clinic/secrets.env`; see
`internal/stack`.

## Backups

The stack's `backup` container (`deployments/scripts/backup.sh`) takes a set
every night at local midnight and on demand (`backup.run`): a `pg_dump -Fc`
of the database and a tarball of the Silo/minio files, each encrypted
(`openssl cms`, AES-256) to the clinic's backup certificate, plus a
`manifest.json` with every file's sha256. Sets are built under
`/var/lib/care/backups/.partial-<id>/` in the VM and renamed to `<id>/` when
complete.

The service copies sets out on its own, app open or not: every 5 minutes and
just after midnight it lists the engine's complete sets (oldest first) and
for each one

1. streams every file into `<backup_dir>/<id>.partial/` (the agent sends
   the sha256, the service checks it while writing);
2. writes `manifest.json`, re-hashes everything from disk and renames the
   folder to `<id>/`;
3. writes `export-state.json` (`last_exported`, `verified_at`, `last_error`);
4. deletes older host sets (only once the new one is verified, so a good
   set is always on the host), then deletes the set in the engine.

A failure leaves the set in the engine and retries 5 minutes later;
`ctl status` (under `backups`) then says `not copied out since <time>`, and
more than 2 sets waiting in the engine sets `critical`. A crash between the
rename and the engine delete is safe: a verified `<id>/` is reused, not
downloaded again. `backup_dir` defaults to
`/Library/Application Support/care-clinic/backups`.

The certificate and its recovery key are created by `ctl clinic install`
(`backup-cert.pem` and `backup-recovery-key.pem` next to `secrets.env`).
**Keep the recovery key somewhere other than this Mac**: without it no
backup can be read.

```sh
"$B" ctl backup run                # take one now and copy it out
"$B" ctl backup list               # host and engine sets
"$B" ctl backup status
"$B" ctl backup restore daily-20260101T000000Z [--recovery-key key.pem]
```

`restore` verifies the host set, uploads it with the recovery key to the
engine's `restore-input` area, runs `db.restore` and removes the inputs
again. The engine stops the app containers, restores the dump into a new
database and swaps it in, replaces the minio files, migrates and starts the
stack.

## Developer install (LaunchDaemon)

```sh
scripts/service-macos.sh install     # builds as you, then sudo for the rest
```

This creates the `care-clinic` group (adding you), installs the binary to
`/Library/Application Support/care-clinic/bin/` (root-owned), installs
`launchd/org.ohc.care-clinic.plist` and bootstraps it. It then prints the
`ctl install` / `ctl activate` commands for the newest engine bundle in
`dist/` (`engine/build.sh arm64` makes one).

```sh
scripts/service-macos.sh status
scripts/service-macos.sh uninstall           # keeps data.img and slots
scripts/service-macos.sh uninstall --purge   # deletes them too
```

Logs: `/Library/Logs/care-clinic/service.log` (rotated at 10 MiB, 5 kept;
Go crash output goes there too). `launchd.log` next to it only catches
output from before the log file is open.

Show or change the configuration (`config.json` in the service root; set
fields through the control API rather than editing the file, which is read
only at startup):

```sh
B='/Library/Application Support/care-clinic/bin/care-clinic-service'
"$B" ctl config
"$B" ctl config mdns_name=clinic-east       # advertise clinic-east.local
"$B" ctl config proxy_protocol=true
"$B" ctl config backup_dir=/Volumes/ClinicBackup/care   # where backups are copied out to
```

### Without root

`care-clinic-service --dev` runs as you, with state in
`~/Library/Application Support/care-clinic-dev`. Use high ports there:

```sh
care-clinic-service --dev --forward-http 127.0.0.1:18080 \
  --forward-https 127.0.0.1:18443 --forward-home 127.0.0.1:18480
```

A process started from a terminal is subject to macOS Local Network privacy,
so mDNS fails with `sendto: no route to host` unless the terminal app is
allowed under System Settings → Privacy & Security → Local Network. The
LaunchDaemon is not subject to it.

## mDNS: one advertiser per Mac

Only one process may advertise the name. When two advertise it, whichever
stops sends a goodbye and clients drop the name even though the other is
still running. The service is that one process: the desktop app no longer
advertises and only checks a name is free before setup. Run the service with
`--mdns=false` only next to an older app that still advertises in-process.

## Reboot test

1. Install as above, install and activate an engine, and wait for
   `ctl watch` to show `"state": "ready"`.
2. Quit the CARE Clinic app.
3. Reboot and do not log in (or log in without opening the app).
4. From another device on the LAN: `ping care.local` and
   `curl -k https://care.local/` (needs a clinic stack).
5. On the Mac: `scripts/service-macos.sh status` shows the job running with
   `runs = 1`, and `ctl status` shows the engine ready, all forwards
   listening and `"advertising": true`.
