# 実装計画（plan.md）

本書は RedminePocketGo を段階的に実装するための計画と進捗チェックリストです。
設計の根拠はすべて [Design.md](Design.md) にあり、本書には設計を書きません
（重複禁止）。運用ルールの正本は `.claude/skills/implement/SKILL.md` です。

## 運用ルール（要約）

- フェーズは番号順に進める。**「進行中」のフェーズは常に 1 つだけ。**
- 1 つのタスクを終えるごとに、実装と同じコミットでチェック `[x]` を付ける。
- 実装より先に失敗するテストを書く（CLAUDE.md §9-5）。
- フェーズの完了条件がすべて満たされるまで、次のフェーズに着手しない。
- 作業中に新しいタスクが見つかったら、先に該当フェーズへ追記してから実装する。
- 計画自体の変更（追加・分割・順序変更）は、末尾の「変更履歴」に 1 行残す。
- タスク着手前に `.claude/skills/test/LESSONS.md` を読み、過去の失敗ルールに従う。
- 無人実行（定期トリガー）は `.claude/skills/implement/SKILL.md` の
  「Unattended runs」節に従う: 最初にオープンな作業 PR を探して処理し、
  PR が閉じていれば新ブランチ + 新 PR を作成。末尾の「自動実行ログ」に
  実行ごとに 1 行追記し、停滞（同一タスクで 2 回連続進捗ゼロ）を検知したら
  トリガーを無効化して報告する。

## フェーズ一覧

| # | フェーズ | 状態 |
|---|---|---|
| 0 | 骨組み（リポジトリ構造と開発ループ） | 完了 |
| 1 | サーバー基盤（config / store / httpapi / webfs） | 完了 |
| 2 | 認証（WebAuthn / セッション / ブートストラップ / 端末管理） | 進行中 |
| 3 | API キー保管庫と中継（credential / proxy） | 未着手 |
| 4 | Redmine クライアントと集約 API | 未着手 |
| 5 | フロントエンド基盤とログイン画面 | 未着手 |
| 6 | 業務画面（projects / issues / issue-detail / settings） | 未着手 |
| 7 | 端末紛失対策とセキュリティ強化 | 未着手 |
| 8 | 統合テストと運用スクリプト | 未着手 |
| — | 地図表示（Design.md §12） | 指示があるまで着手しない |

状態は「未着手 / 進行中 / 完了」の 3 値。変更したら同じコミットで更新する。

---

## フェーズ 0: 骨組み

目的: リポジトリ構造を CLAUDE.md §2 のとおり作り、build / test の
開発ループを成立させる。

- [x] ディレクトリ構成の作成（`app/`, `server/`, `scripts/`。CLAUDE.md §2）
- [x] `.gitignore`（`secrets/`, `data/`, ビルド成果物）
- [x] `server/go.mod`（Go 1.22+）と `cmd/rmapp/main.go` の最小起動
- [x] `server/Makefile`（`build` / `test-unit` / `test-api` ターゲット）
- [x] `scripts/generate-secrets.sh`（`session_key.txt` / `kek.txt`、mode 600、
      冪等、確認リテラル不要の生成のみ）

完了条件: `make build` 成功。`make test-unit` が実行できる（0 件で可）。
`shellcheck scripts/*.sh` 通過。

## フェーズ 1: サーバー基盤

目的: 全ハンドラが乗る土台（設定・永続化・ミドルウェア・静的配信）を作る。

- [x] `internal/config`: config.yaml の読み込み・検証・優先順位
      （flag > env `RMAPP_` > file > 既定値）。必須キー欠落はキー名付きで
      起動中止（Design.md §10）
- [x] `server/config/config.yaml` の雛形（コメントは日本語）
- [x] `internal/store`: SQLite 接続と migrations
      （users / credentials / redmine_credentials / sessions /
      enrollment_codes / webauthn_challenges。Design.md §5）
- [x] `internal/httpapi`: エラーエンベロープとエラーコード表（Design.md §6.5）
- [x] `internal/httpapi`: ミドルウェア連鎖
      `RequestID → RecoverPanic → AccessLog → Session → RequireXHRForWrites`
- [x] `internal/webfs`: 静的配信、`noCache`、`baseURL` サブパス対応
- [x] `log/slog` 構造化ログの初期化（禁止項目は CLAUDE.md §4.6）

完了条件: `make test-unit` 緑。config の必須キー欠落・不正値のテーブル
駆動テストがある。migrations が空 DB に適用できる。

## フェーズ 2: 認証

目的: パスキーでログインでき、初回はRedmine 認証情報で紐付けできる状態。

- [x] `internal/store`: セッション永続化（ID はハッシュ保存、二軸タイムアウト）
- [x] `internal/auth`: セッション発行・検証・失効（Cookie 属性は Design.md §3.5）
- [x] `internal/auth`: WebAuthn 登録セレモニー
      （`/api/auth/register/begin` / `finish`、Discoverable Credential、
      userVerification=required）
- [x] `internal/auth`: WebAuthn 認証セレモニー
      （`/api/auth/login/begin` / `finish`）
- [ ] `GET /api/auth/me` / `POST /api/auth/logout`
- [ ] レート制限（連続 5 回失敗で 60 秒ロック）
- [ ] パスワードブートストラップ（Redmine `/my/account.json` に Basic 認証、
      不存在ユーザーにも同等処理時間、パスワードは保存しない。Design.md §3.3）
- [ ] 登録コードによる端末追加（6 桁、10 分、1 回限り。Design.md §3.4）
- [ ] 端末（パスキー）一覧・名称変更・削除 API。削除時に該当セッション即失効

完了条件: `make test-api` 緑（成功 / 未認証 / 不正入力 / 上流障害の
テーブル駆動）。WebAuthn は go-webauthn のテストヘルパーまたは擬似
認証器で begin→finish を通す。

## フェーズ 3: API キー保管庫と中継

目的: ブラウザに API キーを渡さず Redmine REST API を叩ける状態。

- [ ] `internal/credential`: AES-256-GCM 暗号化保管
      （KEK はファイル、ノンスはレコード毎、`MarshalJSON` は `"[redacted]"`）
- [ ] `internal/proxy`: 許可リスト（`allowlist.go` に宣言的列挙、
      一致しなければ 404。Design.md §6.2）
- [ ] `internal/proxy`: ヘッダー制御（`X-Redmine-API-Key` 受信は 400、
      `Authorization` / `Cookie` / `X-Redmine-Switch-User` は転送禁止）
- [ ] 上流 401 → キー無効化 + 409 `redmine_credential_invalid`、
      上流 5xx → 502 `upstream_error`
- [ ] サブ URI 結合（`redmine.baseURL` + `redmine.subURI` + API パス。
      ハードコード禁止）

完了条件: `make test-api` 緑。許可リスト外 404、ヘッダー拒否・除去、
401→409 変換のテストがある（上流は `httptest.Server`）。

## フェーズ 4: Redmine クライアントと集約 API

目的: 画面が 1〜2 回の呼び出しで初期表示できる状態。

- [ ] `internal/redmine`: 型付きクライアント（タイムアウト、指数バックオフ
      最大 2 回、同時接続上限、ページング取得）
- [ ] `js/common/tree.js` 相当のサーバー側ツリー化
      （`GET /api/projects/tree`、`GET /api/projects/{id}/issues/tree`）
- [ ] `GET /api/issues/{id}/detail`（本体・履歴・添付・選択肢を 1 回で）
- [ ] `GET /api/meta`（トラッカー・ステータス・優先度）
- [ ] サーバー内キャッシュ（メタ 10 分、プロジェクトツリー 60 秒、
      必ずユーザー単位で分離。Design.md §6.6）

完了条件: `make test-unit` / `make test-api` 緑。Redmine クライアントは
`httptest.Server` のみでテスト。ツリー化は純粋関数として単体テスト。

## フェーズ 5: フロントエンド基盤とログイン画面

目的: SPA の骨格が動き、パスキーでログインできる状態。

- [ ] `app/index.html`（共通シェル: トップバー・ナビ・`<main id="screens">`・
      トースト。FOUC 防止インラインスクリプト 1 本のみ）
- [ ] `app/css/tokens.css`（テンプレートのオーシャンブルー一式 +
      `--depth-1..5` / `--status-new|open|closed`、両モード）
- [ ] `base.css` / `layout.css` / `components/`（hex は tokens.css のみ）
- [ ] `js/app.js`（`SCREENS` マニフェスト、ハッシュルーティング、
      起動時 `GET /api/auth/me`）
- [ ] `js/common/api.js`（fetch ラッパー、更新系に `X-Requested-With` 付与）
- [ ] `js/common/shell.js`（ドロワーナビ、テーマ切替、ログインオーバーレイ）
- [ ] `js/common/auth.js`（base64url 変換、`navigator.credentials`、機能判定）
- [ ] `js/common/modal.js` / `js/common/utils.js`
- [ ] `js/common/tree.js`（純粋関数。`node --test` で単体テスト）
- [ ] Tabulator 6 を `js/vendor/` に同梱（ライセンス込み）+
      `js/common/table.js` ラッパー（dataTree 対応）
- [ ] `login` 画面（パスキー主導線、登録コード、ブートストラップ。
      4 状態と失敗のインライン表示。Design.md §7.5）
- [ ] `server/e2e/` E2E ハーネス（chromedp + 同梱 Chromium、build tag
      `e2e`、`make test-e2e` ターゲット、CDP WebAuthn 仮想認証器。
      npm 依存なし — CLAUDE.md §3.1 と非抵触）

完了条件: `node --test app/js/tests/` 緑（tree.js / utils.js）。
`make test-e2e` 緑: サーバー配信でパスキー登録（CDP WebAuthn 仮想
認証器）→ ログイン → `/api/auth/me` 成功までを自動検証し、
スクリーンショットを証跡として保存・送付する（手動確認は行わない）。

## フェーズ 6: 業務画面

目的: 参照・更新の主要フローがスマートフォンで完結する状態。

- [ ] `projects` 画面（dataTree、開閉状態の localStorage 保存、未完了件数、
      検索時の祖先自動展開。Design.md §7.6）
- [ ] `issues` 画面（dataTree、2 段組行、フィルタ行、完了は折りたたみ、
      追加読み込み、作成ボタン。Design.md §7.7）
- [ ] `issue-detail` 画面（インライン編集は変更項目のみ送信、期日の残日数、
      固定コメント欄。Design.md §7.8）
- [ ] チケット作成モーダル（`#modal-` ルート）
- [ ] `settings` 画面（端末一覧・削除、登録コード発行、Redmine 連携状態と
      再紐付け、テーマ、ログアウト。Design.md §7.9）
- [ ] 全画面の 4 状態（loading / empty / error+retry / populated）と
      アクセシビリティ属性（Design.md §7.10）

完了条件: `node --test` 緑。`make test-e2e` 緑: 各画面の 4 状態
（loading / empty / error+retry / populated）と
`redmine_credential_invalid` → 再紐付け画面の導線を自動検証し、
スクリーンショットを証跡として保存・送付する（手動確認は行わない）。

## フェーズ 7: 端末紛失対策とセキュリティ強化

目的: Design.md §11 の脅威対策を完成させる。

- [ ] 初回登録完了時に 2 台目登録を促す画面
- [ ] 回復コード（10 個、1 回限り）の発行・保管促し・ログイン、
      使用後は新パスキー登録を強制（Design.md §11.4）
- [ ] Content-Security-Policy（`script-src 'self'` + FOUC スクリプトの
      ハッシュ許可。Design.md §11.1）
- [ ] 登録コード・回復コードへのレート制限適用の確認

完了条件: `make test-api` 緑。CSP ヘッダーのテストがある。

## フェーズ 8: 統合テストと運用スクリプト

目的: RedmineDocker 実スタックとの疎通と、運用の道具立てを揃える。

- [ ] `scripts/test-stack.sh`（起動確認、ヘルスチェック、許可リスト経由の
      往復 1 件。CLAUDE.md §5）
- [ ] `scripts/backup.sh` / `scripts/restore.sh`（SQLite と secrets、
      restore は確認リテラル必須）
- [ ] ヘルスエンドポイント（test-stack.sh が叩く対象）
- [ ] ドキュメント同期の最終確認（Design.md / Setup.md / Manual.md /
      README.md が実装と一致しているか。`docs-sync` の観点）

完了条件: RedmineDocker 開発スタックを起動した状態で
`scripts/test-stack.sh` 緑。`shellcheck scripts/*.sh` 通過。

---

## 自動実行ログ

定期トリガーによる無人実行の記録。各実行の最後に 1 行追記する
（`.claude/skills/implement/SKILL.md`「Unattended runs」節）。
停滞検知（同一タスクで 2 回連続進捗ゼロ → トリガー無効化）の判定にも
この表を使う。

| 日時 (UTC) | フェーズ | 完了タスク数 | コミット | 結果 |
|---|---|---|---|---|
| 2026-07-19 16:07 | 1 | 7/7 | e37217b〜6dfe12a（タスク 7 + レビュー修正 1） | フェーズ 1 完了。完了条件（test-unit 緑 / config テーブル駆動テスト / 空 DB への migrations 適用）を検証済み。品質ゲート: code-review（medium、ファインダー 2 + 検証）実施、CONFIRMED 7 件修正・3 件は理由付きで見送り |

## 変更履歴

| 日付 | 変更 | 理由 |
|---|---|---|
| 2026-07-19 | 初版作成 | 実装開始のための計画策定 |
| 2026-07-19 | フェーズ 5・6 の完了条件を手動確認から `make test-e2e`（chromedp）による自動検証に変更し、フェーズ 5 に E2E ハーネスのタスクを追加 | 無人実行では手動のブラウザ確認が実施できないため |
| 2026-07-19 | 「自動実行ログ」表を新設し、運用ルールに無人実行の参照を追加 | 定期実行の可視化と停滞検知の判定根拠のため |
