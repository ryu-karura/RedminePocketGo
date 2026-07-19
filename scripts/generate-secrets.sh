#!/usr/bin/env bash
#
# generate-secrets.sh — rmapp が使用するシークレットファイルを生成する
#
# 目的:
#   - secrets/session_key.txt : セッション署名鍵（32 バイト乱数の hex）
#   - secrets/kek.txt         : API キー暗号化鍵 KEK（32 バイト乱数の hex）
#   生成済みのファイルは上書きしない（冪等）。ファイルは mode 600、
#   secrets/ ディレクトリは mode 700 とし、git 管理外に置く。
#
# 使い方:
#   scripts/generate-secrets.sh
#   （どのディレクトリから実行してもよい）
#
# 前提:
#   - openssl コマンドが利用できること
#
set -euo pipefail
umask 077

log() { printf '%s %s\n' "$(date '+%Y-%m-%dT%H:%M:%S%z')" "$*"; }
die() { log "エラー: $*" >&2; exit 1; }

command -v openssl >/dev/null 2>&1 || die "openssl が見つかりません"

# スクリプトの位置からリポジトリルートを求める（カレントディレクトリ非依存）
repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
secrets_dir="${repo_root}/secrets"

mkdir -p "${secrets_dir}"
chmod 700 "${secrets_dir}"

generate() {
  local file="$1"
  local bytes="$2"
  if [[ -f "${file}" ]]; then
    log "既に存在するためスキップします: ${file}"
    return 0
  fi
  openssl rand -hex "${bytes}" > "${file}"
  chmod 600 "${file}"
  log "生成しました: ${file}"
}

generate "${secrets_dir}/session_key.txt" 32
generate "${secrets_dir}/kek.txt" 32

log "完了しました。secrets/ 配下は git 管理外です。コミットしないでください。"
