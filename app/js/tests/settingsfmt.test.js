// settingsfmt.js の単体テスト（node --test 標準ランナーのみ）。
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { deviceLabel, deviceKindLabel, redmineStatusInfo } from '../common/settingsfmt.js';

test('deviceLabel falls back when the label is unset', () => {
  assert.equal(deviceLabel({ label: 'iPhone' }), 'iPhone');
  assert.equal(deviceLabel({ label: '' }), '(表示名未設定)');
  assert.equal(deviceLabel({}), '(表示名未設定)');
});

test('deviceKindLabel falls back when the kind is unset', () => {
  assert.equal(deviceKindLabel({ kind: 'スマートフォン' }), 'スマートフォン');
  assert.equal(deviceKindLabel({ kind: '' }), '不明');
  assert.equal(deviceKindLabel({}), '不明');
});

test('redmineStatusInfo maps status to a label + badge kind', () => {
  assert.deepEqual(redmineStatusInfo('active'), { label: '連携済み', kind: 'ok' });
  assert.deepEqual(redmineStatusInfo('invalid'), { label: '要再連携', kind: 'crit' });
  assert.deepEqual(redmineStatusInfo('unlinked'), { label: '未連携', kind: 'warn' });
  assert.deepEqual(redmineStatusInfo(undefined), { label: '未連携', kind: 'warn' });
});
