#!/bin/sh

set -eu

project_directory=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
installer="$project_directory/install.sh"
test_directory=$(mktemp -d "${TMPDIR:-/tmp}/alih-installer-test.XXXXXX")
trap 'rm -rf "$test_directory"' EXIT HUP INT TERM

fake_bin="$test_directory/bin"
state_directory="$test_directory/state"
mkdir -p "$fake_bin" "$state_directory"

cat >"$fake_bin/uname" <<'EOF'
#!/bin/sh
case "$1" in
    -s) printf '%s\n' "$TEST_UNAME_S" ;;
    -m) printf '%s\n' "$TEST_UNAME_M" ;;
    *) exit 1 ;;
esac
EOF

cat >"$fake_bin/curl" <<'EOF'
#!/bin/sh
output=""
url=""
while [ "$#" -gt 0 ]; do
    case "$1" in
        --output|-o)
            output=$2
            shift 2
            ;;
        --write-out|-w|--proto)
            shift 2
            ;;
        --tlsv1.2|--fail|--silent|--show-error|--location)
            shift
            ;;
        *)
            url=$1
            shift
            ;;
    esac
done

printf '%s\n' "$url" >>"$TEST_STATE/urls"
case "$url" in
    */releases/latest)
        printf '%s' 'https://github.com/rinorouu/alih/releases/tag/v0.2.4'
        ;;
    */SHA256SUMS)
        artifact=$(cat "$TEST_STATE/artifact")
        if [ "${TEST_CHECKSUM_MISMATCH:-0}" = 1 ]; then
            hash=0000000000000000000000000000000000000000000000000000000000000000
        else
            hash=$(sha256sum "$TEST_FIXTURE_BINARY" | awk '{ print $1 }')
        fi
        printf '%s  %s\n' "$hash" "$artifact" >"$output"
        ;;
    */alih-*)
        if [ "${TEST_DOWNLOAD_FAILURE:-0}" = 1 ]; then
            exit 22
        fi
        artifact=${url##*/}
        printf '%s\n' "$artifact" >"$TEST_STATE/artifact"
        cp "$TEST_FIXTURE_BINARY" "$output"
        ;;
    *)
        exit 22
        ;;
esac
EOF

chmod 0755 "$fake_bin/uname" "$fake_bin/curl"

fixture_binary="$test_directory/fixture-alih"
cat >"$fixture_binary" <<'EOF'
#!/bin/sh
if [ "${1:-}" = "--version" ]; then
    printf '%s\n' 'alih 0.2.4'
    exit 0
fi
exit 2
EOF
chmod 0755 "$fixture_binary"

fail() {
	printf 'installer test failed: %s\n' "$*" >&2
	exit 1
}

reset_state() {
	rm -rf "$state_directory"
	mkdir -p "$state_directory"
}

run_success() {
	name=$1
	os=$2
	architecture=$3
	expected_artifact=$4
	reset_state
	install_directory="$test_directory/install-$name"
	PATH="$fake_bin:$PATH" \
		TEST_UNAME_S="$os" TEST_UNAME_M="$architecture" \
		TEST_STATE="$state_directory" TEST_FIXTURE_BINARY="$fixture_binary" \
		ALIH_INSTALL_DIR="$install_directory" \
		sh "$installer" >"$state_directory/output" 2>&1 || fail "$name should succeed"
	[ -x "$install_directory/alih" ] || fail "$name did not install an executable"
	[ "$("$install_directory/alih" --version)" = "alih 0.2.4" ] || fail "$name installed the wrong binary"
	grep -q "/$expected_artifact$" "$state_directory/urls" || fail "$name requested the wrong artifact"
}

run_unsupported() {
	name=$1
	os=$2
	architecture=$3
	reset_state
	if PATH="$fake_bin:$PATH" \
		TEST_UNAME_S="$os" TEST_UNAME_M="$architecture" \
		TEST_STATE="$state_directory" TEST_FIXTURE_BINARY="$fixture_binary" \
		ALIH_INSTALL_DIR="$test_directory/install-$name" \
		sh "$installer" >"$state_directory/output" 2>&1; then
		fail "$name should fail"
	fi
	grep -q 'unsupported' "$state_directory/output" || fail "$name did not explain the unsupported platform"
	[ ! -e "$state_directory/urls" ] || fail "$name attempted a download"
}

run_preserves_existing() {
	name=$1
	failure_variable=$2
	reset_state
	install_directory="$test_directory/install-$name"
	mkdir -p "$install_directory"
	printf '%s\n' 'existing Alih' >"$install_directory/alih"
	before=$(cat "$install_directory/alih")
	if env PATH="$fake_bin:$PATH" \
		TEST_UNAME_S=Linux TEST_UNAME_M=x86_64 \
		TEST_STATE="$state_directory" TEST_FIXTURE_BINARY="$fixture_binary" \
		ALIH_INSTALL_DIR="$install_directory" "$failure_variable=1" \
		sh "$installer" >"$state_directory/output" 2>&1; then
		fail "$name should fail"
	fi
	after=$(cat "$install_directory/alih")
	[ "$after" = "$before" ] || fail "$name replaced the existing installation"
}

run_success linux-x86_64 Linux x86_64 alih-linux-amd64
run_success linux-amd64 Linux amd64 alih-linux-amd64
run_success linux-aarch64 Linux aarch64 alih-linux-arm64
run_success linux-arm64 Linux arm64 alih-linux-arm64
run_success macos-x86_64 Darwin x86_64 alih-darwin-amd64
run_success macos-arm64 Darwin arm64 alih-darwin-arm64
run_unsupported unsupported-os FreeBSD x86_64
run_unsupported unsupported-architecture Linux riscv64
run_preserves_existing checksum-mismatch TEST_CHECKSUM_MISMATCH
run_preserves_existing download-failure TEST_DOWNLOAD_FAILURE

printf '%s\n' 'install.sh tests passed'
