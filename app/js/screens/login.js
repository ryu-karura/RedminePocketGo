// login.js — ログイン画面（Design.md §7.5）。「Redmine でログイン」の 1 ボタンだけで、
// 押すと Redmine の認可画面へ**ページ遷移**する（fetch ではない）。パスワードは
// Redmine の画面にだけ入力され、このアプリには届かない。失敗してコールバックから
// 戻ってきたときの理由は、ボタン直下にインライン表示する。

import { loginErrorMessage, loginUrl } from '../common/loginfmt.js';

// error: サーバーから戻ってきたログイン失敗コード（なければ ''）。
// returnHash: ログイン後に戻る画面ハッシュ（許可外はサーバー既定へ）。
export function initLogin(root, { error = '', returnHash = '' } = {}) {
  root.innerHTML = `
    <div class="card login-card">
      <h1 class="login-logo">RedminePocketGo</h1>
      <button id="loginBtn" class="btn-primary" type="button">
        <span class="label">Redmine でログイン</span>
      </button>
      <div id="loginError" class="inline-error" role="alert"></div>
      <p class="login-note muted">Redmine の画面で認証します。パスワードはこのアプリには送られません。</p>
    </div>`;

  const btn = root.querySelector('#loginBtn');
  const label = btn.querySelector('.label');
  const errBox = root.querySelector('#loginError');
  if (error) errBox.textContent = loginErrorMessage(error);

  btn.addEventListener('click', () => {
    // 多重押下を防ぎ、遷移中であることを示す。
    btn.disabled = true;
    btn.setAttribute('aria-busy', 'true');
    label.textContent = 'Redmine へ移動中…';
    location.assign(loginUrl(returnHash));
  });

  // 戻る操作でこのページが復元されたとき（bfcache）に、押せない状態で残さない。
  window.addEventListener('pageshow', (e) => {
    if (!e.persisted) return;
    btn.disabled = false;
    btn.removeAttribute('aria-busy');
    label.textContent = 'Redmine でログイン';
  });
}

// initReauthPrompt は、API が redmine_credential_invalid を返したときに出す
// 再認可の案内（Design.md §7.5）。Redmine との連携が切れたことを伝え、
// 再認可ボタンで通常のログインフローをやり直す。
export function initReauthPrompt(root, { onReauthorize }) {
  root.innerHTML = `
    <div class="card login-card">
      <h1 class="login-logo">RedminePocketGo</h1>
      <p class="login-lead" role="status">Redmine との連携が切れました。</p>
      <button id="reauthBtn" class="btn-primary" type="button">
        <span class="label">Redmine で再認可</span>
      </button>
      <div id="reauthError" class="inline-error" role="alert"></div>
      <p class="login-note muted">Redmine の画面で許可し直すと、元の画面に戻ります。</p>
    </div>`;
  const btn = root.querySelector('#reauthBtn');
  const label = btn.querySelector('.label');
  const errBox = root.querySelector('#reauthError');
  btn.addEventListener('click', async () => {
    errBox.textContent = '';
    btn.disabled = true;
    btn.setAttribute('aria-busy', 'true');
    label.textContent = 'Redmine へ移動中…';
    try {
      await onReauthorize();
    } catch (e) {
      errBox.textContent = '再認可を開始できませんでした。時間をおいて再試行してください。';
      btn.disabled = false;
      btn.removeAttribute('aria-busy');
      label.textContent = 'Redmine で再認可';
    }
  });
}
