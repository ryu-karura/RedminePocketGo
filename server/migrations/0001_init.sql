-- 0001_init: Design.md §5 のデータモデル初期スキーマ。
-- Redmine のデータは複製しない。rmapp 自身の認証・保管情報のみを持つ。

CREATE TABLE users (
    id                   TEXT PRIMARY KEY,          -- UUID
    redmine_login        TEXT NOT NULL UNIQUE,
    display_name         TEXT NOT NULL DEFAULT '',
    webauthn_user_handle BLOB NOT NULL UNIQUE,
    created_at           TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at           TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- パスキー。端末ごとに 1 行、ユーザーに複数ぶら下がる。
CREATE TABLE credentials (
    id              BLOB PRIMARY KEY,               -- 認証器の Credential ID
    user_id         TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    public_key      BLOB NOT NULL,
    sign_count      INTEGER NOT NULL DEFAULT 0,
    aaguid          BLOB,
    transports      TEXT NOT NULL DEFAULT '',
    device_label    TEXT NOT NULL DEFAULT '',
    device_kind     TEXT NOT NULL DEFAULT '',       -- mobile / desktop / tablet
    backup_eligible BOOLEAN NOT NULL DEFAULT 0,
    created_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_used_at    TIMESTAMP
);
CREATE INDEX idx_credentials_user_id ON credentials(user_id);

-- Redmine API キーはユーザーにつき 1 つ（Redmine 側の制約と同じ）。
CREATE TABLE redmine_credentials (
    user_id            TEXT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    api_key_ciphertext BLOB NOT NULL,
    api_key_nonce      BLOB NOT NULL,
    key_version        INTEGER NOT NULL DEFAULT 1,
    status             TEXT NOT NULL DEFAULT 'active'
                       CHECK (status IN ('active', 'invalid')),
    verified_at        TIMESTAMP
);

-- セッション ID は生の値を保存せず、ハッシュのみを保存する。
CREATE TABLE sessions (
    id                  TEXT PRIMARY KEY,
    user_id             TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    credential_id       BLOB,
    created_at          TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_seen_at        TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    absolute_expires_at TIMESTAMP NOT NULL
);
CREATE INDEX idx_sessions_user_id ON sessions(user_id);

-- 端末追加コード（6 桁、10 分、1 回限り）。
CREATE TABLE enrollment_codes (
    code_hash  TEXT PRIMARY KEY,
    user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    expires_at TIMESTAMP NOT NULL,
    used_at    TIMESTAMP
);

-- 進行中の WebAuthn セレモニー状態（有効期限 5 分、期限切れは定期削除）。
CREATE TABLE webauthn_challenges (
    id         TEXT PRIMARY KEY,
    user_id    TEXT REFERENCES users(id) ON DELETE CASCADE, -- 登録前は NULL
    kind       TEXT NOT NULL CHECK (kind IN ('register', 'login')),
    data       BLOB NOT NULL,                               -- セレモニーのセッションデータ
    expires_at TIMESTAMP NOT NULL
);
