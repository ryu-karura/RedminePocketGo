-- 0003_drop_legacy: パスキー（WebAuthn）と Redmine API キーの保管を廃止する
-- （Design.md §5.5、docs/plan.md フェーズ 10）。認証は Redmine の OAuth 2.0 のみ。
--
-- redmine_credentials には旧 API キーの暗号文がある。DROP しただけでは空きページに
-- 残るため、削除前に secure_delete を有効にして、解放されるページを 0 で上書きする。
PRAGMA secure_delete = ON;

-- 旧方式で発行したセッションは引き継がない（Redmine のログイン名が別人に再利用
-- されても、旧 Cookie が新しい利用者の権限に結び付かないようにする）。
DELETE FROM sessions;

DROP TABLE IF EXISTS webauthn_challenges;
DROP TABLE IF EXISTS enrollment_codes;
DROP TABLE IF EXISTS credentials;
DROP TABLE IF EXISTS redmine_credentials;

DROP INDEX IF EXISTS idx_users_webauthn_user_handle;
ALTER TABLE users DROP COLUMN webauthn_user_handle;
ALTER TABLE sessions DROP COLUMN credential_id;
