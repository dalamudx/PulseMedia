// Standalone controlled Chrome fixture. No PostgreSQL or production App acceptance is claimed.
import assert from 'node:assert/strict';
import { mkdtemp, rm, access } from 'node:fs/promises';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { createServer } from 'vite';
import { chromium } from 'playwright-core';
const tempRoot = process.env.TMPDIR;
assert.ok(tempRoot?.startsWith('/data/temp/') && !tempRoot.split('/').includes('..'), 'Use an owned directory beneath /data/temp');
const work = await mkdtemp(join(tempRoot, 'chrome-'));
const root = fileURLToPath(new URL('..', import.meta.url));
const id = n => `00000000-0000-0000-0000-${n.toString(16).padStart(12, '0')}`;
const subscription = { id: id(2), user_id: id(1), media_id: id(3), root_id: id(4), title: '真实稀疏剧集', media_type: 'tv', root_name: '剧集根', root_path: '/library/tv', intent_version: 1, scope_type: 'all', quality_policy_id: 'default', quality_policy_revision: 1, state: 'active' };
const files = [5, 15, 16].map((n, index) => ({ id: id(n), root_id: id(4), rel_path: index === 0 ? 'Show/很长的真实目录/' + '路径'.repeat(60) + '/S01E03.mkv' : `Show/S01E0${index + 4}.mkv`, root_name: '剧集根' }));
const foreign = { ...files[0], id: id(17), root_id: id(18), root_name: '其它根' };
const unit = { id: id(6), media_id: id(3), unit_key: 'S01E03', retired: false, metadata_version: 1 };
const candidate = { id: id(7), title: 'Show.S01E03.2160p.' + '很长的候选标题'.repeat(20), indexer_name: '受控来源', size_bytes: 123, seeders: 3, leechers: 0 };
const receipt = { id: id(8), subscription_id: id(2), media_id: id(3), query: 'Show S01E03', created_at: '2026-10-27T00:00:00Z', deadline: '2026-10-27T00:02:00Z', source_count: 1, ranking_policy_id: 'default', ranking_policy_revision: 1 };
const requests = [], phaseReceipts = new Map();
let retired = false, emptyCandidates = false, mappingMode = 'single', serverIntent = 1, delayMapping, failNextPage = false, delayFilePage;
const entry = `import React from 'react';import{createRoot}from'react-dom/client';import{TVEpisodeUpgradeWorkspace}from'/src/features/configuration/TVEpisodeUpgradeWorkspace.tsx';import{FileMappingPanel}from'/src/features/configuration/FileMappingPanel.tsx';function App(){const[s,set]=React.useState(${JSON.stringify(subscription)});window.setFixtureSubscription=change=>set(old=>({...old,...change}));return location.search.includes('mapping')?React.createElement(FileMappingPanel):React.createElement(TVEpisodeUpgradeWorkspace,{subscription:s,onUpdated:()=>{window.updated=(window.updated??0)+1}})}createRoot(document.getElementById('root')).render(React.createElement(App));`;
const auth = `import{useSyncExternalStore}from'react';let user={id:'${id(1)}',role:'admin'};const listeners=new Set();window.setFixtureActor=(value)=>{user=value;listeners.forEach(fn=>fn())};export function useAuth(){return{user:useSyncExternalStore(fn=>{listeners.add(fn);return()=>listeners.delete(fn)},()=>user),checking:false,statusError:null}}`;
const client = `async function request(method,path,options={}){const payload={method,path,params:options.params,body:options.body,headers:options.headers};const response=await fetch('/mock',{method:'POST',body:JSON.stringify(payload),signal:options.signal});const data=await response.json();return{response,data:response.ok?data:undefined,error:response.ok?undefined:data}}export const api={GET:(p,o)=>request('GET',p,o),POST:(p,o)=>request('POST',p,o)};`;
let server, browser;
try {
  server = await createServer({ configFile: false, root, cacheDir: join(work, 'vite'), optimizeDeps: { noDiscovery: true, include: ['react', 'react-dom/client', 'react/jsx-dev-runtime'] }, resolve: { alias: [{ find: '@/api/client', replacement: '\0mp22w-client' }, { find: '@/features/auth/AuthContext', replacement: '\0mp22w-auth' }, { find: '@', replacement: join(root, 'src') }] }, server: { host: '127.0.0.1', port: 0 }, plugins: [{
    name: 'mp22-workspace-fixture', resolveId(v) { if (v.startsWith('\0mp22w-')) return v; }, load(v) { return v === '\0mp22w-entry' ? entry : v === '\0mp22w-auth' ? auth : v === '\0mp22w-client' ? client : undefined; },
    configureServer(s) { s.middlewares.use(async (req, res, next) => {
      if (req.url?.startsWith('/fixture')) { res.setHeader('content-type', 'text/html'); res.end('<!doctype html><html lang="zh"><meta name="viewport" content="width=device-width,initial-scale=1"><link rel="stylesheet" href="/src/assets/main.css"><div id="root" style="padding:16px;min-width:0"></div><script type="module" src="/@id/__x00__mp22w-entry"></script></html>'); return; }
      if (req.url !== '/mock') return next();
      try {
        let raw = ''; for await (const chunk of req) raw += chunk;
        const r = JSON.parse(raw); requests.push(r); let data; const p = r.path;
        if (p === '/api/v1/subscriptions/{id}') data = { ...subscription, intent_version: serverIntent };
        else if (p === '/api/v1/subscriptions/{id}/tv-episode-discovery/{unit_id}') data = {subscription_id:subscription.id,unit_id:unit.id,state:'not_scheduled',execution_allowed:false};
        else if (p === '/api/v1/library/mapping-options') {
          const query = r.params?.query ?? {};
          if (query.root_id) assert.equal(query.root_id, subscription.root_id);
          if (query.cursor && delayFilePage) await delayFilePage;
          if (query.cursor && failNextPage) { failNextPage = false; res.statusCode = 503; data = { code: 'FIXTURE_READ_FAILURE' }; }
          else data = { limit: 100, files: retired ? [] : query.cursor ? [...files.slice(1), files[0], foreign] : [files[0], foreign], media: [{ id: subscription.media_id, title: subscription.title, media_type: 'tv' }], files_next_cursor: !retired && !query.cursor ? 'fixture_page2' : '', files_has_more: !retired && !query.cursor };
        }
        else if (p === '/api/v1/library/files/{id}/mapping') {
          const file = files.find(f => f.id === r.params.path.id); assert.ok(file); assert.equal(r.params.query.media_id, subscription.media_id);
          if (delayMapping) await delayMapping;
          data = { file_id: file.id, root_id: file.root_id, rel_path: file.rel_path, media_id: subscription.media_id, media_title: subscription.title, can_correct: !retired, hold_reason: retired ? 'file_retired' : '', mapping_revision: 1, input_stamp: 'fixture', available_units: [unit], associations: [{ active: true, unit }, ...(mappingMode === 'single' ? [{ active: false, unit: { ...unit, id: id(19), retired: true } }] : [{ active: true, unit: { ...unit, id: id(20), media_id: mappingMode === 'foreign' ? id(21) : unit.media_id } }])] };
        } else if (p === '/api/v1/release-searches' && r.method === 'POST') { assert.equal(r.body.subscription_id, subscription.id); assert.equal(r.body.media_id, subscription.media_id); data = receipt; }
        else if (p === '/api/v1/release-search-commands/{key}') data = receipt;
        else if (p === '/api/v1/release-searches/{id}') data = { search: receipt, state: 'succeeded', sources: [{ indexer_id: id(9), name: '受控来源', config_version: 1, state: 'succeeded', reason: '', query_ids: [], partial: false }], results: emptyCandidates ? [] : [candidate], partial: false, truncated: false, total_candidates: emptyCandidates ? 0 : 1, statistics_mode: 'latest_persisted', sort_order: 'quality_desc_seeders_desc_id_asc' };
        else if (p === '/api/v1/subscriptions/{id}/tv-episode-upgrade-previews') data = { ...r.body, version: 'preview:v1', status: 'preview_not_executable', execution_allowed: false, comparable: true, improvement: true, current_score: 10, candidate_score: 20 };
        else if (p === '/api/v1/subscriptions/{id}/tv-episode-upgrade-commands') {
          assert.equal(r.body.action, 'reserve'); phaseReceipts.set(r.headers['Idempotency-Key'], { unit_id: unit.id, current_file_id: r.body.current_file_id, candidate_id: candidate.id, operation_id: id(10), version: 'preview:v1', execution_allowed: false, status: 'reserved_not_executable' });
          res.statusCode = 503; data = { code: 'FIXTURE_LOST_RECEIPT' };
        } else if (p === '/api/v1/subscriptions/{id}/tv-episode-upgrade-commands/{key}') { data = phaseReceipts.get(r.params.path.key); assert.ok(data); }
        else throw Error('Unexpected fixture call: ' + JSON.stringify(r));
        res.setHeader('content-type', 'application/json'); res.end(JSON.stringify(data));
      } catch (error) { res.statusCode = 500; res.end(JSON.stringify({ error: String(error) })); }
    }); },
  }] });
  await server.listen(); const url = `http://127.0.0.1:${server.httpServer.address().port}/fixture`;
  browser = await chromium.launch({ executablePath: process.env.CHROME_BIN || '/usr/bin/google-chrome', headless: true, args: ['--no-sandbox', '--disable-dev-shm-usage'], downloadsPath: join(work, 'downloads') });
  const page = await browser.newPage(), errors = []; page.setDefaultTimeout(8000); page.on('pageerror', e => errors.push(String(e)));
  const button = name => page.getByRole('button', { name, exact: true });
  await page.goto(url); await page.getByRole('heading', { name: '选择 TV 单集升级资源' }).waitFor(); assert.equal(requests.length, 0);
  await button('读取当前文件与订阅').click(); await page.getByLabel('当前文件', { exact: true }).waitFor();
  assert.equal(await page.getByLabel('当前文件', { exact: true }).locator(`option[value="${foreign.id}"]`).count(), 0);
  const optionsRead = requests.find(r => r.path === '/api/v1/library/mapping-options'); assert.equal(optionsRead.params.query.root_id, subscription.root_id);
  assert.equal(await page.getByLabel('当前文件', { exact: true }).locator(`option[value="${files[1].id}"]`).count(), 0);
  failNextPage = true; await button('读取下一页文件').click(); await page.getByRole('alert').waitFor();
  await button('读取下一页文件').click(); await page.getByLabel('当前文件', { exact: true }).locator(`option[value="${files[1].id}"]`).waitFor({ state: 'attached' });
  assert.equal(await page.getByLabel('当前文件', { exact: true }).locator('option').count(), 4, 'root filtering and duplicate-safe append');
  await page.getByLabel('当前文件', { exact: true }).selectOption(files[1].id);
  assert.equal(await button('检索站点').isDisabled(), true);
  for (const mode of ['multiple', 'foreign']) { mappingMode = mode; await button('核验标准集映射').click(); await page.getByRole('alert').waitFor(); assert.equal(await button('检索站点').isDisabled(), true); }
  mappingMode = 'single'; await button('核验标准集映射').click(); await page.getByText('已核验标准集：S01E03（唯一有效关联）', { exact: true }).waitFor();
  assert.equal(requests.filter(r => r.method === 'POST').length, 0);
  await page.getByLabel('单集资源检索词').fill('Show S01E03'); await button('检索站点').click();
  await page.getByLabel('已保存候选资源').locator(`option[value="${candidate.id}"]`).waitFor({state:"attached"});
  await page.getByLabel('已保存候选资源').selectOption(candidate.id);
  for (const width of [1280, 390, 320]) {
    await page.setViewportSize({ width, height: 850 });
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false, `overflow at ${width}`);
    assert.deepEqual(await page.locator('.tv-upgrade-workspace button, .tv-upgrade-workspace select, .tv-upgrade-workspace input').evaluateAll(nodes => nodes.filter(n => n.getBoundingClientRect().height < 44).map(n => n.outerHTML)), []);
    await page.screenshot({ path: join(work, `selection-${width}.png`), fullPage: true });
  }
  assert.equal(requests.filter(r => r.path.includes('tv-episode-upgrade')).length, 0);
  await button('核验选择并进入升级比较').click(); await page.getByRole('heading', { name: 'TV 单集品质升级', exact: true }).waitFor();
  assert.equal(requests.filter(r => r.path.includes('tv-episode-upgrade')).length, 0, 'handoff mounts panel without automatic preview POST');
  await button('读取升级比较').click(); await page.getByText('候选严格优于旧文件。比较结果尚未授权下载。', { exact: true }).waitFor();
  await page.getByRole('checkbox', { name: '我确认：冻结单集升级预留', exact: true }).check(); await button('冻结单集升级预留').click(); await page.getByRole('alert').waitFor();
  const original = requests.find(r => r.body?.action === 'reserve'); assert.ok(original);
  retired = true; emptyCandidates = true;
  await page.reload(); await page.getByRole('heading', { name: '选择 TV 单集升级资源' }).waitFor();
  const count = requests.length; await page.locator('summary').click(); await button('打开原选择 1').click(); await button('读取原命令回执').waitFor();
  assert.deepEqual(requests.slice(count).map(r => [r.method, r.path]), [['GET', '/api/v1/subscriptions/{id}']], 'opening original selection does no search, mapping or phase POST');
  await button('读取原命令回执').click(); await page.getByRole('checkbox', { name: '我确认：取源准备', exact: true }).waitFor();
  const recovered = requests.at(-1); assert.equal(recovered.method, 'GET'); assert.equal(recovered.params.path.key, original.headers['Idempotency-Key']);
  assert.equal(requests.filter(r => r.body?.action === 'reserve').length, 1);
  assert.equal(await page.getByText(unit.id, { exact: true }).count(), 1); assert.equal(await page.getByText(files[1].id, { exact: true }).count(), 1); assert.equal(await page.getByText(candidate.id, { exact: true }).count(), 1);
  await page.evaluate(({ id }) => window.setFixtureActor({ id, role: 'member' }), { id: id(30) }); assert.equal(await page.getByRole('heading', { name: '选择 TV 单集升级资源' }).count(), 0);
  await page.evaluate(({ id }) => window.setFixtureActor({ id, role: 'admin' }), { id: id(30) }); await page.getByRole('heading', { name: '选择 TV 单集升级资源' }).waitFor(); assert.equal(await page.locator('summary').count(), 0);
  await page.evaluate(({ id }) => window.setFixtureActor({ id, role: 'admin' }), { id: id(1) }); await page.locator('summary').waitFor();
  await page.evaluate(() => window.setFixtureSubscription({ intent_version: 2 })); assert.equal(await page.getByRole('heading', { name: 'TV 单集品质升级', exact: true }).count(), 0);
  await button('读取当前文件与订阅').click(); await page.getByRole('alert').waitFor(); assert.equal(await page.getByLabel('当前文件', { exact: true }).count(), 0);
  serverIntent = 2; retired = false; await button('读取当前文件与订阅').click(); await page.getByLabel('当前文件', { exact: true }).waitFor();
  await button('读取下一页文件').click(); await page.getByLabel('当前文件', { exact: true }).locator(`option[value="${files[1].id}"]`).waitFor({ state: 'attached' });
  await page.getByLabel('当前文件', { exact: true }).selectOption(files[0].id);
  let release; delayMapping = new Promise(resolve => { release = resolve; });
  await button('核验标准集映射').click(); await page.getByText('正在核验文件与标准集…', { exact: true }).waitFor();
  await page.getByLabel('当前文件', { exact: true }).selectOption(files[1].id); release(); delayMapping = undefined;
  await page.waitForLoadState('networkidle'); assert.equal(await page.getByText('已核验标准集：S01E03（唯一有效关联）', { exact: true }).count(), 0); assert.equal(await button('检索站点').isDisabled(), true);
  retired = false; mappingMode = 'single'; await page.goto(url + '?mapping');
  const mappingFile = page.getByLabel('文件身份', { exact: true }); await mappingFile.locator(`option[value="${files[0].id}"]`).waitFor({ state: 'attached' });
  await mappingFile.selectOption(files[0].id); failNextPage = true;
  await button('读取下一页文件').click(); await page.getByRole('alert').waitFor(); assert.equal(await mappingFile.inputValue(), files[0].id);
  await button('读取下一页文件').click(); await mappingFile.locator(`option[value="${files[1].id}"]`).waitFor({ state: 'attached' });
  assert.equal(await mappingFile.locator(`option[value="${files[0].id}"]`).count(), 1); assert.equal(await mappingFile.inputValue(), files[0].id);
  await mappingFile.selectOption(files[1].id); await page.getByLabel('持久媒体', { exact: true }).selectOption(subscription.media_id);
  await button('读取当前映射').click(); await page.getByRole('heading', { name: files[1].rel_path, exact: true }).waitFor();
  const correctionPosts = requests.filter(r => r.method === 'POST' && r.path.includes('/mapping')).length; assert.equal(correctionPosts, 0, 'reading later files does not correct mappings');
  for (const width of [1280,390,320]) {
    await page.setViewportSize({width,height:850}); assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth), false, 'mapping page overflow '+width);
    if (process.env.PULSE_MAPPING_EVIDENCE) await page.screenshot({ path: join(tempRoot, `mapping-page-${width}.png`), fullPage: true });
  }
  await page.reload(); await button('读取下一页文件').waitFor();
  let releaseFiles; delayFilePage = new Promise(resolve => { releaseFiles=resolve; });
  await button('读取下一页文件').click();
  await page.evaluate(({id}) => window.setFixtureActor({id,role:'admin'}), {id:id(31)});
  releaseFiles(); delayFilePage = undefined;
  await page.waitForLoadState('networkidle');
  assert.equal(await page.getByLabel('文件身份', { exact: true }).locator(`option[value="${files[1].id}"]`).count(), 0, 'old account page must not append into new account');
  assert.deepEqual(errors, []);
  console.log('PASS controlled Chrome fixture only: real path/unit selection; foreign-root filtering; multiunit/shared-file rejection; explicit bounded search and saved candidates; no auto phase POST; lost reserve + refresh + retired file/missing candidate original-key GET recovery; actor and intent fences; aborted late reads; 1280/390/320px wrap and >=44px controls; zero page errors. NOT PG/App acceptance.');
} finally {
  await browser?.close(); await server?.close(); await rm(work, { recursive: true, force: true });
  await assert.rejects(access(work));
}
