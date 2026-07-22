// modal.js の純粋関数の単体テスト（node --test 標準ランナーのみ）。
// openModal/closeModal は DOM が要るためここでは対象外（isModalHash のみ）。
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { isModalHash } from '../common/modal.js';

test('isModalHash matches #modal-<key> with or without /<param> path', () => {
  assert.equal(isModalHash('#modal-issue-create'), true);
  assert.equal(isModalHash('#modal-issue-create/1'), true);
  assert.equal(isModalHash('#modal-issue-create/1/2'), true);
});

test('isModalHash rejects non-modal or malformed hashes', () => {
  assert.equal(isModalHash('#issues/1'), false);
  assert.equal(isModalHash(''), false);
  assert.equal(isModalHash('#modal-'), false);
  assert.equal(isModalHash(null), false);
});
