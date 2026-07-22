// table.js — Tabulator 6 の薄いラッパー。手書き <table> は禁止（CLAUDE.md
// §3.2）。画面は window.Tabulator を直接触らず、このラッパー経由で使う。
// Tabulator は UMD ビルドを index.html の <script> で読み込み、グローバルの
// window.Tabulator を参照する（vendor 同梱。CDN なし）。
import { toDataTree } from './tree.js';

function Tab() {
  if (typeof window === 'undefined' || !window.Tabulator) {
    throw new Error('table.js: Tabulator が読み込まれていません（vendor スクリプト未ロード）');
  }
  return window.Tabulator;
}

// createTree は集約 API の入れ子データ（children）から dataTree テーブルを作る。
// opts.columns は列定義、opts.data は入れ子配列、opts.childrenKey は子キー
//（既定 children）。開閉状態の保存などは画面側で dataTreeRowExpanded 等に
// フックする。行には role="treeitem" / aria-expanded / aria-level を付与する
//（コンテナ側の role="tree" は画面側の HTML が持つ。CLAUDE.md §3.4）。
export function createTree(el, opts) {
  const T = Tab();
  const data = toDataTree(opts.data || [], opts.childrenKey || 'children');
  const table = new T(el, {
    data,
    layout: opts.layout || 'fitDataStretch',
    columns: opts.columns,
    dataTree: true,
    dataTreeChildField: '_children',
    dataTreeStartExpanded: opts.startExpanded ?? false,
    dataTreeElementColumn: opts.treeColumn,
    reactiveData: false,
    placeholder: opts.placeholder || 'データがありません',
    ...opts.tabulator,
  });
  // Tabulator は自身の grid ARIA パターン（role=grid/row/gridcell）を初期化時に
  // コンテナへ付与し、HTML 側で指定した role="tree" を上書きしてしまうため、
  // 明示的に元へ戻す（このプロジェクトの規約は tree/treeitem。CLAUDE.md §3.4）。
  el.setAttribute('role', 'tree');
  // rowFormatter は行が木構造に配線される前に呼ばれることがあり、その時点
  // では getTreeChildren/getTreeParent が未確定なので使わない。renderComplete
  // （毎回の描画確定後）に全行へ一括で付与し、展開・折りたたみ後は該当行だけ
  // 更新する。
  table.on('renderComplete', () => {
    el.setAttribute('role', 'tree');
    applyTreeA11yRecursive(table.getRows());
  });
  table.on('dataTreeRowExpanded', applyTreeRowA11y);
  table.on('dataTreeRowCollapsed', applyTreeRowA11y);
  return table;
}

// applyTreeA11yRecursive walks getTreeChildren() because table.getRows()
// only returns the top-level rows in dataTree mode — nested rows (even
// currently-hidden ones under a collapsed ancestor) need visiting directly.
function applyTreeA11yRecursive(rows) {
  for (const row of rows || []) {
    applyTreeRowA11y(row);
    if (typeof row.getTreeChildren === 'function') {
      applyTreeA11yRecursive(row.getTreeChildren());
    }
  }
}

// applyTreeRowA11y は 1 行に role="treeitem" / aria-level / （子がある行のみ）
// aria-expanded を設定する。
function applyTreeRowA11y(row) {
  const rowEl = row.getElement();
  rowEl.setAttribute('role', 'treeitem');
  rowEl.setAttribute('aria-level', String(treeLevel(row)));
  const hasChildren = typeof row.getTreeChildren === 'function' && row.getTreeChildren().length > 0;
  if (hasChildren && typeof row.isTreeExpanded === 'function') {
    rowEl.setAttribute('aria-expanded', row.isTreeExpanded() ? 'true' : 'false');
  } else {
    rowEl.removeAttribute('aria-expanded');
  }
}

// treeLevel は 1 始まりの深さ（aria-level の仕様どおり）。
function treeLevel(row) {
  let level = 1;
  let r = row;
  while (typeof r.getTreeParent === 'function' && r.getTreeParent()) {
    level += 1;
    r = r.getTreeParent();
  }
  return level;
}

// createTable は平坦データのテーブルを作る（ツリーでない一覧）。
export function createTable(el, opts) {
  const T = Tab();
  return new T(el, {
    data: opts.data || [],
    layout: opts.layout || 'fitColumns',
    columns: opts.columns,
    placeholder: opts.placeholder || 'データがありません',
    ...opts.tabulator,
  });
}
