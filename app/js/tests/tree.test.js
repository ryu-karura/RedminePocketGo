// tree.js の単体テスト（node --test 標準ランナーのみ。npm 依存なし）。
import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  toDataTree, filterTree, collectMatchAncestors, flatten, expandedIdsFor,
} from '../common/tree.js';

const sample = () => [
  { id: 1, name: '基幹', children: [
    { id: 2, name: '会計', children: [
      { id: 3, name: '帳票', children: [] },
    ] },
    { id: 4, name: '在庫', children: [] },
  ] },
  { id: 5, name: '社内', children: [] },
];

test('toDataTree renames children to _children recursively', () => {
  const out = toDataTree(sample());
  assert.equal(out[0]._children[0]._children[0].name, '帳票');
  assert.ok(!('children' in out[0]), 'original children key removed');
  // 葉に空の _children は付けない（Tabulator の展開矢印を出さない）
  assert.ok(!('_children' in out[1]), 'leaf has no _children');
});

test('toDataTree is non-destructive to the input', () => {
  const input = sample();
  toDataTree(input);
  assert.ok('children' in input[0], 'input untouched');
});

test('flatten yields every node once', () => {
  const ids = flatten(sample()).map((n) => n.id).sort((a, b) => a - b);
  assert.deepEqual(ids, [1, 2, 3, 4, 5]);
});

test('filterTree keeps matches and their ancestors', () => {
  const out = filterTree(sample(), (n) => n.name === '帳票');
  // 基幹 > 会計 > 帳票 の枝だけ残り、在庫・社内は消える
  assert.equal(out.length, 1);
  assert.equal(out[0].name, '基幹');
  assert.equal(out[0].children.length, 1);
  assert.equal(out[0].children[0].name, '会計');
  assert.equal(out[0].children[0].children[0].name, '帳票');
});

test('filterTree keeps a matching parent even if no child matches', () => {
  const out = filterTree(sample(), (n) => n.name === '会計');
  assert.equal(out[0].children[0].name, '会計');
  // マッチした親の子孫は保持する
  assert.equal(out[0].children[0].children[0].name, '帳票');
});

test('filterTree returns empty when nothing matches', () => {
  assert.deepEqual(filterTree(sample(), () => false), []);
});

test('collectMatchAncestors returns ancestor ids of matches for auto-expand', () => {
  const ids = collectMatchAncestors(sample(), (n) => n.name === '帳票');
  assert.deepEqual([...ids].sort((a, b) => a - b), [1, 2]);
});

test('expandedIdsFor without a query returns the persisted set (independent copy)', () => {
  const persisted = new Set([1, 5]);
  const out = expandedIdsFor('', persisted, new Set([2]));
  assert.deepEqual([...out].sort((a, b) => a - b), [1, 5]);
  out.add(99);
  assert.ok(!persisted.has(99), 'input set is not mutated');
});

test('expandedIdsFor with a query unions persisted state and search ancestors', () => {
  const out = expandedIdsFor('帳票', new Set([5]), new Set([1, 2]));
  assert.deepEqual([...out].sort((a, b) => a - b), [1, 2, 5]);
});

test('expandedIdsFor accepts array inputs for persisted state', () => {
  const out = expandedIdsFor('x', [3], [4]);
  assert.deepEqual([...out].sort((a, b) => a - b), [3, 4]);
});
