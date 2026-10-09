// settings.js — 設定画面（Design.md §7.9）。Redmine 連携の状態・付与スコープ・
// 最終更新、再認可、ログアウト。テーマ切替はトップバー（common/shell.js）側。

import { apiGetJson, ApiError } from '../common/api.js';
import { redmineStatusInfo, needsReauthorization, scopesSummary } from '../common/settingsfmt.js';
import { startReauthorize } from '../common/reauth.js';
import { escapeHtml, errorMessage, formatDateTime } from '../common/utils.js';
import { toast } from '../common/shell.js';

export async function initSettings(section) {
  const body = section.querySelector('#settingsBody');
  if (!body) return;

  function showLoading() {
    body.innerHTML = '<div class="skeleton-list" aria-hidden="true">'
      + Array.from({ length: 3 }, () => '<div class="skeleton skeleton-row"></div>').join('')
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
      render(await apiGetJson('/api/auth/me'));
    } catch (e) {
      showError(messageFor(e));
    }
  }

  function render(me) {
    const st = redmineStatusInfo(me.redmineStatus);
    const scopes = scopesSummary(me.redmineScopes);
    const reauth = needsReauthorization(me.redmineStatus);
    body.innerHTML = `
      <section class="settings-section">
        <h2>Redmine 連携</h2>
        <p>
          <span class="badge redmine-${st.kind}">${escapeHtml(st.label)}</span>
          <span class="muted">${escapeHtml(me.redmineLogin || '')}</span>
        </p>
        ${me.redmineRefreshedAt
          ? `<p class="muted">最終更新: <time>${escapeHtml(formatDateTime(me.redmineRefreshedAt))}</time></p>`
          : ''}
        ${scopes.length
          ? `<p class="muted">許可している操作（Redmine の権限）:</p>
             <ul class="scope-list">${scopes.map((s) => `<li><code>${escapeHtml(s)}</code></li>`).join('')}</ul>`
          : ''}
        <button type="button" class="${reauth ? 'btn-primary' : 'btn-link'}" id="reauthBtn">Redmine で再認可</button>
        <div id="reauthError" class="inline-error" role="alert"></div>
        <p class="muted">許可の取り消しは Redmine の「マイアカウント」からも行えます。</p>
      </section>

      <section class="settings-section">
        <button type="button" class="btn-primary" id="settingsLogout">ログアウト</button>
      </section>`;

    body.querySelector('#reauthBtn').addEventListener('click', async (e) => {
      const btn = e.currentTarget;
      const errBox = body.querySelector('#reauthError');
      errBox.textContent = '';
      btn.disabled = true;
      try {
        await startReauthorize('#settings');
      } catch (err) {
        btn.disabled = false;
        errBox.textContent = messageFor(err);
        toast('再認可を開始できませんでした', 'crit');
      }
    });
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
