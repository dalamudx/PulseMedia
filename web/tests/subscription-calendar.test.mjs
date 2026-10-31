import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import ts from 'typescript';

const source = readFileSync(new URL('../src/features/configuration/subscriptionCalendar.ts', import.meta.url), 'utf8');
const compiled = ts.transpileModule(source, { compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.ESNext } }).outputText;
const { subscriptionProgress, subscriptionCalendarItems, calendarDays, moveCalendarAnchor } = await import(`data:text/javascript;base64,${Buffer.from(compiled).toString('base64')}`);
const unit = (id, patch = {}) => ({ id: `wanted-${id}`, unit_id: id, unit_key: id, is_skipped: false, is_satisfied: false, ...patch });
const sub = (patch = {}) => ({ id: 'sub-a', user_id: 'user', media_id: 'media', root_id: 'root', state: 'active', intent_version: 1, scope_type: 'all', quality_policy_id: 'default', quality_policy_revision: 1, media_type: 'tv', title: 'Series', ...patch });

// Real sparse unit identities must survive; aggregate counts and array positions are not episodes.
test('summary progress uses server counts without fabricating calendar episodes',()=>{
  const summary=sub({total_episodes:100,satisfied_episodes:25});
  assert.deepEqual(subscriptionProgress(summary),{mediaType:'tv',total:100,satisfied:25,percent:25});
  assert.deepEqual(subscriptionCalendarItems([summary]),{dated:[],unknown:[]});
});

test('progress counts only eligible Wanted projections, with rounded percentages', () => {
  const wanted_items = [unit('S01E03', { is_satisfied: true }), unit('S02E07'), unit('S03E20'),
    unit('skip', { is_skipped: true, is_satisfied: true }), unit('retired', { retired: true, is_satisfied: true }),
    unit('outside', { scope_excluded: true, is_satisfied: true })];
  const input = sub({ wanted_items, total_episodes: 900, satisfied_episodes: 800 });
  assert.deepEqual(subscriptionProgress(input), { mediaType: 'tv', total: 3, satisfied: 1, percent: 33 });
  assert.deepEqual(subscriptionProgress(sub({ media_type: 'movie', wanted_items: [unit('movie', { is_satisfied: true })] })), { mediaType: 'movie', total: 1, satisfied: 1, percent: 100 });
  assert.equal(subscriptionProgress(sub({ wanted_items: [unit('pending')] })).percent, 0);
});

test('media type is strict and empty or excluded denominators stay unknown', () => {
  for (const media_type of [undefined, '', 'TV', 'series', 'unknown']) {
    for (const wanted_items of [undefined, [unit('movie')], [unit('a'), unit('b')]]) {
      assert.equal(subscriptionProgress(sub({ media_type, wanted_items })).mediaType, 'unknown');
      assert.deepEqual(subscriptionCalendarItems([sub({ media_type, wanted_items })]), { dated: [], unknown: [] });
    }
  }
  for (const wanted_items of [[], [unit('a', { is_skipped: true }), unit('b', { scope_excluded: true }), unit('c', { retired: true })]]) {
    assert.deepEqual(subscriptionProgress(sub({ wanted_items, total_episodes: 100, satisfied_episodes: 100 })), { mediaType: 'tv', total: 0, satisfied: 0, percent: null });
  }
});

test('calendar preserves paused, sparse keys and actual effective skip status', () => {
  const input = sub({ state: 'paused', wanted_items: [
    unit('u3', { unit_key: 'S01E03', air_date: '2024-02-29' }),
    unit('u7', { unit_key: 'S02E07', air_date: '2024-03-01', is_satisfied: true }),
    unit('skip', { air_date: '2024-03-02', is_skipped: true, user_skipped: false, is_satisfied: true }),
    unit('projection', { air_date: '2024-03-03', user_skipped: true, is_skipped: false }),
    unit('retired', { retired: true }), unit('scope', { scope_excluded: true }), unit('unknown', { unit_key: undefined }),
  ] });
  const snapshot = structuredClone(input);
  const { dated, unknown } = subscriptionCalendarItems([input, sub({ id: 'archived', state: 'archived', wanted_items: [unit('x')] }), sub({ id: 'movie', media_type: 'movie', wanted_items: [unit('y')] })]);
  assert.deepEqual(dated[0], { subscriptionId: 'sub-a', unitId: 'u3', unitKey: 'S01E03', title: 'Series', date: '2024-02-29', state: 'paused', status: 'pending' });
  assert.deepEqual(dated.map(item => item.status), ['pending', 'satisfied', 'skipped', 'pending']);
  assert(dated.every(item => item.state === 'paused'));
  assert.deepEqual(unknown, [{ subscriptionId: 'sub-a', unitId: 'unknown', unitKey: '', title: 'Series', date: null, state: 'paused', status: 'pending' }]);
  assert.deepEqual(input, snapshot);
});

test('calendar dates require exact real Gregorian provider dates, with no epoch fallback', () => {
  const valid = ['0001-01-01', '0099-12-31', '1900-03-01', '2000-02-29', '2024-02-29', '9999-12-31'];
  const invalid = [undefined, null, '', '0000-01-01', '1900-02-29', '2023-02-29', '2024-04-31', '2024-13-01', '2024-00-01', '2024-01-00', '2024-1-01', ' 2024-01-01', '2024-01-01\n', 'garbage', '2024-03-01T00:30:00+14:00', '2024-02-29T23:30:00Z'];
  const result = subscriptionCalendarItems([sub({ wanted_items: [...valid, ...invalid].map((air_date, i) => unit(String(i), { air_date })) })]);
  assert.deepEqual(result.dated.map(item => item.date), valid);
  assert.equal(result.unknown.length, invalid.length);
  assert(result.unknown.every(item => item.date === null));
});

test('sort and per-subscription/unit deduplication are deterministic without mutating inputs', () => {
  const a = sub({ id: 'a', title: 'A', wanted_items: [unit('2', { unit_key: 'S02E07', air_date: '2024-03-01' }), unit('1', { unit_key: 'S01E03', air_date: '2024-03-01' }), unit('0', { unit_key: 'S01E03', air_date: '2024-03-01' }), unit('unknown')] });
  const b = sub({ id: 'b', title: 'A', wanted_items: [unit('1', { air_date: '2024-03-01' })] });
  const c = sub({ id: 'c', title: 'B', wanted_items: [unit('1', { air_date: '2024-03-01' }), unit('early', { air_date: '2024-02-29' })] });
  const conflict = sub({ ...a, state: 'paused', wanted_items: [unit('1', { air_date: '2024-04-01' }), unit('unknown')] });
  const inputs = [c, b, a, a, conflict];
  const snapshot = structuredClone(inputs);
  const result = subscriptionCalendarItems(inputs);
  assert.deepEqual(result.dated.map(item => [item.subscriptionId, item.unitId]), [['c', 'early'], ['a', '0'], ['a', '1'], ['a', '2'], ['b', '1'], ['c', '1']]);
  assert.equal(result.unknown.length, 1);
  assert.deepEqual(subscriptionCalendarItems([...inputs].reverse().map(s => ({ ...s, wanted_items: [...s.wanted_items].reverse() }))), result);
  assert.deepEqual(inputs, snapshot);
});

test('week grids start Monday, cross years and include leap days', () => {
  assert.deepEqual(calendarDays('2023-01-01', 'week'), ['2022-12-26', '2022-12-27', '2022-12-28', '2022-12-29', '2022-12-30', '2022-12-31', '2023-01-01']);
  assert.deepEqual(calendarDays('2024-02-29', 'week'), ['2024-02-26', '2024-02-27', '2024-02-28', '2024-02-29', '2024-03-01', '2024-03-02', '2024-03-03']);
  assert.deepEqual(calendarDays('2024-03-03', 'week'), calendarDays('2024-02-26', 'week'));
});

test('month grids use exactly 28, 35 or 42 Monday-to-Sunday civil days', () => {
  for (const [anchor, length, first, last] of [
    ['2021-02-15', 28, '2021-02-01', '2021-02-28'],
    ['2024-02-29', 35, '2024-01-29', '2024-03-03'],
    ['2024-09-01', 42, '2024-08-26', '2024-10-06'],
    ['2024-12-31', 42, '2024-11-25', '2025-01-05'],
  ]) {
    const days = calendarDays(anchor, 'month');
    assert.equal(days.length, length);
    assert.equal(days[0], first);
    assert.equal(days.at(-1), last);
    assert.equal(new Set(days).size, length);
    assert.deepEqual(calendarDays(first, 'week'), days.slice(0, 7));
    assert.deepEqual(calendarDays(last, 'week'), days.slice(-7));
    assert.deepEqual(calendarDays(anchor.slice(0, 8) + '01', 'month'), days);
  }
});

test('anchor moves clamp months, preserve civil weeks and handle year boundaries', () => {
  for (const [anchor, mode, step, expected] of [
    ['2023-01-31', 'month', 1, '2023-02-28'], ['2024-01-31', 'month', 1, '2024-02-29'],
    ['2024-03-31', 'month', -1, '2024-02-29'], ['2024-02-29', 'month', 12, '2025-02-28'],
    ['2024-12-31', 'month', 1, '2025-01-31'], ['2024-01-31', 'month', -13, '2022-12-31'],
    ['2024-02-28', 'week', 1, '2024-03-06'], ['2024-01-01', 'week', -1, '2023-12-25'],
    ['2024-03-10', 'week', 2, '2024-03-24'], ['0099-12-31', 'month', 1, '0100-01-31'],
  ]) assert.equal(moveCalendarAnchor(anchor, mode, step), expected);
  assert.equal(moveCalendarAnchor('2024-02-29', 'month', 0), '2024-02-29');
});

test('invalid anchors and unsupported date range fail safely without guessed dates', () => {
  for (const anchor of ['', 'bad', '2024-02-30', '2023-02-29', '0000-01-01', '2024-01-01T00:00:00Z']) {
    for (const mode of ['week', 'month']) {
      assert.deepEqual(calendarDays(anchor, mode), []);
      assert.equal(moveCalendarAnchor(anchor, mode, 1), anchor);
    }
  }
  for (const step of [NaN, Infinity, -Infinity, 0.5, Number.MAX_SAFE_INTEGER + 1, Number.MAX_SAFE_INTEGER]) {
    for (const mode of ['week', 'month']) assert.equal(moveCalendarAnchor('2024-01-31', mode, step), '2024-01-31');
  }
  assert.equal(moveCalendarAnchor('9999-12-31', 'week', 1), '9999-12-31');
  assert.equal(moveCalendarAnchor('0001-01-01', 'month', -1), '0001-01-01');
  assert.deepEqual(calendarDays('9999-12-31', 'month'), []);
});

test('civil results do not drift in DST, negative or positive offset timezones', () => {
  const previous = process.env.TZ;
  try {
    for (const timezone of ['UTC', 'America/Los_Angeles', 'Pacific/Kiritimati', 'Asia/Shanghai']) {
      process.env.TZ = timezone;
      assert.deepEqual(calendarDays('2024-03-10', 'week'), ['2024-03-04', '2024-03-05', '2024-03-06', '2024-03-07', '2024-03-08', '2024-03-09', '2024-03-10']);
      assert.equal(moveCalendarAnchor('2024-03-10', 'week', 1), '2024-03-17');
      assert.equal(moveCalendarAnchor('2024-11-03', 'week', 1), '2024-11-10');
      assert.equal(subscriptionCalendarItems([sub({ wanted_items: [unit('leap', { air_date: '2024-02-29' })] })]).dated[0].date, '2024-02-29');
    }
  } finally {
    if (previous === undefined) delete process.env.TZ;
    else process.env.TZ = previous;
  }
});
