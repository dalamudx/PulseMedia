import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import ts from 'typescript';

const root = new URL('../src/features/dashboard/', import.meta.url);
const source = name => readFileSync(new URL(name, root), 'utf8');
const stripImports = text => text.replace(/^import[\s\S]*?;\n/gm, '');
const load = text => import(`data:text/javascript;base64,${Buffer.from(ts.transpileModule(text, {
  compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.ESNext, jsx: ts.JsxEmit.React, jsxFactory: 'h', jsxFragmentFactory: 'Fragment' },
}).outputText).toString('base64')}#${crypto.randomUUID()}`);
async function injected(text, values) {
  const name = `__facts_${crypto.randomUUID().replaceAll('-', '')}`;
  globalThis[name] = values;
  try { return await load(`const {${Object.keys(values).join(',')}}=globalThis[${JSON.stringify(name)}];\n${stripImports(text)}`); }
  finally { delete globalThis[name]; }
}
const tick = async () => { for (let i = 0; i < 16; i++) await Promise.resolve(); };
const stamp = '2026-10-19T12:00:00Z';
const overview = (patch = {}) => ({ counts: { movies: '0', tv_shows: '0', episodes: '0', files: '0' }, roots: [], recent: [], observed_at: stamp, ...patch });
const activity = (patch = {}) => ({ items: [], more: false, observed_at: stamp, ...patch });
const rootFact = (patch = {}) => ({ root_id: 'root', name: '电影根', media_type: 'movie', health: 'online', availability: 'available', capacity_state: 'fresh', total_bytes: '100', available_bytes: '25', sampled_at: stamp, ...patch });
const importFact = (patch = {}) => ({ media_id: 'media', media_type: 'movie', title: '真实入库影片', imported_at: stamp, ...patch });
const task = (patch = {}) => ({ operation_id: 'operation', title: '任务', status: 'tracking', ...patch });

// Same slot/effect injection as discovery-catalog.test.mjs: execute the actual hook.
async function hookHost(initial = {}) {
  const slots = [], effects = [], calls = [], timers = new Map(), listeners = new Set();
  let cursor = 0, out, updates = 0, timerId = 0;
  let props = { userId: 'alice', role: 'admin', ...initial };
  const equal = (a, b) => a && b && a.length === b.length && a.every((v, i) => Object.is(v, b[i]));
  const memo = (fn, deps) => {
    const i = cursor++, old = slots[i];
    if (!old || !equal(old.deps, deps)) slots[i] = { deps, value: fn() };
    return slots[i].value;
  };
  const document = {
    visibilityState: 'visible',
    addEventListener(name, fn) { assert.equal(name, 'visibilitychange'); listeners.add(fn); },
    removeEventListener(name, fn) { assert.equal(name, 'visibilitychange'); listeners.delete(fn); },
  };
  const values = {
    document,
    setInterval(fn, ms) { const id = ++timerId; timers.set(id, { fn, ms }); return id; },
    clearInterval(id) { timers.delete(id); },
    useMemo: memo,
    useCallback: (fn, deps) => memo(() => fn, deps),
    useRef(value) { const i = cursor++; return slots[i] ?? (slots[i] = { current: value }); },
    useState(initialValue) {
      const i = cursor++;
      if (!slots[i]) slots[i] = { value: typeof initialValue === 'function' ? initialValue() : initialValue };
      return [slots[i].value, value => { updates++; slots[i].value = typeof value === 'function' ? value(slots[i].value) : value; }];
    },
    useEffect(fn, deps) {
      const i = cursor++, old = slots[i];
      if (!old || !equal(old.deps, deps)) effects.push(() => { old?.cleanup?.(); slots[i] = { deps, fn, cleanup: fn() }; });
    },
    api: { GET(path, options) {
      assert(['/api/v1/library/overview', '/api/v1/dashboard/activity'].includes(path));
      assert.equal(options.cache, 'no-store');
      const call = { path, options, settled: false }; calls.push(call);
      return new Promise((resolve, reject) => {
        call.reply = value => { assert(!call.settled); call.settled = true; resolve(value); };
        call.fail = () => { assert(!call.settled); call.settled = true; reject(Error('secret URL or credentials')); };
      });
    } },
  };
  const module = await injected(source('useDashboardFacts.ts'), values);
  const render = (patch = {}, runEffects = true) => {
    props = { ...props, ...patch }; cursor = 0; out = module.useDashboardFacts(props);
    if (runEffects) for (const effect of effects.splice(0)) effect();
    return out;
  };
  const flush = async () => { await tick(); return render(); };
  render(); await flush();
  return { module, calls, timers, listeners, render, flush, get out() { return out; }, get updates() { return updates; },
    visibility(value) { document.visibilityState = value; for (const fn of listeners) fn(); },
    fire(ms) { for (const timer of [...timers.values()]) if (timer.ms === ms) timer.fn(); },
    replay() { for (const slot of slots) if (slot?.fn) { slot.cleanup?.(); slot.cleanup = slot.fn(); } },
    async dispose() { for (const slot of slots) slot?.cleanup?.(); for (const call of calls) if (!call.settled) call.fail(); await tick(); },
  };
}
const success = (call, data) => call.reply({ data, response: { status: 200 } });
const successPair = (h, data = overview(), page = activity()) => {
  success(h.calls.at(-2), data); success(h.calls.at(-1), page);
};

const helpers = await injected(source('useDashboardFacts.ts'), {
  useCallback() {}, useEffect() {}, useMemo() {}, useRef() {}, useState() {}, api: {},
});

test('actual hook frozen contract, parallel initial GETs, empty success and stable refresh', async () => {
  const h = await hookHost(); try {
    assert.deepEqual(Object.keys(h.out).sort(), ['overview', 'activity', 'overviewStatus', 'activityStatus', 'errors', 'refresh'].sort());
    assert.deepEqual(h.calls.map(c => c.path), ['/api/v1/library/overview', '/api/v1/dashboard/activity']);
    assert.equal(h.out.overviewStatus, 'loading'); assert.equal(h.out.activityStatus, 'loading');
    const refresh = h.out.refresh;
    successPair(h); await h.flush();
    assert.deepEqual(h.out.overview, overview()); assert.deepEqual(h.out.activity, activity());
    assert.equal(h.out.overviewStatus, 'ready'); assert.equal(h.out.activityStatus, 'ready'); assert.deepEqual(h.out.errors, []);
    h.render(); assert.equal(h.out.refresh, refresh);
  } finally { await h.dispose(); }
});

test('manual refresh waits for BOTH actual reads even after one fails, then returns false with partial success', async () => {
  const h = await hookHost(); try {
    successPair(h); await h.flush();
    let done = false; const work = h.out.refresh().then(value => { done = true; return value; });
    h.calls.at(-2).fail(); await h.flush();
    assert.equal(done, false); assert.equal(h.out.overviewStatus, 'error'); assert.deepEqual(h.out.overview, overview());
    assert(!h.out.errors.join().includes('secret'));
    success(h.calls.at(-1), activity({ more: true }));
    assert.equal(await work, false); await h.flush(); assert.equal(h.out.activity.more, true);
    const next = h.out.refresh(); successPair(h); assert.equal(await next, true); await h.flush(); assert.deepEqual(h.out.errors, []);
  } finally { await h.dispose(); }
});

test('refresh supersedes both requests; aborted transports can resolve late without committing', async () => {
  const h = await hookHost(); try {
    const initial = [...h.calls];
    const first = h.out.refresh(), old = h.calls.slice(-2);
    const second = h.out.refresh(), fresh = h.calls.slice(-2);
    assert(initial.every(c => c.options.signal.aborted)); assert(old.every(c => c.options.signal.aborted));
    success(initial[0], overview()); initial[1].fail();
    success(old[0], overview({ counts: { movies: '999', tv_shows: '0', episodes: '0', files: '0' } })); old[1].fail();
    assert.equal(await first, false); await h.flush(); assert.equal(h.out.overview, null); assert.equal(h.out.overviewStatus, 'loading');
    success(fresh[0], overview()); success(fresh[1], activity()); assert.equal(await second, true); await h.flush();
    assert.equal(h.out.overview.counts.movies, '0'); assert.deepEqual(h.out.errors, []);
  } finally { await h.dispose(); }
});

for (const patch of [{ userId: 'bob' }, { role: 'member' }, { userId: undefined }]) test(`scope switch ${JSON.stringify(patch)} masks synchronously and fences late completion`, async () => {
  const h = await hookHost(); try {
    successPair(h); await h.flush();
    const refresh = h.out.refresh, work = refresh(), old = h.calls.slice(-2);
    h.render(patch, false);
    assert.equal(h.out.overview, null); assert.equal(h.out.activity, null); assert.deepEqual(h.out.errors, []);
    assert.equal(h.out.refresh, refresh);
    const before = h.calls.length; assert.equal(await refresh(), false); assert.equal(h.calls.length, before);
    await h.flush(); assert(old.every(c => c.options.signal.aborted));
    success(old[0], overview({ recent: [importFact({ title: '旧管理员记录' })] })); success(old[1], activity());
    assert.equal(await work, false); await h.flush(); assert.equal(h.out.overview, null);
    if (patch.userId !== undefined || Object.hasOwn(patch, 'role')) { successPair(h); await h.flush(); assert.equal(h.out.overviewStatus, 'ready'); }
    else { assert.equal(h.calls.length, before); assert.equal(await refresh(), false); }
  } finally { await h.dispose(); }
});

test('A→B→A never revives the first scope; logged-out or missing-role mounts do not read', async () => {
  const h = await hookHost(); try {
    const old = [...h.calls]; h.render({ userId: 'bob' }); await h.flush(); h.render({ userId: 'alice' }); await h.flush();
    success(old[0], overview({ recent: [importFact()] })); old[1].fail(); await h.flush();
    assert.equal(h.out.overview, null); assert.deepEqual(h.out.errors, []);
    successPair(h); await h.flush(); assert.deepEqual(h.out.overview.recent, []);
  } finally { await h.dispose(); }
  for (const initial of [{ userId: undefined }, { role: undefined }]) {
    const empty = await hookHost(initial); try { assert.equal(empty.calls.length, 0); assert.equal(await empty.out.refresh(), false); assert.equal(empty.timers.size, 0); }
    finally { await empty.dispose(); }
  }
});

for (const lane of ['overview', 'activity']) test(`${lane} transient/malformed responses retain prior data, successful empty clears error`, async () => {
  const h = await hookHost(); try {
    successPair(h, overview({ recent: [importFact()] }), activity({ items: [task()] })); await h.flush();
    const previous = h.out[lane];
    for (const bad of [{}, { error: { detail: 'private' } }, { data: {} }, { data: lane === 'overview' ? overview({ counts: { movies: 9007199254740993 } }) : activity({ items: [null] }) }]) {
      const work = h.out.refresh(); const pair = h.calls.slice(-2), index = lane === 'overview' ? 0 : 1;
      pair[index].reply(bad); success(pair[1 - index], lane === 'overview' ? activity() : overview());
      assert.equal(await work, false); await h.flush(); assert.deepEqual(h.out[lane], previous); assert.equal(h.out[`${lane}Status`], 'error'); assert(h.out.errors.length); assert(!h.out.errors.join().includes('private'));
    }
    const work = h.out.refresh(); successPair(h); assert.equal(await work, true); await h.flush(); assert.deepEqual(h.out.errors, []);
  } finally { await h.dispose(); }
});

for (const lane of ['overview', 'activity']) test(`403 clears denied ${lane} while preserving the independently authorized lane`, async () => {
  const h = await hookHost(); try {
    successPair(h); await h.flush(); const work = h.out.refresh();
    const index = lane === 'overview' ? 0 : 1, pair = h.calls.slice(-2);
    pair[index].reply({ error: { detail: 'secret' }, response: { status: 403 } });
    success(pair[1 - index], lane === 'overview' ? activity() : overview());
    assert.equal(await work, false); await h.flush(); assert.equal(h.out[lane], null); assert.equal(h.out[`${lane}Status`], 'error');
    assert(h.out[lane === 'overview' ? 'activity' : 'overview']); assert.match(h.out.errors.join(), /无访问权限/);
  } finally { await h.dispose(); }
});

test('401 immediately clears BOTH private lanes, aborts the other and rejects late success', async () => {
  const h = await hookHost(); try {
    successPair(h); await h.flush(); const work = h.out.refresh(), pair = h.calls.slice(-2);
    pair[0].reply({ response: { status: 401 } }); await h.flush();
    assert.equal(h.out.overview, null); assert.equal(h.out.activity, null); assert(pair[1].options.signal.aborted);
    success(pair[1], activity({ items: [task()] })); assert.equal(await work, false); await h.flush(); assert.equal(h.out.activity, null);
  } finally { await h.dispose(); }
});

test('visible-only 5s/30s polling skips in-flight reads and owns every timer/listener', async () => {
  const h = await hookHost(); try {
    assert.deepEqual([...h.timers.values()].map(t => t.ms).sort((a, b) => a - b), [5000, 30000]);
    h.fire(5000); h.fire(30000); assert.equal(h.calls.length, 2);
    successPair(h); await h.flush(); h.fire(5000); assert.equal(h.calls.length, 3); assert.equal(h.calls.at(-1).path, '/api/v1/dashboard/activity');
    success(h.calls.at(-1), activity()); await h.flush(); h.fire(30000); assert.equal(h.calls.at(-1).path, '/api/v1/library/overview');
    success(h.calls.at(-1), overview()); await h.flush();
    h.visibility('hidden'); assert.equal(h.timers.size, 0); const before = h.calls.length; h.fire(5000); assert.equal(h.calls.length, before);
    h.visibility('visible'); assert.equal(h.calls.length, before + 2); assert.equal(h.timers.size, 2); assert.equal(h.listeners.size, 1);
  } finally { await h.dispose(); assert.equal(h.timers.size, 0); assert.equal(h.listeners.size, 0); }
});

test('Strict Mode replay and unmount abort old requests and prevent all late writes', async () => {
  const h = await hookHost(); const old = [...h.calls];
  h.replay(); assert(old.every(c => c.options.signal.aborted)); assert.equal(h.timers.size, 2); assert.equal(h.listeners.size, 1);
  success(old[0], overview()); old[1].fail(); await h.flush(); assert.equal(h.out.overview, null);
  const work = h.out.refresh(), pending = h.calls.slice(-2);
  // Deliberately leave transport unresolved through cleanup.
  const reply = pending.map(c => c.reply); pending.forEach(c => { c.settled = true; });
  await h.dispose(); const updates = h.updates;
  pending.forEach(c => { c.settled = false; }); reply[0]({ data: overview() }); reply[1]({ data: activity() });
  assert.equal(await work, false); await tick(); assert.equal(h.updates, updates);
  assert(pending.every(c => c.options.signal.aborted)); assert.equal(await h.out.refresh(), false);
  assert.equal(h.timers.size, 0); assert.equal(h.listeners.size, 0);
});

test('decimal counts preserve bigint precision and reject malformed or oversized numeric strings', () => {
  assert.equal(helpers.countFact('9007199254740993'), '9,007,199,254,740,993'); assert.equal(helpers.countFact('0'), '0');
  for (const value of [0, null, '', '01', '-1', '1.1', '1e3', ' 1', '1\n', '9'.repeat(129)]) assert.equal(helpers.decimalFact(value), null, String(value));
});

test('per-root bigint capacity handles zero available, full/free and bounds displayed percent', () => {
  assert.equal(helpers.capacityFact(rootFact({ available_bytes: '0' })).percent, 100);
  assert.equal(helpers.capacityFact(rootFact({ available_bytes: '100' })).percent, 0);
  const total = '900719925474099300000';
  const sample = helpers.capacityFact(rootFact({ total_bytes: total, available_bytes: '1' }));
  assert.equal(sample.total, BigInt(total)); assert.equal(sample.available, 1n); assert.equal(sample.percent, 99.99);
  assert.equal(helpers.byteFact(0n), '0 B'); assert.equal(helpers.byteFact(1024n), '1.0 KiB');
});

test('capacity unknown/invalid/offline differs from a valid stale sample', () => {
  assert.equal(helpers.capacityFact(rootFact({ capacity_state: 'stale' })).percent, 75);
  for (const patch of [{ capacity_state: 'unknown' }, { availability: 'unknown' }, { health: 'offline' }, { total_bytes: '0', available_bytes: '0' }, { available_bytes: '101' }, { total_bytes: '-1' }, { sampled_at: undefined }, { sampled_at: 'invalid' }, { available_bytes: 'bad' }]) assert.equal(helpers.capacityFact(rootFact(patch)), null, JSON.stringify(patch));
});

test('progress requires finite fraction AND known source, no byte fallback or early 100', () => {
  for (const progress_source of ['native_percent', 'verified_bytes']) {
    assert.equal(helpers.activityProgress(task({ progress: 0, progress_source })), 0);
    assert.equal(helpers.activityProgress(task({ progress: 1, progress_source })), 100);
    assert.equal(helpers.activityProgress(task({ progress: 0.9999999, progress_source })), 99.99);
  }
  for (const progress of [undefined, null, NaN, Infinity, -0.1, 1.01, '0.5']) assert.equal(helpers.activityProgress(task({ progress, progress_source: 'native_percent' })), null);
  for (const progress_source of [undefined, '', 'bytes']) assert.equal(helpers.activityProgress(task({ progress: 0.5, progress_source, downloaded_bytes: '50', total_bytes: '100' })), null);
});

test('trusted posters accept only fixed projected TMDB source without arbitrary hosts, queries, credentials or paths', () => {
  const good = 'https://image.tmdb.org/t/p/w342/Ab_9-x.jpg'; assert.equal(helpers.trustedPoster(good), good);
  for (const url of [null, {}, '/poster.jpg', good + '?secret=x', good + '#secret', good + '\n', good.replace('w342', 'original'), good.replace('image.tmdb.org', 'image.tmdb.org.evil'), good.replace('https://', 'https://user:secret@'), good.replace('Ab_9-x', '../secret'), 'javascript:alert(1)']) assert.equal(helpers.trustedPoster(url), undefined);
});

// Transpile the actual JSX and exercise rendered props/handlers without a server or browser.
async function panel(facts) {
  const scrolls = [], node = { clientWidth: 260, scrollWidth: 900, scrollBy: value => scrolls.push(['by', value]), scrollTo: value => scrolls.push(['to', value]) };
  const h = (type, props, ...children) => {
    props = { ...props, children: children.flat(Infinity).filter(child => child !== null && child !== undefined && child !== false) };
    if (typeof type === 'function') return type(props);
    if (props.ref) props.ref.current = node;
    return { type, props, children: props.children };
  };
  const module = await injected(source('DashboardFactsPanel.tsx'), { ...helpers, h, Fragment: 'fragment', useRef: () => ({ current: null }), Link: 'a', ChevronLeft: 'svg', ChevronRight: 'svg', Film: 'svg', Tv: 'svg' });
  const tree = module.DashboardFactsPanel({ facts });
  const nodes = [];
  const walk = value => { if (value && typeof value === 'object') { nodes.push(value); value.children.forEach(walk); } };
  walk(tree);
  const text = value => typeof value === 'object' ? value.children.map(text).join('') : String(value);
  return { nodes, text: text(tree), scrolls, node };
}
const facts = (patch = {}) => ({ overview: overview(), activity: activity(), overviewStatus: 'ready', activityStatus: 'ready', errors: [], refresh: async () => true, ...patch });

test('panel registers all four shared imported count cards, specials and empty states with only existing routes', async () => {
  const p = await panel(facts());
  assert.equal(p.nodes.filter(n => n.type === 'dt').length, 4); assert.deepEqual(p.nodes.filter(n => n.type === 'dd').map(n => n.children[0]), ['0', '0', '0', '0']);
  for (const word of ['共享媒体库', '当前已入库', '含特别篇', '暂无存储根', '暂无活动记录', '暂无入库记录']) assert(p.text.includes(word), word);
  assert(!/订阅|精选|推荐|满意/.test(p.text));
  assert(p.nodes.filter(n => n.type === 'a').every(n => ['/activity', '/media'].includes(n.props.to)));
});

test('panel error differs from empty success and retained data is visibly labelled', async () => {
  const p = await panel(facts({ overviewStatus: 'error', activityStatus: 'error', errors: ['概览读取失败'] }));
  assert.match(p.text, /上次读取结果/); assert(p.nodes.some(n => n.props.role === 'alert'));
  const denied = await panel(facts({ overview: null, activity: null, overviewStatus: 'error', activityStatus: 'error' }));
  assert.match(denied.text, /读取失败/); assert(!/暂无入库记录|暂无活动记录|暂无存储根/.test(denied.text));
  assert.deepEqual(denied.nodes.filter(n => n.type === 'dd').map(n => n.children[0]), ['—', '—', '—', '—']);
});

test('panel keeps root samples separate with stale/unknown/offline and availability labels', async () => {
  const p = await panel(facts({ overview: overview({ roots: [rootFact({ root_id: 'stale', capacity_state: 'stale' }), rootFact({ root_id: 'offline', health: 'offline', availability: 'unknown', capacity_state: 'unknown' })] }) }));
  assert.equal(p.nodes.filter(n => n.type === 'progress').length, 1);
  for (const word of ['容量样本已过期', '容量未知', '扫描可用性待确认', '根离线', '采样时间']) assert(p.text.includes(word), word);
  assert.equal(p.nodes.filter(n => n.type === 'dd').length, 4);
});

test('panel uses neutral task records and TV observations, unknown/review/organizing never become downloading', async () => {
  for (const [status, label] of [['review_required', '待人工处理'], ['waiting_organization', '等待整理'], ['partial', '部分完成'], ['conflict', '存在冲突']]) assert.equal(helpers.activityStatus(status), label);
  const p = await panel(facts({ activity: activity({ more: true, items: [task({ operation_id: 'a', status: 'unknown', progress: 0.999999, progress_source: 'native_percent' }), task({ operation_id: 'b', status: 'review' }), task({ operation_id: 'c', status: 'organizing', progress: 0, progress_source: 'verified_bytes' }), task({ operation_id: 'd', status: 'new-state', progress: 0.5 })] }) }));
  for (const word of ['任务记录', 'TV 观测', '状态未知', '待人工处理', '整理中', '进度未知', '记录更新时间', '99.99%', '还有更多记录']) assert(p.text.includes(word), word);
  assert(!/下载中|100%|实时探测/.test(p.text)); assert.equal(p.nodes.filter(n => n.type === 'progress').length, 2);
});

test('real import carousel uses quiet placeholder, safe image error fallback and only media-library links', async () => {
  const good = 'https://image.tmdb.org/t/p/w342/real.jpg';
  const p = await panel(facts({ overview: overview({ recent: [importFact({ poster_url: good }), importFact({ media_id: 'second', title: '无海报', poster_url: 'https://private/secret' })] }) }));
  assert.match(p.text, /真实入库影片/); assert.match(p.text, /首次入库/); assert.match(p.text, /暂无海报/);
  const images = p.nodes.filter(n => n.type === 'img'); assert.equal(images.length, 1); assert.equal(images[0].props.src, good); assert.equal(images[0].props.referrerPolicy, 'no-referrer');
  const image = { hidden: false }; images[0].props.onError({ currentTarget: image }); assert.equal(image.hidden, true);
  assert(p.nodes.filter(n => n.type === 'a').every(n => ['/media', '/activity'].includes(n.props.to)));
});

test('carousel prev/next, Arrow keys and Home/End call owned scroll container accessibly', async () => {
  const p = await panel(facts({ overview: overview({ recent: [importFact(), importFact({ media_id: 'two' })] }) }));
  const buttons = p.nodes.filter(n => n.type === 'button'); assert.equal(buttons.length, 2);
  assert(buttons.every(n => n.props.type === 'button' && n.props['aria-label'] && !n.props.disabled));
  buttons[0].props.onClick(); buttons[1].props.onClick();
  assert(p.scrolls[0][1].left < 0); assert(p.scrolls[1][1].left > 0);
  const rail = p.nodes.find(n => n.type === 'ul' && n.props.tabIndex === 0); assert(rail.props['aria-label'].includes('方向键'));
  let prevented = 0;
  for (const key of ['ArrowLeft', 'ArrowRight', 'Home', 'End']) rail.props.onKeyDown({ key, target: p.node, currentTarget: p.node, preventDefault() { prevented++; } });
  assert.equal(prevented, 4); assert.deepEqual(p.scrolls.slice(-2).map(x => x[1].left), [0, 900]);
  const before = p.scrolls.length; rail.props.onKeyDown({ key: 'ArrowRight', target: {}, currentTarget: p.node }); assert.equal(p.scrolls.length, before);
  const empty = await panel(facts()); assert(empty.nodes.filter(n => n.type === 'button').every(n => n.props.disabled));
});

test('CSS is scoped, uses existing tokens, fixed touch controls and responsive scroll snap without narrow-screen wrapping', () => {
  const css = source('dashboard-facts.css');
  assert.match(css, /scroll-snap-type: x mandatory/); assert.match(css, /scroll-snap-align: start/); assert.match(css, /min-width: 0/);
  assert.match(css, /width: 44px/); assert.match(css, /height: 44px/); assert.match(css, /flex-shrink: 0/);
  assert.match(css, /repeat\(2, minmax\(0, 1fr\)\)/); assert.match(css, /var\(--surface\)/);
  for (const selector of css.matchAll(/(?:^|[{}])\s*([^{}]+)\{/g)) {
    if (selector[1].trim().startsWith('@media')) continue;
    assert(selector[1].split(',').every(part => part.trim().startsWith('.dashboard-facts')), selector[1]);
  }
});
