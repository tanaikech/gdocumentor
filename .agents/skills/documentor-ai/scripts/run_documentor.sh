#!/usr/bin/env bash
set -euo pipefail

# Resolve repository root from this script's location (supports skills/documentor-ai/scripts, .agents/skills/documentor-ai/scripts, ~/.claude/skills, ~/.cursor/skills, etc.)
SCRIPT_DIR="$(cd -P "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SKILL_DIR="$(cd -P "${SCRIPT_DIR}/.." && pwd)"

find_repo_root() {
  local dir="$1"
  while [[ "$dir" != "/" ]]; do
    if [[ -f "${dir}/go.mod" && -f "${dir}/main.go" ]]; then
      echo "$dir"
      return 0
    fi
    dir="$(dirname "$dir")"
  done
  return 1
}

find_installed_bin() {
  if command -v gdocumentor >/dev/null 2>&1; then
    command -v gdocumentor
    return 0
  fi
  for cand in "${HOME}/.local/bin/gdocumentor" "${HOME}/go/bin/gdocumentor" "/usr/local/bin/gdocumentor" "/opt/homebrew/bin/gdocumentor"; do
    if [[ -x "${cand}" ]]; then
      echo "${cand}"
      return 0
    fi
  done
  return 1
}

REPO_ROOT="$(find_repo_root "${SKILL_DIR}" || true)"
if [[ -z "${REPO_ROOT}" ]]; then
  if INSTALLED_BIN="$(find_installed_bin)"; then
    exec "${INSTALLED_BIN}" "$@"
  fi
  echo '{"success":false,"error":"Unable to locate gdocumentor repository root (go.mod / main.go) or gdocumentor binary in PATH (~/.local/bin or ~/go/bin)"}' >&2
  exit 1
fi

BIN_PATH="${REPO_ROOT}/gdocumentor"
if [[ ! -x "${BIN_PATH}" && ! -w "${REPO_ROOT}" ]]; then
  CACHE_DIR="${PLUGIN_DATA:-/tmp/gdocumentor_bin}"
  mkdir -p "${CACHE_DIR}" 2>/dev/null || true
  BIN_PATH="${CACHE_DIR}/gdocumentor"
fi

# Auto-build the binary if missing or if any .go source file is newer than the binary
needs_build=0
if [[ ! -x "${BIN_PATH}" ]]; then
  needs_build=1
else
  while IFS= read -r -d '' gofile; do
    if [[ "${gofile}" -nt "${BIN_PATH}" ]]; then
      needs_build=1
      break
    fi
  done < <(find "${REPO_ROOT}" -name '*.go' -not -path '*/.*' -print0)
fi

if [[ "${needs_build}" -eq 1 ]]; then
  if command -v go >/dev/null 2>&1; then
    if [[ ! -w "$(dirname "${BIN_PATH}")" ]]; then
      CACHE_DIR="${PLUGIN_DATA:-/tmp/gdocumentor_bin}"
      mkdir -p "${CACHE_DIR}" 2>/dev/null || true
      BIN_PATH="${CACHE_DIR}/gdocumentor"
    fi
    (cd "${REPO_ROOT}" && go build -o "${BIN_PATH}" .) >&2
  elif INSTALLED_BIN="$(find_installed_bin)"; then
    BIN_PATH="${INSTALLED_BIN}"
  else
    # Fallback to prebuilt binary in bin/ directory if present
    OS_NAME="$(uname -s | tr '[:upper:]' '[:lower:]')"
    ARCH_NAME="$(uname -m)"
    case "${ARCH_NAME}" in
      x86_64|amd64) ARCH_NAME="amd64" ;;
      aarch64|arm64) ARCH_NAME="arm64" ;;
    esac
    PREBUILT="${REPO_ROOT}/bin/gdocumentor_${OS_NAME}_${ARCH_NAME}"
    if [[ -x "${PREBUILT}" ]]; then
      BIN_PATH="${PREBUILT}"
    else
      echo '{"success":false,"error":"Go toolchain (go) is required to build gdocumentor, or install gdocumentor onto PATH"}' >&2
      exit 1
    fi
  fi
fi

exec "${BIN_PATH}" "$@"
