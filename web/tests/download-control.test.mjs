import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import ts from 'typescript';
import React from 'react';

async function host() {
  const calls = [], storage = new Map(), scopes = new Map();
  let user = {id: '10000000-0000-4000-8000-000000000001', role: 'member'}, props = {operationId: '20000000-0000-4000-8000-000000000002', contextReady: true}, current, tree, effects = [], closed = false;
  const descriptors = new Map(['navigator', 'localStorage'].map(key => [key, Object.getOwnPropertyDescriptor(globalThis, key)]));
  Object.defineProperty(globalThis, 'navigator', {configurable: true, value: {locks: {request: async (_key, _options, action) => action()}}});
  Object.defineProperty(globalThis, 'localStorage', {configurable: true, value: {getItem: key => storage.get(key) ?? null, setItem: (key, value) => storage.set(key, value), removeItem: key => storage.delete(key)}});
  const equal = (a, b) => a && b && a.length === b.length && a.every((v, i) => Object.is(v, b[i]));
  const request = method => (path, options) => new Promise((resolve, reject) => calls.push({method, path, options, resolve, reject, settled: false}));
  const h = {
    React, useAuth: () => ({user}), api: {GET: request('GET'), POST: request('POST')},
    useRef(value) { const i = current.cursor++; return current.slots[i] ??= {current: value}; },
    useState(value) { const scope = current, i = scope.cursor++; if (!(i in scope.slots)) scope.slots[i] = value; return [scope.slots[i], value => {if (!scope.active) throw Error('state update after unmount'); scope.slots[i] = typeof value === 'function' ? value(scope.slots[i]) : value;}]; },
    useEffect(fn, deps) { const scope = current, i = scope.cursor++, old = scope.slots[i]; if (!equal(old?.deps, deps)) effects.push(() => {old?.cleanup?.(); scope.slots[i] = {deps, cleanup: fn()};}); },
  };
  const source = readFileSync(new URL('../src/features/configuration/OperationDownloadControl.tsx', import.meta.url), 'utf8');
  const ast = ts.createSourceFile('control.tsx', source, ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX);
  let clean = source;
  for (const node of [...ast.statements.filter(ts.isImportDeclaration)].reverse()) clean = clean.slice(0, node.getFullStart()) + clean.slice(node.end);
  const name = `__control_${crypto.randomUUID().replaceAll('-', '')}`;
  globalThis[name] = h;
  let component;
  try {
    const compiled = ts.transpileModule(`const {${Object.keys(h).join(',')}}=globalThis[${JSON.stringify(name)}];\n${clean}`, {compilerOptions: {target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.ESNext, jsx: ts.JsxEmit.React}}).outputText;
    component = (await import(`data:text/javascript;base64,${Buffer.from(compiled).toString('base64')}`)).OperationDownloadControl;
  } finally {delete globalThis[name];}
  const cleanup = scope => {scope.active = false; for (const slot of scope.slots) slot?.cleanup?.();};
  function render() {
    if (closed) return;
    const used = new Set();
    function visit(node, path) {
      if (Array.isArray(node)) return node.map((child, i) => visit(child, `${path}/${i}`));
      if (!node || typeof node !== 'object' || !node.props) return node;
      if (typeof node.type === 'function') {
        const key = `${path}:${node.type.name}:${node.key ?? ''}`;
        let scope = scopes.get(key); if (!scope) {scope = {slots: [], cursor: 0, active: true}; scopes.set(key, scope);}
        current = scope; scope.cursor = 0; used.add(key); return visit(node.type(node.props), key);
      }
      return {type: node.type, props: {...node.props, children: visit(node.props.children, path + '/child')}};
    }
    tree = visit(React.createElement(component, props), 'root');
    for (const [key, scope] of scopes) if (!used.has(key)) {cleanup(scope); scopes.delete(key);}
    const pending = effects; effects = []; pending.forEach(effect => effect());
  }
  const flatten = node => Array.isArray(node) ? node.flatMap(flatten) : node && typeof node === 'object' && node.props ? [node, ...flatten(node.props.children)] : [];
  const text = node => Array.isArray(node) ? node.map(text).join('') : node && typeof node === 'object' && node.props ? text(node.props.children) : node ?? '';
  const flush = async () => {for (let i = 0; i < 8; i++) {await Promise.resolve(); render();}};
  return {
    calls, storage, render, flush, text: () => text(tree), find: predicate => flatten(tree).find(predicate),
    button(label) {return flatten(tree).find(node => node.type === 'button' && text(node).includes(label));},
    auth(next) {user = next; render();}, props(next) {props = {...props, ...next}; render();},
    reply(index, data) {calls[index].settled = true; calls[index].resolve({data});},
    fail(index) {calls[index].settled = true; calls[index].reject(Error('private native credential'));},
    async close() {for (const scope of scopes.values()) cleanup(scope); closed = true; for (const call of calls) if (!call.settled) {call.settled = true; call.reject(Error('cleanup'));} await flush(); for (const [key, descriptor] of descriptors) {if (descriptor) Object.defineProperty(globalThis, key, descriptor); else delete globalThis[key];}},
  };
}
const receipt = {command_id: '30000000-0000-4000-8000-000000000003', operation_id: '20000000-0000-4000-8000-000000000002', action: 'pause', state: 'unknown'};

test('dedicated control explains tracking scope and retains unknown command without retrying native send', async () => {
  const h = await host();
  try {
    h.render(); await h.flush();
    assert(h.text().includes('自动剧集、整季包和电影升级'));
    assert.equal(h.calls.length, 0);
    h.find(node => node.type === 'input' && node.props.type === 'checkbox').props.onChange({target: {checked: true}}); h.render();
    const send = h.button('确认暂停'); assert.equal(send.props.disabled, false);
    send.props.onClick(); send.props.onClick(); await h.flush();
    assert.equal(h.calls.length, 1); assert.equal(h.calls[0].method, 'POST'); assert.equal(h.storage.size, 1);
    const key = h.calls[0].options.params.header['Idempotency-Key'];
    h.fail(0); await h.flush(); assert(h.text().includes('读取原回执')); assert(!h.text().includes('private native credential'));
    assert.equal(h.button('确认暂停'), undefined);
    h.button('读取原回执').props.onClick(); await h.flush();
    assert.equal(h.calls[1].method, 'GET'); assert.equal(h.calls[1].options.params.header['Idempotency-Key'], key);
    h.reply(1, receipt); await h.flush();
    h.button('核对下载器当前状态').props.onClick(); await h.flush();
    assert.equal(h.calls[2].method, 'POST'); assert(h.calls[2].path.endsWith('/observations')); assert.equal(h.calls[2].options.body, undefined); assert.equal(h.calls[2].options.params.header['Idempotency-Key'], key);
    h.reply(2, {...receipt, state: 'confirmed'}); await h.flush();
    assert(h.text().includes('已确认')); assert.equal(h.calls.filter(call => call.path.endsWith('/download-control') && call.method === 'POST').length, 1);
    h.button('准备下一条控制命令').props.onClick(); await h.flush(); assert.equal(h.storage.size, 0); assert.equal(h.calls.length, 3);
  } finally {await h.close();}
});

test('control requires refreshed context and clears account-specific display on account change', async () => {
  const h = await host();
  try {
    h.props({contextReady: false}); await h.flush();
    assert(h.button('确认暂停').props.disabled);
    assert(h.find(node => node.type === 'input' && node.props.type === 'checkbox').props.disabled);
    h.auth({id: '40000000-0000-4000-8000-000000000004', role: 'admin'}); await h.flush();
    assert(h.text().includes('允许影响此任务的其他共享需求')); assert.equal(h.calls.length, 0);
    h.auth(null); await h.flush(); assert.equal(h.text(), '');
  } finally {await h.close();}
});
