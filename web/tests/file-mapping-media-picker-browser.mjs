import assert from 'node:assert/strict';
import { mkdtemp, writeFile, symlink, rm } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { createServer } from 'vite';
import react from '@vitejs/plugin-react';
import { chromium } from 'playwright-core';

const root = process.env.PULSE_MAPPING_MEDIA_EVIDENCE;
assert(root?.startsWith('/data/temp/') && path.resolve(root) === root, 'owned evidence root required');
const owned = await mkdtemp(path.join(root, 'media-picker-chrome-'));
const web = fileURLToPath(new URL('../', import.meta.url));
const id = n => `00000000-0000-4000-8000-${String(n).padStart(12, '0')}`;
const first = Array.from({ length: 100 }, (_, i) => ({ id: id(1000 + i), title: `已有媒体 ${i + 1}`, media_type: 'movie' }));
const old = { id: id(1200), title: '很早入库的剧集', media_type: 'tv' };
const file = { id: id(1), root_id: id(2), rel_path: 'Shows/Existing.mkv', root_name: '家庭剧集' };
const pageData = (media, more = false, selected) => ({ files: [file], media, limit: 100, files_next_cursor: '', files_has_more: false, media_next_cursor: more ? 'media-page-2' : '', media_has_more: more, ...(selected ? { selected_media: selected } : {}) });
let server, browser, releaseLate;
try {
  await symlink(path.join(web, 'node_modules'), path.join(owned, 'node_modules'), 'dir');
  const auth = path.join(owned, 'auth.ts');
  await writeFile(auth, `import {useSyncExternalStore} from 'react';let state={user:{id:'account-a',role:'admin'},checking:false,statusError:''};const listeners=new Set();globalThis.switchMappingAccount=next=>{state={...state,user:next};for(const f of listeners)f()};export const useAuth=()=>useSyncExternalStore(f=>{listeners.add(f);return()=>listeners.delete(f)},()=>state);`);
  await writeFile(path.join(owned, 'index.html'), '<!doctype html><html lang="zh-CN"><meta name="viewport" content="width=device-width,initial-scale=1"><div id="root"></div><script type="module" src="/main.tsx"></script></html>');
  await writeFile(path.join(owned, 'main.tsx'), `import React from 'react';import{createRoot}from'react-dom/client';import{FileMappingPanel}from'/@fs${web}src/features/configuration/FileMappingPanel.tsx';import{useAuth}from'./auth';import'/@fs${web}src/assets/main.css';import'/@fs${web}src/assets/workspace.css';import'/@fs${web}src/assets/redesign.css';function App(){const{user}=useAuth();return <main style={{padding:16,maxWidth:1000,margin:'auto'}}>{user?.role==='admin'?<FileMappingPanel/>:<p>请联系管理员</p>}</main>}createRoot(document.getElementById('root')).render(<App/>);`);
  server = await createServer({ configFile: false, root: owned, plugins: [react()], resolve: { alias: [{ find: '@/features/auth/AuthContext', replacement: auth }, { find: '@', replacement: path.join(web, 'src') }] }, optimizeDeps: { include: ['react', 'react-dom/client', 'openapi-fetch'] }, server: { host: '127.0.0.1', port: 0, fs: { allow: [owned, web] } } });
  await server.listen();
  browser = await chromium.launch({ executablePath: process.env.CHROME_BIN ?? '/usr/bin/google-chrome', headless: true, args: ['--no-sandbox', '--disable-dev-shm-usage'], downloadsPath: path.join(owned, 'downloads') });
  const page = await browser.newPage({ viewport: { width: 1440, height: 1000 } });
  const errors = [], requests = [];
  page.on('pageerror', e => errors.push(String(e)));
  let account = 'a', failNext = true, mode = 'normal';
  await page.route('**/api/v1/library/mapping-options*', async route => {
    const req = route.request(), params = new URL(req.url()).searchParams;
    requests.push({ account, method: req.method(), query: Object.fromEntries(params) });
    assert.equal(req.method(), 'GET');
    if (params.get('media_query') === 'late') {
      await new Promise(resolve => { releaseLate = resolve; });
      await route.fulfill({ json: pageData([{ ...old, title: 'STALE_ACCOUNT_A' }], false, old) }).catch(() => {}); return;
    }
    if (account === 'b') { await route.fulfill({ json: pageData([{ ...old, id: id(1300), title: '另一账户的当前作品' }]) }); return; }
    if (params.has('media_cursor') && failNext) { failNext = false; await route.fulfill({ status: 503, json: { code: 'UNAVAILABLE' } }); return; }
    const selected = params.get('media_id') === old.id ? old : first.find(m => m.id === params.get('media_id'));
    if (params.has('media_cursor')) { await route.fulfill({ json: pageData([first[99], old], false, selected) }); return; }
    if (params.has('media_query') || params.has('tmdb_id') || params.has('media_type')) {
      await route.fulfill({ json: pageData(mode === 'empty' ? [] : [old], false, selected) }); return;
    }
    await route.fulfill({ json: pageData(first, true, selected) });
  });
  const address = server.httpServer.address();
  await page.goto(`http://127.0.0.1:${address.port}`);
  const panel = page.getByRole('region', { name: '文件识别纠正', exact: true });
  const picker = page.getByRole('group', { name: '已有媒体检索', exact: true });
  const media = picker.getByLabel('持久媒体', { exact: true });
  try { await media.locator(`option[value="${first[0].id}"]`).waitFor({ state: "attached" }); } catch (error) { console.error(JSON.stringify({ pageErrors: errors, requests, body: (await page.locator("body").innerText()).slice(0, 2000) })); await page.screenshot({ path: path.join(root, "mp26-load-failure.png"), fullPage: true }); throw error; }
  await panel.getByLabel('文件身份', { exact: true }).selectOption(file.id);
  await media.selectOption(first[0].id);
  await picker.getByRole('button', { name: '读取下一页媒体', exact: true }).click();
  await picker.getByRole('alert').getByText(/已有结果和选择已保留/).waitFor();
  assert.equal(await media.inputValue(), first[0].id, 'failed continuation changed selection');
  await picker.getByRole('button', { name: '读取下一页媒体', exact: true }).click();
  await media.locator(`option[value="${old.id}"]`).waitFor({ state: 'attached' });
  assert.equal(await media.locator('option').count(), 102, 'overlap not deduplicated');
  await media.selectOption(old.id);
  assert.equal(await panel.getByLabel('文件身份', { exact: true }).inputValue(), file.id, 'media paging reset file');
  await picker.getByLabel('标题', { exact: true }).fill('原始片名%_');
  await picker.getByLabel('媒体类型', { exact: true }).selectOption('tv');
  await picker.getByLabel('TMDB ID', { exact: true }).fill('87654321');
  await picker.getByRole('button', { name: '查找媒体', exact: true }).click();
  await picker.getByRole('button', { name: '查找媒体', exact: true }).waitFor({ state: 'visible' });
  await page.waitForFunction(() => !document.querySelector('[aria-label="已有媒体检索"] fieldset').disabled);
  assert.equal(requests.at(-1).query.media_query, '原始片名%_');
  assert.equal(requests.at(-1).query.tmdb_id, '87654321');
  assert.equal(requests.at(-1).query.media_type, 'tv');
  assert.equal(requests.at(-1).query.media_id, old.id);
  mode = 'empty';
  await picker.getByLabel('标题', { exact: true }).fill('无匹配');
  await picker.getByRole('button', { name: '查找媒体', exact: true }).click();
  await picker.getByText('当前筛选没有已有媒体，可修改条件重新查找。', { exact: true }).waitFor();
  assert.equal(await media.inputValue(), old.id, 'filter discarded saved ID outside page');
  assert.match(await media.locator('option:checked').innerText(), /当前选择/);
  for (const width of [1440, 390, 320]) {
    await page.setViewportSize({ width, height: 1000 });
    assert(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), `overflow ${width}`);
    for (const button of await picker.getByRole('button').all()) { const box = await button.boundingBox(); assert(box && box.height >= 44 && box.width >= 44, `small control ${width}`); }
    await page.screenshot({ path: path.join(root, `mp26-media-${width}.png`), fullPage: true });
  }
  await picker.getByLabel('标题', { exact: true }).fill('late');
  await picker.getByRole('button', { name: '查找媒体', exact: true }).click();
  await page.waitForFunction(() => document.querySelector('[aria-label="已有媒体检索"] fieldset').disabled);
  account = 'b'; await page.evaluate(() => globalThis.switchMappingAccount({ id: 'account-b', role: 'admin' }));
  releaseLate?.();
  await page.getByLabel('持久媒体', { exact: true }).locator(`option[value="${id(1300)}"]`).waitFor({ state: 'attached' });
  assert(!await page.getByText('STALE_ACCOUNT_A', { exact: false }).count(), 'late account A response leaked');
  await page.evaluate(() => globalThis.switchMappingAccount({ id: 'account-b', role: 'member' }));
  await page.getByText('请联系管理员', { exact: true }).waitFor();
  assert.equal(await page.getByRole('group', { name: '已有媒体检索' }).count(), 0);
  assert.equal(errors.length, 0, JSON.stringify(errors));
  console.log(JSON.stringify({ marker: 'MP26_MEDIA_PICKER_CHROME_PASS', checks: ['beyond100', 'failureRetry', 'dedup', 'independentFile', 'titleTypeTMDB', 'savedID', 'empty', 'lateAccountFence', 'memberFence', 'responsive44px'], requests: requests.length, widths: [1440, 390, 320], pageErrors: errors.length }));
} finally {
  releaseLate?.(); await browser?.close(); await server?.close(); await rm(owned, { recursive: true, force: true });
}
