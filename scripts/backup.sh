#!/usr/bin/env bash
#
# backup.sh — rmapp の鍵・設定・データベースを一括バックアップする。
#
# 対象（docs/Manual.md §4.1）:
#   - secrets/kek.txt, secrets/session_key.txt
#   - server/config/config.yaml
#   - SQLite データベース（既定: server/data/rmapp.db）
#
# Redmine 本体（DB・添付ファイル）のバックアップは対象外です。RedmineDocker
# の scripts/backup.sh が別途担当します（docs/Manual.md §4.1、混同禁止）。
#
# 保持世代数: 7（それを超える古いアーカイブは backups/ から自動削除する）
#
# 使い方:
#   scripts/backup.sh
#   （どのディレクトリから実行してもよい）
#
# 環境変数（既定は開発環境のレイアウトに合わせてある。本番はパスを渡す）:
#   RMAPP_SECRETS_DIR  既定: <リポジトリ>/secrets
#   RMAPP_CONFIG_DIR   既定: <リポジトリ>/server/config
#   RMAPP_DB_PATH      既定: <リポジトリ>/server/data/rmapp.db
#   RMAPP_BACKUP_DIR   既定: <リポジトリ>/backups
#
# 前提:
#   - tar コマンドが利用できること
#
set -euo pipefail
umask 077

log() { printf '%s %s\n' "$(date '+%Y-%m-%dT%H:%M:%S%z')" "$*"; }
die() { log "エラー: $*" >&2; exit 1; }

command -v tar >/dev/null 2>&1 || die "tar が見つかりません"

repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
secrets_dir="${RMAPP_SECRETS_DIR:-${repo_root}/secrets}"
config_dir="${RMAPP_CONFIG_DIR:-${repo_root}/server/config}"
db_path="${RMAPP_DB_PATH:-${repo_root}/server/data/rmapp.db}"
backup_dir="${RMAPP_BACKUP_DIR:-${repo_root}/backups}"
keep_generations=7

[[ -d "${secrets_dir}" ]] || die "secrets ディレクトリが見つかりません: ${secrets_dir}（scripts/generate-secrets.sh は実行済みですか）"
[[ -d "${config_dir}" ]] || die "config ディレクトリが見つかりません: ${config_dir}"

mkdir -p "${backup_dir}"
chmod 700 "${backup_dir}"

work="$(mktemp -d)"
trap 'rm -rf "${work}"' EXIT

mkdir -p "${work}/secrets" "${work}/config"
cp -a "${secrets_dir}/." "${work}/secrets/"
cp -a "${config_dir}/." "${work}/config/"
if [[ -f "${db_path}" ]]; then
  mkdir -p "${work}/data"
  cp -a "${db_path}" "${work}/data/$(basename -- "${db_path}")"
else
  log "警告: データベースが見つかりません（未初期化の可能性）: ${db_path}"
fi

timestamp="$(date '+%Y%m%d-%H%M%S')"
archive="${backup_dir}/rmapp-backup-${timestamp}.tar.gz"
tar czf "${archive}" -C "${work}" .
chmod 600 "${archive}"
log "バックアップを作成しました: ${archive}"

# 保持世代数を超えた古いアーカイブを削除する（ファイル名の日時で昇順ソート）。
mapfile -t archives < <(find "${backup_dir}" -maxdepth 1 -name 'rmapp-backup-*.tar.gz' | sort)
excess=$(( ${#archives[@]} - keep_generations ))
if (( excess > 0 )); then
  for ((i = 0; i < excess; i++)); do
    log "世代保持数（${keep_generations}）を超えたため削除します: ${archives[$i]}"
    rm -f "${archives[$i]}"
  done
fi

log "完了しました。保持世代数: ${keep_generations}"
