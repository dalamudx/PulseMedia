import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import ts from 'typescript';

const root = new URL('../src/features/dashboard/', import.meta.url);
const source = name => readFileSync(new URL(name, root), 'utf8');
const tick = async () => { for (let i = 0; i < 16; i++) await Promise.resolve(); };
async function injected(text, values) {
  const key = `__rates_${crypto.randomUUID().replaceAll('-', '')}`;
  globalThis[key] = values;
  const code = `const {${Object.keys(values).join(',')}}=globalThis[${JSON.stringify(key)}];\n${text.replace(/^import[\s\S]*?;\n/gm, '')}`;
  try { return await import(`data:text/javascript;base64,${Buffer.from(ts.transpileModule(code, { compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.ESNext, jsx: ts.JsxEmit.React, jsxFactory: 'h', jsxFragmentFactory: 'Fragment' } }).outputText).toString('base64')}#${crypto.randomUUID()}`); }
  finally { delete globalThis[key]; }
}
const facts = await injected(source('useDashboardFacts.ts'), {});
const helpers = await injected(source('useDownloadGlobalRates.ts'), { byteFact: facts.byteFact, decimalFact: facts.decimalFact });
const at = '2026-10-22T12:00:00Z';
const point = (seconds, down, up = '0') => ({ at: new Date(Date.parse(at) + seconds * 1000).toISOString(), download_bytes_per_second: down, upload_bytes_per_second: up, download_known_clients: down === null ? 0 : 1, upload_known_clients: up === null ? 0 : 1 });
const snapshot = (name = 'native source') => ({ enabled: true, observed_at: at, freshness_seconds: 45, download_bytes_per_second: '9007199254740993', upload_bytes_per_second: '0', download_known_clients: 1, upload_known_clients: 1,
  clients: [{ id: '00000000-0000-4000-8000-000000000001', name, client_type: 'qbittorrent', state: 'observed', failure_code: '', observed_at: at, download_bytes_per_second: '9007199254740993', upload_bytes_per_second: '0', last_good: { observed_at: at, download_bytes_per_second: '9007199254740993', upload_bytes_per_second: '0' } }],
  series: [point(-30, '0'), point(-20, null), point(-10, '4503599627370496'), point(0, '9007199254740993')],
});

// Execute the actual hook with deterministic React slots and controlled transport.
async function host(initial = {}) {
  const slots = [], effects = [], calls = [], intervals = new Map(), timeouts = new Map(), listeners = new Set();
  let cursor = 0, timerID = 0, clock = 0, out, updates = 0;
  let props = { userId: 'alice', role: 'admin', ...initial };
  const equal = (a, b) => a && b && a.length === b.length && a.every((v, i) => Object.is(v, b[i]));
  const memo = (fn, deps) => { const i = cursor++, old = slots[i]; if (!old || !equal(old.deps, deps)) slots[i] = { deps, value: fn() }; return slots[i].value; };
  const document = { visibilityState: 'visible', addEventListener(name, fn) { assert.equal(name, 'visibilitychange'); listeners.add(fn); }, removeEventListener(name, fn) { assert.equal(name, 'visibilitychange'); listeners.delete(fn); } };
  const values = {
    document, performance: { now: () => clock }, byteFact: facts.byteFact, decimalFact: facts.decimalFact,
    setInterval(fn, ms) { const id = ++timerID; intervals.set(id, { fn, ms }); return id; }, clearInterval(id) { intervals.delete(id); },
    setTimeout(fn, ms) { const id = ++timerID; timeouts.set(id, { fn, ms }); return id; }, clearTimeout(id) { timeouts.delete(id); },
    useMemo: memo, useCallback: (fn, deps) => memo(() => fn, deps),
    useRef(value) { const i = cursor++; return slots[i] ?? (slots[i] = { current: value }); },
    useState(initialValue) { const i = cursor++; if (!slots[i]) slots[i] = { value: typeof initialValue === 'function' ? initialValue() : initialValue }; return [slots[i].value, value => { updates++; slots[i].value = typeof value === 'function' ? value(slots[i].value) : value; }]; },
    useEffect(fn, deps) { const i = cursor++, old = slots[i]; if (!old || !equal(old.deps, deps)) effects.push(() => { old?.cleanup?.(); slots[i] = { deps, fn, cleanup: fn() }; }); },
    api: { GET(path, options) {
      assert.equal(path, '/api/v1/download-clients/global-rates'); assert.equal(options.cache, 'no-store');
      const call = { options, settled: false }; calls.push(call);
      return new Promise((resolve, reject) => {
        call.reply = (data = snapshot(), status = 200) => { assert(!call.settled); call.settled = true; resolve({ data: status < 400 ? data : undefined, error: status >= 400 ? { detail: 'private source credentials' } : undefined, response: { ok: status >= 200 && status < 300, status } }); };
        call.fail = () => { assert(!call.settled); call.settled = true; reject(Error('private endpoint credentials')); };
      });
    } },
  };
  const module = await injected(source('useDownloadGlobalRates.ts'), values);
  const commit = () => { for (const effect of effects.splice(0)) effect(); };
  const render = (patch = {}, runEffects = true) => { props = { ...props, ...patch }; cursor = 0; out = module.useDownloadGlobalRates(props); if (runEffects) commit(); return out; };
  const flush = async () => { await tick(); return render(); };
  const unmount = () => { for (const slot of slots) slot?.cleanup?.(); };
  render(); await flush();
  return { calls, intervals, timeouts, listeners, render, flush, commit, unmount, get out() { return out; }, get updates() { return updates; },
    fire(ms) { for (const timer of [...intervals.values()]) if (timer.ms === ms) timer.fn(); for (const [id, timer] of [...timeouts]) if (timer.ms === ms) { timeouts.delete(id); timer.fn(); } },
    advance(ms) { clock += ms; for (const timer of [...intervals.values()]) if (timer.ms === 1000) timer.fn(); },
    visibility(value) { document.visibilityState = value; for (const fn of listeners) fn(); },
    replay() { for (const slot of slots) if (slot?.fn) { slot.cleanup?.(); slot.cleanup = slot.fn(); } },
    async dispose() { unmount(); for (const call of calls) if (!call.settled) call.fail(); await tick(); assert.equal(intervals.size, 0); assert.equal(timeouts.size, 0); assert.equal(listeners.size, 0); },
  };
}

test('snapshot boundary rejects malformed numbers, identities, future history and unknown states', () => {
  assert.equal(helpers.validGlobalRateSnapshot(snapshot()), true);
  const invalid = [v => { v.download_bytes_per_second = 9007199254740993; }, v => { v.upload_bytes_per_second = '01'; }, v => { v.download_bytes_per_second = '9'.repeat(129); }, v => { v.freshness_seconds = 46; }, v => { v.clients.push({ ...v.clients[0] }); }, v => { v.clients[0].failure_code = 'private secret'; }, v => { v.clients[0].state = 'complete'; }, v => { v.clients[0].observed_at = 'invalid'; }, v => { v.clients[0].last_good.upload_bytes_per_second = '-1'; }, v => { v.series[0].at = '2099-01-01T00:00:00Z'; }, v => { v.series[1].at = v.series[0].at; }, v => { v.series = Array.from({ length: 63 }, () => point(0, '0')); }, v => { v.download_known_clients = 2; }];
  for (const change of invalid) { const value = snapshot(); change(value); assert.equal(helpers.validGlobalRateSnapshot(value), false); }
});

test('curve uses exact bigint normalization, known zero, independent direction gaps and immutable input', () => {
  const data = snapshot(), before = JSON.stringify(data);
  const curve = helpers.globalRateCurve(data.series, 'download');
  assert.equal(curve.max, '9007199254740993'); assert.deepEqual(curve.segments.map(s => s.length), [1, 2]);
  assert.equal(curve.segments[0][0].y, 120); assert.equal(curve.segments[1][1].y, 8);
  assert(curve.segments.flat().every(p => Number.isFinite(p.x) && Number.isFinite(p.y) && p.x >= 8 && p.x <= 612 && p.y >= 8 && p.y <= 120));
  assert.equal(helpers.globalRateCurve(data.series, 'upload').segments.length, 1);
  assert.equal(JSON.stringify(data), before); assert.equal(helpers.globalRateText('0'), '0 B/s'); assert.equal(helpers.globalRateText(null), '未知');
});

test('native observation expires locally at the original 45-second boundary, direction by direction', () => {
  const data = snapshot(); data.clients[0].observed_at = new Date(Date.parse(at) - 44000).toISOString();
  assert.equal(helpers.currentGlobalRate(data, 'download', 1000).value, '9007199254740993');
  assert.equal(helpers.currentGlobalRate(data, 'download', 1001).value, null);
  data.clients[0].upload_bytes_per_second = null;
  assert.equal(helpers.currentGlobalRate(data, 'upload', 0).value, null); assert.equal(helpers.currentGlobalRate(data, 'download', 0).known, 1);
  data.clients[0].state = 'failed'; assert.equal(helpers.currentGlobalRate(data, 'download', 0).value, null);
  data.enabled = false; assert.equal(helpers.currentGlobalRate(data, 'download', 0).known, 0);
});

test('actual hook reads one private GET, skips overlap and cleans request deadline after success', async () => {
  const h = await host(); try {
    assert.equal(h.calls.length, 1); assert.equal(h.intervals.size, 2); assert.equal(h.timeouts.size, 1);
    h.fire(5000); assert.equal(h.calls.length, 1);
    h.calls[0].reply(); await h.flush(); assert.equal(h.out.data.download_bytes_per_second, '9007199254740993'); assert.equal(h.out.busy, false); assert.equal(h.timeouts.size, 0);
    const refresh = h.out.refresh; h.render(); assert.equal(h.out.refresh, refresh);
    h.advance(46000); await h.flush(); assert.equal(helpers.currentGlobalRate(h.out.data, 'download', h.out.elapsedMs).value, null);
  } finally { await h.dispose(); }
});

test('A to B to A masks before passive cleanup and rejects both old accounts late responses', async () => {
  const h = await host(); try {
    h.calls[0].reply(snapshot('alice')); await h.flush();
    const oldA = h.out.refresh(); const pendingA = h.calls.at(-1);
    assert.equal(h.render({ userId: 'bob' }, false).data, null);
    pendingA.reply(snapshot('late-alice')); assert.equal(await oldA, false); h.commit(); await h.flush();
    h.calls.at(-1).reply(snapshot('bob')); await h.flush(); assert.equal(h.out.data.clients[0].name, 'bob');
    const oldB = h.out.refresh(), pendingB = h.calls.at(-1);
    assert.equal(h.render({ userId: 'alice' }, false).data, null);
    h.commit(); assert(pendingB.options.signal.aborted); pendingB.reply(snapshot('late-bob')); assert.equal(await oldB, false); await h.flush();
    h.calls.at(-1).reply(snapshot('new-alice')); await h.flush(); assert.equal(h.out.data.clients[0].name, 'new-alice');
  } finally { await h.dispose(); }
});

test('members, logout and role downgrade never fetch or expose admin observations', async () => {
  for (const props of [{ role: 'member' }, { userId: undefined }]) {
    const h = await host(props); try { assert.equal(h.calls.length, 0); assert.equal(h.out.data, null); assert.equal(await h.out.refresh(), false); } finally { await h.dispose(); }
  }
  const h = await host(); try {
    h.calls[0].reply(); await h.flush(); const work = h.out.refresh(), late = h.calls.at(-1);
    assert.equal(h.render({ role: 'member' }, false).data, null); h.commit(); await h.flush(); late.reply(); assert.equal(await work, false);
    assert.equal(h.out.data, null); assert.equal(h.intervals.size, 0); assert.equal(h.timeouts.size, 0);
  } finally { await h.dispose(); }
});

test('hidden pages abort transport and all timers, visible pages resume without stale writes', async () => {
  const h = await host(); try {
    const old = h.calls[0]; h.visibility('hidden'); assert(old.options.signal.aborted); assert.equal(h.intervals.size, 0); assert.equal(h.timeouts.size, 0);
    old.reply(snapshot('hidden-late')); await h.flush(); assert.equal(h.out.data, null); h.fire(5000); assert.equal(h.calls.length, 1);
    h.visibility('visible'); assert.equal(h.calls.length, 2); h.calls[1].reply(); await h.flush(); assert(h.out.data); assert.equal(h.intervals.size, 2);
  } finally { await h.dispose(); }
});

test('503, malformed data, 401 and 403 withdraw current rates; denied polling requires explicit refresh', async () => {
  for (const status of [503, 401, 403, 200]) {
    const h = await host(); try {
      h.calls[0].reply(); await h.flush(); const work = h.out.refresh(); h.calls.at(-1).reply(status === 200 ? {} : undefined, status); assert.equal(await work, false); await h.flush();
      assert.equal(h.out.data, null); assert(h.out.error); assert(!h.out.error.includes('private'));
      const before = h.calls.length; h.fire(5000);
      if (status === 401 || status === 403) { assert.equal(h.calls.length, before); const retry = h.out.refresh(); h.calls.at(-1).reply(); assert.equal(await retry, true); }
      else { assert.equal(h.calls.length, before + 1); h.calls.at(-1).reply(); }
      await h.flush(); assert(h.out.data);
    } finally { await h.dispose(); }
  }
});

test('timed-out transport cannot publish late success and can be superseded safely', async () => {
  const h = await host(); try {
    const old = h.calls[0]; h.fire(10000); await h.flush(); assert.equal(h.out.busy, false); assert(h.out.error); assert(old.options.signal.aborted); assert.equal(h.timeouts.size, 0);
    const retry = h.out.refresh(); old.reply(snapshot('timed-out')); await tick(); h.calls.at(-1).reply(snapshot('new')); assert.equal(await retry, true); await h.flush(); assert.equal(h.out.data.clients[0].name, 'new');
  } finally { await h.dispose(); }
});

test('Strict Mode and unmount dispose all owned timers and block late updates', async () => {
  const h = await host(); try {
    const old = h.calls[0]; h.replay(); assert(old.options.signal.aborted); assert.equal(h.intervals.size, 2); assert.equal(h.timeouts.size, 1);
    const pending = h.calls.at(-1); h.unmount(); const updates = h.updates;
    old.reply(); pending.reply(); await tick(); assert.equal(h.updates, updates); assert.equal(h.intervals.size, 0); assert.equal(h.timeouts.size, 0); assert.equal(h.listeners.size, 0);
  } finally { await h.dispose(); }
});

const h = (type, props, ...children) => typeof type === 'function' ? type({ ...props, children }) : ({ type, props: props ?? {}, children: children.flat(Infinity) });
const walk = node => !node || typeof node !== 'object' ? [] : [node, ...(node.children ?? []).flatMap(walk)];
const text = node => typeof node === 'string' || typeof node === 'number' ? String(node) : (node?.children ?? []).map(text).join(' ');
async function panel(view, role = 'admin') {
  const module = await injected(source('DownloadGlobalRatesPanel.tsx'), { h, Fragment: 'fragment', Link: 'a', RefreshCw: 'icon', useMemo: fn => fn(), factTime: facts.factTime, ...helpers, useDownloadGlobalRates: () => ({ data: null, error: '', busy: false, elapsedMs: 0, refresh: async () => true, ...view }) });
  return module.DownloadGlobalRatesPanel({ userId: 'alice', role });
}

test('actual panel exposes named graphs, known zero, last-good dates and keyboard-readable history', async () => {
  const tree = await panel({ data: snapshot() }), nodes = walk(tree);
  assert.equal(nodes.filter(n => n.props.role === 'img').length, 2); assert.equal(nodes.filter(n => n.type === 'circle').length, 1);
  assert(nodes.some(n => n.type === 'table' && walk(n).some(child => child.type === 'caption')));
  assert(nodes.some(n => n.type === 'summary' && text(n).includes('历史观测数据')));
  assert(text(tree).includes('0 B/s')); assert(text(tree).includes('最后成功')); assert.equal(await panel({ data: snapshot() }, 'member'), null);
});

test('actual panel distinguishes disabled, no clients, stale rates and read failure', async () => {
  const disabled = snapshot(); disabled.enabled = false; assert(text(await panel({ data: disabled })).includes('速率采集尚未开启'));
  const empty = snapshot(); empty.clients = []; assert(text(await panel({ data: empty })).includes('尚未配置下载器'));
  const stale = await panel({ data: snapshot(), elapsedMs: 46000 }); assert(text(stale).includes('当前速率未知')); assert.equal(walk(stale).filter(n => n.props.className === 'global-rate-value' && text(n) === '未知').length, 2);
  const error = await panel({ error: '速率读取失败，请稍后重试。' }); assert(walk(error).some(n => n.props.role === 'alert')); assert.equal(walk(error).filter(n => n.props.role === 'img').length, 0);
});


test('mixed qBittorrent, Transmission and rTorrent response reaches the actual hook and labels', async () => {
  const value = snapshot();
  value.clients = ['qbittorrent', 'transmission', 'rtorrent'].map((client_type, index) => ({ ...value.clients[0], id: `00000000-0000-4000-8000-00000000000${index + 1}`, name: `source ${index + 1}`, client_type, download_bytes_per_second: String(1024 * (index + 1)), last_good: undefined }));
  value.download_bytes_per_second = '6144'; value.download_known_clients = 3; value.upload_known_clients = 3;
  value.series = [{ ...point(0, '6144'), download_known_clients: 3, upload_known_clients: 3 }];
  assert.equal(helpers.validGlobalRateSnapshot(value), true);
  const client = await host();
  try {
    client.calls[0].reply(value); await client.flush();
    assert.equal(client.out.error, ''); assert.equal(client.out.data.clients.length, 3);
    const rendered = text(await panel({ data: client.out.data }));
    for (const label of ['qBittorrent', 'Transmission', 'rTorrent', '6.0 KiB/s']) assert(rendered.includes(label), label);
    for (const badType of ['unknown', 'constructor', '__proto__']) {
      const invalid = structuredClone(value); invalid.clients[2].client_type = badType;
      assert.equal(helpers.validGlobalRateSnapshot(invalid), false);
    }
    const invalid = structuredClone(value); invalid.clients[2].download_bytes_per_second = -1;
    const refresh = client.out.refresh(); client.calls.at(-1).reply(invalid);
    assert.equal(await refresh, false); await client.flush(); assert.equal(client.out.data, null);
  } finally { await client.dispose(); }
});
