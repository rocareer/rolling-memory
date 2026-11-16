#!/bin/sh
# Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
# Author: Albert <albert@rocareer.com>

# Rolling-Memory 插件公共层（纯 POSIX sh · 跨操作系统兼容 macOS / Linux / Alpine / NixOS / FreeBSD / Windows MSYS2）
# 正本：github.com/rocareer/rolling-memory bin/rolling-memory-lib.sh

rm_resolve_bin() {
  # 1) 显式环境变量覆盖（探针 / 高级布点用；设置即权威，不可执行则直报错退出 127）
  if [ -n "${ROLLING_MEMORY_BIN:-}" ]; then
    if [ -x "${ROLLING_MEMORY_BIN}" ]; then
      printf '%s\n' "${ROLLING_MEMORY_BIN}"
      return 0
    fi
    printf '[rm-mcp] 未找到可用的 Rolling-Memory 二进制：ROLLING_MEMORY_BIN 指向的路径不可执行（%s）\n' "${ROLLING_MEMORY_BIN}" >&2
    return 127
  fi

  # 2) 标准单机核心启动器与常见安装位置（含 Windows LOCALAPPDATA 与 POSIX XDG）
  for cand in \
    "${HOME}/.local/share/rolling-memory/bin/rolling-memory.sh" \
    "${HOME}/.local/share/rolling-memory/bin/rolling-memory" \
    "${HOME}/.local/share/rolling-memory/bin/rolling-memory.exe" \
    "${LOCALAPPDATA:-}/rolling-memory/bin/rolling-memory.exe" \
    "${HOME}/.local/bin/rolling-memory" \
    "$(command -v rolling-memory 2>/dev/null || true)"; do
    if [ -n "$cand" ] && [ -x "$cand" ]; then
      printf '%s\n' "$cand"
      return 0
    fi
  done

  # 识别当前 OS 与 CPU 架构
  os_raw="$(uname -s 2>/dev/null | tr '[:upper:]' '[:lower:]')"
  arch_raw="$(uname -m 2>/dev/null | tr '[:upper:]' '[:lower:]')"
  ext=""
  case "$os_raw" in
    darwin*) os_name="darwin" ;;
    linux*) os_name="linux" ;;
    freebsd*) os_name="freebsd" ;;
    mingw*|msys*|cygwin*|windows*) os_name="windows"; ext=".exe" ;;
    *) os_name="$os_raw" ;;
  esac
  case "$arch_raw" in
    x86_64|amd64) arch_name="amd64" ;;
    arm64|aarch64) arch_name="arm64" ;;
    riscv64) arch_name="riscv64" ;;
    *) arch_name="$arch_raw" ;;
  esac

  target_dir="${HOME}/.local/share/rolling-memory/bin"
  target_bin="${target_dir}/rolling-memory${ext}"

  # 3) 自动自举 A（Prebuilt Release Download）：无 Go 工具链或无源码仓时，按 OS/ARCH 自动拉取预编译单二进制。
  #    完整性是硬前提：发布目录必须随附 SHA256SUMS（唯一正典校验来源，与 install.sh 同口径），
  #    缺失 / 无该条目 / 哈希不符 / 无哈希工具 一律拒装（宁不装也不用未校验的二进制）。
  #    基址默认＝产品发布渠道；显式设 ROLLING_MEMORY_RELEASE_BASE 可换镜像（设空 / off / none / 0 可禁用自举）。
  release_base="${ROLLING_MEMORY_RELEASE_BASE-https://github.com/rocareer/rolling-memory/releases/latest/download}"
  # 与 install.sh 同口径：无第三方默认镜像，GitHub 为唯一公开源；需要镜像用
  # ROLLING_MEMORY_RELEASE_MIRROR 显式注入。
  release_mirror="${ROLLING_MEMORY_RELEASE_MIRROR-}"
  release_explicit=0
  [ -n "${ROLLING_MEMORY_RELEASE_BASE:-}" ] && release_explicit=1
  case "${release_base}" in off|none|0) release_base="" ;; esac
  case "${release_mirror}" in off|none|0) release_mirror="" ;; esac
  if [ -n "${release_base}" ] && { command -v curl >/dev/null 2>&1 || command -v wget >/dev/null 2>&1; }; then
    mkdir -p "$target_dir" "${HOME}/.local/bin" 2>/dev/null || true
    asset="rolling-memory-${os_name}-${arch_name}${ext}"
    dl_tmp="${target_bin}.dl.$$"
    sums_tmp="${target_bin}.sums.$$"
    hash_cmd=""
    if command -v sha256sum >/dev/null 2>&1; then
      hash_cmd="sha256sum"
    elif command -v shasum >/dev/null 2>&1; then
      hash_cmd="shasum -a 256"
    fi
    dl_fn() {
      if command -v curl >/dev/null 2>&1; then
        curl -fsSL --connect-timeout 10 --max-time 120 "$1" -o "$2" 2>/dev/null
      else
        wget -q --timeout=30 -O "$2" "$1" 2>/dev/null
      fi
    }
    if [ -z "$hash_cmd" ]; then
      printf '[rm-mcp] 未找到 sha256sum / shasum，跳过预编译自举（拒绝未校验二进制）\n' >&2
    else
      for ch in "$release_base" $([ "$release_explicit" -eq 0 ] && [ -n "$release_mirror" ] && [ "$release_mirror" != "$release_base" ] && printf '%s' "$release_mirror"); do
        printf '[rm-mcp] 首次使用：从发布渠道获取单二进制（%s，SHA256 校验）…\n' "$ch" >&2
        if ! dl_fn "${ch%/}/SHA256SUMS" "$sums_tmp"; then
          rm -f "$sums_tmp" 2>/dev/null || true
          printf '[rm-mcp] 发布目录未提供 SHA256SUMS（%s），跳过该渠道\n' "${ch}" >&2
          continue
        fi
        if dl_fn "${ch%/}/${asset}" "$dl_tmp"; then
          want_sha="$(awk -v a="$asset" '$2 == a || $2 == "*" a { print $1; exit }' "$sums_tmp" 2>/dev/null)"
          got_sha="$($hash_cmd "$dl_tmp" 2>/dev/null | awk '{print $1}')"
          rm -f "$sums_tmp" 2>/dev/null || true
          if [ -z "$want_sha" ] || [ "$want_sha" != "$got_sha" ]; then
            rm -f "$dl_tmp"
            printf '[rm-mcp] SHA256 校验失败（%s）：期望 %s，实得 %s；拒绝安装未校验的二进制\n' \
              "$asset" "${want_sha:-<SHA256SUMS 无该条目>}" "${got_sha:-<无法计算>}" >&2
            return 127
          fi
          chmod 0755 "$dl_tmp" 2>/dev/null || true
          mv -f "$dl_tmp" "$target_bin"
          ln -sf "$target_bin" "${HOME}/.local/bin/rolling-memory${ext}" 2>/dev/null || true
          "$target_bin" install --with-hooks >/dev/null 2>&1 || true
          if [ -x "${target_dir}/rolling-memory.sh" ]; then
            printf '%s\n' "${target_dir}/rolling-memory.sh"
            return 0
          fi
          printf '%s\n' "$target_bin"
          return 0
        fi
        rm -f "$dl_tmp" "$sums_tmp" 2>/dev/null || true
      done
    fi
    rm -f "$dl_tmp" "$sums_tmp" 2>/dev/null || true
  fi

  # 4) 自动自举 B（Local Source Bootstrap）：若插件目录自身或本机有源码仓且有 go 工具链，自动构建单机核心
  plugin_root=""
  if [ -n "${SCRIPT_DIR:-}" ]; then
    plugin_root="$(cd "${SCRIPT_DIR}/.." 2>/dev/null && pwd || true)"
  fi
  src_dir="${ROLLING_MEMORY_SRC:-}"
  if [ -z "$src_dir" ]; then
    if [ -n "$plugin_root" ] && [ -d "${plugin_root}/cmd/rolling-memory" ]; then
      src_dir="$plugin_root"
    else
      src_dir="${HOME}/Rolling-Memory"
    fi
  fi
  go_bin=""
  for g in "$(command -v go 2>/dev/null || true)" /opt/homebrew/bin/go /usr/local/go/bin/go /usr/local/bin/go; do
    if [ -n "$g" ] && [ -x "$g" ]; then
      go_bin="$g"
      break
    fi
  done
  if [ -d "${src_dir}/cmd/rolling-memory" ] && [ -n "$go_bin" ]; then
    mkdir -p "$target_dir" "${HOME}/.local/bin" 2>/dev/null || true
    if (cd "$src_dir" && CGO_ENABLED=0 "$go_bin" build -o "${target_bin}.tmp.$$" ./cmd/rolling-memory 2>/dev/null); then
      chmod 0755 "${target_bin}.tmp.$$" 2>/dev/null || true
      mv -f "${target_bin}.tmp.$$" "$target_bin"
      ln -sf "$target_bin" "${HOME}/.local/bin/rolling-memory${ext}" 2>/dev/null || true
      "$target_bin" install --with-hooks >/dev/null 2>&1 || true
      if [ -x "${target_dir}/rolling-memory.sh" ]; then
        printf '%s\n' "${target_dir}/rolling-memory.sh"
        return 0
      fi
      printf '%s\n' "$target_bin"
      return 0
    fi
    rm -f "${target_bin}.tmp.$$" 2>/dev/null || true
  fi

  printf '[rm-mcp] 未找到 Rolling-Memory 二进制。先完成一次性安装：\n' >&2
  printf '  1) 取二进制：产品发布渠道下载，或自行构建 CGO_ENABLED=0 go build ./cmd/rolling-memory\n' >&2
  printf '  2) 放到 ~/.local/bin/rolling-memory 后执行：rolling-memory install --with-hooks\n' >&2
  return 127
}

rm_dedupe_zcode() {
  bin="$(rm_resolve_bin 2>/dev/null)" || return 0
  "$bin" install zcode --with-hooks >/dev/null 2>&1 || true
}
