// Opt-in integration test: disposable real PG/App.Run + existing Chrome/CDP.
// Only metadata reads use deterministic transport fixtures. All writes/receipts,
// versions, object bindings, auth, polling and reloads use the actual HTTP API.
import assert from 'node:assert/strict';
import { createServer } from 'node:http';
import { once } from 'node:events';
import { setTimeout as delay } from 'node:timers/promises';
import { execFile } from 'node:child_process';
import { promisify } from 'node:util';
import { chromium } from 'playwright-core';

assert.equal(process.env.PULSE_UI_FIXTURE, '1', 'Explicit disposable fixture opt-in required');
const base = process.env.PULSE_UI_BASE_URL;
assert.ok(base, 'PULSE_UI_BASE_URL is required');
assert.ok(['127.0.0.1', 'localhost'].includes(new URL(base).hostname), 'Only local fixtures allowed');
const token = process.env.PULSE_UI_SETUP_TOKEN ?? '';
assert.ok(process.env.PULSE_UI_DATABASE_URL,'Disposable PG URL is required for the imported-status fixture');
const pg = new URL(process.env.PULSE_UI_DATABASE_URL);
assert.ok(['127.0.0.1','localhost'].includes(pg.hostname)&&!pg.password&&/^\/pulse_[a-z0-9_]+$/.test(pg.pathname),'Only passwordless disposable local pulse_* databases accepted');
async function seedStatus(sql){
 try { await promisify(execFile)('mise',['exec','--','psql','-h',pg.hostname,'-p',pg.port||'5432','-U',decodeURIComponent(pg.username),'-d',pg.pathname.slice(1),'-v','ON_ERROR_STOP=1','-c',sql],{timeout:10000}); }
 catch { throw new Error('Disposable PG status fixture update failed (details suppressed)'); }
}
let statusCleanup="";
let feedCalls = 0;let healthEndpointOK=true;
const fixture = createServer((req, res) => {
 if (req.url.startsWith('/poster')) {
  res.setHeader('Content-Type', 'image/svg+xml');
  res.end('<svg xmlns="http://www.w3.org/2000/svg" width="300" height="450"><rect width="300" height="450" fill="#233044"/><text x="32" y="230" fill="white" font-size="26">UI fixture</text></svg>'); return;
 }
 res.setHeader('Content-Type', 'application/rss+xml');
 if (req.url.startsWith('/feed')) { feedCalls++; res.end('<rss><channel><item><title>UI.Movie.2020.1080p.BluRay</title><guid>ui-release</guid><enclosure url="https://fixture.invalid/dl" length="1024"/></item></channel></rss>'); }
 else if(!healthEndpointOK){res.statusCode=500;res.end('fixture-private-error-do-not-log')}else res.end('<rss><channel></channel></rss>');
});
fixture.listen(0, '127.0.0.1'); await once(fixture, 'listening');
const source = `http://127.0.0.1:${fixture.address().port}`;
const browser = await chromium.connectOverCDP(process.env.PULSE_UI_CDP_URL ?? 'http://127.0.0.1:9222');
const context = await browser.newContext({ viewport: { width: 1440, height: 1000 } });
const page = await context.newPage();
page.setDefaultTimeout(12000);
const pageErrors = []; page.on('pageerror', error => pageErrors.push(error.message));
const metadata = [
 { tmdb_id: 900001, title: 'UI Movie', year: 2020, media_type: 'movie', overview: 'Controlled metadata fixture', poster_url: `${source}/poster.svg`, genres: [] },
 { tmdb_id: 900002, title: 'UI Show', year: 2021, media_type: 'tv', overview: 'Two-season metadata fixture', poster_url: `${source}/poster-tv.svg`, genres: [] },
];
let discoveryMode = 'normal';
await page.route(/\/api\/v1\/discover(?:\?.*)?$/, route => route.fulfill({status: discoveryMode === 'failed' ? 503 : 200, contentType: 'application/json', body: JSON.stringify(discoveryMode === 'failed' ? {code:'UNAVAILABLE',message:'fixture error'} : {results: discoveryMode === 'empty' ? [] : metadata})}));
await page.route('**/api/v1/media/tmdb/movie/900001', route => route.fulfill({contentType:'application/json',body:JSON.stringify(metadata[0])}));
await page.route('**/api/v1/media/tmdb/tv/900002', route => route.fulfill({contentType:'application/json',body:JSON.stringify({...metadata[1],seasons:[{season_number:1,name:'第 1 季',episode_count:2},{season_number:2,name:'第 2 季',episode_count:1}]})}));
// Only optional poster metadata is fixture-enriched. Real list bindings, types,
// titles, wanted unit keys and versions are never replaced by the test.
await page.route(/\/api\/v1\/subscriptions(?:\?.*)?$/, async route => {
 if (route.request().method() !== 'GET') { await route.continue(); return; }
 const response=await route.fetch();
 if(response.status()!==200){await route.fulfill({response});return}
 const data=await response.json();
 data.items=data.items.map(item=>({...item,poster_url:`${source}/poster.svg`}));
 await route.fulfill({response,json:data});
});
async function api(path, method = 'GET', body, key) {
 const legacyState=path.match(/^\/api\/v1\/subscriptions\/([a-f0-9-]+)\/(pause|resume)$/);
 if(method==='POST'&&legacyState){const current=await api(`/api/v1/subscriptions/${legacyState[1]}`);return api(`/api/v1/subscriptions/${legacyState[1]}/control`,'PUT',{action:'state',state:legacyState[2]==='pause'?'paused':'active',expected_version:current.intent_version},crypto.randomUUID())}
 const result = await page.evaluate(async ({path,method,body,key}) => {
  const response = await fetch(path,{method,headers:{...(body?{'Content-Type':'application/json'}:{}),...(key?{'Idempotency-Key':key}:{})},...(body?{body:JSON.stringify(body)}:{})});
  return {status:response.status,data:await response.json()};
 }, {path,method,body,key});
 assert.ok(result.status >= 200 && result.status < 300, `API failed: ${method} ${path} (${result.status})`);
 return result.data;
}
async function postUI(button, path) {
 const response = page.waitForResponse(r => new URL(r.url()).pathname === path && r.request().method() === 'POST');
 await button.click(); const received = await response; assert.equal(received.status(), 201, 'Creation did not commit'); return received.json();
}
async function openSubscription(title) {
 await page.goto(`${base}/subscriptions`);
 await page.getByRole('button',{name:`查看${title}订阅详情`,exact:true}).click();
 await page.getByRole('button',{name:'编辑绑定',exact:true}).waitFor();
}
try {
 await page.goto(base);
 const status = await page.evaluate(async () => (await fetch('/api/v1/auth/status')).json());
 if (!status.initialized) {
  const setup = await page.evaluate(async ({token}) => (await fetch('/api/v1/auth/setup',{method:'POST',headers:{'Content-Type':'application/json','X-Setup-Token':token},body:JSON.stringify({username:'ui-fixture-admin',password:'FixtureOnly-Config-2026!'})})).status, {token});
  assert.equal(setup,200,'Setup failed');
 } else {
  const login = await page.evaluate(async () => (await fetch('/api/v1/auth/login',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({username:'ui-fixture-admin',password:'FixtureOnly-Config-2026!'})})).status);
  assert.equal(login,200,'Fixture login failed; never run on a user database');
 }
 await page.reload();
 // Global policy editing: live HTTP commands, no stubbed config/rank outcomes.
 await page.goto(`${base}/settings`);await page.getByRole('button',{name:'质量与过滤策略',exact:true}).click();
 const policyPanel=page.getByRole('region',{name:'质量与过滤策略',exact:true});
 await policyPanel.getByRole('button',{name:'新建策略',exact:true}).click();
 await policyPanel.getByLabel('策略 ID',{exact:true}).fill('ui-policy');await policyPanel.getByLabel('策略名称',{exact:true}).fill('UI quality policy');
 await policyPanel.getByLabel('拒绝词（每行一个，标题包含匹配）',{exact:true}).fill('blocked');
 await policyPanel.getByLabel('资源标题',{exact:true}).fill('UI.Movie.2020.1080p.BluRay-blocked');await policyPanel.getByRole('button',{name:'预览草稿',exact:true}).click();
 await policyPanel.getByText('命中拒绝词: blocked',{exact:true}).waitFor();
 let beforePolicy=await api('/api/v1/policies');assert.ok(!beforePolicy.items.some(p=>p.id==='ui-policy'),'Preview created a policy');
 await postUI(policyPanel.getByRole('button',{name:'创建策略',exact:true}),'/api/v1/policies');
 await policyPanel.getByRole('status').filter({hasText:'版本 1 已发布'}).waitFor();
 const publicationPath='/api/v1/policies/ui-policy/revisions';let capturePublishKey='',dropPublish=true;
 await page.route(`**${publicationPath}`,async route=>{const key=route.request().headers()['idempotency-key'];if(dropPublish){dropPublish=false;capturePublishKey=key;const response=await route.fetch();assert.equal(response.status(),201);await route.abort('failed')}else{assert.equal(key,capturePublishKey,'Policy uncertain receipt changed key');await route.continue()}});
 await policyPanel.getByLabel('1080p 评分',{exact:true}).fill('155');await policyPanel.getByRole('button',{name:'发布新版本',exact:true}).click();
 await policyPanel.getByRole('alert').filter({hasText:'未确认'}).waitFor();
 assert.equal(await policyPanel.getByLabel('1080p 评分',{exact:true}).isDisabled(),true,'Uncertain publication allowed changed draft');
 assert.equal((await api('/api/v1/policies')).items.find(p=>p.id==='ui-policy').revision,2);
 await policyPanel.getByRole('button',{name:'重试同一发布命令',exact:true}).click();await policyPanel.getByRole('status').filter({hasText:'版本 2 已发布'}).waitFor();
 assert.equal((await api('/api/v1/policies')).items.find(p=>p.id==='ui-policy').revision,2);await page.unroute(`**${publicationPath}`);
 // Freeze old revision in panel, but another command wins before restoring it.
 await policyPanel.getByLabel('历史版本号',{exact:true}).fill('1');await policyPanel.getByRole('button',{name:'读取冻结版本',exact:true}).click();
 await policyPanel.getByRole('button',{name:'以新版本恢复历史 1',exact:true}).waitFor();
 const live=(await api('/api/v1/policies')).items.find(p=>p.id==='ui-policy');await api(publicationPath,'POST',{expected_revision:live.revision,rules:live.rules},'external-policy-version');
 await policyPanel.getByRole('button',{name:'以新版本恢复历史 1',exact:true}).click();await policyPanel.getByRole('alert').filter({hasText:'版本或请求已改变'}).waitFor();
 assert.equal(await policyPanel.getByRole('button',{name:'发布新版本',exact:true}).isDisabled(),true);await policyPanel.getByRole('button',{name:'重新加载策略（放弃草稿）',exact:true}).click();
 await policyPanel.getByLabel('历史版本号',{exact:true}).fill('1');await policyPanel.getByRole('button',{name:'读取冻结版本',exact:true}).click();await policyPanel.getByRole('button',{name:'以新版本恢复历史 1',exact:true}).click();
 await policyPanel.getByRole('status').filter({hasText:'版本 4 已发布'}).waitFor();
 assert.equal((await api('/api/v1/policies/ui-policy/revisions/4')).rules.resolution_order['1080p'],beforePolicy.items[0].rules.resolution_order['1080p']);
 // Receipt succeeds; read fails: block publication and recover by reading only.
 let failPolicyRead=false;await page.route('**/api/v1/policies',async route=>{if(failPolicyRead&&route.request().method()==='GET'){failPolicyRead=false;await route.fulfill({status:503,contentType:'application/json',body:'{"code":"UNAVAILABLE"}'})}else await route.continue()});
 failPolicyRead=true;await policyPanel.getByRole('button',{name:'发布新版本',exact:true}).click();await policyPanel.getByRole('alert').filter({hasText:'发布已确认'}).waitFor();assert.equal(await policyPanel.getByRole('button',{name:'发布新版本',exact:true}).isDisabled(),true);
 await policyPanel.getByRole('button',{name:'重新加载策略（放弃草稿）',exact:true}).click();await policyPanel.getByRole('group',{name:'当前草稿（基于版本 5）',exact:true}).waitFor();await page.unroute('**/api/v1/policies');
 const indexers = await api('/api/v1/indexers');
 const idx = indexers.items.find(item=>item.name==='UI Indexer A') ?? await api('/api/v1/indexers','POST',{name:'UI Indexer A',url:source,api_key:'fixture-key',categories:[]});
 const clients = await api('/api/v1/download-clients');
 const dl = clients.items.find(item=>item.name==='UI Downloader') ?? await api('/api/v1/download-clients','POST',{name:'UI Downloader',client_type:'qbittorrent',url:'http://127.0.0.1:9',username:'fixture',password:'fixture-only'});

 await page.goto(`${base}/sites`);
 await page.getByRole('button',{name:'编辑',exact:true}).first().click();
 const endpointForm=page.locator('.form-card');
 assert.equal(await endpointForm.getByLabel('凭据（留空保留已有值）',{exact:true}).inputValue(),'','Credentials were echoed on edit');
 await endpointForm.locator('input[type=url]').fill(source);
 const edited=page.waitForResponse(r=>new URL(r.url()).pathname===`/api/v1/indexers/${idx.id}`&&r.request().method()==='PUT');
 await endpointForm.getByRole('button',{name:'保存站点修改',exact:true}).click();
 const editResponse=await edited;assert.equal(editResponse.status(),200);
 const editBody=JSON.parse(editResponse.request().postData());assert.equal(editBody.api_key,'','Blank edit sent public fallback');assert.equal(editBody.categories,undefined,'Metadata edit rewrote categories');
 assert.equal((await api('/api/v1/indexers')).items.find(item=>item.id===idx.id).url,source);
 const baselineRSS=await api(`/api/v1/indexers/${idx.id}/rss`);
 await api(`/api/v1/indexers/${idx.id}/rss`,'PUT',{enabled:false,kind:'torznab',interval_seconds:300,expected_version:baselineRSS.config_version},crypto.randomUUID());
 await page.goto(`${base}/sites`);
 await page.getByRole('button',{name:'RSS 配置',exact:true}).first().click();
 const rss = page.getByRole('region',{name:'UI Indexer A RSS 配置'});
 await rss.getByRole('button',{name:'保存 RSS 配置',exact:true}).waitFor();
 const initial = await api(`/api/v1/indexers/${idx.id}/rss`);
 await api(`/api/v1/indexers/${idx.id}/rss`,'PUT',{enabled:false,kind:'torznab',interval_seconds:60,expected_version:initial.config_version},crypto.randomUUID());
 await rss.getByRole('checkbox',{name:'启用持久 RSS 轮询'}).check();
 await rss.getByRole('button',{name:'保存 RSS 配置',exact:true}).click();
 await rss.getByRole('alert').filter({hasText:'站点配置已改变'}).waitFor();
 assert.equal(await rss.getByRole('button',{name:'保存 RSS 配置',exact:true}).isDisabled(),true,'Stale draft must not retry without reload');
 await rss.getByRole('button',{name:'重新加载（放弃草稿）'}).click();
 await rss.getByRole('checkbox',{name:'启用持久 RSS 轮询'}).check();
 await rss.getByLabel('来源类型',{exact:true}).selectOption('rss');
 await rss.getByLabel('轮询间隔（秒）').fill('3600');
 await rss.getByLabel('完整 Feed URL',{exact:true}).fill(`${source}/feed?passkey=synthetic-ui-passkey`);
 await rss.getByRole('button',{name:'保存 RSS 配置',exact:true}).click();
 await rss.getByRole('status').filter({hasText:'RSS 配置已保存'}).waitFor();
 assert.equal(await rss.getByLabel('完整 Feed URL',{exact:true}).inputValue(),'','Secret must be cleared after success');
 const saved = await api(`/api/v1/indexers/${idx.id}/rss`);
 assert.equal(saved.enabled,true);assert.equal(saved.interval_seconds,3600);assert.ok(!JSON.stringify(saved).includes('synthetic-ui-passkey'));
 const receiptDeadline=Date.now()+20000;let receipt;
 do {receipt=await api(`/api/v1/indexers/${idx.id}/rss`);if(receipt.last_candidate_count===1&&feedCalls>=1&&receipt.last_success_at!==saved.last_success_at)break;await delay(200);} while(Date.now()<receiptDeadline);
 assert.equal(receipt.last_candidate_count,1,'Persistent RSS receipt did not commit');
 assert.notEqual(receipt.last_success_at,saved.last_success_at,'Reconfigured source only showed an old receipt');
 assert.ok(feedCalls>=1,'App.Run never polled the real HTTP feed');
 await rss.getByRole('button',{name:'重新加载（放弃草稿）'}).click();
 await rss.getByText('1',{exact:true}).waitFor();
 // Scheduled readonly health uses the actual App.Run network workers and receipt.
 assert.match(idx.id,/^[a-f0-9-]{36}$/);healthEndpointOK=false;
 await seedStatus(`UPDATE indexer SET health='offline',health_next_check_at=clock_timestamp(),next_query_at=clock_timestamp() WHERE id='${idx.id}'`);
 let observedHealth;const healthDeadline=Date.now()+15000;
 do {observedHealth=(await api('/api/v1/indexers')).items.find(x=>x.id===idx.id);if(observedHealth.health_reason==='unavailable')break;await delay(200)}while(Date.now()<healthDeadline);
 assert.equal(observedHealth.health_reason,'unavailable','App.Run did not persist failing health query');assert.ok(observedHealth.health_checked_at&&observedHealth.health_next_check_at);
 healthEndpointOK=true;await seedStatus(`UPDATE indexer SET health_next_check_at=clock_timestamp(),next_query_at=clock_timestamp() WHERE id='${idx.id}'`);
 const onlineDeadline=Date.now()+15000;do {observedHealth=(await api('/api/v1/indexers')).items.find(x=>x.id===idx.id);if(observedHealth.health==='online'&&observedHealth.health_reason==='ok')break;await delay(200)}while(Date.now()<onlineDeadline);
 assert.equal(observedHealth.health_reason,'ok','Actual successful empty health response did not recover');
 await page.getByRole('button',{name:'刷新列表',exact:true}).click();await page.getByText('最近观测在线',{exact:true}).waitFor();
 let failHealthList=true;await page.route('**/api/v1/indexers',async route=>{if(failHealthList&&route.request().method()==='GET'){failHealthList=false;await route.fulfill({status:503,contentType:'application/json',body:'{"code":"UNAVAILABLE"}'})}else await route.continue()});
 await page.getByRole('button',{name:'刷新列表',exact:true}).click();await page.getByRole('alert').filter({hasText:'站点状态读取失败'}).waitFor();assert.equal(await page.getByText('最近观测在线',{exact:true}).count(),0,'Health read failure retained old online');
 await page.getByRole('button',{name:'刷新列表',exact:true}).click();await page.getByText('最近观测在线',{exact:true}).waitFor();await page.unroute('**/api/v1/indexers');
 await page.screenshot({path:'/tmp/pulse-config-ui-desktop.png',fullPage:true});
 await rss.getByRole('checkbox',{name:'启用持久 RSS 轮询'}).uncheck();
 await rss.getByRole('button',{name:'保存 RSS 配置',exact:true}).click();
 await rss.getByRole('status').filter({hasText:'RSS 配置已保存'}).waitFor();
 assert.equal((await api(`/api/v1/indexers/${idx.id}/rss`)).enabled,false);

 await page.goto(`${base}/discover`);
 await page.locator('.movie-grid .media-card-wrap').first().waitFor();
 assert.equal(await page.locator('.movie-grid .media-card-wrap').count(),2,'Fixture cards selector must have a positive control');
 await page.getByText('UI Movie',{exact:true}).last().click();
 await page.getByRole('button',{name:'保存电影订阅',exact:true}).waitFor();
 const check = page.getByRole('checkbox',{name:/UI Indexer A/});
 assert.equal(await check.isChecked(),false,'Creation must not default to all endpoints');
 await check.check(); await page.getByLabel('下载器',{exact:true}).selectOption(dl.id);await page.getByLabel('质量与过滤策略',{exact:true}).selectOption('ui-policy');
 const movie = await postUI(page.getByRole('button',{name:'保存电影订阅',exact:true}),'/api/v1/subscriptions');
 assert.deepEqual(movie.indexer_ids,[idx.id]);assert.equal(movie.downloader_id,dl.id);assert.equal(movie.quality_policy_id,"ui-policy");assert.equal(movie.quality_policy_revision,5);
 await page.waitForURL('**/subscriptions');
 await openSubscription('UI Movie');
 await page.getByRole('dialog').getByText('电影目标状态',{exact:true}).waitFor();
 await page.keyboard.press('Shift+Tab');
 assert.equal(await page.evaluate(()=>document.querySelector('[role=dialog]').contains(document.activeElement)),true,'Focus escaped dialog');
 // Versioned UI pause: the request was committed but the browser lost receipt.
 const controlPath=`/api/v1/subscriptions/${movie.id}/control`;let dropControl=true,controlKey='';
 await page.route(`**${controlPath}`,async route=>{if(dropControl){dropControl=false;controlKey=route.request().headers()['idempotency-key'];const received=await route.fetch();assert.equal(received.status(),200);await route.abort('failed')}else {assert.equal(route.request().headers()['idempotency-key'],controlKey,'Unconfirmed state write changed key');await route.continue()}});
 await page.getByRole('dialog').getByRole('button',{name:'暂停订阅',exact:true}).click();const recovery=page.getByRole('region',{name:'订阅控制恢复',exact:true});await recovery.getByRole('alert').filter({hasText:'未确认'}).waitFor();await recovery.getByRole('button',{name:'重试同一控制命令',exact:true}).click();await page.getByRole('dialog').getByText('已暂停',{exact:true}).waitFor();await page.unroute(`**${controlPath}`);
 const pausedControl=await api(`/api/v1/subscriptions/${movie.id}`);assert.equal(pausedControl.intent_version,movie.intent_version+1,'Control replay submitted twice');
 // An external newer intent prevents overwriting using an old visible version.
 await api(controlPath,'PUT',{action:'state',state:'active',expected_version:pausedControl.intent_version},crypto.randomUUID());await page.getByRole('dialog').getByRole('button',{name:'恢复订阅',exact:true}).click();await recovery.getByRole('alert').filter({hasText:'版本或对象权限'}).waitFor();await recovery.getByRole('button',{name:'重新读取订阅控制状态',exact:true}).click();await page.getByRole('dialog').getByText('订阅有效',{exact:true}).waitFor();
 const beforeConfirmedPause=await api(`/api/v1/subscriptions/${movie.id}`);let failControlRead=true;await page.route(`**/api/v1/subscriptions/${movie.id}`,async route=>{if(failControlRead&&route.request().method()==='GET'){failControlRead=false;await route.fulfill({status:503,contentType:'application/json',body:'{"code":"UNAVAILABLE"}'})}else await route.continue()});await page.getByRole('dialog').getByRole('button',{name:'暂停订阅',exact:true}).click();await recovery.getByRole('alert').filter({hasText:'控制操作已确认'}).waitFor();assert.equal(await recovery.getByRole('button',{name:'重试同一控制命令',exact:true}).count(),0,'Confirmed write/read failure offered write retry');await recovery.getByRole('button',{name:'重新读取订阅控制状态',exact:true}).click();await page.unroute(`**/api/v1/subscriptions/${movie.id}`);assert.equal((await api(`/api/v1/subscriptions/${movie.id}`)).intent_version,beforeConfirmedPause.intent_version+1,'Read recovery mutated intent');

 await api(`/api/v1/subscriptions/${movie.id}/resume`,'POST');await page.reload();await openSubscription('UI Movie');
 const binding=page.getByRole('region',{name:'订阅端点配置'});
 await binding.getByRole('button',{name:'编辑绑定',exact:true}).click();
 await binding.getByRole('checkbox',{name:/UI Indexer A/}).uncheck();
 await api(`/api/v1/subscriptions/${movie.id}/pause`,'POST');
 await binding.getByRole('button',{name:'保存绑定',exact:true}).click();
 await binding.getByRole('alert').filter({hasText:'订阅已被其他操作修改'}).waitFor();
 await binding.getByRole('button',{name:'重新加载（放弃草稿）'}).click();
 await binding.getByText('已暂停，不会发起新的自动获取。',{exact:true}).waitFor();
 assert.equal(await binding.getByRole('checkbox',{name:/UI Indexer A/}).isChecked(),true,'Reload failed to show DB binding');
 // The server commits the first PUT; the test drops its receipt. Retry must reuse key.
 let dropReceipt=true;
 await page.route(`**/api/v1/subscriptions/${movie.id}/bindings`,async route=>{if(dropReceipt){dropReceipt=false;const response=await route.fetch();assert.equal(response.status(),200);await route.fulfill({status:503,contentType:'application/json',body:'{"code":"UNAVAILABLE","message":"dropped fixture receipt"}'});}else await route.continue();});
 const beforeRetry=await api(`/api/v1/subscriptions/${movie.id}`);
 await binding.getByLabel("质量与过滤策略",{exact:true}).selectOption("default");
 await binding.getByRole('checkbox',{name:/UI Indexer A/}).uncheck();
 await binding.getByRole('button',{name:'保存绑定',exact:true}).click();
 await binding.getByRole('alert').filter({hasText:'保存未确认'}).waitFor();
 await binding.getByRole('button',{name:'保存绑定',exact:true}).click();
 await binding.getByRole('status').filter({hasText:'端点与策略引用已保存'}).waitFor();
 const afterRetry=await api(`/api/v1/subscriptions/${movie.id}`);
 assert.equal(afterRetry.intent_version,beforeRetry.intent_version+1);assert.equal(afterRetry.state,'paused');assert.deepEqual(afterRetry.indexer_ids,[]);assert.equal(afterRetry.quality_policy_id,"default");assert.equal(afterRetry.quality_policy_revision,1);assert.deepEqual(afterRetry.wanted_items.map(w=>[w.id,w.is_skipped]),beforeRetry.wanted_items.map(w=>[w.id,w.is_skipped]));
 let loseRefresh=true;
 await page.route(`**/api/v1/subscriptions/${movie.id}`,async route=>{if(loseRefresh&&route.request().method()==='GET'){loseRefresh=false;await route.fulfill({status:503,contentType:'application/json',body:'{"code":"UNAVAILABLE"}'});}else await route.continue();});
 await binding.getByRole('checkbox',{name:/UI Indexer A/}).check();
 await binding.getByRole('button',{name:'保存绑定',exact:true}).click();
 await binding.getByRole('alert').filter({hasText:'保存已确认'}).waitFor();
 assert.equal(await binding.getByRole('button',{name:'保存绑定',exact:true}).isDisabled(),true,'Confirmed write/read failure invited a duplicate save');
 const confirmed=await api(`/api/v1/subscriptions/${movie.id}`);
 await binding.getByRole('button',{name:'重新加载（放弃草稿）'}).click();
 assert.equal((await api(`/api/v1/subscriptions/${movie.id}`)).intent_version,confirmed.intent_version,'Reload submitted another intent');
 await binding.getByRole('checkbox',{name:/UI Indexer A/}).uncheck();
 await binding.getByRole('button',{name:'保存绑定',exact:true}).click();
 await binding.getByRole('status').filter({hasText:'端点与策略引用已保存'}).waitFor();
 await page.reload();await page.getByRole('button',{name:'查看UI Movie订阅详情',exact:true}).click();await page.getByRole('button',{name:'编辑绑定',exact:true}).click();
 assert.equal(await page.getByRole('checkbox',{name:/UI Indexer A/}).isChecked(),false,'Refresh resurrected old selection');

 // Actual PG imported observation -> unavailable status must remove visible green.
 assert.equal((await api(`/api/v1/subscriptions/${movie.id}`)).state,'paused');
 assert.deepEqual((await api(`/api/v1/subscriptions/${movie.id}`)).indexer_ids,[]);
 const fileID=crypto.randomUUID(),factID=crypto.randomUUID(),unitID=movie.wanted_items[0].unit_id;
 for(const id of [movie.root_id,unitID,fileID,factID])assert.match(id,/^[a-f0-9-]{36}$/);
 statusCleanup=`BEGIN;UPDATE library_root SET health='online',baseline_completed=false WHERE id='${movie.root_id}';UPDATE library_file SET retired_at=now() WHERE id='${fileID}';UPDATE unit_fact SET revoked_at=now() WHERE id='${factID}';COMMIT;`;
 await seedStatus(`BEGIN;UPDATE library_root SET baseline_completed=true WHERE id='${movie.root_id}';INSERT INTO library_file(id,root_id,rel_path,size_bytes,mtime_ns,file_fingerprint,last_mutation_seq) VALUES('${fileID}','${movie.root_id}','UI-fixture-owned.mkv',100,1,'fixture',0);INSERT INTO library_file_unit(file_id,unit_id) VALUES('${fileID}','${unitID}');INSERT INTO unit_fact(id,unit_id,stage,source_file_id,root_id,quality_profile) VALUES('${factID}','${unitID}','imported','${fileID}','${movie.root_id}','{"resolution":"1080p","source":"Bluray"}');COMMIT;`);
 const movieStatus=page.getByRole('region',{name:'当前目标状态'});
 await movieStatus.getByRole('button',{name:'刷新状态',exact:true}).click();
 await page.getByRole('dialog').locator('.episode-tile-card.tile-satisfied').waitFor();
 assert.equal((await api(`/api/v1/subscriptions/${movie.id}/status`)).items[0].is_satisfied,true);
 // Explicit synthetic completed round: explanation writes only pure cache,
 // never a download job. It evaluates current policy and live root ownership.
 const explainRound=crypto.randomUUID(),allowedCandidate=crypto.randomUUID(),rejectedCandidate=crypto.randomUUID();for(const id of[explainRound,allowedCandidate,rejectedCandidate])assert.match(id,/^[a-f0-9-]{36}$/);
 const wantedID=movie.wanted_items[0].id;assert.match(wantedID,/^[a-f0-9-]{36}$/);
 await seedStatus(`BEGIN;INSERT INTO release_candidate(id,indexer_id,guid,title,link_encrypted,size_bytes,quality_profile,source_config_version) SELECT '${allowedCandidate}',id,'ui-candidate-allow','UI.Movie.2020.1080p.BluRay',decode('0001','hex'),100,'{"resolution":"1080p","source":"Bluray"}',config_version FROM indexer WHERE id='${idx.id}';INSERT INTO release_candidate(id,indexer_id,guid,title,link_encrypted,size_bytes,quality_profile,source_config_version) SELECT '${rejectedCandidate}',id,'ui-candidate-unknown','UI.Other.2020.2160p.Remux',decode('0001','hex'),100,'{"resolution":"2160p","source":"Remux"}',config_version FROM indexer WHERE id='${idx.id}';INSERT INTO search_round(id,subscription_id,wanted_item_id,snapshot_intent_version,snapshot_policy_revision,state,deadline,candidate_ids,decision_snapshot) SELECT '${explainRound}',id,'${wantedID}',intent_version,quality_policy_revision,'expired',clock_timestamp(),jsonb_build_array('${allowedCandidate}','${rejectedCandidate}'),'{"scheduled":false}' FROM subscription WHERE id='${movie.id}';COMMIT;`);
 const explanation=page.getByRole('region',{name:'电影候选解释',exact:true});await explanation.getByRole('button',{name:'查看候选解释',exact:true}).click();await explanation.getByText('规则通过 · 当前允许规则通过',{exact:true}).waitFor();await explanation.getByText('证据不足 · 标题、年份或身份匹配证据不足',{exact:true}).waitFor();
 const candidatePath=`/api/v1/subscriptions/${movie.id}/wanted/${wantedID}/candidates`;const firstExplain=await api(candidatePath);assert.equal(firstExplain.items.length,2);assert.equal(firstExplain.items.every(x=>x.available===false),true,'Explanation authorized download despite owned/paused target');assert.equal(firstExplain.items.some(x=>x.title.includes('UI.Movie')),true);
 let failExplain=true;await page.route(`**${candidatePath}`,async route=>{if(failExplain){failExplain=false;await route.fulfill({status:503,contentType:'application/json',body:'{"code":"UNAVAILABLE"}'})}else await route.continue()});await explanation.getByRole('button',{name:'刷新候选解释',exact:true}).click();await explanation.getByRole('alert').filter({hasText:'读取失败'}).waitFor();assert.equal(await explanation.getByText('规则通过 · 当前允许规则通过',{exact:true}).count(),0,'Explanation read fault retained old accept');await explanation.getByRole('button',{name:'刷新候选解释',exact:true}).click();await explanation.getByText('纯规则结果来自相同完整输入缓存',{exact:false}).first().waitFor();await page.unroute(`**${candidatePath}`);

 // The production projector, not a fabricated cached row, must establish green.
 await api(`/api/v1/subscriptions/${movie.id}/resume`,'POST');
 const projectedDeadline=Date.now()+15000;let projected;
 do {projected=await api(`/api/v1/subscriptions/${movie.id}`);if(projected.wanted_items[0].is_satisfied)break;await delay(200);} while(Date.now()<projectedDeadline);
 assert.equal(projected.wanted_items[0].is_satisfied,true,'App.Run never wrote a current complete input stamp');
 await seedStatus(`UPDATE library_root SET health='offline' WHERE id='${movie.root_id}';`);
 const invalidated=await api(`/api/v1/subscriptions/${movie.id}`);
 assert.equal(invalidated.wanted_items[0].is_satisfied,false,'Root revision commit retained cached green');
 assert.ok(!invalidated.wanted_items[0].current_fact_stage,'Root revision commit retained old imported projection');
 await movieStatus.getByRole('button',{name:'刷新状态',exact:true}).click();
 await movieStatus.getByText('目标库离线，拥有状态未知',{exact:true}).waitFor();
 assert.equal(await page.getByRole('dialog').locator('.episode-tile-card.tile-satisfied').count(),0,'Root offline refreshed to old green');
 await seedStatus(`UPDATE library_root SET health='online' WHERE id='${movie.root_id}';`);
 await api(`/api/v1/subscriptions/${movie.id}/pause`,'POST');
 await movieStatus.getByRole('button',{name:'刷新状态',exact:true}).click();
 await page.getByRole('dialog').locator('.episode-tile-card.tile-satisfied').waitFor();
 let dropGreenStatus=true;
 await page.route(`**/api/v1/subscriptions/${movie.id}/status`,async route=>{if(dropGreenStatus){dropGreenStatus=false;await route.fulfill({status:503,contentType:'application/json',body:'{"code":"UNAVAILABLE"}'});}else await route.continue();});
 await movieStatus.getByRole('button',{name:'刷新状态',exact:true}).click();
 await movieStatus.getByRole('alert').filter({hasText:'读取失败'}).waitFor();
 assert.equal(await page.getByRole('dialog').locator('.episode-tile-card.tile-satisfied').count(),0,'Unavailable status retained the previous green imported observation');
 await seedStatus(`BEGIN;UPDATE library_root SET baseline_completed=false WHERE id='${movie.root_id}';UPDATE library_file SET retired_at=now() WHERE id='${fileID}';UPDATE unit_fact SET revoked_at=now() WHERE id='${factID}';COMMIT;`);
 await movieStatus.getByRole('button',{name:'刷新状态',exact:true}).click();
 await movieStatus.getByText('目标库基线扫描未完成',{exact:true}).waitFor();
 // A foreign-root unresolved physical lane protects this unit without claiming
 // this paused, unbound target has a task or imported file. Explicit PG fixture.
 const globalOp=crypto.randomUUID(),globalTask=crypto.randomUUID(),globalLane=crypto.randomUUID(),globalCandidate=crypto.randomUUID();
 for(const id of[globalOp,globalTask,globalLane,globalCandidate])assert.match(id,/^[a-f0-9-]{36}$/);
 const foreignRoot=(await api('/api/v1/library-roots')).items.find(root=>root.id!==movie.root_id).id;assert.match(foreignRoot,/^[a-f0-9-]{36}$/);
 await seedStatus(`BEGIN;UPDATE library_root SET baseline_completed=true WHERE id='${movie.root_id}';INSERT INTO release_candidate(id,indexer_id,guid,title,link_encrypted,size_bytes,quality_profile,source_config_version) SELECT '${globalCandidate}','${idx.id}','ui-global-lane','UI.Movie.2020.1080p.BluRay',decode('0001','hex'),100,'{"resolution":"1080p","source":"Bluray"}',config_version FROM indexer WHERE id='${idx.id}';INSERT INTO download_task(id,operation_id,client_id,candidate_id,info_hash,state,save_path) VALUES('${globalTask}','${globalOp}','${dl.id}','${globalCandidate}','ui-global','unknown','/fixture/no-read');INSERT INTO acquisition_reservation(id,unit_id,operation_id,candidate_id,task_id,source_root_id,source_generation,source_stamp) SELECT '${globalLane}','${unitID}','${globalOp}','${globalCandidate}','${globalTask}',id,generation,'explicit browser unknown global fixture' FROM library_root WHERE id='${foreignRoot}';COMMIT;`);
 const protectedStatus=await api(`/api/v1/subscriptions/${movie.id}/status`);assert.equal(protectedStatus.items[0].reservation_protected,true);assert.equal(protectedStatus.items[0].is_satisfied,false);assert.equal(protectedStatus.items[0].wait_reason,'shared_acquisition_unknown');
 await movieStatus.getByRole('button',{name:'刷新状态',exact:true}).click();await page.getByRole('dialog').getByText('全局获取结果不明，保持保护等待复核',{exact:true}).first().waitFor();assert.equal(await page.getByRole('dialog').locator('.episode-tile-card.tile-satisfied').count(),0,'Foreign-root global lane became owned');
 const ownRequirement=crypto.randomUUID(),foreignRequirement=crypto.randomUUID();const ownActor=(await api('/api/v1/auth/status')).current_user.id;
 for(const id of[ownActor,ownRequirement,foreignRequirement])assert.match(id,/^[a-f0-9-]{36}$/);
 await seedStatus(`BEGIN;INSERT INTO download_requirement(id,operation_id,unit_id,root_id,actor_type,actor_id,submission_fence) VALUES('${ownRequirement}','${globalOp}','${unitID}','${movie.root_id}','user','${ownActor}','{"fixture":"unknown user demand"}'),('${foreignRequirement}','${globalOp}','${unitID}','${foreignRoot}','user','unrelated-fixture-owner','{"fixture":"other user demand"}');COMMIT;`);
 await page.goto(`${base}/activity`);await page.getByLabel('操作 ID',{exact:true}).fill(globalOp);await page.getByRole('button',{name:'查询 / 刷新',exact:true}).click();
 const ownPanel=page.getByRole('region',{name:'自身入库需求',exact:true});await ownPanel.getByRole('button',{name:'撤销自身入库需求',exact:true}).waitFor();
 const ownRows=(await api(`/api/v1/operations/${globalOp}/requirements`)).items;assert.equal(ownRows.length,1,'Own list exposed another actor');assert.equal(ownRows[0].id,ownRequirement);
 const withdrawal=`/api/v1/operations/${globalOp}/requirements/${ownRequirement}/withdraw`;let dropWithdrawal=true,withdrawalKey='';
 await page.route(`**${withdrawal}`,async route=>{const key=route.request().headers()['idempotency-key'];if(dropWithdrawal){dropWithdrawal=false;withdrawalKey=key;const received=await route.fetch();assert.equal(received.status(),200);assert.equal((await received.json()).physical_released,false);await route.abort('failed')}else{assert.equal(key,withdrawalKey,'Uncertain withdrawal changed command key');await route.continue()}});
 await ownPanel.getByRole('button',{name:'撤销自身入库需求',exact:true}).click();await ownPanel.getByRole('button',{name:'确认撤销自身需求',exact:true}).click();await ownPanel.getByRole('alert').filter({hasText:'未确认'}).waitFor();
 await ownPanel.getByRole('button',{name:'重试同一撤销命令',exact:true}).click();await ownPanel.getByRole('status').filter({hasText:'物理保护保持'}).waitFor();await ownPanel.getByText('当前需求：已撤销',{exact:true}).waitFor();
 assert.equal((await api(`/api/v1/operations/${globalOp}/requirements`)).items[0].state,'released');await page.unroute(`**${withdrawal}`);
 assert.equal((await api(`/api/v1/subscriptions/${movie.id}/status`)).items[0].reservation_protected,true,'Withdrawing own target released unrelated unknown physical action');
 await openSubscription('UI Movie');

 await seedStatus(`BEGIN;UPDATE acquisition_reservation SET released_at=clock_timestamp() WHERE id='${globalLane}';UPDATE library_root SET baseline_completed=false WHERE id='${movie.root_id}';COMMIT;`);
 await movieStatus.getByRole('button',{name:'刷新状态',exact:true}).click();await movieStatus.getByText('目标库基线扫描未完成',{exact:true}).waitFor();
 await page.keyboard.press('Escape');await page.getByRole('dialog').waitFor({state:'hidden'});
 assert.equal(await page.evaluate(()=>document.activeElement.getAttribute('aria-label')),'查看UI Movie订阅详情','Focus did not return to trigger');
 await page.goto(`${base}/discover`);await page.getByText('UI Show',{exact:true}).last().click();
 await page.getByRole('button',{name:'保存剧集订阅（自动获取尚未启用）',exact:true}).waitFor();
 assert.ok(await page.locator('img').evaluateAll(images=>images.some(image=>image.naturalWidth>0)),'Fixture poster not rendered');
 const show=await postUI(page.getByRole('button',{name:'保存剧集订阅（自动获取尚未启用）',exact:true}),'/api/v1/subscriptions');
 await page.waitForURL('**/subscriptions');await openSubscription('UI Show');
 await page.getByRole('button',{name:/第 2 季/}).click();
 await page.getByText('第 2 季 · 剧集追踪状态',{exact:true}).waitFor();
 const showDialog=page.getByRole('dialog');
 const beforeTitle=await showDialog.getByRole('heading',{name:'UI Show',exact:true}).textContent();
 const showBinding=page.getByRole('region',{name:'订阅端点配置'});await showBinding.getByRole('button',{name:'编辑绑定',exact:true}).click();
 await showBinding.getByRole('checkbox',{name:/UI Indexer A/}).check();await showBinding.getByRole('button',{name:'保存绑定',exact:true}).click();
 await showBinding.getByRole('status').filter({hasText:'端点与策略引用已保存'}).waitFor();
 assert.equal(await page.getByText('第 2 季 · 剧集追踪状态',{exact:true}).isVisible(),true,'Saving reset season selection');
 const showDetails=await api(`/api/v1/subscriptions/${show.id}`);
 assert.equal(showDetails.wanted_items.length,3,'Binding save changed TV scope');
 assert.equal(showDetails.media_type,'tv');assert.equal(showDetails.title,'UI Show');
 assert.deepEqual(showDetails.wanted_items.map(item=>item.unit_key).sort(),['S01E01','S01E02','S02E01']);
 assert.equal(await showDialog.getByRole('heading',{name:'UI Show',exact:true}).textContent(),beforeTitle,'Binding save lost title');
 await showBinding.getByText('剧集尚未启用自动获取，绑定仅保存为配置。',{exact:true}).waitFor();
 assert.equal(await showDialog.getByText('全网自动周期检索中',{exact:true}).count(),0,'TV claims unsupported automatic searches');
 const currentPanel=showDialog.getByRole('region',{name:'当前目标状态'});
 await currentPanel.getByText('目标库基线扫描未完成',{exact:true}).waitFor();
 await showDialog.getByText('3 个目标尚未确认拥有，不表示文件已丢失。',{exact:true}).waitFor();
 const liveReport=await api(`/api/v1/subscriptions/${show.id}/status`);
 assert.equal(liveReport.items.length,3);assert.ok(liveReport.items.every(item=>item.wait_reason==='baseline_pending'&&!item.is_satisfied));
 assert.ok(liveReport.blockers.includes('automation_unavailable'));
 let failStatusOnce=true;
 await page.route(`**/api/v1/subscriptions/${show.id}/status`,async route=>{if(failStatusOnce){failStatusOnce=false;await route.fulfill({status:503,contentType:'application/json',body:'{"code":"UNAVAILABLE"}'});}else await route.continue();});
 await currentPanel.getByRole('button',{name:'刷新状态',exact:true}).click();
 await currentPanel.getByRole('alert').filter({hasText:'读取失败'}).waitFor();
 assert.equal(await showDialog.locator('.episode-tile-card.tile-satisfied').count(),0,'Status read failure retained old green projected completion');
 await currentPanel.getByRole('button',{name:'刷新状态',exact:true}).click();
 await currentPanel.getByText('目标库基线扫描未完成',{exact:true}).waitFor();
 assert.equal(await page.getByRole('button',{name:/第 2 季/}).count(),1,'Season navigation was lost after saving');
 assert.equal(await showDialog.locator('.sub-hero-poster-img').evaluate(image=>image.naturalWidth>0),true,'Binding update lost the rendered poster');
 // Scope evolution is independent from user-skip and never resumes pause.
 await api(`/api/v1/subscriptions/${show.id}/pause`,'POST');await page.reload();await openSubscription('UI Show');await page.getByRole('button',{name:/第 2 季/}).click();
 const scope=page.getByRole('region',{name:'订阅范围与跳过',exact:true});await scope.getByRole('button',{name:'编辑范围与跳过',exact:true}).click();await scope.getByRole('button',{name:'保存目标范围',exact:true}).waitFor();
 if(await scope.getByRole('checkbox',{name:'第 2 季',exact:true}).isChecked())await scope.getByRole('checkbox',{name:'第 2 季',exact:true}).uncheck();await scope.getByRole('radio',{name:'所选季中的全部单元（包含今后新增单元）',exact:true}).check();await scope.getByRole('button',{name:'保存目标范围',exact:true}).click();await scope.getByRole('status').filter({hasText:'控制配置已保存'}).waitFor();
 const expanded=await api(`/api/v1/subscriptions/${show.id}`);assert.equal(expanded.wanted_items.length,3);assert.equal(expanded.state,'paused');const skippedUnit=expanded.wanted_items.find(w=>w.unit_key==='S01E01');
 await scope.getByText('S01E01 · 纳入范围',{exact:true}).locator('..').getByRole('button',{name:'设为用户跳过',exact:true}).click();await scope.getByText('S01E01 · 用户跳过',{exact:true}).waitFor();
 await scope.getByRole('radio',{name:'仅明确选择的单元',exact:true}).check();await scope.getByRole('checkbox',{name:'S02E01',exact:true}).check();const scopePath=`/api/v1/subscriptions/${show.id}/control`;let dropScope=true,scopeKey='';await page.route(`**${scopePath}`,async route=>{if(dropScope){dropScope=false;scopeKey=route.request().headers()['idempotency-key'];const response=await route.fetch();assert.equal(response.status(),200);await route.abort('failed')}else{assert.equal(route.request().headers()['idempotency-key'],scopeKey);await route.continue()}});
 await scope.getByRole('button',{name:'保存目标范围',exact:true}).click();await scope.getByRole('alert').filter({hasText:/保存.*未确认/}).waitFor();await scope.getByRole('button',{name:'重试同一范围或跳过命令',exact:true}).click();await scope.getByRole('status').filter({hasText:'控制配置已保存'}).waitFor();await page.unroute(`**${scopePath}`);
 const narrowed=await api(`/api/v1/subscriptions/${show.id}`);assert.equal(narrowed.wanted_items.length,3,'Narrow scope deleted historical units');assert.equal(narrowed.wanted_items.find(w=>w.unit_id===skippedUnit.unit_id).user_skipped,true);assert.equal(narrowed.wanted_items.find(w=>w.unit_key==='S01E02').scope_excluded,true);assert.equal(narrowed.state,'paused');
 await scope.getByRole('radio',{name:'所选季中的全部单元（包含今后新增单元）',exact:true}).check();await scope.getByRole('button',{name:'保存目标范围',exact:true}).click();await scope.getByText('S01E02 · 纳入范围',{exact:true}).waitFor();const allAgain=await api(`/api/v1/subscriptions/${show.id}`);assert.equal(allAgain.wanted_items.find(w=>w.unit_id===skippedUnit.unit_id).user_skipped,true);assert.equal(allAgain.wanted_items.find(w=>w.unit_key==='S01E02').is_skipped,false);assert.equal(allAgain.state,'paused');assert.ok(await page.getByRole('button',{name:/第 2 季/}).count());await scope.getByRole('button',{name:'收起范围编辑',exact:true}).click();

 await page.setViewportSize({width:390,height:844});
 assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth),false,'Mobile document overflow');
 if(await showBinding.getByRole('button',{name:'收起配置',exact:true}).count())await showBinding.getByRole('button',{name:'收起配置',exact:true}).click();
 await showDialog.locator('.episode-tile-card').first().scrollIntoViewIfNeeded();
 assert.equal(await showDialog.locator('.episode-tile-card').first().isVisible(),true,'Current season tile not visible on mobile');
 await page.screenshot({path:'/tmp/pulse-config-ui-mobile.png',fullPage:true});
 await page.reload();await page.getByRole('button',{name:'查看UI Show订阅详情',exact:true}).click();
 await page.getByRole('button',{name:'编辑绑定',exact:true}).click();
 assert.equal(await page.getByRole('checkbox',{name:/UI Indexer A/}).isChecked(),true,'List refresh dropped persisted binding');

 await page.goto(`${base}/discover`);discoveryMode='empty';await page.reload();await page.getByRole('heading',{name:'暂无推荐影视',exact:true}).waitFor();
 assert.equal(await page.locator('.movie-grid .media-card-wrap').count(),0,'Empty response fabricated movie cards');
 discoveryMode='failed';await page.reload();await page.getByRole('alert').filter({hasText:'推荐读取失败'}).waitFor();
 assert.equal(await page.getByText('Fight Club (搏击俱乐部)',{exact:true}).count(),0,'Metadata failure showed demo movies');
 // Inspect after viewport transitions settle, not halfway through drawer transform.
 await page.goto(`${base}/settings`);await page.getByRole('button',{name:'质量与过滤策略',exact:true}).click();await page.getByLabel('当前策略',{exact:true}).selectOption('ui-policy');
 await page.getByRole('group',{name:'当前草稿（基于版本 5）',exact:true}).waitFor();
 for(const [name,width,height]of [['desktop',1365,900],['mobile',390,844]]){await page.setViewportSize({width,height});if(width<1024)await page.waitForFunction(()=>document.querySelector('.sidebar').getBoundingClientRect().right<=1);assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth),false,'Policy configuration horizontal overflow');await page.screenshot({path:`/tmp/pulse-policy-${name}.png`,fullPage:true})}

 assert.deepEqual(pageErrors,[],'Browser had uncaught React errors');
 console.log('PASS: real HTTP/PG policies/preview/publication/replay/frozen restore and subscription reference, configuration and status, actual endpoint health failure/backoff/recovery and list read failure, RSS receipt, safe binding replay, precise cached green -> root revision invalidation, imported green -> 503 unknown, current baseline reasons, TV scope/posters, mobile/keyboard, empty/error discovery');
} finally { try { if(statusCleanup)await seedStatus(statusCleanup); } finally { await context.close(); await browser.close(); await new Promise(resolve=>fixture.close(resolve)); } }
