#!/bin/sh
# Install the CARE Clinic host service as a LaunchDaemon (developer install).
#
#   scripts/service-macos.sh install            build, sign and install; start at boot
#   scripts/service-macos.sh uninstall [--purge] stop and remove (--purge also
#                                                deletes the engine data disk)
#   scripts/service-macos.sh status             launchd and service status
#
# Run as your admin user, not with sudo: install builds the binary as you,
# then re-runs itself with sudo for the root steps.
set -eu

label=org.ohc.care-clinic
group=care-clinic
root="/Library/Application Support/care-clinic"
bin="$root/bin/care-clinic-service"
plist=/Library/LaunchDaemons/$label.plist
logdir=/Library/Logs/care-clinic
repo=$(cd "$(dirname "$0")/.." && pwd)

die() {
	echo "service-macos: $*" >&2
	exit 1
}

need_root() {
	[ "$(id -u)" -eq 0 ] || die "$1 needs root"
	[ -n "${SUDO_USER:-}" ] && [ "$SUDO_USER" != root ] || die "run via sudo from your admin account"
}

loaded() {
	launchctl print "system/$label" >/dev/null 2>&1
}

install_root() {
	built=$1
	need_root install

	if ! dseditgroup -o read "$group" >/dev/null 2>&1; then
		echo "creating group $group"
		dseditgroup -o create -r "CARE Clinic service users" "$group"
	fi
	if ! dseditgroup -o checkmember -m "$SUDO_USER" "$group" >/dev/null 2>&1; then
		echo "adding $SUDO_USER to $group"
		dseditgroup -o edit -a "$SUDO_USER" -t user "$group"
	fi

	# Root-owned all the way up, so no user can swap the binary root runs.
	install -d -o root -g wheel -m 0755 "$root" "$root/bin" "$logdir"
	install -o root -g wheel -m 0755 "$built" "$bin.new"
	mv -f "$bin.new" "$bin"

	if loaded; then
		echo "stopping the running service"
		launchctl bootout "system/$label" || true
	fi
	install -o root -g wheel -m 0644 "$repo/app/cmd/care-clinic-service/launchd/$label.plist" "$plist"
	launchctl bootstrap system "$plist"
	echo "installed and started $label"

	ctl="'$bin' ctl"
	cat <<EOF

Next steps:
  $ctl status
EOF
	tarball=$(find "$repo/dist" -maxdepth 1 -name 'care-engine-*-arm64.tar.gz' 2>/dev/null | sort | tail -n 1)
	if [ -n "$tarball" ]; then
		name=$(basename "$tarball")
		version=${name#care-engine-}
		version=${version%-arm64.tar.gz}
		sum=$(awk -v f="$name" '$2 == f || $2 == "*" f { print $1 }' "$repo/dist/SHA256SUMS" 2>/dev/null || true)
		[ -n "$sum" ] || sum=$(shasum -a 256 "$tarball" | awk '{ print $1 }')
		cat <<EOF
  $ctl install '$tarball' $sum
  $ctl activate $version
EOF
	else
		cat <<EOF
  engine/build.sh arm64      # no engine bundle in dist/ yet
  $ctl install dist/care-engine-<version>-arm64.tar.gz <sha256>
  $ctl activate <version>
EOF
	fi
	cat <<EOF
  $ctl watch                 # until the engine is ready
  $ctl set-name <name>       # advertise <name>.local instead of care.local

If ctl is refused, open a new terminal (or log out and in) so your shell
picks up the $group group. Logs: $logdir/service.log
EOF
}

install_cmd() {
	if [ "$(id -u)" -eq 0 ]; then
		die "run install as your user (it builds as you, then uses sudo)"
	fi
	tmp=$(mktemp -d)
	trap 'rm -rf "$tmp"' EXIT
	"$repo/app/cmd/care-clinic-service/build.sh" "$tmp/care-clinic-service"
	sudo "$0" install-built "$tmp/care-clinic-service"
}

uninstall_cmd() {
	purge=${1:-}
	[ -z "$purge" ] || [ "$purge" = --purge ] || die "unknown option $purge"
	if [ "$(id -u)" -ne 0 ]; then
		exec sudo "$0" uninstall "$@"
	fi
	if loaded; then
		launchctl bootout "system/$label"
	fi
	rm -f "$plist" "$bin"
	rmdir "$root/bin" 2>/dev/null || true
	echo "removed $label (the $group group is kept)"
	if [ "$purge" = --purge ]; then
		rm -rf "$root" "$logdir"
		echo "purged $root and $logdir"
	else
		echo "kept engine data and slots in $root (use --purge to delete them)"
	fi
}

status_cmd() {
	if [ -f "$plist" ]; then
		echo "plist: $plist"
	else
		echo "plist: not installed"
	fi
	if loaded; then
		launchctl print "system/$label" | grep -E '^\s*(state|pid|last exit code|runs) ='
	else
		echo "launchd: not loaded"
	fi
	if [ -x "$bin" ]; then
		"$bin" ctl status || true
	fi
}

case "${1:-}" in
install) install_cmd ;;
install-built)
	[ $# -eq 2 ] || die "usage: install-built <binary>"
	install_root "$2"
	;;
uninstall)
	shift
	uninstall_cmd "$@"
	;;
status) status_cmd ;;
*)
	sed -n '2,10p' "$0" >&2
	exit 2
	;;
esac
