import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import ts from 'typescript';
import React from 'react';
import { renderToStaticMarkup } from 'react-dom/server';

const action = 'account.sessions.revoke_others';
const receiptAction = 'account.sessions.others_revoked';
const journalKey = 'pulse.account-security.v1:account-a:security';

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
    const key = `__other_sessions_test_${crypto.randomUUID().replaceAll('-', '')}`;
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
  const acknowledge = (checked = true) => { field('other-sessions-confirm').props.onChange({ target: { checked } }); render(); };
  const reply = (index, status, data, code = 'FIXTURE_ERROR') => {
    const c = calls[index]; assert(c && !c.settled); c.settled = true;
    c.resolve({ response: { status }, data: status < 400 ? data : undefined, error: status >= 400 ? { code } : undefined });
  };
  async function reads() {
    for (const [i, c] of calls.entries()) if (!c.settled && c.method === 'GET' && !c.path.includes('commands/')) reply(i, 200, c.path.endsWith('/mfa') ? { enabled, recovery_codes_remaining: 10 } : { items: [{ id: 'current', current: true, created_at: '2026-01-01', last_seen_at: '2026-01-01', expires_at: '2027-01-01' }] });
    await flush();
  }
  const cleanup = () => { for (const slot of slots) slot?.cleanup?.(); slots = []; effects = []; };
  return {
    calls, storage, writes, all, text, button, field, form, change, acknowledge, reply, reads, flush,
    markup: () => renderToStaticMarkup(tree),
    journal(pending) { storage.set(journalKey, JSON.stringify(pending)); for (const listener of listeners) listener({ key: journalKey }); render(); render(); },
    async ready() { render(); render(); await reads(); },
    fill(mode = 'otp') { if (all(n => n.type === 'button' && text(n) === '撤销其它会话…').length) this.click('撤销其它会话…'); change('other-sessions-password', 'password-fixture'); if (enabled) { change('other-sessions-factor-mode', mode); change('other-sessions-factor', mode === 'otp' ? '123456' : 'old-recovery-fixture'); } acknowledge(); },
    submit(label = '当前密码（撤销其它会话）') { return form(label).props.onSubmit({ preventDefault() {} }); },
    click(name) { const result = button(name).props.onClick(); render(); return result; },
    fail(index) { const c = calls[index]; c.settled = true; c.reject(Error('lost response')); },
    // AccountView keys its subtree by account id and role. Preserve that lifecycle.
    async auth(next) { cleanup(); user = next; render(); render(); await reads(); },
    async close() { cleanup(); for (const c of calls) if (!c.settled) { c.settled = true; c.reject(Error('cleanup')); } for (let i = 0; i < 20; i++) await Promise.resolve(); assert.equal(listeners.size, 0); Object.assign(globalThis, originals); },
  };
}

for (const enabled of [true, false]) for (const mode of enabled ? ['otp', 'recovery_code'] : ['none']) test(`explicit reauthentication and confirmation send ${mode}, clear secrets and refresh both reads`, async () => {
  const h = await host({ enabled });
  try {
    await h.ready(); assert(h.calls.every(c => c.method === 'GET'));
    assert(h.markup().includes('保留当前会话和全部 API 令牌'));
    h.fill(mode); h.acknowledge(false); await h.submit(); assert.equal(h.calls.length, 2);
    assert.equal(h.button('确认撤销其它会话').props.disabled, true);
    h.acknowledge(); h.change('other-sessions-password', ''); await h.submit(); assert.equal(h.calls.length, 2);
    h.change('other-sessions-password', 'password-fixture');
    if (enabled) { h.change('other-sessions-factor', ''); await h.submit(); assert.equal(h.calls.length, 2); h.fill(mode); }
    const sent = h.submit(); await h.flush();
    const c = h.calls[2], key = c.options.params.header['Idempotency-Key'];
    assert.equal(c.path, '/api/v1/auth/sessions/revoke-others'); assert.equal(c.method, 'POST'); assert.equal(c.options.cache, 'no-store');
    assert.deepEqual(c.options.body, { current_password: 'password-fixture', ...(enabled ? { [mode]: mode === 'otp' ? '123456' : 'old-recovery-fixture' } : {}) });
    assert.deepEqual(JSON.parse(h.storage.get(journalKey)), { key, action });
    assert.equal(h.field('other-sessions-password').props.value, '');
    if (enabled) assert.equal(h.field('other-sessions-factor').props.value, '');
    assert.equal(h.field('other-sessions-confirm').props.checked, false);
    h.reply(2, 200, { replayed: false }); await h.flush();
    assert.deepEqual(h.calls.slice(3).map(c => c.path).sort(), ['/api/v1/auth/mfa', '/api/v1/auth/sessions']);
    await h.reads(); await sent; await h.flush();
    assert.equal(h.storage.size, 0); assert(h.markup().includes('不表示之后新登录的会话已被撤销'));
    for (const value of h.writes) assert.deepEqual(Object.keys(JSON.parse(value)).sort(), ['action', 'key']);
    assert.equal(h.calls.filter(c => c.method === 'POST').length, 1);
  } finally { await h.close(); }
});

test('lost POST is GET-only until explicit GET404; current-only list still allows manual original-key retry', async () => {
  const h = await host();
  try {
    await h.ready(); h.fill(); const first = h.submit(); await h.flush(); const key = h.calls[2].options.params.header['Idempotency-Key'];
    h.fail(2); await first; await h.flush();
    h.fill(); await h.submit(); assert.equal(h.calls.length, 3);
    assert.equal(h.button('停用双因素…').props.disabled, true);
    const missing = h.click('读取原安全命令回执'); await h.flush();
    assert.equal(h.calls[3].method, 'GET'); assert.equal(h.calls[3].options.params.path.command_key, key);
    h.reply(3, 404); await missing; await h.flush();
    assert.equal(h.calls.filter(c => c.method === 'POST').length, 1);
    assert.equal(h.form('当前密码（撤销其它会话）').props.children[0].props.disabled, false);
    await h.submit(); assert.equal(h.calls.length, 4); // GET cleared credentials and confirmation.
    h.fill('recovery_code'); const retry = h.submit(); await h.flush();
    assert.equal(h.calls[4].options.params.header['Idempotency-Key'], key);
    h.reply(4, 200, { replayed: true }); await h.flush(); await h.reads(); await retry; await h.flush();
    assert.equal(h.storage.size, 0); assert.equal(h.calls.filter(c => c.method === 'POST').length, 2);
    assert.equal(new Set(h.writes.map(v => JSON.parse(v).key)).size, 1);
  } finally { await h.close(); }
});

test('matching receipt confirms only original operation and refreshes status and sessions without POST', async () => {
  const key = crypto.randomUUID(), h = await host({ pending: { key, action } });
  try {
    await h.ready(); const recovered = h.click('读取原安全命令回执'); await h.flush();
    h.reply(2, 200, { action: receiptAction, result: { replayed: true } }); await h.flush();
    assert.equal(h.calls.length, 5); await h.reads(); await recovered; await h.flush();
    assert.equal(h.storage.size, 0); assert(h.markup().includes('不表示之后新登录的会话已被撤销')); assert(h.calls.every(c => c.method === 'GET'));
  } finally { await h.close(); }
});

for (const [status, data] of [[503, undefined], [403, undefined], [200, { action: 'account.session.revoked' }]]) test(`GET ${status} wrong or unavailable receipt removes transient retry permission`, async () => {
  const key = crypto.randomUUID(), h = await host({ pending: { key, action } });
  try {
    await h.ready(); const missing = h.click('读取原安全命令回执'); await h.flush(); h.reply(2, 404); await missing; await h.flush();
    const denied = h.click('读取原安全命令回执'); await h.flush(); h.reply(3, status, data); await denied; await h.flush();
    h.fill(); await h.submit(); assert(h.calls.every(c => c.method === 'GET')); assert.equal(JSON.parse(h.storage.get(journalKey)).key, key);
    assert.equal(h.form('当前密码（撤销其它会话）').props.children[0].props.disabled, true);
  } finally { await h.close(); }
});

for (const [status, code, cleared] of [[400, 'REAUTHENTICATION_FAILED', true], [429, 'AUTH_RATE_LIMITED', true], [429, 'OTHER', false], [503, 'AUTH_RATE_LIMITED', false]]) test(`initial ${status}/${code} admission handling`, async () => {
  const h = await host();
  try {
    await h.ready(); h.fill(); const sent = h.submit(); await h.flush(); h.reply(2, status, undefined, code); await sent; await h.flush();
    assert.equal(h.storage.has(journalKey), !cleared); assert.equal(h.field('other-sessions-password').props.value, ''); assert.equal(h.field('other-sessions-factor').props.value, '');
    assert.equal(h.field('other-sessions-confirm').props.checked, false);
    if (code === 'REAUTHENTICATION_FAILED') assert(h.markup().includes('当前密码或第二因素不正确'));
    assert.equal(h.calls.filter(c => c.method === 'POST').length, 1);
  } finally { await h.close(); }
});

test('retry 429 preserves original unknown command and requires another GET before retry', async () => {
  const key = crypto.randomUUID(), h = await host({ pending: { key, action } });
  try {
    await h.ready(); const missing = h.click('读取原安全命令回执'); await h.flush(); h.reply(2, 404); await missing; await h.flush();
    h.fill(); const sent = h.submit(); await h.flush(); assert.equal(h.calls[3].options.params.header['Idempotency-Key'], key);
    h.reply(3, 429, undefined, 'AUTH_RATE_LIMITED'); await sent; await h.flush();
    assert.equal(JSON.parse(h.storage.get(journalKey)).key, key); h.fill(); await h.submit(); assert.equal(h.calls.length, 4);
  } finally { await h.close(); }
});

test('shared journal rejects conflicting commands and ignores changed journal during GET and POST', async () => {
  const h = await host({ pending: { key: crypto.randomUUID(), action: 'account.password.change' } });
  try {
    await h.ready(); h.fill(); await h.submit(); assert(h.calls.every(c => c.method === 'GET'));
    const replacement = { key: crypto.randomUUID(), action }; h.journal(replacement);
    const late = h.click('读取原安全命令回执'); await h.flush();
    const next = { key: crypto.randomUUID(), action }; h.journal(next); h.reply(2, 404); await late; await h.flush();
    h.fill(); await h.submit(); assert.equal(h.calls.length, 3);
    const missing = h.click('读取原安全命令回执'); await h.flush(); h.reply(3, 404); await missing; await h.flush();
    h.fill(); const sent = h.submit(); await h.flush();
    const conflict = { key: crypto.randomUUID(), action: 'account.mfa.recovery.regenerate' }; h.journal(conflict);
    h.reply(4, 200, { replayed: false }); await sent; await h.flush();
    assert.deepEqual(JSON.parse(h.storage.get(journalKey)), conflict); assert.equal(h.calls.length, 5); assert(!h.markup().includes('操作已确认'));
  } finally { await h.close(); }
});

for (const next of [{ id: 'account-b', role: 'member' }, { id: 'account-a', role: 'admin' }]) for (const method of ['GET', 'POST']) test(`late ${method} cannot affect remounted ${next.id}/${next.role}`, async () => {
  const h = await host(method === 'GET' ? { pending: { key: crypto.randomUUID(), action } } : {});
  try {
    await h.ready(); let waiting;
    if (method === 'GET') waiting = h.click('读取原安全命令回执');
    else { h.fill(); waiting = h.submit(); }
    await h.flush(); await h.auth(next); const count = h.calls.length;
    h.reply(2, 200, method === 'GET' ? { action: receiptAction, result: { replayed: true } } : { replayed: false }); await waiting; await h.flush();
    assert.equal(h.calls.length, count); assert(h.storage.has(journalKey)); assert(!h.markup().includes('操作已确认'));
    if (h.all(n => n.props.id === 'other-sessions-password').length) assert.equal(h.field('other-sessions-password').props.value, '');
  } finally { await h.close(); }
});

test('cancel, remount and storage events clear secrets or transient retry permission without auto POST', async () => {
  const h = await host();
  try {
    await h.ready(); h.fill(); h.click('取消撤销其它会话'); h.click('撤销其它会话…');
    assert.equal(h.field('other-sessions-password').props.value, ''); assert.equal(h.field('other-sessions-factor').props.value, ''); assert.equal(h.field('other-sessions-confirm').props.checked, false);
    h.fill(); await h.auth({ id: 'account-a', role: 'admin' }); h.click('撤销其它会话…'); assert.equal(h.field('other-sessions-password').props.value, '');
    const pending = { key: crypto.randomUUID(), action }; h.journal(pending);
    let i = h.calls.length, missing = h.click('读取原安全命令回执'); await h.flush(); h.reply(i, 404); await missing; await h.flush();
    assert.equal(h.form('当前密码（撤销其它会话）').props.children[0].props.disabled, false);
    h.journal(pending); h.fill(); await h.submit(); assert(h.calls.every(c => c.method === 'GET'));
    i = h.calls.length; missing = h.click('读取原安全命令回执'); await h.flush(); h.reply(i, 404); await missing; await h.flush();
    await h.auth({ id: 'account-a', role: 'admin' }); h.fill(); await h.submit(); assert(h.calls.every(c => c.method === 'GET'));
  } finally { await h.close(); }
});