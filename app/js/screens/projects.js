// projects.js — プロジェクト一覧（Design.md §7.6）。Tabulator dataTree で
// 親子ツリーを描画し、開閉状態を localStorage に保存、検索時は該当行の祖先を
// 自動展開する。4 状態（loading / empty / error+retry / populated）を明示する。

import { apiGetJson, ApiError } from '../common/api.js';
import { createTree } from '../common/table.js';
import { filterTree, collectMatchAncestors, expandedIdsFor } from '../common/tree.js';
import { errorMessage, escapeHtml } from '../common/utils.js';

const LS_EXPANDED = 'projects.expanded'; // 展開中プロジェクト id の配列

export async function initProjects(section) {
  const search = section.querySelector('#projectSearch');
  const body = section.querySelector('#projectsBody');
  if (!search || !body) return; // フラグメント未ロード時の保険

  let fullTree = [];
  let table = null;
  let debounce = null;
  const persisted = loadExpanded();

  function destroy() {
    if (table) {
      try { table.destroy(); } catch (e) { /* 破棄失敗は無視 */ }
      table = null;
    }
  }

  function showLoading() {
    body.innerHTML = skeleton();
  }

  function showEmpty(isSearch) {
    destroy();
    const msg = isSearch
      ? '一致するプロジェクトがありません。'
      : '表示できるプロジェクトがありません。Redmine 側でプロジェクトが作成されると、ここに表示されます。';
    body.innerHTML = `<div class="state-empty">${escapeHtml(msg)}</div>`;
  }

  function showError(msg) {
    destroy();
    body.innerHTML = `<div class="state-error" role="alert">`
      + `<p>${escapeHtml(msg)}</p>`
      + `<button type="button" class="btn-link" id="projectsRetry">再試行</button>`
      + `</div>`;
    body.querySelector('#projectsRetry').addEventListener('click', load);
  }

  function mount(tree, expand, persist) {
    destroy();
    body.innerHTML = '<div id="projectsTree" role="tree" aria-label="プロジェクト"></div>';
    table = createTree(body.firstElementChild, {
      data: tree,
      treeColumn: 'name',
      columns: [
        {
          title: '名前', field: 'name', headerSort: false,
          formatter: (cell) => escapeHtml(cell.getValue()),
        },
      ],
      tabulator: { index: 'id' },
    });
    table.on('tableBuilt', () => applyExpansion(table, expand));
    table.on('rowClick', (e, row) => {
      const id = row.getData().id;
      if (id != null) location.hash = `#issues/${id}`;
    });
    if (persist) {
      table.on('dataTreeRowExpanded', (row) => {
        persisted.add(row.getData().id);
        saveExpanded(persisted);
      });
      table.on('dataTreeRowCollapsed', (row) => {
        persisted.delete(row.getData().id);
        saveExpanded(persisted);
      });
    }
  }

  function render() {
    const query = search.value.trim();
    const pred = matcher(query);
    const tree = query ? filterTree(fullTree, pred) : fullTree;
    if (tree.length === 0) {
      showEmpty(Boolean(query));
      return;
    }
    const ancestors = query ? collectMatchAncestors(fullTree, pred) : new Set();
    const expand = expandedIdsFor(query, persisted, ancestors);
    mount(tree, expand, !query);
  }

  async function load() {
    showLoading();
    try {
      const res = await apiGetJson('/api/projects/tree');
      fullTree = (res && res.projects) || [];
      render();
    } catch (e) {
      showError(messageFor(e));
    }
  }

  search.addEventListener('input', () => {
    clearTimeout(debounce);
    debounce = setTimeout(render, 150);
  });

  await load();
}

// matcher は名前の部分一致（大文字小文字を無視）の述語を返す。
function matcher(query) {
  const q = query.toLowerCase();
  return (n) => String(n.name || '').toLowerCase().includes(q);
}

function applyExpansion(table, expandIds) {
  for (const id of expandIds) {
    const row = table.getRow(id);
    if (row && typeof row.getTreeChildren === 'function'
        && row.getTreeChildren().length > 0) {
      row.treeExpand();
    }
  }
}

function loadExpanded() {
  try {
    const arr = JSON.parse(localStorage.getItem(LS_EXPANDED) || '[]');
    return new Set(Array.isArray(arr) ? arr : []);
  } catch {
    return new Set();
  }
}

function saveExpanded(set) {
  try {
    localStorage.setItem(LS_EXPANDED, JSON.stringify([...set]));
  } catch (e) { /* 保存不可（プライベートモード等）は無視 */ }
}

function messageFor(e) {
  if (e instanceof ApiError && e.code) return errorMessage(e.code);
  return 'プロジェクトの取得に失敗しました。時間をおいて再試行してください。';
}

function skeleton() {
  const rows = Array.from({ length: 6 },
    () => '<div class="skeleton skeleton-row"></div>').join('');
  return `<div class="skeleton-list" aria-hidden="true">${rows}</div>`;
}
