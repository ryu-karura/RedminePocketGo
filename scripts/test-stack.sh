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
#   3. OAuth 認可リダイレクト
#                   — GET /api/auth/login が Redmine の認可エンドポイントへ
#                     302 し、state と PKCE（S256 の code_challenge）が付き、
#                     state が発行元ブラウザの Cookie に束縛されること
#                     （Redmine のログイン画面を人が操作する部分は対象外）
#   4. 許可リスト経由の往復 1 件
#                   — 実 Redmine へ GET /issues.json を、OAuth アクセス
#                     トークン（Bearer）で許可リスト経由に中継できること
#                     （server/stacktest、build tag stack）
#
# Redmine の API キーは一切使わない（CLAUDE.md §9-1）。
#
# 使い方:
#   RMAPP_STACK_ACCESS_TOKEN=xxxx scripts/test-stack.sh
#   （どのディレクトリから実行してもよい）
#
# 環境変数:
#   RMAPP_STACK_ACCESS_TOKEN  必須。中継確認に使う Redmine の OAuth アクセス
#                        トークン。scripts/redmine-seed-testdata.sh が Redmine
#                        側（rails runner）で発行して標準出力の最終行
#                        `ACCESS_TOKEN=...` に書く
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
[[ -n "${RMAPP_STACK_ACCESS_TOKEN:-}" ]] || die "RMAPP_STACK_ACCESS_TOKEN が未設定です（scripts/redmine-seed-testdata.sh の出力 ACCESS_TOKEN=... を設定してください）"

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

# 認可リダイレクトの確認では Redmine へクライアントシークレットを送らない（Redmine は
# 呼ばない）ため、起動時の検査（空ファイルは中止）を通すダミーを一時ファイルで与える。
work_dir="$(mktemp -d)"
printf '%s' "stacktest-dummy-client-secret" > "${work_dir}/client_secret.txt"
export RMAPP_REDMINE_OAUTH_CLIENTSECRETFILE="${work_dir}/client_secret.txt"

pid=""
cleanup() {
  if [[ -n "${pid}" ]] && kill -0 "${pid}" 2>/dev/null; then
    kill "${pid}" 2>/dev/null || true
    wait "${pid}" 2>/dev/null || true
  fi
  rm -f "${bin_path}"
  rm -rf "${work_dir}"
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

log "[1/4] 起動確認: SPA（ログイン画面のシェル）"
body="$(curl -fsS "http://${listen_addr}/")" || die "ルートへの応答取得に失敗しました"
[[ "${body}" == *'id="screens"'* ]] || die "SPA のシェルが返っていません（app/index.html の配信を確認してください）"

log "[2/4] ヘルスチェック: /healthz"
curl -fsS "http://${listen_addr}/healthz" >/dev/null || die "/healthz が失敗しました"

log "[2/4] ヘルスチェック: /readyz（Redmine 到達性）"
curl -fsS "http://${listen_addr}/readyz" >/dev/null || die "/readyz が失敗しました。RedmineDocker 開発スタックが起動しているか、redmine.baseURL/subURI を確認してください"

log "[3/4] OAuth 認可リダイレクト（GET /api/auth/login）"
login_headers="$(curl -sS -o /dev/null -D - "http://${listen_addr}/api/auth/login?return=%23issues%2F1")" \
  || die "/api/auth/login への要求に失敗しました"
status_line="$(printf '%s' "${login_headers}" | head -1 | tr -d '\r')"
[[ "${status_line}" == *" 302"* ]] || die "/api/auth/login が 302 を返しません（${status_line}）"
location="$(printf '%s' "${login_headers}" | grep -i '^location:' | head -1 | tr -d '\r' | cut -d' ' -f2-)"
[[ -n "${location}" ]] || die "Location ヘッダーがありません"
[[ "${location}" == *"/oauth/authorize?"* ]] || die "Redmine の認可エンドポイントへ向いていません: ${location}"
for want in 'response_type=code' 'client_id=' 'redirect_uri=' 'scope=' 'code_challenge_method=S256'; do
  [[ "${location}" == *"${want}"* ]] || die "認可 URL に ${want} がありません: ${location}"
done
state_value="$(printf '%s' "${location}" | grep -o '[?&]state=[^&]*' | head -1 | cut -d= -f2-)"
challenge_value="$(printf '%s' "${location}" | grep -o '[?&]code_challenge=[^&]*' | head -1 | cut -d= -f2-)"
[[ "${#state_value}" -ge 32 ]] || die "state が短すぎます（推測されにくい乱数が必要）"
[[ "${#challenge_value}" -ge 43 ]] || die "code_challenge が不正です（S256 は 43 文字）"
[[ "${location}" != *"client_secret"* ]] || die "認可 URL にクライアントシークレットが含まれています"
cookie_line="$(printf '%s' "${login_headers}" | grep -i '^set-cookie: rmapp_oauth_state=' | head -1 | tr -d '\r')"
[[ -n "${cookie_line}" ]] || die "state を発行元ブラウザへ束縛する Cookie（rmapp_oauth_state）がありません"
[[ "${cookie_line}" == *"${state_value}"* ]] || die "state Cookie の値が認可 URL の state と一致しません"
[[ "${cookie_line,,}" == *"httponly"* ]] || die "state Cookie に HttpOnly がありません"
[[ "${cookie_line,,}" == *"samesite=lax"* ]] || die "state Cookie に SameSite=Lax がありません"

log "[4/4] 許可リスト経由の往復 1 件（GET /issues.json、OAuth アクセストークン）"
(cd "${server_dir}" && RMAPP_STACK_ACCESS_TOKEN="${RMAPP_STACK_ACCESS_TOKEN}" RMAPP_STACK_CONFIG="${config_path}" \
  go test -tags stack ./stacktest/... -run TestProxyRoundTripAgainstRealRedmine -count=1 -v) \
  || die "許可リスト経由の Redmine 往復に失敗しました"

log "完了しました。すべての確認に成功しました。"
