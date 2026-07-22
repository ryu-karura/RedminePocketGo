// issues.js — チケット一覧（Design.md §7.7）。Tabulator dataTree で親子ツリーを
// 1 行 2 段組（上段=番号+件名、下段=状態/優先度/担当バッジ）で描画する。
// 状態・担当・優先度で絞り込み、完了は既定で畳む（件数のみ表示）。行タップで
// チケット詳細へ。4 状態（loading / empty / error+retry / populated）。

import { apiGetJson, ApiError } from '../common/api.js';
import { createTree } from '../common/table.js';
import { flatten } from '../common/tree.js';
import {
  pruneIssues, matchIssue, issueBadges, countClosed,
} from '../common/issuefmt.js';
import { escapeHtml, errorMessage } from '../common/utils.js';

export async function initIssues(section, params) {
  const projectId = params && params[0];
  const body = section.querySelector('#issuesBody');
  const statusSel = section.querySelector('#issueStatusFilter');
  const assigneeSel = section.querySelector('#issueAssigneeFilter');
  const prioritySel = section.querySelector('#issuePriorityFilter');
  const fab = section.querySelector('#issueCreateFab');
  if (!body || !statusSel) return;

  let fullTree = [];
  let meta = { statuses: [], priorities: [], trackers: [] };
  let table = null;

  if (fab) {
    fab.addEventListener('click', () => {
      location.hash = `#modal-issue-create/${encodeURIComponent(projectId)}`;
    });
  }

  function destroy() {
    if (table) {
      try { table.destroy(); } catch (e) { /* 破棄失敗は無視 */ }
      table = null;
    }
  }

  function showLoading() {
    body.innerHTML = skeleton();
  }

  function showEmpty(filtered) {
    destroy();
    if (filtered) {
      body.innerHTML = `<div class="state-empty">`
        + `<p>条件に一致するチケットがありません。</p>`
        + `<button type="button" class="btn-link" id="issuesClearFilters">フィルタをクリア</button>`
        + `</div>`;
      body.querySelector('#issuesClearFilters').addEventListener('click', () => {
        statusSel.value = 'open';
        assigneeSel.value = '';
        prioritySel.value = '';
        render();
      });
      return;
    }
    body.innerHTML = '<div class="state-empty"><p>このプロジェクトにはチケットがありません。'
      + '右下の＋ボタンから作成できます。</p></div>';
  }

  function showError(msg) {
    destroy();
    body.innerHTML = `<div class="state-error" role="alert">`
      + `<p>${escapeHtml(msg)}</p>`
      + `<button type="button" class="btn-link" id="issuesRetry">再試行</button>`
      + `</div>`;
    body.querySelector('#issuesRetry').addEventListener('click', load);
  }

  function issueCell(cell) {
    const d = cell.getData();
    const b = issueBadges(d, meta);
    return `<div class="issue-row">`
      + `<div class="issue-row__head"><span class="issue-id">#${escapeHtml(String(d.id))}</span> `
      + `${escapeHtml(d.subject || '')}</div>`
      + `<div class="issue-row__meta">`
      + `<span class="badge status-${b.status.kind}">${escapeHtml(b.status.label)}</span>`
      + `<span class="badge prio-${b.priority.kind}">${escapeHtml(b.priority.label)}</span>`
      + `<span class="assignee">${escapeHtml(b.assignee)}</span>`
      + `</div></div>`;
  }

  function mount(tree, hintHtml) {
    destroy();
    body.innerHTML = '<div id="issuesTree" role="tree" aria-label="チケット"></div>'
      + (hintHtml || '');
    table = createTree(body.querySelector('#issuesTree'), {
      data: tree,
      treeColumn: 'subject',
      columns: [
        {
          title: 'チケット', field: 'subject', headerSort: false,
          formatter: issueCell,
        },
      ],
      startExpanded: true, // 親子の文脈を既定で見せる
      tabulator: { index: 'id', layout: 'fitColumns' },
    });
    table.on('rowClick', (e, row) => {
      const id = row.getData().id;
      if (id != null) location.hash = `#issue-detail/${id}`;
    });
  }

  function render() {
    const filters = {
      status: statusSel.value,
      assigneeId: assigneeSel.value,
      priorityId: prioritySel.value,
    };
    const shown = pruneIssues(fullTree, (n) => matchIssue(n, meta, filters));
    if (shown.length === 0) {
      showEmpty(hasActiveFilter(filters));
      return;
    }
    let hint = '';
    if (filters.status === 'open') {
      const closed = countClosed(fullTree, meta.statuses);
      if (closed > 0) {
        hint = `<p class="issues-hint" role="note">完了 ${closed} 件は非表示です`
          + `（状態フィルタで表示できます）。</p>`;
      }
    }
    mount(shown, hint);
  }

  function populateFilters() {
    // 状態: 既定は未完了（完了は畳む。§7.7）
    setOptions(statusSel, [
      { value: 'open', label: '未完了' },
      { value: '', label: 'すべての状態' },
      { value: 'closed', label: '完了' },
    ], 'open');
    // 優先度
    const prios = (meta.priorities || []).map((p) => ({ value: String(p.id), label: p.name }));
    setOptions(prioritySel, [{ value: '', label: 'すべての優先度' }, ...prios], '');
    // 担当: ツリーから重複なく収集（+ 担当なし）
    const seen = new Map();
    let hasUnassigned = false;
    for (const it of flatten(fullTree)) {
      if (it.assigned_to) seen.set(String(it.assigned_to.id), it.assigned_to.name);
      else hasUnassigned = true;
    }
    const people = [...seen.entries()].map(([value, label]) => ({ value, label }));
    const opts = [{ value: '', label: 'すべての担当' }, ...people];
    if (hasUnassigned) opts.push({ value: '0', label: '担当なし' });
    setOptions(assigneeSel, opts, '');
  }

  async function load() {
    showLoading();
    try {
      const [treeRes, metaRes] = await Promise.all([
        apiGetJson(`/api/projects/${encodeURIComponent(projectId)}/issues/tree`),
        apiGetJson('/api/meta'),
      ]);
      fullTree = (treeRes && treeRes.issues) || [];
      meta = metaRes || meta;
      populateFilters();
      render();
    } catch (e) {
      showError(messageFor(e));
    }
  }

  for (const sel of [statusSel, assigneeSel, prioritySel]) {
    sel.addEventListener('change', render);
  }

  await load();
}

function hasActiveFilter(f) {
  return (f.status && f.status !== '') || f.assigneeId !== '' || f.priorityId !== '';
}

function setOptions(sel, opts, value) {
  sel.innerHTML = opts
    .map((o) => `<option value="${escapeHtml(o.value)}">${escapeHtml(o.label)}</option>`)
    .join('');
  sel.value = value;
}

function messageFor(e) {
  if (e instanceof ApiError && e.code) return errorMessage(e.code);
  return 'チケットの取得に失敗しました。時間をおいて再試行してください。';
}

function skeleton() {
  const rows = Array.from({ length: 6 },
    () => '<div class="skeleton skeleton-row"></div>').join('');
  return `<div class="skeleton-list" aria-hidden="true">${rows}</div>`;
}
