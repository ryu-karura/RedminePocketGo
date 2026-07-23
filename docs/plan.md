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
| 2 | 認証（WebAuthn / セッション / ブートストラップ / 端末管理） | 完了 |
| 3 | API キー保管庫と中継（credential / proxy） | 完了 |
| 4 | Redmine クライアントと集約 API | 完了 |
| 5 | フロントエンド基盤とログイン画面 | 完了 |
| 6 | 業務画面（projects / issues / issue-detail / settings） | 完了 |
| 7 | 端末紛失対策とセキュリティ強化 | スキップ（対応しない） |
| 8 | 統合テストと運用スクリプト | 進行中 |
| 9 | チケット詳細のカスタムフィールド表示 | 完了 |
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
- [x] `GET /api/auth/me` / `POST /api/auth/logout`
- [x] レート制限（連続 5 回失敗で 60 秒ロック）
- [x] パスワードブートストラップ（Redmine `/my/account.json` に Basic 認証、
      不存在ユーザーにも同等処理時間、パスワードは保存しない。Design.md §3.3）
- [x] 登録コードによる端末追加（6 桁、10 分、1 回限り。Design.md §3.4）
- [x] 端末（パスキー）一覧・名称変更・削除 API。削除時に該当セッション即失効

完了条件: `make test-api` 緑（成功 / 未認証 / 不正入力 / 上流障害の
テーブル駆動）。WebAuthn は go-webauthn のテストヘルパーまたは擬似
認証器で begin→finish を通す。

## フェーズ 3: API キー保管庫と中継

目的: ブラウザに API キーを渡さず Redmine REST API を叩ける状態。

- [x] `internal/credential`: AES-256-GCM 暗号化保管
      （KEK はファイル、ノンスはレコード毎、`MarshalJSON` は `"[redacted]"`）
- [x] `internal/proxy`: 許可リスト（`allowlist.go` に宣言的列挙、
      一致しなければ 404。Design.md §6.2）
- [x] `internal/proxy`: ヘッダー制御（`X-Redmine-API-Key` 受信は 400、
      `Authorization` / `Cookie` / `X-Redmine-Switch-User` は転送禁止）
- [x] 上流 401 → キー無効化 + 409 `redmine_credential_invalid`、
      上流 5xx → 502 `upstream_error`
- [x] サブ URI 結合（`redmine.baseURL` + `redmine.subURI` + API パス。
      ハードコード禁止）

完了条件: `make test-api` 緑。許可リスト外 404、ヘッダー拒否・除去、
401→409 変換のテストがある（上流は `httptest.Server`）。

## フェーズ 4: Redmine クライアントと集約 API

目的: 画面が 1〜2 回の呼び出しで初期表示できる状態。

- [x] `internal/redmine`: 型付きクライアント（タイムアウト、指数バックオフ
      最大 2 回、同時接続上限、ページング取得）
- [x] `js/common/tree.js` 相当のサーバー側ツリー化
      （`GET /api/projects/tree`、`GET /api/projects/{id}/issues/tree`）
- [x] `GET /api/issues/{id}/detail`（本体・履歴・添付・選択肢を 1 回で）
- [x] `GET /api/meta`（トラッカー・ステータス・優先度）
- [x] サーバー内キャッシュ（メタ 10 分、プロジェクトツリー 60 秒、
      必ずユーザー単位で分離。Design.md §6.6）

完了条件: `make test-unit` / `make test-api` 緑。Redmine クライアントは
`httptest.Server` のみでテスト。ツリー化は純粋関数として単体テスト。

## フェーズ 5: フロントエンド基盤とログイン画面

目的: SPA の骨格が動き、パスキーでログインできる状態。

- [x] `app/index.html`（共通シェル: トップバー・ナビ・`<main id="screens">`・
      トースト。FOUC 防止インラインスクリプト 1 本のみ）
- [x] `app/css/tokens.css`（テンプレートのオーシャンブルー一式 +
      `--depth-1..5` / `--status-new|open|closed`、両モード）
- [x] `base.css` / `layout.css` / `components/`（hex は tokens.css のみ）
- [x] `js/app.js`（`SCREENS` マニフェスト、ハッシュルーティング、
      起動時 `GET /api/auth/me`）
- [x] `js/common/api.js`（fetch ラッパー、更新系に `X-Requested-With` 付与）
- [x] `js/common/shell.js`（ドロワーナビ、テーマ切替、ログインオーバーレイ）
- [x] `js/common/auth.js`（base64url 変換、`navigator.credentials`、機能判定）
- [x] `js/common/modal.js` / `js/common/utils.js`
- [x] `js/common/tree.js`（純粋関数。`node --test` で単体テスト）
- [x] Tabulator 6 を `js/vendor/` に同梱（ライセンス込み）+
      `js/common/table.js` ラッパー（dataTree 対応）
- [x] `login` 画面（パスキー主導線、登録コード、ブートストラップ。
      4 状態と失敗のインライン表示。Design.md §7.5）
- [x] `server/e2e/` E2E ハーネス（chromedp + 同梱 Chromium、build tag
      `e2e`、`make test-e2e` ターゲット、CDP WebAuthn 仮想認証器。
      npm 依存なし — CLAUDE.md §3.1 と非抵触）

完了条件: `node --test app/js/tests/*.test.js` 緑（tree.js / utils.js）。
`make test-e2e` 緑: サーバー配信でパスキー登録（CDP WebAuthn 仮想
認証器）→ ログイン → `/api/auth/me` 成功までを自動検証し、
スクリーンショットを証跡として保存・送付する（手動確認は行わない）。

## フェーズ 6: 業務画面

目的: 参照・更新の主要フローがスマートフォンで完結する状態。

- [x] `projects` 画面（dataTree、開閉状態の localStorage 保存、
      検索時の祖先自動展開、行タップで issues へ。Design.md §7.6）
- [x] プロジェクト別 未完了チケット数（サーバー: `/api/projects/tree` の各
      ノードに `openIssues` を付与、画面: 右端に表示。Design.md §7.6）
- [x] `issues` 画面（dataTree、2 段組行、フィルタ行、完了は折りたたみ、
      作成ボタン。Design.md §7.7。集約 API は全件を一括返却するため
      クライアント側の追加読み込みは不要）
- [x] `issue-detail` 画面（インライン編集は変更項目のみ送信、期日の残日数、
      固定コメント欄。Design.md §7.8）
- [x] チケット作成モーダル（`#modal-` ルート）
- [x] `settings` 画面（端末一覧・削除、登録コード発行、Redmine 連携状態と
      再紐付け、テーマ、ログアウト。Design.md §7.9）
- [x] 全画面の 4 状態（loading / empty / error+retry / populated）と
      アクセシビリティ属性（Design.md §7.10）

完了条件: `node --test app/js/tests/*.test.js` 緑。`make test-e2e` 緑: 各画面の 4 状態
（loading / empty / error+retry / populated）と
`redmine_credential_invalid` → 再紐付け画面の導線を自動検証し、
スクリーンショットを証跡として保存・送付する（手動確認は行わない）。

## フェーズ 7: 端末紛失対策とセキュリティ強化

目的: Design.md §11 の脅威対策を完成させる。
      本フェーズ対応はスキップする（2026-07-23 オーナー指示。変更履歴参照）

- [ ] 初回登録完了時に 2 台目登録を促す画面
- [ ] 回復コード（10 個、1 回限り）の発行・保管促し・ログイン、
      使用後は新パスキー登録を強制（Design.md §11.4）
- [ ] Content-Security-Policy（`script-src 'self'` + FOUC スクリプトの
      ハッシュ許可。Design.md §11.1）
- [ ] 登録コード・回復コードへのレート制限適用の確認

完了条件: `make test-api` 緑。CSP ヘッダーのテストがある。

## フェーズ 8: 統合テストと運用スクリプト

目的: RedmineDocker 実スタックとの疎通と、運用の道具立てを揃える。

- [x] ヘルスエンドポイント（`GET /healthz` / `GET /readyz`。Setup.md §11。
      test-stack.sh が叩く対象のため test-stack.sh より先に実装）
- [x] `scripts/test-stack.sh`（起動確認、ヘルスチェック、許可リスト経由の
      往復 1 件。CLAUDE.md §5。往復確認は `server/stacktest`（build tag
      `stack`）を呼び出す。実 RedmineDocker への到達が前提のため、通常の
      test-unit/test-api/CI には含めない）
- [x] `scripts/backup.sh` / `scripts/restore.sh`（SQLite と secrets、
      restore は確認リテラル必須。対象・保持世代数は docs/Manual.md §4.1）
- [x] ドキュメント同期の最終確認（Design.md / Setup.md / Manual.md /
      README.md が実装と一致しているか。`docs-sync` の観点。監査の詳細は
      自動実行ログ参照）
- [x] `scripts/redmine-seed-testdata.sh` + `.github/workflows/stack-test.yml`
      （Docker デーモンを持つ CI ランナー上で RedmineDocker 開発スタックを
      起動し、REST API 有効化・管理者 API キー確認・テストデータ投入
      （プロジェクト 1 件 + チケット 3 件、冪等）まで自動化した上で
      `scripts/test-stack.sh` を実行。RedmineDocker リポジトリ自体は
      checkout して起動するのみで変更しない。Docker デーモンのない開発
      サンドボックスでは検証できなかった本フェーズの完了条件を、CI
      （`workflow_dispatch` / 週次定期実行 / 関連ファイル変更時の
      push・pull_request）側で自動検証できるようにした）

完了条件: RedmineDocker 開発スタックを起動した状態で
`scripts/test-stack.sh` 緑。`shellcheck scripts/*.sh` 通過。
上記 CI ワークフローの実行結果をもって満たされたとみなす（Docker
デーモンのない開発サンドボックスでは実行・確認できないため）。

## フェーズ 9: チケット詳細のカスタムフィールド表示

目的: Redmine 上で定義された任意のカスタムフィールド（キー・バリュー
リスト／テキスト／バージョン／ファイル／ユーザー／リスト／リンク／小数／
整数／日付／真偽値／長いテキストの 12 フォーマット）を、Redmine の定義
ルール（表示順、必須可否、長さ・上下限、選択肢）に従って `issue-detail`
画面に表示する。編集（入力バリデーション）は対象外——本フェーズは表示のみ。

- [x] `internal/redmine`: `CustomFieldDef` / `PossibleValue` 型と
      `ListCustomFieldDefs`（`GET /custom_fields.json`、
      `customized_type=="issue"` のみ抽出。`possible_values` は
      素の文字列・`{value,label}` オブジェクトの両方を受け付ける）
- [x] `internal/redmine`: `Issue.CustomFields []CustomFieldValue`
      （id/name/value、value は文字列・配列どちらも許容）
- [x] `internal/redmine`: `ListProjectVersions`
      （`GET /projects/{id}/versions.json`）、`ListProjectMemberships`
      （`GET /projects/{id}/memberships.json`）— version/user 参照解決用
- [x] `internal/proxy/allowlist.go` と Design.md §6.2 に
      `GET /custom_fields.json` / `GET /projects/{id}/versions.json` を追加
- [x] `internal/httpapi/aggregate.go`: `/api/issues/{id}/detail` が
      カスタムフィールド値を定義と突合し、`is_required` / `possible_values`
      を添えて返す。`version`/`user`/`attachment` フォーマットは参照先
      （バージョン名・利用者名・添付ファイル名+URL）を解決して
      `display_value` に入れる。定義取得が失敗（403 等、管理者権限なしを
      想定）した場合は必須・選択肢メタなしの生値表示に degrade し、
      詳細取得自体は失敗させない
- [x] `app/js/common/customfields.js`: 12 フォーマットの表示整形を行う
      純粋関数（DOM 非依存、単体テスト可能）
- [x] `app/js/screens/issue-detail.js` / `app/css/screens/issue-detail.css`:
      カスタムフィールドセクションを追加（表示順どおり、必須バッジ、
      選択肢はラベル表示、リンクはアンカー、複数値は読点区切り）
- [x] `make test-e2e`: 疑似上流にカスタムフィールド定義・値を追加し、
      主要フォーマット（テキスト・リスト・日付・真偽値・リンク）の表示を
      実機検証

完了条件: `make test-unit` / `make test-api` 緑（成功 / 未認証 / 不正入力 /
上流障害 / 定義取得 403 degrade のテーブル駆動）。
`node --test app/js/tests/*.test.js` 緑。`make test-e2e` 緑でカスタム
フィールドの表示を実機検証。

---

## 自動実行ログ

定期トリガーによる無人実行の記録。各実行の最後に 1 行追記する
（`.claude/skills/implement/SKILL.md`「Unattended runs」節）。
停滞検知（同一タスクで 2 回連続進捗ゼロ → トリガー無効化）の判定にも
この表を使う。

| 日時 (UTC) | フェーズ | 完了タスク数 | コミット | 結果 |
|---|---|---|---|---|
| 2026-07-19 16:07 | 1 | 7/7 | e37217b〜6dfe12a（タスク 7 + レビュー修正 1） | フェーズ 1 完了。完了条件（test-unit 緑 / config テーブル駆動テスト / 空 DB への migrations 適用）を検証済み。品質ゲート: code-review（medium、ファインダー 2 + 検証）実施、CONFIRMED 7 件修正・3 件は理由付きで見送り |
| 2026-07-20 04:07 | 2 | 8/8 | b6fd4f2〜08dcf36（タスク 8 + レビュー修正 1） | フェーズ 2 完了。完了条件（test-api 緑=成功/未認証/不正入力/上流障害のテーブル駆動、擬似認証器で begin→finish）を検証済み。品質ゲート: code-review（medium、ファインダー 2 + 検証）実施、CONFIRMED 7 件修正（CloneWarning 拒否、チャレンジ/登録コードの一回性アトミック化、XFF 由来のレート制限キー、bootstrap キーの IP 化、重複登録の 4xx 化、last_seen 書き込み間引き）。見送り: レートリミッタの Allow/Fail バースト競合（WebAuthn は暗号で保護・登録コードはアトミック単回化で緩和、恒久対策は将来）／stubVault の成功偽装（credential 保管庫はフェーズ 3）／レートリミッタの map 無制限増加（低速リーク、将来掃除）／RequireAuth ミドルウェア抽出（altitude、将来）／bootstrap の 401 は仕様どおり（409 はフェーズ 3 の保管キー無効化用） |
| 2026-07-20 10:07 | 3 | 5/5 | 4535d72〜b836589（タスク 5 + レビュー修正 1） | フェーズ 3 完了。完了条件（test-api 緑=許可リスト外 404 / ヘッダー拒否・除去 / 401→409 変換、上流は httptest.Server）を検証済み。品質ゲート: code-review（medium、ファインダー 2 + 検証）実施、CONFIRMED 7 件修正（**§9-1 重大**: /my/account.json を中継許可リストから除外＝api_key 漏洩防止／リレーを httputil.ReverseProxy 化＝リダイレクト非追従で API キー再送防止・gzip/応答ヘッダ/chunked/パスエスケープの各バグ解消／APIKey を値レシーバ化で redaction 迂回防止／subURI 検証／readKEK 厳格化／SetRedmineCredentialStatus の空振り検知）。見送り: KEK ローテ後の復号失敗が status=active のまま 500 継続（keyVersion による運用対応、将来）／前段プロキシ由来の上流 401 を credential-invalid 扱い（Design.md §4.4 の仕様どおり） |
| 2026-07-21 04:07 | 5 | 12/12 | フェーズ 5 レビュー修正コミット | フェーズ 5 完了。完了条件（`node --test app/js/tests/*.test.js` 緑=17 件、`make test-e2e` 緑=ブートストラップ登録→パスキーログイン→/api/auth/me 成功を CDP 仮想認証器で自動検証）を検証済み。品質ゲート: code-review（medium、ファインダー 2 + 検証）実施、CONFIRMED 8 件修正（utils.jstMidnight の実行機 TZ 依存で 1 日ずれるバグ／app.js の hashchange・rmappLogout の再ログイン時多重登録＋ログアウト後もルーティングされる不具合／login.js のパスキー非対応時に登録コード・ブートストラップ導線が create を呼ぶのに誤誘導／modal.js のフォーカストラップ未実装＋初期フォーカスが disabled/非表示要素を掴む／e2e fakeRedmine がパス無検証／`--scrim` トークン化で layout.css の色リテラル 2 箇所除去／`login-overlay`→`loginOverlay` の camelCase 統一／initScreen の catch が実装済み画面の実行時エラーも握りつぶす）。TZ 非依存は Honolulu / Tokyo 両 TZ でテスト実行して確認 |
| 2026-07-20 16:07 | 4 | 5/5 | cb971ff〜418707b（タスク 5 + レビュー修正 1） | フェーズ 4 完了。完了条件（test-unit / test-api 緑、Redmine クライアントは httptest.Server のみ、ツリー化は純粋関数の単体テスト）を検証済み。品質ゲート: code-review（medium、ファインダー 2 + 検証）実施、CONFIRMED 7 件修正（ttlCache を per-key ロック化＝1 ユーザーの遅い上流が全体を止めない／ページング重複の解消＝offset は pageSize で進める／集約 401 も MarkInvalid して proxy と挙動統一／resolve のエラー分類＝未連携は 409・一時障害は 500＋slog 記録／バックオフ overflow の上限／context キャンセルの 502 誤分類回避／兄弟ソートの安定化+ID タイブレーク）。見送り（correctness 影響なしの整理）: ttlCache の map エビクション／meta の並列取得／バックオフ中のセマフォ占有／循環復旧の O(n²)／サブ URI 結合・ヘッダー名定数・ツリービルダの三重複 |
| 2026-07-22 04:07 | 6 | 4/6（進行中） | issue-detail 画面 + issuePatch/dueRemainingLabel + E2E | チケット詳細を実装（Design.md §7.8）。ヘッダー（件名＋状態/優先度/トラッカーバッジ）、属性（担当・期日＋残日数・進捗バー）、状態/優先度/進捗のインライン編集（変更項目だけを PUT `/api/redmine/issues/{id}.json`＝許可リスト既存経路）、説明・添付・コメント表示、下部固定コメント欄（下書きを localStorage 保持）。純粋関数 `issuePatch`（差分のみ・notes 常時）と `dueRemainingLabel` をテストファーストで追加（node 29 件緑）。E2E の擬似上流を状態保持化し、状態インライン編集の往復（変更項目のみ送信→保持→バッジ更新）を実機検証（make test-e2e 緑）。全スイート緑（node/unit/api/build/e2e）。担当者編集は memberships 取得が必要なため表示のみ（将来）。フェーズ 6 継続（作成モーダル / settings / 4 状態・a11y が残り）。失敗なし |
| 2026-07-21 22:07 | 6 | 3/6（進行中） | issues 画面 + issuefmt 純粋関数 + E2E | チケット一覧を実装（Design.md §7.7）。Tabulator dataTree の 1 行 2 段組（番号+件名／状態・優先度・担当バッジ）、状態・担当・優先度フィルタ、完了は既定で畳み「完了 N 件」を告知、行タップで詳細へ、作成 FAB（モーダルは次タスクのためプレースホルダ）。純粋関数モジュール `issuefmt`（assigneeLabel / countClosed / pruneIssues / filterOpen / issueBadges / matchIssue）をテストファーストで追加（node 26 件緑）。優先度バッジ CSS を追加。E2E を拡張し擬似上流に issues ツリー・メタを追加、バッジ描画・完了畳み・状態フィルタ展開を実機検証（make test-e2e 緑）。全スイート緑（node/unit/api/build/e2e）。フェーズ 6 継続（issue-detail / モーダル / settings / 4 状態・a11y が残り）。失敗なし |
| 2026-07-21 16:07 | 6 | 2/6（進行中） | openIssues 集約（client/aggregate）+ projects 件数列 + E2E | プロジェクト別 未完了チケット数を実装。redmine.Client.CountOpenIssues（`/issues.json?project_id&status_id=open&subproject_id=!*&limit=1` の total_count）をテストファーストで追加、集約ハンドラが projects/tree の各ノードに `openIssues` を後付け（キー単位で並行取得、上流 401 のみ伝播しその他障害は件数欠測でツリー描画継続）。ProjectNode に `*int openIssues,omitempty`。画面は右端に件数列を追加。E2E の擬似上流に `/issues.json` を追加し件数（基幹=12/社内=8）の描画を実機検証。全スイート緑（node 20 / unit / api / build / e2e）。フェーズ 6 継続（issues / issue-detail / モーダル / settings / 4 状態・a11y が残り）。失敗なし |
| 2026-07-21 10:07 | 6 | 1/6（進行中） | projects 画面 + tree.expandedIdsFor + E2E + app.js 競合修正 | フェーズ 6 に着手。`projects` 画面を実装（dataTree 描画・開閉状態の localStorage 保存・検索時の祖先自動展開・行タップで issues へ・4 状態）。純粋関数 `expandedIdsFor` を追加し単体テスト（node 20 件緑）。E2E を拡張し、集約 API からのツリー描画と検索絞り込みを実機検証（make test-e2e 緑）。E2E で app.js の画面フラグメント二重生成バグ（`loadFragment` の未解決キャッシュ競合）を発見し修正、LESSONS #3・#4 追記。「未完了件数」はサーバー集約が必要なため独立タスクへ分離。フェーズ 6 は継続（issues / issue-detail / モーダル / settings / 4 状態・a11y が残り）。失敗なし |
| 2026-07-22 13:12 | 6 | 6/6（完了） | e2b2c40c〜720ab16（レビュー修正2＋タスク3件＋品質ゲート） | PR #3 を継続。まずレビューコメント3件に対応（enroll.go のコード発行リトライがエラー原因を握りつぶす不具合を修正／tree.js のコメント明確化／go.mod の go ディレクティブは検証の上 1.25.0 を維持＝`go mod tidy` の正規出力と一致、スレッド上で理由を返信）。続けてフェーズ 6 残タスクを実装: (1) チケット作成モーダル（#modal-<key> ルーティングを app.js/modal.js に新設、POST /api/redmine/issues.json）。(2) settings 画面（端末一覧・削除、登録コード発行、Redmine 再紐付け=新規 POST /api/auth/relink＋GET /api/auth/me の redmineStatus、ログアウト）。(3) 全画面 4 状態・a11y 監査（Tabulator の dataTree 行に role=treeitem/aria-level/aria-expanded を付与＝Tabulator 自身が role=tree を role=grid で上書きする不具合も修正、検索/フィルタの空状態に次アクション導線を追加）。E2E をチケット作成・設定画面・error+retry・redmine_credential_invalid→再紐付け→復旧まで拡張（スクリーンショット 10-17）。LESSONS #5（isModalHash が `/` パラメータ付きハッシュを弾く）・#6（e2e で直前のトーストが固定位置のクリック座標を覆う）を追記。品質ゲート: `code-review` スキルはモデル呼び出し不可（disable-model-invocation）のため、フェーズ 6 全差分（6a9a9ad..HEAD）を 2 並列の独立レビューエージェント（サーバー側／フロント側）による発見＋自分での検証に代替。CONFIRMED 5 件修正（enrichOpenCounts がコンテキストキャンセルを1ノードの一時障害と誤扱いし60秒キャッシュへ欠測値を焼き付ける／POST /api/auth/relink のレート制限キーが認証後にも関わらず IP 単位で無関係な同一 IP 利用者を巻き添えにする／Bootstrap.Relink の store エラー未ラップ／モーダルを開いたまま画面遷移した場合に古い fetch が着地後の画面へ重なる競合／`#modal-*` ハッシュへの直接遷移＝リロード時に背景画面が無いままモーダルだけ浮く／issue-detail のコメント下書きが送信成功後も一瞬再表示され二重送信を誘発）。見送り1件（isConstraintErr が PK 衝突と FK 違反を判別できない、理由付き）。完了条件（`node --test` 緑=36件、`make test-e2e` 緑=全画面4状態＋redmine_credential_invalid→再紐付け→復旧を実機検証）を検証済み。フェーズ 6 完了 |
| 2026-07-22 22:46 | 7→8 | 4/4（進行中） | 88ee732〜0ba79a5（新規 PR #5、レビュー修正1、ドキュメント同期1） | オープンな追跡 PR なし、origin/main から claude/plan-phase-8 を新規作成。フェーズ 7（端末紛失対策とセキュリティ強化）はオーナーが直接 main へコミットしたスキップ指示（6515cf8）を反映して状態を「スキップ」に変更し、フェーズ 8 を進行中へ昇格。フェーズ 8 の 4 タスクを実装: (1) ヘルスエンドポイント `GET /healthz`（liveness）・`GET /readyz`（Redmine 到達性。`redmine.Client.Ping` 新設）。依存関係により test-stack.sh より先に着手する計画変更を plan.md に記録。(2) `scripts/test-stack.sh`（起動確認・ヘルスチェック・許可リスト経由の往復 1 件。往復は `server/stacktest`＝build tag `stack`、`credential.NewTestAPIKey` でバウルトを介さず実 API キーを配線）。ローカルの模擬 Redmine でスクリプト全体を実機検証（成功／上流ダウン／API キー未設定の各失敗経路、プロセス後始末を確認）。実 RedmineDocker スタックでの検証はこの環境に docker デーモンがないため未実施（次回、実スタックのある環境での確認が必要）。(3) `scripts/backup.sh` / `scripts/restore.sh`（鍵・設定・DB を tar.gz 化、7 世代保持、restore は確認語 RESTORE 必須）。フィクスチャでバックアップ→破損→復元の往復と世代保持ロジックを実機検証。(4) ドキュメント同期の最終確認: 専用エージェントによる Design.md/Setup.md/Manual.md/README.md の監査を実施し、11 件の実装との不一致を発見・修正（最重要: config.yaml の webroot/secretFile/kekFile が `../../`＝2 階層上参照になっており、Setup.md の手順どおり `cd server && ./bin/rmapp -config config/config.yaml` で起動すると secrets/DB を見失って起動失敗することを実機再現の上 `../` に修正・再検証／存在しない `rmapp serve`/`migrate`/`validate` サブコマンドの記述を実際の単一起動モードに合わせて修正／存在しない `config.example.yaml`・`scripts/healthcheck.sh` の参照を削除／DSN 例の `_fk=1` を実ドライバ modernc.org/sqlite 向け `_pragma=foreign_keys(1)` に修正／Go バージョン記載を 1.22→1.25 に修正／フェーズ 7 スキップに伴い、実装されていない回復コードを Setup.md・Manual.md から削除し Design.md §11.4 に未実装の注記／Design.md に §6.7 運用監視エンドポイントを追記／Manual.md のログ項目・メッセージ表を実際の slog 呼び出しに合わせて全面修正／Manual.md §4 のバックアップ・復元手順を実スクリプトの環境変数・非停止動作に合わせて修正）。GitHub Copilot のレビューコメント3件にも対応（bin/ ディレクトリ未作成での build 失敗懸念→ mkdir -p 追加／stacktest の http.Get がタイムアウトなし→明示的 http.Client{Timeout} 化／Ping のエラーラップが `%v` で原因を chain に残さない→ Go 1.20+ の複数 `%w` で修正、errors.As の回帰テスト追加）。全スイート緑（build/test-unit/test-api/shellcheck/vet 通常・stack タグ）。フェーズ 8 は全タスク完了だが、完了条件のうち実 RedmineDocker スタックでの test-stack.sh 実行が本環境では検証不能なため、状態は「進行中」のまま維持（次回、実スタック環境での確認をもって「完了」に昇格）。失敗なし |
| 2026-07-23 04:11 | 8 | 0/4（進行中・進捗なし） | なし | 追跡 PR #5（claude/plan-phase-8）を継続。origin と作業ブランチの分岐なし。レビュースレッド 3 件はすべて resolved、CI（shellcheck / frontend / server の 3 ジョブ）は全緑で対応不要。フェーズ 8 は 4 タスクとも [x] 済みで現フェーズに未着手タスクがなく、実装ルール（未着手タスクなしに実装しない）により新規実装は行わず。唯一残る完了条件「RedmineDocker 開発スタックを起動した状態での `scripts/test-stack.sh` 緑」を本セッションで再確認したが、`docker version` はクライアントのみ応答し `/var/run/docker.sock` が存在せず dockerd 未起動（前回実行と同一の環境制約、rootless サンドボックスに docker デーモンなし）。フェーズ 8 は「進行中」を維持。次フェーズは地図表示のみでオーナー指示があるまで不着手のため対象外。コミットなし・失敗なし |
| 2026-07-23 10:11 | 8 | 0/4（進行中・進捗なし・停滞検知） | なし | 追跡 PR #5（claude/plan-phase-8）を継続。origin と作業ブランチの分岐なし。CI 6 ジョブ全緑、レビュースレッド 3 件は resolved のまま新規コメントなし。フェーズ 8 は 4 タスクとも [x] 済みで未着手タスクがなく新規実装は行わず。唯一残る完了条件（実 RedmineDocker スタックでの `scripts/test-stack.sh` 実行）を再確認したが、前回同様 `/var/run/docker.sock` が存在せず dockerd 未起動（rootless サンドボックスに docker デーモンなし。この制約はセッション環境に起因し、無人実行側では解消不能）。前回（04:11）と今回の 2 回連続で同一タスク・実装コミットゼロのため、SKILL.md の停滞検知規則に該当。本セッションには定期トリガーを無効化する `update_trigger` 相当のツールが利用可能なツール一覧に存在しなかったため、トリガー自体は無効化できず、PushNotification でオーナーに原因（実 Docker デーモンが必要な完了条件が無人サンドボックスでは検証不能）と再開方法（Docker デーモンのある環境で `scripts/test-stack.sh` を実行し緑を確認のうえ、フェーズ 8 を手動で完了に昇格するか、トリガーの完了条件をこのサンドボックスで検証可能な範囲に見直す）を報告した。コミットなし・失敗なし |
| 2026-07-23 16:13 | 8 | 0/4（進行中・進捗なし・停滞継続） | なし | 追跡 PR #5（claude/plan-phase-8）を継続。origin と作業ブランチの分岐なし。CI 最新実行 success、レビュースレッド 3 件は resolved のまま新規コメントなし。フェーズ 8 は 4 タスクとも [x] 済みで未着手タスクがなく新規実装は行わず。`docker version` を再確認したが本セッションでも `/var/run/docker.sock` が存在せず dockerd 未起動（3 回連続・同一の構造的ブロッカー）。04:11・10:11 に続き今回で 3 回連続進捗ゼロ。前回ログどおり本セッションの利用可能ツールにも `update_trigger` 相当は存在しない（`ToolSearch` で確認済み）ため、トリガーは無効化できず、PushNotification でオーナーに再度報告した。加えて、別件としてオープン PR を確認したところ **PR #6（claude/redmine-custom-fields-display-br8zwb、フェーズ 9）が、フェーズ 8 未完了・フェーズ 7 未着手のまま `docs/plan.md` にフェーズ 9 を新設し着手・完了させていたことを発見**（コミット d2b820c、変更履歴への記載なし＝計画変更の無断実施）。本フェーズの手番ではないため PR #6 へは着手・マージ操作を行わず、オーナー判断を仰ぐため PushNotification で報告のみ行った。コミットなし・失敗なし |

## 変更履歴

| 日付 | 変更 | 理由 |
|---|---|---|
| 2026-07-19 | 初版作成 | 実装開始のための計画策定 |
| 2026-07-19 | フェーズ 5・6 の完了条件を手動確認から `make test-e2e`（chromedp）による自動検証に変更し、フェーズ 5 に E2E ハーネスのタスクを追加 | 無人実行では手動のブラウザ確認が実施できないため |
| 2026-07-19 | 「自動実行ログ」表を新設し、運用ルールに無人実行の参照を追加 | 定期実行の可視化と停滞検知の判定根拠のため |
| 2026-07-20 | フロントの単体テスト起動コマンドを `node --test app/js/tests/` から `node --test app/js/tests/*.test.js` に修正（フェーズ 5・6 完了条件、test スキル、CI） | node v22 は `--test` にディレクトリを渡すと探索でなく実行対象扱いにするため |
| 2026-07-21 | `--scrim` トークンを tokens.css の両モードに追加し、layout.css のドロワー背景・モーダル背後の色リテラルを置換 | hex/色リテラルは tokens.css のみ（CLAUDE.md §3.4）。品質ゲートの指摘対応 |
| 2026-07-21 | フェーズ 6 の `projects` タスクから「未完了件数」を分離し独立タスク化 | 件数はサーバー側の集約追加（`/api/projects/tree` への `openIssues` 付与）が必要で、画面描画タスクとは粒度が異なるため。テストファースト単位を明確化する |
| 2026-07-21 | app.js の画面フラグメント二重生成バグを修正（`loadFragment` を Promise キャッシュ化 + `enterApp` の route 二重呼び出し回避） | フェーズ 6 の projects E2E で発覚。route が並行呼び出しされると未解決キャッシュを二者が見て section を二重生成し、`#projectsTree` が重複していた（フェーズ 5 コードの欠陥） |
| 2026-07-21 | `issues` タスクから「追加読み込み（無限スクロール）」を除外 | 集約 API `/api/projects/{id}/issues/tree` がサーバー側で全ページを結合し全件返すため、クライアント側のページングは不要。Design.md §7.7 のモックアップ上の表現との差分を明示 |
| 2026-07-23 | フェーズ 7（端末紛失対策とセキュリティ強化）の状態を「未着手」から「スキップ（対応しない）」に変更し、フェーズ 8 を進行中に昇格 | オーナー（ryu-karura）がコミット 6515cf8 で本フェーズ対応をスキップする旨を直接指示したため。無人実行はこの指示を計画の状態に反映し、フェーズ 8 へ進む |
| 2026-07-23 | フェーズ 8 のタスク順を変更し「ヘルスエンドポイント」を「`scripts/test-stack.sh`」より先に並べ替え | test-stack.sh はヘルスエンドポイントを叩く前提のため、記載順のまま着手すると依存が逆転する。実装順を依存関係に合わせて明示化 |
| 2026-07-23 | 停滞検知（04:11・10:11 の 2 回連続でフェーズ 8 が同一タスク・実装コミットゼロ）を記録。定期トリガーは無効化できていない | 唯一残る完了条件「実 RedmineDocker スタックでの `scripts/test-stack.sh` 緑」が無人サンドボックス（docker デーモン不在）では検証不能で、無人実行だけでは解消しない構造的ブロッカーのため SKILL.md の停滞検知規則に該当。本セッションの利用可能ツールに定期トリガーを無効化する手段（`update_trigger` 相当）がなく、トリガー自体の無効化はオーナーへの報告・手動対応に委ねる |
| 2026-07-23 | PR #6（フェーズ 9・カスタムフィールド表示）が、フェーズ 7 未着手・フェーズ 8 未完了のまま計画順序を飛ばして着手・完了していたことを記録として残す（変更自体は取り消していない） | コミット d2b820c がフェーズ 9 を `docs/plan.md` に新設した際、本表（変更履歴）への記載がなく、オーナー承認の記録もない。SKILL.md の「フェーズは番号順に進める」「未着手タスクなしに次フェーズへ進まない」「計画変更は必ず変更履歴に理由を記載」に反する無断の計画変更のため、事実関係のみ記録しオーナー判断（PR #6 の扱い・フェーズ番号の整理）を仰ぐ |
| 2026-07-23 | 上記の記載漏れを是正: フェーズ 9（チケット詳細のカスタムフィールド表示）追加の経緯を遡って記録する | オーナー（ryu-karura）からの直接指示（現行の未クローズ PR の次の作業として、Redmine のカスタムフィールド 12 フォーマットを定義ルールどおりに表示する機能追加）を受けて着手した。フェーズ 8（PR #5）は Docker デーモン非搭載サンドボックスでの検証待ちのみで残タスクはなく、フェーズ 7 はオーナー指示によりスキップ済みのため、オーナーの直接指示を計画の明示的な再順序付けとして扱い、フェーズ 9 として並行して着手した |
| 2026-07-23 | PR #6（フェーズ 9）ブランチに `origin/main`（PR #5 マージ後）を取り込み、`docs/plan.md` フェーズ一覧のフェーズ 7・8 状態を main 側の最新値（スキップ／進行中）で採用し、フェーズ 9 の完了行を追加する形に解消 | オーナーから「main とマージして」の直接指示を受けたため。ブランチ作成後に main 側で PR #5（フェーズ 8）が追加した内容（ヘルスエンドポイント、test-stack.sh、backup/restore、フェーズ 7 スキップ注記、複数の自動実行ログ行）と `docs/plan.md`・`server/internal/redmine/client.go` が競合したため、両者の変更を保持する形でマージ解消した |
| 2026-07-23 | フェーズ 8 に「`scripts/redmine-seed-testdata.sh` + `.github/workflows/stack-test.yml`」タスクを追加し完了 | オーナーの直接指示（ブランチ `claude/docker-config-review-ezxi6j`）。フェーズ 8 唯一の残完了条件「実 RedmineDocker スタックでの `scripts/test-stack.sh` 緑」が、Docker デーモンのない無人サンドボックスでは 3 回連続（自動実行ログ 07-23 04:11・10:11・16:13）検証不能で停滞していたため、Docker デーモンを持つ GitHub Actions 上で REST API 有効化・テストデータ投入・`scripts/test-stack.sh` 実行までを自動化する CI ワークフローを追加し、完了条件の検証手段をサンドボックス非依存にした。本セッションでは PR 作成のみを行い、CI 実行結果（stack-test ワークフローの緑）そのものの確認は次回に委ねる |
