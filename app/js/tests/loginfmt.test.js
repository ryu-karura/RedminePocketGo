// loginfmt.js の単体テスト（node --test 標準ランナーのみ）。
import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  loginErrorFromHash, loginErrorMessage, returnHashFor, loginUrl,
} from '../common/loginfmt.js';

test('loginErrorFromHash extracts the error code from #login?error=...', () => {
  assert.equal(loginErrorFromHash('#login?error=access_denied'), 'access_denied');
  assert.equal(loginErrorFromHash('#login?error=invalid_state'), 'invalid_state');
  assert.equal(loginErrorFromHash('#login?error=rate_limited'), 'rate_limited');
});

test('loginErrorFromHash ignores anything else (never reflects arbitrary text)', () => {
  for (const h of ['', '#', '#projects', '#login', '#login?error=', '#login?error=<script>',
    '#login?error=a b', '#login?x=1&error=access_denied', '#loginx?error=access_denied',
    '#login?error=ACCESS_DENIED', '#login?error=' + 'a'.repeat(65)]) {
    assert.equal(loginErrorFromHash(h), '', JSON.stringify(h));
  }
  assert.equal(loginErrorFromHash(undefined), '');
  assert.equal(loginErrorFromHash(null), '');
});

test('loginErrorMessage covers every code the server can send, with a safe fallback', () => {
  for (const code of ['invalid_state', 'exchange_failed', 'redmine_unavailable',
    'server_misconfigured', 'access_denied', 'rate_limited', 'server_error']) {
    const m = loginErrorMessage(code);
    assert.ok(m && m.length > 5, code);
    assert.notEqual(m, loginErrorMessage('__unknown__'), `${code} must have its own message`);
  }
  assert.ok(loginErrorMessage('something_new').length > 5);
  assert.ok(loginErrorMessage('').length > 5);
});

test('returnHashFor keeps only screen hashes the server would accept', () => {
  for (const ok of ['#projects', '#issues', '#settings', '#issues/12', '#issue-detail/7']) {
    assert.equal(returnHashFor(ok), ok);
  }
  for (const bad of ['', '#', '#modal-issue-create/1', '#issues/abc', '#issues/12/x',
    '#login?error=x', 'https://evil.example/', '//evil', '#projects\r\nX: y', undefined, null]) {
    assert.equal(returnHashFor(bad), '', JSON.stringify(bad));
  }
});

test('loginUrl builds the full-page navigation target', () => {
  assert.equal(loginUrl(''), '/api/auth/login');
  assert.equal(loginUrl('#issues/12'), '/api/auth/login?return=%23issues%2F12');
  assert.equal(loginUrl('#modal-x'), '/api/auth/login');
});
