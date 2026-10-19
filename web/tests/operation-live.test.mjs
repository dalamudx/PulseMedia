import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import ts from 'typescript';

const root = new URL('../src/features/configuration/', import.meta.url);
const source = name => readFileSync(new URL(name, root), 'utf8');
const load = text => import(`data:text/javascript;base64,${Buffer.from(ts.transpileModule(text, { compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.ESNext } }).outputText).toString('base64')}#${crypto.randomUUID()}`);
const model = await load(source('operationActivityModel.ts'));
const tick = async () => { for (let i = 0; i < 20; i++) await Promise.resolve(); };
const detail = (patch = {}) => ({ operation_id: 'op', status: 'tracking', plan_id: 'plan', plan_status: 'approved', ...patch });
const step = (patch = {}) => ({ step_id: 'step', order: 1, action: 'hardlink', state: 'pending', file_name: 'file.mkv', updated_at: '2026-10-19T12:00:00Z', ...patch });
const page = (patch = {}) => ({ operation_id: 'op', plan_id: 'plan', plan_status: 'approved', items: [], observed_at: '2026-10-19T12:00:00Z', ...patch });
const ok = (call, data) => call.reply({ data, response: { status: 200 } });
const pair = (h, d = detail(), p = page()) => { ok(h.calls.at(-2), d); ok(h.calls.at(-1), p); };
async function host(initial = {}) {
  const slots = [], effects = new Map(), calls = [], timers = new Map(), listeners = new Set();
  let cursor = 0, output, updates = 0, timerId = 0;
  let props = { operationId: 'op', userId: 'alice', role: 'admin', refreshVersion: 0, ...initial };
  const equal = (a, b) => a && b && a.length === b.length && a.every((v, i) => Object.is(v, b[i]));
  const memo = (fn, deps) => { const i = cursor++, old = slots[i]; if (!old || !equal(old.deps, deps)) slots[i] = { deps, value: fn() }; return slots[i].value; };
  const document = { visibilityState: 'visible', addEventListener(name, fn) { assert.equal(name, 'visibilitychange'); listeners.add(fn); }, removeEventListener(name, fn) { assert.equal(name, 'visibilitychange'); listeners.delete(fn); } };
  const values = {
    ...model, document,
    useCallback: (fn, deps) => memo(() => fn, deps),
    useRef(value) { const i = cursor++; return slots[i] ?? (slots[i] = { current: value }); },
    useState(initialValue) { const i = cursor++; if (!slots[i]) slots[i] = { value: typeof initialValue === 'function' ? initialValue() : initialValue }; return [slots[i].value, value => { updates++; slots[i].value = typeof value === 'function' ? value(slots[i].value) : value; }]; },
    useEffect(fn, deps) { const i = cursor++, old = slots[i]; if (!old || !equal(old.deps, deps)) effects.set(i, () => { old?.cleanup?.(); slots[i] = { deps, fn, cleanup: fn() }; }); },
    setInterval(fn, ms) { const id = ++timerId; timers.set(id, { fn, ms }); return id; }, clearInterval(id) { timers.delete(id); },
    api: { GET(path, options) {
      assert(['/api/v1/operations/{id}', '/api/v1/operations/{id}/timeline'].includes(path)); assert.equal(options.cache, 'no-store');
      const call = { path, options, settled: false }; calls.push(call);
      return new Promise((resolve, reject) => {
        call.reply = value => { assert(!call.settled); call.settled = true; resolve(value); };
        call.fail = () => { assert(!call.settled); call.settled = true; reject(Error('SECRET URL/cipher')); };
      });
    } },
  };
  const key = `__live_${crypto.randomUUID().replaceAll('-', '')}`; globalThis[key] = values;
  let module;
  try { module = await load(`const {${Object.keys(values).join(',')}}=globalThis[${JSON.stringify(key)}];\n${source('useOperationLive.ts').replace(/^import[^;]*;\n/gm, '').replace(/^export type \{[^;]*;\n/gm, '')}`); }
  finally { delete globalThis[key]; }
  const render = (patch = {}, runEffects = true) => { props = { ...props, ...patch }; cursor = 0; output = module.useOperationLive(props); if (runEffects) { const pending = [...effects.values()]; effects.clear(); for (const fn of pending) fn(); } return output; };
  const flush = async () => { await tick(); return render(); };
  render(); await flush();
  return { calls, timers, listeners, render, flush, get out() { return output; }, get updates() { return updates; },
    fire() { for (const t of [...timers.values()]) { assert.equal(t.ms, 5000); t.fn(); } },
    visibility(value) { document.visibilityState = value; for (const fn of listeners) fn(); },
    replay() { for (const slot of slots) if (slot?.fn) { slot.cleanup?.(); slot.cleanup = slot.fn(); } },
    unmount() { for (const slot of slots) slot?.cleanup?.(); },
    async dispose() { this.unmount(); for (const call of calls) if (!call.settled) call.fail(); await tick(); assert.equal(timers.size, 0); assert.equal(listeners.size, 0); },
  };
}

test('frozen hook returns parallel detail/timeline, valid empty plan, stable refresh and waits both actual reads', async () => {
  const h = await host(); try {
    assert.deepEqual(Object.keys(h.out).sort(), ['detail', 'timeline', 'steps', 'loading', 'loadingMore', 'error', 'timelineError', 'detailStale', 'timelineStale', 'canAct', 'refresh', 'loadMore'].sort());
    assert.equal(h.calls.length, 2); assert.equal(h.calls[1].options.params.query.limit, 50); assert.equal(h.out.canAct, false);
    const refresh = h.out.refresh, work = refresh(); let resolved = false; void work.then(() => { resolved = true; });
    h.fire(); assert.equal(h.calls.length, 2); ok(h.calls[0], detail()); await h.flush(); assert.equal(resolved, false); assert.equal(h.out.canAct, false);
    ok(h.calls[1], page()); assert.equal(await work, true); await h.flush(); assert.equal(h.out.canAct, true); assert.deepEqual(h.out.steps, []); assert.equal(h.out.refresh, refresh);
    const retry = refresh(); await h.flush(); assert.equal(h.out.canAct, false); pair(h); assert.equal(await retry, true);
  } finally { await h.dispose(); }
});

for (const patch of [{ userId: 'bob' }, { role: 'member' }, { operationId: 'other' }, { operationId: undefined }, { userId: undefined }, { role: undefined }]) test(`synchronous scope masking and late-response fencing ${JSON.stringify(patch)}`, async () => {
  const h = await host(); try {
    pair(h, detail(), page({ items: [step()] })); await h.flush(); const refresh = h.out.refresh;
    const work = refresh(), old = h.calls.slice(-2); h.render(patch, false);
    assert.equal(h.out.detail, null); assert.equal(h.out.timeline, null); assert.deepEqual(h.out.steps, []); assert.equal(h.out.canAct, false); assert.equal(h.out.refresh, refresh);
    assert.equal(await refresh(), false); h.render(); await h.flush(); assert(old.every(c => c.options.signal.aborted));
    ok(old[0], detail()); ok(old[1], page({ items: [step({ file_name: 'private old actor' })] })); assert.equal(await work, false); await h.flush(); assert.equal(h.out.detail, null); assert.deepEqual(h.out.steps, []);
  } finally { await h.dispose(); }
});

test('A→B→A cannot revive old actor reads and incomplete scopes never issue GETs', async () => {
  const h = await host(); try {
    const old = h.calls.slice(); h.render({ userId: 'bob' }); await h.flush(); h.render({ userId: 'alice' }); await h.flush();
    pair(h); await h.flush(); ok(old[0], detail({ status: 'SECRET' })); ok(old[1], page({ items: [step()] })); await h.flush(); assert.equal(h.out.detail.status, 'tracking'); assert.deepEqual(h.out.steps, []);
  } finally { await h.dispose(); }
  for (const initial of [{ operationId: undefined }, { userId: undefined }, { role: undefined }]) { const empty = await host(initial); try { assert.equal(empty.calls.length, 0); assert.equal(empty.timers.size, 0); assert.equal(await empty.out.refresh(), false); } finally { await empty.dispose(); } }
});

for (const status of [401, 403, 404]) for (const lane of [0, 1]) test(`${status} from lane ${lane} immediately clears all private data and fences the other response`, async () => {
  const h = await host(); try {
    pair(h, detail(), page({ items: [step()] })); await h.flush(); const work = h.out.refresh(), calls = h.calls.slice(-2);
    calls[lane].reply({ response: { status }, error: { detail: 'SECRET' } }); await h.flush(); assert.equal(h.out.detail, null); assert.equal(h.out.timeline, null); assert.deepEqual(h.out.steps, []); assert.equal(h.out.canAct, false); assert(calls[1 - lane].options.signal.aborted);
    ok(calls[1 - lane], lane === 0 ? page({ items: [step()] }) : detail()); assert.equal(await work, false); await h.flush(); assert.equal(h.out.detail, null); assert(!h.out.error.includes('SECRET'));
  } finally { await h.dispose(); }
});

for (const lane of [0, 1]) for (const failure of ['503', 'network', 'malformed']) test(`${failure} lane ${lane} keeps same-scope data stale and only confirmed retry clears errors`, async () => {
  const h = await host(); try {
    pair(h, detail(), page({ items: [step()] })); await h.flush(); const previous = lane === 0 ? h.out.detail : h.out.timeline;
    const work = h.out.refresh(), calls = h.calls.slice(-2);
    if (failure === 'network') calls[lane].fail(); else calls[lane].reply(failure === '503' ? { response: { status: 503 } } : { data: {}, response: { status: 200 } });
    ok(calls[1 - lane], lane === 0 ? page({ items: [step()] }) : detail()); assert.equal(await work, false); await h.flush();
    assert.deepEqual(lane === 0 ? h.out.detail : h.out.timeline, previous); assert.equal(lane === 0 ? h.out.detailStale : h.out.timelineStale, true); assert.equal(h.out.canAct, false); assert.match(h.out.error + h.out.timelineError, /失败/); assert(!/SECRET/.test(h.out.error + h.out.timelineError));
    const retry = h.out.refresh(); await h.flush(); assert.match(h.out.error + h.out.timelineError, /失败/); pair(h); assert.equal(await retry, true); await h.flush(); assert.equal(h.out.error + h.out.timelineError, ''); assert.equal(h.out.canAct, true);
  } finally { await h.dispose(); }
});

test('version invalidation fences prior snapshot synchronously and supersedes busy reads exactly once', async () => {
  const h = await host(); try {
    pair(h); await h.flush(); const work = h.out.refresh(), old = h.calls.slice(-2);
    h.render({ refreshVersion: 1 }, false); assert.equal(h.out.canAct, false); assert.equal(h.out.detailStale, true); assert.equal(await h.out.refresh(), false);
    h.render(); await h.flush(); assert.equal(h.calls.length, 6); h.fire(); assert.equal(h.calls.length, 6); assert(old.every(c => c.options.signal.aborted));
    ok(old[0], detail()); ok(old[1], page({ items: [step()] })); assert.equal(await work, false); pair(h); await h.flush(); assert.deepEqual(h.out.steps, []); assert.equal(h.out.canAct, true);
  } finally { await h.dispose(); }
});

test('visible 5s polling deduplicates busy reads; hiding removes poll and showing confirms new reads', async () => {
  const h = await host(); try {
    h.fire(); assert.equal(h.calls.length, 2); pair(h); await h.flush();
    h.visibility('hidden'); assert.equal(h.timers.size, 0); h.fire(); assert.equal(h.calls.length, 2);
    h.visibility('visible'); assert.equal(h.calls.length, 4); assert.equal(h.timers.size, 1); pair(h); await h.flush(); h.fire(); assert.equal(h.calls.length, 6);
  } finally { await h.dispose(); }
});

test('background reads preserve confirmed action context while explicit refresh and failures invalidate it', async () => {
  const h = await host(); try {
    pair(h); await h.flush(); assert.equal(h.out.canAct, true);
    h.fire(); await h.flush(); assert.equal(h.out.loading, true); assert.equal(h.out.canAct, true);
    assert.equal(h.out.detailStale, false); assert.equal(h.out.timelineStale, false);
    pair(h); await h.flush(); assert.equal(h.out.canAct, true);
    const refresh = h.out.refresh(); await h.flush(); assert.equal(h.out.canAct, false);
    pair(h); assert.equal(await refresh, true); await h.flush(); assert.equal(h.out.canAct, true);
    h.fire(); h.calls.at(-2).fail(); h.calls.at(-1).fail(); await h.flush();
    assert.equal(h.out.canAct, false); assert.equal(h.out.detailStale, true);
  } finally { await h.dispose(); }
});

test('Strict Mode replay and unmount abort owned GETs and reject held responses ignoring abort', async () => {
  const h = await host(); try {
    const old = h.calls.slice(); h.replay(); assert(old.every(c => c.options.signal.aborted)); assert.equal(h.timers.size, 1); assert.equal(h.listeners.size, 1);
    ok(old[0], detail()); ok(old[1], page()); await h.flush(); assert.equal(h.out.detail, null);
    const work = h.out.refresh(), pending = h.calls.slice(-2); h.unmount(); const updates = h.updates;
    ok(pending[0], detail()); ok(pending[1], page({ items: [step()] })); assert.equal(await work, false); await tick(); assert.equal(h.updates, updates);
  } finally { await h.dispose(); }
});

test('bounded manual pagination deduplicates steps, keeps actual ascending order and never auto-pages', async () => {
  const h = await host(); try {
    pair(h, detail(), page({ items: [step({ order: 3 })], next_cursor: 'cursor-1' })); await h.flush(); assert.equal(h.calls.length, 2);
    const work = h.out.loadMore(), duplicate = h.out.loadMore(); assert.equal(h.calls.length, 3); assert.equal(h.calls[2].options.params.query.limit, 100); assert.equal(h.calls[2].options.params.query.cursor, 'cursor-1');
    ok(h.calls[2], page({ items: [step({ order: 3, state: 'succeeded' }), step({ step_id: 'earlier', order: 2 }), step({ step_id: 'later', order: 9 })], next_cursor: 'cursor-2' }));
    assert.equal(await work, true); assert.equal(await duplicate, true); await h.flush(); assert.deepEqual(h.out.steps.map(s => s.order), [2, 3, 9]); assert.equal(h.out.steps[1].state, 'succeeded'); assert.equal(h.calls.length, 3);
  } finally { await h.dispose(); }
});

test('page errors retain steps and retry cursor; changed plan response never appends', async () => {
  const h = await host(); try {
    pair(h, detail(), page({ items: [step()], next_cursor: 'cursor' })); await h.flush();
    let work = h.out.loadMore(); h.calls.at(-1).fail(); assert.equal(await work, false); await h.flush(); assert.equal(h.out.steps.length, 1); assert.equal(h.out.timeline.next_cursor, 'cursor'); assert.equal(h.out.timelineStale, true); assert.equal(h.out.canAct, false);
    work = h.out.loadMore(); ok(h.calls.at(-1), page({ plan_id: 'new-plan', items: [step({ step_id: 'new' })] })); assert.equal(await work, false); await h.flush(); assert.equal(h.out.steps.length, 1); assert.match(h.out.timelineError, /计划/);
    work = h.out.refresh(); pair(h, detail({ plan_id: 'new-plan' }), page({ plan_id: 'new-plan', items: [] })); assert.equal(await work, true); await h.flush(); assert.deepEqual(h.out.steps, []); assert.equal(h.out.timelineError, '');
  } finally { await h.dispose(); }
});

test('first-page refresh aborts pagination and late previous-plan page cannot replace the current plan', async () => {
  const h = await host(); try {
    pair(h, detail(), page({ items: [step()], next_cursor: 'cursor' })); await h.flush();
    const paging = h.out.loadMore(), old = h.calls.at(-1), refreshing = h.out.refresh(); assert(old.options.signal.aborted);
    pair(h, detail({ plan_id: 'replacement' }), page({ plan_id: 'replacement', items: [] })); assert.equal(await refreshing, true); await h.flush();
    ok(old, page({ items: [step({ step_id: 'late' })] })); assert.equal(await paging, false); await h.flush(); assert.deepEqual(h.out.steps, []); assert.equal(h.out.timeline.plan_id, 'replacement');
  } finally { await h.dispose(); }
});

test('detail/timeline plan mismatch, malformed page and foreign operation never unlock commands', async () => {
  const h = await host(); try {
    pair(h, detail(), page({ plan_id: 'other' })); await h.flush(); assert.equal(h.out.canAct, false); assert.equal(h.out.timelineStale, true);
    const work = h.out.refresh(); pair(h, detail({ operation_id: 'foreign' }), page({ items: [null] })); assert.equal(await work, false); await h.flush(); assert.equal(h.out.canAct, false); assert(h.out.error); assert(h.out.timelineError);
  } finally { await h.dispose(); }
});


test('slow pagination owns its read across poll ticks and a confirmed page retry clears page-only failure', async () => {
  const h = await host(); try {
    pair(h, detail(), page({ items: [step()], next_cursor: 'cursor' })); await h.flush();
    let work = h.out.loadMore(); h.fire(); assert.equal(h.calls.length, 3); assert(!h.calls.at(-1).options.signal.aborted);
    h.calls.at(-1).fail(); assert.equal(await work, false); await h.flush(); assert.equal(h.out.timelineStale, true);
    work = h.out.loadMore(); ok(h.calls.at(-1), page({ items: [step({ step_id: 'second', order: 2 })] })); assert.equal(await work, true); await h.flush();
    assert.equal(h.out.timelineError, ''); assert.equal(h.out.timelineStale, false); assert.equal(h.out.canAct, true); assert.equal(h.out.steps.length, 2);
  } finally { await h.dispose(); }
});

for (const status of [401, 403, 404]) test(`pagination ${status} also clears both private read lanes`, async () => {
  const h = await host(); try {
    pair(h, detail(), page({ items: [step()], next_cursor: 'cursor' })); await h.flush(); const work = h.out.loadMore();
    h.calls.at(-1).reply({ response: { status } }); assert.equal(await work, false); await h.flush();
    assert.equal(h.out.detail, null); assert.equal(h.out.timeline, null); assert.deepEqual(h.out.steps, []); assert.equal(h.out.canAct, false);
  } finally { await h.dispose(); }
});

test('pagination cannot confirm a failed first-page refresh; only actual fresh paired reads unlock it', async () => {
  const h = await host(); try {
    pair(h, detail(), page({ items: [step()], next_cursor: 'cursor' })); await h.flush(); const work = h.out.refresh();
    ok(h.calls.at(-2), detail()); h.calls.at(-1).fail(); assert.equal(await work, false); await h.flush(); const count = h.calls.length;
    assert.equal(await h.out.loadMore(), false); assert.equal(h.calls.length, count); assert.equal(h.out.steps.length, 1);
  } finally { await h.dispose(); }
});
