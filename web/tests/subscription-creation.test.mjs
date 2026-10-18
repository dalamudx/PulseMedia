import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import ts from 'typescript';

const root = new URL('../src/features/configuration/', import.meta.url);
const compile = text => ts.transpileModule(text, { compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.ESNext } }).outputText;
const load = text => import(`data:text/javascript;base64,${Buffer.from(compile(text)).toString('base64')}#${crypto.randomUUID()}`);
const m = await load(readFileSync(new URL('subscriptionCreationRecovery.ts', root), 'utf8'));
const uuid = n => `00000000-0000-4000-8000-${String(n).padStart(12, '0')}`;
const userId = uuid(1), otherUser = uuid(2), key = uuid(3);
const body = { tmdb_id: 123, media_type: 'tv', root_id: uuid(4), quality_policy_id: 'default', scope_type: 'all', seasons: [2], include_specials: true, indexer_ids: [uuid(6), uuid(5)], downloader_id: uuid(7), expected_intent_version: 8 };
const record = { version: 1, userId, key, body };
const result = { id: uuid(10), user_id: userId, media_id: uuid(11), root_id: uuid(4), intent_version: 9, state: 'active', scope_type: 'all', quality_policy_id: 'default', quality_policy_revision: 1, title: 'Original title' };
const storage = () => {
  const values = new Map();
  return { values, getItem: k => values.get(k) ?? null, setItem(k, v) { values.set(k, v); }, removeItem(k) { values.delete(k); } };
};
const never = async () => assert.fail('unexpected API request');
const saved = s => s.getItem(m.subscriptionRecoveryKey(userId));

test('whitelist excludes hints and secrets while preserving original indexer ordering and optional omission', () => {
  const s = storage();
  m.saveSubscriptionRecovery(s, { ...record, token: 'TOKEN', body: { ...body, title: 'PRIVATE', year: 2001, query: 'QUERY', token: 'TOKEN', session: 'SESSION', url: 'https://secret', credentials: 'PASSWORD' } });
  assert.deepEqual(m.parseSubscriptionRecovery(saved(s), userId), record);
  assert(!/PRIVATE|QUERY|TOKEN|SESSION|secret|PASSWORD|title|year|query|token|session|url|credentials/.test(saved(s)));
  const { indexer_ids, media_type, expected_intent_version, ...omitted } = body;
  const clean = m.cleanSubscriptionIntent(omitted);
  assert(!Object.hasOwn(clean, 'indexer_ids'));
  assert(!Object.hasOwn(clean, 'media_type'));
  assert(!Object.hasOwn(clean, 'expected_intent_version'));
  assert.deepEqual(m.cleanSubscriptionIntent({ ...omitted, indexer_ids: [] }).indexer_ids, []);
  assert.deepEqual(m.cleanSubscriptionIntent({ ...omitted, media_id: uuid(99), tmdb_id: undefined }).media_id, uuid(99));
});

test('storage strictly rejects bad shape, key, identity, ids, policy, versions and extra body fields', () => {
  const raw = v => JSON.stringify(v);
  for (const value of ['{', 'null', '[]', '42', raw({ ...record, version: 2 }), raw({ ...record, userId: otherUser }), raw({ ...record, key: '' }), raw({ ...record, key: 'x'.repeat(161) }), raw({ ...record, key: '中文' }), raw({ ...record, key: 'a\nb' }), raw({ ...record, token: 'secret' })]) assert.throws(() => m.parseSubscriptionRecovery(value, userId));
  for (const patch of [{ root_id: 'bad' }, { media_id: 'bad' }, { downloader_id: 'https://secret' }, { indexer_ids: [uuid(6), uuid(6)] }, { indexer_ids: null }, { selected_unit_ids: ['bad'] }, { expected_intent_version: 0 }, { expected_intent_version: 1.5 }, { expected_intent_version: Number.MAX_SAFE_INTEGER + 1 }, { tmdb_id: -1 }, { media_type: 'multi' }, { scope_type: 'unknown' }, { seasons: [-1] }, { episodes: ['query'] }, { quality_policy_id: 'https://secret' }, { include_specials: 'true' }, { title: 'private' }, { query: 'secret' }]) assert.throws(() => m.parseSubscriptionRecovery(raw({ ...record, body: { ...body, ...patch } }), userId));
  assert.throws(() => m.parseSubscriptionRecovery(null, 'bad-owner'));
  assert.equal(m.parseSubscriptionRecovery(null, userId), null);
  assert.notEqual(m.subscriptionRecoveryKey(userId), m.subscriptionRecoveryKey(otherUser));
});

test('season filters use all; only explicit episode keys use selected', () => {
  assert.deepEqual(m.subscriptionScope('selected', [2], []), { scope_type: 'all', seasons: [2] });
  assert.deepEqual(m.subscriptionScope('selected', [2], ['S02E03']), { scope_type: 'selected', seasons: [2], episodes: ['S02E03'] });
  assert.deepEqual(m.subscriptionScope('selected', [], ['S00E01']), { scope_type: 'selected', episodes: ['S00E01'] });
  assert.deepEqual(m.subscriptionScope('all', [2], ['S02E03']), { scope_type: 'all' });
});

test('initial command verifies persistence before POST; network failure preserves exact body and key', async () => {
  const s = storage();
  const outcome = await m.subscriptionCommand({ record, storage: s, fresh: true, active: () => true, lookup: never, send: async () => { assert.deepEqual(m.parseSubscriptionRecovery(saved(s), userId), record); throw Error('lost after commit'); } });
  assert.equal(outcome.phase, 'unknown');
  assert.deepEqual(m.parseSubscriptionRecovery(saved(s), userId), record);
  for (const broken of [{ ...storage(), setItem() { throw Error('quota'); } }, { ...storage(), setItem() {} }]) assert.equal((await m.subscriptionCommand({ record, storage: broken, fresh: true, active: () => true, lookup: never, send: never })).phase, 'blocked');
});

test('reload lookup never posts on 404 or transient read failure; explicit original retry checks GET first', async () => {
  const s = storage(); m.saveSubscriptionRecovery(s, record);
  const restored = m.parseSubscriptionRecovery(saved(s), userId);
  for (const status of [404, 503, 409, 401, 403]) {
    const outcome = await m.subscriptionCommand({ record: restored, storage: s, active: () => true, lookup: async () => ({ status }), send: never });
    assert.equal(outcome.phase, status === 404 ? 'missing' : status === 409 ? 'conflict' : [401, 403].includes(status) ? 'auth' : 'unknown');
    assert.deepEqual(m.parseSubscriptionRecovery(saved(s), userId), record);
  }
  const calls = [];
  const outcome = await m.subscriptionCommand({ record: restored, storage: s, retryOriginal: true, active: () => true, lookup: async () => { calls.push('GET'); return { status: 404 }; }, send: async () => { calls.push('POST'); assert.deepEqual(restored, record); return { status: 201, data: result }; } });
  assert.equal(outcome.phase, 'succeeded'); assert.deepEqual(calls, ['GET', 'POST']);
});

test('storage altered during original lookup blocks replay without replacing any pending work', async () => {
  for (const replace of [() => null, () => '{', () => JSON.stringify({ ...record, key: uuid(90) }), () => JSON.stringify({ ...record, body: { ...body, root_id: uuid(99) } })]) {
    const s = storage(); m.saveSubscriptionRecovery(s, record); let changed;
    const outcome = await m.subscriptionCommand({ record, storage: s, retryOriginal: true, active: () => true,
      lookup: async () => { changed = replace(); if (changed === null) s.removeItem(m.subscriptionRecoveryKey(userId)); else s.setItem(m.subscriptionRecoveryKey(userId), changed); return { status: 404 }; }, send: never });
    assert.equal(outcome.phase, 'blocked'); assert.equal(saved(s), changed);
  }
});

// Exercise the actual React hook with deterministic slots, effects, auth and APIs.
async function hookHost(s = storage(), initialUser = { id: userId }) {
  const slots = [], effects = [], calls = [], pending = [], successes = [];
  let cursor = 0, user = initialUser, out;
  const equal = (a, b) => a && b && a.length === b.length && a.every((v, i) => Object.is(v, b[i]));
  const host = { ...m, sessionStorage: s, crypto, useAuth: () => ({ user }),
    useRef(value) { const i = cursor++; return slots[i] ?? (slots[i] = { current: value }); },
    useState(initial) { const i = cursor++; if (!slots[i]) slots[i] = { value: typeof initial === 'function' ? initial() : initial }; return [slots[i].value, value => { slots[i].value = typeof value === 'function' ? value(slots[i].value) : value; }]; },
    useEffect(fn, deps) { const i = cursor++, old = slots[i]; if (!old || !equal(old.deps, deps)) effects.push(() => { old?.cleanup?.(); slots[i] = { deps, cleanup: fn() }; }); },
    api: Object.fromEntries(['GET', 'POST'].map(method => [method, (path, options) => { calls.push({ method, path, options }); return new Promise((resolve, reject) => pending.push({ resolve, reject })); }])),
  };
  const name = `__subscription_test_${crypto.randomUUID().replaceAll('-', '')}`;
  globalThis[name] = host;
  let useSubscriptionCreation;
  try {
    const source = readFileSync(new URL('useSubscriptionCreation.ts', root), 'utf8').replace(/^import[\s\S]*?;\n/gm, '');
    ({ useSubscriptionCreation } = await load(`const {${Object.keys(host).join(',')}}=globalThis[${JSON.stringify(name)}];\n${source}`));
  } finally { delete globalThis[name]; }
  const render = () => { cursor = 0; out = useSubscriptionCreation({ onSuccess: (data, intent) => successes.push({ data, intent }) }); for (const effect of effects.splice(0)) effect(); return out; };
  const flush = async () => { for (let i = 0; i < 16; i++) await Promise.resolve(); return render(); };
  render(); await flush();
  return { s, calls, pending, successes, render, flush, get out() { return out; }, auth(value) { user = value; return render(); }, reply(status, data) { assert(pending.length); pending.shift().resolve({ response: { status }, data }); }, fail() { assert(pending.length); pending.shift().reject(Error('offline')); }, unmount() { for (const slot of slots) slot?.cleanup?.(); } };
}

test('actual hook deduplicates double click, snapshots body and clears pending only after actual 201', async () => {
  const h = await hookHost(); try {
    const input = structuredClone(body), first = h.out.start(input), original = h.calls.at(-1);
    input.seasons.push(9); input.indexer_ids.reverse();
    assert.equal(h.successes.length, 0); assert.equal(await h.out.start({ ...body, tmdb_id: 999 }), false);
    assert.equal(h.calls.length, 1); assert.equal(original.method, 'POST');
    assert.deepEqual(original.options.body, body); assert.match(original.options.params.header['Idempotency-Key'], /^[\x20-\x7e]{1,160}$/);
    assert.deepEqual(JSON.parse(saved(h.s)).body, body);
    h.reply(201, result); assert.equal(await first, true); await h.flush();
    assert.equal(saved(h.s), null); assert.deepEqual(h.successes, [{ data: result, intent: body }]); assert.equal(h.out.recovery, null);
  } finally { h.unmount(); }
});

test('actual reload and reauthentication GET original key only, then explicit retry uses exact original POST', async () => {
  const s = storage(); let h = await hookHost(s);
  const first = h.out.start(body), original = h.calls.at(-1); h.fail(); await first; await h.flush(); h.unmount();
  h = await hookHost(s); try {
    assert.equal(h.calls.length, 1); assert.equal(h.calls[0].method, 'GET');
    assert.equal(h.calls[0].options.params.path.idempotency_key, original.options.params.header['Idempotency-Key']);
    h.reply(404); await h.flush(); assert.equal(h.out.phase, 'missing'); assert.equal(h.out.canRetryOriginal, true); assert.equal(h.successes.length, 0);
    const newTarget = h.out.start({ ...body, tmdb_id: 999, root_id: uuid(90) }); assert.equal(h.calls.at(-1).method, 'GET'); h.reply(404); await newTarget; await h.flush();
    const retry = h.out.retryOriginal(); assert.equal(h.calls.at(-1).method, 'GET'); h.reply(404); await h.flush();
    assert.equal(h.calls.at(-1).method, 'POST'); assert.deepEqual(h.calls.at(-1).options.body, original.options.body); assert.deepEqual(h.calls.at(-1).options.params.header, original.options.params.header);
    h.reply(201, result); assert.equal(await retry, true); await h.flush(); assert.equal(h.successes.length, 1); assert.equal(saved(s), null);
  } finally { h.unmount(); }
});

test('GET stable original snapshot confirms success and callback only once', async () => {
  const s = storage(); m.saveSubscriptionRecovery(s, record);
  const h = await hookHost(s); try {
    assert.equal(h.successes.length, 0); h.reply(200, result); await h.flush();
    assert.deepEqual(h.successes, [{ data: result, intent: body }]); assert.equal(saved(s), null);
    assert.equal(await h.out.recover(), false); assert.equal(h.calls.length, 1);
  } finally { h.unmount(); }
});

for (const status of [401, 403]) test(`actual ${status} pauses all commands and resumes same-owner GET after reauth`, async () => {
  const h = await hookHost(); try {
    const first = h.out.start(body); h.reply(status); await first; await h.flush();
    assert.equal(h.out.phase, 'auth'); assert.equal(h.out.canRecover, false); assert.equal(h.out.canRetryOriginal, false); assert.equal(h.successes.length, 0);
    const count = h.calls.length; await h.out.start(body); await h.out.recover(); await h.out.retryOriginal(); assert.equal(h.calls.length, count); assert(saved(h.s));
    h.auth(null); assert.equal(h.out.recovery, null); h.auth({ id: userId }); await h.flush(); assert.equal(h.calls.at(-1).method, 'GET');
    h.reply(200, result); await h.flush(); assert.equal(h.successes.length, 1);
  } finally { h.unmount(); }
});

for (const lateStatus of [201, 403]) test(`actor switch fences late ${lateStatus}, aborts own request, keeps original record and other owner work`, async () => {
  const h = await hookHost(); try {
    const first = h.out.start(body), firstCall = h.calls[0];
    h.auth({ id: otherUser }); assert.equal(firstCall.options.signal.aborted, true); assert.equal(h.out.recovery, null);
    const second = h.out.start({ ...body, tmdb_id: 456 }), otherCall = h.calls.at(-1);
    h.reply(lateStatus, lateStatus === 201 ? result : undefined); await first; await h.flush();
    assert.equal(h.successes.length, 0); assert.equal(otherCall.options.signal.aborted, false); assert.equal(h.out.phase, 'creating');
    h.reply(201, { ...result, user_id: otherUser }); await second; await h.flush(); assert.equal(h.successes.length, 1); assert(saved(h.s));
    h.auth({ id: userId }); await h.flush(); assert.equal(h.calls.at(-1).method, 'GET');
    h.reply(200, result); await h.flush(); assert.equal(h.successes.length, 2); assert.equal(h.calls.filter(c => c.method === 'POST').length, 2);
  } finally { h.unmount(); }
});

for (const status of [400, 409]) test(`actual ${status} retains rejected root and version, requires reads and never overwrites target`, async () => {
  const h = await hookHost(); try {
    const first = h.out.start(body), original = h.calls[0]; h.reply(status); await first; await h.flush();
    assert.equal(h.out.phase, status === 400 ? 'rejected' : 'conflict'); assert.equal(h.out.canRetryOriginal, false); assert.match(h.out.message, /核对/);
    const retry = h.out.retryOriginal(); assert.equal(h.calls.at(-1).method, 'GET'); h.reply(503); await retry; await h.flush();
    assert.equal(h.out.phase, 'unknown'); assert.equal(h.calls.filter(c => c.method === 'POST').length, 1);
    const changed = h.out.start({ ...body, root_id: uuid(99), expected_intent_version: 900 }); assert.equal(h.calls.at(-1).method, 'GET'); h.reply(404); await changed; await h.flush();
    assert.deepEqual(h.out.recovery.body, original.options.body); assert.deepEqual(JSON.parse(saved(h.s)).body, body);
    const exact = h.out.retryOriginal(); h.reply(404); await h.flush(); assert.deepEqual(h.calls.at(-1).options.body, body); assert.deepEqual(h.calls.at(-1).options.params.header, original.options.params.header);
    h.reply(status); await exact; await h.flush(); assert.equal(h.successes.length, 0);
  } finally { h.unmount(); }
});

test('corrupted or unavailable storage blocks real hook requests', async () => {
  const corrupt = storage(); corrupt.setItem(m.subscriptionRecoveryKey(userId), '{');
  for (const s of [corrupt, { ...storage(), getItem() { throw Error('disabled'); } }]) {
    const h = await hookHost(s); try {
      assert.equal(h.out.phase, 'blocked'); await h.out.start(body); await h.out.recover(); assert.equal(h.calls.length, 0); assert.equal(h.successes.length, 0);
    } finally { h.unmount(); }
  }
  const h = await hookHost({ ...storage(), setItem() { throw Error('quota'); } }); try {
    assert.equal(await h.out.start(body), false); await h.flush(); assert.equal(h.out.phase, 'blocked'); assert.equal(h.calls.length, 0);
  } finally { h.unmount(); }
});

test('read failure and unmount preserve pending, removed or changed in-memory original fails closed', async () => {
  const h = await hookHost(); try {
    const first = h.out.start(body); h.fail(); await first; await h.flush();
    const read = h.out.recover(); h.fail(); await read; await h.flush(); assert.equal(h.out.phase, 'unknown'); assert(saved(h.s));
    h.s.setItem(m.subscriptionRecoveryKey(userId), JSON.stringify({ ...JSON.parse(saved(h.s)), body: { ...body, root_id: uuid(99) } }));
    const count = h.calls.length; await h.out.recover(); await h.flush(); assert.equal(h.out.phase, 'blocked'); assert.equal(h.calls.length, count);
    h.s.removeItem(m.subscriptionRecoveryKey(userId)); await h.out.start(body); await h.flush(); assert.equal(h.calls.length, count);
  } finally { h.unmount(); }
  const s = storage(), late = await hookHost(s), first = late.out.start(body), call = late.calls[0];
  late.unmount(); assert.equal(call.options.signal.aborted, true); late.reply(201, result); await first;
  assert.equal(late.successes.length, 0); assert(saved(s));
});

test('foreign owner or malformed success cannot clear pending or emit success', async () => {
  for (const patch of [{ user_id: otherUser }, { id: 'bad' }, { root_id: 'bad' }, { intent_version: 0 }]) {
    const h = await hookHost(); try {
      const first = h.out.start(body); h.reply(201, { ...result, ...patch }); await first; await h.flush();
      assert.equal(h.out.phase, 'unknown'); assert.equal(h.successes.length, 0); assert(saved(h.s));
    } finally { h.unmount(); }
  }
});

// Execute each real caller function directly, using the existing TypeScript test loader.
for (const [file, name] of [['DiscoverView.tsx', 'subscribeMedia'], ['DashboardView.tsx', 'handleSubscribe']]) test(`${file} actual callback preserves binding choices and fixes season/episode scopes`, async () => {
  const source = readFileSync(new URL(`../src/views/${file}`, import.meta.url), 'utf8');
  const ast = ts.createSourceFile(file, source, ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX);
  let declaration;
  function visit(node) { if (ts.isVariableDeclaration(node) && node.name.getText(ast) === name) declaration = node.getText(ast); ts.forEachChild(node, visit); }
  visit(ast); assert(declaration); assert(source.includes('useSubscriptionCreation('));
  const calls = [], closes = [], errors = [];
  const token = `__caller_test_${crypto.randomUUID().replaceAll('-', '')}`;
  globalThis[token] = { subscriptionCreation: { start(input) { calls.push(input); return Promise.resolve(false); } }, subscriptionScope: m.subscriptionScope, setShowDetailModal: value => closes.push(value), toast: { error: (...args) => errors.push(args) } };
  let fn;
  try { fn = (await load(`const {subscriptionCreation,subscriptionScope,setShowDetailModal,toast}=globalThis[${JSON.stringify(token)}]; export const ${declaration};`))[name]; }
  finally { delete globalThis[token]; }
  const media = { tmdb_id: 123, media_type: 'tv', title: 'PRIVATE', year: 2020 };
  fn(media, 'selected', [2], [], true, uuid(7), [uuid(6), uuid(5)], 'policy-2', uuid(4));
  assert.deepEqual(calls.at(-1), { tmdb_id: 123, media_type: 'tv', scope_type: 'all', seasons: [2], quality_policy_id: 'policy-2', root_id: uuid(4), include_specials: true, downloader_id: uuid(7), indexer_ids: [uuid(6), uuid(5)] });
  fn(media, 'selected', [2], ['S02E03'], false, uuid(7), [], 'default', uuid(4));
  assert.equal(calls.at(-1).scope_type, 'selected'); assert.deepEqual(calls.at(-1).episodes, ['S02E03']); assert.deepEqual(calls.at(-1).indexer_ids, []);
  fn({ tmdb_id: 9 }, 'all'); assert.equal(calls.at(-1).indexer_ids, undefined); assert.equal(calls.at(-1).media_type, undefined); assert.equal(calls.at(-1).expected_intent_version, undefined);
  assert(!Object.hasOwn(calls[0], 'title')); assert(!Object.hasOwn(calls[0], 'year')); assert(closes.every(value => value === false));
  const count = calls.length; fn({ ...media, media_type: 'multi' }); assert.equal(calls.length, count); assert.equal(errors.length, 1);
});
