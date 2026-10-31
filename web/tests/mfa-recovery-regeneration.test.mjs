import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import ts from 'typescript';
import React from 'react';
import { renderToStaticMarkup } from 'react-dom/server';

const action = 'account.mfa.recovery.regenerate';
const receiptAction = 'account.mfa.recovery.regenerated';
const journalKey = 'pulse.account-security.v1:account-a:security';
const codes = Array.from({ length: 10 }, (_, i) => `new-recovery-${i}`);

// Run the production panel and command hook together, with deferred API replies.
async function host({ enabled = true, pending } = {}) {
  let slots = [], effects = [], cursor = 0, tree, user = { id: 'account-a', role: 'member' };
  const calls = [], storage = new Map(), writes = [], listeners = new Set();
  if (pending) storage.set(journalKey, JSON.stringify(pending));
  const originals = { localStorage: globalThis.localStorage, window: globalThis.window };
  globalThis.localStorage = {
    getItem: key => storage.get(key) ?? null,
    setItem: (key, value) => { storage.set(key, value); writes.push(value); },
    removeItem: key => storage.delete(key),
  };
  globalThis.window = { addEventListener: (_, fn) => listeners.add(fn), removeEventListener: (_, fn) => listeners.delete(fn) };
  const same = (a, b) => a && b && a.length === b.length && a.every((v, i) => Object.is(v, b[i]));
  const request = method => (path, options) => new Promise((resolve, reject) => calls.push({ method, path, options, resolve, reject, settled: false }));
  const hooks = {
    React, api: { GET: request('GET'), POST: request('POST') },
    useAuth: () => ({ user, checkStatus: async () => {} }),
    MFAEnrollmentSecret: () => null,
    useRef(value) { return slots[cursor++] ??= { current: value }; },
    useState(value) {
      const i = cursor++, owned = slots;
      if (!(i in owned)) owned[i] = typeof value === 'function' ? value() : value;
      return [owned[i], next => { owned[i] = typeof next === 'function' ? next(owned[i]) : next; }];
    },
    useEffect(fn, deps) {
      const i = cursor++, old = slots[i];
      if (!same(old?.deps, deps)) effects.push(() => { old?.cleanup?.(); slots[i] = { deps, cleanup: fn() }; });
    },
  };
  async function load(file, symbol) {
    const source = readFileSync(new URL(`../src/features/auth/${file}`, import.meta.url), 'utf8');
    const ast = ts.createSourceFile(file, source, ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX);
    let clean = source;
    for (const node of [...ast.statements.filter(ts.isImportDeclaration)].reverse()) clean = clean.slice(0, node.getFullStart()) + clean.slice(node.end);
    const key = `__recovery_test_${crypto.randomUUID().replaceAll('-', '')}`;
    globalThis[key] = hooks;
    try {
      const compiled = ts.transpileModule(`const {${Object.keys(hooks).join(',')}}=globalThis[${JSON.stringify(key)}];\n${clean}`, { compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.ESNext, jsx: ts.JsxEmit.React } }).outputText;
      return (await import(`data:text/javascript;base64,${Buffer.from(compiled).toString('base64')}`))[symbol];
    } finally { delete globalThis[key]; }
  }
  hooks.useAccountSecurityCommand = await load('useAccountSecurityCommand.ts', 'useAccountSecurityCommand');
  const Panel = await load('AccountSecurityPanel.tsx', 'AccountSecurityPanel');
  const expand = node => Array.isArray(node) ? node.map(expand) : node?.props ? typeof node.type === 'function' ? expand(node.type(node.props)) : React.cloneElement(node, {}, ...(Array.isArray(node.props.children) ? node.props.children.map(expand) : [expand(node.props.children)])) : node;
  const render = () => { cursor = 0; tree = expand(Panel()); for (const effect of effects.splice(0)) effect(); };
  const flush = async () => { for (let i = 0; i < 20; i++) await Promise.resolve(); render(); render(); };
  const flatten = node => Array.isArray(node) ? node.flatMap(flatten) : node?.props ? [node, ...flatten(node.props.children)] : [];
  const text = node => Array.isArray(node) ? node.map(text).join('') : node?.props ? text(node.props.children) : String(node ?? '');
  const all = predicate => flatten(tree).filter(predicate);
  const find = predicate => { const node = all(predicate)[0]; assert(node, 'rendered element exists'); return node; };
  const button = name => find(n => n.type === 'button' && text(n) === name);
  const field = id => find(n => n.props.id === id);
  const form = label => find(n => n.type === 'form' && text(n).includes(label));
  const change = (id, value) => { field(id).props.onChange({ target: { value } }); render(); };
  const acknowledge = (checked = true) => { find(n => n.type === 'input' && n.props.type === 'checkbox').props.onChange({ target: { checked } }); render(); };
  const reply = (index, status, data, code = 'FIXTURE_ERROR') => {
    const c = calls[index]; assert(c && !c.settled); c.settled = true;
    c.resolve({ response: { status }, data: status < 400 ? data : undefined, error: status >= 400 ? { code } : undefined });
  };
  async function reads() {
    for (const [i, c] of calls.entries()) if (!c.settled && c.method === 'GET' && !c.path.includes('commands/')) reply(i, 200, c.path.endsWith('/mfa') ? { enabled, recovery_codes_remaining: 10 } : { items: [] });
    await flush();
  }
  const cleanup = () => { for (const slot of slots) slot?.cleanup?.(); slots = []; effects = []; };
  return {
    calls, storage, writes, all, text, button, field, form, change, acknowledge, reply, reads, flush,
    markup: () => renderToStaticMarkup(tree),
    journal(pending) { storage.set(journalKey, JSON.stringify(pending)); for (const listener of listeners) listener({ key: journalKey }); render(); render(); },
    async ready() { render(); render(); await reads(); },
    fill(mode = 'otp') { change('recovery-refresh-password', 'password-fixture'); change('recovery-refresh-factor-mode', mode); change('recovery-refresh-factor', mode === 'otp' ? '123456' : 'old-recovery-fixture'); acknowledge(); },
    submit(label = '当前密码（更新恢复码）') { return form(label).props.onSubmit({ preventDefault() {} }); },
    click(name) { const result = button(name).props.onClick(); render(); return result; },
    fail(index) { const c = calls[index]; c.settled = true; c.reject(Error('lost response')); },
    // AccountView keys its subtree by account id and role. Preserve that lifecycle.
    async auth(next) { cleanup(); user = next; render(); render(); await reads(); },
    async close() { cleanup(); for (const c of calls) if (!c.settled) { c.settled = true; c.reject(Error('cleanup')); } for (let i = 0; i < 20; i++) await Promise.resolve(); assert.equal(listeners.size, 0); Object.assign(globalThis, originals); },
  };
}

for (const mode of ['otp', 'recovery_code']) test(`regeneration sends ${mode}, clears secrets immediately, and uses the existing one-time presentation`, async () => {
  const h = await host();
  try {
    await h.ready(); h.fill(mode);
    h.acknowledge(false); await h.submit(); assert.equal(h.calls.length, 2);
    assert.equal(h.button('重新生成恢复码').props.disabled, true);
    assert(h.markup().includes('当前会话保留，TOTP 验证器绑定不变'));
    h.acknowledge(); const sent = h.submit(); await h.flush();
    const c = h.calls[2]; assert.equal(c.path, '/api/v1/auth/mfa/recovery-codes'); assert.equal(c.method, 'POST');
    assert.deepEqual(c.options.body, { current_password: 'password-fixture', [mode]: mode === 'otp' ? '123456' : 'old-recovery-fixture' });
    assert.equal(c.options.cache, 'no-store');
    const journal = JSON.parse(h.storage.get(journalKey));
    assert.deepEqual(journal, { key: c.options.params.header['Idempotency-Key'], action });
    assert.equal(h.field('recovery-refresh-password').props.value, ''); assert.equal(h.field('recovery-refresh-factor').props.value, '');
    assert(h.all(n => n.type === 'input' && n.props.type === 'checkbox').every(n => !n.props.checked));
    h.reply(2, 200, { enabled: true, recovery_codes: codes, replayed: false }); await h.flush(); await h.reads(); await sent; await h.flush();
    assert.equal(h.storage.size, 0); assert(!h.markup().includes(codes[0]));
    assert.equal(h.all(n => n.type === 'h3' && h.text(n) === '保存一次性恢复码').length, 1);
    h.click('显示恢复码'); assert.deepEqual(h.all(n => n.type === 'li').map(h.text), codes);
    h.click('已安全保存，清除恢复码'); assert(!h.markup().includes(codes[0]));
    for (const write of h.writes) assert(!/password-fixture|123456|recovery-fixture|new-recovery/.test(write));
    assert.equal(h.calls.filter(c => c.method === 'POST').length, 1);
  } finally { await h.close(); }
});

test('lost response blocks regeneration, password and MFA actions; recovery is GET only until matching receipt', async () => {
  const h = await host();
  try {
    await h.ready(); h.fill(); const sent = h.submit(); await h.flush(); const key = h.calls[2].options.params.header['Idempotency-Key'];
    h.fail(2); await sent; await h.flush();
    assert.equal(h.button('停用双因素…').props.disabled, true);
    assert(h.all(n => n.type === 'fieldset').every(n => n.props.disabled));
    h.fill(); await h.submit(); await h.submit('修改密码'); assert.equal(h.calls.length, 3);
    for (const [status, data] of [[404, undefined], [200, { action: 'account.mfa.disabled' }], [200, { action: receiptAction, result: { enabled: true, replayed: true } }]]) {
      const index = h.calls.length, recovered = h.click('读取原安全命令回执'); await h.flush();
      assert.equal(h.calls[index].method, 'GET'); assert.equal(h.calls[index].options.params.path.command_key, key);
      h.reply(index, status, data); await h.flush();
      if (data?.action === receiptAction) await h.reads();
      await recovered; await h.flush();
      assert.equal(h.storage.has(journalKey), data?.action !== receiptAction);
    }
    assert(!h.markup().includes('保存一次性恢复码')); assert(h.markup().includes('有效的验证器验证码')); assert(!h.markup().includes('已安全保存'));
    assert.equal(h.calls.filter(c => c.method === 'POST').length, 1);
  } finally { await h.close(); }
});

test('restored regeneration journal blocks all other actions even when MFA is disabled', async () => {
  for (const enabled of [true, false]) {
    const h = await host({ enabled, pending: { key: crypto.randomUUID(), action } });
    try {
      await h.ready(); assert(h.all(n => n.type === 'fieldset').every(n => n.props.disabled));
      if (enabled) { h.fill(); await h.submit(); } else { assert.equal(h.all(n => n.props.id === 'recovery-refresh-password').length, 0); await h.submit('当前密码（绑定验证）'); }
      await h.submit('修改密码'); assert(h.calls.every(c => c.method === 'GET'));
      const recovery = h.click('读取原安全命令回执'); await h.flush(); h.reply(2, 200, { action: receiptAction, result: { enabled: true } }); await h.flush(); await h.reads(); await recovery;
      assert.equal(h.storage.size, 0);
    } finally { await h.close(); }
  }
});

test('other pending MFA or password command blocks regeneration in the shared security journal', async () => {
  for (const pendingAction of ['account.password.change', 'account.mfa.disable', 'account.mfa.setup', 'account.mfa.confirm:challenge-a']) {
    const h = await host({ pending: { key: crypto.randomUUID(), action: pendingAction } });
    try { await h.ready(); h.fill(); assert(h.form('当前密码（更新恢复码）').props.children.props.disabled); await h.submit(); assert.equal(h.calls.length, 2); assert.equal(JSON.parse(h.storage.get(journalKey)).action, pendingAction); }
    finally { await h.close(); }
  }
});

test('replayed success never displays codes and tells the user to generate with a valid factor', async () => {
  const h = await host();
  try {
    await h.ready(); h.fill(); const sent = h.submit(); await h.flush();
    h.reply(2, 200, { enabled: true, replayed: true, recovery_codes: codes }); await h.flush(); await h.reads(); await sent; await h.flush();
    assert(!h.markup().includes('保存一次性恢复码')); assert(!h.markup().includes(codes[0])); assert(h.markup().includes('有效的验证器验证码'));
  } finally { await h.close(); }
});

test('account and role changes discard late regeneration success and isolate journals and secrets', async () => {
  for (const next of [{ id: 'account-b', role: 'member' }, { id: 'account-a', role: 'admin' }]) {
    const h = await host();
    try {
      await h.ready(); h.fill(); const sent = h.submit(); await h.flush(); await h.auth(next);
      const count = h.calls.length; h.reply(2, 200, { enabled: true, recovery_codes: codes, replayed: false }); await sent; await h.flush();
      assert.equal(h.calls.length, count); assert(h.storage.has(journalKey)); assert(!h.markup().includes(codes[0])); assert(!h.markup().includes('恢复码已更新'));
      assert.equal(h.field('recovery-refresh-password').props.value, ''); assert.equal(h.field('recovery-refresh-factor').props.value, '');
    } finally { await h.close(); }
  }
});

test('uncertain HTTP failure retains the key and never offers to resend regeneration', async () => {
  const h = await host();
  try {
    await h.ready(); h.fill(); const sent = h.submit(); await h.flush(); h.reply(2, 500); await sent; await h.flush();
    assert(h.storage.has(journalKey)); assert(h.markup().includes('只读取原安全命令回执')); assert(!h.markup().includes('显式使用原键提交'));
    assert.equal(h.field('recovery-refresh-password').props.value, ''); assert.equal(h.field('recovery-refresh-factor').props.value, '');
    h.fill(); await h.submit(); assert.equal(h.calls.length, 3);
  } finally { await h.close(); }
});

test('late GET receipt cannot clear the original account journal after an account switch', async () => {
  const h = await host({ pending: { key: crypto.randomUUID(), action } });
  try {
    await h.ready(); const recovered = h.click('读取原安全命令回执'); await h.flush();
    await h.auth({ id: 'account-b', role: 'member' }); const count = h.calls.length;
    h.reply(2, 200, { action: receiptAction, result: { enabled: true } }); await recovered; await h.flush();
    assert(h.storage.has(journalKey)); assert.equal(h.calls.length, count); assert(!h.markup().includes('恢复码更新已确认'));
  } finally { await h.close(); }
});

test('generic setup recovery keeps rebind guidance but directs lost recovery codes to valid-factor regeneration', async () => {
  const h = await host({ enabled: false, pending: { key: crypto.randomUUID(), action: 'account.mfa.setup' } });
  try {
    await h.ready(); const recovered = h.click('读取原安全命令回执'); await h.flush();
    h.reply(2, 200, { action: 'account.mfa.setup', result: { replayed: true } }); await h.flush(); await h.reads(); await recovered; await h.flush();
    assert(h.markup().includes('秘密不会再次显示')); assert(h.markup().includes('若丢失绑定密钥，请重新绑定')); assert(h.markup().includes('有效的验证器验证码或未使用的恢复码重新生成')); assert(!h.markup().includes('停用后重新绑定'));
  } finally { await h.close(); }
});

test('503 rollback permits only manual original-key retry after explicit GET404, then preserves a conflicting receipt', async () => {
  const h = await host();
  try {
    await h.ready(); h.fill(); const first = h.submit(); await h.flush();
    const key = h.calls[2].options.params.header['Idempotency-Key'];
    h.reply(2, 503); await first; await h.flush();
    h.fill(); await h.submit(); assert.equal(h.calls.length, 3);
    const recovery = h.click('读取原安全命令回执'); await h.flush();
    h.reply(3, 404); await recovery; await h.flush();
    assert.equal(h.form('当前密码（更新恢复码）').props.children.props.disabled, false);
    assert.equal(h.button('使用原键提交恢复码更新').props.disabled, true);
    assert.equal(h.field('recovery-refresh-password').props.value, '');
    assert.equal(h.field('recovery-refresh-factor').props.value, '');
    await h.submit(); assert.equal(h.calls.length, 4);
    h.fill(); h.change('recovery-refresh-factor', '654321');
    const retry = h.submit(); await h.flush();
    assert.equal(h.calls[4].options.params.header['Idempotency-Key'], key);
    assert.equal(h.calls[4].options.body.otp, '654321');
    assert.equal(h.form('当前密码（更新恢复码）').props.children.props.disabled, true);
    h.reply(4, 409); await retry; await h.flush();
    assert.equal(JSON.parse(h.storage.get(journalKey)).key, key);
    h.fill(); await h.submit(); assert.equal(h.calls.length, 5);
    const receipt = h.click('读取原安全命令回执'); await h.flush();
    h.reply(5, 200, { action: receiptAction, result: { replayed: true } });
    await h.flush(); await h.reads(); await receipt; await h.flush();
    assert.equal(h.storage.size, 0); assert(!h.markup().includes('保存一次性恢复码'));
    assert.equal(h.calls.filter(c => c.method === 'POST').length, 2);
    for (const value of h.writes) assert.deepEqual(Object.keys(JSON.parse(value)).sort(), ['action', 'key']);
  } finally { await h.close(); }
});

test('503 followed by GET404 and manual same-key retry can complete regeneration', async () => {
  const h = await host();
  try {
    await h.ready(); h.fill(); const first = h.submit(); await h.flush();
    const key = h.calls[2].options.params.header['Idempotency-Key'];
    h.reply(2, 503); await first; await h.flush();
    const missing = h.click('读取原安全命令回执'); await h.flush();
    h.reply(3, 404); await missing; await h.flush();
    assert.equal(h.calls.filter(c => c.method === 'POST').length, 1);
    h.fill('recovery_code'); const retry = h.submit(); await h.flush();
    assert.equal(h.calls[4].options.params.header['Idempotency-Key'], key);
    assert.equal(h.calls[4].options.body.recovery_code, 'old-recovery-fixture');
    h.reply(4, 200, { enabled: true, recovery_codes: codes, replayed: false });
    await h.flush(); await h.reads(); await retry; await h.flush();
    assert.equal(h.storage.size, 0);
    h.click('显示恢复码'); assert.deepEqual(h.all(n => n.type === 'li').map(h.text), codes);
    assert.equal(h.calls.filter(c => c.method === 'POST').length, 2);
  } finally { await h.close(); }
});

test('rate rejection of an original-key retry preserves the earlier uncertain command', async () => {
  const key = crypto.randomUUID(), h = await host({ pending: { key, action } });
  try {
    await h.ready();
    const missing = h.click('读取原安全命令回执'); await h.flush(); h.reply(2, 404); await missing; await h.flush();
    h.fill(); const retry = h.submit(); await h.flush();
    assert.equal(h.calls[3].options.params.header['Idempotency-Key'], key);
    h.reply(3, 429, undefined, 'AUTH_RATE_LIMITED'); await retry; await h.flush();
    assert.equal(JSON.parse(h.storage.get(journalKey)).key, key);
    assert.equal(h.form('当前密码（更新恢复码）').props.children.props.disabled, true);
    assert.equal(h.calls.filter(c => c.method === 'POST').length, 1);
    h.fill(); await h.submit(); assert.equal(h.calls.filter(c => c.method === 'POST').length, 1);
  } finally { await h.close(); }
});

test('only regeneration AUTH_RATE_LIMITED 429 clears pending as a definite admission rejection', async () => {
  for (const [status, code, cleared] of [[429, 'AUTH_RATE_LIMITED', true], [429, 'FIXTURE_ERROR', false], [503, 'AUTH_RATE_LIMITED', false]]) {
    const h = await host();
    try {
      await h.ready(); h.fill(); const sent = h.submit(); await h.flush();
      h.reply(2, status, undefined, code); await sent; await h.flush();
      assert.equal(h.storage.has(journalKey), !cleared);
      assert.equal(h.form('当前密码（更新恢复码）').props.children.props.disabled, !cleared);
      assert.equal(h.calls.filter(c => c.method === 'POST').length, 1);
    } finally { await h.close(); }
  }
});

test('GET503, GET403 and wrong-action receipts revoke retry permission without implicit POST', async () => {
  for (const [status, data] of [[503, undefined], [403, undefined], [200, { action: 'account.mfa.disabled' }]]) {
    const h = await host({ pending: { key: crypto.randomUUID(), action } });
    try {
      await h.ready();
      const missing = h.click('读取原安全命令回执'); await h.flush(); h.reply(2, 404); await missing; await h.flush();
      assert.equal(h.form('当前密码（更新恢复码）').props.children.props.disabled, false);
      const denied = h.click('读取原安全命令回执'); await h.flush(); h.reply(3, status, data); await denied; await h.flush();
      assert.equal(h.form('当前密码（更新恢复码）').props.children.props.disabled, true);
      h.fill(); await h.submit(); assert(h.storage.has(journalKey)); assert(h.calls.every(c => c.method === 'GET'));
    } finally { await h.close(); }
  }
});

test('GET404 cannot authorize wrong pending actions or a changed journal, and storage changes reset permission', async () => {
  for (const pendingAction of [action, 'account.password.change']) {
    const h = await host({ pending: { key: crypto.randomUUID(), action: pendingAction } });
    try {
      await h.ready(); const missing = h.click('读取原安全命令回执'); await h.flush();
      h.reply(2, 404); await missing; await h.flush();
      assert.equal(h.form('当前密码（更新恢复码）').props.children.props.disabled, pendingAction !== action);
      const replacement = { key: crypto.randomUUID(), action }; h.journal(replacement);
      assert.equal(h.form('当前密码（更新恢复码）').props.children.props.disabled, true);
      const late = h.click('读取原安全命令回执'); await h.flush();
      h.journal({ key: crypto.randomUUID(), action }); h.reply(3, 404); await late; await h.flush();
      h.fill(); await h.submit(); assert(h.calls.every(c => c.method === 'GET'));
    } finally { await h.close(); }
  }
});

test('retry permission is transient across remounts and late account GET404 cannot grant it', async () => {
  const h = await host({ pending: { key: crypto.randomUUID(), action } });
  try {
    await h.ready(); const missing = h.click('读取原安全命令回执'); await h.flush();
    h.reply(2, 404); await missing; await h.flush();
    assert.equal(h.form('当前密码（更新恢复码）').props.children.props.disabled, false);
    await h.auth({ id: 'account-a', role: 'member' });
    assert.equal(h.form('当前密码（更新恢复码）').props.children.props.disabled, true);
    const index = h.calls.length, late = h.click('读取原安全命令回执'); await h.flush();
    await h.auth({ id: 'account-b', role: 'member' });
    h.reply(index, 404); await late; await h.flush();
    assert(!h.markup().includes('使用原键提交恢复码更新'));
    assert(h.storage.has(journalKey)); assert(h.calls.every(c => c.method === 'GET'));
  } finally { await h.close(); }
});

test('MFA disabled has no regeneration form or code presentation', async () => {
  const h = await host({ enabled: false });
  try { await h.ready(); assert.equal(h.all(n => n.props.id?.startsWith('recovery-refresh')).length, 0); assert(!h.markup().includes('重新生成恢复码')); assert.equal(h.button('开始绑定验证器').props.type, 'submit'); assert(h.calls.every(c => c.method === 'GET')); }
  finally { await h.close(); }
});
