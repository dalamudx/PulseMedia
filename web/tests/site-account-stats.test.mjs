import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import ts from 'typescript';
import React from 'react';

const root = new URL('../src/features/configuration/', import.meta.url);
const compile = source => ts.transpileModule(source, { compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.ESNext, jsx: ts.JsxEmit.React } }).outputText;
const model = await import(`data:text/javascript;base64,${Buffer.from(compile(readFileSync(new URL('accountStatsView.ts', root), 'utf8'))).toString('base64')}`);
const { accountStatsView, statsAmount, statsRatio, statsReadError } = model;
const source = { sourceID: 'site-a', sourceVersion: 3 };
const observation = (patch = {}) => ({
  config: { indexer_id: 'site-a', expected_account_id: '42', enabled: true, version: 2, source_config_version: 3 },
  stats: { account_id: '42', uploaded: { value: '1.00', unit: 'GB', approximate: true }, downloaded: null, ratio: { state: 'finite', value: '1.234' }, bonus: '0.0' },
  observed_at: '2026-10-20T01:02:03Z', sample_config_version: 2, sample_source_version: 3,
  last_attempt_at: '2026-10-20T01:02:03Z', collection_state: 'succeeded', stale: false, ...patch,
});
test('preserves zero, decimal precision, canonical groups and very large amounts without byte conversion', () => {
  for (const value of ['0.00', '1.00', '1234.567', '999999999999999999999999999999.00']) {
    assert.equal(statsAmount({ value, unit: 'GB', approximate: true }), `约 ${value} GB`);
  }
  assert.equal(statsAmount(null), '未知');
  assert.deepEqual(accountStatsView(observation(), source).values, ['约 1.00 GB', '未知', '1.234', '0.0']);
  assert.equal(accountStatsView(observation({ stats: null }), source).values[3], '未知');
});
test('ratio preserves all four server states and never recomputes rounded traffic', () => {
  for (const [state, expected] of [['finite', '0.000'], ['infinite', '∞'], ['undefined', '未定义'], ['unknown', '未知']]) {
    assert.equal(statsRatio({ state, value: state === 'finite' ? '0.000' : undefined }), expected);
  }
});
test('missing config and samples do not become zero; observed time is the persisted time', () => {
  assert.equal(accountStatsView(null, source).status, '尚未配置统计采集');
  assert.deepEqual(accountStatsView(observation({ stats: null, observed_at: null }), source).values, ['未知', '未知', '未知', '未知']);
  assert.equal(accountStatsView(observation(), source).observedAt, observation().observed_at);
});
test('disabled, old failure and server/source/sample/config version mismatches are explicit', () => {
  assert.match(accountStatsView(observation({ config: { ...observation().config, enabled: false } }), source).status, /采集已停用/);
  for (const patch of [{ stale: true }, { sample_source_version: 2 }, { sample_config_version: 1 }, { config: { ...observation().config, source_config_version: 2 } }, { collection_state: 'failed' }]) {
    const view = accountStatsView(observation(patch), source);
    assert.equal(view.stale, true);
    assert.match(view.status, /陈旧.*上次有效记录/);
  }
  assert.equal(accountStatsView(observation(), source).stale, false);
  assert.match(accountStatsView(observation({ failure_code: 'auth_required', stale: true }), source).status, /认证失效/);
  assert.match(accountStatsView(observation({ failure_code: 'blocked', stale: true }), source).status, /站点拒绝访问/);
});
test('wrong source ID and PT account cannot display private metrics; errors use fixed safe copy', () => {
  for (const patch of [{ config: { ...observation().config, indexer_id: 'other' } }, { stats: { ...observation().stats, account_id: 'other' } }]) {
    assert.deepEqual(accountStatsView(observation(patch), source).values, ['未知', '未知', '未知', '未知']);
  }
  for (const status of [0, 401, 403, 404, 500, 503]) assert.doesNotMatch(statsReadError(status), /SECRET|https:/);
});

// Execute the actual component's hooks/effect cleanups. Transport deliberately ignores abort.
const componentSource = readFileSync(new URL('SiteAccountStatsPanel.tsx', root), 'utf8');
const stripImports = text => {
  const ast = ts.createSourceFile('panel.tsx', text, ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX);
  for (const node of [...ast.statements.filter(ts.isImportDeclaration)].reverse()) text = text.slice(0, node.getFullStart()) + text.slice(node.end);
  return text;
};
async function harness() {
  let user = { id: 'admin-a', role: 'admin' }, checking = false, props = { ...source }, child, output, current;
  const calls = [], listeners = new Set();
  const document = { hidden: false, addEventListener: (_, fn) => listeners.add(fn), removeEventListener: (_, fn) => listeners.delete(fn) };
  const equal = (a, b) => a && b && a.length === b.length && a.every((v, i) => Object.is(v, b[i]));
  const host = {
    React, ...model, ChevronDown: () => null, ChevronRight: () => null, document,
    useAuth: () => ({ user, checking }), useId: () => 'test-stats',
    useState(initial) { const i = current.cursor++; if (!(i in current.slots)) current.slots[i] = typeof initial === 'function' ? initial() : initial; const owner = current; return [owner.slots[i], next => { owner.slots[i] = typeof next === 'function' ? next(owner.slots[i]) : next; }]; },
    useEffect(fn, deps) { const i = current.cursor++, old = current.slots[i], owner = current; if (!equal(old?.deps, deps)) owner.effects.push(() => { old?.cleanup?.(); owner.slots[i] = { deps, cleanup: fn() }; }); },
    api: { GET: (path, options) => new Promise((resolve, reject) => calls.push({ path, options, resolve, reject })) },
  };
  const key = `__stats_${crypto.randomUUID().replaceAll('-', '')}`;
  globalThis[key] = host;
  let Panel;
  try {
    const text = `const {${Object.keys(host).join(',')}}=globalThis[${JSON.stringify(key)}];\n` + stripImports(componentSource);
    ({ SiteAccountStatsPanel: Panel } = await import(`data:text/javascript;base64,${Buffer.from(compile(text)).toString('base64')}`));
  } finally { delete globalThis[key]; }
  const cleanup = () => { if (child) for (const slot of child.slots) slot?.cleanup?.(); child = null; };
  const render = () => {
    const next = Panel(props);
    if (!next) { cleanup(); output = null; return; }
    if (child?.key !== next.key) { cleanup(); child = { key: next.key, slots: [], cursor: 0, effects: [] }; }
    current = child; current.cursor = 0;
    output = next.type(next.props);
    for (const effect of current.effects.splice(0)) effect();
    current.cursor = 0; output = next.type(next.props);
  };
  const walk = node => !node || typeof node !== 'object' ? [] : [node, ...React.Children.toArray(node.props?.children).flatMap(walk)];
  const text = node => typeof node === 'string' || typeof node === 'number' ? String(node) : !node ? '' : React.Children.toArray(node.props?.children).map(text).join('');
  const flush = async () => { for (let i = 0; i < 12; i++) await Promise.resolve(); render(); };
  const h = {
    calls, render, flush, text: () => text(output),
    click(label) { const button = walk(output).find(n => n.type === 'button' && text(n) === label); assert(button, label); assert(!button.props.disabled, `${label} enabled`); button.props.onClick(); render(); },
    scope(patch) { props = { ...props, ...patch }; render(); },
    auth(next, pending = false) { user = next; checking = pending; render(); },
    hide() { document.hidden = true; for (const fn of listeners) fn(); render(); },
    show() { document.hidden = false; render(); },
    reply(i, status = 200, value = observation()) { calls[i].resolve({ response: { ok: status < 400, status }, data: { observation: value }, error: { message: 'SECRET upstream body' } }); },
    close() { cleanup(); assert.equal(listeners.size, 0); },
    disabled() { return walk(output).filter(n => n.type === 'button').every(n => n.props.disabled); },
  };
  render(); return h;
}
test('closed cards do not fan out; open and manual refresh only GET persisted data; loading/failure removes old values', async () => {
  const h = await harness();
  try {
    assert.equal(h.calls.length, 0);
    h.click('账号统计'); assert.equal(h.calls.length, 1);
    assert.equal(h.calls[0].path, '/api/v1/indexers/{indexer_id}/account-stats');
    assert.equal(h.calls[0].options.cache, 'no-store');
    h.reply(0); await h.flush(); assert.match(h.text(), /约 1.00 GB/);
    h.click('刷新'); assert.doesNotMatch(h.text(), /约 1.00 GB/);
    h.reply(1, 503); await h.flush(); assert.match(h.text(), /读取失败/); assert.doesNotMatch(h.text(), /SECRET|约 1.00 GB/);
    h.click('账号统计'); h.click('账号统计'); assert.equal(h.calls.length, 3);
    h.calls[2].reject(Error('SECRET')); await h.flush(); assert.doesNotMatch(h.text(), /SECRET/);
  } finally { h.close(); }
});
for (const change of ['sourceID', 'sourceVersion', 'account', 'role', 'checking']) test(`actual keyed lifecycle closes and fences abort-ignoring old response on ${change} change`, async () => {
  const h = await harness();
  try {
    h.click('账号统计');
    if (change === 'sourceID') h.scope({ sourceID: 'site-b' });
    if (change === 'sourceVersion') h.scope({ sourceVersion: 4 });
    if (change === 'account') h.auth({ id: 'admin-b', role: 'admin' });
    if (change === 'role') h.auth({ id: 'admin-a', role: 'member' });
    if (change === 'checking') h.auth({ id: 'admin-a', role: 'admin' }, true);
    assert.equal(h.calls[0].options.signal.aborted, true);
    h.reply(0); await h.flush();
    assert.doesNotMatch(h.text(), /约 1.00 GB|观测时间/);
    assert.equal(h.calls.length, 1);
    if (['sourceID', 'sourceVersion', 'account'].includes(change)) { h.click('账号统计'); assert.equal(h.calls.length, 2); }
  } finally { h.close(); }
});
test('403 clears data and disables all reads until identity changes', async () => {
  const h = await harness();
  try {
    h.click('账号统计'); h.reply(0); await h.flush(); h.click('刷新'); h.reply(1, 403); await h.flush();
    assert.match(h.text(), /无权读取/); assert.doesNotMatch(h.text(), /约 1.00 GB|SECRET/); assert.equal(h.disabled(), true);
    h.scope({ sourceVersion: 4 }); h.click('账号统计'); assert.equal(h.calls.length, 3);
  } finally { h.close(); }
});
test('hidden, collapsed and unmounted panels abort and ignore late successes', async () => {
  for (const stop of ['hide', 'collapse', 'unmount']) {
    const h = await harness();
    try {
      h.click('账号统计');
      if (stop === 'hide') h.hide(); else if (stop === 'collapse') h.click('账号统计'); else h.auth(null);
      assert.equal(h.calls[0].options.signal.aborted, true);
      h.reply(0); await h.flush(); assert.doesNotMatch(h.text(), /约 1.00 GB/);
      if (stop === 'hide') { h.show(); assert.equal(h.calls.length, 1); h.click('账号统计'); assert.equal(h.calls.length, 2); }
    } finally { h.close(); }
  }
});
test('known members and absent session never issue a GET', async () => {
  const h = await harness();
  try { h.auth({ id: 'member', role: 'member' }); assert.equal(h.text(), ''); h.auth(null); assert.equal(h.calls.length, 0); }
  finally { h.close(); }
});
