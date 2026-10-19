import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import ts from 'typescript';

const root = new URL('../src/features/configuration/', import.meta.url);
const compile = text => ts.transpileModule(text, { compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.ESNext } }).outputText;
const load = text => import(`data:text/javascript;base64,${Buffer.from(compile(text)).toString('base64')}#${crypto.randomUUID()}`);
const m = await load(readFileSync(new URL('episodeControlRecovery.ts', root), 'utf8'));
const uuid = n => `00000000-0000-4000-8000-${String(n).padStart(12, '0')}`;
const userId = uuid(1), subscriptionId = uuid(2), unitId = uuid(3);
const body = { expected_version: 8, unit_id: unitId, action: 'skip', skipped: true };
const record = { version: 1, userId, subscriptionId, key: uuid(4), body };
const detail = { id: subscriptionId, user_id: userId, media_id: uuid(5), root_id: uuid(6), intent_version: 9, state: 'active', scope_type: 'all', quality_policy_id: 'default', quality_policy_revision: 1, selected_unit_ids: [unitId] };
const receipt = { id: subscriptionId, intent_version: 9, unit_id: unitId, skipped: true, state: 'active' };
const storage = () => { const values = new Map(); return { values, getItem: k => values.get(k) ?? null, setItem(k, v) { values.set(k, v); }, removeItem(k) { values.delete(k); } }; };
const key = (id = userId, role = 'member', sub = subscriptionId) => m.episodeControlRecoveryKey(id, role, sub);

// Same deterministic React slot/effect harness used by creation/search tests, executing the real hook.
async function hookHost(s = storage(), initial = {}) {
  const slots = [], effects = [], calls = [], pending = [], updates = [];
  let cursor = 0, out, user = { id: userId, role: 'member' }, options = { subscriptionId, expectedVersion: 8, onUpdated: value => updates.push(value), ...initial };
  const equal = (a, b) => a && b && a.length === b.length && a.every((v, i) => Object.is(v, b[i]));
  const transport = method => (path, options) => { calls.push({ method, path, options }); return new Promise((resolve, reject) => pending.push({ resolve, reject })); };
  const host = { ...m, sessionStorage: s, crypto, api: { GET: transport('GET'), PUT: transport('PUT') }, useAuth: () => ({ user }),
    useRef(value) { const i = cursor++; return slots[i] ??= { current: value }; },
    useState(value) { const i = cursor++; if (!(i in slots)) slots[i] = typeof value === 'function' ? value() : value; return [slots[i], next => { slots[i] = typeof next === 'function' ? next(slots[i]) : next; }]; },
    useEffect(fn, deps) { const i = cursor++, old = slots[i]; if (!equal(old?.deps, deps)) effects.push(() => { old?.cleanup?.(); slots[i] = { deps, cleanup: fn() }; }); },
  };
  const token = `__episode_control_${crypto.randomUUID().replaceAll('-', '')}`;
  globalThis[token] = host;
  const source = readFileSync(new URL('useEpisodeControl.ts', root), 'utf8');
  const ast = ts.createSourceFile('hook.ts', source, ts.ScriptTarget.Latest, true);
  const imports = ast.statements.filter(ts.isImportDeclaration);
  let stripped = source;
  for (const node of [...imports].reverse()) stripped = stripped.slice(0, node.getFullStart()) + stripped.slice(node.end);
  const prelude = `const {useRef,useState,useEffect,api,useAuth,sessionStorage,crypto,${Object.keys(m).join(',')}}=globalThis[${JSON.stringify(token)}];\n`;
  let hook;
  try { hook = (await load(prelude + stripped)).useEpisodeControl; } finally { delete globalThis[token]; }
  const render = () => { cursor = 0; out = hook(options); for (const effect of effects.splice(0)) effect(); return out; };
  const flush = async () => { for (let i = 0; i < 16; i++) await Promise.resolve(); return render(); };
  render(); await flush();
  return { calls, pending, updates, s, flush, get out() { return out; },
    auth(value) { user = value; return render(); }, options(patch) { options = { ...options, ...patch }; return render(); },
    reply(status, data) { assert(pending.length, 'request available'); pending.shift().resolve({ response: { status }, data, error: status >= 400 ? {} : undefined }); },
    fail() { assert(pending.length); pending.shift().reject(Error('lost response')); },
    unmount() { for (const slot of slots) slot?.cleanup?.(); },
  };
}

test('helper whitelist, validation, role/subscription isolation and independent clones', () => {
  const s = storage();
  m.saveEpisodeControlRecovery(s, { ...record, token: 'SECRET', cookie: 'SECRET' }, 'member', true);
  assert.deepEqual(JSON.parse(s.getItem(key())), record);
  assert(!s.getItem(key()).includes('SECRET'));
  const parsed = m.parseEpisodeControlRecovery(s.getItem(key()), userId, subscriptionId);
  parsed.body.expected_version = 999;
  assert.deepEqual(m.parseEpisodeControlRecovery(s.getItem(key()), userId, subscriptionId), record);
  assert.notEqual(key(), key(userId, 'admin')); assert.notEqual(key(), key(uuid(9))); assert.notEqual(key(), key(userId, 'member', uuid(9)));
  for (const value of ['{', 'null', '[]', JSON.stringify({ ...record, token: 'SECRET' }), JSON.stringify({ ...record, userId: uuid(9) }), JSON.stringify({ ...record, key: 'bad' }), JSON.stringify({ ...record, version: 2 })]) assert.throws(() => m.parseEpisodeControlRecovery(value, userId, subscriptionId));
  for (const patch of [{ expected_version: 0 }, { expected_version: 1.5 }, { expected_version: Number.MAX_SAFE_INTEGER + 1 }, { unit_id: 'bad' }, { skipped: 'true' }, { action: 'resume' }, { token: 'SECRET' }, { action: 'recheck', skipped: true }]) assert.throws(() => m.cleanEpisodeControlBody({ ...body, ...patch }));
  assert.throws(() => m.saveEpisodeControlRecovery(s, { ...record, body: { ...body, expected_version: 99 } }, 'member'));
  assert.throws(() => m.clearEpisodeControlRecovery(s, { ...record, key: uuid(9) }, 'member'));
  assert.deepEqual(JSON.parse(s.getItem(key())), record);
});

for (const action of ['skip', 'resume', 'recheck']) test(`actual ${action}: persist/readback before PUT, frozen request and confirmed detail commit`, async () => {
  const h = await hookHost();
  try {
    assert.equal(h.calls.length, 0);
    const task = h.out.execute(unitId, action), call = h.calls[0];
    const expected = action === 'recheck' ? { expected_version: 8, unit_id: unitId, action: 'recheck' } : { ...body, skipped: action === 'skip' };
    assert.equal(call.method, 'PUT'); assert.equal(call.path, '/api/v1/subscriptions/{id}/control');
    assert.deepEqual(call.options.body, expected);
    assert.deepEqual(JSON.parse(h.s.getItem(key())).body, expected);
    assert.equal(call.options.params.header['Idempotency-Key'], JSON.parse(h.s.getItem(key())).key);
    await h.flush(); assert.equal(h.out.busy, true); assert.equal(h.out.blocked, true);
    const publicPending = h.out.pending; publicPending.body.expected_version = 999;
    assert.equal(JSON.parse(h.s.getItem(key())).body.expected_version, 8);
    assert.equal(await h.out.execute(uuid(9), 'recheck'), false);
    h.reply(200, { ...receipt, skipped: action === 'skip', intent_version: action === 'recheck' ? 8 : 9 }); await h.flush();
    assert.equal(h.s.getItem(key()), null); assert.equal(h.calls.at(-1).method, 'GET');
    assert.equal(await h.out.execute(unitId, 'skip'), false);
    const callbacks = []; h.options({ onUpdated: sub => { callbacks.push(sub); sub.selected_unit_ids.push(uuid(9)); } });
    h.reply(200, detail); assert.equal(await task, true); await h.flush();
    assert.equal(h.out.feedbackKind, 'confirmed'); assert.equal(h.out.blocked, false); assert.equal(h.out.canRetry, false);
    assert.equal(h.updates.length, 0); assert.equal(callbacks.length, 1); assert.deepEqual(detail.selected_unit_ids, [unitId]);
  } finally { h.unmount(); }
});

test('lost PUT reload never sends automatically; refresh preserves pending, explicit retry keeps exact key/body despite changed options', async () => {
  const s = storage(), first = await hookHost(s);
  const task = first.out.execute(unitId, 'skip'), original = structuredClone(first.calls[0].options);
  first.fail(); assert.equal(await task, false); await first.flush(); assert.equal(first.out.canRetry, true); first.unmount();
  const h = await hookHost(s, { expectedVersion: 600 });
  try {
    assert.equal(h.calls.length, 0); assert.equal(h.out.pending.body.expected_version, 8);
    assert.equal(await h.out.execute(uuid(9), 'recheck'), false);
    const read = h.out.refresh(); h.reply(200, { ...detail, intent_version: 600 }); await read; await h.flush();
    assert.deepEqual(h.out.pending.body, body); assert(s.getItem(key())); assert.equal(h.out.canRetry, true);
    const retry = h.out.retryOriginal(); assert.equal(h.calls.at(-1).method, 'PUT');
    assert.deepEqual(h.calls.at(-1).options.body, original.body); assert.deepEqual(h.calls.at(-1).options.params.header, original.params.header);
    h.reply(200, receipt); await h.flush(); h.reply(200, detail); await retry; await h.flush(); assert.equal(h.out.pending, null);
    assert.equal(h.calls.some(c => c.path.includes('commands')), false);
  } finally { h.unmount(); }
});

for (const status of [401, 403, 404, 409]) test(`PUT ${status} preserves original version and pauses unsafe mutations even after detail refresh`, async () => {
  const h = await hookHost();
  try {
    const task = h.out.execute(unitId, 'skip'); h.reply(status); await task; await h.flush();
    assert.equal(h.out.blocked, true); assert.equal(h.out.canRetry, false); assert.deepEqual(h.out.pending.body, body);
    await h.out.execute(unitId, 'resume'); await h.out.retryOriginal(); assert.equal(h.calls.length, 1);
    const read = h.out.refresh(); h.reply(200, { ...detail, intent_version: 500 }); await read; await h.flush();
    assert.deepEqual(h.out.pending.body, body); assert.deepEqual(JSON.parse(h.s.getItem(key())).body, body);
    await h.out.retryOriginal(); assert.equal(h.calls.length, 2);
  } finally { h.unmount(); }
});

for (const status of [401, 403, 404, 409]) test(`detail ${status} preserves pending and pauses retry`, async () => {
  const s = storage(); m.saveEpisodeControlRecovery(s, record, 'member', true);
  const h = await hookHost(s);
  try {
    const read = h.out.refresh(); h.reply(status); await read; await h.flush();
    assert.deepEqual(h.out.pending, record); assert.equal(h.out.canRetry, false);
    await h.out.retryOriginal(); assert.equal(h.calls.filter(c => c.method === 'PUT').length, 0);
  } finally { h.unmount(); }
});

test('confirmed PUT followed by failed GET allows refresh only and never repeats mutation', async () => {
  const h = await hookHost();
  try {
    const task = h.out.execute(unitId, 'skip'); h.reply(200, receipt); await h.flush(); h.fail(); assert.equal(await task, true); await h.flush();
    assert.equal(h.out.pending, null); assert.equal(h.s.getItem(key()), null); assert.equal(h.out.blocked, true); assert.equal(h.out.feedbackKind, 'error');
    await h.out.retryOriginal(); await h.out.execute(unitId, 'resume'); assert.equal(h.calls.length, 2);
    const read = h.out.refresh(); h.reply(200, detail); await read; await h.flush(); assert.equal(h.out.blocked, false); assert.equal(h.updates.length, 1);
    assert.equal(h.calls.filter(c => c.method === 'PUT').length, 1);
  } finally { h.unmount(); }
});

for (const change of ['account', 'role', 'subscription']) test(`${change} switch aborts and fences late PUT/detail; old journal stays scoped`, async () => {
  for (const stage of ['PUT', 'GET']) {
    const h = await hookHost();
    try {
      const task = h.out.execute(unitId, 'skip');
      if (stage === 'GET') { h.reply(200, receipt); await h.flush(); }
      const active = h.calls.at(-1);
      if (change === 'account') h.auth({ id: uuid(9), role: 'member' });
      else if (change === 'role') h.auth({ id: userId, role: 'admin' });
      else h.options({ subscriptionId: uuid(9) });
      await h.flush(); assert.equal(active.options.signal.aborted, true); assert.equal(h.out.pending, null);
      h.reply(200, stage === 'PUT' ? receipt : detail); await task; await h.flush();
      assert.equal(h.updates.length, 0); assert.equal(h.calls.length, stage === 'PUT' ? 1 : 2);
      if (stage === 'PUT') assert(h.s.getItem(key()));
      if (change !== 'subscription') h.auth({ id: userId, role: 'member' }); else h.options({ subscriptionId });
      await h.flush(); assert.equal(h.calls.length, stage === 'PUT' ? 1 : 2);
      if (stage === 'PUT') assert.deepEqual(h.out.pending.body, body);
    } finally { h.unmount(); }
  }
});

test('storage failures and tampering prevent all unsafe PUTs', async () => {
  for (const s of [
    { ...storage(), getItem() { throw Error('disabled'); } },
    { ...storage(), setItem() { throw Error('quota'); } },
    { ...storage(), setItem() {} },
    (() => { const s = storage(); s.setItem(key(), '{'); return s; })(),
  ]) {
    const h = await hookHost(s);
    try { await h.out.execute(unitId, 'skip'); await h.out.retryOriginal(); await h.flush(); assert.equal(h.calls.length, 0); assert.equal(h.out.blocked, true); }
    finally { h.unmount(); }
  }
  for (const value of [null, JSON.stringify({ ...record, body: { ...body, expected_version: 500 } })]) {
    const s = storage(); m.saveEpisodeControlRecovery(s, record, 'member', true); const h = await hookHost(s);
    try {
      if (value === null) s.removeItem(key()); else s.setItem(key(), value);
      await h.out.retryOriginal(); await h.out.execute(unitId, 'resume'); await h.flush(); assert.equal(h.calls.length, 0); assert.equal(h.out.blocked, true);
    } finally { h.unmount(); }
  }
});

test('malformed receipts retain journal; confirmed cleanup failure forbids retry', async () => {
  for (const patch of [{ id: uuid(9) }, { unit_id: uuid(9) }, { intent_version: 500 }, { skipped: false }, { state: 'bad' }]) {
    const h = await hookHost();
    try { const task = h.out.execute(unitId, 'skip'); h.reply(200, { ...receipt, ...patch }); assert.equal(await task, false); await h.flush(); assert(h.s.getItem(key())); assert.equal(h.updates.length, 0); }
    finally { h.unmount(); }
  }
  const s = { ...storage(), removeItem() { throw Error('disabled'); } }, h = await hookHost(s);
  try {
    const task = h.out.execute(unitId, 'skip'); h.reply(200, receipt); assert.equal(await task, true); await h.flush();
    assert.equal(h.out.canRetry, false); await h.out.retryOriginal(); await h.out.execute(unitId, 'skip'); assert.equal(h.calls.length, 1);
    const read = h.out.refresh(); h.reply(200, detail); await read; await h.flush(); assert.equal(h.out.blocked, true); assert(s.getItem(key()));
  } finally { h.unmount(); }
});

test('explicit 409 resolution requires a new successful detail read, keeps key on failure and sends no mutation', async () => {
  const h = await hookHost();
  try {
    const send = h.out.execute(unitId, 'skip'); const original = JSON.parse(h.s.getItem(key()));
    h.reply(409); await send; await h.flush(); assert.equal(h.out.canResolveConflict, true);
    const failed = h.out.resolveConflict(); h.reply(503); assert.equal(await failed, false); await h.flush();
    assert.deepEqual(JSON.parse(h.s.getItem(key())), original); assert.equal(h.out.blocked, true);
    const resolve = h.out.resolveConflict(); h.reply(200, detail); assert.equal(await resolve, true); await h.flush();
    assert.equal(h.s.getItem(key()), null); assert.equal(h.out.pending, null); assert.equal(h.out.blocked, false); assert.equal(h.out.canResolveConflict, false);
    assert.equal(h.calls.filter(c => c.method === 'PUT').length, 1); assert.equal(h.updates.length, 1);
    h.options({ expectedVersion: 9 }); const next = h.out.execute(unitId, 'resume');
    assert.notEqual(h.calls.at(-1).options.params.header['Idempotency-Key'], original.key); assert.equal(h.calls.at(-1).options.body.expected_version, 9);
    h.reply(200, { ...receipt, skipped: false, intent_version: 10 }); await h.flush(); h.reply(200, { ...detail, intent_version: 10 }); await next;
  } finally { h.unmount(); }
});

test('unknown results and denied requests cannot be discarded through conflict resolution', async () => {
  for (const status of [null, 401, 403, 404]) {
    const h = await hookHost();
    try {
      const send = h.out.execute(unitId, 'skip'); const original = h.s.getItem(key());
      if (status === null) h.fail(); else h.reply(status);
      await send; await h.flush(); assert.equal(h.out.canResolveConflict, false);
      assert.equal(await h.out.resolveConflict(), false); assert.equal(h.calls.length, 1); assert.equal(h.s.getItem(key()), original);
    } finally { h.unmount(); }
  }
});

test('administrator may commit a detail read for the subscription owner permitted by the backend', async () => {
  const h = await hookHost();
  try {
    h.auth({ id: userId, role: 'admin' }); await h.flush();
    const send = h.out.execute(unitId, 'skip'); h.reply(200, receipt); await h.flush();
    h.reply(200, { ...detail, user_id: uuid(99) }); await send; await h.flush();
    assert.equal(h.updates.length, 1); assert.equal(h.updates[0].user_id, uuid(99)); assert.equal(h.out.blocked, false);
  } finally { h.unmount(); }
});

test('member never publishes a detail read belonging to another owner', async () => {
  const h = await hookHost();
  try {
    const send = h.out.execute(unitId, 'skip'); h.reply(200, receipt); await h.flush();
    h.reply(200, { ...detail, user_id: uuid(99) }); await send; await h.flush();
    assert.equal(h.updates.length, 0); assert.equal(h.out.blocked, true); assert.equal(h.out.canResolveConflict, false);
  } finally { h.unmount(); }
});

test('unmount aborts physical request and ignores its late receipt', async () => {
  const h = await hookHost(), task = h.out.execute(unitId, 'skip'); h.unmount();
  assert.equal(h.calls[0].options.signal.aborted, true); h.reply(200, receipt); await task;
  assert(h.s.getItem(key())); assert.equal(h.updates.length, 0);
});
