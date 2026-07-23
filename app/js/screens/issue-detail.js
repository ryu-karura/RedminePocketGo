// issue-detail.js — チケット詳細（Design.md §7.8）。属性はその場で編集し、
// 変更した項目だけを PUT する。期日は残り日数を併記、下部に固定コメント欄。
// 4 状態（loading / empty(=not found) / error+retry / populated）。

import { apiGetJson, apiPutJson, ApiError } from '../common/api.js';
import { issuePatch, issueBadges, assigneeLabel } from '../common/issuefmt.js';
import { formatCustomFieldValue, requiredLabel } from '../common/customfields.js';
import {
  escapeHtml, errorMessage, formatDateTime, dueRemainingLabel, dueDateSeverity,
} from '../common/utils.js';
import { toast } from '../common/shell.js';

const DONE_STEPS = [0, 10, 20, 30, 40, 50, 60, 70, 80, 90, 100];

export async function initIssueDetail(section, params) {
  const id = params && params[0];
  const body = section.querySelector('#issueDetailBody');
  if (!body || !id) return;

  let issue = null;
  let meta = { statuses: [], priorities: [] };

  const draftKey = `issue.draft.${id}`;

  function showLoading() {
    body.innerHTML = '<div class="skeleton-list" aria-hidden="true">'
      + Array.from({ length: 5 }, () => '<div class="skeleton skeleton-row"></div>').join('')
      + '</div>';
  }

  function showError(msg) {
    body.innerHTML = `<div class="state-error" role="alert">`
      + `<p>${escapeHtml(msg)}</p>`
      + `<button type="button" class="btn-link" id="detailRetry">再試行</button>`
      + `</div>`;
    body.querySelector('#detailRetry').addEventListener('click', load);
  }

  async function load() {
    showLoading();
    try {
      const [detail, metaRes] = await Promise.all([
        apiGetJson(`/api/issues/${encodeURIComponent(id)}/detail`),
        apiGetJson('/api/meta'),
      ]);
      issue = (detail && detail.issue) || null;
      meta = metaRes || meta;
      if (!issue) {
        body.innerHTML = '<div class="state-empty">チケットが見つかりませんでした。</div>';
        return;
      }
      render();
    } catch (e) {
      showError(messageFor(e));
    }
  }

  async function applyChange(changes) {
    const patch = issuePatch(issue, changes);
    if (!patch) return false;
    try {
      await apiPutJson(`/api/redmine/issues/${encodeURIComponent(id)}.json`, patch);
      // コメント送信なら再描画（load→render→wireComposer）で新しいコメント欄が
      // 下書きを読み直す前に消しておく。後から消すと、既に再描画された新しい
      // 欄へ古い下書きが一度表示されてしまい、送信済みなのに未送信に見える。
      if (changes.notes != null) {
        try { localStorage.removeItem(draftKey); } catch (e) { /* ignore */ }
      }
      toast('更新しました', 'ok');
      await load(); // 最新状態を取り直して再描画
      return true;
    } catch (e) {
      toast(messageFor(e), 'crit');
      render(); // 失敗時は編集値を元へ戻す
      return false;
    }
  }

  function render() {
    const b = issueBadges(issue, meta);
    const sev = dueDateSeverity(issue.due_date);
    const done = Number(issue.done_ratio || 0);
    body.innerHTML = `
      <article class="issue-detail">
        <header class="issue-detail__head">
          <h1><span class="issue-id">#${escapeHtml(String(issue.id))}</span> ${escapeHtml(issue.subject || '')}</h1>
          <div class="badge-row">
            <span class="badge status-${b.status.kind}">${escapeHtml(b.status.label)}</span>
            <span class="badge prio-${b.priority.kind}">${escapeHtml(b.priority.label)}</span>
            ${issue.tracker ? `<span class="badge tracker">${escapeHtml(issue.tracker.name)}</span>` : ''}
          </div>
        </header>

        <dl class="attr-list">
          <div class="attr"><dt>担当者</dt><dd>${escapeHtml(assigneeLabel(issue))}</dd></div>
          <div class="attr"><dt>期日</dt><dd>${issue.due_date
            ? `${escapeHtml(issue.due_date)} <em class="due due-${sev || 'ok'}">${escapeHtml(dueRemainingLabel(issue.due_date))}</em>`
            : '—'}</dd></div>
          <div class="attr"><dt>進捗</dt><dd>
            <span class="progress" role="img" aria-label="進捗 ${done}パーセント">
              <span class="progress__bar" style="width:${done}%"></span>
            </span> ${done}%
          </dd></div>
        </dl>

        ${renderCustomFields(issue.custom_fields)}

        <fieldset class="edit-row">
          <legend class="visually-hidden">属性を編集</legend>
          <label>状態 ${select('editStatus', optionList(meta.statuses, issue.status && issue.status.id))}</label>
          <label>優先度 ${select('editPriority', optionList(meta.priorities, issue.priority && issue.priority.id))}</label>
          <label>進捗 ${select('editDone', DONE_STEPS.map((v) => opt(String(v), `${v}%`, v === done)).join(''))}</label>
        </fieldset>

        <section class="detail-section">
          <h2>説明</h2>
          <div class="issue-desc">${issue.description ? escapeHtml(issue.description) : '<span class="muted">説明はありません。</span>'}</div>
        </section>

        <section class="detail-section">
          <h2>添付 (${(issue.attachments || []).length})</h2>
          ${renderAttachments(issue.attachments)}
        </section>

        <section class="detail-section">
          <h2>コメント (${countNotes(issue.journals)})</h2>
          ${renderJournals(issue.journals)}
        </section>
      </article>

      <form id="commentForm" class="comment-composer">
        <label class="visually-hidden" for="commentInput">コメント</label>
        <textarea id="commentInput" rows="2" placeholder="コメントを追加…"></textarea>
        <button type="submit" class="btn-primary" id="commentSend">送信</button>
      </form>`;

    wireEditing();
    wireComposer();
  }

  function wireEditing() {
    const s = body.querySelector('#editStatus');
    const p = body.querySelector('#editPriority');
    const d = body.querySelector('#editDone');
    s.addEventListener('change', () => applyChange({ statusId: s.value }));
    p.addEventListener('change', () => applyChange({ priorityId: p.value }));
    d.addEventListener('change', () => applyChange({ doneRatio: d.value }));
  }

  function wireComposer() {
    const form = body.querySelector('#commentForm');
    const input = body.querySelector('#commentInput');
    const btn = body.querySelector('#commentSend');
    try { input.value = localStorage.getItem(draftKey) || ''; } catch (e) { /* ignore */ }
    input.addEventListener('input', () => {
      try { localStorage.setItem(draftKey, input.value); } catch (e) { /* ignore */ }
    });
    form.addEventListener('submit', async (e) => {
      e.preventDefault();
      const notes = input.value.trim();
      if (!notes) return;
      // applyChange は成功・失敗どちらでも render() を呼んで DOM を作り直す
      // （成功時は load()→render()、失敗時は直接 render()）ので、await の後は
      // この btn/input は既に切り離されている。新しい要素は render() のたびに
      // wireComposer() が再配線するので、ここでは待つだけでよい。
      btn.disabled = true;
      await applyChange({ notes });
    });
  }

  await load();
}

function select(id, options) {
  return `<select id="${id}">${options}</select>`;
}
function optionList(list, selectedId) {
  return (list || [])
    .map((x) => opt(String(x.id), x.name, String(x.id) === String(selectedId)))
    .join('');
}
function opt(value, label, selected) {
  return `<option value="${escapeHtml(value)}"${selected ? ' selected' : ''}>${escapeHtml(label)}</option>`;
}

// renderCustomFields はカスタムフィールドの一覧を Redmine が返す表示順の
// まま描画する（Design.md §7.8）。値がなければ何も表示しない。
function renderCustomFields(fields) {
  if (!fields || fields.length === 0) return '';
  return `<section class="detail-section custom-fields">
    <h2>カスタムフィールド</h2>
    <dl class="attr-list">${fields.map(renderCustomField).join('')}</dl>
  </section>`;
}

function renderCustomField(field) {
  const r = formatCustomFieldValue(field);
  const req = requiredLabel(field);
  let value;
  if (r.kind === 'link') {
    value = `<a href="${escapeHtml(r.href)}" target="_blank" rel="noopener">${escapeHtml(r.text)}</a>`;
  } else if (r.kind === 'multiline') {
    value = `<span class="cf-multiline">${escapeHtml(r.text)}</span>`;
  } else {
    value = escapeHtml(r.text);
  }
  return `<div class="attr cf-attr">
    <dt>${escapeHtml(field.name || '')}${req ? ` <span class="badge cf-required">${escapeHtml(req)}</span>` : ''}</dt>
    <dd>${value}</dd>
  </div>`;
}

function renderAttachments(atts) {
  if (!atts || atts.length === 0) return '<p class="muted">添付はありません。</p>';
  return '<ul class="attach-list">' + atts.map((a) =>
    `<li>${escapeHtml(a.filename || '')} <span class="muted">(${formatSize(a.filesize)})</span></li>`).join('') + '</ul>';
}

function countNotes(journals) {
  return (journals || []).filter((j) => j.notes && j.notes.trim() !== '').length;
}
function renderJournals(journals) {
  const withNotes = (journals || []).filter((j) => j.notes && j.notes.trim() !== '');
  if (withNotes.length === 0) return '<p class="muted">コメントはありません。</p>';
  return '<ul class="journal-list">' + withNotes.map((j) =>
    `<li class="journal"><div class="journal__meta">`
    + `${escapeHtml((j.user && j.user.name) || '')} `
    + `<time>${escapeHtml(formatDateTime(j.created_on))}</time></div>`
    + `<div class="journal__notes">${escapeHtml(j.notes)}</div></li>`).join('') + '</ul>';
}

function formatSize(bytes) {
  const n = Number(bytes || 0);
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
  return `${(n / (1024 * 1024)).toFixed(1)} MB`;
}

function messageFor(e) {
  if (e instanceof ApiError && e.code) return errorMessage(e.code);
  return 'チケットの取得に失敗しました。時間をおいて再試行してください。';
}
