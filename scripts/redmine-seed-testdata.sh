#!/usr/bin/env bash
#
# scripts/redmine-seed-testdata.sh — 起動中の RedmineDocker 開発スタックに、
# 統合テスト（scripts/test-stack.sh）に必要な最小限の前提を整える冪等スクリプト
# （CLAUDE.md §5、docs/plan.md フェーズ 8）。
#
# 開発サンドボックスには Docker デーモンがなく scripts/test-stack.sh を実行
# できないため、Docker デーモンを持つ CI（.github/workflows/stack-test.yml）
# からのみ呼ばれる想定だが、実 RedmineDocker スタックを手元で起動している
# 開発者が手動で使うこともできる。
#
# 行うこと（すべて RedmineDocker の「起動中のコンテナに対する操作」であり、
# RedmineDocker リポジトリ自体（イメージ定義・compose 定義等）は一切変更しない
# — CLAUDE.md §9-6「本リポジトリは RedmineDocker スタックを変更しない」）:
#   1. Redmine の REST API を有効化する（既定で無効。手動手順は docs/Setup.md §3.2）
#   2. 管理者（既定 admin）のパスワードを既知の値に設定し、初回パスワード
#      変更要求を解除する（rails runner 経由）
#   3. 管理者の API アクセスキーを確認・生成する
#   4. REST API 経由でテスト用プロジェクトとチケットを投入する
#      （再実行しても重複作成しない）
#
# 標準出力には最終行 `API_KEY=<値>` のみを書く。呼び出し元はこれを
# scripts/test-stack.sh の RMAPP_STACK_API_KEY にそのまま渡せる。
# それ以外のログはすべて標準エラーに書く。
#
# 使い方:
#   scripts/redmine-seed-testdata.sh
#   api_key="$(scripts/redmine-seed-testdata.sh | tail -1 | cut -d= -f2-)"
#
# 環境変数（すべて任意。既定値は RedmineDocker の compose.dev.yaml の既定値と
# 一致させている）:
#   REDMINE_WEB_CONTAINER            既定 redmine-web
#   REDMINE_ADMIN_LOGIN              既定 admin
#   REDMINE_ADMIN_PASSWORD           既定は自動生成（16進32文字）
#   REDMINE_BASE_URL                 既定 http://localhost:8080
#   REDMINE_SUBURI                   既定 /redmine
#   REDMINE_TEST_PROJECT_IDENTIFIER  既定 rmapp-ci-testdata
#
# 前提:
#   - redmine-web コンテナが healthy な状態で起動済みであること
#     （docker compose -f compose.dev.yaml up --build -d）
#   - docker, curl, openssl コマンドが利用できること

set -euo pipefail

log() { printf '%s %s\n' "$(date '+%Y-%m-%dT%H:%M:%S%z')" "$*" >&2; }
die() { log "エラー: $*"; exit 1; }

command -v docker >/dev/null 2>&1 || die "docker が見つかりません"
command -v curl >/dev/null 2>&1 || die "curl が見つかりません"
command -v openssl >/dev/null 2>&1 || die "openssl が見つかりません"

REDMINE_WEB_CONTAINER="${REDMINE_WEB_CONTAINER:-redmine-web}"
REDMINE_ADMIN_LOGIN="${REDMINE_ADMIN_LOGIN:-admin}"
REDMINE_BASE_URL="${REDMINE_BASE_URL:-http://localhost:8080}"
REDMINE_SUBURI="${REDMINE_SUBURI:-/redmine}"
REDMINE_TEST_PROJECT_IDENTIFIER="${REDMINE_TEST_PROJECT_IDENTIFIER:-rmapp-ci-testdata}"

if [[ -z "${REDMINE_ADMIN_PASSWORD:-}" ]]; then
  # 16進32文字。`head -c N` で打ち切るパイプは、書き込み側が SIGPIPE を
  # 受けて pipefail 下でパイプライン全体を失敗させることがあるため使わない
  # （出力長を打ち切らずに済む openssl rand を使う）。
  REDMINE_ADMIN_PASSWORD="$(openssl rand -hex 16)"
fi
# CI ログに平文で出た場合でも隠す（GitHub Actions のワークフローコマンド。
# ローカル実行時はそのまま無害な行として扱われる）。
printf '::add-mask::%s\n' "${REDMINE_ADMIN_PASSWORD}" >&2

docker exec "${REDMINE_WEB_CONTAINER}" true >/dev/null 2>&1 \
  || die "${REDMINE_WEB_CONTAINER} コンテナに到達できません（起動・healthy を確認してください）"

log "[1/4] REST API を有効化し、管理者のパスワード / API キーを確認します"
# entrypoint.sh は SECRET_KEY_BASE を自分の bash プロセス内でのみ解決・export
# しており（containers/redmine-web/entrypoint.sh の resolve_secret）、
# コンテナのプロセス環境そのものには残らない。そのため docker exec で
# 新規プロセスを起こすここでは、entrypoint.sh と同じ規約
# （REDMINE_SECRET_KEY_BASE_FILE、compose.dev.yaml が設定するので
# docker exec にもコンテナ環境として見えている）に従い、自前でシークレット
# ファイルを読んで SECRET_KEY_BASE を用意してから rails runner を呼ぶ。
runner_output="$(docker exec -i \
  -e "REDMINE_ADMIN_LOGIN=${REDMINE_ADMIN_LOGIN}" \
  -e "REDMINE_ADMIN_PASSWORD=${REDMINE_ADMIN_PASSWORD}" \
  -u redmine \
  "${REDMINE_WEB_CONTAINER}" \
  bash -c '
    if [ -n "${REDMINE_SECRET_KEY_BASE_FILE:-}" ]; then
      SECRET_KEY_BASE="$(cat "${REDMINE_SECRET_KEY_BASE_FILE}")"
      export SECRET_KEY_BASE
    fi
    exec bundle exec rails runner -
  ' <<'RUBY'
login    = ENV.fetch('REDMINE_ADMIN_LOGIN')
password = ENV.fetch('REDMINE_ADMIN_PASSWORD')

Setting.rest_api_enabled = '1'

user = User.find_by(login: login)
abort("redmine-seed-testdata: admin user not found: #{login}") unless user

user.password = password
user.password_confirmation = password
user.must_change_passwd = false
user.status = User::STATUS_ACTIVE
user.save!(validate: false)

puts "API_KEY=#{user.api_key}"
RUBY
)" || die "rails runner の実行に失敗しました"

ADMIN_API_KEY="$(printf '%s\n' "${runner_output}" | grep '^API_KEY=' | tail -1 | cut -d= -f2- || true)"
if [[ -z "${ADMIN_API_KEY}" ]]; then
  # runner_output に API キーの行が含まれている可能性があるため、そのまま
  # ログへ出さず、値部分を伏せてから提示する（CLAUDE.md §4.6「API キーを
  # 出力しない」）。
  sanitized_output="$(printf '%s\n' "${runner_output}" | sed -E 's/^(API_KEY=).*/\1[redacted]/')"
  die "API キーを取得できませんでした（rails runner の出力: ${sanitized_output}）"
fi
printf '::add-mask::%s\n' "${ADMIN_API_KEY}" >&2

api_get()  { curl -fsS -H "X-Redmine-API-Key: ${ADMIN_API_KEY}" "${REDMINE_BASE_URL}${REDMINE_SUBURI}$1"; }
api_post() { curl -fsS -H "X-Redmine-API-Key: ${ADMIN_API_KEY}" -H 'Content-Type: application/json' -d "$2" "${REDMINE_BASE_URL}${REDMINE_SUBURI}$1"; }

log "[2/4] REST API への疎通を待ちます"
ready=0
for _ in $(seq 1 30); do
  if api_get "/projects.json?limit=1" >/dev/null 2>&1; then
    ready=1
    break
  fi
  sleep 2
done
[[ "${ready}" -eq 1 ]] || die "REST API に到達できませんでした（${REDMINE_BASE_URL}${REDMINE_SUBURI}）"

log "[3/4] テスト用プロジェクト（${REDMINE_TEST_PROJECT_IDENTIFIER}）を確認・作成します"
PROJECT_ID=""
if project_json="$(api_get "/projects/${REDMINE_TEST_PROJECT_IDENTIFIER}.json" 2>/dev/null)"; then
  PROJECT_ID="$(printf '%s' "${project_json}" | grep -o '"id":[0-9]*' | head -1 | cut -d: -f2 || true)"
  log "既存のプロジェクトを再利用します（id=${PROJECT_ID}）"
else
  project_json="$(api_post "/projects.json" "$(cat <<JSON
{"project":{"name":"rmapp CI テストデータ","identifier":"${REDMINE_TEST_PROJECT_IDENTIFIER}","description":"scripts/redmine-seed-testdata.sh が投入する統合テスト用プロジェクト","is_public":true}}
JSON
)")" || die "プロジェクトの作成に失敗しました"
  PROJECT_ID="$(printf '%s' "${project_json}" | grep -o '"id":[0-9]*' | head -1 | cut -d: -f2 || true)"
  log "プロジェクトを作成しました（id=${PROJECT_ID}）"
fi
[[ -n "${PROJECT_ID}" ]] || die "プロジェクト ID を取得できませんでした（応答: ${project_json}）"

log "[4/4] テスト用チケットを確認・投入します"
DESIRED_ISSUE_COUNT=3
existing_count="$(api_get "/issues.json?project_id=${PROJECT_ID}&status_id=*&limit=1" \
  | grep -o '"total_count":[0-9]*' | head -1 | cut -d: -f2 || true)"
existing_count="${existing_count:-0}"

if [[ "${existing_count}" -ge "${DESIRED_ISSUE_COUNT}" ]]; then
  log "既存のチケット ${existing_count} 件を再利用します（新規投入はスキップ）"
else
  # 途中失敗（一部だけ作成済み）で再実行しても、既定件数まで補充する
  # （件数だけを条件にすることで、特定の件名の有無に依存しない）。
  to_create=$(( DESIRED_ISSUE_COUNT - existing_count ))
  TRACKER_ID="$(api_get "/trackers.json" | grep -o '"id":[0-9]*' | head -1 | cut -d: -f2 || true)"
  [[ -n "${TRACKER_ID}" ]] || die "トラッカーを取得できませんでした"

  log "テスト用チケットを ${to_create} 件投入します（既存 ${existing_count} 件）"
  for n in $(seq 1 "${to_create}"); do
    subject="疎通確認用チケット（自動投入 $(( existing_count + n ))）"
    api_post "/issues.json" "$(cat <<JSON
{"issue":{"project_id":${PROJECT_ID},"tracker_id":${TRACKER_ID},"subject":"${subject}"}}
JSON
)" >/dev/null || die "チケットの作成に失敗しました（${subject}）"
  done
  log "テスト用チケットを ${to_create} 件投入しました"
fi

echo "API_KEY=${ADMIN_API_KEY}"
