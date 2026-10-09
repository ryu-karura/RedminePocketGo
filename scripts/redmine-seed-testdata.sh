#!/usr/bin/env bash
#
# scripts/redmine-seed-testdata.sh — 起動中の RedmineDocker 開発スタックに、
# 統合テスト（scripts/test-stack.sh）に必要な最小限の前提を整える冪等スクリプト
# （CLAUDE.md §5、docs/plan.md フェーズ 8・10）。
#
# 開発サンドボックスには Docker デーモンがなく scripts/test-stack.sh を実行
# できないため、Docker デーモンを持つ CI（.github/workflows/）からのみ呼ばれる
# 想定だが、実 RedmineDocker スタックを手元で起動している開発者が手動で使う
# こともできる。
#
# 行うこと（すべて RedmineDocker の「起動中のコンテナに対する操作」であり、
# RedmineDocker リポジトリ自体（イメージ定義・compose 定義等）は一切変更しない
# — CLAUDE.md §9-6「本リポジトリは RedmineDocker スタックを変更しない」）。
# すべてコンテナ内の rails runner だけで行い、**Redmine の API キーは一切使わない**
# （CLAUDE.md §9-1。キーの生成・取得・送信のいずれもしない）:
#   1. Redmine の REST API を有効化する（既定で無効。OAuth プロバイダも REST API が
#      無効のあいだは認証を拒否する。手動手順は docs/Setup.md）
#   2. テスト用プロジェクト 1 件とチケット 3 件を作る（再実行しても重複作成しない）
#   3. テスト用の OAuth アプリケーション（rmapp-ci-testdata）を用意し、管理者を
#      持ち主とするアクセストークンを 1 つ発行する
#
# 標準出力には最終行 `ACCESS_TOKEN=<値>` のみを書く。呼び出し元はこれを
# scripts/test-stack.sh の RMAPP_STACK_ACCESS_TOKEN にそのまま渡せる。
# それ以外のログはすべて標準エラーに書く。トークンは Redmine がハッシュで保存する
# ため、発行した直後のこの出力でしか平文を得られない（実行のたびに新しく発行する）。
#
# 使い方:
#   scripts/redmine-seed-testdata.sh
#   token="$(scripts/redmine-seed-testdata.sh | tail -1 | cut -d= -f2-)"
#
# 環境変数（すべて任意。既定値は RedmineDocker の compose.dev.yaml の既定値と
# 一致させている）:
#   REDMINE_WEB_CONTAINER            既定 redmine-web
#   REDMINE_ADMIN_LOGIN              既定 admin（トークンの持ち主・チケットの作成者）
#   REDMINE_TEST_PROJECT_IDENTIFIER  既定 rmapp-ci-testdata
#
# 前提:
#   - redmine-web コンテナが healthy な状態で起動済みであること
#     （docker compose -f compose.dev.yaml up --build -d）
#   - docker コマンドが利用できること（存在チェックあり）。加えて
#     grep/cut/tail/date などの POSIX 標準コマンドを前提とする（他の
#     scripts/*.sh 同様、これらの存在チェックは行わない）

set -euo pipefail

log() { printf '%s %s\n' "$(date '+%Y-%m-%dT%H:%M:%S%z')" "$*" >&2; }
die() { log "エラー: $*"; exit 1; }

command -v docker >/dev/null 2>&1 || die "docker が見つかりません"

REDMINE_WEB_CONTAINER="${REDMINE_WEB_CONTAINER:-redmine-web}"
REDMINE_ADMIN_LOGIN="${REDMINE_ADMIN_LOGIN:-admin}"
REDMINE_TEST_PROJECT_IDENTIFIER="${REDMINE_TEST_PROJECT_IDENTIFIER:-rmapp-ci-testdata}"
# Redmine の identifier 制約に合わせて検証する（Ruby のコードへ環境変数として
# 渡すため、この文字集合を外れる値は想定外の動作につながる）。
[[ "${REDMINE_TEST_PROJECT_IDENTIFIER}" =~ ^[a-z0-9_-]+$ ]] \
  || die "REDMINE_TEST_PROJECT_IDENTIFIER が不正です（英小文字・数字・-・_ のみ使用可）: ${REDMINE_TEST_PROJECT_IDENTIFIER}"
[[ "${REDMINE_ADMIN_LOGIN}" =~ ^[A-Za-z0-9_.@-]+$ ]] \
  || die "REDMINE_ADMIN_LOGIN が不正です: ${REDMINE_ADMIN_LOGIN}"

# CI（GitHub Actions 等が設定する CI=true）でのみ確認をスキップする
# （破壊的ではないが、起動中のスタックの設定（REST API の有効化）とデータを変更する。
# scripts/restore.sh の確認語方式に合わせる）。標準入力が端末でないだけの
# 非対話実行（cron・パイプ経由など）は CI とはみなさず、確認語の入力を要求する
# （読めなければ read が失敗し安全側に倒れる）。
if [[ -z "${CI:-}" ]]; then
  log "${REDMINE_WEB_CONTAINER} コンテナの REST API を有効化し、テスト用データと OAuth アプリケーション・アクセストークンを作成します。"
  read -rp "続行するには CONTINUE と入力してください: " confirm
  [[ "${confirm}" == "CONTINUE" ]] || die "確認語が一致しないため中止しました"
fi

docker exec "${REDMINE_WEB_CONTAINER}" true >/dev/null 2>&1 \
  || die "${REDMINE_WEB_CONTAINER} コンテナに到達できません（起動・healthy を確認してください）"

log "rails runner で REST API 有効化・テストデータ・OAuth アプリケーション・アクセストークンを用意します"
# entrypoint.sh は SECRET_KEY_BASE を自分の bash プロセス内でのみ解決・export
# しており（containers/redmine-web/entrypoint.sh の resolve_secret）、
# コンテナのプロセス環境そのものには残らない。そのため docker exec で
# 新規プロセスを起こすここでは、entrypoint.sh と同じ規約
# （REDMINE_SECRET_KEY_BASE_FILE、compose.dev.yaml が設定するので
# docker exec にもコンテナ環境として見えている）に従い、自前でシークレット
# ファイルを読んで SECRET_KEY_BASE を用意してから rails runner を呼ぶ。
# シークレットファイルは root にしか読めない（entrypoint.sh も root のうちに
# 読んでから redmine ユーザーへ runuser している）ため、-u redmine は指定
# しない（redmine ユーザーで読もうとすると Permission denied で空文字になり、
# secret_key_base が空文字列という別のエラーで rails runner が失敗する）。
runner_output="$(docker exec -i \
  -e "REDMINE_ADMIN_LOGIN=${REDMINE_ADMIN_LOGIN}" \
  -e "REDMINE_TEST_PROJECT_IDENTIFIER=${REDMINE_TEST_PROJECT_IDENTIFIER}" \
  "${REDMINE_WEB_CONTAINER}" \
  bash -ec '
    if [ -n "${REDMINE_SECRET_KEY_BASE_FILE:-}" ]; then
      SECRET_KEY_BASE="$(cat "${REDMINE_SECRET_KEY_BASE_FILE}")"
      export SECRET_KEY_BASE
    fi
    exec bundle exec rails runner -
  ' <<'RUBY'
Setting.rest_api_enabled = '1'

admin = User.find_by(login: ENV.fetch('REDMINE_ADMIN_LOGIN'))
abort('redmine-seed-testdata: admin user not found') unless admin

ident = ENV.fetch('REDMINE_TEST_PROJECT_IDENTIFIER')
project = Project.find_by(identifier: ident) ||
          Project.create!(name: 'rmapp CI テストデータ', identifier: ident,
                          description: 'scripts/redmine-seed-testdata.sh が投入する統合テスト用プロジェクト',
                          is_public: true, enabled_module_names: %w[issue_tracking])

# 途中失敗（一部だけ作成済み）で再実行しても、既定件数まで補充する。
tracker = project.trackers.first || Tracker.first
existing = Issue.where(project_id: project.id).count
(existing...3).each do |n|
  Issue.create!(project: project, tracker: tracker, author: admin,
                subject: "疎通確認用チケット（自動投入 #{n + 1}）", priority: IssuePriority.default)
end

scopes = 'view_project view_issues add_issues edit_issues add_issue_notes view_members'
app = Doorkeeper::Application.find_by(name: 'rmapp-ci-testdata') ||
      Doorkeeper::Application.create!(name: 'rmapp-ci-testdata',
                                      redirect_uri: 'https://rmapp.example.test/api/auth/callback',
                                      scopes: scopes, confidential: true)
token = Doorkeeper::AccessToken.create!(application_id: app.id, resource_owner_id: admin.id,
                                        scopes: scopes, expires_in: 7200)

puts "ACCESS_TOKEN=#{token.plaintext_token}"
RUBY
)" || die "rails runner の実行に失敗しました（Doorkeeper のモデル・スコープ名の想定違いの可能性）"

ACCESS_TOKEN="$(printf '%s\n' "${runner_output}" | grep '^ACCESS_TOKEN=' | tail -1 | cut -d= -f2- || true)"
if [[ -z "${ACCESS_TOKEN}" ]]; then
  # runner_output にトークンの行が含まれている可能性があるため、そのままログへ
  # 出さず、値部分を伏せてから提示する（CLAUDE.md §4.6「トークンを出力しない」）。
  sanitized_output="$(printf '%s\n' "${runner_output}" | sed -E 's/^(ACCESS_TOKEN=).*/\1[redacted]/')"
  die "アクセストークンを取得できませんでした（rails runner の出力: ${sanitized_output}）"
fi
# CI ログに平文で出た場合でも隠す（GitHub Actions のワークフローコマンド。
# ローカル実行時はそのまま無害な行として扱われる）。
printf '::add-mask::%s\n' "${ACCESS_TOKEN}" >&2

log "完了しました（プロジェクト: ${REDMINE_TEST_PROJECT_IDENTIFIER}）"
echo "ACCESS_TOKEN=${ACCESS_TOKEN}"
