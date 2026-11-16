#!/usr/bin/env bash
# Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
# Author: Albert <albert@rocareer.com>

# install.sh — One-line installer for Rolling-Memory (macOS / Linux).
#
# Usage:
#   bash install.sh [--with-hooks] [--with-instructions]
#   curl -fsSL https://github.com/rocareer/rolling-memory/raw/main/install.sh | bash
set -euo pipefail

DEFAULT_REPO_URL="https://github.com/rocareer/rolling-memory.git"
# Prebuilt release assets live under this base (flat names, SHA256SUMS beside
# them). It is the primary path for end users (no Git or Go toolchain required);
# set ROLLING_MEMORY_RELEASE_BASE to use a custom mirror, or to "" / off to disable it.
DEFAULT_RELEASE_BASE="https://github.com/rocareer/rolling-memory/releases/latest/download"
# No default mirror: GitHub is the single public source for this project
# (see the project policy on external terms). Inject a mirror explicitly with
# ROLLING_MEMORY_RELEASE_MIRROR=<base>/releases/latest/download if you run one.
DEFAULT_RELEASE_MIRROR=""
REPO_URL="${ROLLING_MEMORY_REPO_URL:-${DEFAULT_REPO_URL}}"
# Same rule for the source fallback: no third-party default mirror.
REPO_MIRROR_URL="${ROLLING_MEMORY_REPO_MIRROR:-}"
release_base="${ROLLING_MEMORY_RELEASE_BASE-${DEFAULT_RELEASE_BASE}}"
release_mirror="${ROLLING_MEMORY_RELEASE_MIRROR-${DEFAULT_RELEASE_MIRROR}}"
release_base_explicit=0
if [ -n "${ROLLING_MEMORY_RELEASE_BASE:-}" ]; then release_base_explicit=1; fi
case "${release_base}" in off|none|0) release_base="" ;; esac
case "${release_mirror}" in off|none|0) release_mirror="" ;; esac
INSTALL_DIR="${ROLLING_MEMORY_INSTALL_DIR:-${HOME}/.local/share/rolling-memory/bin}"
LINK_DIR="${ROLLING_MEMORY_LINK_DIR:-${HOME}/.local/bin}"

rm_download() {
  local url="$1" dest="$2"
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL --connect-timeout 10 --max-time 120 "$url" -o "$dest"
  elif command -v wget >/dev/null 2>&1; then
    wget -q --timeout=30 -O "$dest" "$url"
  else
    return 127
  fi
}

rm_sha256() {
  local file="$1"
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$file" | awk '{print $1}'
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$file" | awk '{print $1}'
  elif command -v openssl >/dev/null 2>&1; then
    openssl dgst -sha256 "$file" | awk '{print $NF}'
  else
    return 127
  fi
}

os_name="$(uname -s | tr '[:upper:]' '[:lower:]')"
arch_name="$(uname -m | tr '[:upper:]' '[:lower:]')"
case "${arch_name}" in
  x86_64|amd64) arch_name="amd64" ;;
  arm64|aarch64) arch_name="arm64" ;;
  *) echo "install.sh: unsupported architecture: ${arch_name}" >&2; exit 2 ;;
esac

mkdir -p "${INSTALL_DIR}" "${LINK_DIR}"
TARGET_BIN="${INSTALL_DIR}/rolling-memory"

built=0
prebuilt_err=""
SCRIPT_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")" 2>/dev/null && pwd || true)"
if [ -n "${SCRIPT_ROOT}" ] && [ -f "${SCRIPT_ROOT}/cmd/rolling-memory/main.go" ] && command -v go >/dev/null 2>&1; then
  echo "[rolling-memory] Building from local checkout (${SCRIPT_ROOT})..."
  tmp="${TARGET_BIN}.tmp.$$"
  (cd "${SCRIPT_ROOT}" && CGO_ENABLED=0 go build -trimpath -o "${tmp}" ./cmd/rolling-memory)
  chmod 0755 "${tmp}"
  mv -f "${tmp}" "${TARGET_BIN}"
  built=1
fi

# try_download_prebuilt fetches SHA256SUMS and the platform binary from a given
# release base URL. Returns 0 on verified install, 2 on SHA256 mismatch (fatal),
# or 1 when the channel is unreachable.
try_download_prebuilt() {
  local base_url="$1" asset="$2" tmp="${TARGET_BIN}.tmp.$$"
  if ! rm_download "${base_url%/}/SHA256SUMS" "${tmp}.sums" 2>/dev/null; then
    rm -f "${tmp}.sums"
    prebuilt_err="cannot reach ${base_url%/}/SHA256SUMS"
    return 1
  fi
  local want=""
  want="$(awk -v a="${asset}" '$2 == a || $2 == "*" a { print $1; exit }' "${tmp}.sums")"
  if [ -z "${want}" ]; then
    rm -f "${tmp}.sums"
    prebuilt_err="SHA256SUMS at ${base_url} has no entry for ${asset}"
    return 2
  fi
  if ! rm_download "${base_url%/}/${asset}" "${tmp}" 2>/dev/null; then
    rm -f "${tmp}" "${tmp}.sums"
    prebuilt_err="failed to download ${base_url%/}/${asset}"
    return 1
  fi
  local got=""
  got="$(rm_sha256 "${tmp}" || true)"
  rm -f "${tmp}.sums"
  if [ -z "${got}" ] || [ "${want}" != "${got}" ]; then
    rm -f "${tmp}"
    echo "install.sh: SHA256 mismatch for ${asset} (want ${want}, got ${got:-<no hash tool>})" >&2
    return 2
  fi
  chmod 0755 "${tmp}"
  mv -f "${tmp}" "${TARGET_BIN}"
  return 0
}

# Prebuilt assets are attached to each tagged release under exactly these names;
# the default base is the official release channel (falling back to the release
# mirror and then a source build when not reachable). An explicitly configured
# base stays strict: a missing SHA256SUMS or hash mismatch there is always fatal.
if [ "${built}" -eq 0 ] && [ -n "${release_base}" ]; then
  if ! command -v curl >/dev/null 2>&1 && ! command -v wget >/dev/null 2>&1; then
    prebuilt_err="neither curl nor wget is installed"
    if [ "${release_base_explicit}" -eq 1 ]; then
      echo "install.sh: ${prebuilt_err}; cannot download from ${release_base}" >&2
      exit 1
    fi
  else
    asset="rolling-memory-${os_name}-${arch_name}"
    echo "[rolling-memory] Downloading prebuilt binary (${asset})..."
    rc=0
    try_download_prebuilt "${release_base}" "${asset}" || rc=$?
    if [ "${rc}" -eq 0 ]; then
      built=1
    elif [ "${rc}" -eq 2 ]; then
      if [ "${release_base_explicit}" -eq 1 ]; then
        echo "install.sh: ${prebuilt_err}; refusing an unverifiable binary" >&2
        exit 1
      fi
    else
      if [ "${release_base_explicit}" -eq 1 ]; then
        echo "install.sh: cannot fetch SHA256SUMS from ${release_base}; refusing an unverifiable binary" >&2
        exit 1
      fi
      if [ -n "${release_mirror}" ] && [ "${release_mirror}" != "${release_base}" ]; then
        echo "[rolling-memory] Primary release channel unavailable; trying mirror (${release_mirror})..." >&2
        primary_err="${prebuilt_err}"
        prebuilt_err=""
        rc_mirror=0
        try_download_prebuilt "${release_mirror}" "${asset}" || rc_mirror=$?
        if [ "${rc_mirror}" -eq 0 ]; then
          built=1
        else
          # A mirror rejection is never swallowed: report it (and keep the
          # mirror's own reason for the message below).
          echo "[rolling-memory] Mirror did not provide a verified binary (${prebuilt_err:-rejected}); primary error: ${primary_err}" >&2
        fi
      fi
      if [ "${built}" -eq 0 ]; then
        echo "[rolling-memory] Prebuilt channel unavailable (${prebuilt_err:-${release_base}})." >&2
      fi
    fi
  fi
fi

if [ "${built}" -eq 0 ] && command -v go >/dev/null 2>&1 && command -v git >/dev/null 2>&1; then
  tmp_repo="$(mktemp -d "${TMPDIR:-/tmp}/rm-src.XXXXXX")"
  trap 'rm -rf "${tmp_repo}"' EXIT
  echo "[rolling-memory] Cloning ${REPO_URL} and building..."
  if ! git clone --depth 1 "${REPO_URL}" "${tmp_repo}"; then
    if [ -n "${REPO_MIRROR_URL}" ] && [ "${REPO_URL}" = "${DEFAULT_REPO_URL}" ]; then
      echo "[rolling-memory] Primary source unavailable; retrying mirror ${REPO_MIRROR_URL}..." >&2
      rm -rf "${tmp_repo}"
      mkdir -p "${tmp_repo}"
      git clone --depth 1 "${REPO_MIRROR_URL}" "${tmp_repo}"
    else
      echo "install.sh: failed to clone ${REPO_URL}" >&2
      exit 1
    fi
  fi
  tmp="${TARGET_BIN}.tmp.$$"
  (cd "${tmp_repo}" && CGO_ENABLED=0 go build -trimpath -o "${tmp}" ./cmd/rolling-memory)
  chmod 0755 "${tmp}"
  mv -f "${tmp}" "${TARGET_BIN}"
  if [ -f "${tmp_repo}/evaldata/MANIFEST.json" ] && [ ! -e "${HOME}/.local/share/rolling-memory/evaldata/MANIFEST.json" ]; then
    mkdir -p "${HOME}/.local/share/rolling-memory"
    cp -R "${tmp_repo}/evaldata" "${HOME}/.local/share/rolling-memory/evaldata" 2>/dev/null || true
  fi
  built=1
fi

if [ "${built}" -eq 0 ]; then
  echo "install.sh: 无法获取预编译二进制（原因：${prebuilt_err:-发布渠道暂不可达}），且本机未安装 Git / Go 编译环境。" >&2
  echo "  • 普通用户（免装 Git/Go）：请检查网络连接，或指定可达镜像源重试：" >&2
  echo "      ROLLING_MEMORY_RELEASE_BASE=https://your-mirror/releases/latest/download bash install.sh" >&2
  echo "  • 离线安装：从发布页下载 rolling-memory-${os_name}-${arch_name} 放入 ${TARGET_BIN} 并赋予可执行权限（chmod +x）。" >&2
  echo "  • 开发者：安装 Go 1.26+ 与 Git 后重跑本脚本即可自动从源码编译。" >&2
  exit 1
fi

if [ -n "${SCRIPT_ROOT}" ] && [ -f "${SCRIPT_ROOT}/evaldata/MANIFEST.json" ] && [ ! -e "${HOME}/.local/share/rolling-memory/evaldata/MANIFEST.json" ]; then
  mkdir -p "${HOME}/.local/share/rolling-memory"
  ln -sfn "${SCRIPT_ROOT}/evaldata" "${HOME}/.local/share/rolling-memory/evaldata" 2>/dev/null || true
fi

ln -sf "${TARGET_BIN}" "${LINK_DIR}/rolling-memory"
ln -sf "${TARGET_BIN}" "${LINK_DIR}/rmem"
echo "[rolling-memory] Installed binary to ${TARGET_BIN} (linked in ${LINK_DIR})"

ensure_path_in_profile() {
  [ "${ROLLING_MEMORY_NO_MODIFY_PATH:-0}" = "1" ] && return 0
  local profile="" shell_name
  shell_name="$(basename "${SHELL:-}")"
  case "${shell_name}" in
    zsh)  profile="${HOME}/.zshrc" ;;
    bash)
      if [ -f "${HOME}/.bashrc" ]; then
        profile="${HOME}/.bashrc"
      else
        profile="${HOME}/.bash_profile"
      fi
      ;;
    *)
      if [ -f "${HOME}/.zshrc" ]; then
        profile="${HOME}/.zshrc"
      elif [ -f "${HOME}/.bashrc" ]; then
        profile="${HOME}/.bashrc"
      else
        profile="${HOME}/.profile"
      fi
      ;;
  esac
  [ -z "${profile}" ] && return 0
  if [ -f "${profile}" ] && grep -Fq "${LINK_DIR}" "${profile}" 2>/dev/null; then
    return 0
  fi
  {
    printf '\n# >>> rolling-memory PATH >>>\n'
    printf 'export PATH="%s:$PATH"\n' "${LINK_DIR}"
    printf '# <<< rolling-memory PATH <<<\n'
  } >> "${profile}" 2>/dev/null || return 0
  echo "[rolling-memory] Added ${LINK_DIR} to ${profile} (open a new terminal or run: export PATH=\"${LINK_DIR}:\$PATH\")"
}

case ":${PATH}:" in
  *":${LINK_DIR}:"*) ;;
  *)
    export PATH="${LINK_DIR}:${PATH}"
    ensure_path_in_profile
    ;;
esac

"${TARGET_BIN}" init --write-config >/dev/null 2>&1 || true

install_err="$(mktemp "${TMPDIR:-/tmp}/rm-install-err.XXXXXX")"
if [ "$#" -eq 0 ]; then
  set +e
  "${TARGET_BIN}" install --with-hooks 2>"${install_err}"
  install_rc=$?
  set -e
else
  set +e
  "${TARGET_BIN}" install "$@" 2>"${install_err}"
  install_rc=$?
  set -e
fi
if [ "${install_rc}" -eq 0 ]; then
  cat "${install_err}" >&2
  rm -f "${install_err}"
  "${TARGET_BIN}" install --check
elif grep -q "no harness detected" "${install_err}" 2>/dev/null; then
  cat "${install_err}" >&2
  rm -f "${install_err}"
  echo "[rolling-memory] CLI binary and local vault are installed and ready (no host harness detected under ${HOME} yet)."
  echo "[rolling-memory] Wire a harness anytime with: rolling-memory install --with-hooks [<harness-id>]"
else
  cat "${install_err}" >&2
  rm -f "${install_err}"
  exit "${install_rc}"
fi
echo "[rolling-memory] Web console: run \`rolling-memory panel\` to start it (backend :9006 + web frontend :8008; add --desktop-shortcut to install for a desktop launcher)."
echo "[rolling-memory] Self-update: run \`rolling-memory update\` (or \`rolling-memory update --check\`) anytime to update the CLI, console and host integrations."
