# 詳細設計書

本書は、システム全体の構成と設計判断をまとめたものです。
構築の手順は [Setup.md](Setup.md)、日常の操作は [Manual.md](Manual.md) に
分けて記載しています。本書では手順は扱いません。

本プロジェクトは 2 つの既存リポジトリを前提とします。

| リポジトリ | 役割 | 本書での扱い |
|---|---|---|
| [RedmineDocker](https://github.com/ryu-karura/RedmineDocker) | 接続先の Redmine スタック | 前提事実として参照。変更しない |
| [IoTDesignTemplate](https://github.com/ryu-karura/IoTDesignTemplate) | SPA とサーバーの構成テンプレート | 構成・規約を踏襲。差分を明記 |

---

## 1. 目的と前提

### 1.1 目的

RedmineDocker で運用される Redmine を、スマートフォンから快適に参照・更新
できるようにします。ログインは Redmine 自身の認証（OAuth 2.0）に委ね、
`rmapp` は利用者のパスワードにも API キーにも触れません。権限は利用者が同意
したスコープに限定され、Redmine の「マイアカウント」からいつでも取り消せます。

### 1.2 接続先（RedmineDocker）に関する前提事実

- **Redmine 7.0.2 のみを対象とする**（5.x / 6.x 互換は持たない）。サブ URI
  `/redmine` で配信される
  （開発時: `http://localhost:8080/redmine/`）。
- データベースは PostgreSQL 18 + PostGIS 3.6。
- `redmine_gtt` を含む 15 のプラグインがイメージに焼き込み済み。
  **位置情報の基盤は最初から存在します。**
- 本番はホスト Apache が 443 で TLS を終端し、`/redmine` を
  `redmine-web`（127.0.0.1:80）へ転送する二層構成。
- シークレットはファイルベース（`scripts/generate-secrets.sh` →
  `secrets/*.txt` → Docker/Podman secrets）。平文の環境変数や
  コミットは禁止。
- Redmine の REST API は管理画面で有効化が必要。**OAuth 2.0 プロバイダ
  （Doorkeeper）も REST API が無効のあいだは認証を拒否する。**
- OAuth 2.0 プロバイダとしての Redmine（`config/initializers/30-redmine.rb`
  の Doorkeeper 設定より）:
  - 許可するグラントは `authorization_code` のみ（パスワード・クライアント
    資格情報フローは使えない）。
  - リフレッシュトークンが有効。アクセストークンの有効期限は Doorkeeper の
    既定（2 時間）。リフレッシュのたびにリフレッシュトークンは入れ替わる。
  - スコープ = Redmine の権限名（`view_issues` など）と管理者用 `admin`。
    スコープを指定しない認可要求は公開権限のみになる。
  - クライアントシークレットとトークンは DB にハッシュ保存される
    （シークレットは登録直後の 1 回しか表示されない）。
  - **PKCE は強制されない**（`force_pkce` なし）が、`code_challenge` を付けた
    認可は交換時に検証される（実測: 誤った `code_verifier` は `invalid_grant`、
    欠落は `invalid_request`）。`rmapp` は常に S256 で送る。認可コードは
    1 回限り（再利用は `invalid_grant`）。
  - **同意の送信時に Redmine の sudo モード（パスワード再確認）が挟まる**
    （実測。同一 Redmine セッション内の 2 回目以降は省略される）。再入力は
    Redmine の画面内で完結し、`rmapp` はパスワードに触れない。
  - アプリケーションの登録・編集は **Redmine 管理者のみ**。利用者は
    「マイアカウント」で認可済みアプリを確認・取り消しできる。
  - トークン introspection は無効。利用者の特定には
    `GET /users/current.json` を使う。
  - 実機探査の結果は §14（`scripts/redmine-oauth-probe.sh`、CI の
    `Redmine OAuth Probe` ワークフローで再現できる）。

本リポジトリはこのスタックを**変更しません**。接続するだけです。

### 1.3 利用者に関する前提

- 利用者は 1 人で複数の端末（スマートフォンと PC）を併用する。
- 社内ネットワークまたはインターネット越しに HTTPS で公開される。

### 1.4 対象外

- Redmine 本体・RedmineDocker スタックの改造
- オフライン編集
- プッシュ通知（iOS の PWA 制約が大きいため、初期リリースでは扱わない）

---

## 2. 全体構成

### 2.1 構成図

RedmineDocker の二層構成に、本リポジトリの `rmapp` を並べます。
ホスト Apache が振り分けの起点です。

```
client ──443──► ホスト Apache（TLS, HSTS）
                   │
                   ├── /redmine ──► redmine-web（RedmineDocker）
                   │                     │
                   │                     ▼
                   │                redmine-db（PostgreSQL 18 + PostGIS 3.6）
                   │
                   └── /        ──► rmapp :8090（本リポジトリ）
                                       ├─ SPA 静的配信（app/）
                                       ├─ OAuth クライアント（認可コード + PKCE）
                                       ├─ トークン保管庫（暗号化。アクセス + リフレッシュ）
                                       └─ REST API 中継
                                             │ Authorization: Bearer <アクセストークン>
                                             ▼
                                       http://redmine-web/redmine/...（REST API）

   ログイン時のみ: ブラウザ ──► /redmine/oauth/authorize（Redmine のログイン・同意画面）
                  Redmine ──► rmapp /api/auth/callback（認可コード）
```

開発時はホスト Apache を介さず、`rmapp` に直接アクセスします
（`http://localhost:8090/`、Redmine は `http://localhost:8080/redmine/`）。

### 2.2 なぜ中継サーバーを置くのか

SPA から Redmine の REST API を直接叩く構成も成立しますが、次の理由で
採用しません。

| 論点 | 直接続 | 中継あり |
|---|---|---|
| 認証方式 | API キーまたは Basic 認証のみ。OAuth の機密クライアントにできない（クライアントシークレットをブラウザに置けない） | 機密クライアントとして OAuth 2.0 認可コードフローを使える |
| トークンの所在 | ブラウザ内。XSS で漏洩する | サーバー内のみ |
| CORS | Redmine 側で許可設定が必要 | 同一オリジンのため不要 |
| 通信回数 | 画面ごとに複数回 | サーバー側で集約できる |
| 権限の絞り込み | できない | 許可リストで制御できる |

### 2.3 同一オリジン配信

SPA の静的ファイルと API を同じ `rmapp` から、同じオリジンで配信します。
IoTDesignTemplate と同じく、SPA は Go サーバーが配信する前提です
（画面フラグメントを `fetch` で読み込むため、`file://` や単純な静的サーバー
では動作しません）。

これにより次が同時に満たされます。

- CORS の設定が不要になる
- `SameSite=Lax` の Cookie がそのまま使える
- OAuth のリダイレクト URI（`/api/auth/callback`）がアプリの配信元と一致する

テンプレートの `baseURL`（サブパス配信）設定は引き継ぎます。ホスト Apache
配下で `/app` のようなサブパスに置く場合に使います。既定はルート配信です。

---

## 3. 認証設計

### 3.1 方式

Redmine 7 を **OAuth 2.0 認可サーバー**、`rmapp` を**機密クライアント**とする
認可コードフロー（RFC 6749 §4.1）+ PKCE（RFC 7636、`S256`）です。

- 利用者のパスワードは Redmine のログイン画面にだけ入力され、`rmapp` には
  届きません。ブートストラップ用のパスワード受け取り経路は設けません。
- Redmine の API キーは使いません。`/my/account.json` も呼びません。
- `rmapp` が Redmine に登録するのは 1 件だけです（管理者が一度だけ実施。
  Setup.md）。

IoTDesignTemplate から引き継ぐ設計:

- ログイン関連エンドポイントのレート制限（連続 5 回失敗で 60 秒ロック）
- セッションの二軸タイムアウト（アイドル + 絶対）

### 3.2 アプリケーション登録（Redmine 側）

| 項目 | 値 |
|---|---|
| 名前 | `RedminePocketGo`（任意） |
| リダイレクト URI | `<rmapp の公開 URL>/api/auth/callback`（`redmine.oauth.redirectURI` と完全一致） |
| 機密クライアント | はい（Confidential） |
| スコープ | §3.6 の一覧（`redmine.oauth.scopes` と一致させる） |

登録で得た **Client ID** は設定ファイル、**Client Secret** は
`secrets/redmine_oauth_client_secret.txt` に置きます（登録直後の 1 回しか
表示されない）。紛失したら Redmine 側でシークレットを再生成します。

### 3.3 エンドポイント

| メソッド | パス | 内容 |
|---|---|---|
| GET | `/api/auth/login` | 認可要求の開始。`state` と PKCE を生成して保存し、Redmine の `/oauth/authorize` へ 302 する。クエリ `return` は画面ハッシュ（`#projects` 等）の許可リストのみ受け付ける |
| GET | `/api/auth/callback` | Redmine からの戻り。`state` 検証 → コード交換 → 利用者特定 → セッション発行 → SPA へ 302。失敗時は `#login?error=<code>` へ 302 |
| POST | `/api/auth/logout` | セッションを破棄する。**他の端末のセッションが残っていなければ**、トークンを Redmine 側でも失効（`/oauth/revoke`。リフレッシュ → アクセスの順）させ、ローカルの組も削除する。トークンは利用者単位で端末間共有のため、残っている間は失効させない |
| GET | `/api/auth/me` | 現在のセッション情報とトークンの状態を返す（SPA 起動時に呼ぶ） |
| POST | `/api/auth/reauthorize` | トークン無効時の再認可（`/api/auth/login` の URL を返す。SPA が遷移する） |

`/api/auth/login` と `/api/auth/callback` は**ブラウザのページ遷移**で使うため
`X-Requested-With` を要求しない GET です（状態を変えるのは callback のみで、
`state` がその CSRF 対策を兼ねる）。`/api/auth/logout` など POST は従来どおり
`X-Requested-With` 必須です。

`GET /api/auth/me` を SPA 起動時に呼び、未認証ならログイン画面を出す流れは
テンプレートと同一です。

### 3.4 ログインの流れ

```
1. ログイン画面の「Redmine でログイン」を押す → GET /api/auth/login
2. rmapp が state・code_verifier を生成して保存（10 分・1 回限り）、
   Redmine の /redmine/oauth/authorize?response_type=code&client_id=…
   &redirect_uri=…&scope=…&state=…&code_challenge=…&code_challenge_method=S256 へ 302
3. 利用者が Redmine にログイン（未ログインの場合）し、スコープに同意する。
   同意の送信時に Redmine がパスワードの再確認（sudo モード）を求めることが
   ある。いずれも Redmine の画面内の操作で、`rmapp` には何も渡らない
4. Redmine が /api/auth/callback?code=…&state=… へ 302
5. rmapp: state を検証（一致・未使用・期限内。使用済みにする）
6. rmapp → Redmine（サーバー間）: POST /redmine/oauth/token
   grant_type=authorization_code, code, redirect_uri, code_verifier,
   client_id, client_secret → access_token / refresh_token / expires_in / scope
7. rmapp → Redmine: GET /redmine/users/current.json（Bearer）で利用者を特定
8. users を upsert し、トークンを暗号化して保存し、新しいセッションを発行
9. SPA（#projects 等）へ 302
```

異常系:

| 状況 | 挙動 |
|---|---|
| 利用者が同意を拒否（`error=access_denied`） | `#login?error=access_denied`。ログイン画面に理由を表示 |
| `state` 不一致・期限切れ・使用済み | 401 相当として `#login?error=invalid_state`。詳細はログのみ |
| コード交換が失敗 | `#login?error=exchange_failed`。上流が 5xx なら `upstream_error` |
| Redmine の REST API が無効 | 認可要求が拒否される。`#login?error=redmine_unavailable` で管理者への連絡を案内 |
| 利用者が Redmine で無効化・ロック済み | 認可段階で Redmine が拒否する |

### 3.5 セッション

IoTDesignTemplate の二軸タイムアウトを採用しつつ、モバイル用途に合わせて
既定値を延ばし、保存先をデータベースに変えます。

| 項目 | 値 | テンプレートとの差分 |
|---|---|---|
| 保持方法 | Cookie + データベース | テンプレートはインメモリ。リフレッシュトークンは長寿命であり、サーバー再起動で全員ログアウトは受け入れられないため永続化する |
| Cookie 名 | `rmapp_session` | |
| 属性 | `HttpOnly`, `Secure`, `SameSite=Lax`, `Path=/` | |
| アイドルタイムアウト | 既定 168h（7 日） | テンプレートは 30 分 |
| 絶対タイムアウト | 既定 720h（30 日） | テンプレートは 12 時間 |
| 失効 | ログアウト、期限切れ、トークンの無効化 | |
| 固定化対策 | ログイン成功のたびに新しいセッション ID を発行 | |

セッションと OAuth トークンは別の寿命を持ちます。セッションが生きていても
リフレッシュに失敗すれば §4.4 の再認可になります。逆にセッションが切れても
トークンは残り、次回ログイン時に新しい組で置き換わります。

CSRF 対策はテンプレートの方式をそのまま使います。**更新系リクエスト
（POST / PUT / DELETE）には `X-Requested-With: XMLHttpRequest` ヘッダーを
必須とし、無ければ拒否**します（OAuth の GET 遷移は §3.3 のとおり例外）。

### 3.6 スコープ

Redmine のスコープは権限名です。`rmapp` が機能として使うものだけを要求します
（最小権限）。

| 機能 | スコープ（案） |
|---|---|
| プロジェクト・チケット・メタ情報の参照 | `view_project` `view_issues` |
| チケット作成 | `add_issues` |
| チケット更新 | `edit_issues` |
| コメント追加 | `add_issue_notes` |
| メンバーの参照（担当者候補、カスタムフィールド解決） | `view_members`（実測: 無いと `memberships.json` は 403） |
| バージョンの参照（カスタムフィールド解決） | 上記の `view_project` `view_issues` で足りる（実測: 200） |

- `redmine.oauth.scopes` の初期値は
  `view_project view_issues add_issues edit_issues add_issue_notes view_members`
  （実測で全機能が通った組）。スコープを指定しない認可は `view_project` のみに
  なる。`GET /users/current.json` はどのスコープでも通り、応答に `api_key` は
  含まれない（実測）。
- 利用者の実効権限は「アプリのスコープ ∩ 同意したスコープ ∩ プロジェクトの
  ロール」です。スコープ強制は 7.0.2 で有効（実測: 読み取り専用スコープの
  トークンによるチケット更新・コメント追加・作成はすべて 403）。`rmapp` は権限を追加も緩和もしません（§11.2）。
- `admin` スコープは要求しません。したがって `GET /custom_fields.json`
  （管理者専用）は常に 403 になり、§6.4 のとおり生値表示へ degrade します。
- スコープを増やす機能追加は、`redmine.oauth.scopes` の変更と**全利用者の
  再認可**を伴います（Manual.md に手順化する）。

---

## 4. OAuth トークンの管理

### 4.1 紐付けの単位

トークンの組（アクセス + リフレッシュ）は**ユーザー単位で 1 組**保持します。
端末ごとの認証器はなくなり、端末は Cookie セッション（§3.5）でのみ区別
されます。

```
ユーザー（1）  ← Redmine のユーザー ID が鍵
  ├─ セッション（N） ← 端末・ブラウザごとの Cookie
  └─ OAuth トークンの組（1） ← 暗号化して保管。スコープ付き
```

どの端末からアクセスしても、Redmine 上の操作者・更新履歴は同じ人物として
記録されます。新しい端末でログインすると、トークンの組は新しいものに置き換わり、
他の端末のセッションはそのまま新しいトークンを使います。

### 4.2 設定画面に出す情報

| 項目 | 用途 |
|---|---|
| Redmine 連携状態 | 有効 / 要再認可 |
| 付与されたスコープ | 何を許可しているかの確認 |
| 最終リフレッシュ日時 | 連携が生きているかの目安 |
| セッション一覧（任意） | 他の端末のセッションの把握と個別ログアウト（将来） |

### 4.3 暗号化

- 方式は AES-256-GCM。
- 鍵（KEK）はファイルで与えます（`secrets/kek.txt`）。RedmineDocker の
  「シークレットはファイル、平文環境変数やコミットは禁止」の慣例に
  合わせます。
- アクセストークンとリフレッシュトークンを**別々のノンス**で暗号化します。
  ノンスは書き込みのたびに乱数生成します。
- 平文のトークンは、リクエスト処理中のメモリ上にのみ存在します。
- トークンを保持する型は、JSON 化すると必ず `"[redacted]"` になるよう
  実装します。ログ・エラー・レスポンスに出さないことも同様です。
- クライアントシークレットは `secrets/redmine_oauth_client_secret.txt` から
  起動時に読み、メモリ以外に複製しません。

### 4.4 トークンの更新と無効化

アクセストークンは短命（Redmine 既定 2 時間）です。中継の直前に期限を見て、
期限が近ければ（残り 60 秒未満）リフレッシュします。上流が 401 を返した場合も
1 回だけリフレッシュして再試行します。

1. リフレッシュは**ユーザー単位で直列化**する（single-flight）。Redmine は
   リフレッシュのたびにリフレッシュトークンを入れ替え、古いものは即座に
   `invalid_grant` になる（実測）。同じ古いトークンで並行にリフレッシュすると
   連鎖が壊れる。一方、リフレッシュ前の古いアクセストークンは期限まで有効な
   ままなので（実測）、処理中の他リクエストは失敗しない。
2. 新しい組を**先に永続化してから**使う。
3. `invalid_grant`（利用者が Redmine で取り消した、管理者がアプリを削除した、
   ローテーションを取りこぼした）の場合は組を「無効」としてマークする。
4. SPA には `code: "redmine_credential_invalid"`（409）を返す。
5. SPA は再認可画面を表示し、`POST /api/auth/reauthorize` が返す URL へ
   遷移する。Redmine 側のセッションが生きていれば同意だけで戻れる。
6. 再認可に成功したら新しい組を保存する。

リフレッシュの一時的な失敗（接続エラー、5xx）は組を無効化せず、502
`upstream_error` を返します。

ログアウト時は、他の端末のセッションが残っていない場合に限り
`POST /redmine/oauth/revoke`（リフレッシュ + アクセス）をベストエフォートで呼び、
ローカルの組も削除します。Redmine に届かなくてもログアウトは完了させます
（その場合の取り消しは Redmine の「マイアカウント」から行えます）。

---

## 5. データモデル

`rmapp` が持つデータベースです。SQLite を使用します。Redmine のデータは
複製しません。

### 5.1 users

| カラム | 型 | 内容 |
|---|---|---|
| id | TEXT (UUID) | 主キー |
| redmine_user_id | INTEGER | Redmine のユーザー ID。一意。同一性の鍵 |
| redmine_login | TEXT | Redmine のログイン名（ログインのたびに更新） |
| display_name | TEXT | 表示名（ログインのたびに更新） |
| created_at / updated_at | TIMESTAMP | |

ログイン名は Redmine 側で変更されうるため、一意性の鍵には使いません。

### 5.2 oauth_tokens

| カラム | 型 | 内容 |
|---|---|---|
| user_id | TEXT | 主キー。users.id への外部キー |
| access_ciphertext / access_nonce | BLOB | 暗号化されたアクセストークンとそのノンス |
| refresh_ciphertext / refresh_nonce | BLOB | 暗号化されたリフレッシュトークンとそのノンス |
| key_version | INTEGER | 鍵のローテーション世代 |
| scopes | TEXT | 付与されたスコープ（空白区切り） |
| access_expires_at | TIMESTAMP | アクセストークンの有効期限 |
| status | TEXT | `active` / `invalid` |
| refreshed_at | TIMESTAMP | 最後にリフレッシュまたは発行した時刻 |

### 5.3 sessions

| カラム | 型 | 内容 |
|---|---|---|
| id | TEXT | セッション ID のハッシュ。主キー |
| user_id | TEXT | users.id への外部キー |
| created_at / last_seen_at | TIMESTAMP | アイドルタイムアウトの判定に使用 |
| absolute_expires_at | TIMESTAMP | 絶対タイムアウト |

セッション ID は生の値を保存せず、ハッシュのみを保存します。

### 5.4 oauth_states

進行中の認可要求の状態を保持します。有効期限は 10 分、1 回限り。期限切れは
定期的に削除します。

| カラム | 型 | 内容 |
|---|---|---|
| state_hash | TEXT | `state` のハッシュ。主キー |
| code_verifier_ciphertext / nonce | BLOB | PKCE の `code_verifier`（暗号化） |
| return_to | TEXT | ログイン後に戻る画面ハッシュ（許可リスト済み） |
| expires_at | TIMESTAMP | 発行から 10 分 |
| used_at | TIMESTAMP | 使用済みなら非 NULL |

### 5.5 廃止されるテーブル

`credentials`（パスキー）、`redmine_credentials`（API キー）、
`enrollment_codes`、`webauthn_challenges`。マイグレーションで削除します。
**API キーの暗号文は削除時に残さない**（DROP のみ。バックアップ世代は
Manual.md のとおり別途廃棄）。既存利用者は初回の OAuth ログインで
`redmine_user_id` を埋めて既存の users 行に紐付けます（ログイン名一致）。

---

## 6. API 設計

### 6.1 方針

SPA が呼ぶ API は、Redmine の REST API をそのまま透過させるのではなく、
画面が必要とする形に整えて返します。透過が適切なものだけを中継します。

上流のパスはすべて `redmine.base_url` + サブ URI `/redmine` の配下です。
サブ URI は設定から与え、コードにハードコードしません。

### 6.2 中継の許可リスト

`(メソッド, パス)` の組を明示的に列挙し、一致しないリクエストは 404 を
返します。前方一致による一括透過は行いません。

透過中継の入口は `/api/redmine/` 配下です。SPA は `/api/redmine/<API パス>`
を呼び、サーバーが許可リストで検査してから
`<redmine.baseURL>` + `<redmine.subURI>` + `<API パス>` へ中継します
（例: `GET /api/redmine/issues.json` → `…/redmine/issues.json`）。

| メソッド | パス（`/redmine` 配下） | 用途 |
|---|---|---|
| GET | `/projects.json` | プロジェクト一覧 |
| GET | `/projects/{id}.json` | プロジェクト詳細 |
| GET | `/issues.json` | チケット一覧 |
| GET | `/issues/{id}.json` | チケット詳細 |
| PUT | `/issues/{id}.json` | チケット更新 |
| POST | `/issues.json` | チケット作成 |
| GET | `/issue_statuses.json` | ステータス一覧 |
| GET | `/trackers.json` | トラッカー一覧 |
| GET | `/enumerations/issue_priorities.json` | 優先度一覧 |
| GET | `/projects/{id}/memberships.json` | 担当者候補、カスタムフィールド（ユーザー形式）の参照解決 |
| GET | `/projects/{id}/versions.json` | カスタムフィールド（バージョン形式）の参照解決 |
| GET | `/attachments/{id}.json` | 添付情報、カスタムフィールド（ファイル形式）の参照解決 |
| GET | `/custom_fields.json` | カスタムフィールド定義（表示順・必須・長さ/上下限・選択肢）。管理者権限が必要な上流仕様のため、非管理者アカウントでは 403 になりうる——§6.4 のとおり集約側で degrade する |

管理系（ユーザー作成、グループなど）は列挙しません。

`/my/account.json` は**中継せず、サーバー内部でも呼びません**。応答本文に
Redmine の `api_key` が含まれ、API キー禁止（CLAUDE.md §9-1）に反するため
です。利用者の特定には `GET /users/current.json`（Bearer）を、OAuth フローの
内部処理（`internal/redmine`）からのみ呼びます。許可リストには載せません。

### 6.3 ヘッダーの取り扱い

| ヘッダー | 扱い |
|---|---|
| `X-Redmine-API-Key` | サーバーも付与しない。受信したら 400 で拒否 |
| クエリ `key=` | Redmine が API キーとして解釈しうるため、受信したら 400 で拒否（名前は大文字小文字を区別しない） |
| `Authorization` | 受信したものは Redmine へ転送しない。サーバーが `Bearer <利用者のアクセストークン>` を付与する |
| `Cookie` | Redmine へ転送しない |
| `X-Redmine-Switch-User` | 受信・送信ともに禁止 |
| `X-Requested-With` | 更新系で必須（CSRF 対策。テンプレートの慣例） |

### 6.4 集約エンドポイント

画面の初期表示に必要なデータをまとめて返します。ツリー化のための
親子解決はサーバー側で全ページを取得してから行います。

| メソッド | パス | 内容 |
|---|---|---|
| GET | `/api/projects/tree` | 全プロジェクトを親子構造に整形して返す |
| GET | `/api/projects/{id}/issues/tree` | チケットを親子構造に整形して返す |
| GET | `/api/issues/{id}/detail` | チケット本体、履歴、添付、選択肢を 1 回で返す |
| GET | `/api/meta` | トラッカー、ステータス、優先度をまとめて返す |

ステータスなどの表示名をフロントに直書きせず、サーバーから取得した
マスタで解決する方針は、テンプレートの status-master の考え方
（表示名のハードコード禁止）を踏襲したものです。

#### カスタムフィールドの解決（`/api/issues/{id}/detail`）

チケットのカスタムフィールド値（`custom_fields`）は、Redmine 上の定義
（`GET /custom_fields.json`）と `id` で突合してから返します。フロントは
届いた配列の順序をそのまま表示順として使い（Redmine が既にトラッカーの
表示順で値を返すため、サーバー側で並べ替えません）、`is_required` /
`possible_values` / `min_length` / `max_length` を添えて返すことで、SPA が
Redmine の定義ルール（必須可否・選択肢・長さ/上下限）を再実装せずに
参照できるようにします。

`version` / `user` / `attachment`（ファイル）フォーマットは、格納されて
いる値が参照先の ID のみのため、該当チケットのプロジェクトの
バージョン一覧・メンバー一覧、または添付情報を追加取得して
`display_value` に人が読める名前を解決します。

`GET /custom_fields.json` は上流仕様上、管理者権限が必要です
（Redmine の隠れた制約であり、本リポジトリが権限を追加/緩和するわけでは
ありません。§11.2 のとおり Redmine の権限をそのまま反映します）。
非管理者アカウントで 403 になった場合は、定義なし（必須表示・選択肢
ラベル解決なし）の生値表示に degrade し、チケット詳細の取得自体は
失敗させません。

### 6.5 エラー表現

```json
{ "error": { "code": "redmine_credential_invalid", "message": "..." } }
```

| code | HTTP | 意味 |
|---|---|---|
| `unauthenticated` | 401 | セッションがない、または期限切れ |
| `forbidden` | 403 | 権限がない |
| `not_found` | 404 | 対象がない、または許可リスト外 |
| `invalid_request` | 400 | 入力が不正 |
| `redmine_credential_invalid` | 409 | OAuth トークンが無効（リフレッシュ失敗・取り消し）。再認可が必要（§4.4） |
| `upstream_error` | 502 | Redmine 側の障害 |
| `rate_limited` | 429 | 呼び出し過多 |
| `internal_error` | 500 | サーバー内部エラー（パニック回復時など） |

`message` は開発者・ログ向けです。利用者に見せる文言は SPA が `code` から
決定します。

### 6.6 キャッシュ

| 対象 | 方針 |
|---|---|
| トラッカー・ステータス・優先度 | サーバー側で 10 分キャッシュ |
| プロジェクトツリー | サーバー側で 60 秒キャッシュ（ユーザー単位） |
| チケット一覧・詳細 | キャッシュしない |

キャッシュは必ずユーザー単位で分離します。なお、テンプレートには全レスポンス
にキャッシュ抑止ヘッダーを付与する `noCache` 設定があり、これも引き継ぎます
（上記はサーバー内部のキャッシュであり、ブラウザキャッシュとは別です）。

### 6.7 運用監視エンドポイント

`/api/` 配下や `baseURL` の対象外。認証不要で常にルート直下に配信します
（Setup.md §11）。

| メソッド | パス | 内容 |
|---|---|---|
| GET | `/healthz` | プロセスが HTTP に応答できるかのみを見る（liveness）。依存先の障害と無関係 |
| GET | `/readyz` | Redmine への到達性を確認する（readiness）。上流に接続できなければ 503 |

---

## 7. 画面設計

### 7.1 SPA の構成（IoTDesignTemplate 踏襲）

- **フレームワークなし・ビルドなしの Vanilla JS（ES モジュール）。**
- **ハッシュルーティング。** `app/js/app.js` の `SCREENS` マニフェストが
  全画面の唯一の一覧です（`key`、`label`、`init` 関数）。
- 画面のマークアップは `app/screens/<key>.html` のフラグメントとして持ち、
  起動時に `<main id="screens">` 配下の
  `<section data-screen="<key>" class="screen">` に読み込みます。
- 共通シェル（`index.html`）はトップバー・ナビゲーション・トースト置き場
  のみを持ちます。
- モーダルはハッシュ（`#modal-<key>`）で開閉します。
- 外部ライブラリは `app/js/vendor/` に同梱し、CDN を使いません。

**テンプレートとの差分:**

| 項目 | テンプレート | 本プロジェクト |
|---|---|---|
| ナビゲーション | サイドバー中心（3 メニューグループ） | モバイルファースト。狭い画面はドロワー、画面遷移は階層型（プロジェクト → チケット → 詳細）で、戻るはハッシュ履歴 |
| Chart.js | ダッシュボードで使用 | 使用しない（同梱もしない） |
| Tabulator | テーブル全般 | 引き続き使用。ツリーは Tabulator の dataTree 機能で描画 |
| TypeScript / ビルド | なし | 同じくなし |

### 7.2 画面の一覧

| key | 画面 | 内容 |
|---|---|---|
| `login` | ログイン | 「Redmine でログイン」（OAuth 認可へ遷移）。失敗理由の表示 |
| `projects` | プロジェクト一覧 | 親子ツリー |
| `issues` | チケット一覧 | 親子ツリー、フィルタ |
| `issue-detail` | チケット詳細 | 属性、説明、添付、コメント |
| `settings` | 設定 | Redmine 連携状態と再認可、付与スコープ、テーマ、ログアウト |

### 7.3 デザイントークン

**IoTDesignTemplate の `app/css/tokens.css`（オーシャンブルー、
ライト/ダーク 2 モード）をそのまま持ち込みます。** hex を直書きしてよいのは
このファイルだけ、コンポーネント CSS と画面 CSS は必ず `var(--xxx)` を参照、
という規約も同じです。

持ち込む主なトークン（名称・値ともテンプレートに従う）:

| 分類 | トークン |
|---|---|
| 面と文字 | `--bg` `--surface` `--surface-2` `--fg` `--muted` |
| 枠線 | `--border` `--border-strong` |
| 主要色 | `--primary` `--primary-hover` `--primary-soft` `--on-primary` `--accent` |
| 状態色（両モード共通の固定色） | `--ok` `--warn` `--crit` と各 `-soft` |
| スケール | `--space-1..5` `--fs-*` `--radius-*` `--shadow-*` |

テーマ切替もテンプレートと同一です: `<html>` の `dark` クラス、
localStorage キー `theme`、初回描画前にテーマを当てるインラインスクリプト
（FOUC 防止。唯一許可されるインラインスクリプト）。

**本プロジェクトで tokens.css に追加するトークン**（ライト/ダーク両方で定義）:

| トークン | 用途 |
|---|---|
| `--depth-1` 〜 `--depth-5` | ツリー階層を示す左端の縦線の色。深さで循環 |
| `--status-new` | 新規ステータスのバッジ（`--primary` 系から派生） |
| `--status-open` | 進行中ステータスのバッジ |
| `--status-closed` | 完了ステータスのバッジ（`--ok` 系から派生） |

### 7.4 色の意味付け

「色分けしてきれいに見やすく」の要件を、次の割り当てで実現します。
テンプレートの規約どおり、**色だけに意味を持たせず、必ずアイコンまたは
文言を併記**します。

#### チケットステータス

Redmine のステータスは自由に定義できるため、固定の対応表は持ちません。
`is_closed` フラグと表示順から割り当てます。

| 区分 | トークン | 表示 |
|---|---|---|
| 新規（最初のステータス） | `--status-new` | 塗りつぶしバッジ ○付き |
| 進行中（中間） | `--status-open` | 塗りつぶしバッジ ●付き |
| 完了（`is_closed`） | `--status-closed` | 枠線バッジ ✓付き、文字は淡色 |

#### 優先度

| 優先度 | トークン | 表示 |
|---|---|---|
| 低 | `--muted` | 下向き矢印 |
| 通常 | — | 表示なし |
| 高 | `--warn` | 上向き矢印 |
| 急いで・今すぐ | `--crit` | 二重の上向き矢印 |

#### 期日

| 状態 | トークン |
|---|---|
| 余裕あり | `--muted` |
| 7 日以内 | `--warn` |
| 超過 | `--crit` |

#### 階層

ツリーの各行は、左端の縦線の色（`--depth-N`）とインデントで深さを示します。

### 7.5 ログイン画面

```
┌──────────────────────────┐
│        ロゴ                │
│   ┌──────────────────┐   │
│   │  Redmine でログイン  │   │  ← --primary の主要ボタン
│   └──────────────────┘   │
│   Redmine の画面で認証します。     │
│   パスワードはこのアプリに         │
│   送られません。                │
└──────────────────────────┘
```

- ボタンは `fetch` ではなく**ページ遷移**（`GET /api/auth/login`）です。
- 同意拒否・`state` 不一致などで戻った場合は、`#login?error=<code>` の
  コードに応じた説明をボタン直下にインライン表示します（§3.4 の表）。
  トーストは補助的な通知にのみ使います。
- 押下後は遷移完了までボタンをローディング表示にし、多重押下を防ぎます。
- トークン無効（`redmine_credential_invalid`）では同じ画面ではなく、再認可の
  案内（「Redmine との連携が切れました」＋再認可ボタン）を出します。

### 7.6 プロジェクト一覧

```
┌──────────────────────────┐
│ ≡  プロジェクト        🔍 ⚙ │  ← トップバー固定
├──────────────────────────┤
│ ▼ ┃ 基幹システム         12 │
│   ┃  ▼ ┃ 会計モジュール    5 │
│   ┃    ┃  ・ 帳票          2 │
│   ┃  ▶ ┃ 在庫モジュール    3 │
│ ▶ ┃ 社内インフラ          8 │
│ ・ ┃ 総務                 1 │
└──────────────────────────┘
```

- Tabulator の dataTree で親子ツリーを描画します（`js/common/table.js` の
  ラッパー経由。テンプレートの規約どおり手書き `<table>` は禁止）。
- 開閉状態は localStorage に保存し、次回に引き継ぎます。
- 右端の数字は未完了チケット数。
- 検索は名前の部分一致。絞り込み中は該当行の祖先を自動展開します。
- `role="tree"` / `role="treeitem"`、`aria-expanded`、`aria-level` を付与
  します。

### 7.7 チケット一覧

```
┌──────────────────────────┐
│ ←  会計モジュール       🔍 ⚙ │
├──────────────────────────┤
│ [状態▾] [担当▾] [優先度▾]    │  ← フィルタ行、横スクロール
├──────────────────────────┤
│ ▼ ┃ #1024 帳票出力の刷新     │
│   ┃ ●進行中  ↑高  山田       │
│   ┃  ・ #1031 PDF 出力       │
│   ┃    ○新規  担当なし        │
│ ・ ┃ #1040 締め処理の高速化    │
│   ┃ ✓完了  通常  佐藤        │
└──────────────────────────┘
```

- 親子ツリーは同じく Tabulator dataTree。1 行 2 段組
  （上段: 番号 + 件名、下段: バッジ類）。
- 完了チケットは既定で折りたたみ、件数のみ表示します。
- 下端で追加読み込み。右下にチケット作成のフローティングボタン。

### 7.8 チケット詳細

```
┌──────────────────────────┐
│ ←  #1024                ⋮ │
├──────────────────────────┤
│ 帳票出力の刷新               │
│ ●進行中   ↑高   バグ         │
├──────────────────────────┤
│ 担当者    山田 太郎           │
│ 期日      2026-08-15   残9日 │
│ 進捗      ████████░░  60%   │
├──────────────────────────┤
│ 説明 / 添付 (2) / コメント (5) │
└──────────────────────────┘
```

- 属性の編集はその場で行い、変更した項目だけを送信します。
- 期日は残り日数を併記し、超過時は `--crit` で表示します。
- 画面下部に固定のコメント入力欄を置きます。
- 通信中は該当箇所のみをローディング表示にします。

#### カスタムフィールドの表示

属性の下にカスタムフィールドの一覧を、Redmine が返す順序（トラッカーの
表示順）のまま表示します。必須項目には「必須」バッジを添えます。
本フェーズは表示のみを対象とし、編集（入力バリデーション）は対象外です。

| フォーマット | 表示 |
|---|---|
| テキスト（`string`） | そのままテキスト表示 |
| 長いテキスト（`text`） | 改行を保持した複数行表示（`white-space: pre-wrap`） |
| 整数（`int`） | 数値としてそのまま表示 |
| 小数（`float`） | Redmine が返す文字列表現をそのまま表示 |
| 日付（`date`） | `YYYY-MM-DD` 表示 |
| 真偽値（`bool`） | 「はい」/「いいえ」 |
| リスト（`list`） | 定義の `possible_values` で値をラベルに解決して表示。複数選択はカンマ区切り |
| キー・バリュー リスト（`key_value_list`） | リストと同様に `possible_values` でラベル解決 |
| バージョン（`version`） | プロジェクトのバージョン一覧から名称を解決して表示 |
| ユーザー（`user`） | プロジェクトのメンバー一覧から利用者名を解決して表示 |
| リンク（`link`） | クリック可能なアンカー（`target="_blank" rel="noopener"`） |
| ファイル（`attachment`） | 添付情報を解決し、ファイル名で表示（属性欄の添付一覧と同様、リンク化はしない） |

`version` / `user` / `attachment` は参照解決に失敗した場合（削除済み・
権限不足など）、生の値（ID）を代替表示します。

### 7.9 設定画面

- Redmine 連携の状態（有効 / 要再認可）、付与されたスコープ、最終リフレッシュ
- 再認可ボタン（`POST /api/auth/reauthorize` → 返された URL へ遷移）
- テーマ（ライト / ダーク。テンプレートのトップバー切替を踏襲）
- ログアウト（Redmine 側のトークンも失効させる）

### 7.10 共通の振る舞い

すべての一覧・詳細画面は、次の 4 状態を明示的に持ちます。

| 状態 | 表示 |
|---|---|
| 読み込み中 | 骨組み表示（スケルトン） |
| 空 | 説明文と、次にとるべき操作への導線 |
| エラー | 原因の説明と再試行ボタン |
| 表示 | 通常のコンテンツ |

その他の共通規約（テンプレート踏襲）: 要素 ID は camelCase、画面キーと
CSS クラスは kebab-case、日時は ISO8601 + 明示的タイムゾーン（`+09:00`）、
操作要素には `aria-label`、タッチ領域は 44×44px 以上。

---

## 8. フロントエンド実装構成

```
app/
├── index.html        共通シェル
├── screens/          画面フラグメント（login.html, projects.html, ...）
├── js/
│   ├── app.js        SCREENS マニフェスト、ルーティング、起動処理
│   ├── common/
│   │   ├── shell.js  ナビ生成、ドロワー、テーマ、ログイン・再認可オーバーレイ
│   │   ├── api.js    fetch ラッパー。X-Requested-With 付与。fetch 直呼び禁止
│   │   ├── table.js  Tabulator ラッパー（dataTree 対応に拡張）
│   │   ├── tree.js   フラット配列 → ツリー変換の純粋関数。DOM 禁止
│   │   ├── modal.js  ハッシュ連動モーダル
│   │   └── utils.js  日付・書式ヘルパー
│   ├── screens/      画面ごとの init（login.js, projects.js, ...）
│   └── vendor/       Tabulator 6（将来 MapLibre GL JS）。ライセンス同梱
└── css/
    ├── tokens.css    テンプレート由来 + 本プロジェクト追加トークン
    ├── base.css / layout.css
    ├── components/   badge.css, tree.css, form.css, button.css, ...
    ├── screens/      画面固有のみ
    └── vendor/       Tabulator ベース CSS
```

ブラウザに保存してよいもの:

| データ | 保存先 | 可否 |
|---|---|---|
| セッション | Cookie（サーバー管理） | 可 |
| OAuth トークン・API キー・クライアントシークレット | — | 不可。ブラウザに置かない |
| テーマ / ツリー開閉 / フィルタ | localStorage | 可 |
| コメント下書き | localStorage | 可（ログアウト時に消去） |

---

## 9. サーバー実装構成

IoTDesignTemplate の `server/` の構成（`cmd/` + `internal/` +
`config/*.yaml`）を踏襲し、責務を置き換え・追加します。

| パッケージ | 責務 | テンプレートとの関係 |
|---|---|---|
| `config` | config.yaml の読み込みと検証 | 踏襲 |
| `auth` | OAuth ログイン（authorize / callback）、`state`・PKCE、セッション、レート制限 | パスワード認証を OAuth 2.0 認可コードフローに置換。レート制限・タイムアウト設計は踏襲 |
| `credential` | OAuth トークン（アクセス + リフレッシュ）の暗号化保管、ユーザー単位 single-flight リフレッシュ | 新規 |
| `proxy` | 中継、許可リスト、ヘッダー制御 | 新規 |
| `redmine` | 型付き Redmine クライアント（OAuth の authorize URL 構築・トークン交換・失効を含む）、集約、ツリー化 | 新規（datasource に相当） |
| `httpapi` | ハンドラ、ミドルウェア、エラー表現 | 踏襲 |
| `store` | SQLite 永続化 | 新規（テンプレートはインメモリ） |
| `webfs` | 静的アセット配信 | 踏襲 |

**Go バージョンに関する差分**: テンプレートは組み込み機器へのデプロイの
ため Go 1.17 互換を維持していますが、本サーバーはその対象外であり、
依存ライブラリの要件もあるため **Go 1.25 以降**とします。OAuth クライアントは
標準ライブラリ（`net/http`）で実装し、`golang.org/x/oauth2` は使いません
（ユーザー単位 single-flight とローテーションの先行永続化を自前で制御する
ため）。

Redmine への接続:

- 専用の `http.Client` にタイムアウトを設定します。
- 一時的な失敗（接続エラー、502、503）に限り指数バックオフで最大 2 回
  再試行します。4xx は再試行しません。
- 同時接続数に上限を設けます。

ログは `log/slog` の構造化ログ。ボディ、Cookie、セッション ID、OAuth トークン、
認可コード、`state`、PKCE の `code_verifier`、クライアントシークレットは記録
しません。

---

## 10. 設定項目

設定は `server/config/config.yaml` に集約します（コメントは日本語）。
起動時に一度だけ読み込み、その場で検証します。必須項目が欠けている場合は、
該当するキー名を示して起動を中止します。

優先順位は、コマンドライン引数 > 環境変数（接頭辞 `RMAPP_`）> 設定ファイル
> 既定値です。

キーの命名と考え方は IoTDesignTemplate の `config.yaml`
（`listen` / `baseURL` / `serveStatic` / `noCache` / `session.*` /
`logLevel`）に合わせ、本プロジェクト固有の節を追加します。

各項目の具体的な設定方法は [Setup.md](Setup.md)、運用中の変更手順は
[Manual.md](Manual.md) を参照してください。

### 10.1 サーバー（テンプレート踏襲）

| キー | 必須 | 既定値 | 内容 |
|---|---|---|---|
| `listen` | | `:8090` | 待ち受けアドレス。Redmine 開発スタックの 8080 と重ならない値 |
| `baseURL` | | 空 | サブパス配信する場合のパス。空でルート配信 |
| `webroot` | | `../app` | SPA の配置先。`server/` をカレントディレクトリとして起動する前提 |
| `serveStatic` | | `true` | SPA を本サーバーから配信するか |
| `noCache` | | `true` | キャッシュ抑止ヘッダーを付与するか |
| `logLevel` | | `info` | `debug` / `info` / `warn` / `error` |
| `trustedProxies` | | `127.0.0.0/8`, `::1/128` | `X-Forwarded-For` を信用する直接の接続元（CIDR または単一 IP の配列。環境変数ではカンマ区切り）。接続元がこの一覧に含まれるときだけ、ヘッダー最右の値をレート制限のクライアント IP とする。含まれなければヘッダーは無視し、接続元アドレスを使う。`[]` で信用しない。偽装ヘッダーによるレート制限の回避・他人の IP のロックを防ぐ |

### 10.2 セッション（テンプレートの二軸方式）

| キー | 必須 | 既定値 | 内容 |
|---|---|---|---|
| `session.idleTimeoutHours` | | `168` | 操作がない場合の失効（7 日） |
| `session.absoluteTimeoutHours` | | `720` | 絶対失効（30 日） |
| `session.secureCookie` | | `true` | Secure 属性。localhost 検証時のみ false |
| `session.cookieName` | | `rmapp_session` | Cookie 名 |
| `session.secretFile` | ✓ | | 署名鍵ファイル（`secrets/session_key.txt`） |

### 10.3 OAuth（本プロジェクト固有）

| キー | 必須 | 既定値 | 内容 |
|---|---|---|---|
| `redmine.oauth.clientId` | ✓ | | Redmine に登録したアプリケーションの Client ID |
| `redmine.oauth.clientSecretFile` | ✓ | | Client Secret のファイル（`secrets/redmine_oauth_client_secret.txt`）。空ファイルは起動時エラー |
| `redmine.oauth.redirectURI` | ✓ | | `<rmapp の公開 URL>/api/auth/callback`。Redmine の登録値と**完全一致**させる |
| `redmine.oauth.scopes` | ✓ | | 要求するスコープの一覧（§3.6）。Redmine の登録値と一致させる |
| `redmine.oauth.stateTTLMinutes` | | `10` | `state`・`code_verifier` の有効期間 |
| `redmine.oauth.refreshSkewSeconds` | | `60` | 期限の何秒前からリフレッシュするか |

`redirectURI` を変えるときは Redmine 側の登録も同時に更新します。
`webauthn.*` と `features.passwordBootstrap` は廃止されました。

### 10.4 暗号化（本プロジェクト固有）

| キー | 必須 | 既定値 | 内容 |
|---|---|---|---|
| `crypto.kekFile` | ✓ | | OAuth トークン暗号化鍵ファイル（`secrets/kek.txt`） |
| `crypto.keyVersion` | | `1` | 鍵のローテーション世代 |

`kek` を失うと、保存済みのトークンはすべて復号できません（全利用者が再ログインすれば復旧します）。

### 10.5 Redmine（本プロジェクト固有）

| キー | 必須 | 既定値 | 内容 |
|---|---|---|---|
| `redmine.baseURL` | ✓ | | サーバー間通信（トークン交換・REST API）の起点 URL（開発: `http://localhost:8080`） |
| `redmine.publicBaseURL` | | `redmine.baseURL` | **ブラウザ**から見える Redmine の起点 URL。`/oauth/authorize` への遷移に使う。コンテナ内部名で接続し公開 URL が別になる本番で必須 |
| `redmine.subURI` | | `/redmine` | サブ URI。RedmineDocker の `REDMINE_SUBURI` と一致させる |
| `redmine.timeoutSeconds` | | `10` | 1 リクエストのタイムアウト |
| `redmine.maxRetries` | | `2` | 再試行回数 |
| `redmine.maxConcurrency` | | `8` | 同時接続数の上限 |
| `redmine.pageSize` | | `100` | 一覧取得時の 1 ページ件数 |

### 10.6 データベース（本プロジェクト固有）

| キー | 必須 | 既定値 | 内容 |
|---|---|---|---|
| `database.dsn` | ✓ | | SQLite（`modernc.org/sqlite`）の接続文字列（例: `file:data/rmapp.db?_pragma=foreign_keys(1)`） |

### 10.7 機能の有効化

| キー | 必須 | 既定値 | 内容 |
|---|---|---|---|
| `features.mapEnabled` | | `false` | 地図機能（将来） |
| `features.issueCreate` | | `true` | チケット作成の可否 |

### 10.8 シークレットの扱い

RedmineDocker の慣例に合わせ、シークレットは**ファイルで持ちます**。
`scripts/generate-secrets.sh` が `secrets/session_key.txt` と
`secrets/kek.txt` を生成し、`secrets/redmine_oauth_client_secret.txt` は
空の置き場所だけを作ります（mode 600、git 管理外。値は Redmine が発行する
ため生成できない）。設定ファイルには
値そのものではなくファイルパスだけを書きます。

---

## 11. セキュリティ設計

### 11.1 通信

- HTTPS を必須とします（`Secure` Cookie と OAuth のリダイレクト URI の要件）。本番はホスト Apache が TLS を
  終端し HSTS を付与します（RedmineDocker と同じ二層構成の流儀）。
- Content-Security-Policy を設定し、`script-src 'self'` とします。
  インラインスクリプトは FOUC 防止の 1 本のみをハッシュ指定で許可します。

### 11.2 権限

Redmine 上の権限がそのまま反映されます（実効権限は §3.6 のとおり
スコープとロールの積）。中継サーバーは権限を追加も緩和もしません。利用者が見られないプロジェクトは Redmine が返さないため一覧にも
現れません。

### 11.3 想定する脅威と対策

| 脅威 | 対策 |
|---|---|
| XSS によるトークン窃取 | ブラウザにトークンを置かない。CSP |
| セッション窃取 | HttpOnly + Secure + SameSite。ID はハッシュ保存。ログインごとに再発行 |
| CSRF | SameSite=Lax + 更新系での `X-Requested-With` 必須。OAuth callback は `state` |
| ログイン CSRF（攻撃者の認可コードを被害者に踏ませる） | `state` を発行元ブラウザのセッション前提で検証し、1 回限り・10 分。PKCE |
| 認可コード横取り | PKCE `S256`、リダイレクト URI は設定固定（リクエストから組み立てない） |
| オープンリダイレクト | ログイン後の戻り先は画面ハッシュの許可リストのみ |
| 中継の悪用 | 許可リストによるパス制限 |
| 総当たり・認可エンドポイントへの連打 | login / callback のレート制限（5 回失敗 / 60 秒ロック） |
| DB / バックアップの漏洩 | トークンは AES-256-GCM。KEK は別ファイル。クライアントシークレットは DB に置かない |
| リフレッシュの取りこぼしによる連携断 | ユーザー単位 single-flight、新しい組を先に永続化（§4.4） |
| 利用者の離職・権限変更 | Redmine 側の取り消し・ロック・ロール変更が即座に効く（トークンは Redmine が検証する）。`rmapp` 側の特別対応は不要 |

### 11.4 端末紛失への備え

端末固有の認証器を持たないため、専用の仕組みは不要になりました（旧設計の
回復コード・2 台目登録の構想は廃止）。

- 端末を紛失したら、利用者が Redmine の「マイアカウント」から
  `RedminePocketGo` の認可を取り消す。トークンは即座に無効になり、紛失端末の
  セッションは次の API 呼び出しで `redmine_credential_invalid` になる。
- 管理者が Redmine でユーザーをロックしても同様。
- `rmapp` 側の即時失効が必要な場合の運用は Manual.md に記載する。

---

## 12. 将来機能：地図表示

### 12.1 前提

RedmineDocker には `redmine_gtt` プラグインと PostGIS が**最初から含まれて
います**。追加のプラグイン導入は不要で、Redmine 側の準備は整っています。

### 12.2 データの取得

`redmine_gtt` は GeoJSON 形式のジオメトリを扱います。地図表示用の専用
エンドポイントを分け、表示範囲（バウンディングボックス）とズームレベルを
引数に取り、範囲外は返しません。

| メソッド | パス | 内容 |
|---|---|---|
| GET | `/api/projects/{id}/geometries` | 表示範囲内のジオメトリ |
| GET | `/api/issues/{id}/geometry` | 単一チケットのジオメトリ |

### 12.3 描画

- 地図ライブラリは MapLibre GL JS を `app/js/vendor/` に**同梱**します
  （CDN 禁止のフロントエンド規約に従う）。
- ポイントはピン、線は太めのライン、多角形は半透明の塗りと輪郭。
- 色はステータス・優先度のトークン（7.4 節）と揃えます。
- 重なったポイントはクラスタ表示。タイル配信元は設定で指定します。

### 12.4 画面への統合

- チケット一覧に「リスト / 地図」の切り替えを追加します。
- チケット詳細に、ジオメトリを持つ場合のみ地図を表示します。
- 現在地の表示は、利用者の明示的な操作があったときのみ行います。

初期リリースでは実装しません。

---

## 13. 非機能要件

| 項目 | 目標 |
|---|---|
| 初回表示 | モバイル回線で 3 秒以内 |
| 画面遷移 | 1 秒以内 |
| 同時利用者 | 100 人程度 |
| 対応言語 | 日本語（英語対応の余地を残す） |
| ブラウザ | iOS Safari 16 以降、Android Chrome 108 以降 |
| コントラスト | WCAG 2.1 AA（トークンはテンプレート側で検証済みの値を継承） |

---

## 14. 設計上の未決事項

| 項目 | 内容 |
|---|---|
| OAuth 実機探査の結果（Redmine 7.0.2、2026-10-09、CI 実行） | ① `/users/current.json` はスコープ指定なし（= `view_project`）でも通る。② 主要 GET は 200。`custom_fields.json` は 403（管理者専用）、`memberships.json` は `view_members` が必要。③ PKCE は検証される（§1.2）。④ リフレッシュでリフレッシュトークンは入れ替わり、旧トークンは `invalid_grant`、旧アクセストークンは有効なまま。⑤ スコープ外の書き込みは 403（6.1.x の未適用報告はチケット更新・コメント・作成では再現せず）。⑥ `/oauth/revoke` は 200、失効後は 401。`expires_in` は 7200。同意時に sudo モードの再確認あり。**未検証**: DELETE（本アプリは許可リストに無く使わない）、sudo モードの有効時間、`GET /my/account.json` が Bearer でも `api_key` を返しうること（中継・内部呼び出しとも禁止のまま） |
| E2E / スタック試験でのトークン調達 | Redmine のパスワードを使わず、`rails runner` で `Doorkeeper::Application` と `Doorkeeper::AccessToken` を作る方式で、ブラウザを介さず試験用の組を得る。管理者の API キーは使わない（`scripts/redmine-seed-testdata.sh` の書き換えを含む） |
| 添付ファイルのアップロード | Redmine は 2 段階（トークン取得 → 本体送信）のため、中継方式を別途検討する |
| 全文検索 | Redmine の検索 API を使うか、絞り込みのみに留めるか |
| 通知 | Web Push の採用可否。iOS の制約を実機で確認してから判断する |
| Tabulator dataTree の仮想スクロール併用 | 大規模ツリーでの描画性能を実測してから判断する |
