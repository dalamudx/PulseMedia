import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import ts from 'typescript';
import React from 'react';
import { renderToStaticMarkup } from 'react-dom/server';

const root = new URL('../src/features/configuration/', import.meta.url);
const compile = text => ts.transpileModule(text, { compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.ESNext, jsx: ts.JsxEmit.React } }).outputText;
const stripImports = source => {
  const ast = ts.createSourceFile('source.tsx', source, ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX);
  for (const node of [...ast.statements.filter(ts.isImportDeclaration)].reverse()) source = source.slice(0, node.getFullStart()) + source.slice(node.end);
  return source;
};
async function load(file, host) {
  const key = `__endpoint_${crypto.randomUUID().replaceAll('-', '')}`;
  globalThis[key] = host;
  try {
    const source = `const {${Object.keys(host).join(',')}}=globalThis[${JSON.stringify(key)}];\n` + stripImports(readFileSync(new URL(file, root), 'utf8'));
    return await import(`data:text/javascript;base64,${Buffer.from(compile(source)).toString('base64')}`);
  } finally { delete globalThis[key]; }
}
const account = { id: 'account-a', role: 'admin' };
const version = '2026-10-19T10:00:00.123456Z', nextVersion = '2026-10-19T10:01:00.123456Z';
const row = (family = 'download_client', patch = {}) => ({ id: 'endpoint-1', name: '本地服务', url: 'http://127.0.0.1:8080/path?token=***', updated_at: version, ...(family === 'download_client' ? { client_type: 'qbittorrent', username: 'admin' } : { server_type: 'jellyfin', health: 'unknown' }), ...patch });
const response = (status, data) => ({ response: { status }, data, error: status >= 400 ? { message: 'UNSAFE remote secret' } : undefined });

// Real transpiled hook, deterministic React slots/effects, and deferred API transport.
async function hookHost(family = 'download_client', initialUser = account) {
  const slots = [], effects = [], calls = [];
  let cursor = 0, out, user = initialUser;
  const equal = (a, b) => a && b && a.length === b.length && a.every((v, i) => Object.is(v, b[i]));
  const transport = method => (path, options) => new Promise((resolve, reject) => calls.push({ method, path, options, resolve, reject, settled: false }));
  const host = {
    api: { GET: transport('GET'), POST: transport('POST'), PUT: transport('PUT'), DELETE: transport('DELETE') },
    useAuth: () => ({ user }),
    useRef(value) { const i = cursor++; return slots[i] ??= { current: value }; },
    useState(value) { const i = cursor++; if (!(i in slots)) slots[i] = typeof value === 'function' ? value() : value; return [slots[i], next => { slots[i] = typeof next === 'function' ? next(slots[i]) : next; }]; },
    useEffect(fn, deps) { const i = cursor++, old = slots[i]; if (!equal(old?.deps, deps)) effects.push(() => { old?.cleanup?.(); slots[i] = { deps, cleanup: fn() }; }); },
  };
  const { useEndpointConfiguration } = await load('useEndpointConfiguration.ts', host);
  const render = () => { cursor = 0; out = useEndpointConfiguration(family); for (const effect of effects.splice(0)) effect(); return out; };
  const flush = async () => { for (let i = 0; i < 16; i++) await Promise.resolve(); return render(); };
  const h = {
    calls, flush, get out() { return out; },
    auth(value) { user = value; return render(); },
    family(value) { family = value; return render(); },
    reply(status, data, index = calls.findIndex(c => !c.settled)) { const call = calls[index]; assert(call && !call.settled, 'owned pending request'); call.settled = true; call.resolve(response(status, data)); },
    fail(index = calls.findIndex(c => !c.settled)) { const call = calls[index]; assert(call && !call.settled); call.settled = true; call.reject(Error('connection lost with SECRET')); },
    async ready(items = [row(family)]) { h.reply(200, { items }); await flush(); },
    async close() { for (const slot of slots) slot?.cleanup?.(); for (const call of calls) if (!call.settled) { call.settled = true; call.reject(Error('test cleanup')); } await flush(); },
  };
  render(); await flush();
  return h;
}
const fillCreate = h => { h.out.change('name', '新配置'); h.out.change('url', 'http://localhost:8096'); };
const writes = h => h.calls.filter(c => c.method === 'PUT' || c.method === 'DELETE' || c.method === 'POST' && !c.path.endsWith('/test'));

for (const family of ['download_client', 'media_server']) {
  test(`${family}: actual create/edit bodies, empty credentials, safe URL and captured microsecond version`, async () => {
    const h = await hookHost(family);
    try {
      await h.ready([row(family, { password: 'NEVER_COPY', api_key: 'NEVER_COPY' })]);
      h.out.startEdit('endpoint-1'); await h.flush();
      assert.equal(h.out.draft.password, ''); assert.equal(h.out.draft.api_key, '');
      assert(!JSON.stringify(h.out).includes('NEVER_COPY'));
      h.out.change('name', '已修改'); if (family === 'download_client') h.out.change('username', '');
      const send = h.out.save(), call = h.calls.at(-1);
      assert.equal(call.method, 'PUT');
      assert.equal(call.path, family === 'download_client' ? '/api/v1/download-clients/{id}' : '/api/v1/media-servers/{id}');
      const expected = { name: '已修改', url: row(family).url, expected_updated_at: version, ...(family === 'download_client' ? { client_type: 'qbittorrent', username: '', password: '' } : { server_type: 'jellyfin', api_key: '' }) };
      assert.deepEqual(call.options.body, expected);
      h.reply(200, row(family, { name: '已修改', updated_at: nextVersion })); await h.flush();
      assert.equal(h.out.gate, 'saved'); assert.equal(h.out.busy, 'save'); assert.equal(h.out.blocked, true); assert.equal(h.out.items.length, 0);
      await h.out.save(); await h.out.remove('endpoint-1'); await h.out.probe('endpoint-1'); assert.equal(writes(h).length, 1);
      h.reply(200, { items: [row(family, { name: '已修改', updated_at: nextVersion })] }); assert.equal(await send, true); await h.flush();
      assert.equal(h.out.blocked, false); assert.equal(h.out.editing, null);
      fillCreate(h); h.out.change('type', family === 'download_client' ? 'transmission' : 'emby');
      const create = h.out.save(), creation = h.calls.at(-1);
      assert.equal(creation.method, 'POST');
      assert.deepEqual(creation.options.body, { name: '新配置', url: 'http://localhost:8096', ...(family === 'download_client' ? { client_type: 'transmission', username: '', password: '' } : { server_type: 'emby', api_key: '' }) });
      h.reply(201, row(family)); await h.flush(); h.reply(200, { items: [row(family)] }); await create;
    } finally { await h.close(); }
  });

  test(`${family}: 409 preserves draft/version, ordinary GET cannot resolve; explicit reread clears secret`, async () => {
    const h = await hookHost(family);
    try {
      await h.ready(); h.out.startEdit('endpoint-1'); h.out.change('name', '我的草稿'); h.out.change(family === 'download_client' ? 'password' : 'api_key', 'entered-secret');
      const send = h.out.save(); h.reply(409); assert.equal(await send, false); await h.flush();
      assert.equal(h.out.gate, 'conflict'); assert.equal(h.out.draft.name, '我的草稿'); assert.equal(h.out.editing.version, version);
      const read = h.out.refresh(); h.reply(200, { items: [row(family, { updated_at: nextVersion, name: '另一修改' })] }); await read; await h.flush();
      assert.equal(h.out.editing.version, version); assert.equal(h.out.draft.name, '我的草稿'); assert.equal(h.out.blocked, true);
      await h.out.save(); assert.equal(writes(h).length, 1);
      const failed = h.out.reread(); h.reply(503); assert.equal(await failed, false); await h.flush();
      assert.equal(h.out.draft.name, '我的草稿'); assert.equal(h.out.editing.version, version);
      const discard = h.out.reread(); h.reply(200, { items: [row(family, { updated_at: nextVersion, name: '另一修改' })] }); await discard; await h.flush();
      assert.equal(h.out.draft.name, '另一修改'); assert.equal(h.out.editing.version, nextVersion); assert.equal(h.out.draft.password, ''); assert.equal(h.out.draft.api_key, ''); assert.equal(h.out.blocked, false);
      const save = h.out.save(); assert.equal(h.calls.at(-1).options.body.expected_updated_at, nextVersion); h.reply(400); await save;
    } finally { await h.close(); }
  });

  for (const failure of [503, 'network']) test(`${family}: acknowledged save + failed refresh stays saved and locked; no duplicate write (${failure})`, async () => {
    const h = await hookHost(family);
    try {
      await h.ready(); fillCreate(h);
      const save = h.out.save(); assert.equal(await h.out.save(), false); h.reply(201, row(family)); await h.flush();
      assert.equal(h.out.busy, 'save'); assert.equal(h.out.blocked, true);
      if (failure === 'network') h.fail(); else h.reply(failure);
      assert.equal(await save, true); await h.flush();
      assert.equal(h.out.gate, 'saved'); assert.equal(h.out.listStatus, 'error'); assert.match(h.out.message, /已保存.*刷新失败/);
      await h.out.save(); await h.out.remove('endpoint-1'); assert.equal(writes(h).length, 1);
      const read = h.out.refresh(); h.reply(200, { items: [row(family)] }); await read; await h.flush(); assert.equal(h.out.blocked, false); assert.equal(writes(h).length, 1);
    } finally { await h.close(); }
  });

  for (const mutation of ['create', 'edit', 'delete']) test(`${family}: lost ${mutation} result never replays; manual fresh list required`, async () => {
    const h = await hookHost(family);
    try {
      await h.ready(); if (mutation === 'create') fillCreate(h); else h.out.startEdit('endpoint-1');
      const task = mutation === 'delete' ? h.out.remove('endpoint-1') : h.out.save(); h.fail(); assert.equal(await task, false); await h.flush();
      assert.equal(h.out.gate, 'unknown'); assert.match(h.out.message, /结果未知/);
      await h.out.save(); await h.out.remove('endpoint-1'); await h.out.probe('endpoint-1'); await h.flush(); assert.equal(h.calls.length, 2);
      const fail = h.out.refresh(); h.fail(); await fail; await h.flush(); assert.equal(h.out.gate, 'unknown'); assert.equal(h.out.items.length, 0);
      const verify = h.out.refresh(); h.reply(200, { items: mutation === 'delete' ? [] : [row(family)] }); await verify; await h.flush();
      assert.equal(h.out.gate, 'none'); assert.equal(h.out.draft.password, ''); assert.equal(h.out.draft.api_key, ''); assert.equal(writes(h).length, 1);
      assert.match(h.out.message, /上次操作结果仍未确认/);
    } finally { await h.close(); }
  });
}

for (const failure of [403, 404, 503, 'network', 'malformed']) test(`list failure ${failure}: never an empty success; old items hidden, form blocked, explicit recovery`, async () => {
  const h = await hookHost();
  try {
    if (typeof failure === 'number') h.reply(failure); else if (failure === 'network') h.fail(); else h.reply(200, {});
    await h.flush(); assert.equal(h.out.listStatus, 'error'); assert.equal(h.out.items.length, 0); assert.equal(h.out.blocked, true); assert.match(h.out.message, /读取失败/); assert(!h.out.message.includes('UNSAFE'));
    await h.out.save(); assert.equal(writes(h).length, 0);
    const recover = h.out.refresh(); h.reply(200, { items: [row()] }); await recover; await h.flush();
    h.out.startEdit('endpoint-1'); const read = h.out.refresh(); await h.flush(); assert.equal(h.out.items.length, 0); assert.equal(h.out.blocked, true);
    h.reply(503); await read; await h.flush(); assert.equal(h.out.items.length, 0); assert.equal(h.out.blocked, true);
  } finally { await h.close(); }
});

test('only a valid fresh empty list exposes empty state and enables create', async () => {
  const h = await hookHost();
  try { await h.ready([]); assert.equal(h.out.listStatus, 'ready'); assert.equal(h.out.items.length, 0); assert.equal(h.out.blocked, false); }
  finally { await h.close(); }
});

for (const failure of [false, 403, 404, 503, 'network', 'malformed']) test(`probe ${failure}: native false is offline, read/transport failures are errors`, async () => {
  const h = await hookHost();
  try {
    await h.ready(); const probe = h.out.probe('endpoint-1');
    assert.equal(h.calls.at(-1).path, '/api/v1/download-clients/{id}/test');
    await h.out.probe('endpoint-1'); await h.out.remove('endpoint-1'); assert.equal(h.calls.length, 2);
    if (failure === false) h.reply(200, { alive: false, message: 'native false' }); else if (typeof failure === 'number') h.reply(failure); else if (failure === 'network') h.fail(); else h.reply(200, {});
    await probe; await h.flush(); assert.equal(h.out.probes['endpoint-1'].kind, failure === false ? 'offline' : 'error'); assert(!h.out.probes['endpoint-1'].message.includes('UNSAFE'));
  } finally { await h.close(); }
});

test('probe cleared after edit and fresh list version change; normal GET never rebases draft', async () => {
  const h = await hookHost();
  try {
    await h.ready(); const probe = h.out.probe('endpoint-1'); h.reply(200, { alive: true }); await probe; await h.flush(); assert.equal(h.out.probes['endpoint-1'].kind, 'online');
    h.out.startEdit('endpoint-1'); h.out.change('name', '草稿');
    const read = h.out.refresh(); h.reply(200, { items: [row()] }); await read; await h.flush();
    assert.equal(h.out.probes['endpoint-1'].kind, 'online'); assert.equal(h.out.editing.version, version);
    const changed = h.out.refresh(); h.reply(200, { items: [row('download_client', { updated_at: nextVersion })] }); await changed; await h.flush();
    assert.deepEqual(h.out.probes, {}); assert.equal(h.out.gate, 'conflict'); assert.equal(h.out.editing.version, version); assert.equal(h.out.draft.name, '草稿');
    const reread = h.out.reread(); h.reply(200, { items: [row('download_client', { updated_at: nextVersion })] }); await reread;
    const p = h.out.probe('endpoint-1'); h.reply(200, { alive: true }); await p;
    const save = h.out.save(); h.reply(200, row()); await h.flush(); assert.deepEqual(h.out.probes, {});
    h.reply(200, { items: [row('download_client', { updated_at: nextVersion })] }); await save;
  } finally { await h.close(); }
});

for (const switchKind of ['account', 'role', 'accountABA', 'roleABA', 'family']) for (const stage of ['list', 'save', 'refresh', 'probe', 'delete']) test(`${switchKind} identity fence during ${stage}: immediate mask, abort, late success/failure ignored`, async () => {
  for (const lateFailure of [false, true]) {
    const h = await hookHost();
    try {
      let task;
      if (stage !== 'list') {
        await h.ready(); h.out.startEdit('endpoint-1'); h.out.change('password', 'private draft');
        if (stage === 'save' || stage === 'refresh') task = h.out.save();
        else if (stage === 'delete') task = h.out.remove('endpoint-1');
        else task = h.out.probe('endpoint-1');
        if (stage === 'refresh') { h.reply(200, row()); await h.flush(); }
      }
      const oldIndex = h.calls.length - 1, old = h.calls[oldIndex], staleMethods = h.out;
      const masked = switchKind === 'family' ? h.family('media_server') : h.auth(switchKind === 'role' || switchKind === 'roleABA' ? { ...account, role: 'member' } : { id: 'account-b', role: 'admin' });
      assert.deepEqual(masked.items, []); assert.equal(masked.draft.password, ''); assert.equal(masked.editing, null); assert.deepEqual(masked.probes, {}); assert.equal(old.options.signal.aborted, true);
      if (switchKind === 'accountABA' || switchKind === 'roleABA') h.auth(account);
      assert.equal(await staleMethods.save(), false);
      if (lateFailure) h.fail(oldIndex); else h.reply(200, stage === 'probe' ? { alive: true } : stage === 'save' ? row() : { items: [row()] }, oldIndex);
      if (task) await task; await h.flush();
      assert.equal(h.out.draft.password, ''); assert.equal(h.out.editing, null); assert.equal(h.out.message, ''); assert.deepEqual(h.out.probes, {});
      assert.equal(h.calls.filter(c => c.method !== 'GET').length, stage === 'list' ? 0 : 1);
      if (switchKind === 'role') { assert.equal(h.out.isAdmin, false); assert.equal(h.calls.length, oldIndex + 1); }
    } finally { await h.close(); }
  }
});

test('unmount aborts owned request and settles without follow-up reads or state publication', async () => {
  const h = await hookHost(); await h.ready(); fillCreate(h); const send = h.out.save(), call = h.calls.at(-1);
  await h.close(); assert.equal(call.options.signal.aborted, true); assert.equal(await send, false); assert.equal(h.calls.length, 2);
});

test('changed placeholder URL rejected; explicit local URL allowed; undeclared protocol never sent', async () => {
  const h = await hookHost();
  try {
    await h.ready(); h.out.startEdit('endpoint-1'); h.out.change('url', 'http://localhost/new?token=***'); assert.equal(await h.out.save(), false); assert.equal(writes(h).length, 0);
    h.out.change('url', 'http://localhost/new?token=%2A%2A%2A'); assert.equal(await h.out.save(), false);
    h.out.change('url', 'file:///secret'); assert.equal(await h.out.save(), false);
    h.out.change('url', 'http://192.168.1.10:9091'); h.out.change('type', 'future-protocol'); assert.equal(await h.out.save(), false);
    h.out.change('type', 'transmission'); const save = h.out.save(); assert.equal(h.calls.at(-1).options.body.url, 'http://192.168.1.10:9091'); h.reply(400); await save;
  } finally { await h.close(); }
});

test('legacy Plex stays displayed and editable without advertising new Plex creation', async () => {
  const h = await hookHost('media_server');
  try {
    await h.ready([row('media_server', { server_type: 'plex' })]); h.out.startEdit('endpoint-1'); await h.flush(); assert.equal(h.out.draft.type, 'plex');
    const edit = h.out.save(); assert.equal(h.calls.at(-1).options.body.server_type, 'plex'); h.reply(400); await edit;
    h.out.cancelEdit(); fillCreate(h); h.out.change('type', 'plex'); assert.equal(await h.out.save(), false); assert.equal(writes(h).length, 1);
  } finally { await h.close(); }
});

test('delete acknowledgement refreshes, rejects duplicate commands, and clears deleted edit', async () => {
  const h = await hookHost('media_server');
  try {
    await h.ready(); h.out.startEdit('endpoint-1'); const remove = h.out.remove('endpoint-1');
    assert.equal(h.calls.at(-1).method, 'DELETE'); assert.equal(h.calls.at(-1).path, '/api/v1/media-servers/{id}');
    assert.equal(await h.out.remove('endpoint-1'), false); h.reply(204); await h.flush(); assert.equal(h.out.blocked, true);
    h.reply(200, { items: [] }); assert.equal(await remove, true); await h.flush(); assert.equal(h.out.editing, null); assert.equal(h.out.listStatus, 'ready');
  } finally { await h.close(); }
});

test('panel uses existing layout, explicit labels, safe secret input, distinct errors and member visibility', async () => {
  const h = await hookHost();
  try {
    h.reply(503); await h.flush();
    const panel = await load('EndpointConfigurationPanel.tsx', { React, useEndpointConfiguration: () => h.out });
    const html = () => renderToStaticMarkup(React.createElement(panel.EndpointConfigurationPanel, { family: 'download_client' }));
    assert(!html().includes('暂无配置')); assert(html().includes('读取失败')); assert(html().includes('disabled=""'));
    const read = h.out.refresh(); h.reply(200, { items: [row()] }); await read; await h.flush();
    h.out.startEdit('endpoint-1'); await h.flush(); const rendered = html();
    for (const value of ['integration-row', 'form-grid', 'config-actions', 'Web UI 地址', '留空保留已有值', 'type="password"', 'autoComplete="new-password"']) assert(rendered.includes(value), value);
    assert(rendered.includes('for="endpoint-download_client-url"')); assert(!rendered.includes('required="" autoComplete="new-password"'));
    h.auth({ ...account, role: 'member' }); await h.flush(); assert(!html().includes('<form')); assert(!html().includes('127.0.0.1'));
  } finally { await h.close(); }
});

test('no secret/URL persistence, automatic mutation retry, timer, or generic protocol cast', () => {
  const source = readFileSync(new URL('useEndpointConfiguration.ts', root), 'utf8');
  for (const forbidden of ['localStorage', 'sessionStorage', 'setTimeout', 'setInterval', 'as "qbittorrent"', 'as "jellyfin"']) assert(!source.includes(forbidden), forbidden);
});

for (const family of ['download_client', 'media_server']) for (const status of [500, 503, 200]) test(`${family}: unconfirmed mutation HTTP ${status} requires fresh verification and never auto-retries`, async () => {
  const h = await hookHost(family);
  try {
    await h.ready(); fillCreate(h); const save = h.out.save(); h.reply(status, row(family)); assert.equal(await save, false); await h.flush();
    assert.equal(h.out.gate, 'unknown'); await h.out.save(); await h.out.remove('endpoint-1'); assert.equal(writes(h).length, 1);
    const read = h.out.refresh(); h.reply(200, { items: [row(family)] }); await read; await h.flush(); assert.equal(h.out.gate, 'none'); assert.equal(writes(h).length, 1);
  } finally { await h.close(); }
});

for (const status of [401, 403, 404]) test(`mutation ${status} blocks commands until new successful read`, async () => {
  const h = await hookHost();
  try {
    await h.ready(); h.out.startEdit('endpoint-1'); const save = h.out.save(); h.reply(status); await save; await h.flush();
    assert.equal(h.out.gate, 'denied'); assert.equal(h.out.blocked, true); assert(!h.out.message.includes('UNSAFE')); await h.out.save(); assert.equal(writes(h).length, 1);
    const read = h.out.refresh(); h.reply(200, { items: [row()] }); await read; await h.flush(); assert.equal(h.out.gate, 'none'); assert.equal(h.out.editing.version, version);
  } finally { await h.close(); }
});

test('member and anonymous initial views send no configuration requests and expose no admin form', async () => {
  for (const user of [null, { ...account, role: 'member' }]) {
    const h = await hookHost('download_client', user);
    try { assert.equal(h.calls.length, 0); assert.equal(h.out.isAdmin, false); assert.equal(h.out.blocked, true); assert.equal(await h.out.refresh(), false); assert.equal(await h.out.save(), false); assert.equal(h.calls.length, 0); }
    finally { await h.close(); }
  }
});

test('legacy unknown protocol is displayed without silently selecting a supported protocol', async () => {
  const h = await hookHost('media_server');
  try {
    await h.ready([row('media_server', { server_type: 'old-server' })]); h.out.startEdit('endpoint-1'); await h.flush();
    const panel = await load('EndpointConfigurationPanel.tsx', { React, useEndpointConfiguration: () => h.out });
    const html = renderToStaticMarkup(React.createElement(panel.EndpointConfigurationPanel, { family: 'media_server' }));
    assert(html.includes('value="old-server" disabled="" selected=""')); assert(!html.includes('value="plex"'));
    assert.equal(await h.out.save(), false); assert.equal(writes(h).length, 0); assert.equal(h.out.draft.type, 'old-server');
  } finally { await h.close(); }
});
