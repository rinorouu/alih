#!/bin/sh

set -eu
umask 077

repository_url="https://github.com/rinorouu/alih"
latest_release_url="$repository_url/releases/latest"
temporary_directory=""
temporary_install_path=""

cleanup() {
	if [ -n "$temporary_install_path" ]; then
		rm -f "$temporary_install_path"
	fi
	if [ -n "$temporary_directory" ]; then
		rm -rf "$temporary_directory"
	fi
}

fail() {
	printf 'Alih installation failed: %s\n' "$*" >&2
	exit 1
}

trap cleanup EXIT HUP INT TERM

command -v curl >/dev/null 2>&1 || fail "curl is required."
command -v awk >/dev/null 2>&1 || fail "awk is required."

operating_system=$(uname -s) || fail "could not detect the operating system."
architecture=$(uname -m) || fail "could not detect the architecture."

case "$operating_system" in
	Linux)
		platform="linux"
		;;
	Darwin)
		platform="darwin"
		;;
	*)
		fail "unsupported operating system: $operating_system. Supported systems are Linux and macOS."
		;;
esac

case "$architecture" in
	x86_64 | amd64)
		machine="amd64"
		;;
	arm64 | aarch64)
		machine="arm64"
		;;
	*)
		fail "unsupported architecture: $architecture. Supported architectures are amd64 and arm64."
		;;
esac

artifact="alih-$platform-$machine"

latest_url=$(curl --proto '=https' --tlsv1.2 --fail --silent --show-error \
	--location --output /dev/null --write-out '%{url_effective}' \
	"$latest_release_url") || fail "could not determine the latest stable release."

case "$latest_url" in
	"$repository_url/releases/tag/"*)
		tag=${latest_url#"$repository_url/releases/tag/"}
		;;
	*)
		fail "GitHub returned an unexpected latest-release URL."
		;;
esac

case "$tag" in
	v[0-9]*)
		case "$tag" in
			*[!A-Za-z0-9._-]*) fail "GitHub returned an invalid release tag." ;;
		esac
		;;
	*)
		fail "GitHub returned an invalid release tag."
		;;
esac

temporary_directory=$(mktemp -d "${TMPDIR:-/tmp}/alih-install.XXXXXX") || \
	fail "could not create a temporary directory."
binary_path="$temporary_directory/$artifact"
checksums_path="$temporary_directory/SHA256SUMS"
release_download_url="$repository_url/releases/download/$tag"

download() {
	url=$1
	destination=$2
	curl --proto '=https' --tlsv1.2 --fail --silent --show-error \
		--location --output "$destination" "$url"
}

download "$release_download_url/$artifact" "$binary_path" || \
	fail "could not download $artifact from release $tag."
download "$release_download_url/SHA256SUMS" "$checksums_path" || \
	fail "could not download SHA256SUMS from release $tag."

expected_hash=$(awk -v artifact="$artifact" '
	$2 == artifact || $2 == "*" artifact { count++; hash = $1 }
	END { if (count == 1) print hash; else exit 1 }
' "$checksums_path") || fail "SHA256SUMS does not contain exactly one entry for $artifact."

if [ "${#expected_hash}" -ne 64 ]; then
	fail "SHA256SUMS contains an invalid checksum for $artifact."
fi
case "$expected_hash" in
	*[!0-9A-Fa-f]* | '') fail "SHA256SUMS contains an invalid checksum for $artifact." ;;
esac

if command -v sha256sum >/dev/null 2>&1; then
	actual_hash=$(sha256sum "$binary_path" | awk '{ print $1 }')
elif command -v shasum >/dev/null 2>&1; then
	actual_hash=$(shasum -a 256 "$binary_path" | awk '{ print $1 }')
else
	fail "sha256sum or shasum is required to verify the download."
fi

expected_hash=$(printf '%s' "$expected_hash" | tr 'A-F' 'a-f')
actual_hash=$(printf '%s' "$actual_hash" | tr 'A-F' 'a-f')
if [ "$actual_hash" != "$expected_hash" ]; then
	fail "SHA-256 checksum verification failed for $artifact; the existing Alih installation was not changed."
fi

chmod 0755 "$binary_path" || fail "could not make the verified binary executable."
expected_version="alih ${tag#v}"
reported_version=$("$binary_path" --version) || fail "the verified binary could not be executed."
if [ "$reported_version" != "$expected_version" ]; then
	fail "the verified binary reported '$reported_version', expected '$expected_version'."
fi

if [ -n "${ALIH_INSTALL_DIR:-}" ]; then
	install_directory=$ALIH_INSTALL_DIR
else
	[ -n "${HOME:-}" ] || fail "HOME is not set; set ALIH_INSTALL_DIR to an absolute user-writable directory."
	install_directory="$HOME/.local/bin"
fi
case "$install_directory" in
	/*) ;;
	*) fail "installation directory must be an absolute path: $install_directory" ;;
esac

mkdir -p "$install_directory" || fail "could not create $install_directory."
[ -d "$install_directory" ] || fail "$install_directory is not a directory."
[ -w "$install_directory" ] || fail "$install_directory is not writable. No privilege escalation was attempted."

install_path="$install_directory/alih"
if [ -d "$install_path" ]; then
	fail "$install_path is a directory and cannot be replaced."
fi
temporary_install_path=$(mktemp "$install_directory/.alih.XXXXXX") || \
	fail "could not create a temporary file in $install_directory."
cp "$binary_path" "$temporary_install_path" || fail "could not stage the verified binary."
chmod 0755 "$temporary_install_path" || fail "could not protect the staged binary."
mv -f "$temporary_install_path" "$install_path" || fail "could not install Alih at $install_path."
temporary_install_path=""

printf 'Alih %s installed successfully.\n' "${tag#v}"
printf 'Installed to: %s\n' "$install_path"
case ":${PATH:-}:" in
	*":$install_directory:"*)
		printf '\nNext: run "alih setup" to choose how Alih is operated.\n'
		;;
	*)
		printf 'The installation directory is not currently in PATH.\n'
		printf 'Add this directory to PATH: %s\n' "$install_directory"
		printf '\nNext: run "%s setup" to choose how Alih is operated.\n' "$install_path"
		;;
esac
