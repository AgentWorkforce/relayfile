#!/bin/sh

set -eu

REPO="agentworkforce/relayfile"
CLI_ASSET_PREFIX="relayfile"
INSTALL_PATH="${INSTALL_PATH:-/usr/local/bin/relayfile}"
TMP_DIR="$(mktemp -d)"
RELEASE_TAG="${RELAYFILE_VERSION:-}"

cleanup() {
	rm -rf "$TMP_DIR"
}
trap cleanup EXIT INT TERM

log() {
	printf '%s\n' "$*" >&2
}

require_cmd() {
	if ! command -v "$1" >/dev/null 2>&1; then
		log "missing required command: $1"
		exit 1
	fi
}

detect_os() {
	case "$(uname -s)" in
		Darwin) printf 'darwin' ;;
		Linux) printf 'linux' ;;
		*)
			log "unsupported operating system: $(uname -s)"
			exit 1
			;;
	esac
}

detect_arch() {
	case "$(uname -m)" in
		x86_64|amd64) printf 'amd64' ;;
		arm64|aarch64) printf 'arm64' ;;
		*)
			log "unsupported architecture: $(uname -m)"
			exit 1
			;;
	esac
}

fetch_latest_tag() {
	curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" | \
		sed -n 's/.*"tag_name":[[:space:]]*"\([^"]*\)".*/\1/p' | head -n 1
}

checksum_verify() {
	archive_path="$1"
	checksums_path="$2"
	expected_line="$(grep " $(basename "$archive_path")\$" "$checksums_path" || true)"
	if [ -z "$expected_line" ]; then
		log "checksum entry not found for $(basename "$archive_path")"
		exit 1
	fi
	expected_sum="$(printf '%s' "$expected_line" | awk '{print $1}')"
	if command -v sha256sum >/dev/null 2>&1; then
		actual_sum="$(sha256sum "$archive_path" | awk '{print $1}')"
	else
		actual_sum="$(shasum -a 256 "$archive_path" | awk '{print $1}')"
	fi
	if [ "$expected_sum" != "$actual_sum" ]; then
		log "checksum verification failed for $(basename "$archive_path")"
		exit 1
	fi
}

install_binary() {
	src="$1"
	dest="$2"
	dest_dir="$(dirname "$dest")"
	install_cmd="install"
	if [ ! -w "$dest_dir" ]; then
		if command -v sudo >/dev/null 2>&1; then
			install_cmd="sudo install"
		else
			log "write access required for $dest_dir and sudo is not available"
			exit 1
		fi
	fi
	# shellcheck disable=SC2086
	$install_cmd -d "$dest_dir"
	# shellcheck disable=SC2086
	$install_cmd "$src" "$dest"
}

require_cmd curl
require_cmd tar
require_cmd grep
require_cmd sed
require_cmd awk
if ! command -v sha256sum >/dev/null 2>&1 && ! command -v shasum >/dev/null 2>&1; then
	log "missing required checksum tool: sha256sum or shasum"
	exit 1
fi

OS="$(detect_os)"
ARCH="$(detect_arch)"
if [ -z "$RELEASE_TAG" ]; then
	RELEASE_TAG="$(fetch_latest_tag)"
fi
if [ -z "$RELEASE_TAG" ]; then
	log "failed to resolve latest release tag"
	exit 1
fi

ARCHIVE_NAME="${CLI_ASSET_PREFIX}_${OS}_${ARCH}.tar.gz"
CHECKSUM_NAME="checksums.txt"
BASE_URL="https://github.com/${REPO}/releases/download/${RELEASE_TAG}"
ARCHIVE_PATH="${TMP_DIR}/${ARCHIVE_NAME}"
CHECKSUM_PATH="${TMP_DIR}/${CHECKSUM_NAME}"

log "downloading ${ARCHIVE_NAME} from ${RELEASE_TAG}"
curl -fsSL "${BASE_URL}/${ARCHIVE_NAME}" -o "$ARCHIVE_PATH"
curl -fsSL "${BASE_URL}/${CHECKSUM_NAME}" -o "$CHECKSUM_PATH"

checksum_verify "$ARCHIVE_PATH" "$CHECKSUM_PATH"
tar -xzf "$ARCHIVE_PATH" -C "$TMP_DIR"

if [ ! -f "${TMP_DIR}/${CLI_ASSET_PREFIX}" ]; then
	log "archive did not contain ${CLI_ASSET_PREFIX}"
	exit 1
fi

install_binary "${TMP_DIR}/${CLI_ASSET_PREFIX}" "$INSTALL_PATH"
log "installed relayfile to ${INSTALL_PATH}"
