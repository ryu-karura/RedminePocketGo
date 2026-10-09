// settingsfmt.js の単体テスト（node --test 標準ランナーのみ）。
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { redmineStatusInfo, needsReauthorization, scopesSummary } from '../common/settingsfmt.js';

test('redmineStatusInfo maps status to a label + badge kind', () => {
  assert.deepEqual(redmineStatusInfo('active'), { label: '連携済み', kind: 'ok' });
  assert.deepEqual(redmineStatusInfo('invalid'), { label: '要再認可', kind: 'crit' });
  assert.deepEqual(redmineStatusInfo('unlinked'), { label: '未連携', kind: 'warn' });
  assert.deepEqual(redmineStatusInfo(undefined), { label: '未連携', kind: 'warn' });
  assert.deepEqual(redmineStatusInfo('something-else'), { label: '未連携', kind: 'warn' });
});

test('needsReauthorization is true unless the grant is active', () => {
  assert.equal(needsReauthorization('active'), false);
  assert.equal(needsReauthorization('invalid'), true);
  assert.equal(needsReauthorization('unlinked'), true);
  assert.equal(needsReauthorization(undefined), true);
});

test('scopesSummary lists granted scopes and tolerates missing data', () => {
  assert.deepEqual(scopesSummary(['view_issues', 'edit_issues']), ['view_issues', 'edit_issues']);
  assert.deepEqual(scopesSummary([]), []);
  assert.deepEqual(scopesSummary(undefined), []);
  assert.deepEqual(scopesSummary(null), []);
  assert.deepEqual(scopesSummary('view_issues'), [], 'a non-array is not trusted');
  assert.deepEqual(scopesSummary(['ok', 3, null, '']), ['ok'], 'only non-empty strings');
});
