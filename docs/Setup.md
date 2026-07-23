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

パスキーは HTTPS でしか動作しません。`localhost` のみ例外です。

| 用途 | 必要なもの |
|---|---|
| 開発 | `localhost` で可。証明書は不要 |
| 本番 | 独自ドメインと TLS 証明書。RedmineDocker と同じホスト Apache で終端 |

**ドメイン名（= パスキーの RP ID）は運用開始後に変更できません。**
変更すると登録済みのパスキーがすべて無効になります。着手前に確定させて
ください。

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

### 3.3 動作確認

1. 右上のユーザー名 →「個人設定」→「APIアクセスキー」の「表示」でキーを確認
2. コマンドで確認します。**サブ URI `/redmine` を忘れないでください。**

```bash
curl -H "X-Redmine-API-Key: 取得したキー" \
     http://localhost:8080/redmine/projects.json
```

JSON が返れば成功です。

### 3.4 地図機能について

`redmine_gtt` は RedmineDocker のイメージに**同梱済み**です。将来の地図
機能のための Redmine 側の追加作業はありません。

---

## 4. 鍵の生成

中継サーバーは 2 種類の鍵をファイルで必要とします
（RedmineDocker と同じファイルベースのシークレット方式）。

| ファイル | 用途 | 失った場合 |
|---|---|---|
| `secrets/session_key.txt` | セッションの改ざん防止 | 全員が再ログイン |
| `secrets/kek.txt` | API キーの暗号化 | 全員が Redmine 連携を再設定 |

生成します。

```bash
bash scripts/generate-secrets.sh
```

スクリプトは `secrets/` を mode 700 で作成し、各ファイルを mode 600 で
生成します。`secrets/` は `.gitignore` 登録済みです。

**この 2 つのファイルは必ずバックアップしてください。** 特に `kek.txt` を
失うと、保存済みの API キーは復号できなくなります。

---

## 5. 設定ファイルの作成

`server/config/config.yaml` は雛形として既にコミットされているので、
そのまま編集します（コメントは日本語で書かれています）。
各項目の意味は [Design.md](Design.md) の「10. 設定項目」を参照してください。

### 5.1 最低限変更が必要な項目

| キー | 設定する値 |
|---|---|
| `webauthn.rpId` | ドメイン名（ポート番号とスキームを含めない） |
| `webauthn.rpName` | 端末の認証画面に表示される名称 |
| `webauthn.origins` | アクセス元のオリジン（スキームとポートを含む） |
| `session.secretFile` | `secrets/session_key.txt` へのパス |
| `crypto.kekFile` | `secrets/kek.txt` へのパス |
| `redmine.baseURL` | Redmine の起点 URL |
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

webauthn:
  rpId: "localhost"
  rpName: "Redmine モバイル"
  origins:
    - "http://localhost:8090"

crypto:
  kekFile: "../secrets/kek.txt"

redmine:
  baseURL: "http://localhost:8080"
  subURI: "/redmine"           # RedmineDocker の REDMINE_SUBURI と一致させる

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

webauthn:
  rpId: "redmine-app.example.jp"
  rpName: "Redmine モバイル"
  origins:
    - "https://redmine-app.example.jp"

crypto:
  kekFile: "/opt/rmapp/secrets/kek.txt"

redmine:
  # 同一ホストのコンテナへループバック経由で接続します
  baseURL: "http://127.0.0.1:80"
  subURI: "/redmine"

database:
  dsn: "file:/var/lib/rmapp/rmapp.db?_pragma=foreign_keys(1)"
```

### 5.4 rpId と origins の関係

間違えやすい箇所です。

| 項目 | 含めるもの | 例 |
|---|---|---|
| `rpId` | ドメイン名のみ | `redmine-app.example.jp` |
| `origins` | スキーム + ドメイン + ポート | `https://redmine-app.example.jp` |

`rpId` にスキームやポートを含めると、パスキーの登録・認証が失敗します。

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

### 8.2 最初のユーザー登録

1. ブラウザで `http://localhost:8090/`（本番は公開 URL）を開く
2. 「Redmine の情報でログイン」を選択する
3. Redmine のログイン名とパスワードを入力する
4. 認証に成功すると、パスキーの登録を求められる
5. 端末の生体認証または PIN で登録を完了する

Redmine のパスワードはこの手順の中でのみ使用され、保存されません。

### 8.3 2 台目の端末を登録する

**必ず登録してください。** 回復コードのような救済手段は現時点では
ありません。端末が 1 台だけの状態でその端末を失うと、Redmine の認証情報に
よる再紐付け（8.1 と同じ手順）以外にログインする方法がなくなります。

1. 登録済みの端末でログインする
2. 設定画面を開く
3. 「別の端末を追加」を選択し、登録コードを発行する
4. 追加したい端末でログイン画面を開き、「登録コードで端末を追加」を選択する
5. コードを入力し、パスキーを登録する

登録コードの有効期限は 10 分、1 回限りです。

---

## 9. ホスト Apache の設定（本番）

RedmineDocker がすでにホスト Apache（`host-apache/redmine-proxy.conf`）で
`/redmine` を転送している前提で、同じ vhost に本サーバーへの転送を
追加します。RedmineDocker 側の設定は変更しません。

```apache
# rmapp（Redmine モバイル）への転送を、既存の redmine vhost に追加します。
# /redmine は RedmineDocker の設定（redmine-proxy.conf）がすでに処理する
# ため、ここでは / だけを rmapp へ渡します。

# 元のホスト名とスキームを渡します。これがないとパスキーの検証が失敗します
ProxyPreserveHost On
RequestHeader set X-Forwarded-Proto "https"

# /redmine 以外を rmapp へ
ProxyPassMatch ^/(?!redmine)(.*)$ http://127.0.0.1:8090/$1
ProxyPassReverse / http://127.0.0.1:8090/
```

HSTS は RedmineDocker のホスト Apache 設定が既に付与しています。

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
サーバーの起動・ヘルスチェック（`/healthz` / `/readyz`）・許可リスト経由の
Redmine 往復 1 件を自動で確認します。

```bash
RMAPP_STACK_API_KEY="取得した API キー" scripts/test-stack.sh
```

`RMAPP_STACK_API_KEY`（3.3 で確認した API キー）は必須です。設定ファイルは
既定で `server/config/config.yaml` を使います。別の設定を使う場合は
`RMAPP_STACK_CONFIG` でパスを指定します。

それ以外の項目（パスキー登録・ログイン、プロジェクト表示など）は
ブラウザでの手動確認が必要です。手動で確認する場合は表のとおりです。

| 確認項目 | 方法 |
|---|---|
| サーバーが応答する | `curl -i http://localhost:8090/healthz` が 200 を返す |
| Redmine に到達できる | `curl -i http://localhost:8090/readyz` が 200 を返す |
| SPA が表示される | ブラウザで開いてログイン画面が出る |
| パスキーで登録できる | 初回登録が完了する |
| パスキーでログインできる | 一度ログアウトして再ログインする |
| プロジェクトが見える | 一覧に Redmine のプロジェクトが並ぶ |
| ツリーが正しい | 親子関係が Redmine と一致する |
| チケットが見える | 一覧と詳細が表示される |
| 2 台目が登録できる | 登録コードで別端末を追加できる |

---

## 12. うまくいかないとき

| 症状 | 確認すること |
|---|---|
| パスキーのボタンが押せない | HTTPS でアクセスしているか。`localhost` 以外で http になっていないか |
| 登録時に「操作できません」と出る | `webauthn.rpId` がアクセス中のドメインと一致しているか |
| ログインは通るがプロジェクトが空 | Redmine の REST API が有効か。そのユーザーがプロジェクトに参加しているか |
| Redmine への接続が 404 になる | `redmine.subURI` が RedmineDocker の `REDMINE_SUBURI`（既定 `/redmine`）と一致しているか |
| `redmine_credential_invalid` が出る | Redmine 側で API キーが再生成されていないか |
| 起動時に設定エラーで止まる | ログに出力されたキー名を確認する（起動時に自動検証される） |
| Apache 経由で認証が失敗 | `ProxyPreserveHost On` と `X-Forwarded-Proto` が設定されているか |
| Redmine スタック自体が不調 | RedmineDocker の `docs/Manual.md` / `scripts/test-stack.sh` で切り分ける |
