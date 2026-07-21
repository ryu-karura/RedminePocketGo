// issuefmt.js の単体テスト（node --test 標準ランナーのみ。npm 依存なし）。
import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  assigneeLabel, countClosed, filterOpen, issueBadges, matchIssue, pruneIssues,
} from '../common/issuefmt.js';

const statuses = [
  { id: 1, name: '新規', is_closed: false },
  { id: 2, name: '進行中', is_closed: false },
  { id: 5, name: '完了', is_closed: true },
];
const priorities = [
  { id: 3, name: '低' },
  { id: 4, name: '通常' },
  { id: 6, name: '高' },
];
const meta = { statuses, priorities };

const tree = () => [
  {
    id: 10, subject: '親', status: { id: 2, name: '進行中' },
    priority: { id: 6, name: '高' }, assigned_to: { id: 7, name: '山田' },
    children: [
      { id: 11, subject: '子・完了', status: { id: 5, name: '完了' }, priority: { id: 4, name: '通常' } },
      { id: 12, subject: '子・新規', status: { id: 1, name: '新規' }, priority: { id: 4, name: '通常' } },
    ],
  },
  { id: 20, subject: '完了ルート', status: { id: 5, name: '完了' }, priority: { id: 3, name: '低' } },
];

test('assigneeLabel falls back to 担当なし', () => {
  assert.equal(assigneeLabel({ assigned_to: { id: 1, name: '佐藤' } }), '佐藤');
  assert.equal(assigneeLabel({}), '担当なし');
  assert.equal(assigneeLabel(null), '担当なし');
});

test('countClosed counts closed issues across the whole tree', () => {
  assert.equal(countClosed(tree(), statuses), 2); // 子・完了 と 完了ルート
});

test('filterOpen drops closed nodes but keeps open descendants context', () => {
  const out = filterOpen(tree(), statuses);
  // 完了ルート(20) は消える。親(10) は残り、子は 新規(12) のみ、完了(11) は消える。
  assert.equal(out.length, 1);
  assert.equal(out[0].id, 10);
  assert.deepEqual(out[0].children.map((c) => c.id), [12]);
});

test('pruneIssues promotes kept descendants of dropped nodes', () => {
  // 親(10) を落とすと、残る子(11,12) がルートへ昇格する。
  const out = pruneIssues(tree(), (n) => n.id !== 10);
  assert.deepEqual(out.map((n) => n.id).sort((a, b) => a - b), [11, 12, 20]);
});

test('issueBadges classifies status and priority and resolves assignee', () => {
  const b = issueBadges(tree()[0], meta);
  assert.equal(b.status.kind, 'open');
  assert.equal(b.status.label, '進行中');
  assert.equal(b.priority.kind, 'urgent'); // 最上位 = urgent
  assert.equal(b.assignee, '山田');
});

test('matchIssue applies status/priority/assignee filters, ignoring empties', () => {
  const parent = tree()[0];
  const closedRoot = tree()[1];
  assert.equal(matchIssue(parent, meta, {}), true); // 条件なし
  assert.equal(matchIssue(parent, meta, { status: 'open' }), true);
  assert.equal(matchIssue(closedRoot, meta, { status: 'open' }), false);
  assert.equal(matchIssue(closedRoot, meta, { status: 'closed' }), true);
  assert.equal(matchIssue(parent, meta, { priorityId: 6 }), true);
  assert.equal(matchIssue(parent, meta, { priorityId: 4 }), false);
  assert.equal(matchIssue(parent, meta, { assigneeId: 7 }), true);
  assert.equal(matchIssue(parent, meta, { assigneeId: 0 }), false); // 担当なし指定
  assert.equal(matchIssue({ subject: 'x' }, meta, { assigneeId: 0 }), true); // 担当なし
});
