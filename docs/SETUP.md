# 構築手順

本書は、はじめて環境を構築するための手順書です。

設計の意図や各設定項目が何を意味するかは [DESIGN.md](DESIGN.md) の
「10. 設定項目」に記載しています。本書では、実際に何を入力し、どのコマンドを
実行するかだけを扱います。

構築が終わったあとの日常の操作は [MANUAL.md](MANUAL.md) を参照してください。

---

## 1. 事前準備

### 1.1 必要なもの

| ソフトウェア | バージョン | 用途 |
|---|---|---|
| Docker | Compose v2 | Redmine の実行 |
| Go | 1.22 以降 | 中継サーバーのビルド |
| Node.js | 20 以降 | SPA のビルド |
| Git | | ソースの取得 |

インストール済みか確認します。

```bash
docker compose version
go version
node --version
```

### 1.2 ドメインと証明書

パスキーは HTTPS でしか動作しません。`localhost` のみ例外です。

| 用途 | 必要なもの |
|---|---|
| 開発 | `localhost` で可。証明書は不要 |
| 本番 | 独自ドメインと TLS 証明書 |

本番では、前段にリバースプロキシ（Nginx、Caddy など）を置いて TLS を
終端する構成を想定しています。

**ドメイン名は運用開始後に変更できません。** 変更すると登録済みの
パスキーがすべて無効になります。着手前に確定させてください。

---

## 2. ソースの取得

```bash
git clone <このリポジトリの URL> redmine-mobile
cd redmine-mobile
```

---

## 3. Redmine の構築

すでに稼働中の Redmine を使う場合は、この章を飛ばして「3.3 REST API の
有効化」だけを実施してください。

### 3.1 起動

```bash
cd deploy
cp .env.example .env
```

`.env` を編集します。

```bash
# データベースのパスワード。任意の文字列に変更してください
REDMINE_DB_PASSWORD=変更してください

# Redmine のセッション用シークレット。下のコマンドで生成した値を入れます
REDMINE_SECRET_KEY_BASE=

# Redmine を公開するポート
REDMINE_PORT=3000
```

シークレットを生成します。

```bash
openssl rand -hex 64
```

出力された値を `REDMINE_SECRET_KEY_BASE` に設定し、起動します。

```bash
docker compose up -d
```

初回はデータベースの初期化に数分かかります。ログで完了を確認します。

```bash
docker compose logs -f redmine
```

### 3.2 初期ログイン

ブラウザで `http://localhost:3000` を開きます。

| 項目 | 初期値 |
|---|---|
| ログイン名 | `admin` |
| パスワード | `admin` |

初回ログイン時にパスワードの変更を求められます。変更してください。

### 3.3 REST API の有効化

Redmine の REST API は既定で無効です。有効にしないと本アプリは動作しません。

1. 管理者でログインする
2. 「管理」→「設定」→「API」タブを開く
3. 「RESTによるWebサービスを有効にする」にチェックを入れる
4. 保存する

### 3.4 動作確認

自分の API キーを確認します。

1. 右上のユーザー名 →「個人設定」を開く
2. 画面右側の「APIアクセスキー」の「表示」をクリック

コマンドで確認します。

```bash
curl -H "X-Redmine-API-Key: 取得したキー" \
     http://localhost:3000/projects.json
```

JSON が返れば成功です。

### 3.5 地図機能を使う場合（任意）

将来の地図機能を使う予定がある場合は、`redmine_gtt` プラグインを
導入します。初期リリースでは不要です。

導入手順はプラグインの配布元に従ってください。プラグイン導入後は
Redmine の再起動とデータベースのマイグレーションが必要です。

---

## 4. 鍵の生成

中継サーバーは 2 種類の鍵を必要とします。

| 鍵 | 用途 | 失った場合 |
|---|---|---|
| セッション署名鍵 | セッションの改ざん防止 | 全員が再ログイン |
| 暗号化鍵（KEK） | API キーの暗号化 | 全員が Redmine 連携を再設定 |

生成します。

```bash
mkdir -p secrets
chmod 700 secrets

openssl rand -base64 32 > secrets/session.key
openssl rand -base64 32 > secrets/kek.key

chmod 600 secrets/*.key
```

**この 2 つのファイルは必ずバックアップしてください。** 特に `kek.key` を
失うと、保存済みの API キーは復号できなくなります。

`secrets/` はバージョン管理に含めないでください。`.gitignore` に登録済みです。

---

## 5. 設定ファイルの作成

```bash
cd backend
cp configs/config.example.yaml configs/config.yaml
```

`configs/config.yaml` を編集します。各項目の意味は
[DESIGN.md](DESIGN.md) の「10. 設定項目」を参照してください。

### 5.1 最低限変更が必要な項目

| キー | 設定する値 |
|---|---|
| `server.base_url` | ブラウザからアクセスする URL |
| `webauthn.rp_id` | ドメイン名（ポート番号とスキームを含めない） |
| `webauthn.rp_name` | 端末の認証画面に表示される名称 |
| `webauthn.origins` | アクセス元のオリジン（スキームとポートを含む） |
| `session.secret_file` | `secrets/session.key` へのパス |
| `crypto.kek_file` | `secrets/kek.key` へのパス |
| `redmine.base_url` | Redmine の URL |
| `database.dsn` | データベースの接続先 |

### 5.2 開発環境の設定例

```yaml
server:
  addr: ":8080"
  base_url: "http://localhost:8080"
  static_dir: "./public"

webauthn:
  rp_id: "localhost"
  rp_name: "Redmine モバイル"
  origins:
    - "http://localhost:8080"

session:
  secret_file: "../secrets/session.key"
  secure: false          # localhost の http では false

crypto:
  kek_file: "../secrets/kek.key"

redmine:
  base_url: "http://localhost:3000"

database:
  driver: "sqlite"
  dsn: "file:./data/app.db?_fk=1"

log:
  level: "debug"
  format: "text"
```

### 5.3 本番環境の設定例

```yaml
server:
  addr: "127.0.0.1:8080"
  base_url: "https://redmine-app.example.jp"
  static_dir: "/opt/rmapp/public"
  trusted_proxies:
    - "127.0.0.1/32"

webauthn:
  rp_id: "redmine-app.example.jp"
  rp_name: "Redmine モバイル"
  origins:
    - "https://redmine-app.example.jp"

session:
  secret_file: "/etc/rmapp/secrets/session.key"
  secure: true
  ttl: "720h"

crypto:
  kek_file: "/etc/rmapp/secrets/kek.key"

redmine:
  base_url: "https://redmine.example.jp"
  timeout: "10s"
  max_concurrency: 8

database:
  driver: "postgres"
  dsn: "postgres://rmapp:パスワード@127.0.0.1:5432/rmapp?sslmode=disable"

log:
  level: "info"
  format: "json"
```

### 5.4 rp_id と origins の関係

間違えやすい箇所です。

| 項目 | 含めるもの | 例 |
|---|---|---|
| `rp_id` | ドメイン名のみ | `redmine-app.example.jp` |
| `origins` | スキーム + ドメイン + ポート | `https://redmine-app.example.jp` |

`rp_id` にスキームやポートを含めると、パスキーの登録・認証が失敗します。

---

## 6. フロントエンドのビルド

```bash
cd frontend
npm ci
npm run build
```

`frontend/dist/` に成果物が生成されます。これを中継サーバーの配信先へ
コピーします。

```bash
mkdir -p ../backend/public
cp -r dist/* ../backend/public/
```

`scripts/build.sh` を使うと、この一連の処理をまとめて実行できます。

---

## 7. 中継サーバーのビルド

```bash
cd backend
go build -o bin/rmapp ./cmd/server
```

---

## 8. データベースの初期化

```bash
cd backend
mkdir -p data
./bin/rmapp migrate --config configs/config.yaml
```

テーブルが作成されます。このコマンドは何度実行しても安全です。

---

## 9. 起動と初期設定

### 9.1 起動

```bash
cd backend
./bin/rmapp serve --config configs/config.yaml
```

次のようなログが出れば起動成功です。

```
level=INFO msg="server started" addr=:8080 rp_id=localhost
```

起動に失敗する場合、設定の不備であればキー名がログに出力されます。

### 9.2 最初のユーザー登録

1. ブラウザで `server.base_url` に設定した URL を開く
2. 「Redmine の情報でログイン」を選択する
3. Redmine のログイン名とパスワードを入力する
4. 認証に成功すると、パスキーの登録を求められる
5. 端末の生体認証または PIN で登録を完了する
6. 回復コードが表示されるので、印刷または安全な場所に保管する

Redmine のパスワードはこの手順の中でのみ使用され、保存されません。

### 9.3 2 台目の端末を登録する

**必ず登録してください。** 端末が 1 台だけの状態でその端末を失うと、
回復コードなしにはログインできなくなります。

1. 登録済みの端末でログインする
2. 設定画面を開く
3. 「別の端末を追加」を選択し、登録コードを発行する
4. 追加したい端末でログイン画面を開き、「登録コードで端末を追加」を選択する
5. コードを入力し、パスキーを登録する

登録コードの有効期限は 10 分、1 回限りです。

---

## 10. リバースプロキシの設定（本番）

前段で TLS を終端する場合の設定例です。

### 10.1 Nginx

```nginx
server {
    listen 443 ssl http2;
    server_name redmine-app.example.jp;

    ssl_certificate     /etc/letsencrypt/live/redmine-app.example.jp/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/redmine-app.example.jp/privkey.pem;

    # HSTS。パスキーは HTTPS 必須のため有効にします
    add_header Strict-Transport-Security "max-age=31536000" always;

    location / {
        proxy_pass http://127.0.0.1:8080;

        # 元のホスト名とスキームを渡します。
        # これがないとパスキーの検証が失敗します
        proxy_set_header Host              $host;
        proxy_set_header X-Real-IP         $remote_addr;
        proxy_set_header X-Forwarded-For   $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;

        # ファイルのアップロードに備えて上限を広げます
        client_max_body_size 50m;
    }
}

server {
    listen 80;
    server_name redmine-app.example.jp;
    return 301 https://$host$request_uri;
}
```

### 10.2 Caddy

```
redmine-app.example.jp {
    # 証明書は自動で取得・更新されます
    reverse_proxy 127.0.0.1:8080
}
```

---

## 11. サービスとして常駐させる（本番）

systemd のユニット例です。`/etc/systemd/system/rmapp.service` に配置します。

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
ExecStart=/opt/rmapp/bin/rmapp serve --config /etc/rmapp/config.yaml
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

起動・停止・ログ確認の日常操作は [MANUAL.md](MANUAL.md) を参照してください。

---

## 12. 構築後の確認

次がすべて成功すれば、構築は完了です。

| 確認項目 | 方法 |
|---|---|
| サーバーが応答する | `curl -i https://ドメイン/healthz` が 200 を返す |
| SPA が表示される | ブラウザで開いてログイン画面が出る |
| パスキーで登録できる | 初回登録が完了する |
| パスキーでログインできる | 一度ログアウトして再ログインする |
| プロジェクトが見える | 一覧に Redmine のプロジェクトが並ぶ |
| ツリーが正しい | 親子関係が Redmine と一致する |
| チケットが見える | 一覧と詳細が表示される |
| 2 台目が登録できる | 登録コードで別端末を追加できる |

---

## 13. うまくいかないとき

| 症状 | 確認すること |
|---|---|
| パスキーのボタンが押せない | HTTPS でアクセスしているか。`localhost` 以外で http になっていないか |
| 登録時に「操作できません」と出る | `webauthn.rp_id` がアクセス中のドメインと一致しているか |
| ログインは通るがプロジェクトが空 | Redmine の REST API が有効か。そのユーザーがプロジェクトに参加しているか |
| `redmine_credential_invalid` が出る | Redmine 側で API キーが再生成されていないか |
| 起動時に設定エラーで止まる | ログに出力されたキー名を確認する |
| Redmine への接続が失敗する | `redmine.base_url` がサーバーから到達できるか。自己署名証明書なら `tls_skip_verify` を検討する |
| リバースプロキシ経由で認証が失敗 | `Host` と `X-Forwarded-Proto` が転送されているか |
