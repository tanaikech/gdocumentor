#!/usr/bin/env bash
# ==============================================================================
# DocuMentor AI (gdocumentor) — Cross-Platform One-Liner Installer (Linux / macOS / WSL2)
# Repository: https://github.com/tanaikech/gdocumentor
#
# Usage Examples:
#   # Install gdocumentor + configure ALL AI agents (Antigravity, Claude Desktop, Claude Code, Cursor, VS Code/Copilot, Windsurf, Gemini CLI)
#   curl -fsSL https://raw.githubusercontent.com/tanaikech/gdocumentor/main/scripts/install.sh | bash
#
#   # Install in WSL2 + configure ALL Windows Host GUI apps (Windows Claude Desktop, Cursor, VS Code, Windsurf) to call WSL2 gdocumentor via wsl.exe
#   curl -fsSL https://raw.githubusercontent.com/tanaikech/gdocumentor/main/scripts/install.sh | bash -s -- --all --wsl-host
#
#   # Install for specific agent(s) or project scope
#   curl -fsSL https://raw.githubusercontent.com/tanaikech/gdocumentor/main/scripts/install.sh | bash -s -- --agent claude-desktop,cursor
# ==============================================================================
set -euo pipefail

REPO_URL="${GDOCUMENTOR_REPO_URL:-https://github.com/tanaikech/gdocumentor}"
RELEASE_BASE="${GDOCUMENTOR_RELEASE_BASE:-https://github.com/tanaikech/gdocumentor/releases/latest/download}"
INSTALL_DIR="${GDOCUMENTOR_INSTALL_DIR:-${HOME}/.local/bin}"

OS_NAME="$(uname -s | tr '[:upper:]' '[:lower:]')"
case "${OS_NAME}" in
  linux*)  OS_NAME="linux" ;;
  darwin*) OS_NAME="darwin" ;;
  *)
    echo "Unsupported OS: ${OS_NAME}. On Windows, use scripts/install.ps1 in PowerShell or run inside WSL2." >&2
    exit 1
    ;;
esac

ARCH_NAME="$(uname -m)"
case "${ARCH_NAME}" in
  x86_64|amd64) ARCH_NAME="amd64" ;;
  aarch64|arm64) ARCH_NAME="arm64" ;;
  *)
    echo "Unsupported architecture: ${ARCH_NAME} (supported: amd64, arm64)" >&2
    exit 1
    ;;
esac

mkdir -p "${INSTALL_DIR}"
TARGET_BIN="${INSTALL_DIR}/gdocumentor"
BIN_NAME="gdocumentor_${OS_NAME}_${ARCH_NAME}"
DOWNLOAD_URL="${RELEASE_BASE}/${BIN_NAME}"

echo "⬇️  Installing gdocumentor (${OS_NAME}/${ARCH_NAME}) to ${TARGET_BIN} ..."
if command -v curl >/dev/null 2>&1; then
  curl -fsSL -L "${DOWNLOAD_URL}" -o "${TARGET_BIN}"
elif command -v wget >/dev/null 2>&1; then
  wget -qO "${TARGET_BIN}" "${DOWNLOAD_URL}"
else
  echo "Error: curl or wget is required to download gdocumentor." >&2
  exit 1
fi

chmod +x "${TARGET_BIN}"
echo "✅ Installed binary: ${TARGET_BIN} ($("${TARGET_BIN}" --version 2>/dev/null || echo "v1.1.0"))"

# Run multi-agent setup (passes any extra arguments such as --all, --agent, --scope, --wsl-host, --win-user)
if [[ $# -eq 0 ]]; then
  "${TARGET_BIN}" setup --all --binary-path "${TARGET_BIN}"
else
  HAS_TARGET_FLAG=0
  for arg in "$@"; do
    case "${arg}" in
      -a|--all|--install-all|--agent|--agent=*|--agents|--agents=*|--client|--client=*|--clients|--clients=*|--status|--uninstall|--remove-agent|-h|--help|help)
        HAS_TARGET_FLAG=1
        ;;
    esac
  done
  if [[ "${HAS_TARGET_FLAG}" -eq 1 ]]; then
    "${TARGET_BIN}" setup --binary-path "${TARGET_BIN}" "$@"
  else
    "${TARGET_BIN}" setup --all --binary-path "${TARGET_BIN}" "$@"
  fi
fi

if ! command -v gdocumentor >/dev/null 2>&1; then
  echo ""
  echo "💡 Tip: Add ${INSTALL_DIR} to your PATH if not already present:"
  echo "   export PATH=\"${INSTALL_DIR}:\$PATH\""
fi
