// customfields.js の単体テスト（node --test 標準ランナーのみ。npm 依存なし）。
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { formatCustomFieldValue, requiredLabel } from '../common/customfields.js';

test('string/int/float/date: 生値をそのまま表示', () => {
  assert.equal(formatCustomFieldValue({ field_format: 'string', value: 'テキスト' }).kind, 'text');
  assert.equal(formatCustomFieldValue({ field_format: 'string', value: 'テキスト' }).text, 'テキスト');
  assert.equal(formatCustomFieldValue({ field_format: 'int', value: '42' }).text, '42');
  assert.equal(formatCustomFieldValue({ field_format: 'float', value: '3.5' }).text, '3.5');
  assert.equal(formatCustomFieldValue({ field_format: 'date', value: '2026-08-15' }).text, '2026-08-15');
});

test('未設定値は — 表示', () => {
  assert.equal(formatCustomFieldValue({ field_format: 'string', value: null }).text, '—');
  assert.equal(formatCustomFieldValue({ field_format: 'string', value: '' }).text, '—');
});

test('long text: 改行を保持する multiline kind', () => {
  const r = formatCustomFieldValue({ field_format: 'text', value: '1行目\n2行目' });
  assert.equal(r.kind, 'multiline');
  assert.equal(r.text, '1行目\n2行目');
});

test('bool: はい/いいえ/未設定', () => {
  assert.equal(formatCustomFieldValue({ field_format: 'bool', value: '1' }).text, 'はい');
  assert.equal(formatCustomFieldValue({ field_format: 'bool', value: '0' }).text, 'いいえ');
  assert.equal(formatCustomFieldValue({ field_format: 'bool', value: true }).text, 'はい');
  assert.equal(formatCustomFieldValue({ field_format: 'bool', value: null }).text, '未設定');
});

test('link: アンカー化', () => {
  const r = formatCustomFieldValue({ field_format: 'link', value: 'https://example.com/x' });
  assert.equal(r.kind, 'link');
  assert.equal(r.href, 'https://example.com/x');
  assert.equal(r.text, 'https://example.com/x');
});

test('link: 未設定は — 表示（href なし）', () => {
  const r = formatCustomFieldValue({ field_format: 'link', value: null });
  assert.equal(r.kind, 'text');
  assert.equal(r.text, '—');
});

test('list/key_value_list: display_value をそのまま使う', () => {
  const r = formatCustomFieldValue({ field_format: 'list', value: 'a', display_value: '重要' });
  assert.equal(r.kind, 'text');
  assert.equal(r.text, '重要');
});

test('list: 定義取得 degrade 時（display_value なし）は生値を表示', () => {
  const r = formatCustomFieldValue({ field_format: 'list', value: 'a' });
  assert.equal(r.text, 'a');
});

test('複数選択（配列）は display_value なしでも読点で連結', () => {
  const r = formatCustomFieldValue({ field_format: 'list', multiple: true, value: ['a', 'b'] });
  assert.equal(r.text, 'a、b');
});

test('version/user/attachment: display_value（解決済み名称）を表示', () => {
  assert.equal(formatCustomFieldValue({ field_format: 'version', value: '3', display_value: 'v2.0' }).text, 'v2.0');
  assert.equal(formatCustomFieldValue({ field_format: 'user', value: '7', display_value: 'Alice' }).text, 'Alice');
  assert.equal(formatCustomFieldValue({ field_format: 'attachment', value: '9', display_value: 'spec.pdf' }).text, 'spec.pdf');
});

test('version/user: 参照解決に失敗した場合は生の値（ID）を表示', () => {
  const r = formatCustomFieldValue({ field_format: 'version', value: '999' });
  assert.equal(r.text, '999');
});

test('requiredLabel は is_required のときだけ「必須」', () => {
  assert.equal(requiredLabel({ is_required: true }), '必須');
  assert.equal(requiredLabel({ is_required: false }), '');
  assert.equal(requiredLabel({}), '');
  assert.equal(requiredLabel(null), '');
});
