import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import ts from 'typescript';
import React from 'react';

async function host() {
  const scopes = new Map(), calls = [];
  let user = { id: 'owner', role: 'member' }, props = { operationId: 'operation-a', contextReady: true }, current, tree, effects = [], closed = false;
  const equal = (a, b) => a && b && a.length === b.length && a.every((v, i) => Object.is(v, b[i]));
  const h = {
    React, useAuth: () => ({ user }),
    api: { POST: (path, options) => new Promise((resolve, reject) => calls.push({ path, options, resolve, reject, settled: false })) },
    useRef(value) { const i = current.cursor++; return current.slots[i] ??= { current: value }; },
    useState(value) { const scope = current, i = scope.cursor++; if (!(i in scope.slots)) scope.slots[i] = value; return [scope.slots[i], value => { if (!scope.active) throw Error('state update after unmount'); scope.slots[i] = typeof value === 'function' ? value(scope.slots[i]) : value; }]; },
    useEffect(fn, deps) { const scope = current, i = scope.cursor++, old = scope.slots[i]; if (!equal(old?.deps, deps)) effects.push(() => { old?.cleanup?.(); scope.slots[i] = { deps, cleanup: fn() }; }); },
  };
  const source = readFileSync(new URL('../src/features/configuration/OperationSeedingInspection.tsx', import.meta.url), 'utf8');
  const ast = ts.createSourceFile('component.tsx', source, ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX);
  let clean = source;
  for (const node of [...ast.statements.filter(ts.isImportDeclaration)].reverse()) clean = clean.slice(0, node.getFullStart()) + clean.slice(node.end);
  const name = `__inspection_${crypto.randomUUID().replaceAll('-', '')}`;
  globalThis[name] = h;
  let component;
  try {
    const compiled = ts.transpileModule(`const {${Object.keys(h).join(',')}}=globalThis[${JSON.stringify(name)}];\n${clean}`, { compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.ESNext, jsx: ts.JsxEmit.React } }).outputText;
    component = (await import(`data:text/javascript;base64,${Buffer.from(compiled).toString('base64')}`)).OperationSeedingInspection;
  } finally { delete globalThis[name]; }
  const cleanup = scope => { scope.active = false; for (const slot of scope.slots) slot?.cleanup?.(); };
  function render() {
    if (closed) return;
    const used = new Set();
    function visit(node, path) {
      if (Array.isArray(node)) return node.map((child, index) => visit(child, `${path}/${index}`));
      if (!node || typeof node !== 'object' || !node.props) return node;
      if (typeof node.type === 'function') {
        const key = `${path}:${node.type.name}:${node.key ?? ''}`;
        let scope = scopes.get(key);
        if (!scope) { scope = { slots: [], cursor: 0, active: true }; scopes.set(key, scope); }
        scope.cursor = 0; used.add(key); current = scope;
        return visit(node.type(node.props), key);
      }
      return { type: node.type, props: { ...node.props, children: visit(node.props.children, path + '/children') } };
    }
    tree = visit(React.createElement(component, props), 'root');
    for (const [key, scope] of scopes) if (!used.has(key)) { cleanup(scope); scopes.delete(key); }
    for (const effect of effects.splice(0)) effect();
  }
  const flatten = node => Array.isArray(node) ? node.flatMap(flatten) : node && typeof node === 'object' && node.props ? [node, ...flatten(node.props.children)] : [];
  const text = node => Array.isArray(node) ? node.map(text).join(' ') : node && typeof node === 'object' && node.props ? text(node.props.children) : String(node ?? '');
  const flush = async () => { for (let i = 0; i < 12; i++) await Promise.resolve(); render(); };
  return {
    calls, render, flush, get tree() { return tree; }, text: () => text(tree),
    button: () => flatten(tree).find(node => node.type === 'button'),
    find: predicate => flatten(tree).find(predicate),
    auth(next) { user = next; render(); }, props(next) { props = { ...props, ...next }; render(); },
    reply(index, data, error = false) { calls[index].settled = true; calls[index].resolve({ data, error: error ? { message: 'unsafe secret' } : undefined }); },
    fail(index) { calls[index].settled = true; calls[index].reject(Error('unsafe secret')); },
    async close() { for (const scope of scopes.values()) cleanup(scope); closed = true; for (const call of calls) if (!call.settled) { call.settled = true; call.reject(Error('cleanup')); } await flush(); },
  };
}
const facts = { observed_at: '2026-10-27T00:00:00Z', complete: true, share_ratio: null, infinite_share_ratio: false, seeding_seconds: null };

test('seeding inspection is explicit, blocks duplicate clicks, preserves unknown/zero/infinite and exact seconds', async () => {
  const h = await host();
  try {
    h.render(); assert.equal(h.calls.length, 0);
    h.button().props.onClick(); h.button().props.onClick(); await h.flush();
    assert.equal(h.calls.length, 1); assert.equal(h.calls[0].options.cache, 'no-store'); assert.deepEqual(h.calls[0].options.body, {}); assert.equal(h.calls[0].options.params.header, undefined);
    assert(h.find(node => node.type === 'fieldset').props.disabled);
    h.reply(0, facts); await h.flush(); assert.equal(h.find(node => node.type === 'dl').props.children.filter(node => node?.type === 'dd' && node.props.children === '未知').length, 2);
    h.button().props.onClick(); h.reply(1, { ...facts, share_ratio: 0, seeding_seconds: '0' }); await h.flush(); assert(h.text().includes('0 秒')); assert(h.find(node => node.type === 'dd' && node.props.children === '0'));
    h.button().props.onClick(); h.reply(2, { ...facts, infinite_share_ratio: true, seeding_seconds: '9223372036854775807' }); await h.flush(); assert(h.text().includes('无限')); assert(h.text().includes('9223372036854775807 秒'));
    h.render(); await h.flush(); assert.equal(h.calls.length, 3);
  } finally { await h.close(); }
});

test('seeding inspection clears previous facts on failure and rejects malformed native facts', async () => {
  const h = await host();
  try {
    h.render(); h.button().props.onClick(); h.reply(0, facts); await h.flush(); assert(h.find(node => node.type === 'dl'));
    h.button().props.onClick(); h.render(); assert.equal(h.find(node => node.type === 'dl'), undefined);
    h.fail(1); await h.flush(); assert(h.find(node => node.props.role === 'alert')); assert(!h.text().includes('unsafe secret'));
    for (const bad of [{ ...facts, share_ratio: -1 }, { ...facts, share_ratio: 1, infinite_share_ratio: true }, { ...facts, seeding_seconds: 0 }, { ...facts, seeding_seconds: '01' }, { ...facts, observed_at: 'bad' }]) {
      h.button().props.onClick(); h.reply(h.calls.length - 1, bad); await h.flush(); assert.equal(h.find(node => node.type === 'dl'), undefined); assert(h.find(node => node.props.role === 'alert'));
    }
  } finally { await h.close(); }
});

test('seeding inspection cancels and ignores late account, role, operation, context and unmount results', async () => {
  const h = await host();
  try {
    h.render();
    for (const change of [() => h.auth({ id: 'other', role: 'member' }), () => h.auth({ id: 'other', role: 'admin' }), () => h.props({ operationId: 'operation-b' }), () => h.props({ contextReady: false })]) {
      h.button().props.onClick(); const index = h.calls.length - 1; change();
      assert(h.calls[index].options.signal.aborted); h.reply(index, facts); await h.flush(); assert.equal(h.find(node => node.type === 'dl'), undefined);
    }
    h.props({ contextReady: true }); await h.flush(); assert.equal(h.calls.length, 4);
    h.button().props.onClick(); const last = h.calls.length - 1; h.auth(null); assert.equal(h.tree, null); assert(h.calls[last].options.signal.aborted);
    h.reply(last, facts); await h.flush(); assert.equal(h.tree, null);
  } finally { await h.close(); }
});
