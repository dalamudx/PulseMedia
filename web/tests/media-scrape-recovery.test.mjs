import {test} from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import {stripTypeScriptTypes} from 'node:module';
const source = readFileSync(new URL('../src/features/media-scrape/recovery.ts', import.meta.url), 'utf8');
const m = await import(`data:text/javascript;base64,${Buffer.from(stripTypeScriptTypes(source)).toString('base64')}`);
const userId = '00000000-0000-4000-8000-000000000001';
const other = '00000000-0000-4000-8000-000000000002';
const key = '00000000-0000-4000-8000-000000000003';
const batch = {id: '00000000-0000-4000-8000-000000000004', created_at: '2026-10-20T12:00:00Z', total: 3, queued: 3, running: 0, succeeded: 0, review: 0};
const preview = {total: 7, missing: 1, queued: 1, running: 1, complete: 3, review: 1};
const pending = {version: 1, userId, key};
function setup() {
  const values = new Map();
  const calls = [];
  const storage = {getItem: k => values.get(k) ?? null, setItem: (k, v) => values.set(k, v), removeItem: k => values.delete(k)};
  const options = {
    mode: 'read', userId, storage, active: () => true, newKey: () => key,
    get: async k => {calls.push(['GET', k]); return {status: 200, data: {preview, batch: null}};},
    post: async k => {calls.push(['POST', k]); assert.deepEqual(m.parsePendingScrape(storage.getItem(m.scrapeStorageKey(userId)), userId), pending); return {status: 200, data: batch};},
  };
  return {values, calls, storage, options, save: () => storage.setItem(m.scrapeStorageKey(userId), JSON.stringify(pending))};
}

test('latest status read is read-only with no command key', async () => {
  const s = setup();
  const r = await m.scrapeCommand(s.options);
  assert.equal(r.phase, 'ready');
  assert.deepEqual(r.snapshot.preview, preview);
  assert.deepEqual(s.calls, [['GET', undefined]]);
  assert.equal(s.values.size, 0);
});
test('deliberate start persists minimal account metadata before POST and clears on acknowledgement', async () => {
  const s = setup();
  const r = await m.scrapeCommand({...s.options, mode: 'start'});
  assert.equal(r.phase, 'acknowledged');
  assert.deepEqual(r.batch, batch);
  assert.equal(r.commandKey, key);
  assert.deepEqual(s.calls, [['POST', key]]);
  assert.equal(s.values.size, 0);
});
test('lost response survives reload; read uses only the original account key and never POSTs', async () => {
  const s = setup();
  await m.scrapeCommand({...s.options, mode: 'start', post: async () => {throw Error('response lost');}});
  assert.deepEqual(m.parsePendingScrape(s.storage.getItem(m.scrapeStorageKey(userId)), userId), pending);
  const r = await m.scrapeCommand(s.options);
  assert.equal(r.phase, 'missing');
  assert.deepEqual(s.calls, [['GET', key]]);
  assert.deepEqual(r.pending, pending);
});
test('recovered receipt clears only original pending command with no replay', async () => {
  const s = setup(); s.save();
  const r = await m.scrapeCommand({...s.options, get: async k => {assert.equal(k, key); return {status: 200, data: {preview, batch}};}});
  assert.equal(r.phase, 'acknowledged');
  assert.equal(s.values.size, 0);
  assert.equal(s.calls.length, 0);
});
test('explicit retry first reads original receipt then sends exact persisted key', async () => {
  const s = setup(); s.save();
  const r = await m.scrapeCommand({...s.options, mode: 'retry', newKey: () => assert.fail('new key generated')});
  assert.equal(r.phase, 'acknowledged');
  assert.deepEqual(s.calls, [['GET', key], ['POST', key]]);
});
test('retry finding an existing receipt does not resend', async () => {
  const s = setup(); s.save();
  const r = await m.scrapeCommand({...s.options, mode: 'retry', get: async () => ({status: 200, data: {preview, batch}}), post: () => assert.fail('replayed')});
  assert.equal(r.phase, 'acknowledged');
});
test('new command cannot replace an unresolved original', async () => {
  const s = setup(); s.save();
  assert.equal((await m.scrapeCommand({...s.options, mode: 'start'})).phase, 'blocked');
  assert.deepEqual(s.calls, []);
  assert.equal(JSON.parse(s.storage.getItem(m.scrapeStorageKey(userId))).key, key);
});
for (const status of [400, 401, 403, 409, 503]) {
  test(`POST ${status} retains original identity and withholds ready state`, async () => {
    const s = setup();
    const r = await m.scrapeCommand({...s.options, mode: 'start', post: async () => ({status})});
    assert.equal(r.phase, [401, 403].includes(status) ? 'auth' : 'unknown');
    assert.equal(r.snapshot, undefined);
    assert.deepEqual(m.parsePendingScrape(s.storage.getItem(m.scrapeStorageKey(userId)), userId), pending);
  });
  test(`GET ${status} cannot retry or retain stale counters`, async () => {
    const s = setup(); s.save();
    const r = await m.scrapeCommand({...s.options, mode: 'retry', get: async () => ({status, data: {preview, batch}})});
    assert.equal(r.snapshot, undefined);
    assert.equal(r.batch, undefined);
    assert.notEqual(r.phase, 'ready');
    assert.deepEqual(s.calls, []);
    assert(s.storage.getItem(m.scrapeStorageKey(userId)));
  });
}
test('another account reads latest and cannot consume old account pending identity', async () => {
  const s = setup(); s.save();
  const r = await m.scrapeCommand({...s.options, userId: other});
  assert.equal(r.phase, 'ready');
  assert.deepEqual(s.calls, [['GET', undefined]]);
  assert(s.storage.getItem(m.scrapeStorageKey(userId)));
  assert.notEqual(m.scrapeStorageKey(userId), m.scrapeStorageKey(other));
});
test('account or role fence before transport prevents all storage writes and I/O', async () => {
  const s = setup();
  assert.equal((await m.scrapeCommand({...s.options, mode: 'start', active: () => false})).phase, 'inactive');
  assert.equal(s.values.size, 0);
  assert.deepEqual(s.calls, []);
});
for (const mode of ['read', 'retry']) test(`account switch during ${mode} ignores receipt and preserves old pending record`, async () => {
  const s = setup(); s.save(); let active = true;
  const r = await m.scrapeCommand({...s.options, mode, active: () => active, get: async () => {active = false; return {status: 200, data: {preview, batch}};}});
  assert.equal(r.phase, 'inactive');
  assert(s.storage.getItem(m.scrapeStorageKey(userId)));
  assert.deepEqual(s.calls, []);
});
test('account switch before retry POST prevents stale write', async () => {
  const s = setup(); s.save(); let active = true;
  const r = await m.scrapeCommand({...s.options, mode: 'retry', active: () => active, get: async () => {active = false; return {status: 200, data: {preview, batch: null}};}});
  assert.equal(r.phase, 'inactive');
  assert.deepEqual(s.calls, []);
});
test('late POST acknowledgement after account switch does not clear old account storage', async () => {
  const s = setup(); let active = true;
  const r = await m.scrapeCommand({...s.options, mode: 'start', active: () => active, post: async () => {active = false; return {status: 200, data: batch};}});
  assert.equal(r.phase, 'inactive');
  assert(s.storage.getItem(m.scrapeStorageKey(userId)));
});
test('persistence failure prevents transport', async () => {
  const s = setup(); s.storage.setItem = () => {throw Error('quota');};
  assert.equal((await m.scrapeCommand({...s.options, mode: 'start'})).phase, 'unknown');
  assert.deepEqual(s.calls, []);
});
test('cleanup failure remains recoverable by read without another POST', async () => {
  const s = setup(); s.save(); const remove = s.storage.removeItem;
  s.storage.removeItem = () => {throw Error('storage blocked');};
  const options = {...s.options, get: async () => ({status: 200, data: {preview, batch}})};
  assert.equal((await m.scrapeCommand(options)).phase, 'unknown');
  assert(s.storage.getItem(m.scrapeStorageKey(userId)));
  s.storage.removeItem = remove;
  assert.equal((await m.scrapeCommand(options)).phase, 'acknowledged');
  assert.deepEqual(s.calls, []);
});
test('record change while reading cannot be cleared or overwritten by retry', async () => {
  for (const result of [null, batch]) {
    const s = setup(); s.save();
    const r = await m.scrapeCommand({...s.options, mode: 'retry', get: async () => {
      s.storage.setItem(m.scrapeStorageKey(userId), JSON.stringify({...pending, key: other}));
      return {status: 200, data: {preview, batch: result}};
    }});
    assert.equal(r.phase, 'blocked');
    assert.equal(JSON.parse(s.storage.getItem(m.scrapeStorageKey(userId))).key, other);
    assert.deepEqual(s.calls, []);
  }
});
test('malformed, cross-account, oversized and secret-bearing recovery metadata fail closed', async () => {
  for (const raw of ['{', '{}', 'x'.repeat(513), JSON.stringify({...pending, userId: other}), JSON.stringify({...pending, path: '/private/media'}), JSON.stringify({...pending, token: 'secret'}), JSON.stringify({...pending, key: 'not-a-command-key'})]) {
    const s = setup(); s.storage.setItem(m.scrapeStorageKey(userId), raw);
    assert.equal((await m.scrapeCommand({...s.options, mode: 'start'})).phase, 'unknown');
    assert.deepEqual(s.calls, []);
    assert.equal(s.storage.getItem(m.scrapeStorageKey(userId)), raw);
  }
});
test('malformed success bodies never clear pending or become ready', async () => {
  for (const invalid of [{...batch, id: 'bad'}, {...batch, queued: -1}, {...batch, created_at: 'invalid'}, {...batch, review: 1.5}, null]) {
    const s = setup(); s.save();
    const r = await m.scrapeCommand({...s.options, get: async () => ({status: 200, data: {preview, batch: invalid, ...(invalid === null ? {preview: {...preview, running: '1'}} : {})}})});
    assert.equal(r.phase, 'unknown');
    assert.equal(r.snapshot, undefined);
    assert(s.storage.getItem(m.scrapeStorageKey(userId)));
  }
});
test('acknowledged receipt progress can refresh exact original without persisting a new command', async () => {
  const s = setup();
  const r = await m.scrapeCommand({...s.options, receiptKey: key});
  assert.equal(r.phase, 'ready');
  assert.deepEqual(s.calls, [['GET', key]]);
  assert.equal(s.values.size, 0);
});
test('completed job records and empty batch are valid without claiming file existence', () => {
  assert(m.validScrapeStatus({preview: {...preview, total: 3, missing: 0, queued: 0, running: 0, complete: 3, review: 0}}));
  assert(m.validScrapeBatch({...batch, total: 0, queued: 0}));
});
