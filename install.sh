#!/bin/sh
#
# Install the released option-berth CLI without a Go toolchain.
#
#   curl -fsSL https://raw.githubusercontent.com/SheathedSharp/option-berth/main/install.sh | sh
#
# The script resolves the latest engine release, downloads the archive for this
# machine, verifies its SHA256 sidecar, and installs oberth + jev-attention into
# ~/.local/bin. Licences and the build manifest stay in ~/.local/share/option-berth.
# It starts no services and writes no Git data.
#
# Flags:
#   --dir DIR          install directory (default: $HOME/.local/bin)
#   --version TAG      install a pinned release instead of the latest
#   --no-modify-path   leave shell startup files alone
set -eu

REPO="${BERTH_INSTALL_REPO:-SheathedSharp/option-berth}"
BASE_URL="${BERTH_INSTALL_BASE_URL:-https://github.com/$REPO/releases/download}"
LATEST_URL="${BERTH_INSTALL_LATEST_URL:-https://github.com/$REPO/releases/latest}"
RAW_URL="${BERTH_INSTALL_RAW_URL:-https://raw.githubusercontent.com/$REPO}"
VERSION="${BERTH_INSTALL_VERSION:-}"
DIR="${BERTH_INSTALL_DIR:-$HOME/.local/bin}"
DATA_DIR="${BERTH_INSTALL_DATA_DIR:-${XDG_DATA_HOME:-$HOME/.local/share}/option-berth}"
MODIFY_PATH=1

fail() {
	echo "install: $*" >&2
	exit 1
}

usage() {
	cat <<'EOF'
Install the released option-berth CLI without a Go toolchain.

  curl -fsSL https://raw.githubusercontent.com/SheathedSharp/option-berth/main/install.sh | sh

Flags:
  --dir DIR          install directory (default: $HOME/.local/bin)
  --version TAG      install a pinned release instead of the latest
  --no-modify-path   leave shell startup files alone
EOF
}

while [ $# -gt 0 ]; do
	case "$1" in
	--dir)
		[ $# -ge 2 ] || fail "--dir needs a value"
		DIR=$2
		shift 2
		;;
	--version)
		[ $# -ge 2 ] || fail "--version needs a value"
		VERSION=$2
		shift 2
		;;
	--no-modify-path)
		MODIFY_PATH=0
		shift
		;;
	-h | --help)
		usage
		exit 0
		;;
	*)
		fail "unknown argument: $1"
		;;
	esac
done

case "$DIR" in
"~/"*) DIR="$HOME/${DIR#~/}" ;;
esac

os=$(uname -s)
case "$os" in
Darwin) os=darwin ;;
Linux) os=linux ;;
*) fail "unsupported operating system: $os (Windows uses install.ps1)" ;;
esac

machine=$(uname -m)
case "$machine" in
x86_64 | amd64) arch=amd64 ;;
arm64 | aarch64) arch=arm64 ;;
*) fail "unsupported architecture: $machine" ;;
esac

if command -v sha256sum >/dev/null 2>&1; then
	sha256_file() { sha256sum "$1" | awk '{print $1}'; }
elif command -v shasum >/dev/null 2>&1; then
	sha256_file() { shasum -a 256 "$1" | awk '{print $1}'; }
else
	fail "need sha256sum or shasum to verify the download"
fi

resolve_version() {
	if [ -n "$VERSION" ]; then
		case "$VERSION" in
		v*) ;;
		*) VERSION="v$VERSION" ;;
		esac
		case "$VERSION" in
		v[0-9]*.[0-9]*.[0-9]*) ;;
		*) fail "invalid version: $VERSION" ;;
		esac
		return
	fi
	effective=$(curl --fail --location --silent --show-error --max-redirs 5 \
		--output /dev/null --write-out '%{url_effective}' "$LATEST_URL") ||
		fail "could not resolve the latest release from $LATEST_URL"
	VERSION=${effective##*/}
	case "$VERSION" in
	v[0-9]*.[0-9]*.[0-9]*) ;;
	*) fail "latest release did not resolve to a version tag (got: $effective)" ;;
	esac
}

fetch() {
	curl --fail --location --silent --show-error --retry 3 --connect-timeout 15 \
		--output "$2" "$1" || fail "download failed: $1"
}

resolve_version

archive="option-berth-$VERSION-$os-$arch.tar.gz"
tmp=$(mktemp -d "${TMPDIR:-/tmp}/berth-install.XXXXXX")
cleanup() { rm -rf "$tmp"; }
trap cleanup 0
trap 'exit 1' 1 2 15

echo "install: $REPO $VERSION ($os/$arch)"
fetch "$BASE_URL/$VERSION/$archive" "$tmp/$archive"
fetch "$BASE_URL/$VERSION/$archive.sha256" "$tmp/$archive.sha256"

expected=""
recorded=""
read -r expected recorded <"$tmp/$archive.sha256" || true
case "$expected" in
"" | *[!0-9a-fA-F]*) fail "checksum file is malformed" ;;
esac
[ ${#expected} -eq 64 ] || fail "checksum file is malformed"
[ "$recorded" = "$archive" ] || fail "checksum names $recorded, expected $archive"
actual=$(sha256_file "$tmp/$archive")
[ "$actual" = "$expected" ] || fail "SHA256 mismatch for $archive"

tar -xzf "$tmp/$archive" -C "$tmp"
root="$tmp/option-berth-$VERSION-$os-$arch"
for name in oberth jev-attention; do
	[ -f "$root/$name" ] || fail "archive is missing $name"
done

mkdir -p "$DIR"
for name in oberth jev-attention; do
	staged="$DIR/.$name.$$"
	cp "$root/$name" "$staged"
	chmod 0755 "$staged"
	mv -f "$staged" "$DIR/$name"
done

# The release archive carries the licence and per-target notices; keep them
# beside the installed binaries instead of deleting them with the temp tree.
mkdir -p "$DATA_DIR/licenses"
if [ -f "$root/LICENSE" ]; then cp "$root/LICENSE" "$DATA_DIR/LICENSE"; fi
if [ -d "$root/licenses" ]; then cp -R "$root/licenses/." "$DATA_DIR/licenses/"; fi
if [ -f "$root/build-manifest.json" ]; then cp "$root/build-manifest.json" "$DATA_DIR/build-manifest.json"; fi
echo "installed  $DIR"
echo "notices    $DATA_DIR"

if [ "$MODIFY_PATH" = 1 ]; then
	# Old releases predate the helper inside the archive; the same file is at
	# the tag, so the PATH behaviour still has one implementation.
	helper="$root/install-path.sh"
	if [ ! -f "$helper" ]; then
		fetch "$RAW_URL/$VERSION/scripts/install-path.sh" "$tmp/install-path.sh"
		helper="$tmp/install-path.sh"
	fi
	sh "$helper" "$DIR" || fail "installed $DIR/oberth, but PATH setup failed"
else
	echo "PATH       not modified. Add this directory to PATH: $DIR"
fi

echo
"$DIR/oberth" version || fail "installed $DIR/oberth, but it did not run"
