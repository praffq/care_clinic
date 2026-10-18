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
PROXY v2 header (off until Caddy is configured for it). Changing it through
the control API applies to new connections at once; open ones are unaffected.

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
still running. Until the desktop app stops advertising in-process, run the
service with `--mdns=false` on a Mac where that app is in use, or quit the
app.

## Reboot test

1. Install as above, install and activate an engine, and wait for
   `ctl watch` to show `"state": "ready"`.
2. Quit the CARE Clinic app and make sure it does not start at login.
3. Reboot and do not log in (or log in without opening the app).
4. From another device on the LAN: `ping care.local` and
   `curl -k https://care.local/` (needs a clinic stack).
5. On the Mac: `scripts/service-macos.sh status` shows the job running with
   `runs = 1`, and `ctl status` shows the engine ready, all forwards
   listening and `"advertising": true`.
