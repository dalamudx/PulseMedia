import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import ts from 'typescript';

const root = new URL('../src/features/configuration/', import.meta.url);
const source = name => readFileSync(new URL(name, root), 'utf8');
const compile = text => ts.transpileModule(text, { compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.ESNext, jsx: ts.JsxEmit.React, jsxFactory: 'h', jsxFragmentFactory: 'Fragment' } }).outputText;
const load = text => import(`data:text/javascript;base64,${Buffer.from(compile(text)).toString('base64')}#${crypto.randomUUID()}`);
const model = await load(source('operationActivityModel.ts'));
async function panel(name) {
  const key = `__operation_${crypto.randomUUID().replaceAll('-', '')}`;
  const h = (type, props, ...children) => ({ type, props: props ?? {}, children: children.flat(Infinity).filter(v => v !== null && v !== false && v !== undefined) });
  globalThis[key] = { ...model, h, Fragment: 'fragment' };
  try { return await load(`const {${Object.keys(globalThis[key]).join(',')}}=globalThis[${JSON.stringify(key)}];\n${source(name).replace(/^import[^;]*;\n/gm, '')}`); }
  finally { delete globalThis[key]; }
}
const { OperationTelemetry } = await panel('OperationTelemetry.tsx');
const { OperationTimeline } = await panel('OperationTimeline.tsx');
const nodes = node => typeof node !== 'object' ? [] : [node, ...node.children.flatMap(nodes)];
const text = node => typeof node !== 'object' ? String(node) : node.children.map(text).join(' ');
const step = (patch = {}) => ({ step_id: 'step', order: 1, action: 'hardlink', state: 'pending', file_name: '电影.mkv', updated_at: '2026-10-19T12:00:00Z', ...patch });
const live = patch => ({ detail: null, timeline: { operation_id: 'op', plan_id: 'plan', plan_status: 'approved', items: [], observed_at: '2026-10-19T12:00:00Z' }, steps: [], loading: false, loadingMore: false, error: '', timelineError: '', detailStale: false, timelineStale: false, canAct: false, refresh: async () => true, loadMore: async () => true, ...patch });

test('exact native decimal rates include zero and MaxInt64; invalid values stay unknown', () => {
  for (const value of [undefined, null, 100, '', '-1', '+1', '01', '1.0', '1e3', ' 1', '9223372036854775808', '9'.repeat(200)]) {
    assert.equal(model.nativeRate(value), null); assert.equal(model.formatRate(value), '速度未知');
  }
  assert.equal(model.nativeRate('9223372036854775807'), 9223372036854775807n);
  for (const [value, expected] of [['0', '0 B/s'], ['1023', '1023 B/s'], ['1024', '1 KiB/s'], ['1536', '1.5 KiB/s'], ['1048576', '1 MiB/s'], ['1073741824', '1 GiB/s'], ['1099511627776', '1 TiB/s'], ['9223372036854775807', '8388607.9 TiB/s']]) assert.equal(model.formatRate(value), expected);
});

test('freshness accepts 45 seconds but rejects older, invalid and future timestamps', () => {
  const now = Date.parse('2026-10-19T12:00:45Z');
  assert(model.observationFresh('2026-10-19T12:00:00Z', now));
  for (const stamp of ['2026-10-19T11:59:59.999Z', '2026-10-19T12:00:45.001Z', 'bad', undefined]) assert(!model.observationFresh(stamp, now));
});

test('only recognized progress is rendered; one-byte-short floating precision never rounds to 100', () => {
  for (const progress of [NaN, Infinity, -0.01, 1.01, undefined, '0.5']) assert.equal(model.progressSnapshot({ progress, progress_source: 'verified_bytes' }), null);
  assert.equal(model.progressSnapshot({ progress: .5, progress_source: 'invented' }), null);
  assert.equal(model.progressSnapshot({ progress: 1 - Number.EPSILON, progress_source: 'verified_bytes' }).percent, 99);
  assert.deepEqual(model.progressSnapshot({ progress: 1, progress_source: 'native_percent' }), { percent: 100, source: '下载器记录' });
  assert.equal(model.progressSnapshot({ progress: 0, progress_source: 'verified_bytes' }).source, '已验证字节');
});

test('step merge deduplicates identity and retains actual numeric ordering and updated state', () => {
  const result = model.mergeSteps([step({ step_id: 'late', order: 9 }), step()], [step({ state: 'succeeded' }), step({ step_id: 'middle', order: 4 })]);
  assert.deepEqual(result.map(v => v.order), [1, 4, 9]); assert.equal(result[0].state, 'succeeded');
});

test('safe labels never echo arbitrary status, action or path evidence', () => {
  for (const name of ['/root/secret', 'folder\\secret', 'https://secret', '..', 'a\nsecret']) assert.equal(model.safeFileName(name), '文件名不可用');
  assert.equal(model.safeFileName('超长电影文件.mkv'), '超长电影文件.mkv');
  for (const state of ['pending', 'executing', 'unknown', 'review_required', 'retry_wait', 'paused', 'succeeded', 'skipped', 'failed', 'aborted']) assert.notEqual(model.stepStateLabel(state), '步骤状态未知');
  assert.equal(model.stepStateLabel('completed'), '此步骤完成'); assert.equal(model.stepStateLabel('succeeded'), '此步骤完成'); assert.equal(model.stepStateLabel('skipped'), '已跳过');
  assert.equal(model.stepStateLabel('secret'), '步骤状态未知'); assert.equal(model.planStateLabel('secret'), '计划状态未知'); assert.equal(model.stepActionLabel('secret'), '整理操作');
});

test('read validation rejects mismatched identities, malformed and unbounded pages while accepting a real empty plan', () => {
  assert(model.validDetail({ operation_id: 'op', status: 'unknown' }, 'op'));
  assert(!model.validDetail({ operation_id: 'other', status: 'tracking' }, 'op'));
  const page = live({}).timeline;
  assert(model.validTimeline(page, 'op'));
  for (const patch of [{ operation_id: 'other' }, { items: null }, { items: [null] }, { items: [step({ order: 1.5 })] }, { items: Array.from({ length: 101 }, () => step()) }]) assert(!model.validTimeline({ ...page, ...patch }, 'op'));
});

test('telemetry shows source/time and stale snapshots, no progress bar for legacy or invalid progress', () => {
  for (const telemetry of [undefined, { progress: .6 }, { progress: 1.5, progress_source: 'verified_bytes' }]) {
    const tree = OperationTelemetry({ telemetry }); assert.equal(nodes(tree).filter(n => n.type === 'progress').length, 0); assert.match(text(tree), /进度未知/);
  }
  const stamp = new Date(Date.now() - 46000).toISOString();
  const tree = OperationTelemetry({ telemetry: { progress: 1 - Number.EPSILON, progress_source: 'verified_bytes', progress_observed_at: stamp, download_rate_bytes_per_second: '0', rate_status: 'current', rate_observed_at: stamp } });
  assert.match(text(tree), /99%/); assert.match(text(tree), /已验证字节/); assert.match(text(tree), /0 B\/s/); assert.match(text(tree), /观测已过期/);
  assert.equal(nodes(tree).find(n => n.type === 'progress').props.value, 99);
  assert.match(text(OperationTelemetry({ stale: true, telemetry: { rate_status: 'unknown', download_rate_bytes_per_second: '1024' } })), /速度未知.*读取尚未确认/);
});

test('timeline displays only actual steps, safe filenames and distinct states with no overall ownership claim', () => {
  const tree = OperationTimeline({ live: live({ steps: [step({ state: 'succeeded', file_name: '/private/SECRET' }), step({ step_id: 'skip', order: 2, state: 'skipped', action: 'SECRET' })] }) });
  assert.equal(tree.props['aria-busy'], false); assert.match(text(tree), /此步骤完成/); assert.match(text(tree), /已跳过/); assert(!/SECRET|已入库/.test(text(tree)));
  assert.equal(nodes(tree).filter(n => n.type === 'li').length, 2);
  assert.match(text(OperationTimeline({ live: live({}) })), /当前计划没有整理步骤/);
  assert(!/此步骤完成/.test(text(OperationTimeline({ live: live({}) }))));
});

test('timeline retry and load more remain explicit read buttons and expose busy state', async () => {
  let refreshes = 0, pages = 0;
  const state = live({ timelineError: '读取失败', timelineStale: true, timeline: { ...live({}).timeline, next_cursor: 'next' }, refresh: async () => { refreshes++; return true; }, loadMore: async () => { pages++; return true; } });
  const tree = OperationTimeline({ live: state }), buttons = nodes(tree).filter(n => n.type === 'button');
  assert.equal(buttons.length, 2); buttons[0].props.onClick(); buttons[1].props.onClick(); assert.equal(refreshes, 1); assert.equal(pages, 1);
  const busy = OperationTimeline({ live: { ...state, loadingMore: true } }); assert.equal(busy.props['aria-busy'], true); assert(nodes(busy).filter(n => n.type === 'button').every(n => n.props.disabled));
});


test('actual transfer plan lifecycle states have explicit labels', () => {
  for (const [state, label] of Object.entries({ created: '已创建', approved: '已批准', executing: '执行中', completed: '计划步骤完成', failed: '执行失败', review_required: '需要人工复核' })) assert.equal(model.planStateLabel(state), label);
  assert.equal(model.planStateLabel('future-state'), '计划状态未知');
});
