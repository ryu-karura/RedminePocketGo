# Redmine モバイルクライアント

[RedmineDocker](https://github.com/ryu-karura/RedmineDocker) スタックで動作する
Redmine 7.0.2 と連携する、スマートフォン向け HTML5 SPA と、その認証・中継を
担う Go 製 Web サーバーです。

Redmine の OAuth 2.0（認可コードフロー）でログインします。本サーバーは
Redmine に OAuth アプリケーションとして登録され、ユーザーは Redmine 自身の
ログイン・同意画面で認証します。**Redmine の API キーは一切使いません**。
取得したトークンはサーバー側にのみ保管し、ブラウザには渡しません。

フロントエンドの構成とデザインは
[IoTDesignTemplate](https://github.com/ryu-karura/IoTDesignTemplate) を
踏襲しています（Vanilla JS の SPA、デザイントークンによる配色、
ライト/ダークテーマ）。

---

## 特徴

- **Redmine の OAuth 2.0 でログイン** — Redmine のログイン画面（二要素認証や
  SSO を含む）をそのまま使います。本サーバーはパスワードを見ません。
- **API キーを使わない・トークンをブラウザに渡さない** — OAuth のアクセス/
  更新トークンは Go サーバーが暗号化して保持し、リクエストのたびに
  `Authorization: Bearer` を付与します。
- **スマホと PC の併用** — どの端末からログインしても同じ Redmine
  アカウントとして動作します。
- **親子関係を保った一覧** — プロジェクト、チケットともにツリー構造を
  そのまま表示します。
- **ビルド不要のフロントエンド** — フレームワークもバンドラも使わない
  Vanilla JS。ライブラリは同梱（vendored）し、CDN に依存しません。
- **モバイルファースト** — 縦持ちスマートフォンを基準に設計し、広い画面では
  レイアウトが切り替わります。

---

## 構成

```
[スマホ / PC ブラウザ]
        │ HTTPS
        ▼
[ホスト Apache（TLS 終端）]
   ├── /redmine → RedmineDocker スタック（redmine-web）
   └── /        → 本リポジトリ（rmapp: Go 中継サーバー + SPA）
                        │ OAuth 2.0（認可コード + PKCE）
                        │ Authorization: Bearer
                        └──────→ Redmine REST API（/redmine 配下）
```

Redmine 本体（Redmine 7.0.2、PostgreSQL 18 + PostGIS 3.6、`redmine_gtt` を
含む 15 プラグイン同梱）は RedmineDocker リポジトリが担当します。
本リポジトリは RedmineDocker のスタックに**接続するだけ**で、変更しません。

構成の詳細な考え方、データモデル、API 仕様、画面設計は
[docs/Design.md](docs/Design.md) を参照してください。

---

## 画面

| 画面 | 内容 |
|---|---|
| ログイン | 「Redmine でログイン」ボタン → Redmine のログイン・同意画面 |
| プロジェクト一覧 | 親子関係をツリーで表示 |
| チケット一覧 | 選択したプロジェクトのチケットをツリーで表示 |
| チケット詳細 | 属性、説明、コメント、添付を表示 |
| 設定 | Redmine 連携状態・許可スコープの確認、再認可、テーマ切替、ログアウト |

ベースの Redmine には位置情報プラグイン `redmine_gtt` が最初から含まれて
いるため、将来的にプロジェクト・チケットの位置情報（ポイント・線・多角形）を
地図上に描画する機能を追加します。

---

## 動作要件

| 項目 | 要件 |
|---|---|
| Redmine | RedmineDocker スタック（Redmine 7.0.2、サブ URI `/redmine`）。REST API が有効で、管理者が OAuth アプリケーションを登録済みであること |
| Go | 1.25 以降（サーバーのビルド時のみ） |
| ブラウザ | iOS Safari 16 以降 / Android Chrome 108 以降 |
| 通信 | HTTPS 必須（OAuth のリダイレクト URI。`localhost` のみ例外） |

フロントエンドにビルド工程はないため、Node.js は不要です。

---

## はじめかた

RedmineDocker スタックの起動を含む構築手順、設定項目の一覧は
[docs/Setup.md](docs/Setup.md) にまとめています。

概略は次のとおりです。

1. RedmineDocker の手順で Redmine を起動し、REST API を有効にして、rmapp を OAuth アプリケーションとして登録する
2. `scripts/generate-secrets.sh` で鍵ファイルを生成する
3. `server/config/config.yaml` を用意する
4. Go サーバーをビルド・起動する
5. ブラウザからアクセスし、「Redmine でログイン」で認可する

---

## 日常の操作

起動・停止、設定ファイルの書き換え、端末紛失時の認可の取り消し、
ログの確認方法は [docs/Manual.md](docs/Manual.md) を参照してください。

---

## ドキュメント

| ファイル | 内容 |
|---|---|
| [docs/Design.md](docs/Design.md) | 詳細設計書。構成、データモデル、API、画面、設定項目の設計 |
| [docs/Setup.md](docs/Setup.md) | 構築手順と設定項目の説明 |
| [docs/Manual.md](docs/Manual.md) | 起動・停止・運用・利用者向けの操作手順 |
| `CLAUDE.md` | AI エージェント向けの開発規約（英語） |
| `.claude/rules/` | パス別の詳細ルール（フロントエンド / サーバー / ドキュメント） |

ドキュメントのファイル名・言語・役割分担は RedmineDocker の慣例
（`docs/Design.md` / `Setup.md` / `Manual.md`、日本語）に合わせています。
各ドキュメントは役割を分けており、内容の重複を避けています。

---

## ライセンス

MIT License
