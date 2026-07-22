// settings.js — 設定画面（Design.md §7.9）。端末（パスキー）の一覧・削除、
// 登録コードの発行、Redmine 連携の状態表示と再紐付け、ログアウト。
// テーマ切替はトップバー（common/shell.js）側にあるためここでは扱わない。

import {
  apiGetJson, apiPostJson, apiDeleteJson, ApiError,
} from '../common/api.js';
import { deviceLabel, deviceKindLabel, redmineStatusInfo } from '../common/settingsfmt.js';
import { escapeHtml, errorMessage, formatDateTime } from '../common/utils.js';
import { toast } from '../common/shell.js';

export async function initSettings(section) {
  const body = section.querySelector('#settingsBody');
  if (!body) return;

  let devices = [];
  let me = { redmineLogin: '', redmineStatus: 'unlinked' };
  let confirmDeleteId = null; // 削除確認中の端末 id（インライン確認。1 件のみ）
  let relinkOpen = false;
  let relinkError = '';
  let enrollment = null; // { code, expiresAt } | null

  function showLoading() {
    body.innerHTML = '<div class="skeleton-list" aria-hidden="true">'
      + Array.from({ length: 4 }, () => '<div class="skeleton skeleton-row"></div>').join('')
      + '</div>';
  }

  function showError(msg) {
    body.innerHTML = `<div class="state-error" role="alert">`
      + `<p>${escapeHtml(msg)}</p>`
      + `<button type="button" class="btn-link" id="settingsRetry">再試行</button>`
      + `</div>`;
    body.querySelector('#settingsRetry').addEventListener('click', load);
  }

  async function load() {
    showLoading();
    try {
      const [devRes, meRes] = await Promise.all([
        apiGetJson('/api/devices'),
        apiGetJson('/api/auth/me'),
      ]);
      devices = (devRes && devRes.devices) || [];
      me = meRes || me;
      confirmDeleteId = null;
      render();
    } catch (e) {
      showError(messageFor(e));
    }
  }

  function render() {
    const st = redmineStatusInfo(me.redmineStatus);
    body.innerHTML = `
      <section class="settings-section">
        <h2>Redmine 連携</h2>
        <p>
          <span class="badge redmine-${st.kind}">${escapeHtml(st.label)}</span>
          <span class="muted">${escapeHtml(me.redmineLogin || '')}</span>
        </p>
        <button type="button" class="btn-link" id="relinkToggle">認証情報を再入力</button>
        ${relinkOpen ? relinkFormHtml() : ''}
      </section>

      <section class="settings-section">
        <h2>端末</h2>
        ${devices.length === 0
          ? '<p class="state-empty">登録済みの端末がありません。</p>'
          : `<ul class="device-list">${devices.map(deviceRowHtml).join('')}</ul>`}
        <button type="button" class="btn-link" id="enrollIssue">新しい端末を追加</button>
        ${enrollment ? enrollmentHtml() : ''}
      </section>

      <section class="settings-section">
        <button type="button" class="btn-primary" id="settingsLogout">ログアウト</button>
      </section>`;

    wire();
  }

  function deviceRowHtml(d) {
    const confirming = confirmDeleteId === d.id;
    return `<li class="device-row" data-id="${escapeHtml(d.id)}">
      <div class="device-row__info">
        <div class="device-row__label">${escapeHtml(deviceLabel(d))}</div>
        <div class="device-row__meta muted">
          ${escapeHtml(deviceKindLabel(d))} ・
          登録: ${escapeHtml(formatDateTime(d.createdAt))} ・
          最終利用: ${d.lastUsedAt ? escapeHtml(formatDateTime(d.lastUsedAt)) : '—'}
        </div>
      </div>
      ${confirming
        ? `<span class="device-row__confirm">
             本当に削除しますか？
             <button type="button" class="btn-link device-delete-yes" data-id="${escapeHtml(d.id)}">はい</button>
             <button type="button" class="btn-link device-delete-no">いいえ</button>
           </span>`
        : `<button type="button" class="btn-link device-delete" data-id="${escapeHtml(d.id)}" aria-label="削除">削除</button>`}
    </li>`;
  }

  function relinkFormHtml() {
    return `<form id="relinkForm" class="relink-form">
      ${relinkError ? `<div class="inline-error" role="alert">${escapeHtml(relinkError)}</div>` : ''}
      <label class="form-field">
        <span>Redmine ログイン ID</span>
        <input id="relinkLogin" type="text" autocomplete="username" required>
      </label>
      <label class="form-field">
        <span>パスワード</span>
        <input id="relinkPassword" type="password" autocomplete="current-password" required>
      </label>
      <button type="submit" class="btn-primary" id="relinkSubmit">再連携する</button>
    </form>`;
  }

  function enrollmentHtml() {
    return `<div class="enrollment-code" role="status">
      <p>新しい端末で下のコードを入力してください（<time>${escapeHtml(formatDateTime(enrollment.expiresAt))}</time> まで有効）。</p>
      <p class="enrollment-code__value">${escapeHtml(enrollment.code)}</p>
    </div>`;
  }

  function wire() {
    body.querySelector('#relinkToggle').addEventListener('click', () => {
      relinkOpen = !relinkOpen;
      relinkError = '';
      render();
    });

    const relinkForm = body.querySelector('#relinkForm');
    if (relinkForm) {
      relinkForm.addEventListener('submit', async (e) => {
        e.preventDefault();
        const login = body.querySelector('#relinkLogin').value;
        const password = body.querySelector('#relinkPassword').value;
        try {
          await apiPostJson('/api/auth/relink', { login, password });
          toast('Redmine と再連携しました', 'ok');
          relinkOpen = false;
          relinkError = '';
          await load();
        } catch (err) {
          relinkError = messageFor(err);
          render();
        }
      });
    }

    body.querySelector('#enrollIssue').addEventListener('click', async () => {
      try {
        enrollment = await apiPostJson('/api/auth/enrollment-code');
        render();
      } catch (err) {
        toast(messageFor(err), 'crit');
      }
    });

    for (const btn of body.querySelectorAll('.device-delete')) {
      btn.addEventListener('click', () => {
        confirmDeleteId = btn.dataset.id;
        render();
      });
    }
    const cancelBtn = body.querySelector('.device-delete-no');
    if (cancelBtn) {
      cancelBtn.addEventListener('click', () => {
        confirmDeleteId = null;
        render();
      });
    }
    const yesBtn = body.querySelector('.device-delete-yes');
    if (yesBtn) {
      yesBtn.addEventListener('click', async () => {
        const id = yesBtn.dataset.id;
        try {
          await apiDeleteJson(`/api/devices/${encodeURIComponent(id)}`);
          toast('端末を削除しました', 'ok');
          await load();
        } catch (err) {
          toast(messageFor(err), 'crit');
          confirmDeleteId = null;
          render();
        }
      });
    }

    body.querySelector('#settingsLogout').addEventListener('click', () => {
      if (window.rmappLogout) window.rmappLogout();
    });
  }

  function messageFor(e) {
    if (e instanceof ApiError && e.code) return errorMessage(e.code);
    return 'エラーが発生しました。時間をおいて再試行してください。';
  }

  await load();
}
