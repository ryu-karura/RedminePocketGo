#!/usr/bin/env bash
#
# scripts/redmine-oauth-probe.sh — 起動中の RedmineDocker 開発スタック（Redmine 7）
# の OAuth 2.0 プロバイダ（Doorkeeper）の実挙動を探査する
# （docs/plan.md フェーズ 10 第 1 タスク、docs/Design.md §14「OAuth の実機確認」）。
#
# Redmine の管理画面・ブラウザを使わず、次を HTTP だけで再現して結果を記録する:
#   ① 発行トークンで GET /users/current.json が通る最小スコープ
#   ② 各 REST API が（非管理者・スコープ付きで）通るか
#   ③ 認可要求の code_challenge（PKCE S256）の受理と検証
#   ④ リフレッシュ時の旧リフレッシュ/アクセストークンの扱い
#   ⑤ スコープ外の書き込みが拒否されるか（6.1.x の未適用報告の再現確認）
#   ⑥ /oauth/revoke の効果
#
# Redmine の API キーは一切使わない（CLAUDE.md §9-1）。必要な前提（REST API の
# 有効化、試験用の非管理者ユーザー・プロジェクト・チケット・OAuth アプリケーション）
# は、起動中コンテナ内の rails runner だけで作る（RedmineDocker リポジトリ自体は
# 変更しない — CLAUDE.md §9-6）。
#
# 出力: 標準出力に `PROBE|<項目>|<結果>` の行を書く。GITHUB_STEP_SUMMARY が
# あれば同じ内容を表で追記する。基本フロー（認可コード取得 → 交換 → 利用者特定）
# が成立しない場合のみ非 0 で終了する。それ以外の観測結果は記録だけで失敗にしない。
#
# 使い方:
#   scripts/redmine-oauth-probe.sh
#
# 環境変数（任意）:
#   REDMINE_WEB_CONTAINER  既定 redmine-web
#   REDMINE_BASE_URL       既定 http://localhost:8080
#   REDMINE_SUBURI         既定 /redmine
#
# 前提: redmine-web が healthy で起動済み。docker, curl, jq, openssl, python3。

set -euo pipefail

log() { printf '%s %s\n' "$(date '+%Y-%m-%dT%H:%M:%S%z')" "$*" >&2; }
die() { log "エラー: $*"; exit 1; }

for c in docker curl jq openssl python3; do
  command -v "${c}" >/dev/null 2>&1 || die "${c} が見つかりません"
done

REDMINE_WEB_CONTAINER="${REDMINE_WEB_CONTAINER:-redmine-web}"
REDMINE_BASE_URL="${REDMINE_BASE_URL:-http://localhost:8080}"
REDMINE_SUBURI="${REDMINE_SUBURI:-/redmine}"
BASE="${REDMINE_BASE_URL}${REDMINE_SUBURI}"
# 実際には到達されない（Location ヘッダーを読むだけ）。https にして
# Doorkeeper の redirect_uri SSL 検査に依存しないようにする。
REDIRECT_URI="https://rmapp.example.test/api/auth/callback"
FULL_SCOPES="view_project view_issues add_issues edit_issues add_issue_notes"
RO_SCOPES="view_project view_issues"

WORK="$(mktemp -d)"
trap 'rm -rf "${WORK}"' EXIT
JAR="${WORK}/jar"

RESULTS=()
record() { # 項目 結果
  RESULTS+=("$1|$2")
  printf 'PROBE|%s|%s\n' "$1" "$2"
}

PROBE_LOGIN="rmapp_probe"
PROBE_PASSWORD="$(openssl rand -hex 16)"
printf '::add-mask::%s\n' "${PROBE_PASSWORD}" >&2

docker exec "${REDMINE_WEB_CONTAINER}" true >/dev/null 2>&1 \
  || die "${REDMINE_WEB_CONTAINER} コンテナに到達できません"

log "[1/6] rails runner で前提（REST API・ユーザー・プロジェクト・チケット・OAuth アプリ）を用意します"
# SECRET_KEY_BASE の扱いは scripts/redmine-seed-testdata.sh と同じ理由
# （docker exec の新規プロセスには entrypoint.sh の export が伝わらない）。
runner_output="$(docker exec -i \
  -e "PROBE_LOGIN=${PROBE_LOGIN}" \
  -e "PROBE_PASSWORD=${PROBE_PASSWORD}" \
  -e "PROBE_REDIRECT=${REDIRECT_URI}" \
  -e "PROBE_SCOPES=${FULL_SCOPES}" \
  "${REDMINE_WEB_CONTAINER}" \
  bash -ec '
    if [ -n "${REDMINE_SECRET_KEY_BASE_FILE:-}" ]; then
      SECRET_KEY_BASE="$(cat "${REDMINE_SECRET_KEY_BASE_FILE}")"
      export SECRET_KEY_BASE
    fi
    exec bundle exec rails runner -
  ' <<'RUBY'
Setting.rest_api_enabled = '1'

project = Project.find_by(identifier: 'rmapp-oauth-probe') ||
          Project.create!(name: 'rmapp OAuth probe', identifier: 'rmapp-oauth-probe',
                          is_public: false, enabled_module_names: %w[issue_tracking])

user = User.find_by(login: ENV.fetch('PROBE_LOGIN')) ||
       User.new(login: ENV.fetch('PROBE_LOGIN'), firstname: 'Probe', lastname: 'User',
                mail: 'rmapp-probe@example.com', language: 'en')
user.password = ENV.fetch('PROBE_PASSWORD')
user.password_confirmation = ENV.fetch('PROBE_PASSWORD')
user.must_change_passwd = false
user.status = User::STATUS_ACTIVE
user.admin = false
user.save!

role = Role.givable.find_by(name: 'Developer') || Role.givable.first
Member.create!(user: user, project: project, roles: [role]) unless project.members.exists?(user_id: user.id)

issue = Issue.find_by(project_id: project.id, subject: 'rmapp oauth probe issue') ||
        Issue.create!(project: project, tracker: project.trackers.first, author: user,
                      subject: 'rmapp oauth probe issue', priority: IssuePriority.default)

Doorkeeper::Application.where(name: 'rmapp-probe').destroy_all
app = Doorkeeper::Application.create!(name: 'rmapp-probe', redirect_uri: ENV.fetch('PROBE_REDIRECT'),
                                      scopes: ENV.fetch('PROBE_SCOPES'), confidential: true)

puts "PROJECT_ID=#{project.id}"
puts "ISSUE_ID=#{issue.id}"
puts "CLIENT_ID=#{app.uid}"
puts "CLIENT_SECRET=#{app.plaintext_secret}"
puts "PERMISSIONS=#{Redmine::AccessControl.permissions.map(&:name).join(' ')}"
RUBY
)" || die "rails runner の実行に失敗しました（Doorkeeper のモデル名・スコープ名の想定違いの可能性）"

pick() { printf '%s\n' "${runner_output}" | grep "^$1=" | tail -1 | cut -d= -f2- || true; }
PROJECT_ID="$(pick PROJECT_ID)"
ISSUE_ID="$(pick ISSUE_ID)"
CLIENT_ID="$(pick CLIENT_ID)"
CLIENT_SECRET="$(pick CLIENT_SECRET)"
[[ -n "${PROJECT_ID}" && -n "${ISSUE_ID}" && -n "${CLIENT_ID}" && -n "${CLIENT_SECRET}" ]] \
  || die "前提の払い出しに失敗しました（出力を確認してください）"
printf '::add-mask::%s\n' "${CLIENT_SECRET}" >&2
record "Redmine 権限名一覧（スコープの候補）" "$(pick PERMISSIONS)"

# --- ユーティリティ ---------------------------------------------------------

# HTML 中のフォームを解析する。
#   authenticity <file>        最初の authenticity_token の値
#   authorize-form <file>      承認（POST /oauth/authorize）フォームの hidden 項目を name=value 行で出力
html_helper() {
  python3 - "$1" "$2" <<'PY'
import sys
from html.parser import HTMLParser

mode, path = sys.argv[1], sys.argv[2]

class P(HTMLParser):
    def __init__(self):
        super().__init__()
        self.forms = []
        self.cur = None
    def handle_starttag(self, tag, attrs):
        a = dict(attrs)
        if tag == "form":
            self.cur = {"action": a.get("action", ""), "inputs": []}
            self.forms.append(self.cur)
        elif tag == "input" and self.cur is not None and "name" in a:
            self.cur["inputs"].append((a["name"], a.get("value", "") or ""))
    def handle_endtag(self, tag):
        if tag == "form":
            self.cur = None

p = P()
p.feed(open(path, encoding="utf-8", errors="replace").read())
if mode == "authenticity":
    for f in p.forms:
        for n, v in f["inputs"]:
            if n == "authenticity_token":
                print(v)
                sys.exit(0)
    sys.exit(1)
if mode == "authorize-form":
    for f in p.forms:
        names = dict(f["inputs"])
        if f["action"].rstrip("/").endswith("/oauth/authorize") and names.get("_method") != "delete":
            for n, v in f["inputs"]:
                print(f"{n}={v}")
            sys.exit(0)
    sys.exit(1)
sys.exit(2)
PY
}

b64url() { openssl base64 -A | tr '+/' '-_' | tr -d '='; }
pkce_challenge() { printf '%s' "$1" | openssl dgst -sha256 -binary | b64url; }

query_param() { # URL パラメータ名 → 値
  printf '%s' "$1" | sed -n "s/.*[?&]$2=\([^&#]*\).*/\1/p" | head -1
}

# 認可コードを得る。引数: scope code_challenge(空可)。標準出力にコード、
# 取得できなければ空文字（理由は record 済み）。
authorize_code() {
  local scope="$1" challenge="${2:-}" label="$3"
  local -a q=(--data-urlencode "client_id=${CLIENT_ID}"
              --data-urlencode "redirect_uri=${REDIRECT_URI}"
              --data-urlencode "response_type=code"
              --data-urlencode "state=probe-state")
  [[ -n "${scope}" ]] && q+=(--data-urlencode "scope=${scope}")
  if [[ -n "${challenge}" ]]; then
    q+=(--data-urlencode "code_challenge=${challenge}" --data-urlencode "code_challenge_method=S256")
  fi
  local out status redirect
  out="$(curl -sS -G -b "${JAR}" -c "${JAR}" -o "${WORK}/authz.html" \
          -w '%{http_code} %{redirect_url}' "${q[@]}" "${BASE}/oauth/authorize")"
  status="${out%% *}"; redirect="${out#* }"
  if [[ "${status}" == "200" ]]; then
    local -a fields=() args=()
    mapfile -t fields < <(html_helper authorize-form "${WORK}/authz.html" || true)
    if [[ "${#fields[@]}" -eq 0 ]]; then
      record "${label}: 同意画面" "承認フォームを解析できません（HTTP 200）"
      return 0
    fi
    local f
    for f in "${fields[@]}"; do args+=(--data-urlencode "${f}"); done
    out="$(curl -sS -b "${JAR}" -c "${JAR}" -o "${WORK}/authz2.html" \
            -w '%{http_code} %{redirect_url}' "${args[@]}" "${BASE}/oauth/authorize")"
    status="${out%% *}"; redirect="${out#* }"
  fi
  local code
  code="$(query_param "${redirect}" code)"
  if [[ -z "${code}" ]]; then
    record "${label}: 認可" "コードを得られません（HTTP ${status} Location=${redirect:-なし}）"
  fi
  printf '%s' "${code}"
}

# トークンエンドポイントへ POST。引数: ファイル名接頭辞 + curl 用 --data-urlencode 群。
# 応答 JSON を ${WORK}/<prefix>.json に保存し、HTTP ステータスを標準出力へ。
token_post() {
  local prefix="$1"; shift
  curl -sS -o "${WORK}/${prefix}.json" -w '%{http_code}' \
    --data-urlencode "client_id=${CLIENT_ID}" \
    --data-urlencode "client_secret=${CLIENT_SECRET}" \
    "$@" "${BASE}/oauth/token"
}

api() { # トークン メソッド パス [curl 追加引数...] → 標準出力: HTTP ステータス
  local token="$1" method="$2" path="$3"; shift 3
  curl -sS -o "${WORK}/api.out" -w '%{http_code}' -X "${method}" \
    -H "Authorization: Bearer ${token}" -H 'Accept: application/json' "$@" "${BASE}${path}"
}

# --- ログイン（Redmine セッション。ブラウザ相当）------------------------------

log "[2/6] 試験用ユーザーで Redmine にログインします（Cookie セッション）"
curl -sS -c "${JAR}" -b "${JAR}" -o "${WORK}/login.html" "${BASE}/login"
login_token="$(html_helper authenticity "${WORK}/login.html")" || die "ログイン画面の authenticity_token を取得できません"
login_status="$(curl -sS -c "${JAR}" -b "${JAR}" -o /dev/null -w '%{http_code}' \
  --data-urlencode "authenticity_token=${login_token}" \
  --data-urlencode "username=${PROBE_LOGIN}" \
  --data-urlencode "password=${PROBE_PASSWORD}" \
  "${BASE}/login")"
[[ "${login_status}" == "302" ]] || die "Redmine へのログインに失敗しました（HTTP ${login_status}）"

# --- ③ PKCE と基本フロー ----------------------------------------------------

log "[3/6] 認可コードフロー（PKCE S256）の基本動作と検証挙動"
verifier="$(openssl rand -hex 32)"
challenge="$(pkce_challenge "${verifier}")"

code="$(authorize_code "${FULL_SCOPES}" "${challenge}" "基本フロー")"
[[ -n "${code}" ]] || die "認可コードを取得できません（基本フローが成立しません）"
st="$(token_post tok_main --data-urlencode "grant_type=authorization_code" \
      --data-urlencode "code=${code}" --data-urlencode "redirect_uri=${REDIRECT_URI}" \
      --data-urlencode "code_verifier=${verifier}")"
[[ "${st}" == "200" ]] || die "コード交換に失敗しました（HTTP ${st}: $(jq -c 'del(.access_token,.refresh_token)' "${WORK}/tok_main.json" 2>/dev/null || true)）"
ACCESS="$(jq -r .access_token "${WORK}/tok_main.json")"
REFRESH="$(jq -r .refresh_token "${WORK}/tok_main.json")"
printf '::add-mask::%s\n::add-mask::%s\n' "${ACCESS}" "${REFRESH}" >&2
record "③ 正しい code_verifier での交換" "HTTP ${st}"
record "⑥-0 トークン応答のフィールド" "$(jq -c 'del(.access_token,.refresh_token) + {has_refresh: (.refresh_token != null)}' "${WORK}/tok_main.json" 2>/dev/null || jq -c 'keys' "${WORK}/tok_main.json")"

# 誤った verifier
code2="$(authorize_code "${FULL_SCOPES}" "${challenge}" "誤 verifier")"
if [[ -n "${code2}" ]]; then
  st="$(token_post tok_wrong --data-urlencode "grant_type=authorization_code" \
        --data-urlencode "code=${code2}" --data-urlencode "redirect_uri=${REDIRECT_URI}" \
        --data-urlencode "code_verifier=$(openssl rand -hex 32)")"
  record "③ 誤った code_verifier での交換" "HTTP ${st} $(jq -c '{error}' "${WORK}/tok_wrong.json" 2>/dev/null || true)"
fi
# verifier なし（challenge を付けた要求）
code3="$(authorize_code "${FULL_SCOPES}" "${challenge}" "verifier 欠落")"
if [[ -n "${code3}" ]]; then
  st="$(token_post tok_noverifier --data-urlencode "grant_type=authorization_code" \
        --data-urlencode "code=${code3}" --data-urlencode "redirect_uri=${REDIRECT_URI}")"
  record "③ challenge ありで verifier なしの交換" "HTTP ${st} $(jq -c '{error}' "${WORK}/tok_noverifier.json" 2>/dev/null || true)"
fi
# コード再利用
st="$(token_post tok_reuse --data-urlencode "grant_type=authorization_code" \
      --data-urlencode "code=${code}" --data-urlencode "redirect_uri=${REDIRECT_URI}" \
      --data-urlencode "code_verifier=${verifier}")"
record "③ 使用済みコードの再利用" "HTTP ${st} $(jq -c '{error}' "${WORK}/tok_reuse.json" 2>/dev/null || true)"

# --- ① 最小スコープ -----------------------------------------------------------

log "[4/6] スコープ別に /users/current.json を確認します"
for sc in "" "view_project" "view_issues"; do
  ch="$(pkce_challenge "${verifier}")"
  c="$(authorize_code "${sc}" "${ch}" "① スコープ『${sc:-指定なし}』")"
  [[ -n "${c}" ]] || continue
  st="$(token_post tok_sc --data-urlencode "grant_type=authorization_code" \
        --data-urlencode "code=${c}" --data-urlencode "redirect_uri=${REDIRECT_URI}" \
        --data-urlencode "code_verifier=${verifier}")"
  if [[ "${st}" != "200" ]]; then
    record "① スコープ『${sc:-指定なし}』のコード交換" "HTTP ${st} $(jq -c '{error}' "${WORK}/tok_sc.json" 2>/dev/null || true)"
    continue
  fi
  tk="$(jq -r .access_token "${WORK}/tok_sc.json")"
  printf '::add-mask::%s\n' "${tk}" >&2
  s2="$(api "${tk}" GET /users/current.json)"
  record "① /users/current.json（スコープ『${sc:-指定なし}』、付与=$(jq -r .scope "${WORK}/tok_sc.json")）" "HTTP ${s2}"
done

# --- ② エンドポイント ---------------------------------------------------------

log "[5/6] FULL スコープのトークンで各 REST API を呼びます"
record "② /users/current.json の応答キー" "$(api "${ACCESS}" GET /users/current.json >/dev/null; jq -c '.user | {id, login, admin: (.admin // null)} + {has_api_key: (.api_key != null)}' "${WORK}/api.out" 2>/dev/null || echo '解析不可')"
for path in \
  "/projects.json" "/projects/${PROJECT_ID}.json" "/issues.json?project_id=${PROJECT_ID}" \
  "/issues/${ISSUE_ID}.json?include=journals,attachments,children" "/issue_statuses.json" \
  "/trackers.json" "/enumerations/issue_priorities.json" \
  "/projects/${PROJECT_ID}/memberships.json" "/projects/${PROJECT_ID}/versions.json" \
  "/custom_fields.json" "/my/account.json"; do
  record "② GET ${path%%\?*}（FULL）" "HTTP $(api "${ACCESS}" GET "${path}")"
done

# --- ⑤ スコープ強制 -----------------------------------------------------------

log "[6/6] スコープ外の書き込み、リフレッシュ、失効"
ch="$(pkce_challenge "${verifier}")"
c="$(authorize_code "${RO_SCOPES}" "${ch}" "⑤ 読み取り専用スコープ")"
if [[ -n "${c}" ]]; then
  st="$(token_post tok_ro --data-urlencode "grant_type=authorization_code" \
        --data-urlencode "code=${c}" --data-urlencode "redirect_uri=${REDIRECT_URI}" \
        --data-urlencode "code_verifier=${verifier}")"
  if [[ "${st}" == "200" ]]; then
    RO="$(jq -r .access_token "${WORK}/tok_ro.json")"
    printf '::add-mask::%s\n' "${RO}" >&2
    record "⑤ 読み取り専用トークンで GET /issues/{id}.json" "HTTP $(api "${RO}" GET "/issues/${ISSUE_ID}.json")"
    record "⑤ 読み取り専用トークンで PUT（notes 追加）" "HTTP $(api "${RO}" PUT "/issues/${ISSUE_ID}.json" -H 'Content-Type: application/json' -d '{"issue":{"notes":"probe (read-only token)"}}') ※期待 403。204 ならスコープ未適用"
    record "⑤ 読み取り専用トークンで PUT（subject 変更）" "HTTP $(api "${RO}" PUT "/issues/${ISSUE_ID}.json" -H 'Content-Type: application/json' -d '{"issue":{"subject":"rmapp oauth probe issue"}}') ※期待 403"
    record "⑤ 読み取り専用トークンで POST /issues.json" "HTTP $(api "${RO}" POST "/issues.json" -H 'Content-Type: application/json' -d "{\"issue\":{\"project_id\":${PROJECT_ID},\"subject\":\"probe create (read-only token)\"}}") ※期待 403"
  fi
fi
record "⑤ FULL トークンで PUT（notes 追加）" "HTTP $(api "${ACCESS}" PUT "/issues/${ISSUE_ID}.json" -H 'Content-Type: application/json' -d '{"issue":{"notes":"probe (full token)"}}') ※期待 204"

# --- ④ リフレッシュ -----------------------------------------------------------

st="$(token_post tok_ref --data-urlencode "grant_type=refresh_token" --data-urlencode "refresh_token=${REFRESH}")"
record "④ リフレッシュ" "HTTP ${st} $(jq -c 'del(.access_token,.refresh_token)' "${WORK}/tok_ref.json" 2>/dev/null || true)"
if [[ "${st}" == "200" ]]; then
  NEW_ACCESS="$(jq -r .access_token "${WORK}/tok_ref.json")"
  NEW_REFRESH="$(jq -r .refresh_token "${WORK}/tok_ref.json")"
  printf '::add-mask::%s\n::add-mask::%s\n' "${NEW_ACCESS}" "${NEW_REFRESH}" >&2
  record "④ リフレッシュトークンが入れ替わったか" "$([[ "${NEW_REFRESH}" != "${REFRESH}" ]] && echo 入れ替わった || echo 同一のまま)"
  record "④ リフレッシュ後に旧アクセストークンで /users/current.json" "HTTP $(api "${ACCESS}" GET /users/current.json)"
  record "④ 新アクセストークンで /users/current.json" "HTTP $(api "${NEW_ACCESS}" GET /users/current.json)"
  st="$(token_post tok_ref_old --data-urlencode "grant_type=refresh_token" --data-urlencode "refresh_token=${REFRESH}")"
  record "④ 旧リフレッシュトークンの再使用" "HTTP ${st} $(jq -c '{error}' "${WORK}/tok_ref_old.json" 2>/dev/null || true)"
  ACCESS="${NEW_ACCESS}"
  REFRESH="${NEW_REFRESH}"
fi

# --- ⑥ 失効 ------------------------------------------------------------------

st="$(curl -sS -o "${WORK}/revoke.out" -w '%{http_code}' \
      --data-urlencode "client_id=${CLIENT_ID}" --data-urlencode "client_secret=${CLIENT_SECRET}" \
      --data-urlencode "token=${REFRESH}" --data-urlencode "token_type_hint=refresh_token" \
      "${BASE}/oauth/revoke")"
record "⑥ /oauth/revoke（リフレッシュトークン）" "HTTP ${st}"
record "⑥ 失効後に /users/current.json" "HTTP $(api "${ACCESS}" GET /users/current.json) ※期待 401"

# --- サマリ -------------------------------------------------------------------

if [[ -n "${GITHUB_STEP_SUMMARY:-}" ]]; then
  {
    echo "### Redmine OAuth 探査結果（Design.md §14）"
    echo
    echo "| 項目 | 結果 |"
    echo "|---|---|"
    for r in "${RESULTS[@]}"; do
      printf '| %s | %s |\n' "${r%%|*}" "${r#*|}"
    done
  } >> "${GITHUB_STEP_SUMMARY}"
fi
log "探査完了（${#RESULTS[@]} 項目）"
