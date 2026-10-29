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
# GitHub is reachable per host, not as a whole: github.com and
# raw.githubusercontent.com can time out while api.github.com still answers. When
# the official host fails, downloads fall back to a short list of GitHub proxies.
# The checksum is still verified, and a mirror checksum is corroborated by a
# second source before the archive is trusted. Set BERTH_INSTALL_MIRRORS='' to use
# only the official host.
#
# Flags:
#   --dir DIR          install directory (default: $HOME/.local/bin)
#   --version TAG      install a pinned release instead of the latest
#   --no-modify-path   leave shell startup files alone
#
# Environment:
#   BERTH_INSTALL_MIRRORS  space-separated GitHub proxy prefixes, tried after the
#                          official host (default: https://ghfast.top https://gh-proxy.com).
#                          Empty disables the fallback.
#   BERTH_INSTALL_VERSION  same as --version
#   BERTH_INSTALL_DIR      same as --dir
#   BERTH_INSTALL_QUIET=1  no progress bar
set -eu

REPO="${BERTH_INSTALL_REPO:-SheathedSharp/option-berth}"
DEFAULT_BASE_URL="https://github.com/$REPO/releases/download"
DEFAULT_LATEST_URL="https://github.com/$REPO/releases/latest"
DEFAULT_RAW_URL="https://raw.githubusercontent.com/$REPO"
DEFAULT_MIRRORS="https://ghfast.top https://gh-proxy.com"
BASE_URL="${BERTH_INSTALL_BASE_URL:-$DEFAULT_BASE_URL}"
LATEST_URL="${BERTH_INSTALL_LATEST_URL:-$DEFAULT_LATEST_URL}"
RAW_URL="${BERTH_INSTALL_RAW_URL:-$DEFAULT_RAW_URL}"
MIRRORS="${BERTH_INSTALL_MIRRORS-$DEFAULT_MIRRORS}"
VERSION="${BERTH_INSTALL_VERSION:-}"
DIR="${BERTH_INSTALL_DIR:-$HOME/.local/bin}"
DATA_DIR="${BERTH_INSTALL_DATA_DIR:-${XDG_DATA_HOME:-$HOME/.local/share}/option-berth}"
MODIFY_PATH=1
CHECKSUM=""

if [ -t 2 ] && [ "${BERTH_INSTALL_QUIET:-0}" != 1 ]; then
	PROGRESS="--progress-bar"
else
	PROGRESS="--silent --show-error"
fi

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

Environment:
  BERTH_INSTALL_MIRRORS  space-separated GitHub proxy prefixes tried after the
                         official host. Empty disables the fallback.
  BERTH_INSTALL_QUIET=1  no progress bar
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

# One quick probe decides whether the official host is worth trying. On a
# blocked network github.com costs a full connect timeout per request, so
# skipping it after a single 5s probe removes roughly a minute of dead time.
official_github_up() {
	OFFICIAL_UP=1
	if [ -z "$MIRRORS" ]; then return; fi
	# A pinned custom source already owns its own routing and must not trigger a
	# probe against a host it does not use.
	if [ "$BASE_URL" != "$DEFAULT_BASE_URL" ] && [ "$LATEST_URL" != "$DEFAULT_LATEST_URL" ]; then
		return
	fi
	OFFICIAL_UP=0
	if curl --fail --silent --show-error --head \
		--connect-timeout 5 --max-time 8 https://github.com >/dev/null 2>&1; then
		OFFICIAL_UP=1
	fi
}

# Source order: the official host first, then each proxy prefix applied to the
# official URL. A prefix is the ghfast.top form (https://proxy/https://github.com/...).
# A pinned custom URL always stays in the list, because it owns its own routing.
build_sources() {
	official_github_up
	ASSET_BASES=""
	if [ "$BASE_URL" != "$DEFAULT_BASE_URL" ] || [ -z "$MIRRORS" ] || [ "$OFFICIAL_UP" = 1 ]; then
		ASSET_BASES=$BASE_URL
	elif [ -n "$MIRRORS" ]; then
		echo "install: official GitHub host is unreachable; using the mirror list" >&2
	fi
	RAW_BASES=$RAW_URL
	LATEST_LIST=""
	if [ "$LATEST_URL" != "$DEFAULT_LATEST_URL" ] || [ -z "$MIRRORS" ] || [ "$OFFICIAL_UP" = 1 ]; then
		LATEST_LIST=$LATEST_URL
	fi
	for prefix in $MIRRORS; do
		prefix=${prefix%/}
		ASSET_BASES="$ASSET_BASES $prefix/https://github.com/$REPO/releases/download"
		RAW_BASES="$RAW_BASES $prefix/https://raw.githubusercontent.com/$REPO"
		LATEST_LIST="$LATEST_LIST $prefix/https://github.com/$REPO/releases/latest"
	done
	ASSET_BASES=${ASSET_BASES# }
	RAW_BASES=${RAW_BASES# }
	LATEST_LIST=${LATEST_LIST# }
}

# Small metadata requests never show a progress bar and give up quickly so a
# blocked host cannot hold the whole install for minutes.
fetch_quiet() {
	curl --fail --location --silent --show-error \
		--connect-timeout 10 --retry 1 --retry-delay 1 --max-time 60 \
		--output "$2" "$1"
}

# Downloads print the source they are about to use, so a fallback is visible
# instead of silent. The official host is abandoned below 200KB/s for 15s, a
# mirror below 50KB/s for 20s; the caller makes a second, patient pass when
# every source was merely slow.
fetch_file() {
	printf 'install:   %s\n' "$1" >&2
	case "${3:-fast}" in
	patient)
		# Patient still means "not dead": a stalled transfer hands over instead
		# of hanging forever.
		curl --fail --location --show-error $PROGRESS \
			--connect-timeout 10 --retry 2 --retry-delay 1 \
			--speed-limit 1024 --speed-time 60 --max-time 1800 \
			--output "$2" "$1"
		;;
	strict)
		# The official host is the one most often throttled, so it gets a short
		# leash; the first mirror that can beat 200KB/s wins.
		# No --retry here: a slow source should hand over to the next one
		# instead of stalling the install through its own retries.
		curl --fail --location --show-error $PROGRESS \
			--connect-timeout 10 \
			--speed-limit 204800 --speed-time 15 --max-time 120 \
			--output "$2" "$1"
		;;
	*)
		curl --fail --location --show-error $PROGRESS \
			--connect-timeout 10 \
			--speed-limit 51200 --speed-time 20 --max-time 180 \
			--output "$2" "$1"
		;;
	esac
}

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
	for url in $LATEST_LIST; do
		effective=$(curl --fail --location --silent --show-error \
			--connect-timeout 10 --retry 1 --max-time 30 \
			--output /dev/null --write-out '%{url_effective}' "$url" 2>/dev/null) || effective=""
		candidate=${effective##*/}
		case "$candidate" in
		v[0-9]*.[0-9]*.[0-9]*)
			VERSION=$candidate
			return
			;;
		esac
	done
	# Different hosts are blocked independently, so keep two non-GitHub
	# fallbacks for the version only. They are skipped when the caller pinned a
	# custom latest URL, because that URL owns the resolution contract.
	if [ "$LATEST_URL" = "$DEFAULT_LATEST_URL" ]; then
		body=$(curl --fail --location --silent --show-error --max-time 20 \
			"https://api.github.com/repos/$REPO/releases/latest" 2>/dev/null) || body=""
		candidate=$(printf '%s\n' "$body" |
			sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\(v[0-9][0-9.]*\)".*/\1/p' | head -1)
		if [ -n "$candidate" ]; then
			VERSION=$candidate
			return
		fi
		body=$(curl --fail --location --silent --show-error --max-time 20 \
			"https://cdn.jsdelivr.net/gh/$REPO@main/VERSION" 2>/dev/null) || body=""
		candidate=$(printf '%s' "$body" | tr -d '[:space:]')
		case "$candidate" in
		"" | *[!0-9.]*) ;;
		[0-9]*.[0-9]*.[0-9]*)
			VERSION="v$candidate"
			return
			;;
		esac
	fi
	fail "could not resolve the latest release from any source; pass --version vX.Y.Z"
}

# Prints the digest recorded in a sidecar, or fails if the sidecar is malformed
# or names a different file.
checksum_from_file() {
	expected=""
	recorded=""
	read -r expected recorded <"$1" || true
	case "$expected" in
	"" | *[!0-9a-fA-F]*) return 1 ;;
	esac
	[ ${#expected} -eq 64 ] || return 1
	[ "$recorded" = "$2" ] || return 1
	printf '%s\n' "$expected"
}

# Fetches the sidecar before the archive. The official sidecar is authoritative.
# When it comes from a mirror, a second, different source must report the same
# digest: a proxy that rewrites the archive would also have to rewrite the
# checksum on another host. A malformed sidecar only skips that source; two
# well-formed but different digests stop the install.
resolve_checksum() {
	archive=$1
	destination=$2
	first=""
	for base in $ASSET_BASES; do
		if ! fetch_quiet "$base/$VERSION/$archive.sha256" "$destination.sha256"; then
			continue
		fi
		digest=$(checksum_from_file "$destination.sha256" "$archive") || continue
		if [ -z "$first" ]; then
			first=$digest
			[ "$base" = "$BASE_URL" ] && break
		elif [ "$digest" != "$first" ]; then
			fail "checksum sources disagree for $archive: $base reports $digest"
		else
			break
		fi
	done
	[ -n "$first" ] || fail "could not download a valid $archive.sha256 from any source"
	CHECKSUM=$first
}

download_archive() {
	archive=$1
	destination=$2
	for base in $ASSET_BASES; do
		mode=fast
		if [ "$base" = "$BASE_URL" ]; then
			mode=strict
		fi
		if fetch_file "$base/$VERSION/$archive" "$destination" "$mode"; then
			return 0
		fi
	done
	echo "install: no source was fast enough; retrying without the speed limit" >&2
	for base in $ASSET_BASES; do
		if fetch_file "$base/$VERSION/$archive" "$destination" patient; then
			return 0
		fi
	done
	fail "could not download $archive from any source
Tried:
$(for base in $ASSET_BASES; do printf '  %s\n' "$base/$VERSION/$archive"; done)
Set BERTH_INSTALL_MIRRORS to a working proxy, or download the release manually:
https://github.com/$REPO/releases"
}

download_raw() {
	path=$1
	destination=$2
	for base in $RAW_BASES; do
		if fetch_file "$base/$path" "$destination"; then
			return 0
		fi
	done
	return 1
}

build_sources
resolve_version

archive="option-berth-$VERSION-$os-$arch.tar.gz"
tmp=$(mktemp -d "${TMPDIR:-/tmp}/berth-install.XXXXXX")
cleanup() { rm -rf "$tmp"; }
trap cleanup 0
trap 'exit 1' 1 2 15

echo "install: $REPO $VERSION ($os/$arch)"
resolve_checksum "$archive" "$tmp/$archive"
download_archive "$archive" "$tmp/$archive"

actual=$(sha256_file "$tmp/$archive")
[ "$actual" = "$CHECKSUM" ] || fail "SHA256 mismatch for $archive"

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
		download_raw "$VERSION/scripts/install-path.sh" "$tmp/install-path.sh" ||
			fail "installed $DIR/oberth, but could not fetch install-path.sh; add $DIR to PATH"
		helper="$tmp/install-path.sh"
	fi
	sh "$helper" "$DIR" || fail "installed $DIR/oberth, but PATH setup failed"
else
	echo "PATH       not modified. Add this directory to PATH: $DIR"
fi

echo
"$DIR/oberth" version || fail "installed $DIR/oberth, but it did not run"
