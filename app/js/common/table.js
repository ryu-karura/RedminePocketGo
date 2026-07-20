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
// フックする。
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
  return table;
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
