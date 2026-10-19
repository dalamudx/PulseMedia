import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import ts from 'typescript';

const root = new URL('../src/features/configuration/', import.meta.url);
const compile = source => ts.transpileModule(source, { compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.ESNext, jsx: ts.JsxEmit.React } }).outputText;
const load = source => import(`data:text/javascript;base64,${Buffer.from(compile(source)).toString('base64')}#${crypto.randomUUID()}`);
const stripImports = source => source.replace(/^import[\s\S]*?;\n/gm, '');
const selection = await load(stripImports(readFileSync(new URL('mediaDetailSelection.ts', root), 'utf8')));
const tick = async () => { for (let i = 0; i < 20; i++) await Promise.resolve(); };
const user = { id: 'actor-a', role: 'admin' };
const media = { tmdb_id: 42, media_type: 'tv', title: 'Card title' };
const summary = (sn, count = 2) => ({ season_number: sn, episode_count: count, name: sn === 0 ? 'Specials' : `Season ${sn}` });
const tv = (id = 42, items = [summary(0), summary(1), summary(3)]) => ({ tmdb_id: id, title: 'TV actual', seasons_count: items.filter(s => s.season_number > 0).length, episodes_count: items.filter(s => s.season_number > 0).reduce((n, s) => n + s.episode_count, 0), seasons: items });
const movie = (id = 42) => ({ tmdb_id: id, title: 'Movie actual', runtime: 120 });
const list = (sn = 1, numbers = [1, 3], id = 42) => ({ tmdb_id: id, season_number: sn, complete: true, episodes: numbers.map(n => ({ tmdb_id: sn * 1000 + n + 100, episode_number: n })) });
const cast = (id = 42, type = 'tv', members = []) => ({ tmdb_id: id, media_type: type, cast: members });

async function harness(initialMedia = media, component = false) {
  let auth = user, input = initialMedia, disabled = false, cursor = 0, out, tree, mounted = true, afterUnmount = 0, slots = [], pendingEffects = new Map();
  const calls = [], creations = [], resources = [], closes = [], focus = [];
  const depsEqual = (a, b) => a && b && a.length === b.length && a.every((v, i) => Object.is(v, b[i]));
  const host = { ...selection,
    useAuth: () => ({ user: auth }),
    useRef(value) { const i = cursor++; return slots[i] ?? (slots[i] = { current: value }); },
    useState(initial) { const i = cursor++; if (!slots[i]) slots[i] = { value: typeof initial === 'function' ? initial() : initial }; const slot = slots[i]; return [slot.value, value => { if (!mounted) { afterUnmount++; return; } slot.value = typeof value === 'function' ? value(slot.value) : value; }]; },
    useEffect(fn, deps) { const i = cursor++, old = slots[i]; if (!old || !depsEqual(old.deps, deps)) pendingEffects.set(i, { fn, deps }); },
    useId() { const i = cursor++; return `detail-${i}`; },
    useDialogFocus(ref, close) { focus.push({ ref, close }); },
    api: { GET(path, options) { const call = { path, options, settled: false }; calls.push(call); return new Promise((resolve, reject) => { call.reply = (data, status = 200) => { call.settled = true; resolve({ data, response: { status, ok: status >= 200 && status < 300 } }); }; call.fail = () => { call.settled = true; reject(Error('transport failed')); }; }); } },
    React: { createElement(type, props, ...children) { return { type, props: { ...props, children: children.flat(Infinity) } }; } },
    X: 'icon', Rss: 'icon', Search: 'icon', Star: 'icon',
    RootSelector: 'RootSelector', PolicySelector: 'PolicySelector', EndpointBindingFields: 'EndpointBindingFields',
  };
  async function module(source) {
    const key = `__media_detail_${crypto.randomUUID().replaceAll('-', '')}`;
    globalThis[key] = host;
    try { return await load(`const {${Object.keys(host).join(',')}} = globalThis[${JSON.stringify(key)}];\n${stripImports(source)}`); }
    finally { delete globalThis[key]; }
  }
  const hookModule = await module(readFileSync(new URL('useMediaDetail.ts', root), 'utf8'));
  host.useMediaDetail = hookModule.useMediaDetail;
  const modalModule = component ? await module(readFileSync(new URL('../src/components/dialog/MediaDetailModal.tsx', import.meta.url), 'utf8')) : null;
  let componentKey;
  const props = () => ({ media: input, subscribing: disabled, onClose: () => closes.push(true), onSubscribe: (...args) => creations.push(args), onSearchReleases: m => resources.push(m) });
  function cleanup() { for (const slot of slots) slot?.cleanup?.(); }
  function commit() {
    const effects = [...pendingEffects]; pendingEffects.clear();
    for (const [i, effect] of effects) { slots[i]?.cleanup?.(); slots[i] = { deps: effect.deps, fn: effect.fn, cleanup: effect.fn() }; }
  }
  function render(effects = true) {
    cursor = 0; pendingEffects.clear();
    if (component) {
      const node = modalModule.MediaDetailModal(props());
      if (!node) { tree = null; return null; }
      if (componentKey !== node.props.key) { cleanup(); slots = []; componentKey = node.props.key; }
      tree = node.type(node.props);
    } else out = hookModule.useMediaDetail(input, disabled);
    if (effects) commit();
    return component ? tree : out;
  }
  const flush = async () => { for (let i = 0; i < 4; i++) { await tick(); render(); } return component ? tree : out; };
  const nodes = (node = tree) => !node || typeof node !== 'object' ? [] : [node, ...(node.props?.children ?? []).filter(n => n !== undefined).flatMap(n => nodes(n))];
  const text = (node = tree) => typeof node === 'string' || typeof node === 'number' ? String(node) : node && typeof node === 'object' ? (node.props?.children ?? []).filter(n => n !== undefined).map(n => text(n)).join('') : '';
  const button = label => { const n = nodes().find(n => n.type === 'button' && (text(n).includes(label) || n.props['aria-label'] === label)); assert(n, `missing button ${label}`); return n; };
  render();
  return { calls, creations, resources, closes, focus, render, flush, commit, nodes, text, button,
    get out() { return out; },
    image(node) {
      const savedSlots = slots, savedCursor = cursor; slots = []; cursor = 0;
      try { const before = node.type(node.props); before.props.onError?.(); cursor = 0; return { before, afterFailure: node.type(node.props) }; }
      finally { slots = savedSlots; cursor = savedCursor; }
    },
    auth(value, effects = true) { auth = value; return render(effects); },
    media(value, effects = true) { input = value; return render(effects); },
    lock(value = true) { disabled = value; return render(); },
    reply(index, data, status = 200) { calls[index].reply(data, status); },
    fail(index) { calls[index].fail(); },
    async ready() { this.reply(0, initialMedia?.media_type === 'tv' ? tv(initialMedia.tmdb_id) : movie(initialMedia.tmdb_id)); this.reply(1, cast(initialMedia.tmdb_id, initialMedia?.media_type ?? 'movie')); await flush(); },
    replay() { cleanup(); for (const slot of slots) if (slot?.fn) slot.cleanup = slot.fn(); },
    unmount() { cleanup(); mounted = false; },
    async dispose() { if (mounted) this.unmount(); for (const call of calls) if (!call.settled) call.fail(); await tick(); assert.equal(afterUnmount, 0, 'no setters after unmount'); },
  };
}

function choose(h, sn = 1) { h.out.setMode('selected'); h.out.toggleSeason(sn); h.render(); }
async function loaded(h, sn = 1, numbers = [1, 3]) { const call = h.calls.findLast(c => c.path.endsWith('/seasons/{season}') && c.options.params.path.season === sn && !c.settled); assert(call, `season ${sn} requested`); call.reply(list(sn, numbers)); await h.flush(); }

test('sparse selection operations use actual numbers, including reverse shift and no default forward anchor', () => {
  const actual = [1, 3, 8];
  assert.deepEqual(selection.changeEpisodes(actual, [], 'all'), actual);
  assert.deepEqual(selection.changeEpisodes(actual, [1, 2], 'invert'), [3, 8]);
  assert.deepEqual(selection.changeEpisodes(actual, [], 'forward', 3), [3, 8]);
  assert.deepEqual(selection.changeEpisodes(actual, [], 'forward'), []);
  assert.deepEqual(selection.changeEpisodes(actual, [], 'forward', 13), []);
  assert.deepEqual(selection.changeEpisodes(actual, [8], 'toggle', 1, 8), actual);
  assert.deepEqual(selection.changeEpisodes(actual, [1, 3], 'toggle', 1, 8), []);
  assert.deepEqual(selection.changeEpisodes(actual, [1, 3, 8], 'clear'), []);
  assert.deepEqual(selection.changeEpisodes(actual, [1], 'toggle', 2), [1]);
});

test('complete episode validation rejects malformed, duplicate, partial, wrong-scope and impossible date arrays', () => {
  assert.equal(selection.validSeason(list(), 42, 1), true);
  assert.equal(selection.validSeason(list(1, []), 42, 1), true);
  for (const patch of [{ complete: false }, { complete: undefined }, { episodes: null }, { episodes: {} }, { tmdb_id: 43 }, { season_number: 0 }, { episodes: [null] }, { episodes: [{ tmdb_id: 1, episode_number: 0 }] }, { episodes: [{ tmdb_id: 1, episode_number: 1.5 }] }, { episodes: [{ tmdb_id: 0, episode_number: 1 }] }, { episodes: [{ tmdb_id: 1, episode_number: 1 }, { tmdb_id: 2, episode_number: 1 }] }, { episodes: [{ tmdb_id: 1, episode_number: 1 }, { tmdb_id: 1, episode_number: 3 }] }, { episodes: [{ tmdb_id: 1, episode_number: 1, name: null }] }, { episodes: [{ tmdb_id: 1, episode_number: 1, air_date: '2026-02-30' }] }]) assert.equal(selection.validSeason({ ...list(), ...patch }, 42, 1), false, JSON.stringify(patch));
});

test('selected keys require complete nonempty valid lists for every selected season and explicit specials', () => {
  const s = { ...selection.emptySelection(), mode: 'selected', selectedSeasons: [3, 1], episodes: { 1: [3, 1], 3: [9] } };
  const lists = { 1: { status: 'ready', data: list() }, 3: { status: 'ready', data: list(3, [9]) } };
  assert.deepEqual(selection.selectedEpisodeKeys(s, lists), ['S01E01', 'S01E03', 'S03E09']);
  for (const state of [{}, { ...lists, 3: { status: 'error' } }, { ...lists, 1: { status: 'loading' } }, { ...lists, 3: { status: 'ready', data: list(3, []) } }]) assert.equal(selection.selectedEpisodeKeys(s, state), null);
  for (const episodes of [{ 1: [2], 3: [9] }, { 1: [], 3: [9] }, { 1: [1, 1], 3: [9] }]) assert.equal(selection.selectedEpisodeKeys({ ...s, episodes }, lists), null);
  const special = { ...s, selectedSeasons: [0], episodes: { 0: [1] } }, specialList = { 0: { status: 'ready', data: list(0, [1]) } };
  assert.equal(selection.selectedEpisodeKeys(special, specialList), null);
  assert.deepEqual(selection.selectedEpisodeKeys({ ...special, includeSpecials: true }, specialList), ['S00E01']);
});

test('safe images accept fixed TMDB projection only', () => {
  assert.equal(selection.safeTMDBImage('https://image.tmdb.org/t/p/w185/abc-123.jpg'), 'https://image.tmdb.org/t/p/w185/abc-123.jpg');
  for (const src of [undefined, 'https://evil.test/x.jpg', 'http://image.tmdb.org/t/p/w185/x.jpg', 'https://image.tmdb.org.evil/t/p/w185/x.jpg', 'https://image.tmdb.org/t/p/w185/../x.jpg', 'https://image.tmdb.org/t/p/w185/x.jpg?secret=x', 'https://image.tmdb.org/t/p/w185/x.svg', 'javascript:alert(1)', '//image.tmdb.org/t/p/w185/x.jpg']) assert.equal(selection.safeTMDBImage(src), undefined);
});

test('actual hook reads detail and cast independently, lazily reads one season, and all mode needs no season array', async () => {
  const h = await harness(); try {
    assert.equal(h.out.detail.status, 'idle'); await h.flush(); assert.equal(h.out.detail.status, 'loading');
    assert.equal(h.calls.length, 2); assert.deepEqual(h.calls.map(c => c.path), ['/api/v1/media/tmdb/tv/{id}', '/api/v1/media/tmdb/{media_type}/{id}/credits']);
    h.reply(0, tv()); await h.flush();
    assert.equal(h.out.canSubscribe, true); assert.equal(h.out.credits.status, 'loading'); assert.equal(h.calls.length, 3);
    assert.equal(h.calls[2].options.params.path.season, 1); assert.equal(h.out.seasons[3], undefined);
    for (const call of h.calls) { assert.equal(call.options.cache, 'no-store'); assert(call.options.signal instanceof AbortSignal); }
  } finally { await h.dispose(); }
});

test('actual movie hook uses movie namespace with same numeric ID and creates no season reads', async () => {
  const h = await harness({ ...media, media_type: 'movie' }); try { await h.ready(); assert.equal(h.out.detail.data.title, 'Movie actual'); assert.equal(h.out.tv, null); assert.equal(h.out.canSubscribe, true); assert.equal(h.calls.length, 2); } finally { await h.dispose(); }
});

test('actual hook multi-season custom scope requires explicit actual selections in every chosen season', async () => {
  const h = await harness(); try {
    await h.ready(); choose(h); assert.equal(h.out.canSubscribe, false);
    await loaded(h); assert.deepEqual(h.out.selection.episodes[1], []); assert.equal(h.out.canSubscribe, false);
    h.out.selectEpisodes('all'); h.render(); assert.deepEqual(h.out.episodeKeys, ['S01E01', 'S01E03']);
    h.out.toggleSeason(3); h.render(); assert.deepEqual(h.out.selection.selectedSeasons, [1, 3]); assert.equal(h.out.canSubscribe, false);
    h.out.selectSeason(3); h.render(); await h.flush(); await loaded(h, 3, [4, 9]); h.out.selectEpisodes('forward', 9); h.render();
    assert.deepEqual(h.out.episodeKeys, ['S01E01', 'S01E03', 'S03E09']); assert.equal(h.out.canSubscribe, true);
    h.out.selectSeason(1); h.render(); assert.deepEqual(h.out.selection.selectedSeasons, [1, 3]); assert.equal(h.out.selection.anchor, null);
    h.out.setMode('all'); h.render(); h.out.setMode('selected'); h.render(); assert.deepEqual(h.out.selection.selectedSeasons, [1, 3]);
  } finally { await h.dispose(); }
});

test('actual hook reverse ranges, deselection ranges, inversion and forward operations never invent E02/E13', async () => {
  const h = await harness(); try {
    await h.ready(); choose(h); await loaded(h, 1, [1, 3, 8]);
    h.out.selectEpisodes('toggle', 8); h.render(); h.out.selectEpisodes('toggle', 1, true); h.render(); assert.deepEqual(h.out.selection.episodes[1], [1, 3, 8]);
    h.out.selectEpisodes('toggle', 8); h.render(); h.out.selectEpisodes('toggle', 1, true); h.render(); assert.deepEqual(h.out.selection.episodes[1], []);
    h.out.selectEpisodes('invert'); h.render(); assert.deepEqual(h.out.selection.episodes[1], [1, 3, 8]);
    h.out.selectEpisodes('forward', 3); h.render(); assert.deepEqual(h.out.selection.episodes[1], [3, 8]);
    h.out.selectEpisodes('toggle', 2); h.render(); assert.deepEqual(h.out.selection.episodes[1], [3, 8]);
    h.out.selectEpisodes('clear'); h.render(); h.out.selectEpisodes('forward'); h.render(); assert.deepEqual(h.out.selection.episodes[1], []);
  } finally { await h.dispose(); }
});

test('actual hook keeps same-scope selection on valid reread and clamps to changed actual lists', async () => {
  const h = await harness(); try {
    await h.ready(); choose(h); await loaded(h); h.out.selectEpisodes('all'); h.render();
    h.out.retrySeason(1); assert.equal(h.out.getSubscription(), null); h.render(); assert.equal(h.out.canSubscribe, false); await loaded(h, 1, [1, 3]); assert.deepEqual(h.out.selection.episodes[1], [1, 3]);
    h.out.selectEpisodes('toggle', 1); h.render(); h.out.selectEpisodes('toggle', 1); h.render();
    h.out.retrySeason(1); h.render(); await loaded(h, 1, [3, 9]); assert.deepEqual(h.out.selection.episodes[1], [3]); assert.equal(h.out.selection.anchor, null); assert.deepEqual(h.out.episodeKeys, ['S01E03']);
    h.out.retryDetail(); h.render(); h.calls.at(-1).reply(tv()); await h.flush(); assert.deepEqual(h.out.selection.episodes[1], [3]);
    h.out.retryDetail(); h.render(); h.calls.at(-1).reply(tv(42, [summary(3)])); await h.flush(); assert.deepEqual(h.out.selection.selectedSeasons, []); assert.equal(h.out.selection.episodes[1], undefined); assert.equal(h.out.canSubscribe, false);
  } finally { await h.dispose(); }
});

test('actual specials are opt-in, never auto-selected, and disabling removes S00 keys and anchor', async () => {
  const h = await harness(); try {
    await h.ready(); h.out.selectSeason(0); h.out.toggleSeason(0); h.render(); assert.equal(h.out.selection.activeSeason, 1); assert.deepEqual(h.out.selection.selectedSeasons, []);
    h.out.setIncludeSpecials(true); h.render(); h.out.selectSeason(0); choose(h, 0); await h.flush(); await loaded(h, 0, [2, 5]);
    h.out.selectEpisodes('all'); h.render(); assert.deepEqual(h.out.episodeKeys, ['S00E02', 'S00E05']);
    h.out.setIncludeSpecials(false); h.render(); assert.deepEqual(h.out.selection.selectedSeasons, []); assert.equal(h.out.selection.episodes[0], undefined); assert.equal(h.out.canSubscribe, false);
    h.out.setIncludeSpecials(true); h.render(); assert.deepEqual(h.out.selection.selectedSeasons, []);
  } finally { await h.dispose(); }
});

test('unknown, partial, malformed and empty actual lists block custom; failed read retry recovers without fabricating choices', async () => {
  for (const value of [{ ...list(), complete: false }, { ...list(), episodes: null }, list(1, []), list(1, [1, 1]), list(3)]) {
    const h = await harness(); try {
      await h.ready(); choose(h); h.reply(2, value); await h.flush(); assert.equal(h.out.canSubscribe, false); assert.equal(h.out.episodeKeys, null);
      assert.equal(h.out.seasons[1].status, value.episodes?.length === 0 ? 'ready' : 'error');
      h.out.retrySeason(1); h.render(); await loaded(h); h.out.selectEpisodes('all'); h.render(); assert.deepEqual(h.out.episodeKeys, ['S01E01', 'S01E03']);
    } finally { await h.dispose(); }
  }
});

test('actual detail and credits errors have independent retries; real empty cast remains distinguishable', async () => {
  const h = await harness(); try {
    h.fail(0); h.reply(1, cast()); await h.flush(); assert.equal(h.out.detail.status, 'error'); assert.equal(h.out.credits.status, 'ready'); assert.deepEqual(h.out.credits.data.cast, []); assert.equal(h.out.canSubscribe, false);
    h.out.retryDetail(); h.render(); h.calls.at(-1).reply(tv()); await h.flush(); assert.equal(h.out.canSubscribe, true);
    h.out.retryCredits(); h.render(); h.calls.at(-1).reply(undefined, 503); await h.flush(); assert.equal(h.out.credits.status, 'error'); assert.equal(h.out.credits.data, undefined); assert.equal(h.out.canSubscribe, true);
    h.out.retryCredits(); h.render(); h.calls.at(-1).reply(cast(42, 'tv', [{ tmdb_id: 7, name: 'Actor' }])); await h.flush(); assert.equal(h.out.credits.data.cast[0].name, 'Actor');
  } finally { await h.dispose(); }
});

test('wrong identity and malformed metadata/cast reject successful HTTP responses', async () => {
  for (const value of [tv(99), { ...tv(), seasons: null }, { ...tv(), seasons: [summary(1), summary(1)] }, { ...tv(), seasons: [{ ...summary(1), episode_count: -1 }] }]) {
    const h = await harness(); try { h.reply(0, value); h.reply(1, cast(42, 'movie')); await h.flush(); assert.equal(h.out.detail.status, 'error'); assert.equal(h.out.credits.status, 'error'); assert.equal(h.out.canSubscribe, false); assert.equal(h.calls.length, 2); } finally { await h.dispose(); }
  }
});

for (const change of ['account', 'role', 'type', 'id', 'logout', 'away-back']) test(`actual hook masks metadata before effects and fences late success/error/403 across ${change}`, async () => {
  for (const late of ['success', 'error', '403']) {
    const h = await harness(); try {
      await h.ready(); choose(h); await loaded(h); h.out.selectEpisodes('all'); h.render();
      h.out.retryDetail(); h.out.retryCredits(); h.out.retrySeason(1); h.render(); const old = h.calls.slice(-3), retained = h.out;
      if (change === 'account') h.auth({ ...user, id: 'actor-b' }, false);
      if (change === 'role') h.auth({ ...user, role: 'member' }, false);
      if (change === 'type') h.media({ ...media, media_type: 'movie' }, false);
      if (change === 'id') h.media({ ...media, tmdb_id: 99 }, false);
      if (change === 'logout') h.auth(null, false);
      if (change === 'away-back') { h.auth({ ...user, id: 'actor-b' }, false); h.auth(user, false); }
      assert.equal(h.out.detail.data, undefined); assert.equal(h.out.credits.data, undefined); assert.deepEqual(h.out.seasons, {}); assert.deepEqual(h.out.selection.selectedSeasons, []); assert.equal(h.out.canSubscribe, false);
      const count = h.calls.length; retained.retryDetail(); retained.retryCredits(); retained.retrySeason(1); assert.equal(retained.getSubscription(), null); retained.setIncludeSpecials(true); retained.selectEpisodes('all'); assert.equal(h.calls.length, count);
      h.commit(); await h.flush(); assert(old.every(c => c.options.signal.aborted));
      for (const call of old) { if (late === 'error') call.fail(); else call.reply(call.path.includes('credits') ? cast() : call.path.includes('seasons') ? list() : tv(), late === '403' ? 403 : 200); }
      await h.flush(); assert.equal(h.out.detail.data, undefined); assert.equal(h.out.credits.data, undefined); assert.deepEqual(h.out.selection.selectedSeasons, []);
      if (change !== 'logout') { const pending = h.calls.filter(c => !c.settled); pending.find(c => c.path.endsWith('credits'))?.reply(cast(change === 'id' ? 99 : 42, change === 'type' ? 'movie' : 'tv')); pending.find(c => !c.path.endsWith('credits'))?.reply(change === 'type' ? movie() : tv(change === 'id' ? 99 : 42)); await h.flush(); assert.equal(h.out.detail.status, 'ready'); }
    } finally { await h.dispose(); }
  }
});

test('same-scope retries supersede ignored abort including delayed 403 without erasing new observations', async () => {
  const h = await harness(); try {
    const old = [...h.calls]; h.out.retryDetail(); h.out.retryCredits(); h.render(); assert(old.every(c => c.options.signal.aborted));
    h.reply(2, tv()); h.reply(3, cast()); await h.flush(); h.reply(0, undefined, 403); h.fail(1); await h.flush(); assert.equal(h.out.detail.status, 'ready'); assert.equal(h.out.credits.status, 'ready'); assert.equal(h.out.canSubscribe, true);
    const firstSeason = h.calls.at(-1); h.out.retrySeason(1); h.render(); await loaded(h, 1, [7]); firstSeason.reply(list()); await h.flush(); assert.deepEqual(selection.actualEpisodeNumbers(h.out.seasons[1].data), [7]);
  } finally { await h.dispose(); }
});

test('current 401/403 on detail, credits or season clears all observations and aborts sibling reads, then explicit retry works', async () => {
  for (const slot of ['detail', 'credits', 'season']) for (const status of [401, 403]) {
    const h = await harness(); try {
      await h.ready(); h.out.retryCredits(); h.out.retryDetail(); h.render();
      if (slot === 'season') { h.calls.findLast(c => c.path === '/api/v1/media/tmdb/tv/{id}').reply(tv()); await h.flush(); h.reply(2, undefined, status); }
      else h.calls.findLast(c => c.path === (slot === 'detail' ? '/api/v1/media/tmdb/tv/{id}' : '/api/v1/media/tmdb/{media_type}/{id}/credits')).reply(undefined, status);
      await h.flush(); assert.equal(h.out.detail.status, 'error'); assert.equal(h.out.detail.data, undefined); assert.equal(h.out.credits.data, undefined); assert.deepEqual(h.out.seasons, {}); assert.equal(h.out.canSubscribe, false);
      h.out.retryDetail(); h.render(); h.calls.at(-1).reply(tv()); await h.flush(); assert.equal(h.out.canSubscribe, true);
    } finally { await h.dispose(); }
  }
});

test('subscribing locks all hook selection and retries without aborting accepted observations', async () => {
  const h = await harness(); try {
    await h.ready(); choose(h); await loaded(h); h.out.selectEpisodes('all'); h.render(); const before = structuredClone(h.out.selection), n = h.calls.length;
    const retained = h.out; h.lock(); for (const handle of [retained, h.out]) { handle.setMode('all'); handle.toggleSeason(3); handle.selectSeason(3); handle.setIncludeSpecials(true); handle.selectEpisodes('clear'); handle.retryDetail(); handle.retryCredits(); handle.retrySeason(1); }
    h.render(); assert.deepEqual(h.out.selection, before); assert.equal(h.calls.length, n); assert.equal(h.out.canSubscribe, false); assert.equal(retained.getSubscription(), null); h.lock(false); assert.equal(h.out.canSubscribe, true);
  } finally { await h.dispose(); }
});

test('unmount aborts detail/cast/season, rejects retained actions, and ignores late errors/403 and success', async () => {
  for (const late of ['success', '403', 'error']) {
    const h = await harness(); try {
      await h.ready(); const retained = h.out; retained.retryDetail(); retained.retryCredits(); h.render(); const pending = h.calls.filter(c => !c.settled); h.unmount(); assert(pending.every(c => c.options.signal.aborted));
      const n = h.calls.length; retained.retryDetail(); retained.retryCredits(); retained.retrySeason(1); retained.setMode('selected'); assert.equal(retained.getSubscription(), null); assert.equal(h.calls.length, n);
      for (const call of pending) { if (late === 'error') call.fail(); else call.reply(tv(), late === '403' ? 403 : 200); } await tick();
    } finally { await h.dispose(); }
  }
});

test('effect replay owns fresh requests and ignores the first generation', async () => {
  const h = await harness(); try {
    const old = [...h.calls]; h.replay(); h.render(); assert(old.every(c => c.options.signal.aborted)); h.reply(2, tv()); h.reply(3, cast()); await h.flush(); h.reply(0, undefined, 403); h.fail(1); await h.flush(); assert.equal(h.out.detail.status, 'ready');
  } finally { await h.dispose(); }
});

test('invalid media scope or no authenticated account performs no metadata IO', async () => {
  for (const value of [null, { ...media, tmdb_id: 0 }, { ...media, tmdb_id: NaN }, { ...media, media_type: 'person' }]) { const h = await harness(value); try { await h.flush(); assert.equal(h.calls.length, 0); assert.equal(h.out.canSubscribe, false); } finally { await h.dispose(); } }
  const h = await harness(); try { h.auth(null); await h.flush(); const n = h.calls.length; h.out.retryDetail(); assert.equal(h.calls.length, n); } finally { await h.dispose(); }
});

test('actual modal renders real sparse names/dates, native season select and existing binding callbacks', async () => {
  const h = await harness(media, true); try {
    await h.ready(); await loaded(h); assert.match(h.text(), /S01E01.*集名未知/); assert.match(h.text(), /S01E03.*集名未知/); assert.doesNotMatch(h.text(), /S01E02|第 13 集|自动获取尚未启用/); assert.match(h.text(), /播出日期未知/);
    const picker = h.nodes().find(n => n.type === 'select'); assert(picker.props.id); assert(h.nodes().some(n => n.type === 'label' && n.props.htmlFor === picker.props.id));
    assert(h.focus.length); assert.equal(h.button('保存剧集订阅').props.disabled, true);
    const root = h.nodes().find(n => n.type === 'RootSelector'), policy = h.nodes().find(n => n.type === 'PolicySelector'), endpoints = h.nodes().find(n => n.type === 'EndpointBindingFields');
    root.props.onChange('root-a'); root.props.onReady(true); policy.props.onChange('policy-a'); policy.props.onReady(true); endpoints.props.onIndexers(['idx-a']); endpoints.props.onDownloader('dl-a'); endpoints.props.onReady(true); h.render();
    assert.equal(h.button('保存剧集订阅').props.disabled, false); h.button('保存剧集订阅').props.onClick(); assert.deepEqual(h.creations, [[media, 'all', [], [], false, 'dl-a', ['idx-a'], 'policy-a', 'root-a']]);
    h.button('检索资源').props.onClick(); assert.deepEqual(h.resources, [media]); h.button('关闭影视详情').props.onClick(); assert.equal(h.closes.length, 1);
    h.lock(); assert.equal(h.button('检索资源').props.disabled, true); assert(h.nodes().filter(n => ['RootSelector', 'PolicySelector', 'EndpointBindingFields'].includes(n.type)).every(n => n.props.disabled)); assert(h.nodes().filter(n => n.type === 'fieldset').every(n => n.props.disabled));
  } finally { await h.dispose(); }
});

test('actual modal credits failure is explicit and does not present an empty cast; account/type switch hides old detail and resets configuration', async () => {
  const h = await harness(media, true); try {
    h.reply(0, tv()); h.fail(1); await h.flush(); assert.match(h.text(), /演员信息读取失败/); assert.doesNotMatch(h.text(), /元数据源未提供演员/);
    h.auth({ ...user, id: 'actor-b' }); await h.flush(); assert.doesNotMatch(h.text(), /TV actual|剧情简介/); assert.equal(h.button('保存剧集订阅').props.disabled, true);
    h.media({ ...media, media_type: 'movie' }); await h.flush(); assert.doesNotMatch(h.text(), /TV actual|选季选集/); const pending = h.calls.filter(c => !c.settled && !c.options.signal.aborted); pending.find(c => c.path.endsWith('credits')).reply(cast(42, 'movie')); pending.find(c => !c.path.endsWith('credits')).reply(movie()); await h.flush(); assert.match(h.text(), /Movie actual/); assert.match(h.text(), /元数据源未提供演员/); assert.equal(h.button('保存电影订阅').props.disabled, true);
  } finally { await h.dispose(); }
});

function readyBindings(h) {
  const nodes = h.nodes();
  const root = nodes.find(n => n.type === 'RootSelector'), policy = nodes.find(n => n.type === 'PolicySelector'), endpoints = nodes.find(n => n.type === 'EndpointBindingFields');
  root.props.onChange('root-a'); root.props.onReady(true); policy.props.onChange('policy-a'); policy.props.onReady(true);
  endpoints.props.onIndexers(['idx-a']); endpoints.props.onDownloader('dl-a'); endpoints.props.onReady(true); h.render();
}

test('actual modal submits sparse multi-season selected keys with original root/policy/endpoint arguments', async () => {
  const h = await harness(media, true); try {
    await h.ready(); await loaded(h);
    h.nodes().find(n => n.type === 'input' && n.props.type === 'radio' && !n.props.checked).props.onChange(); h.render();
    const check = sn => { const label = h.nodes().find(n => n.type === 'label' && h.text(n).startsWith(`S${String(sn).padStart(2, '0')} ·`)); assert(label); h.nodes(label).find(n => n.type === 'input').props.onChange(); h.render(); };
    check(1); h.button('全选本季').props.onClick(); h.render(); check(3);
    assert.equal(h.button('保存选集订阅').props.disabled, true);
    h.nodes().find(n => n.type === 'select').props.onChange({ target: { value: '3' } }); h.render(); await h.flush(); await loaded(h, 3, [4, 9]);
    h.button('从 S03E09 向后选').props.onClick(); h.render(); readyBindings(h);
    const retained = h.button('保存选集订阅').props.onClick;
    retained(); assert.deepEqual(h.creations[0], [media, 'selected', [1, 3], ['S01E01', 'S01E03', 'S03E09'], false, 'dl-a', ['idx-a'], 'policy-a', 'root-a']);
    h.lock(); retained(); assert.equal(h.creations.length, 1, 'retained submit is fenced while parent creates');
    h.lock(false); h.auth({ ...user, id: 'actor-b' }); retained(); assert.equal(h.creations.length, 1, 'retained submit is fenced after account remount');
  } finally { await h.dispose(); }
});

test('actual movie modal retains subscribe/resource actions and actual actor images are lazy with explicit missing/error fallbacks', async () => {
  const m = { ...media, media_type: 'movie' }, h = await harness(m, true); try {
    h.reply(0, { ...movie(), backdrop_url: 'https://image.tmdb.org/t/p/w1280/art.jpg' });
    h.reply(1, cast(42, 'movie', [{ tmdb_id: 1, name: 'Known', character: 'Role', profile_url: 'https://image.tmdb.org/t/p/w185/person.jpg' }, { tmdb_id: 2, name: 'Missing' }, { tmdb_id: 3, name: 'Unsafe', profile_url: 'https://evil.test/x.jpg' }])); await h.flush();
    readyBindings(h); h.button('保存电影订阅').props.onClick(); assert.deepEqual(h.creations[0], [m, 'all', [], [], false, 'dl-a', ['idx-a'], 'policy-a', 'root-a']);
    h.button('检索资源').props.onClick(); assert.deepEqual(h.resources, [m]);
    const imageNodes = h.nodes().filter(n => typeof n.type === 'function' && n.props.className === 'media-detail-portrait');
    assert.equal(imageNodes.length, 3);
    const known = h.image(imageNodes[0]); assert.equal(known.before.type, 'img'); assert.equal(known.before.props.loading, 'lazy'); assert.equal(known.before.props.alt, 'Known'); assert.match(h.text(known.afterFailure), /暂无照片/);
    for (const node of imageNodes.slice(1)) { const image = h.image(node).before; assert.notEqual(image.type, 'img'); assert.equal(image.props['aria-label'], '暂无照片'); }
    const artwork = h.image(h.nodes().find(n => typeof n.type === 'function' && n.props.className === 'media-detail-artwork')).before;
    assert.equal(artwork.type, 'img'); assert.equal(artwork.props.src, 'https://image.tmdb.org/t/p/w1280/art.jpg');
  } finally { await h.dispose(); }
});
