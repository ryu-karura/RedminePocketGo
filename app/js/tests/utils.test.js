import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  daysUntil, dueDateSeverity, statusKind, priorityKind,
  escapeHtml, formatDateTime, errorMessage,
} from '../common/utils.js';

const at = (s) => new Date(s);

test('daysUntil counts whole days to a due date', () => {
  const now = at('2026-07-20T09:00:00+09:00');
  assert.equal(daysUntil('2026-07-25', now), 5);
  assert.equal(daysUntil('2026-07-20', now), 0);
  assert.equal(daysUntil('2026-07-18', now), -2);
  assert.equal(daysUntil('', now), null);
  assert.equal(daysUntil(null, now), null);
});

test('dueDateSeverity: ok >7, warn within 7, crit overdue', () => {
  const now = at('2026-07-20T00:00:00+09:00');
  assert.equal(dueDateSeverity('2026-08-01', now), 'ok');
  assert.equal(dueDateSeverity('2026-07-25', now), 'warn');
  assert.equal(dueDateSeverity('2026-07-20', now), 'warn'); // 当日
  assert.equal(dueDateSeverity('2026-07-19', now), 'crit'); // 超過
  assert.equal(dueDateSeverity('', now), null);
});

test('statusKind maps by is_closed and position', () => {
  const statuses = [
    { id: 1, name: '新規', is_closed: false },
    { id: 2, name: '進行中', is_closed: false },
    { id: 3, name: '完了', is_closed: true },
  ];
  assert.equal(statusKind({ id: 1 }, statuses), 'new'); // 最初の未完了
  assert.equal(statusKind({ id: 2 }, statuses), 'open'); // 中間の未完了
  assert.equal(statusKind({ id: 3 }, statuses), 'closed'); // is_closed
  assert.equal(statusKind({ id: 99 }, statuses), 'open'); // 不明は open 扱い
});

test('priorityKind maps to low/normal/high/urgent by position', () => {
  const prios = [
    { id: 1, name: '低' }, { id: 2, name: '通常' },
    { id: 3, name: '高' }, { id: 4, name: '急いで' },
  ];
  assert.equal(priorityKind({ id: 1 }, prios), 'low');
  assert.equal(priorityKind({ id: 2 }, prios), 'normal');
  assert.equal(priorityKind({ id: 3 }, prios), 'high');
  assert.equal(priorityKind({ id: 4 }, prios), 'urgent');
});

test('escapeHtml neutralizes markup', () => {
  assert.equal(escapeHtml('<img src=x onerror=alert(1)>'),
    '&lt;img src=x onerror=alert(1)&gt;');
  assert.equal(escapeHtml('a & b "q" \'p\''), 'a &amp; b &quot;q&quot; &#39;p&#39;');
  assert.equal(escapeHtml(null), '');
});

test('formatDateTime renders ISO with +09:00 offset', () => {
  const s = formatDateTime('2026-07-20T00:00:00Z');
  assert.match(s, /^2026-07-20T09:00:00\+09:00$/);
  assert.equal(formatDateTime(''), '');
});

test('errorMessage maps envelope codes to Japanese', () => {
  assert.match(errorMessage('redmine_credential_invalid'), /再/);
  assert.match(errorMessage('rate_limited'), /しばらく|回数/);
  assert.equal(typeof errorMessage('unknown_code'), 'string');
});

test('daysUntil is timezone-independent (uses absolute time, not local TZ)', () => {
  // JST 2026-07-20 20:00 == UTC 2026-07-20 11:00。期日 07-21 までは 1 日。
  const now = new Date('2026-07-20T11:00:00Z');
  assert.equal(daysUntil('2026-07-21', now), 1);
  assert.equal(daysUntil('2026-07-20', now), 0);
  // JST 深夜直前でも同様（UTC 15:00 = JST 翌 00:00）
  const nowLate = new Date('2026-07-20T14:59:00Z'); // JST 23:59
  assert.equal(daysUntil('2026-07-20', nowLate), 0);
  assert.equal(daysUntil('2026-07-21', nowLate), 1);
});
