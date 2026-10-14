# Redesign background

The [agreed redesign](redesign.md) is authoritative on all conflicts. This
document explains its motivation; it does not introduce alternative release
policies, compatibility promises or additional implementation scope.

The desktop has accumulated release selection, host-side orchestration and
settings/build responsibilities that need explicit boundaries. The redesign
separates release production, approved clinic updates, effective configuration,
verified backups and engine execution.

One human-edited `versions.toml` feeds strict validation and selective builds.
CARE 3.1 is the only supported CARE version. CI snapshots upstream branch heads,
publishes digest-pinned runtime/builder images and immutable binaries, then
atomically advertises them through an Ed25519-signed, revisioned manifest. Two
embedded public keys allow rotation without trusting unsigned discovery.

A single update coordinator owns installed-version changes. Durable journals
and history make interruptions recoverable; downloaded updates wait for staff
approval. Requirement constraints, pre-flight checks, previous artifacts and
database backups provide dependency planning and rollback.

Configuration separates release defaults, persistent explicit overrides,
hostname-derived values and private generated secrets. Frontend option A
compiles settings locally when they differ from the published build; the API
URL alone is substituted at startup.

Backup safety means keeping the previous verified set until its replacement
passes checks, continuously protecting WAL on local disk, and treating removable
storage as an optional asynchronous copy rather than a clinic dependency.

Finally, the mTLS agent becomes the sole Docker/compose executor on both VM
platforms and native Linux. The host helper remains responsible only for VM boot
and privileged port forwarding. No backward compatibility is required.

Delivery follows the five phases in `redesign.md`: signed releases; coordinated
updates and support logs; configuration/frontend/secrets; verified backups; then
the agent execution boundary. Do not interpret future phases as implemented by
the first release-pipeline PR.

## First-publication trust setup

The two public keys in `app/internal/release/manifest.go` are public-only
bootstrap rotation slots, not operational signing identities. Before publishing,
operators must replace both with their own base64 Ed25519 public keys, retaining
the corresponding private keys outside the repository. Configure
`UPDATES_SIGNING_KEY` (base64, 64-byte Ed25519 private key) only in the
reviewer-required GitHub Actions publishing environment. Require designated
reviewers in that environment's repository settings; workflow YAML cannot
enforce that administrative protection itself.

Signing rejects an absent key or one that does not match an embedded public key.
No unsigned fallback exists. For rotation, first ship a desktop trusting current
and next operator keys, then switch the protected signing secret to the next
key. Keep the previous public key until installed desktops can accept its
replacement. Never put private keys in release files, workflow logs or artifacts.
