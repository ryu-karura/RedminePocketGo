#!/usr/bin/env bash
#
# restore.sh — backup.sh が作成したアーカイブから、鍵・設定・データベースを
#              復元する（破壊的操作。既存の内容を上書きする）。
#
# 使い方:
#   scripts/restore.sh <アーカイブのパス>
#   （どのディレクトリから実行してもよい。実行すると確認語 RESTORE の
#    入力を求める）
#
# 環境変数（既定は開発環境のレイアウトに合わせてある。backup.sh と同じ）:
#   RMAPP_SECRETS_DIR  既定: <リポジトリ>/secrets
#   RMAPP_CONFIG_DIR   既定: <リポジトリ>/server/config
#   RMAPP_DB_PATH      既定: <リポジトリ>/server/data/rmapp.db
#
# 前提:
#   - 復元前に rmapp プロセスを止めておくこと（データベース破損を防ぐため）
#   - 鍵とデータベースは必ず同じ時点のバックアップを組で戻すこと
#     （食い違うと API キーが復号できなくなる。docs/Manual.md §4.3）
#   - tar コマンドが利用できること
#
set -euo pipefail
umask 077

log() { printf '%s %s\n' "$(date '+%Y-%m-%dT%H:%M:%S%z')" "$*"; }
die() { log "エラー: $*" >&2; exit 1; }

archive="${1:-}"
[[ -n "${archive}" ]] || die "使い方: scripts/restore.sh <アーカイブのパス>"
[[ -f "${archive}" ]] || die "アーカイブが見つかりません: ${archive}"
command -v tar >/dev/null 2>&1 || die "tar が見つかりません"

repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
secrets_dir="${RMAPP_SECRETS_DIR:-${repo_root}/secrets}"
config_dir="${RMAPP_CONFIG_DIR:-${repo_root}/server/config}"
db_path="${RMAPP_DB_PATH:-${repo_root}/server/data/rmapp.db}"

log "復元元のアーカイブ: ${archive}"
log "復元先: secrets=${secrets_dir} config=${config_dir} db=${db_path}"
log "既存の内容は上書きされます。rmapp プロセスは事前に止めてください。"
read -rp "続行するには RESTORE と入力してください: " confirm
[[ "${confirm}" == "RESTORE" ]] || die "確認語が一致しないため中止しました"

work="$(mktemp -d)"
trap 'rm -rf "${work}"' EXIT
tar xzf "${archive}" -C "${work}"

[[ -d "${work}/secrets" ]] || die "アーカイブに secrets/ が含まれていません（backup.sh で作成したものですか）"
[[ -d "${work}/config" ]] || die "アーカイブに config/ が含まれていません"

mkdir -p "${secrets_dir}" "${config_dir}"
cp -a "${work}/secrets/." "${secrets_dir}/"
cp -a "${work}/config/." "${config_dir}/"
chmod 700 "${secrets_dir}"
find "${secrets_dir}" -maxdepth 1 -type f -exec chmod 600 {} +

db_basename="$(basename -- "${db_path}")"
if [[ -f "${work}/data/${db_basename}" ]]; then
  mkdir -p "$(dirname -- "${db_path}")"
  cp -a "${work}/data/${db_basename}" "${db_path}"
  log "データベースを復元しました: ${db_path}"
else
  log "警告: アーカイブにデータベースが含まれていません（未初期化の状態でバックアップされた可能性）"
fi

log "復元が完了しました。rmapp を再起動してください。"
