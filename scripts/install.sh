#!/bin/sh

set -eu

REPO="agentworkforce/relayfile"
CLI_BIN="relayfile-cli"
INSTALL_PATH="${INSTALL_PATH:-/usr/local/bin/relayfile}"
RELEASE_TAG="${RELAYFILE_VERSION:-}"
CHECKSUM_FILE="checksums.txt"
TMP_DIR="$(mktemp -d)"

cleanup() {
	rm -rf "$TMP_DIR"
}
trap cleanup EXIT INT TERM

log() {
	printf '%s\n' "$*" >&2
}

fail() {
	log "error: $*"
	exit 1
}

require_cmd() {
	if ! command -v "$1" >/dev/null 2>&1; then
		fail "missing required command: $1"
	fi
}

detect_os() {
	case "$(uname -s)" in
		Darwin) printf 'darwin' ;;
		Linux) printf 'linux' ;;
		*) fail "unsupported operating system: $(uname -s)" ;;
	esac
}

detect_arch() {
	case "$(uname -m)" in
		x86_64|amd64) printf 'amd64' ;;
		arm64|aarch64) printf 'arm64' ;;
		*) fail "unsupported architecture: $(uname -m)" ;;
	esac
}

fetch_latest_tag() {
	curl -fsSL \
		-H "Accept: application/vnd.github+json" \
		-H "User-Agent: relayfile-install-script" \
		"https://api.github.com/repos/${REPO}/releases/latest" | \
		sed -n 's/.*"tag_name":[[:space:]]*"\([^"]*\)".*/\1/p' | head -n 1
}

verify_checksum() {
	archive_path="$1"
	checksums_path="$2"
	archive_name="$(basename "$archive_path")"
	expected_line="$(grep "[[:space:]]${archive_name}\$" "$checksums_path" || true)"
	if [ -z "$expected_line" ]; then
		fail "checksum entry not found for ${archive_name}"
	fi

	expected_sum="$(printf '%s\n' "$expected_line" | awk '{print $1}')"
	if command -v sha256sum >/dev/null 2>&1; then
		actual_sum="$(sha256sum "$archive_path" | awk '{print $1}')"
	else
		actual_sum="$(shasum -a 256 "$archive_path" | awk '{print $1}')"
	fi

	if [ "$expected_sum" != "$actual_sum" ]; then
		fail "checksum verification failed for ${archive_name}"
	fi
}

pick_install_cmd() {
	target_dir="$1"
	check_dir="$target_dir"
	while [ ! -d "$check_dir" ]; do
		next_dir="$(dirname "$check_dir")"
		if [ "$next_dir" = "$check_dir" ]; then
			break
		fi
		check_dir="$next_dir"
	done

	if [ -w "$check_dir" ]; then
		printf 'install'
		return
	fi
	if command -v sudo >/dev/null 2>&1; then
		printf 'sudo install'
		return
	fi
	fail "write access required for ${target_dir}; rerun with sudo or set INSTALL_PATH"
}

install_binary() {
	src="$1"
	dest="$2"
	dest_dir="$(dirname "$dest")"
	install_cmd="$(pick_install_cmd "$dest_dir")"
	# shellcheck disable=SC2086
	$install_cmd -d "$dest_dir"
	# shellcheck disable=SC2086
	$install_cmd -m 0755 "$src" "$dest"
}

require_cmd curl
require_cmd tar
require_cmd grep
require_cmd sed
require_cmd awk
if ! command -v sha256sum >/dev/null 2>&1 && ! command -v shasum >/dev/null 2>&1; then
	fail "missing required checksum tool: sha256sum or shasum"
fi

OS="$(detect_os)"
ARCH="$(detect_arch)"

if [ -z "$RELEASE_TAG" ]; then
	RELEASE_TAG="$(fetch_latest_tag)"
fi
if [ -z "$RELEASE_TAG" ]; then
	fail "failed to resolve latest release tag"
fi

ARCHIVE_NAME="${CLI_BIN}_${OS}_${ARCH}.tar.gz"
BASE_URL="https://github.com/${REPO}/releases/download/${RELEASE_TAG}"
ARCHIVE_PATH="${TMP_DIR}/${ARCHIVE_NAME}"
CHECKSUM_PATH="${TMP_DIR}/${CHECKSUM_FILE}"

log "downloading ${ARCHIVE_NAME} from ${RELEASE_TAG}"
curl -fsSL "${BASE_URL}/${ARCHIVE_NAME}" -o "$ARCHIVE_PATH"
curl -fsSL "${BASE_URL}/${CHECKSUM_FILE}" -o "$CHECKSUM_PATH"

verify_checksum "$ARCHIVE_PATH" "$CHECKSUM_PATH"
tar -xzf "$ARCHIVE_PATH" -C "$TMP_DIR"

if [ ! -f "${TMP_DIR}/${CLI_BIN}" ]; then
	fail "archive did not contain ${CLI_BIN}"
fi

install_binary "${TMP_DIR}/${CLI_BIN}" "$INSTALL_PATH"
log "installed relayfile to ${INSTALL_PATH}"
