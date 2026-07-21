// login.js — ログイン画面（Design.md §7.5）。パスキー主導線、登録コードでの
// 端末追加、Redmine 認証によるブートストラップ。4 状態のうち error は
// ボタン直下にインライン表示する。

import { apiPostJson, ApiError } from '../common/api.js';
import {
  isPasskeySupported, runLogin, runRegistration,
} from '../common/auth.js';
import { errorMessage } from '../common/utils.js';

export function initLogin(root, { onSuccess } = {}) {
  root.innerHTML = template();
  const els = {
    passkeyBtn: root.querySelector('#passkeyBtn'),
    err: root.querySelector('#loginError'),
    enrollLink: root.querySelector('#enrollLink'),
    bootstrapLink: root.querySelector('#bootstrapLink'),
    panels: root.querySelector('#loginPanels'),
  };

  if (!isPasskeySupported()) {
    // 登録コード・ブートストラップ経路も最終的に navigator.credentials.create を
    // 呼ぶため、パスキー非対応環境ではすべての導線を無効化する（誤誘導しない）。
    els.passkeyBtn.disabled = true;
    els.enrollLink.disabled = true;
    els.bootstrapLink.disabled = true;
    showError(els.err, 'この環境はパスキーに対応していません。パスキー対応ブラウザ（かつ HTTPS などのセキュアコンテキスト）でアクセスしてください。');
    return;
  }

  els.passkeyBtn.addEventListener('click', () => withBusy(els.passkeyBtn, els.err, async () => {
    const begin = await apiPostJson('/api/auth/login/begin');
    const res = await runLogin(begin, '/api/auth/login/finish');
    finish(res, onSuccess);
  }));

  els.enrollLink.addEventListener('click', () => renderEnroll(els.panels, els.err, onSuccess));
  els.bootstrapLink.addEventListener('click', () => renderBootstrap(els.panels, els.err, onSuccess));
}

function template() {
  return `
    <div class="card login-card">
      <h1 class="login-logo">RedminePocketGo</h1>
      <button id="passkeyBtn" class="btn-primary" type="button">
        <span class="label">パスキーでログイン</span>
      </button>
      <div id="loginError" class="inline-error" role="alert"></div>
      <div class="divider">または</div>
      <button id="enrollLink" class="btn-link" type="button">登録コードで端末を追加</button>
      <button id="bootstrapLink" class="btn-link" type="button">Redmine の情報でログイン</button>
      <div id="loginPanels"></div>
    </div>`;
}

function renderEnroll(panels, errBox, onSuccess) {
  clearError(errBox);
  panels.innerHTML = `
    <form id="enrollForm" class="login-panel">
      <label>登録コード（6 桁）
        <input id="enrollCode" inputmode="numeric" autocomplete="one-time-code"
               pattern="\\d{6}" maxlength="6" required>
      </label>
      <button class="btn-primary" type="submit">この端末を登録</button>
      <div id="enrollError" class="inline-error" role="alert"></div>
    </form>`;
  const form = panels.querySelector('#enrollForm');
  const err = panels.querySelector('#enrollError');
  const btn = form.querySelector('button');
  form.addEventListener('submit', (e) => {
    e.preventDefault();
    withBusy(btn, err, async () => {
      const code = form.querySelector('#enrollCode').value.trim();
      const begin = await apiPostJson('/api/auth/enroll', { code });
      const res = await runRegistration(begin, '/api/auth/register/finish');
      finish(res, onSuccess);
    });
  });
}

function renderBootstrap(panels, errBox, onSuccess) {
  clearError(errBox);
  panels.innerHTML = `
    <form id="bootstrapForm" class="login-panel">
      <label>Redmine ログイン名
        <input id="bsLogin" autocomplete="username" required>
      </label>
      <label>Redmine パスワード
        <input id="bsPass" type="password" autocomplete="current-password" required>
      </label>
      <button class="btn-primary" type="submit">確認してパスキーを登録</button>
      <div id="bootstrapError" class="inline-error" role="alert"></div>
    </form>`;
  const form = panels.querySelector('#bootstrapForm');
  const err = panels.querySelector('#bootstrapError');
  const btn = form.querySelector('button');
  form.addEventListener('submit', (e) => {
    e.preventDefault();
    withBusy(btn, err, async () => {
      const login = form.querySelector('#bsLogin').value.trim();
      const password = form.querySelector('#bsPass').value;
      const begin = await apiPostJson('/api/auth/bootstrap', { login, password });
      const res = await runRegistration(begin, '/api/auth/register/finish');
      finish(res, onSuccess);
    });
  });
}

function finish(res, onSuccess) {
  if (res && res.userId && typeof onSuccess === 'function') onSuccess(res);
}

// withBusy はボタンをローディング表示にして多重押下を防ぎ、失敗はインライン表示。
async function withBusy(btn, errBox, fn) {
  clearError(errBox);
  const prev = btn.innerHTML;
  btn.disabled = true;
  btn.innerHTML = '<span class="spinner"></span> 処理中…';
  try {
    await fn();
  } catch (e) {
    showError(errBox, toMessage(e));
  } finally {
    btn.disabled = false;
    btn.innerHTML = prev;
  }
}

function toMessage(e) {
  if (e instanceof ApiError && e.code) return errorMessage(e.code);
  if (e && e.name === 'NotAllowedError') return 'パスキーの操作がキャンセルされました。';
  return 'ログインに失敗しました。もう一度お試しください。';
}

function showError(box, msg) { if (box) box.textContent = msg; }
function clearError(box) { if (box) box.textContent = ''; }
