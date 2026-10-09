-- migrate:foreign-keys=off
-- 0002_oauth: Redmine 7 の OAuth 2.0 ログインへ移行するための追加
-- （Design.md §5）。旧テーブル（credentials / redmine_credentials /
-- enrollment_codes / webauthn_challenges）は、参照する実装を消す後続の
-- マイグレーションで削除する。
--
-- users は作り直す（SQLite は NOT NULL / UNIQUE の外し方が限られるため）。
-- 先頭の migrate:foreign-keys=off は、適用側に「外部キー検査を切って実行し、
-- 終了前に foreign_key_check で整合を確かめる」ことを求める印。切らずに
-- DROP TABLE users すると、sessions などが ON DELETE CASCADE で消える。
CREATE TABLE users_new (
    id                   TEXT PRIMARY KEY,          -- UUID
    redmine_user_id      INTEGER,                   -- Redmine のユーザー ID。同一性の鍵（旧行は NULL → 初回ログインで埋める）
    redmine_login        TEXT NOT NULL,             -- 表示用。Redmine 側で変わりうるので一意の鍵にしない
    display_name         TEXT NOT NULL DEFAULT '',
    webauthn_user_handle BLOB,                      -- 旧方式の残り。OAuth の利用者は NULL
    created_at           TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at           TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
INSERT INTO users_new (id, redmine_login, display_name, webauthn_user_handle, created_at, updated_at)
    SELECT id, redmine_login, display_name, webauthn_user_handle, created_at, updated_at FROM users;
DROP TABLE users;
ALTER TABLE users_new RENAME TO users;
-- SQLite の UNIQUE は NULL を複数許すため、未紐付けの旧行・OAuth 利用者が並存できる。
CREATE UNIQUE INDEX idx_users_redmine_user_id ON users(redmine_user_id);
CREATE UNIQUE INDEX idx_users_webauthn_user_handle ON users(webauthn_user_handle);
CREATE INDEX idx_users_redmine_login ON users(redmine_login);

-- ユーザーにつき OAuth トークンの組を 1 つ。アクセスとリフレッシュは
-- 別々のノンスで AES-256-GCM 暗号化する（Design.md §4.3）。
CREATE TABLE oauth_tokens (
    user_id            TEXT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    access_ciphertext  BLOB NOT NULL,
    access_nonce       BLOB NOT NULL,
    refresh_ciphertext BLOB NOT NULL,
    refresh_nonce      BLOB NOT NULL,
    key_version        INTEGER NOT NULL DEFAULT 1,
    scopes             TEXT NOT NULL DEFAULT '',     -- 空白区切り
    access_expires_at  TIMESTAMP NOT NULL,
    status             TEXT NOT NULL DEFAULT 'active'
                       CHECK (status IN ('active', 'invalid')),
    refreshed_at       TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- 進行中の認可要求（state と PKCE の code_verifier）。10 分・1 回限り。
CREATE TABLE oauth_states (
    state_hash          TEXT PRIMARY KEY,            -- state のハッシュ（生の値は保存しない）
    verifier_ciphertext BLOB NOT NULL,
    verifier_nonce      BLOB NOT NULL,
    return_to           TEXT NOT NULL DEFAULT '',    -- ログイン後に戻る画面ハッシュ（許可リスト済み）
    expires_at          TIMESTAMP NOT NULL,
    used_at             TIMESTAMP
);
CREATE INDEX idx_oauth_states_expires_at ON oauth_states(expires_at);
