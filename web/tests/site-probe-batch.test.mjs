import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import ts from 'typescript';

const source = readFileSync(new URL('../src/features/configuration/siteProbeBatch.ts', import.meta.url), 'utf8');
const compiled = ts.transpileModule(source, { compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.ESNext } }).outputText;
const { createSiteProbeBatch } = await import(`data:text/javascript;base64,${Buffer.from(compiled).toString('base64')}`);
const site = (id, configVersion = 1) => ({ id, configVersion, name: `Site ${id}` });
const good = { status: 200, alive: true, latencyMs: 12 };
const deferred = () => {
  let resolve, reject;
  const promise = new Promise((yes, no) => { resolve = yes; reject = no; });
  return { promise, resolve, reject };
};
const tick = async () => { for (let i = 0; i < 8; i++) await Promise.resolve(); };
function harness(overrides = {}) {
  const h = { sites: [site('a'), site('b'), site('c')], allowed: true, changes: [], calls: [], refreshes: [], time: 1234 };
  h.batch = createSiteProbeBatch({
    getSites: () => h.sites,
    canRun: () => h.allowed,
    now: () => h.time,
    onChange: state => h.changes.push(state),
    probe: (target, signal) => {
      const pending = deferred();
      h.calls.push({ target, signal, ...pending });
      return pending.promise; // Deliberately ignores abort: physical completion is independent.
    },
    refresh: () => {
      const pending = deferred();
      h.refreshes.push(pending);
      return pending.promise;
    },
    ...overrides,
  });
  h.state = () => h.changes.at(-1);
  h.states = () => h.state().entries.map(entry => entry.state);
  return h;
}
async function finish(h, index, reply = good) {
  h.calls[index].resolve(reply);
  await tick();
  h.refreshes.at(-1).resolve();
  await tick();
}

test('deduplicates frozen requested IDs; sequential transport and refresh; overlapping start ignored', async () => {
  const h = harness();
  const ids = ['b', 'missing', 'b', 'a'];
  const run = h.batch.start(ids);
  assert.deepEqual(h.changes[0].entries.map(e => e.state), ['queued', 'queued']);
  assert.deepEqual(h.states(), ['running', 'queued']);
  ids.push('c');
  await h.batch.start(['c']);
  assert.equal(h.calls.length, 1);
  assert.equal(h.calls[0].target.id, 'b');
  h.calls[0].resolve(good); await tick();
  assert.equal(h.state().running, true);
  assert.deepEqual(h.states(), ['online', 'queued']);
  assert.equal(h.calls.length, 1);
  await h.batch.start(['c']);
  h.refreshes[0].resolve(); await tick();
  assert.equal(h.calls.length, 2);
  assert.equal(h.calls[1].target.id, 'a');
  await finish(h, 1, { status: 204, alive: false, latencyMs: 0 }); await run;
  assert.deepEqual(h.states(), ['online', 'offline']);
  assert.deepEqual(h.state().entries.map(e => [e.latencyMs, e.observedAt]), [[12, 1234], [0, 1234]]);
  assert.equal(h.state().running, false);
});

for (const phase of ['request', 'refresh']) test(`stopQueued during ${phase} retains actual pending slot`, async () => {
  const h = harness();
  let done = false;
  const run = h.batch.start(['a', 'b', 'c']).then(() => { done = true; });
  if (phase === 'refresh') { h.calls[0].resolve(good); await tick(); }
  h.batch.stopQueued();
  assert.deepEqual(h.states().slice(1), ['cancelled', 'cancelled']);
  assert.equal(h.state().stopping, true);
  assert.equal(h.state().running, true);
  assert.equal(h.calls[0].signal.aborted, false);
  await h.batch.start(['c']);
  assert.equal(h.calls.length, 1);
  if (phase === 'request') { h.calls[0].resolve(good); await tick(); }
  assert.equal(done, false);
  assert.equal(h.state().running, true);
  assert.equal(h.state().stopping, true);
  h.refreshes[0].resolve(); await run;
  assert.equal(done, true);
  assert.equal(h.calls.length, 1);
  assert.equal(h.state().running, false);
  assert.equal(h.state().stopping, false);
});

for (const change of ['replace', 'delete']) test(`stale ${change} before dispatch and after refresh suppresses old health`, async () => {
  const h = harness();
  const run = h.batch.start(['a', 'b', 'c']);
  h.sites = change === 'replace' ? [site('a', 2), site('b', 2), site('c')] : [site('c')];
  h.calls[0].resolve(good); await tick();
  assert.equal(h.states()[0], 'stale');
  assert.equal(h.state().entries[0].latencyMs, null);
  h.refreshes[0].resolve(); await tick();
  assert.deepEqual(h.states(), ['stale', 'stale', 'running']);
  assert.equal(h.calls.length, 2);
  assert.equal(h.calls[1].target.id, 'c');
  h.calls[1].resolve(good); await tick();
  assert.equal(h.states()[2], 'online');
  h.sites = []; h.refreshes[1].resolve(); await run;
  assert.deepEqual(h.states(), ['stale', 'stale', 'stale']);
  assert(h.state().entries.every(e => e.latencyMs === null && e.observedAt === null));
});

test('later list reread invalidates an earlier completed observation', async () => {
  const h = harness(); const run = h.batch.start(['a', 'b']);
  await finish(h, 0);
  h.sites = [site('b')];
  await finish(h, 1); await run;
  assert.deepEqual(h.states(), ['stale', 'online']);
});

for (const phase of ['request', 'refresh']) test(`dispose during ${phase}: late ignored-abort settlement cannot publish or send`, async () => {
  const h = harness(); const run = h.batch.start(['a', 'b']);
  if (phase === 'refresh') { h.calls[0].resolve(good); await tick(); }
  h.batch.dispose(); const count = h.changes.length;
  assert.equal(h.calls[0].signal.aborted, true);
  await h.batch.start(['c']);
  if (phase === 'request') h.calls[0].resolve(good);
  else h.refreshes[0].resolve();
  await run;
  assert.equal(h.changes.length, count);
  assert.equal(h.calls.length, 1);
  assert.equal(h.refreshes.length, phase === 'request' ? 0 : 1);
});

for (const status of [401, 403, 400, 404, 409, 429, 500, 503]) test(`HTTP ${status}: rejected, never offline, auth alone stops queue`, async () => {
  const h = harness(); const run = h.batch.start(['a', 'b']);
  await finish(h, 0, { status, alive: false, message: 'SECRET remote error' });
  if (status !== 401 && status !== 403) await finish(h, 1);
  await run;
  assert.equal(h.states()[0], 'rejected');
  assert.equal(h.state().entries[0].status, status);
  assert.equal(h.state().entries[0].observedAt, null);
  assert.equal(h.calls.length, status === 401 || status === 403 ? 1 : 2);
  assert(!JSON.stringify(h.changes).includes('SECRET'));
});

for (const reply of [undefined, null, 'malformed bytes', {}, { status: '200', alive: true }, { status: 600 }, { status: NaN }, { status: 200 }, { status: 200, alive: 'false' }, { status: 200, alive: 1 }, { status: 200, alive: null }]) {
  test(`malformed reply ${JSON.stringify(reply)} is unknown with no replay`, async () => {
    const h = harness(); const run = h.batch.start(['a', 'b']);
    h.calls[0].resolve(reply); await tick();
    h.refreshes[0].resolve(); await run;
    assert.deepEqual(h.states(), ['unknown', 'cancelled']);
    assert.equal(h.calls.length, 1);
    assert.equal(h.state().entries[0].latencyMs, null);
  });
}

for (const latencyMs of [undefined, null, -1, 1.5, Infinity, NaN, Number.MAX_SAFE_INTEGER + 1, '8']) {
  test(`invalid/absent latency ${String(latencyMs)} preserves honest boolean health`, async () => {
    const h = harness(); const run = h.batch.start(['a', 'b']);
    await finish(h, 0, { status: 200, alive: true, latencyMs });
    await finish(h, 1, { status: 200, alive: false, latencyMs }); await run;
    assert.deepEqual(h.states(), ['online', 'offline']);
    assert(h.state().entries.every(e => e.latencyMs === null && e.observedAt === 1234));
  });
}

for (const time of [-1, Infinity, NaN]) test(`invalid display timestamp ${time} gives unknown`, async () => {
  const h = harness(); h.time = time;
  const run = h.batch.start(['a', 'b']); await finish(h, 0); await run;
  assert.deepEqual(h.states(), ['unknown', 'cancelled']);
  assert.equal(h.state().entries[0].observedAt, null);
});

test('snapshot, source target and transport mutation cannot rewrite frozen fences; only explicit new batch resets', async () => {
  const h = harness(); const run = h.batch.start(['a', 'b']);
  const queued = h.changes[0];
  queued.entries[1].target.configVersion = 99;
  queued.entries[1].state = 'cancelled'; queued.entries.push({ target: site('injected') });
  h.calls[0].target.id = 'transport mutation';
  h.sites[0].name = 'renamed';
  await finish(h, 0); await finish(h, 1); await run;
  assert.deepEqual(h.state().entries.map(e => e.target), [site('a'), site('b')]);
  const completed = h.state(); completed.entries[0].target.id = 'snapshot mutation';
  await tick(); assert.equal(h.calls.length, 2);
  const next = h.batch.start(['c']);
  assert.deepEqual(h.state().entries.map(e => e.target.id), ['c']);
  await finish(h, 2); await next;
});

test('queued callback mutation of live config is checked before any IO', async () => {
  let sites = [site('a')]; const changes = []; let calls = 0;
  const batch = createSiteProbeBatch({ getSites: () => sites, canRun: () => true, now: () => 1,
    probe: async () => { calls++; return good; }, refresh: async () => {},
    onChange: state => { changes.push(state); sites = [site('a', 2)]; },
  });
  await batch.start(['a']); assert.equal(calls, 0);
  assert.equal(changes.at(-1).entries[0].state, 'stale');
});

test('lost response never retries and holds slot through failed refresh', async () => {
  const h = harness(); const run = h.batch.start(['a', 'b']);
  h.calls[0].reject(Error('lost response SECRET')); await tick();
  assert.deepEqual(h.states(), ['unknown', 'cancelled']);
  assert.equal(h.state().running, true);
  await h.batch.start(['a']); assert.equal(h.calls.length, 1);
  h.refreshes[0].reject(Error('refresh failed')); await run;
  assert.deepEqual(h.states(), ['unknown', 'cancelled']);
  assert(!JSON.stringify(h.changes).includes('SECRET'));
});

for (const status of [200, 401, 403, 503]) test(`refresh fault after ${status} stops queue and preserves only auth rejection`, async () => {
  const h = harness(); const run = h.batch.start(['a', 'b']);
  h.calls[0].resolve({ ...good, status }); await tick();
  h.refreshes[0].reject(Error('list unavailable')); await run;
  assert.deepEqual(h.states(), [status === 401 || status === 403 ? 'rejected' : 'unknown', 'cancelled']);
  assert.equal(h.calls.length, 1);
});

for (const phase of ['initial', 'request', 'refresh']) test(`canRun false at ${phase} prevents further IO and clears old health`, async () => {
  const h = harness();
  if (phase === 'initial') h.allowed = false;
  const run = h.batch.start(['a', 'b']);
  if (phase === 'initial') { await run; assert.equal(h.calls.length, 0); assert.equal(h.changes.length, 0); return; }
  if (phase === 'refresh') { h.calls[0].resolve(good); await tick(); }
  h.allowed = false; h.sites = [];
  if (phase === 'request') h.calls[0].resolve(good);
  else h.refreshes[0].resolve();
  await run;
  assert.deepEqual(h.states(), ['unknown', 'cancelled']);
  assert.equal(h.calls.length, 1);
  assert.equal(h.refreshes.length, phase === 'request' ? 0 : 1);
  assert.equal(h.state().entries[0].latencyMs, null);
});

test('empty selection emits completed empty batch and does no IO', async () => {
  const h = harness(); await h.batch.start(['missing']);
  assert.deepEqual(h.state(), { running: false, stopping: false, entries: [] });
  assert.equal(h.calls.length + h.refreshes.length, 0);
});
