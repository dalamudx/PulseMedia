import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import ts from 'typescript';

const root = new URL('../src/features/configuration/', import.meta.url);
const compile = source => ts.transpileModule(source, { compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.ESNext, jsx: ts.JsxEmit.React } }).outputText;
const load = source => import(`data:text/javascript;base64,${Buffer.from(compile(source)).toString('base64')}#${crypto.randomUUID()}`);
const pure = await load(readFileSync(new URL('discoveryReviewHandoff.ts', root), 'utf8'));
const uuid = n => `00000000-0000-4000-8000-${String(n).padStart(12, '0')}`;
const user = { id: uuid(1), role: 'admin' };
const ref = (patch = {}) => ({ userId: user.id, subscriptionId: uuid(2), candidateId: uuid(3), tmdbId: 42, searchId: uuid(4), createdAt: 1000000, ...patch });
const row = (patch = {}) => ({ id: uuid(2), user_id: user.id, media_id: uuid(5), root_id: uuid(6), media_type: 'tv', tmdb_id: 42, state: 'active', intent_version: 1, downloader_id: uuid(7), quality_policy_id: 'default', quality_policy_revision: 1, root_name: 'TV', ...patch });
const report = (patch = {}) => ({ id: uuid(2), intent_version: 1, policy_revision: 1, blockers: [], ...patch });
const tick = async () => { for (let i = 0; i < 20; i++) await Promise.resolve(); };

// Execute the real provider and picker with controlled hook slots, JSX, clock and HTTP.
async function harness(initialProps = {}) {
  let active, auth = user, now = 1000000, context = null;
  const timers = new Map(), calls = [], navigations = [], offers = [], pending = [];
  const createElement = (type, props, ...children) => ({ type, props: { ...props, children } });
  function hooks() {
    const slots = []; let cursor = 0; const effects = [];
    const equal = (a, b) => a && b && a.length === b.length && a.every((v, i) => Object.is(v, b[i]));
    return {
      slots, effects,
      useRef(value) { const i = cursor++; return slots[i] ?? (slots[i] = { current: value }); },
      useState(value) {
        const i = cursor++; slots[i] ??= { value: typeof value === 'function' ? value() : value };
        return [slots[i].value, next => { slots[i].value = typeof next === 'function' ? next(slots[i].value) : next; }];
      },
      useEffect(fn, deps) {
        const i = cursor++, old = slots[i];
        if (!old || !equal(old.deps, deps)) effects.push(() => { old?.cleanup?.(); slots[i] = { deps, fn, cleanup: fn() }; });
      },
      useId() { const i = cursor++; return `field-${i}`; },
      render(fn, props, runEffects = true) { cursor = 0; active = this; const result = fn(props); if (runEffects) this.flush(); return result; },
      flush() { for (const effect of effects.splice(0)) effect(); },
      replay() { for (const slot of slots) if (slot?.fn) { slot.cleanup?.(); slot.cleanup = slot.fn(); } },
      dispose() { for (const slot of slots) slot?.cleanup?.(); effects.length = 0; },
    };
  }
  const host = { ...pure,
    React: { createElement, Fragment: 'fragment' },
    createContext: () => ({ Provider: 'provider' }), useContext: () => context,
    useRef: value => active.useRef(value), useState: value => active.useState(value),
    useEffect: (fn, deps) => active.useEffect(fn, deps), useId: () => active.useId(),
    useAuth: () => ({ user: auth }), useNavigate: () => path => navigations.push(path),
    useDiscoveryReviewHandoff: () => ({ offer(value) { offers.push(value); return true; } }),
    Date: { now: () => now },
    currentDiscoveryReview: (value, id, role) => pure.currentDiscoveryReview(value, id, role, now),
    setTimeout: (fn, delay) => { const id = Symbol(); timers.set(id, { fn, at: now + delay }); return id; },
    clearTimeout: id => timers.delete(id),
    api: { GET(path, options) {
      const call = { path, options, settled: false }; calls.push(call);
      return new Promise((resolve, reject) => {
        call.reply = data => { assert(!call.settled); call.settled = true; resolve({ data }); };
        call.error = () => { assert(!call.settled); call.settled = true; resolve({ error: {} }); };
        call.fail = () => { assert(!call.settled); call.settled = true; reject(Error('secret upstream URL')); };
      });
    } },
  };
  const name = `__handoff_${crypto.randomUUID().replaceAll('-', '')}`;
  globalThis[name] = host;
  const loadComponent = file => {
    const source = readFileSync(new URL(file, root), 'utf8').replace(/^import[^\n]*\n/gm, '');
    return load(`const {${Object.keys(host).filter(key => file !== 'DiscoveryReviewHandoff.tsx' || key !== 'useDiscoveryReviewHandoff').join(',')}} = globalThis[${JSON.stringify(name)}];\n${source}`);
  };
  let providerModule, pickerModule;
  try { providerModule = await loadComponent('DiscoveryReviewHandoff.tsx'); pickerModule = await loadComponent('DiscoveryTVReviewTarget.tsx'); }
  finally { delete globalThis[name]; }
  const providerHooks = hooks(), outerHooks = hooks(); let pickerHooks = hooks(), key, tree;
  let props = { tmdbId: 42, candidateId: uuid(3), searchId: uuid(4), onPending: value => pending.push(value), ...initialProps };
  const provider = (runEffects = true) => {
    context = providerHooks.render(providerModule.DiscoveryReviewHandoffProvider, { children: null }, runEffects).props.value;
    return context;
  };
  const render = (patch = {}) => {
    props = { ...props, ...patch };
    const element = outerHooks.render(pickerModule.DiscoveryTVReviewTarget, props);
    if (!element) { pickerHooks.dispose(); tree = null; return tree; }
    if (key !== element.props.key) { pickerHooks.dispose(); pickerHooks = hooks(); key = element.props.key; }
    tree = pickerHooks.render(element.type, element.props); return tree;
  };
  const nodes = (node = tree) => !node || typeof node !== 'object' ? [] : [node, ...node.props.children.flat(Infinity).flatMap(nodes)];
  const text = (node = tree) => typeof node === 'string' ? node : node && typeof node === 'object' ? node.props.children.flat(Infinity).map(text).join('') : '';
  const button = label => { const found = nodes().find(n => n.type === 'button' && text(n).includes(label)); assert(found, `missing ${label}`); return found; };
  const click = label => { const node = button(label); assert.equal(!!node.props.disabled, false, label); node.props.onClick(); };
  const flush = async () => { await tick(); render(); };
  provider(); render();
  return { calls, navigations, offers, pending, provider, providerHooks, render, nodes, text, button, click, flush,
    setAuth(value) { auth = value; },
    setNow(value) { now = value; },
    fireTimers() { for (const [id, timer] of timers) if (timer.at <= now) { timers.delete(id); timer.fn(); } },
    get timers() { return timers.size; },
    contextHook: () => providerModule.useDiscoveryReviewHandoff(),
    clearContext() { context = null; },
    async list(items) { click('前往订阅审核'); calls.at(-1).reply({ items }); await flush(); },
    async detail(detail = row(), status = report()) {
      calls.findLast(c => c.path === '/api/v1/subscriptions/{id}').reply(detail);
      calls.findLast(c => c.path.endsWith('/status')).reply(status);
      await flush();
    },
    async dispose() { providerHooks.dispose(); outerHooks.dispose(); pickerHooks.dispose(); for (const call of calls) if (!call.settled) call.error(); await tick(); assert.equal(timers.size, 0); },
  };
}

 test('pure reference validation rejects malformed UUIDs, unsafe IDs, future and expired references', () => {
  const original = ref(); assert.equal(pure.currentDiscoveryReview(original, user.id, 'admin', 1000000), original);
  for (const patch of [{ userId: 'alice' }, { subscriptionId: '00000000-0000-0000-0000-000000000000' }, { candidateId: '../x' }, { searchId: uuid(4) + '?x' }, { tmdbId: 0 }, { tmdbId: 1.5 }, { tmdbId: Number.MAX_SAFE_INTEGER + 1 }, { createdAt: 1000001 }, { createdAt: NaN }]) assert.equal(pure.currentDiscoveryReview(ref(patch), user.id, 'admin', 1000000), null);
  assert.equal(pure.currentDiscoveryReview(ref(), user.id, 'member', 1000000), null);
  assert.equal(pure.currentDiscoveryReview(ref(), uuid(9), 'admin', 1000000), null);
  assert.ok(pure.currentDiscoveryReview(ref(), user.id, 'admin', 1299999));
  assert.equal(pure.currentDiscoveryReview(ref(), user.id, 'admin', 1300000), null);
});

test('pure target requires current actor, exact TV identity, binding, active state, intent and status versions', () => {
  assert.equal(pure.currentDiscoveryTVTarget(row(), row({ tmdb_id: undefined }), report({ blockers: ['root_offline', 'no_downloader'] }), user.id, 42), true);
  for (const patch of [{ id: uuid(10) }, { user_id: uuid(10) }, { media_id: uuid(10) }, { root_id: uuid(10) }, { media_type: 'movie' }, { tmdb_id: 43 }, { state: 'paused' }, { intent_version: 2 }, { intent_version: NaN }, { downloader_id: uuid(10) }, { quality_policy_id: 'other' }, { quality_policy_revision: 2 }]) assert.equal(pure.currentDiscoveryTVTarget(row(), row(patch), report(), user.id, 42), false, JSON.stringify(patch));
  for (const patch of [{ id: uuid(10) }, { intent_version: 2 }, { policy_revision: 2 }, { blockers: null }]) assert.equal(pure.currentDiscoveryTVTarget(row(), row(), report(patch), user.id, 42), false);
});

test('actual provider copies only reference fields, offers, clears and expires synchronously and by timer', async () => {
  const h = await harness(); try {
    let value = h.provider(); const offered = ref({ secret: 'never retain' }); assert.equal(value.offer(offered), true);
    offered.candidateId = uuid(99); value = h.provider();
    assert.deepEqual(value.candidate, ref()); assert.equal(Object.isFrozen(value.candidate), true); assert.equal(h.timers, 1);
    h.setNow(1300000); assert.equal(value.candidate, null); h.fireTimers(); assert.equal(h.provider().candidate, null);
    h.setNow(1000000); assert.equal(value.offer(ref()), true); h.provider(); value.clear(); assert.equal(value.candidate, null); h.provider(); assert.equal(h.timers, 0);
    assert.equal(value.offer(ref({ tmdbId: -1 })), false);
  } finally { await h.dispose(); }
});

test('actual provider invalidates retained handles before effects on account and role changes and never resurrects', async () => {
  const h = await harness(); try {
    const old = h.provider(); old.offer(ref()); h.provider();
    h.setAuth({ id: uuid(9), role: 'admin' }); const other = h.provider(false);
    assert.equal(old.candidate, null); assert.equal(old.offer(ref()), false); assert.equal(other.candidate, null);
    h.setAuth(user); let current = h.provider(false); assert.equal(current.candidate, null); assert.equal(old.offer(ref()), false);
    current.offer(ref()); h.setAuth({ ...user, role: 'member' }); current = h.provider(false);
    assert.equal(current.candidate, null); assert.equal(current.offer(ref()), false);
    h.setAuth(null); assert.equal(h.provider(false).candidate, null);
  } finally { await h.dispose(); }
});

test('actual provider supports effect replay and rejects reads/offers after unmount; hook requires provider', async () => {
  const h = await harness(); try {
    h.providerHooks.replay(); const value = h.provider(); assert.equal(value.offer(ref()), true);
    assert.equal(h.contextHook(), value); h.providerHooks.dispose(); assert.equal(value.candidate, null); assert.equal(value.offer(ref()), false);
    h.clearContext(); assert.throws(h.contextHook, /requires DiscoveryReviewHandoffProvider/);
  } finally { await h.dispose(); }
});

test('actual picker uses exact current-user TV TMDB list identity and gives subscribe-first guidance', async () => {
  const h = await harness(); try {
    await h.list([row({ user_id: uuid(9) }), row({ media_type: 'movie' }), row({ tmdb_id: undefined }), row({ tmdb_id: 43 })]);
    assert.match(h.text(), /请先订阅本剧/); assert.equal(h.nodes().some(n => n.type === 'select'), false);
    assert.equal(h.button('进入原订阅审核').props.disabled, true); assert.equal(h.calls.length, 1);
    assert.equal(h.calls[0].options.cache, 'no-store'); assert.deepEqual(h.offers, []);
  } finally { await h.dispose(); }
});

test('actual picker requires explicit selection among roots, reGETs detail/status, and navigates with only subscription UUID', async () => {
  const h = await harness(); try {
    await h.list([row(), row({ id: uuid(20), root_id: uuid(21), root_name: 'Other' })]);
    const select = h.nodes().find(n => n.type === 'select'); assert.equal(select.props.value, '');
    assert.equal(h.button('进入原订阅审核').props.disabled, true);
    select.props.onChange({ target: { value: uuid(2) } }); h.render(); h.click('进入原订阅审核');
    assert.deepEqual(h.calls.map(c => c.path), ['/api/v1/subscriptions', '/api/v1/subscriptions/{id}', '/api/v1/subscriptions/{id}/status']);
    for (const call of h.calls.slice(1)) { assert.equal(call.options.params.path.id, uuid(2)); assert.equal(call.options.cache, 'no-store'); }
    await h.detail(row({ tmdb_id: undefined }), report({ blockers: ['root_offline', 'no_indexers'] }));
    assert.deepEqual(h.offers, [ref()]); assert.deepEqual(h.navigations, [`/subscriptions?subscription=${uuid(2)}`]);
    assert.deepEqual(h.pending, [true, false, true, false]);
  } finally { await h.dispose(); }
});

test('actual picker rejects stale detail and status, including account/media/root/intent/downloader changes', async () => {
  const cases = [{ user_id: uuid(9) }, { media_id: uuid(9) }, { root_id: uuid(9) }, { state: 'archived' }, { intent_version: 2 }, { downloader_id: uuid(9) }, { tmdb_id: 99 }];
  for (const patch of cases) {
    const h = await harness(); try {
      await h.list([row()]); h.click('进入原订阅审核'); await h.detail(row(patch));
      assert.deepEqual(h.offers, []); assert.deepEqual(h.navigations, []); assert.match(h.text(), /已变化/);
    } finally { await h.dispose(); }
  }
  const h = await harness(); try { await h.list([row()]); h.click('进入原订阅审核'); await h.detail(row(), report({ intent_version: 2 })); assert.deepEqual(h.offers, []); } finally { await h.dispose(); }
});

test('actual picker ignores late list/detail responses after close, disabled, account, role and candidate changes', async () => {
  for (const phase of ['list', 'detail']) for (const change of ['close', 'disabled', 'account', 'role', 'candidate', 'search', 'unmount']) {
    const h = await harness(); try {
      let retainedProceed;
      if (phase === 'list') { h.click('前往订阅审核'); h.render(); }
      else { await h.list([row()]); retainedProceed = h.button('进入原订阅审核').props.onClick; h.click('进入原订阅审核'); }
      const unfinished = h.calls.filter(c => !c.settled);
      if (change === 'close') h.click('取消选择');
      if (change === 'disabled') h.render({ disabled: true });
      if (change === 'account') { h.setAuth({ id: uuid(9), role: 'admin' }); h.render(); }
      if (change === 'role') { h.setAuth({ ...user, role: 'member' }); h.render(); }
      if (change === 'candidate') h.render({ candidateId: uuid(9) });
      if (change === 'search') h.render({ searchId: uuid(9) });
      if (change === 'unmount') await h.dispose();
      for (const call of unfinished) if (!call.settled) call.reply(call.path.endsWith('/status') ? report() : phase === 'list' ? { items: [row()] } : row());
      await tick();
      const count = h.calls.length; retainedProceed?.(); await tick(); assert.equal(h.calls.length, count, `${phase}/${change}: retained handler`);
      assert.deepEqual(h.offers, [], `${phase}/${change}`); assert.deepEqual(h.navigations, []);
      for (const call of unfinished) assert.equal(call.options.signal.aborted, true);
    } finally { await h.dispose(); }
  }
});

test('actual picker handles HTTP and transport failures without exposing errors or keeping stale selection', async () => {
  for (const failure of ['error', 'fail']) {
    const h = await harness(); try {
      h.click('前往订阅审核'); h.calls.at(-1)[failure](); await h.flush();
      assert.match(h.text(), /读取失败/); assert.doesNotMatch(h.text(), /secret/); assert.equal(h.button('进入原订阅审核').props.disabled, true);
      h.click('重新读取订阅'); h.calls.at(-1).reply({ items: [row()] }); await h.flush(); h.click('进入原订阅审核');
      h.calls.at(-2)[failure](); h.calls.at(-1).reply(report()); await h.flush(); assert.deepEqual(h.offers, []); assert.equal(h.pending.at(-1), false);
    } finally { await h.dispose(); }
  }
});

test('actual picker refuses malformed inputs and member rendering without any requests', async () => {
  for (const patch of [{ tmdbId: 0 }, { tmdbId: Number.MAX_SAFE_INTEGER + 1 }, { candidateId: 'bad' }, { searchId: 'bad' }, { disabled: true }]) {
    const h = await harness(patch); try { assert.equal(h.button('前往订阅审核').props.disabled, true); assert.deepEqual(h.calls, []); } finally { await h.dispose(); }
  }
  const h = await harness(); try { h.setAuth({ ...user, role: 'member' }); assert.equal(h.render(), null); assert.deepEqual(h.calls, []); } finally { await h.dispose(); }
});
