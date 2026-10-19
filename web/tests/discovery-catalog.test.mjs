import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import ts from 'typescript';

const root = new URL('../src/features/configuration/', import.meta.url);
const compile = text => ts.transpileModule(text, { compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.ESNext } }).outputText;
const load = text => import(`data:text/javascript;base64,${Buffer.from(compile(text)).toString('base64')}#${crypto.randomUUID()}`);
const catalog = await load(readFileSync(new URL('discoveryCatalog.ts', root), 'utf8'));
const item = (id = 1, patch = {}) => ({ tmdb_id: id, title: `Title ${id}`, media_type: 'movie', ...patch });
const tick = async () => { for (let i = 0; i < 12; i++) await Promise.resolve(); };

// Same injected slot/effect approach as subscription-creation.test.mjs; execute the actual hook.
async function hookHost(initial = {}) {
  const slots = [], effects = [], calls = [];
  let cursor = 0, updates = 0, out;
  let props = { category: 'trending', locationKey: 'first', userId: 'alice', ...initial };
  const equal = (a, b) => a && b && a.length === b.length && a.every((v, i) => Object.is(v, b[i]));
  const memo = (fn, deps) => {
    const i = cursor++, old = slots[i];
    if (!old || !equal(old.deps, deps)) slots[i] = { deps, value: fn() };
    return slots[i].value;
  };
  const host = { ...catalog,
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
      const call = { path, options, settled: false };
      calls.push(call);
      return new Promise((resolve, reject) => {
        call.reply = value => { assert(!call.settled); call.settled = true; resolve(value); };
        call.fail = () => { assert(!call.settled); call.settled = true; reject(Error('offline / secret credentials')); };
      });
    } },
  };
  const name = `__discovery_test_${crypto.randomUUID().replaceAll('-', '')}`;
  globalThis[name] = host;
  let useDiscoveryCatalog;
  try {
    const source = readFileSync(new URL('useDiscoveryCatalog.ts', root), 'utf8').replace(/^import[\s\S]*?;\n/gm, '');
    ({ useDiscoveryCatalog } = await load(`const {${Object.keys(host).join(',')}}=globalThis[${JSON.stringify(name)}];\n${source}`));
  } finally { delete globalThis[name]; }
  const render = (patch = {}, runEffects = true) => {
    props = { ...props, ...patch }; cursor = 0; out = useDiscoveryCatalog(props);
    if (runEffects) for (const effect of effects.splice(0)) effect();
    return out;
  };
  const flush = async () => { await tick(); return render(); };
  render(); await flush();
  return { calls, render, flush, get out() { return out; }, get updates() { return updates; },
    async dispose() {
      for (const slot of slots) slot?.cleanup?.();
      for (const call of calls) if (!call.settled) call.fail();
      await tick();
    },
    replayEffects() { for (const slot of slots) if (slot?.fn) { slot.cleanup?.(); slot.cleanup = slot.fn(); } },
  };
}
const replyItems = (call, results = []) => call.reply({ data: { results } });

const match = catalog.matchesDiscoveryGenre;
test('genre matching uses only complete trusted labels and known TMDB compounds', () => {
  const misleading = item(1, { title: '科幻战争', overview: '动作冒险悬疑犯罪' });
  assert.equal(match(misleading, '全部'), true);
  assert.equal(match(misleading, 'All'), true);
  for (const genre of ['科幻', '战争', '动作', '悬疑', '']) assert.equal(match(misleading, genre), false);
  assert.equal(match(item(1, { genres: ['剧情'] }), '剧情'), true);
  for (const [label, genres] of Object.entries({ '动作冒险': ['动作', '冒险'], '动作与冒险': ['动作', '冒险'], '动作和冒险': ['动作', '冒险'], '科幻与奇幻': ['科幻', '奇幻'], '科幻和奇幻': ['科幻', '奇幻'], '战争与政治': ['战争', '政治'], '战争和政治': ['战争', '政治'] })) {
    for (const genre of genres) assert.equal(match(item(1, { genres: [label] }), genre), true, `${label}/${genre}`);
  }
  for (const genre of ['科', '动作片', '犯罪', 'constructor']) assert.equal(match(item(1, { genres: ['科幻与奇幻', '动作', '犯罪心理', 'constructor'] }), genre), genre === 'constructor');
});

test('actual hook exposes frozen contract, auto-loads page one and preserves MediaItem fields', async () => {
  const h = await hookHost(); try {
    assert.deepEqual(Object.keys(h.out).sort(), ['feedItems', 'movies', 'loadingFeed', 'searching', 'searched', 'feedError', 'searchError', 'search', 'refreshFeed', 'resetSearch'].sort());
    assert.equal(h.calls.length, 1); assert.equal(h.calls[0].path, '/api/v1/discover');
    assert.deepEqual(h.calls[0].options.params.query, { category: 'trending', page: 1 });
    assert.equal(h.out.loadingFeed, true);
    replyItems(h.calls[0], [item(7, { year: null, overview: null, poster_url: null, backdrop_url: '/b', rating: 0, genres: ['剧情'] })]);
    await h.flush(); assert.equal(h.out.loadingFeed, false); assert.equal(h.out.feedError, '');
    assert.deepEqual(h.out.feedItems, [catalog.discoveryMediaItem(item(7, { year: null, overview: null, poster_url: null, backdrop_url: '/b', rating: 0, genres: ['剧情'] }))]);
    const stableSearch = h.out.search; h.render(); assert.equal(h.out.search, stableSearch); assert.equal(h.calls.length, 1);
  } finally { await h.dispose(); }
});

test('same-category refresh preserves items; superseded feed success and failure cannot clear current busy', async () => {
  const h = await hookHost(); try {
    replyItems(h.calls[0], [item()]); await h.flush();
    const old = h.out.refreshFeed(), oldCall = h.calls.at(-1);
    await h.flush(); assert.equal(h.out.feedItems[0].tmdb_id, 1); assert.equal(h.out.loadingFeed, true);
    const current = h.out.refreshFeed(), currentCall = h.calls.at(-1);
    assert.equal(oldCall.options.signal.aborted, true); oldCall.fail(); assert.equal(await old, false);
    await h.flush(); assert.equal(h.out.loadingFeed, true); assert.equal(h.out.feedError, '');
    replyItems(currentCall, [item(2)]); assert.equal(await current, true); await h.flush();
    assert.equal(h.out.feedItems[0].tmdb_id, 2);
    const a = h.out.refreshFeed(), ca = h.calls.at(-1), b = h.out.refreshFeed(), cb = h.calls.at(-1);
    replyItems(ca, [item(99)]); assert.equal(await a, false); await h.flush(); assert.equal(h.out.loadingFeed, true);
    replyItems(cb); assert.equal(await b, true); await h.flush(); assert.deepEqual(h.out.feedItems, []);
  } finally { await h.dispose(); }
});

for (const patch of [{ category: 'tv' }, { locationKey: 'second' }, { userId: 'bob' }]) test(`scope switch ${JSON.stringify(patch)} masks immediately, aborts both lanes and rejects late responses`, async () => {
  const h = await hookHost(); try {
    replyItems(h.calls[0], [item(1)]); await h.flush();
    const oldFeed = h.out.refreshFeed(), feedCall = h.calls.at(-1);
    const oldSearch = h.out.search('old', 'movie', ''), searchCall = h.calls.at(-1), oldMethod = h.out.search;
    h.render(patch, false);
    assert.deepEqual(h.out.feedItems, []); assert.deepEqual(h.out.movies, []); assert.equal(h.out.searched, false);
    // Even before passive cleanup, old closures cannot issue a request into a new scope.
    const count = h.calls.length; assert.equal(await oldMethod('stale', 'all', ''), false); assert.equal(h.calls.length, count);
    await h.flush(); assert.equal(feedCall.options.signal.aborted, true); assert.equal(searchCall.options.signal.aborted, true);
    const newFeed = h.calls.at(-1), next = h.out.search('new', 'tv', '2020'), nextCall = h.calls.at(-1);
    replyItems(feedCall, [item(99)]); searchCall.reply({ error: { message: 'stale failure' } });
    assert.equal(await oldFeed, false); assert.equal(await oldSearch, false); await h.flush();
    assert.equal(h.out.loadingFeed, true); assert.equal(h.out.searching, true); assert.equal(h.out.searchError, '');
    replyItems(newFeed, [item(2, { media_type: 'tv' })]); replyItems(nextCall, [item(3, { media_type: 'tv' })]);
    assert.equal(await next, true); await h.flush(); assert.equal(h.out.feedItems[0].tmdb_id, 2); assert.equal(h.out.movies[0].tmdb_id, 3);
  } finally { await h.dispose(); }
});

test('explicit search snapshots query/type/year, clears old results and ignores out-of-order success or rejection', async () => {
  const h = await hookHost(); try {
    replyItems(h.calls[0]); await h.flush();
    let query = '  Original  ', type = 'tv', year = '2001';
    const first = h.out.search(query, type, year), c1 = h.calls.at(-1);
    query = 'Changed'; type = 'movie'; year = '';
    assert.deepEqual(c1.options.params.query, { query: 'Original', type: 'tv', year: 2001 });
    const second = h.out.search(query, type, year), c2 = h.calls.at(-1);
    await h.flush(); assert.equal(h.out.searching, true); assert.equal(h.out.searched, true); assert.deepEqual(h.out.movies, []);
    replyItems(c1, [item(99)]); assert.equal(await first, false); await h.flush(); assert.equal(h.out.searching, true);
    replyItems(c2, [item(2)]); assert.equal(await second, true); await h.flush(); assert.equal(h.out.movies[0].tmdb_id, 2);
    const third = h.out.search('third', 'all', ''), c3 = h.calls.at(-1);
    await h.flush(); assert.deepEqual(h.out.movies, []);
    const fourth = h.out.search('fourth', 'all', ''), c4 = h.calls.at(-1);
    c3.fail(); assert.equal(await third, false); await h.flush(); assert.equal(h.out.searching, true); assert.equal(h.out.searchError, '');
    replyItems(c4); assert.equal(await fourth, true); await h.flush(); assert.equal(h.out.searched, true); assert.equal(h.out.searchError, ''); assert.deepEqual(h.out.movies, []);
  } finally { await h.dispose(); }
});

test('reset aborts search, fences late success and leaves feed untouched', async () => {
  const h = await hookHost(); try {
    replyItems(h.calls[0], [item(5)]); await h.flush();
    const first = h.out.search('before reset', 'all', ''), c1 = h.calls.at(-1);
    h.out.resetSearch(); await h.flush();
    assert.equal(c1.options.signal.aborted, true); assert.equal(h.out.searched, false); assert.equal(h.out.searching, false); assert.equal(h.out.feedItems[0].tmdb_id, 5);
    const second = h.out.search('after reset', 'tv', ''), c2 = h.calls.at(-1);
    replyItems(c1, [item(99)]); assert.equal(await first, false); await h.flush(); assert.equal(h.out.searching, true);
    replyItems(c2); assert.equal(await second, true); await h.flush(); assert.deepEqual(h.out.movies, []);
    h.out.resetSearch(); await h.flush(); assert.equal(h.out.searched, false); assert.equal(h.out.searchError, '');
  } finally { await h.dispose(); }
});

test('strict year, empty/oversized query and invalid type never reach network and invalidate older search', async () => {
  const h = await hookHost(); try {
    replyItems(h.calls[0]); await h.flush();
    const old = h.out.search('old', 'all', ''), oldCall = h.calls.at(-1), count = h.calls.length;
    for (const year of ['1799', '10000', '2020tail', '2020.0', '2e3', '+2020', '-2020', ' 2020', '2020 ', '２０２０', '0000', '02020', '2020\n']) {
      assert.equal(await h.out.search('valid', 'all', year), false, year);
      await h.flush(); assert.equal(h.out.searching, false); assert(h.out.searchError); assert.equal(h.calls.length, count);
    }
    for (const [query, type] of [['  ', 'all'], ['x'.repeat(257), 'movie'], ['ok', 'multi']]) {
      assert.equal(await h.out.search(query, type, ''), false); assert.equal(h.calls.length, count);
    }
    assert.equal(oldCall.options.signal.aborted, true); replyItems(oldCall, [item(99)]); assert.equal(await old, false);
    for (const year of ['', '1800', '9999']) {
      const work = h.out.search('  合法  ', 'tv', year), call = h.calls.at(-1);
      assert.deepEqual(call.options.params.query, { query: '合法', type: 'tv', ...(year ? { year: Number(year) } : {}) });
      replyItems(call); assert.equal(await work, true); await h.flush(); assert.equal(h.out.searchError, '');
    }
  } finally { await h.dispose(); }
});

for (const lane of ['feed', 'search']) test(`${lane} distinguishes empty success, API/missing/malformed failures and connection rejection`, async () => {
  const h = await hookHost(); try {
    replyItems(h.calls[0]); await h.flush();
    const start = () => lane === 'feed' ? h.out.refreshFeed() : h.out.search('query', 'all', '');
    const errorKey = lane === 'feed' ? 'feedError' : 'searchError', busyKey = lane === 'feed' ? 'loadingFeed' : 'searching';
    for (const response of [{ error: { message: 'private provider URL' } }, {}, { data: { results: null } }, { data: {} }]) {
      const work = start(); h.calls.at(-1).reply(response); assert.equal(await work, false); await h.flush();
      assert(h.out[errorKey]); assert(!/private/.test(h.out[errorKey])); assert.equal(h.out[busyKey], false);
    }
    const failed = start(); h.calls.at(-1).fail(); assert.equal(await failed, false); await h.flush();
    assert.match(h.out[errorKey], /连接失败/); assert.equal(h.out[busyKey], false);
    const empty = start(); await h.flush(); assert.equal(h.out[errorKey], ''); replyItems(h.calls.at(-1));
    assert.equal(await empty, true); await h.flush(); assert.equal(h.out[errorKey], '');
  } finally { await h.dispose(); }
});

test('logout suppresses requests and masks data; login and ABA scope changes cannot revive old work', async () => {
  const h = await hookHost(); try {
    const oldFeed = h.calls[0], old = h.out.search('old', 'all', ''), oldSearch = h.calls.at(-1);
    h.render({ userId: undefined }); await h.flush();
    const count = h.calls.length; assert.equal(await h.out.search('private', 'all', ''), false); assert.equal(await h.out.refreshFeed(), false); assert.equal(h.calls.length, count);
    assert.deepEqual(h.out.feedItems, []); assert.equal(h.out.searched, false);
    h.render({ userId: 'alice' }); await h.flush(); const newFeed = h.calls.at(-1);
    replyItems(oldFeed, [item(99)]); oldSearch.fail(); assert.equal(await old, false); await h.flush();
    assert.deepEqual(h.out.feedItems, []); assert.equal(h.out.loadingFeed, true); assert.equal(h.out.searchError, '');
    replyItems(newFeed, [item(2)]); await h.flush(); assert.equal(h.out.feedItems[0].tmdb_id, 2);
  } finally { await h.dispose(); }
  const loggedOut = await hookHost({ userId: undefined }); try { assert.equal(loggedOut.calls.length, 0); } finally { await loggedOut.dispose(); }
});

test('Strict Mode effect replay aborts original feed; unmount aborts both requests and permits no late state writes', async () => {
  const h = await hookHost();
  const original = h.calls[0]; h.replayEffects(); assert.equal(original.options.signal.aborted, true);
  const feed = h.calls.at(-1); replyItems(original, [item(99)]); await h.flush(); assert.equal(h.out.loadingFeed, true);
  const search = h.out.search('pending', 'tv', ''), call = h.calls.at(-1), methods = h.out;
  await h.dispose(); assert.equal(feed.options.signal.aborted, true); assert.equal(call.options.signal.aborted, true);
  assert.equal(await search, false); const updates = h.updates;
  assert.equal(await methods.search('late', 'all', ''), false); assert.equal(await methods.refreshFeed(), false); methods.resetSearch(); await tick();
  assert.equal(h.updates, updates);
});
