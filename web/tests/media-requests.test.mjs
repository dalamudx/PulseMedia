import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import ts from 'typescript';

// Same in-memory transpilation and deterministic React-slot host as download-global-rates.test.mjs.
// Execute the production hook and JSX, with controlled transport and browser ownership boundaries.
const source = name => readFileSync(new URL(`../src/${name}`, import.meta.url), 'utf8');
const feature = name => source(`features/media-requests/${name}`);
const tick = async () => { for (let i = 0; i < 24; i++) await Promise.resolve(); };
async function injected(text, values) {
  const key = `__media_requests_${crypto.randomUUID().replaceAll('-', '')}`;
  globalThis[key] = values;
  const code = `const {${Object.keys(values).join(',')}}=globalThis[${JSON.stringify(key)}];\n${text.replace(/^import[\s\S]*?;\n/gm, '')}`;
  try { return await import(`data:text/javascript;base64,${Buffer.from(ts.transpileModule(code, { compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.ESNext, jsx: ts.JsxEmit.React, jsxFactory: 'h', jsxFragmentFactory: 'Fragment' } }).outputText).toString('base64')}#${crypto.randomUUID()}`); }
  finally { delete globalThis[key]; }
}
const j = await injected(feature('commandJournal.ts'), {});
const types = await injected(feature('types.ts'), {});
const id = n => `00000000-0000-4000-8000-${String(n).padStart(12, '0')}`;
const A = id(1), B = id(2), requestID = id(3);
const intent = { media_type: 'tv', tmdb_id: 1399, seasons: [1, 3], include_specials: false };
const row = (patch = {}) => ({ id: requestID, owner_id: A, intent: structuredClone(intent), state: 'pending', version: 1, created_at: '2026-10-22T12:00:00Z', updated_at: '2026-10-22T12:00:00Z', reason: '', ...patch });
const command = (role = 'member') => j.makeCommand(A, role, 'create', intent);
const receipt = c => ({ command_key: c.key, request: row({ owner_id: c.ownerId, intent: c.intent, ...(c.kind === 'close' ? { version: c.body.expected_version + 1, state: c.body.action === 'reject' ? 'rejected' : 'withdrawn', reason: c.body.reason } : {}) }) });
function storage() {
  const map = new Map(); let fail = false;
  return { map, get fail() { return fail; }, set fail(v) { fail = v; }, getItem(k) { if (fail) throw Error('storage denied'); return map.get(k) ?? null; }, setItem(k, v) { if (fail) throw Error('storage denied'); map.set(k, v); }, removeItem(k) { if (fail) throw Error('storage denied'); map.delete(k); } };
}
async function host(initial = {}, disk = storage()) {
  const slots = [], effects = [], calls = [], listeners = new Set(), timers = new Map();
  let cursor = 0, serial = 0, out, updates = 0, props = { userId: A, role: 'member', ...initial };
  const equal = (a, b) => a && b && a.length === b.length && a.every((v, i) => Object.is(v, b[i]));
  let lockTail = Promise.resolve();
  const navigator = { locks: { request(_key, options, fn) { const callback = typeof options === 'function' ? options : fn; const work = lockTail.then(callback); lockTail = work.catch(() => {}); return work; } } };
  const api = Object.fromEntries(['GET', 'POST'].map(method => [method, (path, options) => {
    const call = { method, path, options, settled: false }; calls.push(call);
    return new Promise((resolve, reject) => {
      call.reply = (data, status = 200) => { assert(!call.settled); call.settled = true; resolve({ data: status < 400 ? data : undefined, response: { status, ok: status >= 200 && status < 300 } }); };
      call.fail = () => { assert(!call.settled); call.settled = true; reject(Error('private transport diagnostic')); };
    });
  }]));
  const module = await injected(feature('useMediaRequests.ts'), {
    ...j, api, navigator, localStorage: disk,
    window: { addEventListener(name, fn) { assert.equal(name, 'storage'); listeners.add(fn); }, removeEventListener(name, fn) { assert.equal(name, 'storage'); listeners.delete(fn); } },
    setTimeout(fn, ms) { assert.equal(ms, 15000); const id = ++serial; timers.set(id, fn); return id; }, clearTimeout(id) { timers.delete(id); },
    useRef(value) { const i = cursor++; return slots[i] ?? (slots[i] = { current: value }); },
    useState(value) { const i = cursor++; slots[i] ??= { value: typeof value === 'function' ? value() : value }; return [slots[i].value, next => { updates++; slots[i].value = typeof next === 'function' ? next(slots[i].value) : next; }]; },
    useEffect(fn, deps) { const i = cursor++, old = slots[i]; if (!old || !equal(old.deps, deps)) effects.push(() => { old?.cleanup?.(); slots[i] = { deps, fn, cleanup: fn() }; }); },
  });
  const commit = () => { for (const fn of effects.splice(0)) fn(); };
  const render = (patch = {}, effectsNow = true) => { props = { ...props, ...patch }; cursor = 0; out = module.useMediaRequests(props); if (effectsNow) commit(); return out; };
  const flush = async () => { await tick(); return render(); };
  const unmount = () => { for (const slot of slots) slot?.cleanup?.(); };
  render(); await flush();
  return { calls, disk, timers, listeners, navigator, render, flush, commit, unmount, get out() { return out; }, get updates() { return updates; },
    posts: () => calls.filter(c => c.method === 'POST'),
    event(key = j.journalKey(props.userId)) { for (const fn of listeners) fn({ key }); },
    replay() { for (const slot of slots) if (slot?.fn) { slot.cleanup?.(); slot.cleanup = slot.fn(); } },
    timeout() { for (const fn of [...timers.values()]) fn(); },
    async dispose() { unmount(); for (const call of calls) if (!call.settled) call.fail(); await tick(); assert.equal(timers.size, 0); assert.equal(listeners.size, 0); },
  };
}
const last = h => h.calls.at(-1);
async function resolveList(h, items = []) { const c = h.calls.find(c => c.path === '/api/v1/media-requests' && c.method === 'GET' && !c.settled); c.reply({ items }); await h.flush(); }

test('bounded journal roundtrips exact frozen payload and rejects malformed fields and account or role mismatch', () => {
  const disk = storage(), c = command(); j.saveJournal(disk, c); assert.deepEqual(j.readJournal(disk, A, 'member'), c);
  const bad = [v => { v.extra = true; }, v => { v.body.owner_id = A; }, v => { v.body.tmdb_id = Number.MAX_SAFE_INTEGER + 1; }, v => { v.intent = { ...v.intent, title: 'untrusted' }; }, v => { v.key = 'x'.repeat(81); }, v => { v.kind = 'execute'; }, v => { v.role = 'admin'; }, v => { v.ownerId = B; }, v => { v.body.seasons = [0]; }, v => { v.body.seasons = Array(101).fill(1); }, v => { v.body.include_specials = 'false'; }];
  for (const change of bad) { const value = structuredClone(c); change(value); assert.throws(() => j.parseJournal(JSON.stringify(value), A, 'member')); }
  assert.throws(() => j.parseJournal(JSON.stringify(c), B, 'member')); assert.throws(() => j.parseJournal(JSON.stringify(c), A, 'admin')); assert.throws(() => j.parseJournal('x'.repeat(20001), A, 'member'));
});

test('typed discovery identity and explicit seasons reject unsafe IDs, duplicate seasons and unapproved season zero', () => {
  assert.equal(j.intentFromInputs('tv', '9007199254740992', '', false), null); assert.equal(j.intentFromInputs('movie', '-1', '', false), null);
  assert.equal(j.intentFromInputs('tv', '1', '1,1', false), null); assert.equal(j.intentFromInputs('tv', '1', '0,1', false), null); assert(j.intentFromInputs('tv', '1', '0,1', true));
  assert.equal(j.discoveryPrefill('?media_type=movie&tmdb_id=1&tmdb_id=2').tmdbId, '');
  const p = j.discoveryPrefill('?media_type=movie&tmdb_id=123&title=%3Cscript%3E'); assert.equal(p.tmdbId, '123'); assert.equal(p.title, '<script>');
  assert.deepEqual(Object.keys(j.intentFromInputs(p.mediaType, p.tmdbId, '', false)), ['media_type', 'tmdb_id', 'seasons', 'include_specials']);
});

test('storage failure prevents every POST and exposes a safe local error', async () => {
  const h = await host(); try { h.disk.fail = true; assert.equal(await h.out.create(intent), false); await h.flush(); assert.equal(h.posts().length, 0); assert.equal(h.out.canWrite, false); assert(!h.out.message.includes('private')); } finally { h.disk.fail = false; await h.dispose(); }
});

test('restored journal, Strict Mode and manual refresh never auto POST or auto read a receipt', async () => {
  const disk = storage(), c = command(); j.saveJournal(disk, c); const h = await host({}, disk); try {
    assert.equal(h.out.pending.key, c.key); h.replay(); await h.flush(); assert.equal(h.posts().length, 0); assert(h.calls.every(c => c.path === '/api/v1/media-requests'));
    assert.equal(await h.out.create({ ...intent, tmdb_id: 123 }), false); assert.equal(h.posts().length, 0);
    const work = h.out.readReceipt(); await tick(); assert.equal(last(h).method, 'GET'); assert.equal(last(h).options.params.path.idempotency_key, c.key); last(h).reply(receipt(c)); assert.equal(await work, true); await h.flush(); assert.equal(h.out.pending, null); assert.equal(disk.getItem(j.journalKey(A)), null);
  } finally { await h.dispose(); }
});

test('lost original POST persists frozen body first, explicit GET 404 enables only same-key manual retry', async () => {
  const h = await host(); try {
    const first = h.out.create(intent); await tick(); const post = last(h), c = j.readJournal(h.disk, A, 'member'); assert.equal(post.method, 'POST'); assert.deepEqual(post.options.body, intent); assert.equal(post.options.params.header['Idempotency-Key'], c.key);
    intent.seasons.push(99); assert.deepEqual(c.body.seasons, [1, 3]); intent.seasons.pop(); post.fail(); assert.equal(await first, false); await h.flush();
    const read = h.out.readReceipt(); await tick(); last(h).reply(undefined, 404); assert.equal(await read, false); await h.flush(); assert.equal(h.out.phase, 'missing'); assert.equal(h.posts().length, 1);
    const retry = h.out.retryOriginal(); await tick(); const second = last(h); assert.equal(second.options.params.header['Idempotency-Key'], c.key); assert.deepEqual(second.options.body, c.body); second.reply(receipt(c), 201); assert.equal(await retry, true); await h.flush(); assert.equal(h.out.phase, 'confirmed');
  } finally { await h.dispose(); }
});

test('403 and 503 preserve the original key and protect against a second POST', async () => {
  for (const status of [403, 503]) { const disk = storage(), c = command(); j.saveJournal(disk, c); const h = await host({}, disk); try {
    const work = h.out.readReceipt(); await tick(); last(h).reply(undefined, status); assert.equal(await work, false); await h.flush(); assert.equal(h.out.phase, 'protected'); assert.equal(h.out.pending.key, c.key); assert.equal(await h.out.retryOriginal(), false); assert.equal(await h.out.create(intent), false); assert.equal(h.posts().length, 0); assert.equal(j.readJournal(disk, A, 'member').key, c.key);
  } finally { await h.dispose(); } }
});

test('malformed success and wrong command, intent, request ID or owner never clear original journal', async () => {
  const disk = storage(), c = j.makeCommand(A, 'member', 'close', { expected_version: 1, action: 'withdraw', reason: '' }, row()); j.saveJournal(disk, c); const h = await host({}, disk); try {
    const bad = [{}, { ...receipt(c), command_key: id(99) }, { command_key: c.key, request: row({ id: id(4), state: 'withdrawn', version: 2 }) }, { command_key: c.key, request: row({ owner_id: B, state: 'withdrawn', version: 2 }) }, { command_key: c.key, request: row({ intent: { ...intent, tmdb_id: 3 }, state: 'withdrawn', version: 2 }) }];
    for (const data of bad) { const work = h.out.readReceipt(); await tick(); last(h).reply(data); assert.equal(await work, false); await h.flush(); assert.equal(h.out.pending.key, c.key); }
    assert.equal(h.posts().length, 0); assert(j.readJournal(disk, A, 'member'));
  } finally { await h.dispose(); }
});

test('409 requires verified current GET and explicit local ending, with no extra POST', async () => {
  const disk = storage(), c = j.makeCommand(A, 'member', 'close', { expected_version: 1, action: 'withdraw', reason: '' }, row()); j.saveJournal(disk, c); const h = await host({}, disk); try {
    const read = h.out.readReceipt(); await tick(); last(h).reply(undefined, 409); await read; await h.flush(); assert.equal(h.out.phase, 'conflict'); assert.equal(await h.out.finishConflict(), false);
    const failed = h.out.readCurrent(); await tick(); last(h).reply(undefined, 503); await failed; await h.flush(); assert(j.readJournal(disk, A, 'member')); assert.equal(await h.out.finishConflict(), false);
    const verified = h.out.readCurrent(); await tick(); last(h).reply(row({ state: 'approved', version: 2, subscription_id: id(9) })); assert.equal(await verified, true); await h.flush(); assert.equal(h.out.phase, 'verified-conflict'); assert.equal(await h.out.finishConflict(), true); await h.flush(); assert.equal(disk.getItem(j.journalKey(A)), null); assert.equal(h.posts().length, 0);
  } finally { await h.dispose(); }
});

test('A-B-A synchronously masks data before effects and old receipt completions cannot clear A journal', async () => {
  const disk = storage(), c = command(); j.saveJournal(disk, c); const h = await host({}, disk); try {
    await resolveList(h, [row()]); const read = h.out.readReceipt(); await tick(); const old = last(h);
    const maskedB = h.render({ userId: B }, false); assert.deepEqual(maskedB.items, []); assert.equal(maskedB.pending, null); h.commit(); await h.flush();
    assert.equal(h.render({ userId: A }, false).pending, null); h.commit(); await h.flush(); old.reply(receipt(c)); assert.equal(await read, false); await h.flush(); assert.equal(h.out.pending.key, c.key); assert(j.readJournal(disk, A, 'member')); assert.equal(h.posts().length, 0);
  } finally { await h.dispose(); }
});

test('role change rejects journal restoration and late admin receipt writes', async () => {
  const disk = storage(), c = command('admin'); j.saveJournal(disk, c); const h = await host({ role: 'admin' }, disk); try {
    const work = h.out.readReceipt(); await tick(); const late = last(h); assert.equal(h.render({ role: 'member' }, false).pending, null); h.commit(); await h.flush(); assert.equal(h.out.phase, 'blocked'); assert.equal(h.out.options, null); late.reply(receipt(c)); assert.equal(await work, false); assert(j.readJournal(disk, A, 'admin')); assert.equal(h.posts().length, 0);
  } finally { await h.dispose(); }
});

test('unmount and timeout abort transport, retain the key, clear timers and ignore late success', async () => {
  for (const unmount of [true, false]) { const disk = storage(), c = command(); j.saveJournal(disk, c); const h = await host({}, disk); try {
    const work = h.out.readReceipt(); await tick(); const late = last(h); if (unmount) h.unmount(); else h.timeout(); const updates = h.updates; assert.equal(await work, false); late.reply(receipt(c)); await tick(); assert(j.readJournal(disk, A, 'member')); assert(late.options.signal.aborted); if (unmount) assert.equal(h.updates, updates); assert.equal(h.timers.size, 0);
  } finally { await h.dispose(); } }
});

test('members perform zero admin option GETs, and account scope rejects foreign list rows', async () => {
  const h = await host(); try {
    assert.equal(await h.out.loadOptions(), false); assert(h.calls.every(c => c.path === '/api/v1/media-requests'));
    await resolveList(h, [row({ owner_id: B })]); assert.equal(h.out.items.length, 0); assert(h.out.listError); assert.equal(h.out.loaded, false); assert.equal(h.posts().length, 0);
  } finally { await h.dispose(); }
});

test('original create receipt pending v1 resolves command without replacing already-approved current row', async () => {
  const disk = storage(), c = command(); j.saveJournal(disk, c); const h = await host({}, disk); try {
    await resolveList(h, [row({ state: 'approved', version: 2, subscription_id: id(9) })]); const work = h.out.readReceipt(); await tick(); last(h).reply(receipt(c)); assert.equal(await work, true); await h.flush(); assert.equal(h.out.items[0].state, 'approved'); assert.equal(h.out.items[0].version, 2); assert.equal(h.out.pending, null); assert.equal(h.posts().length, 0);
  } finally { await h.dispose(); }
});

test('cross-tab removal or replacement cannot erase known unknown or authorize a new key', async () => {
  for (const replace of [false, true]) { const disk = storage(), c = command(); j.saveJournal(disk, c); const h = await host({}, disk); try {
    if (replace) disk.setItem(j.journalKey(A), JSON.stringify(command())); else disk.removeItem(j.journalKey(A)); h.event(); await h.flush(); assert.equal(h.out.pending.key, c.key); assert.equal(h.out.phase, 'blocked'); assert.equal(await h.out.create(intent), false);
    const work = h.out.readReceipt(); await tick(); last(h).reply(undefined, 404); await work; await h.flush(); assert.equal(await h.out.retryOriginal(), false); assert.equal(h.out.pending.key, c.key); assert.equal(h.posts().length, 0);
  } finally { await h.dispose(); } }
});

test('admin approval receipt validates exact subscription owner and target, never authority from display text', () => {
  const c = j.makeCommand(A, 'admin', 'approve', { expected_version: 1, root_id: id(10), quality_policy_id: 'default', indexer_ids: [id(11)], downloader_id: id(12) }, row({ owner_id: B }));
  const result = { request_id: requestID, subscription: { id: id(13), user_id: B, media_id: id(14), root_id: id(10), quality_policy_id: 'default', indexer_ids: [id(11)], downloader_id: id(12), intent_version: 1, quality_policy_revision: 1, state: 'active', scope_type: 'seasons', media_type: 'tv', tmdb_id: 1399 } };
  assert(j.validReceipt(result, c));
  for (const patch of [{ user_id: A }, { root_id: id(20) }, { tmdb_id: 99 }, { indexer_ids: [] }, { downloader_id: id(21) }]) assert.equal(j.validReceipt({ ...result, subscription: { ...result.subscription, ...patch } }, c), false);
  assert.equal(j.validReceipt({ ...result, request_id: id(22) }, c), false);
  assert.equal(j.validCommand({ ...c, role: 'member' }, A, 'member'), false);
});

test('pagination uses 25 and last UUID; loading, failure and empty are distinct without any mutation', async () => {
  const h = await host(); try {
    assert.equal(h.out.loading, true); assert.equal(h.out.loaded, false); const items = Array.from({ length: 25 }, (_, i) => row({ id: id(i + 30) })); await resolveList(h, items); assert.equal(h.out.next, items[24].id);
    const work = h.out.refresh(h.out.next); assert.deepEqual(last(h).options.params.query, { limit: 25, before_id: items[24].id }); assert.equal(last(h).options.cache, 'no-store'); last(h).reply({ items: [] }); assert.equal(await work, true); await h.flush(); assert.equal(h.out.loaded, true); assert.deepEqual(h.out.items, []); assert.equal(h.out.next, null);
    const failed = h.out.refresh(); last(h).reply(undefined, 503); await failed; await h.flush(); assert.equal(h.out.loaded, false); assert(h.out.listError); assert.equal(h.posts().length, 0);
  } finally { await h.dispose(); }
});

const h = (type, props, ...children) => typeof type === 'function' ? type({ ...props, children }) : { type, props: props ?? {}, children: children.flat(Infinity) };
const walk = node => !node || typeof node !== 'object' ? [] : [node, ...(node.children ?? []).flatMap(walk)];
const text = node => typeof node === 'string' || typeof node === 'number' ? String(node) : (node?.children ?? []).map(text).join(' ');
async function renderPage(role = 'member', patch = {}) {
  const view = { items: [], loaded: true, loading: false, listError: '', options: null, optionsError: '', optionsLoading: false, pending: null, phase: 'idle', busy: false, message: '', current: null, cursor: null, next: null, canWrite: true, ...patch };
  const m = await injected(source('views/RequestsView.tsx'), { h, Fragment: 'fragment', Link: 'a', useState: initial => [typeof initial === 'function' ? initial() : initial, () => {}], useLocation: () => ({ search: '?media_type=movie&tmdb_id=123&title=%3Cscript%3E' }), useAuth: () => ({ user: { id: A, role } }), useMediaRequests: () => view, ...j, ...types });
  return m.RequestsView();
}

test('actual page JSX exposes labelled forms and own withdrawal, with no admin controls for members', async () => {
  const tree = await renderPage('member', { items: [row(), row({ id: id(4), state: 'approved', version: 2, subscription_id: id(9) })] }), nodes = walk(tree);
  assert(nodes.some(n => n.type === 'form' && n.props['aria-label'] === '提交媒体请求'));
  assert(nodes.some(n => n.type === 'nav' && n.props['aria-label'] === '请求分页'));
  assert(text(tree).includes('撤回本人请求')); assert(!nodes.some(n => n.type === 'form' && String(n.props['aria-label'] ?? '').startsWith('批准请求 '))); assert(!nodes.some(n => n.type === 'button' && text(n).includes('批准并创建订阅'))); assert(text(tree).includes('查看本人订阅')); assert(text(tree).includes('不表示下载成功')); assert(text(tree).includes('<script>')); assert(nodes.every(n => !n.props.dangerouslySetInnerHTML));
});

test('actual admin JSX requires explicit root, policy, downloader, indexer and reject reason', async () => {
  const tree = await renderPage('admin', { items: [row({ owner_id: B })], options: { roots: [], policies: [], downloaders: [], indexers: [] } }), nodes = walk(tree);
  assert(nodes.some(n => n.type === 'form' && n.props['aria-label'] === `批准请求 ${requestID}`));
  assert(text(tree).includes('在线目标根')); assert(text(tree).includes('质量策略')); assert(text(tree).includes('下载器')); assert(text(tree).includes('至少选择一个'));
  assert(nodes.some(n => n.type === 'button' && text(n) === '批准并创建订阅' && n.props.disabled)); assert(nodes.some(n => n.type === 'button' && text(n) === '拒绝请求' && n.props.disabled)); assert(!text(tree).includes('撤回本人请求')); assert(!text(tree).includes('查看本人订阅'));
});

test('write-quota failure after readable storage saves no POST and retains the attempted key in memory', async () => {
  const disk = storage(), h = await host({}, disk); try {
    const set = disk.setItem; disk.setItem = () => { throw Error('quota'); };
    const work = h.out.create(intent); await tick(); assert.equal(await work, false); await h.flush(); assert.equal(h.posts().length, 0); assert(h.out.pending?.key); assert.equal(h.out.phase, 'blocked');
    disk.setItem = set; const read = h.out.readReceipt(); await tick(); last(h).reply(undefined, 404); await read; await h.flush(); assert.equal(await h.out.retryOriginal(), false); assert.equal(h.posts().length, 0);
  } finally { await h.dispose(); }
});

test('actual admin hook reads current options and persists frozen approval before exactly one explicit POST', async () => {
  const h = await host({ role: 'admin' }); try {
    await resolveList(h, [row({ owner_id: B })]);
    const resources = {
      '/api/v1/library-roots': [{ id: id(10), name: 'TV', media_type: 'tv', health: 'online', baseline_completed: true, generation: 1 }, { id: id(20), name: 'offline', media_type: 'tv', health: 'offline', baseline_completed: true, generation: 1 }],
      '/api/v1/policies': [{ id: 'default', name: 'Default', revision: 1 }],
      '/api/v1/indexers': [{ id: id(11), name: 'Source', health: 'online' }],
      '/api/v1/download-clients': [{ id: id(12), name: 'Downloader', client_type: 'qbittorrent' }],
    };
    for (const call of h.calls) if (!call.settled && resources[call.path]) call.reply({ items: resources[call.path] }); await h.flush(); assert.equal(h.out.options.roots.length, 1); assert.equal(h.posts().length, 0);
    const body = { expected_version: 1, root_id: id(10), quality_policy_id: 'default', indexer_ids: [id(11)], downloader_id: id(12) };
    const work = h.out.approve(h.out.items[0], body); await tick(); const post = last(h), saved = j.readJournal(h.disk, A, 'admin'); assert.equal(saved.kind, 'approve'); assert.equal(saved.ownerId, B); assert.equal(post.path, '/api/v1/media-requests/{request_id}/approve'); assert.deepEqual(post.options.body, body); assert.equal(post.options.params.header['Idempotency-Key'], saved.key);
    post.reply({ request_id: requestID, subscription: { id: id(13), user_id: B, media_id: id(14), root_id: id(10), quality_policy_id: 'default', indexer_ids: [id(11)], downloader_id: id(12), intent_version: 1, quality_policy_revision: 1, state: 'active', scope_type: 'seasons', media_type: 'tv', tmdb_id: 1399 } }, 201);
    assert.equal(await work, true); await h.flush(); assert.equal(h.posts().length, 1); assert.equal(h.out.pending, null); assert(h.out.message.includes('不表示下载成功')); assert.equal(h.out.items[0].state, 'pending');
  } finally { await h.dispose(); }
});

test('create conflict ends only after explicit GET of matching owner and frozen identity', async () => {
  const disk = storage(), c = command(); j.saveJournal(disk, c); const h = await host({}, disk); try {
    const work = h.out.readReceipt(); await tick(); last(h).reply(undefined, 409); await work; await h.flush();
    const foreign = h.out.readCurrent(requestID); await tick(); last(h).reply(row({ owner_id: B })); assert.equal(await foreign, false); await h.flush(); assert.equal(await h.out.finishConflict(), false);
    const current = h.out.readCurrent(requestID); await tick(); last(h).reply(row({ state: 'approved', version: 2 })); assert.equal(await current, true); await h.flush(); assert.equal(await h.out.finishConflict(), true); assert.equal(h.posts().length, 0); assert.equal(disk.getItem(j.journalKey(A)), null);
  } finally { await h.dispose(); }
});

test('another tab pending record and unavailable browser lock both prevent fresh writes', async () => {
  const h = await host(); try {
    const c = command(); j.saveJournal(h.disk, c); h.event(); await h.flush(); assert.equal(h.out.pending.key, c.key); assert.equal(await h.out.create(intent), false); assert.equal(h.posts().length, 0);
  } finally { await h.dispose(); }
  const unlocked = await host(); try {
    unlocked.navigator.locks = undefined; assert.equal(await unlocked.out.create(intent), false); await unlocked.flush(); assert.equal(unlocked.posts().length, 0); assert.equal(unlocked.out.phase, 'blocked');
  } finally { await unlocked.dispose(); }
});

test('late original POST after A-B-A cannot clear its journal or replace the new lifecycle state', async () => {
  const h = await host(); try {
    const work = h.out.create(intent); await tick(); const old = last(h), c = j.readJournal(h.disk, A, 'member');
    assert.equal(h.render({ userId: B }, false).pending, null); h.commit(); await h.flush(); assert.equal(h.render({ userId: A }, false).pending, null); h.commit(); await h.flush();
    old.reply(receipt(c), 201); assert.equal(await work, false); await h.flush(); assert.equal(h.out.pending.key, c.key); assert(j.readJournal(h.disk, A, 'member')); assert.equal(h.posts().length, 1);
  } finally { await h.dispose(); }
});

test('Strict Mode cleanup of an in-flight POST retains journal and blocks prior lifecycle callbacks', async () => {
  const h = await host(); try {
    const work = h.out.create(intent); await tick(); const old = last(h), c = j.readJournal(h.disk, A, 'member'); h.replay(); await h.flush();
    assert.equal(h.out.pending.key, c.key); assert.equal(h.out.phase, 'unknown'); const updates = h.updates; old.reply(receipt(c), 201); assert.equal(await work, false); await tick(); assert.equal(h.updates, updates); assert.equal(h.posts().length, 1); assert(j.readJournal(h.disk, A, 'member'));
  } finally { await h.dispose(); }
});

test('actual member close POST uses current expected version, own scope and original 200 receipt', async () => {
  const h = await host(); try {
    await resolveList(h, [row()]); const work = h.out.close(h.out.items[0], { expected_version: 1, action: 'withdraw', reason: '不再需要' }); await tick();
    const post = last(h), c = j.readJournal(h.disk, A, 'member'); assert.equal(post.method, 'POST'); assert.equal(post.path, '/api/v1/media-requests/{request_id}/close'); assert.deepEqual(post.options.body, { expected_version: 1, action: 'withdraw', reason: '不再需要' }); assert.equal(post.options.params.path.request_id, requestID); assert.equal(c.ownerId, A);
    post.reply(receipt(c), 200); assert.equal(await work, true); await h.flush(); assert.equal(h.out.pending, null); assert.equal(h.posts().length, 1); assert.equal(h.out.items[0].state, 'pending');
  } finally { await h.dispose(); }
});
