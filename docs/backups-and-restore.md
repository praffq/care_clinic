[Documentation index](README.md)

# Backups and restore

This guide explains the backup and restore part of the Wails backend: what is
saved, how encryption works, how a replacement clinic is prepared, and how the
application decides what to do after an interrupted restore.

The main implementation is
[`app/internal/backup/`](../app/internal/backup), wired into the clinic by
[`backupstore.go`](../app/internal/clinic/backupstore.go). Backup creation runs
through [`deployments/scripts/backup.sh`](../deployments/scripts/backup.sh).
The source-file map at the end includes every Go file in this guide's scope,
including tests.

This is an explanation of the current code, not evidence of a completed disaster
recovery exercise. All example paths, dates, identifiers, and data below are
synthetic. Do not experiment against the only copy of a clinic.

Related guides:

- [Desktop workflows](desktop-workflows.md): the operator-facing Backups page,
  restore dialog and Advanced unlock behavior.
- [Architecture](architecture.md) and [repository map](repository-map.md):
  where this subsystem fits.
- [Wails application](wails-application.md): desktop calls, authorization, jobs,
  events, and the shared-read/exclusive-write application gate.
- [Configuration and settings](configuration-and-settings.md): effective paths,
  passwords, the optional backup copy destination, and changing the backup
  destination.
- [Clinic lifecycle](clinic-lifecycle.md): starting services, live migrations,
  worker sequencing, and deployment services.
- [Cleanup and uninstall](cleanup-and-uninstall.md): explicit deletion choices
  and preservation of recovery material.
- [Native integrations](native-integrations.md): the process runner, operating
  system credential storage, and native file behavior.
- [Development and release](development-and-release.md): images, pins, builds,
  and interpreting test results.

## 1. The mental model

A clinic has two different kinds of clinical data:

1. **The database** stores structured records in PostgreSQL.
2. **Uploaded files** live in the storage service's Docker volume. A database
   reference to an uploaded object is not the object's contents.

The storage implementation is now **Silo**, but its Compose service is still
named `minio`, and its normal data volume is still
`care-clinic_minio-data`. Backup and recovery intentionally use those existing
names. They are not instructions to rename a service or migrate a volume.

| Term | Meaning in this implementation |
| --- | --- |
| Verified backup set | The single kept set: encrypted physical full copy (`base-<ts>`), database dump, files archive and `manifest.json`. A new set replaces the previous one only after every check passes (section 4). |
| "Back up now" | Runs the same verified cycle immediately. |
| Change files (WAL) | PostgreSQL write-ahead log segments, compressed and encrypted to the clinic certificate in `backups/wal`, used to replay changes after the full copy. |
| Point-in-time restore | Rebuilds the database from the full copy plus change files up to a chosen moment, then swaps it in through the staged restore. |
| Optional copy | A mirror of the verified set and change files on a USB drive, second disk or network folder. |
| Restore source | The folder containing the selected dump, optional files archive, and preferably their recovery key. It can differ from the current backup destination. |
| Staging | Preparing replacement data separately from the current database and uploaded files. |
| Cutover | Stopping writers, replacing the live files when requested, and renaming databases so the staged database takes the live name. |
| Activation | Bringing the Compose stack up after a committed cutover and waiting for health. |
| Recovery journal | Local metadata describing an unfinished restore. It is not another copy of the clinical data. |
| Retained originals | The previous database and, for a DB + files restore, a separate copy of the pre-cutover uploaded files. |

A database-only restore leaves the contents of the uploaded-files volume alone.
That can still produce a mismatch between restored database references and the
files currently present. Choose the restore scope deliberately.

### Who performs which work?

```mermaid
flowchart TD
    UI["Desktop request"] --> App["Wails App boundary: authorize and coordinate"]
    App --> Clinic["Clinic"]
    Clinic --> Now["BackupNow"]
    Now --> Sidecar["Existing backup container"]
    Sidecar --> Script["backup.sh: full copy, dump, files, verify, manifest, prune previous"]
    DB["PostgreSQL archive_command"] --> WAL["wal-archive.sh: compress, encrypt, temp then rename"]
    App --> Copier["backupcopy: optional verified second copy"]
    Clinic --> Store["backup.Store"]
    Store --> Local["Go: names, paths, key copies, journal"]
    Store --> Helpers["Docker and Compose helper processes"]
    Helpers --> PG["PostgreSQL staging and cutover"]
    Helpers --> Volumes["Staging, previous, and live files volumes"]
    Store --> Callback["Clinic-supplied staged migration callback"]
    App --> Recovery["Native recovery-file picker"]
```

The Go package does not implement a PostgreSQL server, storage server, encryption
engine, or scheduling service. It validates inputs, records local state, and
orchestrates external commands. Docker, PostgreSQL tools, OpenSSL, Python, and
the shell do the corresponding external work.

The desktop's application gate is separate from these mechanisms. It now allows
authenticated read operations under a shared `RWMutex` read gate and reserves
protected writes for the exclusive gate. `backup.Store` does not contain that
gate or independently serialize every possible caller. Likewise, the shell
backup lock described below is not a global lock for every restore, settings
change, or deletion.

## 2. Files, names, and locations

### Backup artifacts

The examples use the synthetic timestamp `20260102-030405`.

| Name | Contents |
| --- | --- |
| `base-20260102-030405/base.tar.gz.enc` | Encrypted `pg_basebackup` physical full copy of the verified set. |
| `care-20260102-030405.dump.enc` | PostgreSQL custom-format dump of the verified set, encrypted as an OpenSSL CMS envelope. |
| `files-20260102-030405.tar.gz.enc` | Matching encrypted gzip-compressed tar archive of the storage volume. |
| `manifest.json` | Record of the verified set: checksums, checks passed, base LSNs and `ended_at`, WAL start, trial restore. |
| `wal/<segment>.gz.enc` and `wal/<segment>.sha256` | Encrypted change file and its checksum/verification record. `wal/<timeline>.history` files are kept. |
| `care-pre-update-20260102-030405.dump.enc` / `files-pre-update-…` | Newest pre-update safety copy only. |
| `care-manual-20260102-030405.dump.enc` / `files-manual-…` | Older manual names, still recognized as restore input. Current writers no longer create them. |
| `care-20260102-030405.dump` | Recognized unencrypted database dump, supported as restore input. Current backup writers do not create this final form. |
| `files-20260102-030405.tar.gz` | Recognized unencrypted files archive, also supported as restore input. |
| `backup-cert.pem` | Public certificate identifying the backup encryption key. It cannot decrypt backups. |
| `.backup.lock` | Lock file shared by scheduled and manual writers in this folder. Its existence does not itself mean a writer is running. |
| `.care-20260102-030405.dump.tmp` | Intermediate plaintext database dump during backup creation. |
| `.care-20260102-030405.dump.tmp.enc` | Intermediate encrypted dump, before publication under its final name. |
| `.files-20260102-030405.tar.gz.tmp` and its `.enc` counterpart | Equivalent intermediate files for the files archive. |

The timestamp format is `YYYYMMDD-HHMMSS`. The filenames contain no timezone.
Manual timestamps come from the Go process; scheduled timestamps come from the
sidecar's `date`. The list sorts by the timestamp embedded in the filename, not
by filesystem modification time.

The suffix `.enc` identifies encrypted backup content. The separate
`CARE-<clinic>-backup-recovery-<UTC timestamp>-<random suffix>.pem` file is a
private key, not a backup. Recovery PEM exports and CARE Clinic admin code sheets
use a portable UTC timestamp (including nanoseconds) plus a random suffix in
their suggested filenames, so repeated saves have distinct names. CARE never
overwrites an existing recovery file, even if a native dialog approves it.

During setup, **Save a new recovery file** remains available next to
**Select saved file**, including after saving or verification. This explicitly
replaces the setup key and clears verification: select the new PEM before
continuing. Cancellation or a failed save leaves the previous key active.
Replacement is locked once installation starts.

For an installed clinic, **Backups > Re-download backup key** requires the
current CARE Clinic admin password. Once enrolled, it decrypts the encrypted local
copy even if the original PEM has been deleted or disconnected. Existing
installations without this copy need one successful export from their original
saved PEM, or **Select another saved copy**. The dialog explains that this
enrolls password-only downloads. The key must match both the configured and
installed public certificate before a save dialog opens. No key rotation or
change to earlier backups occurs. Keep the exported copy outside CARE's
settings, installation, logs and backup folder.

Cancellation leaves enrollment unchanged. A failed enrollment persistence
after export explicitly reports that the exported PEM was saved but the local
copy was not confirmed. Preserve that PEM and retry. A corrupt local copy
does not silently fall back to a file: explicitly select a matching PEM to
re-enroll. Keys already lost before this feature was enrolled cannot be
reconstructed.

### Host folders versus container paths

`Store.Dir` is the installation directory. `Store.BackupDir` is the selected
backup destination. With no configured override, the default is
`care-db-backups` inside the Windows Desktop known folder on Windows,
`<user home>/Desktop/care-db-backups` on Linux and macOS, from
[`Clinic.backupDir`](../app/internal/clinic/clinic.go).
The Linux backup default deliberately stays independent of localized XDG
Desktop selection to preserve existing empty-config backup destinations.
Windows respects OneDrive and administrator redirection; it does not assume
the visible Desktop is `C:\Users\<name>\Desktop`. A known-folder lookup failure
is logged before falling back to that legacy path. An explicitly configured
backup destination is never relocated by this lookup.
Use the application's reported effective path rather than assuming that the
executable directory or current shell directory contains the backups.

Windows folder selection and recovery save dialogs start at the visible Desktop.
Setup shows free space without a days/years-of-backups estimate, retains
insufficient-space checks, and offers **Open folder** for saved recovery files
and admin codes. Desktop may be cloud-synced: use a secure separate location for
recovery materials and preferably an external drive for backups. CARE warns
about possible OneDrive syncing, but does not detect or block every cloud folder.

```text
<installation>/
|-- backend.env
|-- backup-state/
|   |-- backup-status        # last cycle: running, ok, or failed and why
|   |-- wal-status           # change-file archiving: state, saved_through, gap
|   `-- copy-status.json     # optional second copy: ok, disconnected, copying, failed
|-- keys/
|   `-- backup-cert.pem
`-- restore-state.json       # exists while a restore needs recovery/finalization

<separate backup folder>/
|-- backup-cert.pem          # public ownership marker, cannot decrypt
|-- manifest.json
|-- base-20260102-030405/base.tar.gz.enc
|-- care-20260102-030405.dump.enc
|-- files-20260102-030405.tar.gz.enc
`-- wal/
    |-- 000000010000000000000004.gz.enc
    `-- 000000010000000000000004.sha256

<optional copy destination>/care-backup-copy/   # same layout, manifest written last
```

The backup service's mounts in
[`docker-compose.yml`](../deployments/docker-compose.yml) provide:

| Container path | Purpose |
| --- | --- |
| `/scripts` | Read-only backup, WAL and point-in-time helper scripts. |
| `/backups` | Writable backup destination supplied through `BACKUP_DIR`. |
| `/keys` | Read-only installation keys directory. The writer uses the certificate, not the private key or its password. |
| `/minio-data` | Read-only mount of the storage data volume for archiving. |
| `/state` | Writable `<installation>/backup-state`, holding `backup-status`, `wal-status` and trial stamps. |
| `/pgdata` | Read-only view of the database volume, used only to check free space for the trial restore. |

The Compose file also has a raw relative backup-path fallback. Running Compose
outside the desktop's configured environment is not equivalent to using the
application's effective backup destination.

[`backup.Dockerfile`](../deployments/backup.Dockerfile) is deliberately small:
it inherits the pinned `POSTGRES_IMAGE` and adds OpenSSL with `apk`. It does not
contain a Go scheduler or bake the mounted script into the image. The runtime
image must also supply the PostgreSQL and shell utilities the script uses,
including `tar`, `find`, and `flock`; the Dockerfile explicitly adds only
OpenSSL.

## 3. Backup recovery file and CARE Clinic admin recovery

### These are not interchangeable

| Item | What it does | What it cannot replace |
| --- | --- | --- |
| `keys/backup-cert.pem` | Public certificate for unattended encryption; also copied into the backup folder as an ownership marker. | The private recovery file. This is not the clinic's HTTPS certificate. |
| `CARE-<clinic>-backup-recovery-<timestamp>-<suffix>.pem` | Private key exported to a location chosen by the clinic manager. Select it to restore encrypted backups. | Backup data, Desktop authorisation, or another clinic's key. |
| CARE Clinic admin password | Authorises protected Desktop operations and unlocks an enrolled encrypted local key copy. | Direct OpenSSL backup decryption without the private key, or the independently stored CARE web login. |
| Six CARE Clinic admin recovery codes | Each unused code can reset the CARE Clinic password offline. | Backup decryption or CARE web password recovery. |
| Database password | Authenticates PostgreSQL commands. | Any recovery material. |

### Setup and custody

There is no separate backup password or OS-keyring entry. During setup,
[`app_recovery.go`](../app/app_recovery.go) uses Go's standard cryptographic
library to generate a random RSA-4096 key and a self-signed X.509 certificate
with `CN=care-backup` and 100-year validity. The PEM private key is compatible
with OpenSSL. The plaintext PEM is written only to the user-selected recovery
file, never to the installation or backup folder. When installation starts,
`RunSetup` uses the finalized admin password to encrypt the verified PEM and
atomically save the encrypted copy with the password hash and other config.
The password is chosen after the backup wizard step, so there is no retained
local copy before installation begins.

### Encrypted local copy: protection and tradeoffs

`app_backup_key.go` uses the existing `golang.org/x/crypto` dependency:
Argon2id (64 MiB memory, three passes, two lanes) derives a 256-bit wrapping
key from the CARE Clinic password and a fresh 16-byte random salt. AES-256-GCM
uses a fresh 12-byte random nonce. Authenticated associated data binds the
versioned envelope to the clinic address and SHA-256 digest of its public
certificate. Fixed versioned parameters and bounded envelope sizes avoid
accepting arbitrary KDF work or malformed nonce sizes from settings.
No plaintext password, wrapping key, or private PEM is persisted in config.

Config writes are atomic and synced before replacement. POSIX files are mode
0600 in a mode-0700-created directory; Windows applies a protected DACL granting
only the current user access to the new file before writing its contents.
The encrypted copy is not installed beside backups or sent to a service.
Private-key byte buffers and derived keys are cleared after use where
practical, but Go/runtime and operating-system memory cannot be guaranteed
forensically erased.

This is a convenience/security tradeoff, not protection against a compromised
computer. A stolen config enables offline password guessing (the existing
bcrypt hash can also assist guessing); choose a strong unique CARE Clinic
password, use OS full-disk encryption, and protect the user account. Malware
running as that user can observe passwords or decrypted keys. Local
administrators can bypass file permissions. Older copies/snapshots of config
can remain unlockable with the old password after a password change.

**Change password**, with the current password, decrypts and re-encrypts the
same private key using a fresh salt/nonce and atomically saves both its new
envelope and new password hash. A key-unlock or persistence failure leaves
the old password and envelope unchanged.

**Forgot CARE Clinic password?**, using a recovery code, cannot decrypt the
old-password envelope. The UI warns before resetting; reset preserves that
ciphertext but marks password-only export as needing re-enrollment from a
surviving PEM. The code consumption, password hash and enrollment flag are
saved together. Re-enrollment does not rotate the backup key. If every PEM is
lost, try recovering the old password before resetting. Recovery codes do not
decrypt or rewrap the local key.

The manager must select the saved file again. CARE parses it and matches its
public key to the clinic certificate before installation is allowed. Recovery
files must be outside the installation, settings, logs and backup directories;
the UI asks the manager to keep them outside the computer and retain a second
secure copy. CARE cannot prove the chosen storage is physically offsite.

Setup presents separate **Saved** and **Verified** completion cards, with
checkmarks and a clear pending action for each step. The six-code sheet has its
own saved confirmation; a cancelled or failed operation does not mark a step done.

**Anyone with the recovery file and the backups can decrypt patient data.**
Keep them separate. An enrolled local copy can replace a lost PEM only while
the matching CARE Clinic password and settings remain available. Losing the
computer/settings, uninstalling, or resetting a forgotten password may remove
that route. Keep an independent off-device PEM even after enrollment. The
recovery file contains no patient-data backup. Uninstall removes the local
config copy but does not delete user-exported recovery materials.

`InstallCertificate` validates and installs only the public certificate.
Copying the same certificate is idempotent; a different certificate or a
symbolic link is not overwritten. Foreign backup folders are rejected.
`CopyBackupCertificate`, `PreserveBackupCertificate` and
`DiscardUnusedCertificate` manage this public ownership marker, not a secret.
`BackupEncryptionOn()` only checks certificate presence, not recoverability.

Exports are exclusive-create files with requested mode `0600`, synced before
success. Existing exports are never overwritten. Keys directories use `0700`.
POSIX modes are not equivalent to Windows ACL guarantees.

### Offline CARE Clinic password recovery

Setup also exports a printable text sheet of exactly six random 128-bit codes,
including clinic name, issue time and usage instructions. The application
stores only SHA-256 hashes of the normalised codes; spaces, hyphens and case
do not affect entry. Plaintext codes are never returned through the UI bridge.

The forgot-password form shows the last saved recovery-sheet path, when recorded,
with a reminder that the file may since have been moved, printed or deleted.
It refreshes that metadata through the server-only `GetAdminRecoveryCodesPath`
query on opening, without reading the sheet or PEM. It does not display codes or setup-oriented
missing-file warnings, and does not block reset if the location lookup fails.
Lookup failures are logged and shown as a nonblocking hint. Ordinary password
change does not request this hint; the backup-PEM enrollment warning is unchanged.

Advanced provides **Forgot CARE Clinic password?**, **Change CARE Clinic password** and
**Replace recovery codes**. A reset atomically changes the bcrypt password hash
and clears the used code hash. Other codes remain usable. Failed writes leave
both unchanged in memory. Five invalid attempts start a persisted cooldown;
further invalid attempts increase it, capped at eleven minutes.
Authenticated replacement saves a new sheet before activating its hashes and
invalidating the entire prior set. Cancelling the dialog changes nothing.

These operations do not require Docker or internet access. They do not change
the CARE web `admin` password, which setup only initialises to the same value.
Local OS-account access remains a separate security boundary: an OS user who
can rewrite the application's configuration is not constrained by this gate.

### Encryption and restore

OpenSSL CMS still encrypts each backup with AES-256-CBC in a streaming DER
envelope addressed to the public certificate. Restore explicitly mounts the
selected recovery file read-only into its preflight helper and decrypts there,
before stopping writers or replacing data. There is no local-key fallback.
Database credentials remain in process environment variables, not argument
values. Private-key contents are not placed in arguments, logs or the journal.

Encryption is not a signed manifest or proof of cross-file consistency.
Only restore backups from trusted sources. Docker administrators and privileged
OS users can inspect mounted secrets and decrypted staging data.

## 4. Latest verified backup, continuous changes, and the optional copy

This section is the operating model from points 44–54 of the
[redesign](redesign.md). In short: CARE keeps **one** verified backup set on
the local backup folder, archives database changes continuously between full
copies, can optionally mirror that set to a second place, and lets an
administrator restore the database to any moment the archive covers. There is
no retention-period setting: `DB_BACKUP_RETENTION_PERIOD`, the Advanced
"Backups" group and the old `GetBackupPolicy()` bridge were removed.

### Latest verified backup set

The sidecar's nightly cycle ([`backup.sh`](../deployments/scripts/backup.sh),
`run_cycle`) writes a complete new set next to the current one:

| Member | Purpose |
| --- | --- |
| `base-<ts>/base.tar.gz.enc` | `pg_basebackup` physical full copy, compressed and CMS-encrypted to the clinic certificate. Starting point for continuous-change replay and point-in-time restore. |
| `care-<ts>.dump.enc` | Daily `pg_dump -Fc`, kept for moves across PostgreSQL major versions or CPU architectures, where a physical copy cannot be used. |
| `files-<ts>.tar.gz.enc` | Uploaded files from the storage volume. |
| `manifest.json` | The record of the verified set: SHA-256 of every member (ciphertext and plaintext), base start/end LSN, `base.ended_at`, starting WAL segment and timeline, the checks that passed, and the latest weekly trial restore. |

The order is fixed: **write → verify → record → only then delete the previous
set**. Before `manifest.json` is replaced, every member must pass all of:

| Check | Meaning |
| --- | --- |
| `checksums` | Each published file is read back against the SHA-256 recorded for it. |
| `decrypt_test` | Each member is encrypted to the clinic certificate **and** a single-use verification key held only in memory-backed storage. The sidecar decrypts with that key, checks the plaintext hash, then deletes the key. The recovery key never enters the sidecar, and the envelope is checked to be addressed to the clinic certificate. |
| `pg_restore_list` | The dump's table of contents is readable. |
| `pg_verifybackup` | The physical full copy matches PostgreSQL's own backup manifest. |
| `pg_waldump` | The WAL range inside the full copy parses from start to end LSN. |
| `wal_chain` | Every archived change file from the copy's start segment to the newest one is present, intact and on the same timeline. |

`manifest.json` is written to a temporary name and renamed into place. Only
after that does `prune_previous` remove the older set and change files that
are wholly older than the **new** full copy's start segment. If any step
fails, the new files are discarded, the previous set and **every** change file
stay, the status records the failure, and Backups raises an alert. If the new
set was recorded but removing the old one failed, the new set is kept and the
failure is still reported.

Pre-update safety copies (`backup.sh once pre-update-<ts>`, used by the update
coordinator's checkpoint) are a database dump plus files archive; only the
newest safety copy is kept.

**Back up now** runs the same verified cycle (`backup.sh now`) inside the
running sidecar; it is not a separate, less-checked path.

### Space checks

Because the previous set stays until the new one verifies, the backup folder
must hold a second encrypted set beside the current one, and the data disk must
hold the plaintext work area. `space_ok` sizes both from the live cluster and
files volume, with a quarter for growth plus 256 MB and a 1 GB floor;
[`storage.BackupNeed`](../app/internal/storage/backup.go) uses the same
latest-set formula for folder choice and the Backups summary ("Room for the
next backup; only the newest verified backup is kept"). A shortfall refuses
before anything is written and records `disk_full`.

### Continuous WAL archive

The database runs with `wal_level=replica`, `archive_mode=on` and
`archive_timeout=300`, set by
[`db-entrypoint.sh`](../deployments/scripts/db-entrypoint.sh). Its
`archive_command` is [`wal-archive.sh`](../deployments/scripts/wal-archive.sh):

1. Compress the segment and encrypt it with `openssl cms` to the **same clinic
   certificate** used for backups.
2. Write `backups/wal/.tmp-*`, flush, verify, then rename to
   `<segment>.gz.enc`, followed by a `<segment>.sha256` record (also via
   temp-then-rename). A name already archived with different contents is
   refused rather than overwritten.
3. Return success to PostgreSQL only after both are published, so PostgreSQL
   keeps the segment until it is safely archived.

Timeline `.history` files are archived and copied but never pruned. The live
clinic never changes timeline (restores swap a database inside the same
cluster, and point-in-time replay happens in a throwaway cluster), so archived
segments on a later timeline are treated as an unsupported branch: the
`wal_chain` check refuses to record a set rather than claim an unbroken chain.

Change files are deleted only after a **new** full copy is verified, and only
those wholly older than that copy's start segment. A failed cycle deletes no
change files.

Every minute the sidecar writes `backup-state/wal-status`
(`state`, `checked_at`, `saved_through`, `last_archived_at`, `last_failed_at`,
`gap`, `message`). `saved_through` is the last moment nothing was waiting to
be archived — the **changes saved through** time shown in Backups.

### Point-in-time restore

**Backups > Restore to a point in time** opens the restore dialog in time mode:
"The latest saved change" or "A specific time". The time is bounded by:

- **Earliest:** `base.ended_at`, when the physical copy became consistent
  (not the start of the copy, which is not yet restorable).
- **Latest:** `saved_through`, never earlier than `base.ended_at`. A moment
  after the last archived change is refused rather than silently restoring to
  an earlier point.

It also requires a verified set, no WAL gap, the recovery file, the CARE
Clinic admin password and replacement acknowledgement.

`RestorePointInTime` ([`pitr.go`](../app/internal/backup/pitr.go)) re-checks
those bounds in Go, then runs
[`pitr-stage.sh`](../deployments/scripts/pitr-stage.sh) in a throwaway
helper container with the backup folder read-only and no network. The helper:

1. Verifies the full copy against the manifest and the WAL chain.
2. Decrypts and extracts the full copy into the staging volume.
3. Starts a private PostgreSQL with a `restore_command` that runs
   [`wal-restore.sh`](../deployments/scripts/wal-restore.sh), which decrypts
   each intact change file with the recovery key, plus
   `recovery_target_time`, `recovery_target_action=promote` and
   `recovery_target_timeline=current`.
4. Waits for promotion. For a time target it requires PostgreSQL's
   "recovery stopping before/after" record; replay that ran out of change
   files fails here.
5. Checks health and dumps the result.

Only then does the normal journaled staged restore (section 6 onwards) load
that dump into a staging database and swap it in. Uploaded files are not
changed by a point-in-time restore. Any failure before the swap leaves the
live database untouched.

### Optional second copy

**Backups > Extra copy (optional)** lets an administrator choose a USB drive,
second disk or network folder (`SetBackupCopyDir` / `ClearBackupCopyDir`,
stored as `BackupCopyDir` in the desktop config). The main backup always stays
on the local backup folder.

On each storage check (about every 5 minutes) the app's copier
([`backupcopy`](../app/internal/backupcopy/copy.go)) mirrors the verified set
and the contiguous WAL chain plus `.history` files into
`<chosen>/care-backup-copy/`:

- If the destination is absent, the state is **disconnected**: nothing is
  created on the parent path, local backups and WAL archiving continue, and
  Backups shows when it was last copied. The copy catches up on reconnect.
- Each file is written to a temporary name, flushed, hashed back and renamed;
  the copy's `manifest.json` is written last.
- Files the new copy no longer needs are deleted only after everything else
  verified.
- The result is recorded in `backup-state/copy-status.json` and emitted as the
  `care-backup-copy` event. A failure to record status is itself raised as an
  alert (`copy_status_unwritable`).

### Weekly trial restore

At most once a week (`CARE_TRIAL_RESTORE_INTERVAL`, default 7 days) the cycle
restores the new dump into a throwaway database, compares per-table row counts
with counts taken before and after the dump, then drops the throwaway database.

The trial runs only if free space on the database disk exceeds the database
size plus a margin (a fifth of the database, at least 512 MB). Otherwise it is
**skipped with a notice** in Backups. A failed trial fails the cycle, so the
previous set is kept. The result is recorded in `manifest.json.trial_restore`.

### Backups health and alerts

`GetBackupHealth()` ([`storage/health.go`](../app/internal/storage/health.go))
combines the manifest, `backup-status`, `wal-status` and `copy-status.json`.
The Backups screen shows:

- the last backup time
- a **✓ Verified** badge, or "Not verified"
- the **changes saved through** time
- the weekly trial restore
- the copy's state and its last-copied time

| Alert | When |
| --- | --- |
| `no_backup`, `unverified`, `backup_stale`, `backup_failed` | No verified set; newest set missing a check; no verified set for over a day while running; last cycle failed (previous kept). |
| `wal_failing`, `wal_stale`, `wal_off`, `wal_gap`, `wal_unknown` | PostgreSQL cannot archive; changes waiting too long; archiving off; missing segment; no report yet. |
| `status_stale`, `status_unreadable` | The sidecar has not refreshed its status for 15 minutes (for example it cannot write `/state`); a status file is unreadable. |
| `trial_failed`, `trial_skipped` | Weekly trial restore failed or was skipped for space. |
| `copy_missing`, `copy_failed`, `copy_stale`, `copy_status_unwritable` | Copy drive disconnected (with last-copied time); copy failed; copy older than two days; copy status could not be saved. |

The sidecar treats status persistence as part of the job. `write_status`,
`stamp_file` and `write_wal_status` log an error and fail when `/state` is
missing or unwritable. A cycle whose success cannot be recorded exits
non-zero, so **Back up now** reports it.

### Schedule, locking and statuses

The sidecar loop checks WAL status every minute (`CARE_BACKUP_TICK`) and runs
the cycle when it is due: one day after the last success
(`CARE_BACKUP_INTERVAL`), or one hour after a failure (`CARE_BACKUP_RETRY`). It
waits for PostgreSQL to accept connections first and records
`database_unavailable` without touching files if it never does. `flock` on
`.backup.lock` serializes the loop, **Back up now** and pre-update safety
copies.

`backup-state/backup-status` holds the last run:

```text
state=failed        # running | ok | failed
reason=disk_full    # disk_full | database_unavailable | error
at=1767225600
need_kb=1048576
free_kb=1024
message=not enough space for a new backup; the previous backup was kept
```

## 5. Discovering and selecting a restore

### Desktop backup summaries and restore consent

Overview presents the newest backup's status, size and encryption without
printing its filesystem location. Backups retains the effective destination
and folder-management controls; hiding the summary path does not move files or
change the backup policy.

The health card calls `GetBackupHealth()` (section 4) and also reads the
`health` field of each storage report. It shows only the newest verified set;
there is no retention setting. Current backup activity and completion come
from native state and events; request acceptance alone cannot mark a backup
successful.

There is one visible **Restore from a backup file** route for local and imported
files. It calls `ChooseBackupFile`, then `InspectBackupFile`, and presents the
database-only or database-plus-files scope. Before `RestoreFromFile` it
re-inspects the selected metadata, checks the CARE Clinic password, requires the
selected recovery file for encryption and requires explicit replacement
acknowledgement. The protected native job performs the deeper checks described
below; the UI does not replace them.

Cancel and Forgot CARE Clinic password remain usable before native submission,
including while a read-only picker or preflight result is pending. Cancellation
invalidates that operation, clears the password and acknowledgement, and keeps
the last accepted file selection. A late picker cannot replace it or reopen the
dialog. After submission, cancellation is disabled because the native job may
already be modifying data.

Desktop updates block backup mutations with both rendered disabled controls
and a live guard. Update progress invalidates earlier folder picks, backup and
recovery picks, and restore preflight. Even if the installer handoff is
acknowledged before an old promise returns, that old work cannot write a new
destination or start a restore. The user must repeat the interrupted action.

See [`backups-tab.tsx`](../app/frontend/src/screens/panel/backups-tab.tsx),
[`restore-backup-dialog.tsx`](../app/frontend/src/screens/panel/restore-backup-dialog.tsx),
and their [UI regressions](../app/frontend/tests/backups.spec.ts).

### Native backup inventory

[`ListBackups()`](../app/internal/backup/restore.go) reads only the configured
backup folder. A missing folder returns no entries without an error.

It recognizes regular dump files with valid timestamps and constructs one
`Backup` value per dump:

| Field / JSON name | Meaning |
| --- | --- |
| `DBDump` / `db_dump` | Dump basename, not an arbitrary path. |
| `FilesArchive` / `files_archive` | Selected matching archive basename, or empty for database-only. |
| `Label` / `label` | Human-readable time, `daily` or `manual`, `DB only` or `DB + files`, and an encryption suffix when applicable. |
| `Manual` / `manual` | Whether the dump name starts with `care-manual-`. |
| `Encrypted` / `encrypted` | Whether the dump **or its selected archive** has `.enc`. |
| `SizeBytes` / `size_bytes` | Size of the dump alone, not the combined set size. |

Pairing uses the set name: `care-<ts>` pairs with `files-<ts>` and
`care-manual-<ts>` with `files-manual-<ts>`. A manual dump is never paired with a
scheduled archive of the same timestamp. It prefers an archive with the same
encrypted/plain form as the dump, then accepts the other form if that is the
available match. Orphan files
archives do not create list entries.

The label `daily` comes from the filename convention; it is not proof that a
scheduled cycle succeeded. Listing is discovery, not decryption or a successful
restore rehearsal. It does not fully inspect dump/archive contents, and the
stricter restore path rejects empty files even if their names appeared in a
list.

### Configured folder and external import use the same pipeline

```mermaid
flowchart TD
    Local["Restore: configured backup folder"] --> Common["RestoreFrom"]
    External["External selection: source folder plus basenames"] --> Common
    Common --> Names["Validate names, timestamps, regular files, and scope"]
    Names --> Enc{"Any selected member encrypted?"}
    Enc -- No --> Pending["Reject an existing or invalid recovery journal"]
    Enc -- Yes --> Key["Require explicitly selected backup recovery file"]
    Key --> Pending
    Pending --> Images["Require staged migration wiring and restore images"]
    Images --> Journal["Create staging journal"]
    Journal --> Stage["Copy into owned staging volume and validate"]
```

- `Restore(dbDump, filesArchive, recoveryFile)` delegates to `RestoreFrom` using
  `Store.BackupDir`.
- `RestoreFrom(srcDir, dbDump, filesArchive, recoveryFile)` uses the supplied
  folder, or the configured folder when `srcDir` is empty, and converts it to
  an absolute path.
- The dump and optional archive must be in that same source folder, with the
  required basenames. This is not a remote URL fetch or a files-only restore.
- An encrypted member requires a selected, valid private recovery file.
- There is no automatic lookup beside backups or fallback to an installation key.
- Neither source-folder restore nor staging relocates the original backups into
  the configured destination. The input is mounted read-only and copied into
  a Docker staging volume for this operation.

The desktop pickers, job handling, and administrator authorisation belong to the
[Wails application guide](wails-application.md). At the package boundary, import
is simply a different source folder, not a separate destructive restore
algorithm.

### Filename and filesystem checks

Restore refuses:

- A dump name outside the `care-[manual-]YYYYMMDD-HHMMSS.dump[.enc]` convention.
- A calendar-invalid timestamp.
- A files archive from another set: a different timestamp, or a scheduled
  archive with a manual dump and the other way round.
- Path separators, traversal, or arbitrary shell text in the basenames.
- A selected backup that `Lstat` identifies as a directory, symbolic link,
  non-regular file, or empty file.
- A chosen recovery key that is not a nonempty regular file.

During the container copy step, each source backup is checked again with
`-f` and `! -L`. Docker `--mount` arguments are assembled using CSV escaping,
so a comma in a host path is not simply concatenated as a new mount option.

These are deliberate entry/path checks, not a claim to eliminate every
filesystem race. The code does not freeze an externally writable source folder
or forbid all symlinked ancestor directories. Use stable, trusted source
folders.

## 6. Preparing the replacement without replacing live data

### The `Store` and callback boundary

[`Store`](../app/internal/backup/store.go) is a dependency container, not an
independent running service:

| Field or constructor | Contract |
| --- | --- |
| `Dir`, `BackupDir` | Installation and backup folders. The journal and connection settings are installation-local. |
| `Image`, `BackendImage` | Backup-tools image and CARE backend image. The backend image supplies Python for archive validation and is needed for staged migrations. |
| `Host` | Clinic hostname used in the completion message; not the PostgreSQL host. |
| `Project` | Docker project/network/resource prefix; the clinic wiring uses `care-clinic`. |
| `Log` | Optional log callback. |
| `EnsureImage` | Callback to ensure the backup image, used by key setup and prepared recovery. |
| `EnsureRestoreImages` | Callback ensuring the images needed for staged restore. |
| `Migrate(database, restoreID)` | Callback that must migrate the named staging database, not the current live database. |
| `New(run, s)` | Attach the supplied `proc.Runner` to the supplied store and return that same store. It does not create keys or launch work. |

[`Clinic.Backups()`](../app/internal/clinic/backupstore.go) fills these fields
and attaches the clinic runner, which supplies the working directory,
environment, and command logging. It wires:

- `EnsureImage` to `Builder().EnsureBackupImage`.
- `EnsureRestoreImages` to ensuring the backend image, then the backup image.
- `Migrate` to `Clinic.migrateStaged`.

Restore explicitly refuses missing `EnsureRestoreImages`, missing `Migrate`, or
an empty backend image before creating its journal. Key generation also
expects a properly wired `EnsureImage`. Constructing a bare `Store` is not a
substitute for these contracts.

### Resource identities

Each restore gets a random 12-byte identifier rendered as 24 lowercase
hexadecimal characters. For a synthetic ID such as
`0123456789abcdef01234567`:

| Resource | Naming / ownership |
| --- | --- |
| Staged database | `care_restore_<id>` |
| Previous database | `care_previous_<id>` |
| Staging Docker volume | `<project>_restore_<id>_stage` |
| Previous-files Docker volume | `<project>_restore_<id>_previous` |
| Helper container | `<project>-restore-<id>-<step>` |
| Restore label | Exported `RestoreLabel` constant: `org.care-clinic.restore=<id>` |
| Project label | `com.docker.compose.project=<project>` |
| PostgreSQL ownership/application tag | `<project>:restore:<id>` |

The stage volume exists for database-only restores too. It holds copied dump
input and the decrypted `database.dump`; a DB + files restore also holds
`files.tar.gz` and its extracted `files/` directory. For encrypted inputs, the
copied encrypted inputs remain there as well.

The previous-files volume is only needed when files are being restored. The
previous database is preserved by renaming, not by making a second database
dump at cutover.

### Preflight is more than checking an archive's table of contents

The preparation order in
[`prepareRestore`](../app/internal/backup/restore.go) is:

1. Create and verify ownership of the staging volume.
2. Run a backup-image helper with **no network**, read-only source/key mounts,
   a writable stage volume, and `umask 077`.
3. Copy the selected inputs into staging. Decrypt encrypted members there,
   using the selected recovery file, mounted read-only, with no password.
4. Fully read the dump with
   `pg_restore --exit-on-error --file=/dev/null /restore/database.dump`,
   then sync.
5. If files were requested, validate and extract the archive into staging.
6. Start/wait for only the database service without recreating it:
   Compose uses `--no-recreate`, `--wait`, and a 300-second wait timeout.
   A separate `pg_isready` command must also succeed.
7. Inspect the existing database identities, record the original database's
   OID in the journal, and require that no staged/previous name is already in
   use.
8. Create the tagged staging database from `template0` and load the dump into
   it with `--exit-on-error --single-transaction --no-owner --no-privileges`.
9. Run migrations against that staging database.
10. Recheck that the live database still has its original OID, the staged
    database has the correct restore tag, and the previous name is still
    unused. Record the staged database's OID.

An **OID** is PostgreSQL's object identifier. Renaming a database changes its
name, not its OID, so recovery can distinguish the original from the replacement
even when one of them now uses the ordinary live name.

Capture-time `pg_restore --list` can succeed for a dump whose later payload is
corrupt. Restore's full read catches more than that listing, and the actual
transactional load checks whether the SQL can be applied to the staging
database. These are separate checks, neither of which proves clinical data
correctness.

`--single-transaction` applies to loading the dump. It is not a transaction
covering database creation, all migrations, files, and activation together.
If the load fails, an empty tagged staging database can still need cleanup.

### Archive validation and extraction safety

[`validateRestoreFiles`](../app/internal/backup/restore_data.go) runs Python
from the backend image with:

- `--network none`.
- A read-only container root filesystem.
- A writable staging-volume mount.
- Explicit `--entrypoint python`, `-B`, and user `0:0`.

It does not boot the backend application to extract an archive.

The Python code first drains the gzip stream completely, then reads the tar
members. Before extracting any member, it requires every entry to:

- Have a nonempty, relative POSIX path.
- Contain no `..` component.
- Be a regular file or directory.

Symbolic links, hard links, devices, FIFOs, and other non-file/non-directory
entries are rejected. Only after checking the member list does it create the
staging `files/` directory, extract with numeric ownership, and sync.

This protects the live volume from malformed or path-escaping archive contents.
It does not impose an archive expansion limit, member-count limit, or free-space
budget. It also does not make untrusted database SQL safe. Disk exhaustion,
incompatible data, and malicious content remain reasons to use trusted backups
and an isolated recovery exercise.

### Why staged migrations are not live migrations

The callback receives both `care_restore_<id>` and the restore ID.
[`Clinic.migrateStaged`](../app/internal/clinic/migrate.go) validates that
relationship, then runs a one-off backend service command:

```text
docker compose run --rm --no-deps
    ... --entrypoint python
    -e POSTGRES_DB -e DATABASE_URL -e PGAPPNAME
    backend manage.py migrate --noinput
```

This is a schematic excerpt, not a command to run against a clinic.

The callback overrides **both** `POSTGRES_DB` and `DATABASE_URL`. Merely changing
one would be insufficient if the backend preferred the other. Its URL helper
changes the database path and removes competing `dbname`/`database` query
parameters. The restore tag is placed in `PGAPPNAME` and the URL's
`application_name` so interrupted database sessions can be identified.

`--no-deps` prevents this helper from starting ordinary dependencies as a side
effect, and the Python entrypoint bypasses the backend's usual service startup.
During this preparation, existing CARE services have not yet been stopped.
The intended database target is the isolated replacement, not the live schema
used by current workers.

That is why this hook must not be replaced with the normal startup/live
migration routine. The [clinic lifecycle guide](clinic-lifecycle.md) separately
explains worker pausing around ordinary live migrations.

The restore code's SQL helpers use the `POSTGRES_*` connection settings in
`backend.env`. A configured `DATABASE_URL` used for migrations still needs to
describe the intended PostgreSQL server. The source does not prove arbitrary,
conflicting connection configurations equivalent, nor can it guarantee that
every upstream/plugin migration lacks external side effects.

## 7. Cutover, durable state, and activation

### Stopping writers is a checked operation

After successful staging and migrations, restore stops these Compose services:

```text
backend  celery-worker  celery-beat  minio  backup  caddy
```

It then obtains their container IDs and inspects each status. Every returned
container must be `exited` or `created`; a failed stop, incomplete inspection,
or still-running service prevents cutover.

This stops normal application, worker, scheduler, upload, backup, and ingress
activity before replacing data. It is not a universal lock on unrelated
PostgreSQL clients or administrator commands. It also means restore is **not
zero-downtime**. Even a database-only restore stops this service list, although
it does not replace the uploaded-files volume's contents.

For a DB + files restore, the stopped live files are copied with `cp -a` into
the owned previous-files volume and synced before the cutover phase is
recorded. During this initial snapshot, the code can create the normally
labelled live volume if it is absent. That can represent an empty source;
it is not proof that missing historical uploads have been recovered.
Later prepared recovery requires the existing recovery/live volumes and
does not manufacture an empty replacement for a missing recovery copy.

### The journal is local and durable by design

[`restore_journal.go`](../app/internal/backup/restore_journal.go) records
`<installation>/restore-state.json`. Its JSON fields are:

| Field | Purpose |
| --- | --- |
| `id`, `project` | Identify this restore and this installation's resource namespace. |
| `phase` | One of the four exact states below. |
| `database`, `host`, `port`, `user` | Original database connection identity. These are distinct from the clinic's web hostname. |
| `original_oid`, `staged_oid` | Original/replacement identities, populated as staging progresses. |
| `with_files` | Whether uploaded files participate in cutover/recovery. |

It does not store the database password, private-key contents,
source folder, or selected backup filenames. Passwords are not needed to replay
rollback from the already prepared resources.

The journal reader requires a regular non-symlink file no larger than 16 KiB,
valid JSON, a valid restore/project identity matching the current store, valid
connection fields, and recognized phases. Prepared/committed journals require
two distinct valid OIDs. Protected PostgreSQL database names such as `postgres`,
`template0`, and `template1` are rejected.

Writes use
[`atomicfile.Write`](../app/internal/sys/atomicfile/atomicfile.go) with mode
`0600`: write a sibling file, sync and close it, then replace the journal.
Unix replacement syncs the containing directory; Windows uses replacement
with write-through semantics. This is the durability mechanism used to order
recovery decisions. It is not a promise that a failed disk, filesystem, Docker
engine, or power-loss scenario has been exhaustively tested.

Before each SQL helper, current database name/host/port/user settings are
compared with the journal. A mismatch stops recovery with a request to restore
the original connection settings. The database password is reread from
`backend.env`; it is not frozen in the journal.

### The four exact phases

| On-disk phase | When it is written | What the next recovery does |
| --- | --- | --- |
| `staging` | Before helper resources are created; rewritten as original/staged OIDs become known. | Stop this restore's helper containers, leave live data alone, then mark `rolled-back`. Staging is abandoned, not automatically resumed. |
| `prepared` | After replacement validation/migrations, checked writer shutdown, and the previous-files snapshot when applicable. Before modifying live files or renaming databases. | Stop/verify writers and helpers, check ownership, restore the original database/files, then mark `rolled-back`. |
| `committed` | After files replacement when applicable and a successful database swap, **before** starting the normal stack. | Keep the replacement as live. Never automatically replay the old snapshot over potentially newer writes. |
| `rolled-back` | After staging abandonment or successful prepared rollback. | Do not replay rollback again. Keep current data and leave final cleanup for a successful start. |

There is no separate persisted `activating`, `healthy`, or `finished` phase.
A removed journal means no restore is pending in this protocol; malformed
metadata is an error, not permission to treat it as absent.

```mermaid
stateDiagram-v2
    [*] --> staging: Inputs and image preparation accepted; write journal
    staging --> prepared: Stage and migrate; stop writers; preserve files if needed
    staging --> rolled_back: Recover by abandoning staging
    prepared --> committed: Replace files; swap databases; persist commitment
    prepared --> rolled_back: Recover original data before activation
    committed --> committed: Recover without reverting new writes
    rolled_back --> rolled_back: Recover without repeating rollback
    committed --> [*]: Successful activation then FinishRestore
    rolled_back --> [*]: Successful normal start then FinishRestore
    state "rolled-back" as rolled_back
```

### Database and file cutover are different mechanisms

Files are replaced **in the existing live volume**:

1. Verify the source staging volume's labels and the live volume's Compose
   project/volume labels.
2. Remove current top-level entries, including hidden entries.
3. Copy the staged `files/` tree into the live volume.
4. Sync the live volume.

This is a stopped-writer, journal-recoverable copy, not an atomic volume rename.
Interruption can leave the live files partly replaced; the separately retained
previous-files volume is what makes prepared recovery possible.

The database swap uses both Go-side inspection and SQL assertions:

1. Require the live OID to be the original, the staged OID/tag to be the
   replacement, and the previous name to be unused.
2. Disable new connections to the live and staged databases.
3. Terminate their sessions and assert no sessions remain.
4. In one SQL transaction, rename live to `care_previous_<id>` and staged to
   the original live name.
5. Re-enable connections to the newly live database.

The two renames are transactional; the entire restore is not. In particular,
connection disabling occurs outside that rename transaction. If interruption
leaves the original database in place but connections disabled, recovery
explicitly reconnects it.

```mermaid
sequenceDiagram
    participant S as backup.Store
    participant J as Local journal
    participant H as Staging helpers and migration callback
    participant W as CARE writers
    participant D as PostgreSQL and files volumes
    S->>J: Persist staging
    S->>H: Copy, decrypt, fully validate, load separate DB, migrate it
    H-->>S: Replacement ready; original and staged identities checked
    S->>W: Stop and inspect all listed writers
    S->>D: Preserve previous files when included
    S->>J: Persist prepared
    S->>D: Replace live files when included
    S->>D: Transactionally swap database names
    S->>J: Persist committed
    S->>W: Compose up with wait
    S->>S: Wait for CARE health
    S->>D: FinishRestore removes owned previous/staging resources
    S->>J: Remove journal last
```

After committing, restore calls Compose `up -d --wait --wait-timeout 300`,
then `health.Wait` with a three-minute timeout. Only after these checks does
this path call `FinishRestore()` and log completion.

## 8. Recovery after interruption

### Start through the application

Errors after journal creation generally identify the restore and say that
recovery data was retained. They direct the operator to start CARE to recover
safely and not start containers manually.

That instruction matters:
[`Clinic.Start`](../app/internal/clinic/start.go) calls `RecoverRestore()`
before ordinary startup and `FinishRestore()` after successful health.
Starting writers outside that lifecycle can introduce new data before the
recorded recovery decision has been applied.

Keep the installation's journal and the owned staging/previous Docker resources
intact while investigating. Do not "fix" an error by deleting
`restore-state.json`, editing OIDs/phases, renaming databases, or replacing a
missing recovery volume with an empty one.

### Recovery decisions by interruption point

| Interruption or error | Durable phase normally left behind | Intended next action |
| --- | --- | --- |
| Input rejection or image preparation failure before the journal | None | Report the error; no restore resources have been intentionally created by this pipeline. |
| Copy, decrypt, full dump read, archive validation, staged load, or staged migration | `staging` | Abandon staging on recovery; originals were not intentionally replaced. |
| Writer stop/inspection or previous-files snapshot failure | `staging` | Preserve originals, but recognize that services may already be stopped. Recover and use normal startup. |
| After preparation, during files replacement, before/during/after database swap but before commitment | `prepared` | Roll back to the original database/files before normal writers start. |
| After commitment but before or during Compose activation | `committed` | Resume with the replacement. Some services may already have accepted writes. Do not automatically roll back. |
| Health failure after activation | `committed` | Preserve current replacement data and retained originals; retry normal startup/recovery, then finalize only after success. |
| Cleanup failure after successful start | `committed` or `rolled-back` | Retry finalization; some owned cleanup may already have completed. |

An error message alone does not establish which metadata write reached storage.
The validated journal read on the next attempt is the recovery authority.

### What `RecoverRestore()` actually does

With no journal it returns without starting Docker work. With a journal it
first finds only containers bearing **both** this project's label and this
restore's label, force-removes those helpers, and rechecks that none remain.
This also covers an interrupted migration helper.

For `prepared` recovery it additionally:

1. Stops and verifies the normal writers again.
2. Ensures the backup image when the callback is supplied.
3. For a files restore, requires the labelled previous-files volume and the
   correct existing live volume before attempting database recovery.
4. Starts/checks PostgreSQL and terminates remaining database sessions tagged
   with this restore's application name.
5. Inspects the database identities.
6. Either reconnects the still-original database, or transactionally renames
   the original back to live and the replacement back to staged.
7. Copies the previous files back into the live files volume when applicable.
8. Persists `rolled-back`.

Only two database arrangements are accepted for prepared rollback:

| Arrangement | Required identities | Recovery |
| --- | --- | --- |
| Swap did not happen | Live = original OID; staged = recorded replacement OID with this tag; no previous database. | Re-enable original connections. |
| Swap happened | Live = replacement OID with this tag; previous = original OID; no staged database. | Reverse the database-name swap and reconnect the original. |

Anything else is an error rather than a guessed rename. Missing/unowned
recovery volumes, an unremovable helper, changed settings, and unexpected OIDs
likewise stop the operation and retain metadata.

For `staging`, no files/database rollback is necessary: normal preparation
did not intentionally replace them. After stopping helper containers, recovery
marks the operation `rolled-back`; temporary resources remain for later
cleanup.

For `committed` and already `rolled-back`, recovery does not replay a
snapshot. This is essential: data written after an earlier successful
activation or rollback must not be overwritten on every later start.

`RecoverRestore()` by itself does not activate the whole clinic or declare it
healthy. It establishes which data normal startup may use.

### Why originals remain until `FinishRestore()`

[`FinishRestore()`](../app/internal/backup/restore_journal.go) only accepts
`committed` or `rolled-back`. It:

1. Stops lingering owned helper containers and tagged database sessions.
2. Rechecks live/previous/staged identities and staged ownership.
3. For a committed restore, drops the previous database if still present and
   still the recorded original.
4. For a rolled-back restore, drops the tagged staged database if present.
5. Removes the owned stage volume, plus the previous-files volume for a
   files restore.
6. Removes the journal **last**.

The normal live files volume is not a cleanup target. Previously published
backup files and recovery-key exports are not deleted by `FinishRestore`.

Missing resources that were already cleaned up can be accepted, allowing
partial cleanup to resume. A resource that exists under an unexpected identity
or label is not blindly removed. Cleanup is not one all-or-nothing transaction,
so a failure can leave only some of the recovery copies.

**Caller contract:** `FinishRestore()` does not run its own health check.
The restore and clinic-start callers arrange successful startup/health before
calling it. Calling this public method directly is not a safe health test and
would bypass that sequencing.

Retained originals are temporary recovery resources, not a user-selectable
second backup history. They occupy disk space until finalization succeeds.
After commitment, automatic recovery intentionally will not choose them over
possibly newer replacement data.

### Other public recovery operations

| Operation | Contract |
| --- | --- |
| `PendingRestore()` | Return whether a valid journal exists. Propagate unreadable/corrupt/foreign metadata as an error; do not interpret an error as "safe to start another restore". |
| `RecoverRestore()` | Resolve an interrupted restore before ordinary writers start; retain copies for successful-start finalization. |
| `FinishRestore()` | Finalize only a committed/rolled-back restore after the caller has established successful startup. |
| `Clinic.RestartBackupSidecar()` | Run recovery first, then force-recreate only the `backup` service. It is not whole-clinic activation or finalization. |
| `Clinic.BackupDirPath()` | Return the effective backup path; no disk backup is created. |

Starting another restore while any valid journal remains is refused, including
a committed restore whose cleanup still needs finishing.

## 9. Moving folders, preserving keys, and deleting backups

### Moving a folder does not recreate a key

Keep the private recovery file secure and separate when moving backups to
another folder or computer. Select the original clinic's recovery file for
imported backups. An encrypted dump and a public certificate cannot recover
data when every copy of the matching private recovery file has been lost.

External restore requires the original clinic's explicitly selected recovery
file, even when the current installation uses a different encryption key.
No cloud account or OS keyring sync is implemented here.

When the desktop changes its configured destination, it copies the public certificate
before using the new destination and recreates a running backup sidecar to
update mounts. Existing backups and their public certificate remain in the old folder; it is
not an automatic migration of old archives. The settings guide describes that
operation's configuration-save and rollback ordering.

### Key/location operations

| Operation | Contract |
| --- | --- |
| `EnsureKeysDir()` | Create the installation keys directory. It does not establish a valid keypair. |
| `GenerateRecoveryFile()` / `InstallCertificate(certificate)` | Generate a public certificate and private recovery file for export; install only the certificate without replacing a different identity. |
| `BackupEncryptionOn()` | Certificate-path presence check only. |
| `CopyBackupCertificate()` | Copy the public certificate to the backup destination without overwriting a different certificate. |
| `CheckLocation(dir, protected)` | Require absolute paths, resolve existing symlink ancestors even for not-yet-created descendants, and reject a backup directory equal to or inside the supplied protected directory. |
| `PreserveBackupCertificate()` | Check directory separation and retain the public ownership certificate before removing installed files. |
| `ForeignRecoveryData()` | Detect encrypted backups without a matching public certificate, or a certificate mismatch. This does not decrypt backups. |
| `DiscardUnusedCertificate()` | Remove a matching public certificate copy during failed-setup cleanup only when no encrypted backups exist. |
| `DeleteBackups()` | Explicitly delete recognized top-level backup-owned entries from the configured folder, keeping unrelated entries. |

The location check is about the protected directory supplied by its caller.
It is not a universal check of disk health, writability, every symlink race,
or every possible cleanup target.

Normal uninstall and purge preserve recovery keys when keeping backups.
The `RemoveUnusedRecoveryKey` option in
[`uninstall.go`](../app/internal/clinic/uninstall.go) exists for **failed-install
cleanup**, not routine uninstall/purge. Do not apply its narrow "provably unused
and ours" exception to keys protecting retained backups. Full lifecycle choices
belong in [cleanup and uninstall](cleanup-and-uninstall.md).

### Explicit deletion versus latest-only replacement

`DeleteBackups()` enumerates the selected folder's immediate entries:

- It recognizes the recovery key, `.backup.lock`, supported dump/archive
  basenames, and corresponding intermediate names after removing an initial
  dot and one `.tmp` component.
- It never recursively deletes directories.
- It leaves unrecognized entries in place and logs that they were kept.
- It removes the containing folder only when no unrelated entries were kept
  and the recognized removals succeeded.
- It aggregates removal errors; successful earlier deletions are not rolled
  back.

A recognized symbolic link can be unlinked by explicit deletion, but its
target is not recursively deleted. The verified cycle's own replacement of the
previous set (section 4) only removes regular files and its own `base-<ts>`
folders, and only after the new set is recorded.

Ownership here is a naming convention within the selected backup folder, not
proof of who created the data. A personal file deliberately given a recognized
backup name can be a deletion target. The exported recovery key is intentionally
a target when backups are explicitly removed.

This method does not acquire the shell's `flock`, stop services, or perform
authorization on its own. Those are caller/lifecycle responsibilities. Do not
invoke it concurrently with an independently running writer or treat it as
secure disk erasure.

## 10. Limits and operational expectations

The code supports guarded backup creation and staged restore, but several
boundaries are important:

1. **A local backup is still local.** The optional second copy can live on a
   USB drive or network folder, but nothing is uploaded to the cloud. Without
   a second copy, losing the machine and its backup drive loses both.
2. **A scheduled pair is not an application-wide snapshot.** PostgreSQL is
   dumped, then the storage volume is archived while ordinary services can
   still run. No cross-database/files snapshot transaction or quiescing step
   is implemented during backup capture.
3. **Verification is strong but not a rehearsal.** Manifest checks, decrypt
   tests and the weekly row-count trial restore catch most damage. They do not
   prove that a full clinic on a replacement machine starts with that data.
4. **Restore requires working infrastructure.** Docker/Compose, compatible
   images, required command-line tools, readable connection settings, and
   sufficient privileges must be available. Prepared recovery cannot succeed
   by guessing around missing infrastructure.
5. **Capacity is not pre-reserved.** Staging can retain encrypted and decrypted
   inputs, extracted files, a restored database, the original database, and
   previous uploaded files simultaneously. There is no explicit free-space
   preflight or expansion quota.
6. **Plaintext exists during operations.** Backup intermediates and
   restore-stage volumes can contain unencrypted clinical data. Failure
   deliberately retains some recovery resources. Removal is not secure erasure.
7. **Restore is not zero-downtime.** Writers are stopped for cutover and remain
   stopped on some failures until the normal recovery/start sequence succeeds.
8. **Commitment changes the recovery choice.** Failure after commitment does
   not trigger automatic rollback, because the replacement may already contain
   new writes.
9. **The journal is not the data.** Deleting Docker recovery volumes/databases
   or losing the installation directory can remove what automated recovery
   needs. Keep both metadata and recovery resources while investigating.
10. **Compatibility has limits.** A dump plus uploaded files does not include
    all installation configuration, TLS trust state, image pins, plugins, or
    the OS keyring. This code does not prove arbitrary PostgreSQL-version,
    upstream-schema, plugin, or storage-format transitions safe.
11. **Native access remains powerful.** Process environments, Docker access,
    and filesystem access can expose recovery material. The UI gate and
    resource labels are coordination/ownership mechanisms, not protection
    against a hostile machine administrator.

For an operator, the practical recovery checklist is: identify the intended
database-only or DB + files scope; preserve the source files and separately stored
recovery file; use the application-mediated restore/start path; read the actual
error and pending-state behavior; and do not delete originals or metadata to
make a warning disappear. A separate, controlled restore exercise is still
needed to establish a site's recovery procedure.

## 11. What the existing tests demonstrate

No clinic commands, live data access, or tests were run to write this guide.
The following describes the checked-in test source, not observed results on
this machine or proof of real-world disaster recovery.

| Test file | What it exercises | Boundaries and skips |
| --- | --- | --- |
| [`crypto_test.go`](../app/internal/backup/crypto_test.go) | Recovery-file matching; public-only installation; non-overwrite; symlink/location rejection; certificate ownership and cleanup; real OpenSSL CMS round trip. | Uses synthetic data and production-sized keys. OpenSSL absence skips the CLI round trip; symlink cases skip on Windows. |
| [`restore_test.go`](../app/internal/backup/restore_test.go) | Unsafe input rejection before Docker; preparation failures preserving live data; rollback before commitment; committed recovery preserving new writes; missing/foreign-resource refusal; journal validation; environment-only secret values; manual scope; pairing rules; resumable cleanup. | Docker is a test double implemented by rerunning the Go test binary. Database IDs, file contents, and container states are synthetic. It does not exercise a real Docker activation/health sequence. POSIX fixture cases skip on Windows. |
| [`restore_archive_test.go`](../app/internal/backup/restore_archive_test.go) | Real Python validation of synthetic regular/corrupt/truncated/path-escaping/link archives. An isolated PostgreSQL fixture tests full-payload validation, transactional load, quoted database names, tagged-session termination, transactional rename failure, swap, and rollback. | Python case skips without `python3`. PostgreSQL case skips on Windows, as root, or without its local PostgreSQL tools. It starts its own local fixture with TCP listening disabled, not a live clinic. No Docker-volume or power-loss disaster is reproduced. |
| [`backup_script_test.go`](../app/internal/clinic/backup_script_test.go) | Real OpenSSL/jq against faked PostgreSQL tools. Covers: WAL encryption and atomic publication; replace-previous-only-after-verification; failure keeps the previous set and all WAL; start-LSN WAL pruning; refusal of later-timeline segments; trial-restore space gating; status-write and missing `/state` failures; `pitr-stage.sh` refusing an unreached target; collision refusal; writer serialization. | PostgreSQL programs, `df`, `flock` and the clock are simulated. POSIX shell required; serialization cases skip without native `flock`. |
| [`pitr_test.go`](../app/internal/backup/pitr_test.go) | Point-in-time bounds (earliest = `base.ended_at`, latest = saved-through), WAL-gap refusal before any helper runs, and the staged replay → stage-db → swap order. | Docker is the same re-executed test double as `restore_test.go`. |
| [`health_test.go`](../app/internal/storage/health_test.go) and [`copy_test.go`](../app/internal/backupcopy/copy_test.go) | Health alerts and saved-through; copy disconnected and reconnect catch-up; temp-then-rename; prune only after verification; missing WAL never marked current; status-write errors surfaced. | Filesystem fixtures only. |

Two particularly useful regression distinctions are:

- The PostgreSQL fixture demonstrates a corrupt dump whose table of contents
  can still be listed, while full payload validation fails. This explains why
  restore must do more than reuse capture-time `--list`.
- The interruption fixtures distinguish `prepared` from `committed` and simulate
  writes after activation/rollback. They test that repeated recovery does not
  overwrite those newer writes.

`CARE_RESTORE_DOCKER_STATE` in `restore_test.go` is internal test-double
plumbing, not a switch granting permission to exercise real Docker.
The tests owned by this guide mainly use fixtures and prerequisite-based skips;
they do not expose a single opt-in "disaster test" mode.

Native and integration tests also have platform, tool, and fixture
prerequisites. Consult their source and
[development and release](development-and-release.md) before running them.
A skipped case is not a pass for that behavior, and exercising a fixture is
not evidence of a complete clinic backup/restore rehearsal. Documentation-only
changes do not require a live restore or a broad test run.

## 12. Current source file map

This is the complete Go-file inventory owned by this guide.

| Source file | Current role |
| --- | --- |
| [`app/internal/backup/store.go`](../app/internal/backup/store.go) | `Store` dependency fields, runner attachment through `New`, optional logging, and the Compose command helper. |
| [`app/internal/backup/crypto.go`](../app/internal/backup/crypto.go) | Recovery-file generation/validation, public certificate installation, location checks, certificate ownership, and owned-entry backup deletion. |
| [`app/app_recovery.go`](../app/app_recovery.go) | Native recovery exports, setup verification, single-use CARE Clinic recovery codes, password changes, and offline reset throttling. |
| [`app/app_backup_key.go`](../app/app_backup_key.go) | Versioned Argon2id/AES-GCM local key encryption, certificate/clinic binding, and setup enrollment. |
| [`app/internal/backup/restore.go`](../app/internal/backup/restore.go) | `Backup` list shape, filename recognition/pairing, configured/external source validation, key selection, staging orchestration, cutover orchestration, activation, and finalization call. |
| [`app/internal/backup/restore_data.go`](../app/internal/backup/restore_data.go) | Database readiness/settings/identity helpers, SQL load/swap/rollback/cleanup scripts, file snapshot/replacement helpers, and Python archive validation. |
| [`app/internal/backup/restore_journal.go`](../app/internal/backup/restore_journal.go) | Restore IDs/tags, journal schema/validation/durable writes, helper/writer shutdown, Docker-volume ownership, pending detection, recovery, and final cleanup. |
| [`app/internal/backup/crypto_test.go`](../app/internal/backup/crypto_test.go) | Key preservation, non-overwrite, foreign data, safe location/deletion, and certificate recovery regressions. |
| [`app/internal/backup/restore_test.go`](../app/internal/backup/restore_test.go) | Synthetic Docker/state fixture and restore input, interruption, commitment, cleanup, ownership, secret-transport, and listing regressions. |
| [`app/internal/backup/restore_archive_test.go`](../app/internal/backup/restore_archive_test.go) | Synthetic archive tests through Python plus tool-gated, isolated PostgreSQL payload/transaction/session/swap regressions. |
| [`app/internal/clinic/backup.go`](../app/internal/clinic/backup.go) | `Clinic.BackupNow`: encryption and space guards, then the sidecar's verified cycle (`backup.sh now`). |
| [`app/internal/backup/pitr.go`](../app/internal/backup/pitr.go) | `RestorePointInTime`, `PITRTarget` and `SavedThrough` bounds, and the staged replay helper. |
| [`app/internal/storage/health.go`](../app/internal/storage/health.go) | Manifest, WAL and copy status, and `ReadHealth` alerts. |
| [`app/internal/backupcopy/copy.go`](../app/internal/backupcopy/copy.go) | Optional verified second copy. |
| [`app/internal/clinic/backupstore.go`](../app/internal/clinic/backupstore.go) | Compose project constant, clinic-to-store dependency/callback wiring, effective backup path, and recovery-before-sidecar-restart. |
| [`app/internal/clinic/backup_script_test.go`](../app/internal/clinic/backup_script_test.go) | Isolated shell-script fixtures for the verified cycle, WAL archive/restore, latest-only replacement, trial restore, status reporting, staging replay and locking. |

The two runtime deployment sources in scope are:

| Source file | Current role |
| --- | --- |
| [`deployments/scripts/backup.sh`](../deployments/scripts/backup.sh) | Sidecar loop, `now` and pre-update modes. Covers: verified latest-only cycle, manifest, trial restore, WAL status, start-segment pruning, and status files. |
| [`deployments/scripts/backup-lib.sh`](../deployments/scripts/backup-lib.sh), [`wal-archive.sh`](../deployments/scripts/wal-archive.sh), [`wal-restore.sh`](../deployments/scripts/wal-restore.sh), [`pitr-stage.sh`](../deployments/scripts/pitr-stage.sh), [`db-entrypoint.sh`](../deployments/scripts/db-entrypoint.sh) | Shared WAL helpers, encrypted archive/restore commands, staged point-in-time replay, and database WAL settings. |
| [`storage/backup.go`](../app/internal/storage/backup.go) | Latest-set sizing, `BackupNeed`, and `backup-status` parsing. |
| [`deployments/backup.Dockerfile`](../deployments/backup.Dockerfile) | PostgreSQL-image-derived tool image with OpenSSL added. |

### Current restore architecture

The current implementation has `restore_data.go` and `restore_journal.go`,
`BackendImage` and `EnsureRestoreImages` dependencies, and a
`Migrate(database, restoreID)` callback. It stages and migrates a separate
database, retains originals, records the four phases, and finalizes after
successful startup. There is no current `restoreDB`/`restoreFiles` live
drop-and-load path or old `waitForDB` helper in this package.

Follow the current sources and this guide's staged protocol rather than
historical direct-replacement diagrams. Silo continues to use the existing
`minio` service and volume names; no migration or renaming is implied.
