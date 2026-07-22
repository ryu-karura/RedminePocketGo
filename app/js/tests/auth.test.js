import { test } from 'node:test';
import assert from 'node:assert/strict';
import { b64urlToBuf, bufToB64url } from '../common/auth.js';

test('base64url roundtrips arbitrary bytes', () => {
  const bytes = new Uint8Array([0, 1, 2, 250, 251, 252, 253, 254, 255, 62, 63]);
  const s = bufToB64url(bytes.buffer);
  assert.ok(!/[+/=]/.test(s), 'url-safe alphabet, no padding');
  const back = new Uint8Array(b64urlToBuf(s));
  assert.deepEqual([...back], [...bytes]);
});

test('b64urlToBuf tolerates missing padding', () => {
  // "hello" -> aGVsbG8 (no padding)
  const back = new Uint8Array(b64urlToBuf('aGVsbG8'));
  assert.equal(String.fromCharCode(...back), 'hello');
});
