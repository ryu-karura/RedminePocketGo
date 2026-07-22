#!/usr/bin/env bash
#
# test-stack.sh — 起動中の RedmineDocker 開発スタックに対して rmapp を実際に
# 起動し、疎通確認を行う統合テスト（CLAUDE.md §5、docs/plan.md フェーズ 8）。
#
# 確認する項目:
#   1. 起動確認     — サーバーをビルドして起動し、SPA（ログイン画面のシェル）
#                     が返ること
#   2. ヘルスチェック — /healthz（自身が応答するか）・/readyz（Redmine へ
#                     到達できるか）がともに 200 を返すこと
#   3. 許可リスト経由の往復 1 件
#                   — 実 Redmine へ GET /issues.json を許可リスト経由で
#                     中継できること（server/stacktest、build tag stack）
#
# 使い方:
#   RMAPP_STACK_API_KEY=xxxx scripts/test-stack.sh
#   （どのディレクトリから実行してもよい）
#
# 環境変数:
#   RMAPP_STACK_API_KEY  必須。中継確認に使う Redmine の API キー
#                        （取得方法は docs/Setup.md §3.3）
#   RMAPP_STACK_CONFIG   任意。設定ファイルのパス
#                        （既定: server/config/config.yaml）
#
# 前提:
#   - RedmineDocker 開発スタックが起動済みで REST API が有効なこと
#     （docs/Setup.md §3）
#   - scripts/generate-secrets.sh 実行済み（secrets/ が存在すること）
#   - go, curl コマンドが利用できること
#
set -euo pipefail

log() { printf '%s %s\n' "$(date '+%Y-%m-%dT%H:%M:%S%z')" "$*"; }
die() { log "エラー: $*" >&2; exit 1; }

command -v go >/dev/null 2>&1 || die "go が見つかりません"
command -v curl >/dev/null 2>&1 || die "curl が見つかりません"
[[ -n "${RMAPP_STACK_API_KEY:-}" ]] || die "RMAPP_STACK_API_KEY が未設定です（docs/Setup.md §3.3 で取得した Redmine の API キーを設定してください）"

# スクリプトの位置からリポジトリルートを求める（カレントディレクトリ非依存）
repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
server_dir="${repo_root}/server"

config_input="${RMAPP_STACK_CONFIG:-${server_dir}/config/config.yaml}"
[[ -f "${config_input}" ]] || die "設定ファイルが見つかりません: ${config_input}"
# 絶対パス化しておく（rmapp 起動時と go test 実行時でカレントディレクトリが
# 異なるため、どちらから見ても解決できるようにする）。
config_path="$(cd -- "$(dirname -- "${config_input}")" && pwd)/$(basename -- "${config_input}")"

listen_addr="127.0.0.1:18090"
bin_path="${server_dir}/bin/rmapp-stacktest"

log "サーバーをビルドします"
mkdir -p "$(dirname -- "${bin_path}")"
(cd "${server_dir}" && go build -o "${bin_path}" ./cmd/rmapp)

pid=""
cleanup() {
  if [[ -n "${pid}" ]] && kill -0 "${pid}" 2>/dev/null; then
    kill "${pid}" 2>/dev/null || true
    wait "${pid}" 2>/dev/null || true
  fi
  rm -f "${bin_path}"
}
trap cleanup EXIT

log "サーバーを起動します（${listen_addr}）"
(cd "${server_dir}" && exec "${bin_path}" -config "${config_path}" -listen "${listen_addr}") &
pid=$!

log "起動を待ちます"
ready=0
for _ in $(seq 1 30); do
  if curl -fsS "http://${listen_addr}/healthz" >/dev/null 2>&1; then
    ready=1
    break
  fi
  kill -0 "${pid}" 2>/dev/null || die "サーバーが起動直後に終了しました（config: ${config_path}）"
  sleep 0.5
done
[[ "${ready}" -eq 1 ]] || die "サーバーが起動しませんでした（/healthz が応答しません）"

log "[1/3] 起動確認: SPA（ログイン画面のシェル）"
body="$(curl -fsS "http://${listen_addr}/")" || die "ルートへの応答取得に失敗しました"
[[ "${body}" == *'id="screens"'* ]] || die "SPA のシェルが返っていません（app/index.html の配信を確認してください）"

log "[2/3] ヘルスチェック: /healthz"
curl -fsS "http://${listen_addr}/healthz" >/dev/null || die "/healthz が失敗しました"

log "[2/3] ヘルスチェック: /readyz（Redmine 到達性）"
curl -fsS "http://${listen_addr}/readyz" >/dev/null || die "/readyz が失敗しました。RedmineDocker 開発スタックが起動しているか、redmine.baseURL/subURI を確認してください"

log "[3/3] 許可リスト経由の往復 1 件（GET /issues.json）"
(cd "${server_dir}" && RMAPP_STACK_API_KEY="${RMAPP_STACK_API_KEY}" RMAPP_STACK_CONFIG="${config_path}" \
  go test -tags stack ./stacktest/... -run TestProxyRoundTripAgainstRealRedmine -count=1 -v) \
  || die "許可リスト経由の Redmine 往復に失敗しました"

log "完了しました。すべての確認に成功しました。"
