#!/bin/sh
# Installs strux on macOS or Linux, at the release this script is published with:
#
#   curl -fsSL https://github.com/altakleos/strux/releases/latest/download/install.sh | sh
#
# It downloads the release's archive for this machine and checks its SHA-256 against the
# release's checksums.txt. When gh is installed and logged in to github.com, it also checks the
# archive's build provenance: that altakleos/strux's release workflow built it from this
# release's tag. It installs strux into ~/.local/bin, replacing any strux there, and needs only
# curl and the tools every macOS and Linux system has.
#
# STRUX_INSTALL_DIR names another directory to install into. STRUX_RELEASE_URL names another
# place to download the release's assets from, such as a mirror; the provenance check still
# requires them to be this release's.
#
# Everything runs from main, called on the last line, so a partial download runs nothing.

set -eu

version='@VERSION@'
repo=altakleos/strux

main() {
	platform
	command -v curl >/dev/null 2>&1 || die "install.sh needs curl"
	release=${STRUX_RELEASE_URL:-https://github.com/$repo/releases/download/v$version}
	dir=${STRUX_INSTALL_DIR:-$HOME/.local/bin}
	archive=strux_${version}_${os}_${arch}.tar.gz

	tmp=$(mktemp -d)
	staged=$dir/.strux.$$
	trap 'rm -rf "$tmp" "$staged"' EXIT
	trap 'exit 1' HUP INT TERM

	echo "Installing strux $version for $os/$arch from $release"
	fetch checksums.txt
	fetch "$archive"
	check_sha256
	check_provenance
	tar -xzf "$tmp/$archive" -C "$tmp" strux

	# The new binary runs before it replaces the old one, and replaces it by a rename, which
	# leaves no partly written strux even while the old one runs.
	mkdir -p "$dir"
	cp "$tmp/strux" "$staged"
	chmod 755 "$staged"
	"$staged" version >/dev/null || die "strux $version does not run on this machine"
	mv -f "$staged" "$dir/strux"
	echo "  installed:  $dir/strux"
	check_path
}

# platform sets os and arch to this machine's, as the release names them.
platform() {
	case $(uname -s) in
	Darwin) os=darwin ;;
	Linux) os=linux ;;
	MINGW* | MSYS* | CYGWIN*)
		die "on Windows, install strux from PowerShell: irm https://github.com/$repo/releases/download/v$version/install.ps1 | iex"
		;;
	*) die "strux has no release for $(uname -s)" ;;
	esac
	case $(uname -m) in
	x86_64 | amd64) arch=amd64 ;;
	arm64 | aarch64) arch=arm64 ;;
	*) die "strux has no release for $(uname -m) machines" ;;
	esac
	# A shell under Rosetta reports x86_64 on Apple silicon, which runs arm64 natively.
	if [ "$os" = darwin ] && [ "$arch" = amd64 ] && [ "$(sysctl -n sysctl.proc_translated 2>/dev/null)" = 1 ]; then
		arch=arm64
	fi
}

fetch() {
	curl --fail --silent --show-error --location --retry 3 --output "$tmp/$1" "$release/$1" ||
		die "could not download $release/$1"
}

check_sha256() {
	want=$(awk -v name="$archive" '$2 == name { print $1 }' "$tmp/checksums.txt")
	[ -n "$want" ] || die "checksums.txt lists no $archive"
	case $os in
	darwin) got=$(shasum -a 256 "$tmp/$archive") ;;
	*) got=$(sha256sum "$tmp/$archive") ;;
	esac
	got=${got%% *}
	[ "$got" = "$want" ] || die "$archive has SHA-256 $got, but checksums.txt lists $want"
	echo "  checksum:   matches checksums.txt"
}

# check_provenance checks, with gh, that the release workflow built the archive from this
# release's tag. Without a gh that can, it says so and goes on.
check_provenance() {
	if ! command -v gh >/dev/null 2>&1; then
		echo "  provenance: not checked, since gh is not installed"
	elif ! gh attestation verify --help 2>&1 | grep -q -e --source-ref; then
		echo "  provenance: not checked, since this gh is too old"
	elif ! gh auth status --hostname github.com >/dev/null 2>&1; then
		echo "  provenance: not checked, since gh is not logged in to github.com"
	elif out=$(gh attestation verify "$tmp/$archive" --repo "$repo" \
		--signer-workflow "$repo/.github/workflows/release.yml" \
		--source-ref "refs/tags/v$version" --deny-self-hosted-runners 2>&1); then
		echo "  provenance: verified by gh: built by $repo's release workflow from v$version"
	else
		die "gh could not verify that $repo's release workflow built $archive from v$version:
$out"
	fi
}

# check_path says when running strux by name would not run the strux just installed.
check_path() {
	found=$(command -v strux 2>/dev/null) || found=
	[ "$found" != "$dir/strux" ] || return 0
	case :$PATH: in
	*:"$dir":*)
		echo "  note:       $found runs instead, since it comes first on your PATH"
		;;
	*)
		echo "  note:       $dir is not on your PATH; to run strux by name, add it:"
		echo "                export PATH=\"$dir:\$PATH\""
		;;
	esac
}

die() {
	echo "install.sh: $*" >&2
	exit 1
}

main
