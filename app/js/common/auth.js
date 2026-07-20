// auth.js — WebAuthn セレモニーのブラウザ側ヘルパー。base64url ⇄ ArrayBuffer
// 変換、navigator.credentials 呼び出し、機能判定をまとめる。画面が
// navigator.credentials を直接触るのは禁止（CLAUDE.md §3.2）。

import { apiPostJson } from './api.js';

// ---- base64url ⇄ ArrayBuffer ----

export function b64urlToBuf(s) {
  const pad = '='.repeat((4 - (s.length % 4)) % 4);
  const b64 = (s + pad).replaceAll('-', '+').replaceAll('_', '/');
  const bin = atob(b64);
  const buf = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) buf[i] = bin.charCodeAt(i);
  return buf.buffer;
}

export function bufToB64url(buf) {
  const bytes = new Uint8Array(buf);
  let bin = '';
  for (const b of bytes) bin += String.fromCharCode(b);
  return btoa(bin).replaceAll('+', '-').replaceAll('/', '_').replaceAll('=', '');
}

// isPasskeySupported はこの環境がパスキーを扱えるかを返す。
export function isPasskeySupported() {
  return typeof window !== 'undefined' &&
    !!window.PublicKeyCredential &&
    typeof navigator !== 'undefined' &&
    !!(navigator.credentials && navigator.credentials.create && navigator.credentials.get);
}

// サーバーの options JSON（PublicKeyCredentialCreationOptions 相当）の
// base64url フィールドを ArrayBuffer に復元する。
function decodeCreation(publicKey) {
  publicKey.challenge = b64urlToBuf(publicKey.challenge);
  publicKey.user.id = b64urlToBuf(publicKey.user.id);
  if (publicKey.excludeCredentials) {
    publicKey.excludeCredentials = publicKey.excludeCredentials.map((c) => ({
      ...c, id: b64urlToBuf(c.id),
    }));
  }
  return publicKey;
}

function decodeRequest(publicKey) {
  publicKey.challenge = b64urlToBuf(publicKey.challenge);
  if (publicKey.allowCredentials) {
    publicKey.allowCredentials = publicKey.allowCredentials.map((c) => ({
      ...c, id: b64urlToBuf(c.id),
    }));
  }
  return publicKey;
}

// 認証器の作成レスポンスをサーバーへ送れる JSON に整形する。
function encodeAttestation(cred) {
  const r = cred.response;
  return {
    id: cred.id,
    rawId: bufToB64url(cred.rawId),
    type: cred.type,
    response: {
      clientDataJSON: bufToB64url(r.clientDataJSON),
      attestationObject: bufToB64url(r.attestationObject),
    },
    clientExtensionResults: cred.getClientExtensionResults ? cred.getClientExtensionResults() : {},
  };
}

function encodeAssertion(cred) {
  const r = cred.response;
  return {
    id: cred.id,
    rawId: bufToB64url(cred.rawId),
    type: cred.type,
    response: {
      clientDataJSON: bufToB64url(r.clientDataJSON),
      authenticatorData: bufToB64url(r.authenticatorData),
      signature: bufToB64url(r.signature),
      userHandle: r.userHandle ? bufToB64url(r.userHandle) : null,
    },
    clientExtensionResults: cred.getClientExtensionResults ? cred.getClientExtensionResults() : {},
  };
}

// runRegistration は begin レスポンス（{challengeId, options}）から登録
// セレモニーを実行し、finish に POST する。finishPath は challengeId を
// クエリで付ける。戻り値は finish のレスポンス JSON。
export async function runRegistration(begin, finishPath) {
  const publicKey = decodeCreation(begin.options.publicKey);
  const cred = await navigator.credentials.create({ publicKey });
  const url = `${finishPath}?challengeId=${encodeURIComponent(begin.challengeId)}`;
  return apiPostJson(url, encodeAttestation(cred));
}

// runLogin は Discoverable Credential でのログインを実行する。
export async function runLogin(begin, finishPath) {
  const publicKey = decodeRequest(begin.options.publicKey);
  const cred = await navigator.credentials.get({ publicKey });
  const url = `${finishPath}?challengeId=${encodeURIComponent(begin.challengeId)}`;
  return apiPostJson(url, encodeAssertion(cred));
}
