# 操作・運用マニュアル

本書は、構築が終わったあとの日常的な操作をまとめたものです。

はじめて環境を構築する場合は [SETUP.md](SETUP.md) を、設計の意図や各設定
項目の意味は [DESIGN.md](DESIGN.md) を参照してください。本書では、
すでに動いているシステムをどう扱うかだけを扱います。

前半は運用担当者向け、後半は利用者向けです。

---

# 第 1 部 運用担当者向け

## 1. 起動と停止

### 1.1 systemd で常駐させている場合

```bash
# 起動
sudo systemctl start rmapp

# 停止
sudo systemctl stop rmapp

# 再起動（設定を変更したあと）
sudo systemctl restart rmapp

# 状態の確認
sudo systemctl status rmapp
```

### 1.2 手動で起動する場合

```bash
cd backend
./bin/rmapp serve --config configs/config.yaml
```

停止は `Ctrl+C` です。処理中のリクエストを待ってから終了します。
待ち時間の上限は 30 秒です。

### 1.3 Redmine（Docker）

```bash
cd deploy

# 起動
docker compose up -d

# 停止
docker compose stop

# 停止してコンテナを削除（データは残ります）
docker compose down

# 状態の確認
docker compose ps
```

### 1.4 起動する順序

```
1. Redmine（と PostgreSQL）
2. 中継サーバー
```

中継サーバーは Redmine が停止していても起動します。ただしログインや
一覧の取得は失敗します。

---

## 2. 設定ファイル

### 2.1 場所

| ファイル | 内容 |
|---|---|
| `backend/configs/config.yaml` | 中継サーバーの設定 |
| `deploy/.env` | Redmine コンテナの設定 |
| `secrets/session.key` | セッション署名鍵 |
| `secrets/kek.key` | API キー暗号化鍵 |

本番環境では、`/etc/rmapp/config.yaml` と `/etc/rmapp/secrets/` に
配置している場合があります。systemd ユニットの `ExecStart` を見れば
実際のパスがわかります。

各設定項目の意味は [DESIGN.md](DESIGN.md) の「10. 設定項目」に
一覧があります。

### 2.2 変更の手順

```bash
# 1. 現在の設定を控える
sudo cp /etc/rmapp/config.yaml /etc/rmapp/config.yaml.bak

# 2. 編集する
sudo vi /etc/rmapp/config.yaml

# 3. 内容を検証する（起動はしません）
/opt/rmapp/bin/rmapp validate --config /etc/rmapp/config.yaml

# 4. 反映する
sudo systemctl restart rmapp

# 5. 起動を確認する
sudo systemctl status rmapp
```

`validate` が失敗した場合は、問題のあるキー名が表示されます。
その状態で再起動すると起動に失敗するので、必ず先に検証してください。

### 2.3 変更してはいけない項目

| キー | 理由 |
|---|---|
| `webauthn.rp_id` | 登録済みのパスキーがすべて無効になります |
| `crypto.kek` / `crypto.kek_file` | 保存済みの API キーが復号できなくなります |

`server.base_url` と `webauthn.origins` の変更も、実質的にドメイン変更を
意味するため同じ影響があります。

やむを得ず変更する場合は、全利用者にパスキーの再登録と Redmine 連携の
再設定を依頼する必要があります。

### 2.4 変更しても影響が小さい項目

| キー | 反映方法 |
|---|---|
| `log.level` | 再起動 |
| `redmine.timeout` | 再起動 |
| `redmine.max_concurrency` | 再起動 |
| `session.ttl` | 再起動。既存セッションは元の期限のまま |
| `features.*` | 再起動 |

---

## 3. ログ

### 3.1 確認

```bash
# 直近のログ
sudo journalctl -u rmapp -n 100

# リアルタイムで追う
sudo journalctl -u rmapp -f

# エラーだけ
sudo journalctl -u rmapp -p err

# 期間を指定
sudo journalctl -u rmapp --since "2026-07-19 09:00" --until "2026-07-19 18:00"
```

Redmine のログは次で確認します。

```bash
cd deploy
docker compose logs -f redmine
```

### 3.2 読み方

ログは JSON で出力されます。主なフィールドは次のとおりです。

| フィールド | 内容 |
|---|---|
| `time` | 発生時刻 |
| `level` | `DEBUG` / `INFO` / `WARN` / `ERROR` |
| `msg` | 内容 |
| `request_id` | リクエストの識別子。1 つの処理を追跡できる |
| `user_id` | 利用者の識別子 |
| `path` | リクエストのパス |
| `status` | 応答したステータスコード |
| `duration_ms` | 処理時間 |

特定のリクエストを追う場合は `request_id` で絞り込みます。

```bash
sudo journalctl -u rmapp | grep '"request_id":"abc123"'
```

### 3.3 記録されないもの

調査時に見つからなくても異常ではありません。設計上、意図的に記録して
いません。

- リクエストとレスポンスの本文
- Cookie とセッション ID
- API キー、暗号化キー
- パスキーのチャレンジと署名

### 3.4 注意して見るべきログ

| メッセージ | 意味 | 対応 |
|---|---|---|
| `upstream error` | Redmine が応答しない | Redmine の状態を確認 |
| `credential marked invalid` | API キーが無効化された | 利用者に再紐付けを案内 |
| `allowlist rejected` | 許可されていないパスへのアクセス | 頻発するなら調査 |
| `rate limited` | 呼び出し回数の上限に到達 | 攻撃か設定不足かを判断 |

---

## 4. バックアップ

### 4.1 対象

| 対象 | 重要度 | 内容 |
|---|---|---|
| `secrets/kek.key` | 最重要 | 失うと API キーが全滅 |
| `secrets/session.key` | 高 | 失うと全員が再ログイン |
| 中継サーバーの DB | 高 | パスキーと連携情報 |
| `config.yaml` | 中 | 再作成可能 |
| Redmine の DB | 最重要 | 業務データそのもの |

### 4.2 手順

鍵と設定は一度取得すれば十分です。変更したときだけ取り直します。

```bash
sudo tar czf rmapp-secrets-$(date +%Y%m%d).tar.gz \
    -C /etc/rmapp secrets config.yaml
```

中継サーバーのデータベース（SQLite の場合）は次のとおりです。

```bash
sudo systemctl stop rmapp
sudo cp /var/lib/rmapp/app.db /backup/app-$(date +%Y%m%d).db
sudo systemctl start rmapp
```

PostgreSQL の場合は `pg_dump` を使います。

```bash
pg_dump -U rmapp rmapp | gzip > /backup/rmapp-$(date +%Y%m%d).sql.gz
```

Redmine のデータベースは次のとおりです。

```bash
cd deploy
docker compose exec -T db pg_dump -U redmine redmine \
    | gzip > /backup/redmine-$(date +%Y%m%d).sql.gz
```

`scripts/backup.sh` にこれらをまとめてあります。

### 4.3 復元

```bash
# 1. サービスを止める
sudo systemctl stop rmapp

# 2. 鍵と設定を戻す
sudo tar xzf rmapp-secrets-20260719.tar.gz -C /etc/rmapp

# 3. データベースを戻す
sudo cp /backup/app-20260719.db /var/lib/rmapp/app.db
sudo chown rmapp:rmapp /var/lib/rmapp/app.db

# 4. 起動する
sudo systemctl start rmapp
```

鍵とデータベースは**必ず同じ時点のものを組で戻してください。**
食い違うと API キーが復号できなくなります。

---

## 5. 更新

```bash
# 1. バックアップを取る
scripts/backup.sh

# 2. ソースを更新する
git pull

# 3. ビルドする
scripts/build.sh

# 4. データベースを更新する
/opt/rmapp/bin/rmapp migrate --config /etc/rmapp/config.yaml

# 5. 再起動する
sudo systemctl restart rmapp

# 6. 動作を確認する
curl -i https://ドメイン/healthz
```

`migrate` は何度実行しても安全です。

---

## 6. 利用者からの問い合わせへの対応

### 6.1 ログインできない

まず確認する順序です。

```
1. HTTPS でアクセスしているか
   → http だとパスキーが動作しません

2. その端末のパスキーが登録されているか
   → 別の端末で設定画面を開き、一覧を確認してもらう

3. 端末の生体認証・PIN が有効か
   → 端末側の設定の問題であることが多いです

4. サーバーが動いているか
   → systemctl status rmapp
```

### 6.2 端末をなくした

```
1. 別の登録済み端末でログインしてもらう
2. 設定画面で該当端末を削除してもらう
   → その端末のセッションは即座に失効します
3. 代わりの端末を登録してもらう
```

登録済み端末が他にない場合は、回復コードを使ってログインします。
回復コードも失っている場合は、Redmine の認証情報による再紐付けが
最後の手段です。この経路を無効にしている場合
（`features.password_bootstrap: false`）、運用担当者が一時的に有効化して
再登録を案内する必要があります。

### 6.3 「Redmine との連携が切れました」と表示される

Redmine 側で API キーが再生成されると発生します。

```
1. 利用者に、画面の案内に従って再紐付けしてもらう
2. Redmine のログイン名とパスワードを入力してもらう
3. パスキーの再登録は不要
```

### 6.4 プロジェクトが表示されない

```
1. その利用者が Redmine 側でプロジェクトのメンバーになっているか
2. Redmine の REST API が有効のままか
3. ログに upstream error が出ていないか
```

権限は Redmine 側の設定がそのまま反映されます。中継サーバー側で
見えるプロジェクトを増やすことはできません。

### 6.5 動作が遅い

```
1. Redmine 自体の応答を確認する
   time curl -o /dev/null -s -H "X-Redmine-API-Key: ..." \
        https://redmine.example.jp/projects.json

2. ログの duration_ms を見て、どこで時間がかかっているか特定する

3. プロジェクト数・チケット数が多い場合は
   redmine.page_size と redmine.max_concurrency の調整を検討する
```

---

## 7. 運用スクリプト

`scripts/` に用意しています。すべて日本語のコメントと出力です。

| スクリプト | 内容 |
|---|---|
| `scripts/build.sh` | フロントとバックエンドをまとめてビルド |
| `scripts/backup.sh` | 鍵・設定・データベースを一括バックアップ |
| `scripts/healthcheck.sh` | 稼働確認。監視から呼ぶ想定 |
| `scripts/gen-secrets.sh` | 鍵の生成 |

いずれも、引数なしで実行すると使い方が表示されます。

---

## 8. 監視

| 項目 | 方法 | 異常の判断 |
|---|---|---|
| 稼働 | `GET /healthz` | 200 以外 |
| Redmine 接続 | `GET /readyz` | 200 以外 |
| エラー率 | ログの `status >= 500` | 増加傾向 |
| 応答時間 | ログの `duration_ms` | 悪化傾向 |
| 証明書の期限 | 証明書の有効期限 | 残り 14 日 |

`/healthz` はサーバー自身の稼働のみを、`/readyz` は Redmine への到達性まで
確認します。監視には `/readyz` を使ってください。

---

# 第 2 部 利用者向け

## 9. はじめて使うとき

### 9.1 ログインの準備

1. 案内された URL をブラウザで開きます
2. 「Redmine の情報でログイン」を選びます
3. Redmine のログイン名とパスワードを入力します
4. パスキーの登録を求められるので、端末の指紋認証・顔認証・PIN で
   登録します
5. 回復コードが表示されます。**印刷するか、安全な場所に保管してください**

以降、パスワードの入力は不要です。

### 9.2 2 台目の端末を登録する

**必ず登録してください。** 端末が 1 台だけだと、その端末をなくしたときに
ログインできなくなります。

1. すでに使える端末でログインします
2. 右上の設定（歯車）を開きます
3. 「別の端末を追加」を選び、表示された 6 桁のコードを控えます
4. 追加したい端末でログイン画面を開きます
5. 「登録コードで端末を追加」を選び、コードを入力します
6. その端末の生体認証・PIN で登録します

コードの有効期限は 10 分です。過ぎたら発行し直してください。

### 9.3 ホーム画面に追加する

アプリのように使えます。

**iPhone / iPad（Safari）**
共有ボタン →「ホーム画面に追加」

**Android（Chrome）**
右上のメニュー →「ホーム画面に追加」または「アプリをインストール」

---

## 10. 毎日の使い方

### 10.1 ログイン

「パスキーでログイン」を押し、生体認証または PIN で認証します。
ログイン名の入力は不要です。

ログイン状態は既定で 30 日間保持されます。

### 10.2 プロジェクトを探す

- `▶` を押すと子プロジェクトが開きます。`▼` で閉じます
- 開閉した状態は次回も引き継がれます
- 虫めがねで名前を検索できます。検索中は該当するプロジェクトの親も
  自動的に開きます
- プロジェクト名の右の数字は、未完了のチケット数です

### 10.3 チケットを見る

- プロジェクトを選ぶとチケット一覧が開きます
- 親子関係のあるチケットはツリーで表示されます
- 上部のボタンで、状態・担当者・優先度を絞り込めます
- 完了したチケットは既定で折りたたまれています。件数の部分を押すと
  開きます
- 下までスクロールすると続きが読み込まれます

色とアイコンの意味は次のとおりです。

| 表示 | 意味 |
|---|---|
| 青のバッジ | 進行中 |
| 水色のバッジ | 新規 |
| 緑の枠のバッジ | 完了 |
| 上向きの矢印（橙） | 優先度が高い |
| 二重の上向き矢印（赤） | 優先度が急いで・今すぐ |
| 下向きの矢印（灰） | 優先度が低い |
| 赤い日付 | 期日を過ぎている |

### 10.4 チケットを更新する

- 変更したい項目を押すとその場で編集できます
- 変更した項目だけが送信されます
- 画面下部の入力欄からコメントを追加できます
- 送信中は該当箇所だけがローディング表示になります

### 10.5 チケットを作る

一覧画面の右下の「＋」から作成します。

### 10.6 表示を変える

設定画面から、明るい表示・暗い表示・端末に合わせる、を選べます。

### 10.7 ログアウト

設定画面の「ログアウト」を選びます。

パスキーは削除されないので、次回も「パスキーでログイン」から入れます。

---

## 11. 困ったとき

| 症状 | 対処 |
|---|---|
| パスキーのボタンが押せない | URL が `https://` で始まっているか確認してください |
| 認証に失敗する | 端末の生体認証・PIN が有効か確認してください |
| 「Redmine との連携が切れました」 | 画面の案内に従って再設定してください。パスキーの登録し直しは不要です |
| プロジェクトが 1 つも出ない | Redmine 側でプロジェクトのメンバーになっているか、管理者に確認してください |
| 端末をなくした | 別の端末でログインし、設定画面から該当端末を削除してください |
| 使える端末が 1 つもない | 回復コードでログインしてください。それも無い場合は管理者に連絡してください |
| 表示が古い | 画面を下に引っ張ると再読み込みされます |

解決しない場合は、次を添えて運用担当者に連絡してください。

- 発生した日時
- 使っている端末とブラウザ
- 画面に表示されたメッセージ

---

## 12. 将来追加される機能

Redmine 側に位置情報のプラグインが導入されている場合、プロジェクトや
チケットの位置（地点・線・範囲）を地図上に表示できるようになる予定です。

初期リリースには含まれていません。
