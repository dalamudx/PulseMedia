import { test, before, after } from 'node:test';
import assert from 'node:assert/strict';
import { createRequire } from 'node:module';
import { mkdtemp, writeFile, readFile, rm } from 'node:fs/promises';
import { fileURLToPath } from 'node:url';
import { dirname, join, extname } from 'node:path';
import { createServer } from 'node:http';

const web = dirname(dirname(fileURLToPath(import.meta.url)));
const load = createRequire(join(web, 'package.json'));
const { build } = load('vite');
const { chromium } = load('playwright-core');
const user = '00000000-0000-4000-8000-000000000001';
const file = '00000000-0000-4000-8000-000000000002';
const unit = '00000000-0000-4000-8000-000000000003';
const previewId = '00000000-0000-4000-8000-000000000004';
const other = '00000000-0000-4000-8000-000000000005';
const storageKey = `pulse:managed-sidecar:v1:${user}:${file}:${unit}`;
const hash = 'a'.repeat(64);
const preview = () => ({ id: previewId, file_id: file, state: 'preview', expires_at: new Date(Date.now() + 60000).toISOString(), artifacts: [{ name: 'movie.nfo', state: 'ready', old_hash: hash, new_hash: 'b'.repeat(64) }, { name: 'poster.jpg', state: 'held', old_hash: null, new_hash: null }] });
let temp, server, browser, base;
before(async () => {
  const root = process.env.TMPDIR;
  assert(root?.startsWith('/data/temp/'), 'Set TMPDIR to a unique task directory under /data/temp');
  temp = await mkdtemp(join(root, 'managed-sidecar-test.'));
  await writeFile(join(temp, 'index.html'), '<html lang="zh-CN"><head><meta name="viewport" content="width=device-width,initial-scale=1"></head><body><div id="root"></div><script type="module" src="/entry.tsx"></script></body></html>');
  await writeFile(join(temp, 'entry.tsx'), `import React,{useState} from ${JSON.stringify(join(web, 'node_modules/react/index.js'))};
import {createRoot} from ${JSON.stringify(join(web, 'node_modules/react-dom/client.js'))};
import {AuthProvider} from ${JSON.stringify(join(web, 'src/features/auth/AuthContext.tsx'))};
import {ManagedSidecarUpdatePanel} from ${JSON.stringify(join(web, 'src/features/media-scrape/ManagedSidecarUpdatePanel.tsx'))};
import ${JSON.stringify(join(web, 'src/assets/main.css'))};
import ${JSON.stringify(join(web, 'src/assets/workspace.css'))};
import ${JSON.stringify(join(web, 'src/assets/redesign.css'))};
function App(){const [target,setTarget]=useState({fileId:${JSON.stringify(file)},unitId:${JSON.stringify(unit)}});window.setTarget=setTarget;return <AuthProvider><ManagedSidecarUpdatePanel {...target}/></AuthProvider>};createRoot(document.getElementById('root')).render(<App/>);`);
  await build({ configFile: false, root: temp, cacheDir: join(temp, 'cache'), logLevel: 'error', resolve: { alias: { '@': join(web, 'src'), react: join(web, 'node_modules/react'), 'react-dom': join(web, 'node_modules/react-dom') }, dedupe: ['react', 'react-dom'] }, esbuild: { jsx: 'automatic' }, build: { outDir: join(temp, 'dist'), emptyOutDir: true, minify: true } });
  server = createServer(async (req, res) => {
    try { const path = req.url === '/' ? '/index.html' : req.url; if (!/^\/(?:index\.html|assets\/[a-zA-Z0-9_.-]+)$/.test(path)) { res.writeHead(404).end(); return; } const content = await readFile(join(temp, 'dist', path)); res.setHeader('Content-Type', extname(path) === '.js' ? 'application/javascript' : extname(path) === '.css' ? 'text/css' : 'text/html'); res.end(content); }
    catch { res.writeHead(404).end(); }
  });
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  base = `http://127.0.0.1:${server.address().port}`;
  browser = await chromium.launch({ executablePath: process.env.CHROME_PATH || '/usr/bin/google-chrome', headless: true, args: ['--no-sandbox', '--disable-dev-shm-usage', '--disk-cache-dir=' + join(temp, 'chrome-cache')] });
});
after(async () => { await browser?.close(); if (server) await new Promise(resolve => server.close(resolve)); if (temp) await rm(temp, { recursive: true, force: true }); });

async function setup(t, options = {}) {
  const context = await browser.newContext({ viewport: { width: options.width || 1280, height: 900 }, serviceWorkers: 'block' });
  t.after(() => context.close());
  const calls = [], errors = [];
  let currentUser = user;
  await context.route('**/api/**', async route => {
    const request = route.request(), path = new URL(request.url()).pathname;
    if (path === '/api/v1/auth/status') return route.fulfill({ json: { initialized: true, current_user: { id: currentUser, username: 'admin', role: options.role || 'admin' } } });
    calls.push({ path, method: request.method(), body: request.postData() ? request.postDataJSON() : null, key: request.headers()['idempotency-key'] });
    if (path.endsWith('/sidecar-update-previews')) return route.fulfill({ json: options.preview ? options.preview() : preview() });
    if (path.endsWith('/sidecar-update-commands')) {
      if (options.post) return options.post(route, calls.at(-1));
      return route.fulfill({ json: { ...preview(), state: 'completed', command_key: calls.at(-1).key } });
    }
    if (path.includes('/sidecar-update-commands/')) {
      if (options.get) return options.get(route, calls.at(-1));
      return route.fulfill({ json: { ...preview(), state: 'completed', command_key: path.split('/').at(-1) } });
    }
    return route.fulfill({ status: 404, json: {} });
  });
  const page = await context.newPage(); page.setDefaultTimeout(5000); page.on('pageerror', error => errors.push(error.message));
  await page.goto(base); await page.getByRole('heading', { name: '受管 NFO 与海报更新' }).waitFor({ state: options.role === 'member' ? 'hidden' : 'visible' });
  t.after(() => assert.deepEqual(errors, []));
  return { page, context, calls, account: id => { currentUser = id; } };
}
const previewButton = page => page.getByRole('button', { name: '预览 NFO 与海报更新', exact: true });
const confirmButton = page => page.getByRole('button', { name: '确认更新 NFO 与海报', exact: true });
async function prepare(page) { await previewButton(page).click(); await page.getByRole('checkbox').check(); }
const posts = calls => calls.filter(c => c.path.endsWith('/sidecar-update-commands'));

test('explicit preview then confirmation uses exact body and persisted original key', async t => {
  const { page, calls } = await setup(t);
  assert.equal(calls.length, 0); await previewButton(page).click(); await confirmButton(page).waitFor();
  assert.deepEqual(calls[0].body, { unit_id: unit }); assert(await confirmButton(page).isDisabled());
  await page.getByRole('checkbox').check(); await confirmButton(page).click(); await page.getByText('更新已完成', { exact: true }).waitFor();
  assert.equal(posts(calls).length, 1); assert.deepEqual(posts(calls)[0].body, { preview_id: previewId, confirmed: true });
  const saved = await page.evaluate(k => JSON.parse(localStorage.getItem(k)), storageKey);
  assert.equal(saved.key, posts(calls)[0].key); assert.equal(saved.userId, user); assert.equal(saved.resolved, true);
  assert.equal(await page.locator('a,img').count(), 0);
});
for (const state of ['held', 'unchanged', 'expired']) test(`${state} preview cannot authorize confirmation`, async t => {
  const { page, calls } = await setup(t, { preview: () => { const value = preview(); if (state === 'held') value.state = 'held'; if (state === 'unchanged') value.artifacts.forEach(a => a.state = 'unchanged'); if (state === 'expired') value.expires_at = '2000-01-01T00:00:00Z'; return value; } });
  await previewButton(page).click(); await page.getByText('movie.nfo', { exact: true }).waitFor();
  if (state === 'held') assert.equal(await confirmButton(page).count(), 0);
  else { await page.getByRole('checkbox').check(); assert(await confirmButton(page).isDisabled()); }
  assert.equal(posts(calls).length, 0);
});
test('lost POST survives reload; restoration is GET only with original key', async t => {
  const { page, calls } = await setup(t, { post: route => route.abort('failed') });
  await prepare(page); await confirmButton(page).click(); await page.getByRole('alert').waitFor();
  const original = posts(calls)[0].key; assert(await previewButton(page).isDisabled());
  await page.reload(); await page.getByText('更新已完成', { exact: true }).waitFor();
  assert.equal(posts(calls).length, 1); assert(calls.some(c => c.method === 'GET' && c.path.endsWith('/' + original)));
});
test('unknown POST and GET 403 preserve key and block new sends', async t => {
  const { page, calls } = await setup(t, { post: route => route.abort('failed'), get: route => route.fulfill({ status: 403, json: {} }) });
  await prepare(page); await confirmButton(page).click(); await page.getByRole('alert').waitFor();
  await page.getByRole('button', { name: '读取原键结果' }).click(); await page.getByRole('alert').waitFor();
  assert(await previewButton(page).isDisabled()); assert.equal(posts(calls).length, 1);
  assert.equal(await page.evaluate(k => JSON.parse(localStorage.getItem(k)).resolved, storageKey), false);
});
test('malformed or URL artifact previews never permit writes', async t => {
  const { page, calls } = await setup(t, { preview: () => ({ ...preview(), artifacts: [{ name: 'https://example.org/poster.jpg', state: 'ready', old_hash: hash, new_hash: hash }] }) });
  await previewButton(page).click(); await page.getByRole('alert').waitFor(); assert.equal(await confirmButton(page).count(), 0); assert.equal(posts(calls).length, 0); assert.equal(await page.locator('a,img').count(), 0);
});
test('storage failure stops confirmation before POST', async t => {
  const { page, calls } = await setup(t); await prepare(page);
  await page.evaluate(() => { Storage.prototype.setItem = () => { throw Error('denied'); }; });
  await confirmButton(page).click(); await page.getByRole('alert').waitFor(); assert.equal(posts(calls).length, 0);
});
test('another account cannot restore previous account command', async t => {
  const s = await setup(t, { post: route => route.abort('failed') }); await prepare(s.page); await confirmButton(s.page).click(); await s.page.getByRole('alert').waitFor();
  const count = s.calls.length; s.account(other); await s.page.reload(); await previewButton(s.page).waitFor();
  assert.equal(s.calls.length, count); assert(await s.page.evaluate(k => !!localStorage.getItem(k), storageKey));
});
test('file and unit changes discard preview and old confirmation', async t => {
  const { page, calls } = await setup(t); await prepare(page);
  await page.evaluate(({ other, unit }) => window.setTarget({ fileId: other, unitId: unit }), { other, unit });
  await confirmButton(page).waitFor({ state: 'hidden' }); assert.equal(posts(calls).length, 0);
  await page.evaluate(({ file, other }) => window.setTarget({ fileId: file, unitId: other }), { file, other });
  assert.equal(await confirmButton(page).count(), 0);
});
test('cross-tab confirmation checks shared journal under Web Lock', async t => {
  const { page, context, calls } = await setup(t, { post: route => route.abort('failed') });
  const second = await context.newPage(); second.setDefaultTimeout(5000); await second.goto(base); await prepare(page); await prepare(second);
  await confirmButton(page).click(); await page.getByRole('alert').waitFor(); await confirmButton(second).click(); await second.getByRole('alert').waitFor();
  assert.equal(posts(calls).length, 1); assert(await previewButton(second).isDisabled());
});
test('member has no update panel or API side effects', async t => {
  const { page, calls } = await setup(t, { role: 'member' }); assert.equal(await previewButton(page).count(), 0); assert.equal(calls.length, 0);
});
test('real backend omitempty hashes and updated artifacts render correctly', async t => {
  const { page } = await setup(t, { preview: () => { const value = preview(); delete value.artifacts[1].old_hash; return value; }, post: (route, call) => route.fulfill({ json: { ...preview(), state: 'completed', artifacts: [{ name: 'movie.nfo', state: 'updated', old_hash: hash, new_hash: hash }, { name: 'poster.jpg', state: 'held' }], command_key: call.key } }) });
  await prepare(page); await confirmButton(page).click(); await page.getByText('更新已完成', { exact: true }).waitFor(); await page.getByText('已更新', { exact: false }).waitFor();
});
test('review_required command is shown as manual review without resending', async t => {
  const { page, calls } = await setup(t, { post: (route, call) => route.fulfill({ json: { ...preview(), state: 'review_required', artifacts: [{ name: 'movie.nfo', state: 'review_required' }], command_key: call.key } }) });
  await prepare(page); await confirmButton(page).click(); await page.getByText('需要人工核对；保留已有文件', { exact: true }).waitFor(); assert.equal(posts(calls).length, 1);
});
test('wrong command key never resolves the persisted unknown operation', async t => {
  const { page, calls } = await setup(t, { post: route => route.fulfill({ json: { ...preview(), state: 'completed', command_key: other } }) });
  await prepare(page); await confirmButton(page).click(); await page.getByRole('alert').waitFor(); assert(await previewButton(page).isDisabled()); assert.equal(posts(calls).length, 1); assert.equal(await page.evaluate(k => JSON.parse(localStorage.getItem(k)).resolved, storageKey), false);
});
test('corrupt journal cannot authorize a replacement command', async t => {
  const { page, calls } = await setup(t); await page.evaluate(k => localStorage.setItem(k, '{bad'), storageKey); await page.reload(); await page.getByRole('alert').waitFor(); await previewButton(page).click(); await page.getByRole('alert').waitFor(); assert.equal(calls.length, 0);
});
test('late preview for a replaced file cannot expose its confirmation', async t => {
  let release;
  const { page, calls } = await setup(t, { preview: () => preview() });
  await page.route('**/sidecar-update-previews', async route => { await new Promise(resolve => { release = resolve; }); await route.fulfill({ json: preview() }).catch(() => {}); });
  await previewButton(page).click(); await page.waitForFunction(() => document.querySelector('section')?.getAttribute('aria-busy') === 'true');
  await page.evaluate(({ other, unit }) => window.setTarget({ fileId: other, unitId: unit }), { other, unit });
  release?.(); await previewButton(page).waitFor(); assert.equal(await confirmButton(page).count(), 0); assert.equal(posts(calls).length, 0);
});

// The App acceptance owner supplies an isolated listening App, genuine Cookie,
// preview-ready file/unit, and the production page where Main mounts this panel.
if (process.env.PULSE_MP29_LIVE_URL) test('LIVE App Cookie production panel updates or restores original command', async t => {
  const url = new URL(process.env.PULSE_MP29_LIVE_URL);
  assert(['127.0.0.1', 'localhost', '[::1]'].includes(url.hostname), 'Use the isolated local acceptance App');
  const fileId = process.env.PULSE_MP29_LIVE_FILE_ID, unitId = process.env.PULSE_MP29_LIVE_UNIT_ID;
  const session = process.env.PULSE_MP29_LIVE_SESSION;
  assert(fileId && unitId && session, 'Provide live file/unit/session through the acceptance process environment');
  const scenario = process.env.PULSE_MP29_LIVE_SCENARIO || 'completed';
  assert(['completed', 'held', 'unknown'].includes(scenario));
  const context = await browser.newContext({ viewport: { width: 1280, height: 1000 }, serviceWorkers: 'block' });
  t.after(() => context.close());
  await context.addCookies([{ name: 'pulsemedia_session', value: session, url: url.origin, httpOnly: true, sameSite: 'Strict' }]);
  const page = await context.newPage(); page.setDefaultTimeout(15000);
  const errors = [], sent = [], replies = [];
  page.on('pageerror', error => errors.push(error.message));
  const commandPath = `/api/v1/library/files/${fileId}/sidecar-update-commands`;
  const previewPath = `/api/v1/library/files/${fileId}/sidecar-update-previews`;
  page.on('request', request => { const path = new URL(request.url()).pathname; if ([commandPath, previewPath].includes(path)) sent.push({ path, method: request.method(), key: request.headers()['idempotency-key'], body: request.postDataJSON() }); });
  page.on('response', response => { const path = new URL(response.url()).pathname; if (path.startsWith(commandPath) || path === previewPath) replies.push({ path, status: response.status() }); });
  if (scenario === 'unknown') await context.route('**' + commandPath, async route => {
    if (route.request().method() !== 'POST') return route.continue();
    const real = await route.fetch(); assert.equal(real.status(), 200);
    const result = await real.json(); assert(['completed', 'review_required'].includes(result.state));
    await route.abort('failed');
  });
  await page.goto(url.href, { waitUntil: 'domcontentloaded' });
  const panel = page.getByRole('region', { name: '受管 NFO 与海报更新', exact: true });
  await panel.waitFor(); assert.equal(sent.length, 0);
  await panel.getByRole('button', { name: '预览 NFO 与海报更新', exact: true }).click();
  const target = panel.getByRole('button', { name: '确认更新 NFO 与海报', exact: true });
  if (scenario === 'held') {
    await panel.getByText('本次更新已保留，请人工核对', { exact: true }).waitFor();
    assert.equal(await target.count(), 0); assert.equal(sent.filter(c => c.path === commandPath).length, 0);
  } else {
    await target.waitFor(); assert(await target.isDisabled());
    await panel.getByRole('checkbox').check(); await target.click();
    if (scenario === 'unknown') { await panel.getByRole('alert').waitFor(); await page.reload(); }
    await panel.getByText(/更新已完成|需要人工核对；保留已有文件/).waitFor();
    assert.equal(sent.filter(c => c.path === commandPath).length, 1);
    const original = sent.find(c => c.path === commandPath);
    assert(original.key); assert.deepEqual(Object.keys(original.body).sort(), ['confirmed', 'preview_id']); assert.equal(original.body.confirmed, true);
    await Promise.all([page.waitForResponse(response => new URL(response.url()).pathname === commandPath + '/' + original.key && response.status() === 200), panel.getByRole('button', { name: '读取原键结果', exact: true }).click()]);
    assert.equal(sent.filter(c => c.path === commandPath).length, 1);
  }
  assert.deepEqual(sent.find(c => c.path === previewPath)?.body, { unit_id: unitId });
  for (const width of [1280, 375, 320]) {
    await page.setViewportSize({ width, height: 1000 });
    assert(await panel.evaluate(root => root.getBoundingClientRect().right <= innerWidth + 1 && root.scrollWidth <= root.clientWidth + 1));
    assert(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1));
  }
  assert.deepEqual(errors, []); assert(replies.every(reply => reply.status === 200));
  t.diagnostic(`LIVE App Cookie Chrome ${scenario}: preview=${sent.filter(c => c.path === previewPath).length} command=${sent.filter(c => c.path === commandPath).length}; real responses=${replies.length}; 1280/375/320px passed`);
});

for (const width of [1280, 375, 320]) test(`production panel fits ${width}px with hashes and usable controls`, async t => {
  const { page } = await setup(t, { width }); await prepare(page);
  const issues = await page.locator('section').evaluate(root => {
    const failures = []; if (document.documentElement.scrollWidth > innerWidth + 1) failures.push('viewport overflow');
    for (const node of root.querySelectorAll('button,input,li,p')) { const r = node.getBoundingClientRect(); if (r.width && (r.right > innerWidth + 1 || r.left < -1)) failures.push(node.tagName + ' overflow'); if (node.tagName === 'BUTTON' && r.height < 44) failures.push('short button'); }
    return failures;
  }); assert.deepEqual(issues, []);
});
