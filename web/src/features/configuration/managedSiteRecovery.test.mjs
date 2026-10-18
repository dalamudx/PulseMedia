import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { stripTypeScriptTypes } from 'node:module';
const source = readFileSync(new URL('./managedSiteRecovery.ts', import.meta.url), 'utf8');
const recovery = await import(`data:text/javascript;base64,${Buffer.from(stripTypeScriptTypes(source)).toString('base64')}`);
const userID = '00000000-0000-4000-8000-000000000001';
const other = '00000000-0000-4000-8000-000000000002';
const key = '00000000-0000-4000-8000-000000000003';
const targetId = '00000000-0000-4000-8000-000000000004';
test('reload metadata excludes credentials, complete URL and request material', () => {
  const text = recovery.serializeSitePending({ key, action: 'update', targetId, expectedVersion: 7, userID, body: { url: 'https://example.invalid/feed?passkey=private', api_key: 'cookie-secret' }, hash: 'secret-derived' });
  assert.deepEqual(JSON.parse(text), { key, action: 'update', targetId, expectedVersion: 7, userID });
  assert.equal(/private|cookie-secret|passkey|hash|body/.test(text), false);
  assert.deepEqual(recovery.parseSitePending(text, userID), JSON.parse(text));
});
test('recovery requires the original actor and uses separate storage scopes', () => {
  const text = recovery.serializeSitePending({ key, action: 'create', userID });
  assert.equal(recovery.parseSitePending(text, other), null);
  assert.notEqual(recovery.sitePendingStorageKey(userID), recovery.sitePendingStorageKey(other));
});
test('reload rejects corrupted, oversized and secret-bearing records', () => {
  for (const text of ['{', ' '.repeat(1025), 'null', '[]', JSON.stringify({ key, action: 'create', userID, api_key: 'secret' }), JSON.stringify({ key, action: 'create', userID, targetId }), JSON.stringify({ key, action: 'update', userID, targetId, expectedVersion: 0 }), JSON.stringify({ key: 'bad', action: 'create', userID })]) assert.equal(recovery.parseSitePending(text, userID), null);
});
test('update and delete recovery preserve exact target, version and original command key', () => {
  for (const action of ['update', 'delete']) {
    const record = { key, action, targetId, expectedVersion: 19, userID };
    assert.deepEqual(recovery.parseSitePending(recovery.serializeSitePending(record), userID), record);
    assert.equal(recovery.parseSitePending(JSON.stringify({ ...record, targetId: other, expectedVersion: 1.5 }), userID), null);
  }
});
