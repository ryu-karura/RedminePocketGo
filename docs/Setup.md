# 構築手順

本書は、はじめて環境を構築するための手順書です。

設計の意図や各設定項目の意味は [Design.md](Design.md) の「10. 設定項目」に
記載しています。本書では、実際に何を入力し、どのコマンドを実行するかだけを
扱います。構築が終わったあとの日常の操作は [Manual.md](Manual.md) を参照
してください。

Redmine 本体の構築は
[RedmineDocker](https://github.com/ryu-karura/RedmineDocker) リポジトリの
`docs/Setup.md` が正となる手順書です。本書では重複を避け、そちらへの参照と
本アプリに必要な差分だけを記載します。

---

## 1. 事前準備

### 1.1 必要なもの

| ソフトウェア | バージョン | 用途 |
|---|---|---|
| RedmineDocker の動作環境 | 同リポジトリの要件どおり | Redmine の実行 |
| Go | 1.25 以降 | 中継サーバーのビルド |
| Git | | ソースの取得 |
| shellcheck | | シェルスクリプトの検証（開発時） |

フロントエンドはビルド不要（Vanilla JS、ライブラリ同梱）のため、
**Node.js は不要**です。

```bash
go version
docker compose version   # RedmineDocker 開発スタックを同居させる場合
```

### 1.2 ドメインと証明書

OAuth のリダイレクト URI は、Redmine が **HTTPS のみ**受け付けます
（実測: `https://…` と `http://localhost` / `http://127.0.0.1` は登録でき、
それ以外の `http://…` は「Redirect URI must be an HTTPS/SSL URI.」で拒否）。
本サーバー側も同じ規則で設定を検証します。

| 用途 | 必要なもの |
|---|---|
| 開発 | `localhost` で可。証明書は不要 |
| 本番 | 独自ドメインと TLS 証明書。RedmineDocker と同じホスト Apache で終端 |

**公開 URL（= リダイレクト URI のホスト）を変えたら、Redmine 側の
アプリケーション登録も `redmine.oauth.redirectURI` も更新してください。**
両者は完全一致が必要です。

---

## 2. ソースの取得

```bash
git clone <このリポジトリの URL> redmine-mobile
cd redmine-mobile
```

---

## 3. Redmine（RedmineDocker）の準備

### 3.1 スタックの起動

**手順は RedmineDocker の `docs/Setup.md` に従ってください。**
開発時の要点だけ再掲します（詳細・トラブル対応はあちらが正）。

```bash
git clone https://github.com/ryu-karura/RedmineDocker.git
cd RedmineDocker
bash scripts/generate-secrets.sh
docker compose -f compose.dev.yaml up --build -d
# 初回ビルドは時間がかかります（プラグインの gem と webpack ビルド）
docker compose -f compose.dev.yaml logs -f redmine-web
```

起動後、`http://localhost:8080/redmine/` を開きます
（初期ログイン: `admin` / `admin`。初回にパスワード変更を求められます）。

本番（RHEL + rootless Podman + Quadlets）の構築も RedmineDocker の
`docs/Setup.md` に従ってください。

### 3.2 REST API の有効化（本アプリに必須の差分）

Redmine の REST API は既定で無効です。有効にしないと本アプリは動作しません。

1. 管理者でログインする
2. 「管理」→「設定」→「API」タブを開く
3. 「RESTによるWebサービスを有効にする」にチェックを入れる
4. 保存する

### 3.3 OAuth アプリケーションの登録

本アプリは Redmine 7 の OAuth 2.0 プロバイダに「アプリケーション」として
登録します（管理者のみ操作可能。**一度だけ**行う作業です）。

1. 管理者でログインする
2. 「管理」メニューの「アプリケーション」（`/redmine/oauth/applications`）を開く
3. 「新しいアプリケーション」を選び、次のとおり入力する

| 項目 | 値 |
|---|---|
| 名称 | 任意（例: `Redmine モバイル`） |
| リダイレクト URI | 開発: `http://localhost:8090/api/auth/callback`、本番: `https://<公開ドメイン>/api/auth/callback` |
| Confidential | チェックを入れる（クライアントシークレットを使う） |
| スコープ | `view_project` `view_issues` `add_issues` `edit_issues` `add_issue_notes` `view_members`（`config.yaml` の `redmine.oauth.scopes` と同じにする。`admin` は付けない） |

4. 保存すると **Client ID** と **Client Secret** が表示される。
   Client Secret は**この画面でしか表示されません**。
   `secrets/redmine_oauth_client_secret.txt` に書き込む（4 章）。
   Client ID は `config.yaml` の `redmine.oauth.clientId` に設定する（5 章）。

```bash
umask 077
printf '%s' 'ここに Client Secret' > secrets/redmine_oauth_client_secret.txt
```

Client Secret を紛失した場合は、Redmine でアプリケーションのシークレットを
再発行し、ファイルを書き換えてサーバーを再起動します。

### 3.3.1 動作確認

ブラウザで次の順に確認します（手順 8.2 と同じ画面遷移）。

1. `http://localhost:8090/` を開き「Redmine でログイン」を押す
2. Redmine のログイン画面でログインする
3. 同意画面が出る（このとき Redmine がパスワードの再確認を求めることが
   あります。Redmine 7 のsudo モードによる仕様です）
4. 「許可」で本アプリのプロジェクト一覧へ戻る

### 3.4 地図機能について

`redmine_gtt` は RedmineDocker のイメージに**同梱済み**です。将来の地図
機能のための Redmine 側の追加作業はありません。

---

## 4. 鍵の生成

中継サーバーは次のシークレットをファイルで必要とします
（RedmineDocker と同じファイルベースのシークレット方式）。

| ファイル | 用途 | 失った場合 |
|---|---|---|
| `secrets/session_key.txt` | セッションの改ざん防止 | 全員が再ログイン |
| `secrets/kek.txt` | OAuth トークンの暗号化 | 全員が再ログイン（再認可） |
| `secrets/redmine_oauth_client_secret.txt` | Redmine が発行する Client Secret（`generate-secrets.sh` は空のファイルを作るだけ） | Redmine でシークレットを再発行 |

生成します。

```bash
bash scripts/generate-secrets.sh
```

スクリプトは `secrets/` を mode 700 で作成し、各ファイルを mode 600 で
生成します。`secrets/` は `.gitignore` 登録済みです。

**`session_key.txt` と `kek.txt` は必ずバックアップしてください。** 特に `kek.txt` を
失うと、保存済みの OAuth トークンは復号できなくなります（再ログインで復旧）。
`redmine_oauth_client_secret.txt` が空だとサーバーは起動時に中止します。

---

## 5. 設定ファイルの作成

`server/config/config.yaml` は雛形として既にコミットされているので、
そのまま編集します（コメントは日本語で書かれています）。
各項目の意味は [Design.md](Design.md) の「10. 設定項目」を参照してください。

### 5.1 最低限変更が必要な項目

| キー | 設定する値 |
|---|---|
| `redmine.oauth.clientId` | 3.3 で Redmine が発行した Client ID |
| `redmine.oauth.clientSecretFile` | `secrets/redmine_oauth_client_secret.txt` へのパス |
| `redmine.oauth.redirectURI` | 公開 URL + `/api/auth/callback`（Redmine の登録値と完全一致） |
| `redmine.oauth.scopes` | Redmine の登録スコープと同じ一覧 |
| `session.secretFile` | `secrets/session_key.txt` へのパス |
| `crypto.kekFile` | `secrets/kek.txt` へのパス |
| `redmine.baseURL` | Redmine の起点 URL（サーバー間通信用） |
| `redmine.publicBaseURL` | ブラウザから見える Redmine の URL（`baseURL` と異なる本番では必須） |
| `database.dsn` | SQLite の接続先 |

### 5.2 開発環境の設定例

RedmineDocker 開発スタック（`localhost:8080/redmine`）と同居する構成です。
本サーバーは 8090 で待ち受けます。

```yaml
listen: ":8090"
webroot: "../app"      # server/ をカレントディレクトリとして起動する前提
serveStatic: true
noCache: true
logLevel: "debug"

session:
  idleTimeoutHours: 168
  absoluteTimeoutHours: 720
  secureCookie: false          # localhost の http では false
  secretFile: "../secrets/session_key.txt"

crypto:
  kekFile: "../secrets/kek.txt"

redmine:
  baseURL: "http://localhost:8080"
  subURI: "/redmine"           # RedmineDocker の REDMINE_SUBURI と一致させる
  oauth:
    clientId: "Redmine が発行した Client ID"
    clientSecretFile: "../secrets/redmine_oauth_client_secret.txt"
    redirectURI: "http://localhost:8090/api/auth/callback"
    scopes:
      - view_project
      - view_issues
      - add_issues
      - edit_issues
      - add_issue_notes
      - view_members

database:
  dsn: "file:data/rmapp.db?_pragma=foreign_keys(1)"
```

上記はすべて `cd server` した状態（カレントディレクトリが `server/`）で
`./bin/rmapp` を起動する前提の相対パスです。他のディレクトリから起動する
場合は絶対パスに置き換えてください。

### 5.3 本番環境の設定例

ホスト Apache が TLS を終端し、`/redmine` は RedmineDocker、`/` は本サーバー
へ振り分ける構成です。

```yaml
listen: "127.0.0.1:8090"
webroot: "/opt/rmapp/app"
serveStatic: true
noCache: true
logLevel: "info"

session:
  idleTimeoutHours: 168
  absoluteTimeoutHours: 720
  secureCookie: true
  secretFile: "/opt/rmapp/secrets/session_key.txt"

crypto:
  kekFile: "/opt/rmapp/secrets/kek.txt"

redmine:
  # 同一ホストのコンテナへループバック経由で接続します
  baseURL: "http://127.0.0.1:80"
  # ブラウザが認可画面へ遷移する先（公開 URL）。baseURL と別なので必須
  publicBaseURL: "https://redmine-app.example.jp"
  subURI: "/redmine"
  oauth:
    clientId: "Redmine が発行した Client ID"
    clientSecretFile: "/opt/rmapp/secrets/redmine_oauth_client_secret.txt"
    redirectURI: "https://redmine-app.example.jp/api/auth/callback"
    scopes:
      - view_project
      - view_issues
      - add_issues
      - edit_issues
      - add_issue_notes
      - view_members

database:
  dsn: "file:/var/lib/rmapp/rmapp.db?_pragma=foreign_keys(1)"
```

### 5.4 URL 設定の関係

間違えやすい箇所です。

| 項目 | 意味 | 例 |
|---|---|---|
| `redmine.baseURL` | サーバーが Redmine を呼ぶ URL（内部でよい） | `http://127.0.0.1:80` |
| `redmine.publicBaseURL` | ブラウザが認可画面へ移動する URL | `https://redmine-app.example.jp` |
| `redmine.oauth.redirectURI` | Redmine が認可後にブラウザを戻す URL | `https://redmine-app.example.jp/api/auth/callback` |

`redirectURI` は Redmine のアプリケーション登録値と**1 文字も違わず**
一致させてください（食い違うと Redmine が認可画面でエラーを出します）。
スコープを増やした場合は Redmine の登録も更新し、全員が再認可します。

---

## 6. ビルド

フロントエンドにビルド工程はありません。サーバーのみビルドします。

```bash
cd server
make build          # = go build -o bin/rmapp ./cmd/rmapp
```

シェルスクリプトを変更した場合は検証します。

```bash
shellcheck scripts/*.sh
```

---

## 7. データベースの準備

```bash
cd server
mkdir -p data
```

テーブルは `rmapp` の起動時に自動で作成・更新されます（マイグレーションは
何度実行されても安全で、別コマンドでの事前実行は不要です）。

---

## 8. 起動と初期設定

### 8.1 起動

```bash
cd server
./bin/rmapp -config config/config.yaml
```

次のようなログが出れば起動成功です。

```
{"time":"...","level":"INFO","msg":"rmapp starting","listen":":8090","version":"dev"}
```

設定に不備がある場合は、起動前にキー名を示して即座に終了します
（例: `crypto.kekFile を読めません`）。別途の検証専用コマンドはありません。

### 8.2 最初のログイン

1. ブラウザで `http://localhost:8090/`（本番は公開 URL）を開く
2. 「Redmine でログイン」を選択する
3. Redmine のログイン画面でログインする（Redmine の認証設定がそのまま使われます）
4. 同意画面で「許可」する（パスワードの再確認を求められることがあります）
5. 本アプリに戻り、プロジェクト一覧が表示される

本サーバーは Redmine のパスワードを一切受け取りません。2 台目以降の端末も
同じ手順でログインするだけで利用できます（端末ごとの登録は不要）。

#### 旧方式（パスキー・API キー）からの移行

起動時に DB マイグレーション（0002・0003）が自動で適用され、旧方式の
データ（パスキー、暗号化 API キー、回復コード）は削除されます。
`webauthn.*` と `features.passwordBootstrap` は設定に残っていると
起動が止まるので削除してください。3.3 のアプリケーション登録と
`redmine.oauth.*` の設定を済ませたうえで起動し、全員が 8.2 の手順で
ログインし直します（旧セッションは無効になります）。

---

## 9. ホスト Apache の設定（本番）

RedmineDocker がすでにホスト Apache（`host-apache/redmine-proxy.conf`）で
`/redmine` を転送している前提で、同じ vhost に本サーバーへの転送を
追加します。RedmineDocker 側の設定は変更しません。

```apache
# rmapp（Redmine モバイル）への転送を、既存の redmine vhost に追加します。
# /redmine は RedmineDocker の設定（redmine-proxy.conf）がすでに処理する
# ため、ここでは / だけを rmapp へ渡します。

# 元のホスト名とスキームを渡します。これがないと OAuth のリダイレクト URI やセッション Cookie の判定が崩れます
ProxyPreserveHost On
RequestHeader set X-Forwarded-Proto "https"

# /redmine 以外を rmapp へ
ProxyPassMatch ^/(?!redmine)(.*)$ http://127.0.0.1:8090/$1
ProxyPassReverse / http://127.0.0.1:8090/
```

HSTS は RedmineDocker のホスト Apache 設定が既に付与しています。

Apache（`mod_proxy`）は転送時に `X-Forwarded-For` へ接続元 IP を追記します。
rmapp はこのヘッダーを、直接の接続元が `trustedProxies`（既定はループ
バックのみ）に含まれるときだけ信用してレート制限のキーにします。Apache を
別ホストに置く場合は、そのアドレスを `server/config/config.yaml` の
`trustedProxies` に追加してください。

---

## 10. サービスとして常駐させる（本番）

systemd のユニット例です。`/etc/systemd/system/rmapp.service` に配置します
（RedmineDocker の本番はユーザー単位の Quadlet ですが、本サーバーは
コンテナ化していない単一バイナリのため、通常のシステムサービスとします）。

```ini
[Unit]
Description=Redmine モバイル 中継サーバー
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=rmapp
Group=rmapp
WorkingDirectory=/opt/rmapp
ExecStart=/opt/rmapp/bin/rmapp -config /opt/rmapp/config/config.yaml
Restart=on-failure
RestartSec=5s

# 書き込みを許可するディレクトリのみを指定します
ReadWritePaths=/var/lib/rmapp
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true
NoNewPrivileges=true

[Install]
WantedBy=multi-user.target
```

有効化します。

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now rmapp
sudo systemctl status rmapp
```

起動・停止・ログ確認の日常操作は [Manual.md](Manual.md) を参照してください。

---

## 11. 構築後の確認

`scripts/test-stack.sh` が、起動中の RedmineDocker 開発スタックに対して
サーバーの起動・ヘルスチェック（`/healthz` / `/readyz`）・OAuth 認可
リダイレクト（`state` と PKCE S256 の付与、状態 Cookie の束縛）・許可リスト
経由の Redmine 往復 1 件を自動で確認します。

```bash
token="$(scripts/redmine-seed-testdata.sh | tail -1 | cut -d= -f2-)"
RMAPP_STACK_ACCESS_TOKEN="${token}" scripts/test-stack.sh
```

`redmine-seed-testdata.sh` は REST API の有効化とテストデータ投入に加え、
Redmine 側（rails runner）で OAuth アクセストークンを発行して最終行に出力します
（Redmine の API キーは使いません）。設定ファイルは既定で
`server/config/config.yaml` を使います。別の設定は `RMAPP_STACK_CONFIG` で指定します。

Docker デーモンのない環境では GitHub Actions の `.github/workflows/oauth-probe.yml`
（push で起動）が RedmineDocker 開発スタックを起動し、Redmine 7 の OAuth の
挙動の探査と、上記 2 スクリプトの実行までを自動で行います。
`stack-test.yml` は同じ確認の定期実行版ですが、リポジトリが一定期間更新されないと
GitHub に自動で無効化されるため、リポジトリ管理者が Actions 画面から再有効化してください。

ログイン画面の操作（Redmine のログイン・同意）やプロジェクト表示などは
ブラウザでの手動確認が必要です。手動で確認する場合は表のとおりです。

| 確認項目 | 方法 |
|---|---|
| サーバーが応答する | `curl -i http://localhost:8090/healthz` が 200 を返す |
| Redmine に到達できる | `curl -i http://localhost:8090/readyz` が 200 を返す |
| SPA が表示される | ブラウザで開いてログイン画面が出る |
| Redmine でログインできる | 同意後にプロジェクト一覧へ戻る |
| ログアウト後に再ログインできる | 設定画面からログアウトし、再度ログインする |
| プロジェクトが見える | 一覧に Redmine のプロジェクトが並ぶ |
| ツリーが正しい | 親子関係が Redmine と一致する |
| チケットが見える | 一覧と詳細が表示される |
| 別端末でも使える | 別の端末で同じ手順でログインできる |

---

## 12. うまくいかないとき

| 症状 | 確認すること |
|---|---|
| 認可画面で「リダイレクト URI が無効」と出る | `redmine.oauth.redirectURI` と Redmine のアプリケーション登録値が完全一致しているか（http は localhost のみ可） |
| 「許可されなかった」と表示される | 同意画面で拒否していないか。スコープが Redmine の登録と一致しているか |
| 起動時に `redmine.oauth.clientSecretFile` で止まる | `secrets/redmine_oauth_client_secret.txt` に Redmine の Client Secret を書いたか |
| ログインは通るがプロジェクトが空 | Redmine の REST API が有効か。そのユーザーがプロジェクトに参加しているか |
| Redmine への接続が 404 になる | `redmine.subURI` が RedmineDocker の `REDMINE_SUBURI`（既定 `/redmine`）と一致しているか |
| `redmine_credential_invalid` が出る | Redmine の「マイアカウント」で本アプリの認可が取り消されていないか。設定画面の「Redmine で再認可」で復旧 |
| 起動時に設定エラーで止まる | ログに出力されたキー名を確認する（起動時に自動検証される） |
| Apache 経由で認証が失敗 | `ProxyPreserveHost On` と `X-Forwarded-Proto` が設定されているか |
| Redmine スタック自体が不調 | RedmineDocker の `docs/Manual.md` / `scripts/test-stack.sh` で切り分ける |
