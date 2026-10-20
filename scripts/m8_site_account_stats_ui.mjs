import assert from 'node:assert/strict';
import {createRequire} from 'node:module';
import {mkdir} from 'node:fs/promises';
const load = createRequire(process.env.PULSE_M8_STATS_UI_REPO + '/web/package.json');
const {chromium} = load('playwright-core');
const setup = JSON.parse(process.env.PULSE_M8_STATS_UI_SETUP);
const {base, sourceID, sourceVersion, unknownID} = setup;
const shots = process.env.PULSE_M8_STATS_UI_SHOTS;
let phase = 'launch', browser, context, timer;
const routeErrors = [], pageErrors = [], gates = new Set();
let statsReads = 0;
let requests = 0, mutations = 0, authWrites = 0, mode = 'native', held = null;
const statsPath = '/api/v1/indexers/' + sourceID + '/account-stats';
const delay = ms => new Promise(resolve => setTimeout(resolve, ms));
async function eventually(check, message) {
  const end = Date.now() + 8000;
  while (Date.now() < end) { if (await check()) return; await delay(25); }
  assert.fail(message);
}
function gate() {
  let mark, release, timeout;
  const ready = new Promise(resolve => { mark = resolve; });
  const wait = new Promise((resolve, reject) => {
    release = resolve;
    timeout = setTimeout(() => reject(new Error('bounded response gate')), 12000);
  });
  wait.catch(() => {});
  const g = {ready, mark, wait, release() { clearTimeout(timeout); release(); gates.delete(g); }};
  gates.add(g); return g;
}
async function run() {
  // Playwright owns an ephemeral Chrome profile and debugging pipe; no shared port/profile.
  browser = await chromium.launch({executablePath:process.env.PULSE_M5_CHROME || '/usr/bin/google-chrome',headless:true,timeout:15000,
    args:['--no-sandbox','--disable-dev-shm-usage','--disable-background-networking']});
  context = await browser.newContext({viewport:{width:1440,height:1000},serviceWorkers:'block'});
  await context.addCookies([{name:'pulsemedia_session',value:setup.cookie,url:base,httpOnly:true,sameSite:'Strict'}]);
  delete setup.cookie;
  const page = await context.newPage(); page.setDefaultTimeout(8000);
  page.on('pageerror', () => pageErrors.push('pageerror'));
  page.on('request', request => {
    const url = new URL(request.url());
    if (url.origin === base && /^\/api\/v1\/indexers\/[^/]+\/account-stats$/.test(url.pathname)) statsReads++;
    if (url.origin !== base || !url.pathname.startsWith('/api/') || !['POST','PUT','PATCH','DELETE'].includes(request.method())) return;
    if (['/api/v1/auth/login','/api/v1/auth/logout','/api/v1/auth/setup'].includes(url.pathname)) authWrites++;
    else mutations++;
  });
  let active = 0, nativeReplies = 0;
  await page.route('**' + statsPath, async route => {
    requests++; active++;
    const state = mode, currentGate = held; held = null;
    let captured = false;
    try {
      assert.equal(route.request().method(),'GET');
      const response = await route.fetch({timeout:8000,maxRetries:0});
      assert.equal(response.status(),200);
      assert.equal(response.headers()['cache-control'],'private, no-store');
      const body = await response.json();
      assert.equal(body.observation.config.indexer_id,sourceID);
      assert.equal(body.observation.sample_source_version,sourceVersion);
      assert.equal(body.observation.stats.account_id,'42');
      nativeReplies++;
      if (currentGate) { captured = true; currentGate.mark(); await currentGate.wait; }
      // These are semantic state-model examples, never evidence of native failures.
      if (state !== 'native') {
        const o = body.observation;
        if (state === 'zero') { o.stats.uploaded.value='0.00'; o.stats.downloaded.value='0.00'; o.stats.ratio={state:'finite',value:'0.000'}; o.stats.bonus='0.0'; }
        if (['unknown','infinite','undefined'].includes(state)) o.stats.ratio={state};
        if (state === 'stale') { o.stale=true; o.collection_state='failed'; o.failure_code='rate_limited'; }
        await route.fulfill({response,json:body});
      } else await route.fulfill({response});
    } catch (error) {
      // Only late delivery to a disposed, owned request may fail. Never double-handle it.
      if (!(captured && /aborted|closed|disposed|Invalid InterceptionId|No resource with given identifier/i.test(String(error.message)))) routeErrors.push('stats-route');
    } finally { active--; }
  });
  phase = 'mount';
  await page.goto(base+'/sites');
  const list = await context.request.get(base+'/api/v1/indexers'); assert.equal(list.status(),200);
  const items = (await list.json()).items;
  const byID = id => { const site=items.find(s=>s.id===id); assert(site,'owned source missing'); return page.locator('.indexer-row').filter({has:page.getByText(site.name,{exact:true})}); };
  const card = byID(sourceID), unknownCard = byID(unknownID);
  assert.equal(items.find(s=>s.id===sourceID).config_version,sourceVersion);
  const panel = card.getByRole('region',{name:'账号统计',exact:true});
  const toggle = panel.getByRole('button',{name:'账号统计',exact:true});
  const refresh = panel.getByRole('button',{name:'刷新',exact:true});
  const values = () => panel.locator('dd').allTextContents();
  const expectValues = expected => eventually(async()=>JSON.stringify(await values())===JSON.stringify(expected),'metric values differ');
  const fresh = ['约 2.00 GB','约 1.00 GB','2.000','1234.5'];
  await toggle.waitFor(); assert.equal(await card.count(),1); assert.equal(await unknownCard.count(),1);
  assert.equal(await toggle.getAttribute('aria-expanded'),'false');
  await delay(150); assert.equal(statsReads,0,'closed panels issued stats GET');
  phase='fresh-native'; await toggle.click(); await expectValues(fresh);
  assert.match(await panel.getByRole('status').innerText(),/已保存统计/);
  assert(!/陈旧/.test(await panel.innerText()));
  const stamp = await panel.locator('time').getAttribute('datetime'); assert(stamp && Number.isFinite(Date.parse(stamp)));
  const count = requests; await refresh.click(); await expectValues(fresh); assert.equal(requests,count+1);
  async function bounds(width, name) {
    await page.setViewportSize({width,height:1000});
    await page.evaluate(()=>new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve))));
    await page.waitForFunction(() => {
      const navigation = document.getElementById('app-navigation');
      return !navigation || navigation.getAnimations().every(animation => animation.playState !== 'running');
    });
    const errors = await panel.evaluate(root => {
      const failures=[];
      if(document.documentElement.scrollWidth>innerWidth+1) failures.push('horizontal overflow');
      const box=root.getBoundingClientRect();
      for(const element of [root,...root.querySelectorAll('button,dl,dl div,dt,dd,p')]) {
        const r=element.getBoundingClientRect(); if(!r.width||!r.height)continue;
        if(r.left < -1 || r.right > innerWidth+1 || r.left < box.left-1 || r.right > box.right+1) failures.push('panel bounds');
        if(element.tagName==='BUTTON'&&(r.width<44||r.height<44))failures.push('target below 44px');
        if(element.scrollWidth>element.clientWidth+1) failures.push('element clipping');
        if(['DT','DD','P'].includes(element.tagName)) {
          const range=document.createRange(); range.selectNodeContents(element);
          for(const text of range.getClientRects()) if(text.left<r.left-1||text.right>r.right+1)failures.push('text clipping');
        }
      }
      return failures.length ? [{
        rootWidth: box.width, failures: [...new Set(failures)],
        elements: [root,...root.querySelectorAll('button,dl,dl div,dt,dd,p')].map(element => {
          const rect = element.getBoundingClientRect();
          return {tag: element.tagName, x: rect.x, width: rect.width, height: rect.height, client: element.clientWidth, scroll: element.scrollWidth};
        }),
      }] : [];
    });
    if (errors.length) console.error('M8_STATS_UI_LAYOUT_DETAIL ' + JSON.stringify({width, errors}));
    assert.deepEqual(errors, [], 'viewport ' + width);
    await page.screenshot({path:shots+'/'+name+'-'+width+'.png',fullPage:true});
  }
  await mkdir(shots,{recursive:true});
  for(const width of [375,320,768,1440]) await bounds(width,'fresh');
  phase='cancel-reopen';
  let g=gate(); held=g; await refresh.click(); await g.ready;
  assert.equal(await panel.locator('dd').count(),0,'old values retained while pending');
  await toggle.click(); assert.equal(await panel.locator('dd').count(),0);
  g.release(); await eventually(()=>active===0,'cancelled route did not drain');
  g=gate(); held=g; const before=requests; await toggle.click(); await g.ready;
  assert.equal(requests,before+1,'reopen must read again'); assert.equal(await panel.locator('dd').count(),0,'reopen reused old data');
  g.release(); await expectValues(fresh);
  phase='unconfigured-real-API';
  const unknownPanel=unknownCard.getByRole('region',{name:'账号统计',exact:true});
  await unknownPanel.getByRole('button',{name:'账号统计',exact:true}).click();
  await unknownPanel.getByText('尚未配置统计采集',{exact:true}).waitFor();
  assert.deepEqual(await unknownPanel.locator('dd').allTextContents(),['未知','未知','未知','未知']);
  await unknownPanel.getByRole('button',{name:'账号统计',exact:true}).click();
  phase='semantic-state-model';
  mode='zero'; await refresh.click(); await expectValues(['约 0.00 GB','约 0.00 GB','0.000','0.0']);
  for(const [state,value] of [['unknown','未知'],['infinite','∞'],['undefined','未定义']]) {
    mode=state; await refresh.click(); await expectValues([fresh[0],fresh[1],value,fresh[3]]);
    if(state==='unknown')await bounds(320,'unknown');
  }
  mode='stale'; await refresh.click(); await expectValues(fresh);
  assert.match(await panel.getByRole('status').innerText(),/采集频率受限.*陈旧.*上次有效记录/);
  assert.equal(await panel.locator('time').getAttribute('datetime'),stamp);
  await bounds(375,'stale');
  mode='native'; await refresh.click(); await expectValues(fresh);
  phase='identity-disposal-real-logout';
  await page.setViewportSize({width:1440,height:1000});
  g=gate(); held=g; await refresh.click(); await g.ready;
  await page.getByTitle('退出登录',{exact:true}).click(); await page.waitForURL('**/login');
  // Fixture command installs a separately issued real auth Cookie; no invented identity state.
  const login=await context.request.post(base+'/owned/auth-login'); assert.equal(login.status(),204);
  await page.goto(base+'/sites'); await toggle.waitFor();
  assert.equal(await toggle.getAttribute('aria-expanded'),'false');
  g.release(); await eventually(()=>active===0,'identity route did not drain');
  assert.equal(await panel.locator('dd').count(),0,'late former-session values restored');
  await toggle.click(); await expectValues(fresh);
  phase='role-disposal-real-member';
  g=gate(); held=g; await refresh.click(); await g.ready;
  const demote=await context.request.post(base+'/owned/demote'); assert.equal(demote.status(),204);
  await page.reload(); await page.getByRole('status').filter({hasText:'当前为成员账户'}).waitFor();
  g.release(); await eventually(()=>active===0,'role route did not drain');
  assert.equal(await page.getByRole('region',{name:'账号统计',exact:true}).count(),0);
  const member=await context.request.get(base+statsPath); assert.equal(member.status(),403);
  const memberCount=requests; await page.reload(); await page.getByRole('status').filter({hasText:'当前为成员账户'}).waitFor();
  assert.equal(requests,memberCount,'member emitted stats GET');
  assert.equal(await page.getByRole('region',{name:'账号统计',exact:true}).count(),0);
  assert.equal(mutations,0); assert.equal(authWrites,1); assert.deepEqual(pageErrors,[]); assert.deepEqual(routeErrors,[]);
  const storage=await page.evaluate(()=>JSON.stringify({local:{...localStorage},session:{...sessionStorage}}));
  for(const secret of ['owned-account-fixture','owned-private-response-body','1234.5'])assert(!storage.includes(secret),'private stats persisted in browser storage');
  console.log('M8_STATS_UI_BROWSER_PASS '+JSON.stringify({nativeReplies,businessMutations:mutations,authWrites,sourceIDBound:true,currentUID:'42',nativeFresh:true,semanticModels:['zero','unknown','infinite','undefined','stale'],realUnconfigured:true,realLogout:true,realMember403:true,lateResponseDisposed:true,widths:[375,320,768,1440],targets44:true}));
}
let failed=false;
try {
  await Promise.race([run(),new Promise((_,reject)=>{timer=setTimeout(()=>reject(new Error('browser deadline')),78000);})]);
} catch(error) {
  failed=true;
  const line=String(error.stack||'').match(/m8_site_account_stats_ui\.mjs:(\d+):\d+/)?.[1]||'unknown';
  console.error('M8_STATS_UI_BROWSER_FAIL phase='+phase+' line='+line);
} finally {
  clearTimeout(timer); for(const g of gates)g.release();
  try { if(context)await context.close(); if(browser)await browser.close(); assert(!browser?.isConnected()); console.log('M8_STATS_UI_CHROME_CLEANUP_VERIFIED'); }
  catch { failed=true; console.error('M8_STATS_UI_CLEANUP_FAILED phase=close'); }
}
process.exitCode=failed?1:0;
