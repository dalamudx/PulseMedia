import assert from 'node:assert/strict';
import {existsSync, mkdirSync} from 'node:fs';
import {join} from 'node:path';
import {setTimeout as delay} from 'node:timers/promises';
import {chromium} from 'playwright-core';

const env=process.env,mode=process.argv[2],base=env.PULSE_M6_DASHBOARD_BASE;
assert(['states','recovery'].includes(mode),'explicit states or recovery mode required');
assert(base&&env.PULSE_M6_DASHBOARD_SESSION&&env.PULSE_M6_DASHBOARD_MEMBER_SESSION&&env.PULSE_M6_DASHBOARD_CDP&&env.PULSE_M6_DASHBOARD_SCREENSHOTS?.startsWith('/data/temp/'),'owned fixture required');
assert.notEqual(new URL(env.PULSE_M6_DASHBOARD_CDP).port,'9222','never attach shared CDP');
const shots=join(env.PULSE_M6_DASHBOARD_SCREENSHOTS,mode);mkdirSync(shots,{recursive:true});
const browser=await chromium.connectOverCDP(env.PULSE_M6_DASHBOARD_CDP);
const context=await browser.newContext({viewport:{width:1280,height:900},colorScheme:'dark',reducedMotion:'reduce',serviceWorkers:'block'});
let checks=0;
const check=(value,message)=>{assert(value,message);checks++;};
try {
 // Test transport only: each response first comes from the real authenticated
 // HTTP endpoint. Holding delivery (even after abort) tests React's scope fence;
 // injected status codes exercise failures without replacing business fixtures.
 await context.addInitScript(()=>{
  const native=window.fetch.bind(window),rules={},pending=[],receipts=[];
  window.__m6Transport={rules,pending,receipts,release(lane){
   const index=pending.findIndex(item=>item.lane===lane);if(index<0)throw new Error('no held '+lane);
   pending.splice(index,1)[0].release();
  }};
  window.fetch=async(input,init)=>{
   const url=new URL(typeof input==='string'?input:input.url,location.href);
   const lane=({'/api/v1/library/overview':'overview','/api/v1/dashboard/activity':'activity','/api/v1/auth/status':'auth'})[url.pathname];
   if(!lane)return native(input,init);
   const rule=rules[lane];delete rules[lane];
   // Deliberately ignore abort ONLY when requested by the test. Old responses
   // must still be rejected by the production scope/owner checks.
   const request=rule?.ignoreAbort&&input instanceof Request?new Request(input,{signal:new AbortController().signal}):input;
   const options=rule?.ignoreAbort?{...init,signal:new AbortController().signal}:init;
   const response=await native(request,options);
   const data=await response.clone().json();
   receipts.push({lane,status:response.status,data});
   if(rule?.hold)await new Promise(release=>pending.push({lane,release}));
   if(rule?.status)return new Response(JSON.stringify({type:'about:blank',title:'Injected transport failure',status:rule.status,code:'UNAVAILABLE',message:'fixture transport fault',request_id:'m6-browser'}),{status:rule.status,headers:{'Content-Type':'application/problem+json'}});
   return response;
  };
 });
 const external=[];
 await context.route('**/*',async route=>{
  const url=new URL(route.request().url());
  if(url.origin===base)return route.continue();
  if(url.origin==='https://image.tmdb.org'&&/^\/t\/p\/w342\/(Poster_\d+|Broken)\.png$/.test(url.pathname)) {
   if(url.pathname.endsWith('/Broken.png'))return route.fulfill({status:404,body:'missing'});
   return route.fulfill({status:200,contentType:'image/svg+xml',body:'<svg xmlns="http://www.w3.org/2000/svg" width="2" height="3"><path fill="#476175" d="M0 0h2v3H0z"/></svg>'});
  }
  external.push(url.origin+url.pathname);return route.abort();
 });
 const cookie=async value=>context.addCookies([{name:'pulsemedia_session',value,url:base,httpOnly:true,sameSite:'Lax'}]);
 await cookie(env.PULSE_M6_DASHBOARD_SESSION);
 const page=await context.newPage();page.setDefaultTimeout(12000);
 const errors=[];page.on('pageerror',error=>errors.push(error.message));
 await page.goto(base,{waitUntil:'domcontentloaded'});
 const facts=()=>page.locator('.dashboard-facts');
 const overview=()=>page.getByRole('region',{name:'共享媒体库当前入库统计',exact:true});
 const activity=()=>page.getByRole('region',{name:'活动记录',exact:true});
 const recent=()=>page.getByRole('region',{name:'近期真实入库',exact:true});
 const refresh=()=>page.getByRole('button',{name:'刷新首页数据',exact:true});
 const ready=async()=>{
  await page.waitForFunction(()=>{const button=document.querySelector('[aria-label="刷新首页数据"]');return button&&!button.disabled&&document.querySelectorAll('.facts-import').length===10;});
 };
 const dismiss=async()=>{
  await page.locator('.toast-card').evaluateAll(toasts=>toasts.forEach(toast=>toast.click()));
  await page.waitForFunction(()=>document.querySelectorAll('.toast-card').length===0);
 };
 const arm=async rules=>page.evaluate(rules=>Object.assign(window.__m6Transport.rules,rules),rules);
 // Arm faults in the actual click's capture phase, so background polling cannot
 // consume a rule between arming it and the user's refresh handler.
 const refreshWith=async rules=>{
  await page.evaluate(rules=>document.querySelector('[aria-label="刷新首页数据"]').addEventListener('click',()=>Object.assign(window.__m6Transport.rules,rules),{capture:true,once:true}),rules);
  await refresh().click();
 };
 const held=async lanes=>page.waitForFunction(lanes=>lanes.every(lane=>window.__m6Transport.pending.some(item=>item.lane===lane)),lanes);
 const release=async lane=>page.evaluate(lane=>window.__m6Transport.release(lane),lane);
 const counts=()=>overview().locator('dd').allTextContents();
 const settleRefresh=async()=>{await page.waitForFunction(()=>!document.querySelector('[aria-label="刷新首页数据"]')?.disabled);};
 const fixture=async action=>{
  const result=await context.request.post(base+'/__m6_fixture/'+action);
  check(result.status()===204,'controlled SQL fixture transition '+action+': '+result.status());
 };
 const changeIdentity=async token=>{
  await cookie(token);await arm({auth:{hold:true,ignoreAbort:true}});
  await page.evaluate(()=>{void window.__m6DashboardAuth.checkStatus();});
  await held(['auth']);
  check(await facts().count()===0,'auth recheck must hide old scope before new status resolves');
  await release('auth');
 };
 await ready();
 check(JSON.stringify(await counts())===JSON.stringify(['9','1','2','11']),'four actual imported counts, including special/multi-unit file');
 check(await overview().locator('dt').allTextContents().then(labels=>labels.join('|')==='电影|电视剧|集数（含特别篇）|媒体文件'),'count card labels distinguish imported facts');
 const subscriptionReply=await context.request.get(base+'/api/v1/subscriptions');
 check(subscriptionReply.status()===200,'legacy subscription counts remain readable');
 const subscriptionItems=(await subscriptionReply.json()).items;
 const subscriptionCounts=['movie','tv'].map(type=>String(subscriptionItems.filter(item=>item.media_type===type).length));
 await page.waitForFunction(expected=>JSON.stringify([...document.querySelectorAll('.stats-grid-modern strong')].slice(0,2).map(node=>node.textContent))===JSON.stringify(expected),subscriptionCounts,{timeout:15000});
 check(JSON.stringify((await page.locator('.stats-grid-modern strong').allTextContents()).slice(0,2))===JSON.stringify(subscriptionCounts),'subscription cards use subscription API independently of imported counts');
 const initial=await page.evaluate(()=>window.__m6Transport.receipts);
 const actualOverview=initial.find(item=>item.lane==='overview'&&item.status===200)?.data;
 const actualActivity=initial.find(item=>item.lane==='activity'&&item.status===200)?.data;
 check(actualOverview&&actualActivity,'both new GETs return through App/Cookie/real store');
 check(actualOverview.recent.length===10&&actualOverview.recent.every(item=>item.title.startsWith('Imported ')),'recent source is persisted import history');
 check(await recent().locator('.facts-import').count()===10&&!(await recent().innerText()).includes('TMDB'),'recent imports are separate from trending recommendations');
 for(const state of ['fresh','full','stale','unknown','offline','partial','failed']) {
  const root=page.locator('.facts-root').filter({has:page.getByText('Capacity '+state,{exact:true})});
  const record=actualOverview.roots.find(item=>item.name==='Capacity '+state);
  check(!!record,'root returned '+state);
  if(['fresh','full','stale'].includes(state)) {
   check(record.total_bytes==='9007199254740993','exact >2^53 decimal total '+state);
   check(record.capacity_state===(state==='stale'?'stale':'fresh'),'capacity age '+state);
   const available=BigInt(record.available_bytes),total=BigInt(record.total_bytes);
   const expected=Number((total-available)*10000n/total)/100;
   check(Number(await root.locator('progress').getAttribute('value'))===expected,'integer-derived capacity progress '+state);
   check((await root.innerText()).includes(state==='stale'?'容量样本已过期':'容量样本有效'),'capacity timestamp meaning '+state);
   if(state==='full')check(record.available_bytes==='0'&&(await root.innerText()).includes('可用 0 B'),'disk-full zero remains known');
  } else {
   check(record.capacity_state==='unknown'&&record.total_bytes==null&&record.available_bytes==null,'no stale-success fallback '+state);
   check(await root.locator('progress').count()===0&&(await root.innerText()).includes('容量未知'),'unknown capacity has no invented percent '+state);
  }
 }
 const native=activity().locator('.facts-activity').filter({has:page.getByText('Member native record',{exact:true})});
 check(await native.locator('progress').getAttribute('value')==='25','native 25%, never downloaded_bytes/total_bytes=500%');
 check((await native.innerText()).includes('任务记录')&&(await native.innerText()).includes('2020')&&!(await native.innerText()).includes('实时'),'native percent retains TASK record time');
 check(actualActivity.items.find(item=>item.title==='Member native record').updated_at.startsWith('2020-01-02'),'API preserves old task timestamp');
 for(const [title,label] of [['Member unknown','状态未知'],['Member review','待人工处理'],['Member organizing','整理中']]) {
  const row=activity().locator('.facts-activity').filter({has:page.getByText(title,{exact:true})});
  check((await row.innerText()).includes(label)&&await row.locator('progress').count()===0,'activity is not all download: '+title);
 }
 check((await activity().innerText()).includes('Other member secret'),'admin sees other member activity');
 const rail=recent().locator('.facts-carousel');
 const position=()=>rail.evaluate(el=>el.scrollLeft);
 const moveUntil=async predicate=>{await page.waitForFunction(predicate);};
 await rail.evaluate(el=>el.scrollTo({left:0,behavior:'instant'}));
 await page.getByRole('button',{name:'下一组入库媒体',exact:true}).click();
 await moveUntil(()=>document.querySelector('.facts-carousel').scrollLeft>0);
 const afterNext=await position();check(afterNext>0,'real carousel next scrolls');
 await page.getByRole('button',{name:'上一组入库媒体',exact:true}).click();
 await page.waitForFunction(value=>document.querySelector('.facts-carousel').scrollLeft<value,afterNext);
 check(await position()<afterNext,'real carousel previous scrolls');
 await rail.focus();await page.keyboard.press('End');
 await page.waitForFunction(()=>{const el=document.querySelector('.facts-carousel');return el.scrollLeft+el.clientWidth>=el.scrollWidth-5;});
 await page.keyboard.press('Home');await page.waitForFunction(()=>document.querySelector('.facts-carousel').scrollLeft<5);
 await page.keyboard.press('ArrowRight');await moveUntil(()=>document.querySelector('.facts-carousel').scrollLeft>0);
 const afterKey=await position();await page.keyboard.press('ArrowLeft');
 await page.waitForFunction(value=>document.querySelector('.facts-carousel').scrollLeft<value,afterKey);
 check(true,'real keyboard End/Home/ArrowRight/ArrowLeft move carousel');
 for(const title of ['Imported 00','Imported 01','Imported 02','Imported 03']) {
  const card=recent().locator('.facts-import').filter({has:page.getByText(title,{exact:true})});
  await card.scrollIntoViewIfNeeded();
  if(title==='Imported 02')check(await card.locator('img').count()===0&&await card.getByText('暂无海报').count()===1,'missing poster placeholder');
  else if(title==='Imported 01') {
   await card.locator('img').waitFor({state:'hidden'});
   check(await card.locator('img').evaluate(el=>el.hidden)&&await card.getByText('暂无海报').isVisible(),'failed poster exposes placeholder');
  } else {
   await page.waitForFunction(title=>{const card=[...document.querySelectorAll('.facts-import')].find(el=>el.querySelector('strong').textContent===title);const img=card?.querySelector('img');return img?.complete&&img.naturalWidth>0;},title);
   check(true,'locally fulfilled real HTTPS poster '+title);
  }
 }
 for(const width of [1280,768,375,320]) {
  await page.setViewportSize({width,height:900});
  await rail.evaluate(el=>el.scrollTo({left:0,behavior:'instant'}));
  await refresh().scrollIntoViewIfNeeded();
  const geometry=await page.evaluate(()=>{
   const bounds=[];
   const fits=(el,label)=>{const r=el.getBoundingClientRect();bounds.push({label,ok:r.width>0&&r.left>=-1&&r.right<=innerWidth+1&&(el.classList.contains('welcome-banner-modern')||el.scrollWidth<=el.clientWidth+1),width:r.width});};
   for(const el of document.querySelectorAll('.dashboard-container,.dashboard-facts,.facts-section,.facts-counts,.facts-count,.facts-heading,.welcome-banner-modern,.welcome-actions,.topbar'))fits(el,el.className);
   for(const el of document.querySelectorAll('.facts-count dt,.facts-count dd,.facts-heading h2,.facts-heading>a,.facts-root strong,.facts-activity strong')) {
    const r=el.getBoundingClientRect(),range=document.createRange();range.selectNodeContents(el);
    const rects=[...range.getClientRects()];
    bounds.push({label:el.textContent,ok:parseFloat(getComputedStyle(el).fontSize)>=12&&rects.every(text=>text.left>=r.left-1&&text.right<=r.right+1),width:r.width});
   }
   for(const el of document.querySelectorAll('.welcome-actions button,.facts-carousel-controls button,.mobile-menu-btn,.btn-logout')) {
    if(!el.getClientRects().length)continue;
    const r=el.getBoundingClientRect();
    const x=r.left+r.width/2,y=r.top+r.height/2;
    const hit=y>=0&&y<innerHeight?document.elementFromPoint(x,y):null;
    bounds.push({label:el.getAttribute('aria-label')||el.textContent,ok:r.width>=32&&r.height>=32&&r.left>=0&&r.right<=innerWidth+1&&(y<0||y>=innerHeight||!!hit&&el.contains(hit)),width:r.width});
   }
   return {width:innerWidth,main:document.querySelector('#main-content').getBoundingClientRect().width,bounds};
  });
  check(geometry.width===width&&geometry.main>0&&geometry.main<=width,'actual viewport and main widths '+width);
  for(const item of geometry.bounds)check(item.ok,'readable, contained, clickable at '+width+': '+JSON.stringify(item));
  if(width!==1280) {
   const menu=page.getByRole('button',{name:'打开导航菜单',exact:true});await menu.click();
   await page.getByRole('dialog',{name:'导航菜单'}).waitFor();
   check(await page.locator('.main-wrapper').evaluate(el=>el.inert),'inherited shell drawer isolates dashboard '+width);
   await page.keyboard.press('Escape');
   check(await menu.evaluate(el=>el===document.activeElement),'inherited shell Escape restores focus '+width);
  }
  await page.screenshot({path:join(shots,width===1280?'dashboard-desktop.png':width===768?'dashboard-tablet.png':`dashboard-${width}.png`),fullPage:true});
 }
 if(env.PULSE_M6_DASHBOARD_REVIEW==='1'&&mode==='states') {
  console.log('M6_DASHBOARD_SCREENSHOTS_READY '+shots);
  const until=Date.now()+180000;
  while(!existsSync(join(shots,'inspected'))) {assert(Date.now()<until,'bounded screenshot review timed out');await delay(100);}
 }
 await page.setViewportSize({width:1280,height:900});
 await overview().getByRole('link',{name:'浏览媒体库',exact:true}).click();await page.waitForURL(base+'/media');
 await page.getByRole('navigation',{name:'主导航',exact:true}).getByRole('link',{name:'仪表盘',exact:true}).click();await page.waitForURL(base+'/');await ready();
 check((await counts()).join(',')==='9,1,2,11','inherited shell navigation returns to persisted dashboard facts');
 if(mode==='recovery') {
  await dismiss();await refreshWith({overview:{hold:true,ignoreAbort:true},activity:{hold:true,ignoreAbort:true}});await held(['overview','activity']);
  check(await refresh().isDisabled()&&await refresh().getAttribute('aria-busy')==='true','manual refresh stays disabled/loading while real GETs held');
  check(await page.locator('.toast-card.success').count()===0,'no early success toast with both GETs held');
  await release('overview');
  await page.waitForFunction(()=>document.querySelector('[aria-label="共享媒体库当前入库统计"]').getAttribute('aria-busy')==='false');
  check(await refresh().isDisabled()&&await page.locator('.toast-card.success').count()===0,'one released lane cannot complete refresh');
  await release('activity');await page.getByText('控制台状态已刷新',{exact:true}).waitFor();await settleRefresh();
  check(true,'actual success only after both real GETs released');
  for(const lane of ['overview','activity']) {
   await dismiss();await refreshWith({[lane]:{status:503}});
   await page.getByText('部分数据未刷新',{exact:true}).waitFor();await settleRefresh();
   const region=lane==='overview'?overview():activity();
   check((await region.innerText()).includes('读取失败，显示上次读取结果'),'explicit same-account retained data for failed '+lane);
   check((await counts()).join(',')==='9,1,2,11'&&(await activity().innerText()).includes('Member native record'),'one lane failure retains previous same-account facts');
   await dismiss();await refresh().click();await page.getByText('控制台状态已刷新',{exact:true}).waitFor();await settleRefresh();
  }
  for(const lane of ['overview','activity']) {
   await dismiss();await refreshWith({[lane]:{status:403}});
   await page.getByText('部分数据未刷新',{exact:true}).waitFor();await settleRefresh();
   check((await facts().innerText()).includes('无访问权限'),'403 explicit permission state '+lane);
   if(lane==='overview')check((await counts()).every(value=>value==='—')&&await recent().locator('.facts-import').count()===0&&await page.locator('.facts-root').count()===0,'403 clears imported and capacity/recent data');
   else check(await activity().locator('.facts-activity').count()===0,'403 clears restricted activity');
   await dismiss();await refresh().click();await page.getByText('控制台状态已刷新',{exact:true}).waitFor();await settleRefresh();
  }
  await dismiss();await refreshWith({overview:{status:401}});
  await page.waitForURL(base+'/login');
  check(await facts().count()===0&&await page.locator('.app-shell').count()===0,'401 removes populated restricted data and private shell');
  await page.goto(base,{waitUntil:'domcontentloaded'});await ready();
  await dismiss();await refreshWith({overview:{hold:true,ignoreAbort:true},activity:{hold:true,ignoreAbort:true}});await held(['overview','activity']);
  await changeIdentity(env.PULSE_M6_DASHBOARD_MEMBER_SESSION);await ready();
  check(await activity().locator('.facts-activity').count()===4&&!(await activity().innerText()).includes('Other member secret'),'member sees only own activities after account switch');
  await release('overview');await release('activity');
  await page.evaluate(()=>new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve))));
  check(await activity().locator('.facts-activity').count()===4&&!(await activity().innerText()).includes('Other member secret'),'late ignored-abort old account response cannot replace new scope');
  await changeIdentity(env.PULSE_M6_DASHBOARD_SESSION);await ready();
  check((await activity().innerText()).includes('Other member secret'),'real admin cookie restores admin visibility');
  await refreshWith({overview:{hold:true,ignoreAbort:true},activity:{hold:true,ignoreAbort:true}});await held(['overview','activity']);
  await fixture('demote');await fixture('empty');
  await changeIdentity(env.PULSE_M6_DASHBOARD_SESSION);
  await page.waitForFunction(()=>document.querySelector('.facts-count dd')?.textContent==='0'&&document.querySelector('[aria-label="活动记录"]')?.textContent.includes('暂无活动记录'));
  check((await counts()).join(',')==='0,0,0,0'&&await activity().locator('.facts-activity').count()===0,'role downgrade removes old admin data and reads actual empty store');
  await release('overview');await release('activity');
  await page.evaluate(()=>new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve))));
  check((await counts()).join(',')==='0,0,0,0'&&await activity().locator('.facts-activity').count()===0,'late old-role responses cannot restore restricted data or old overview');
  await dismiss();await refresh().click();await page.getByText('控制台状态已刷新',{exact:true}).waitFor();await settleRefresh();
  check((await recent().innerText()).includes('暂无入库记录')&&(await activity().innerText()).includes('暂无活动记录')&&await facts().locator('.facts-errors,.facts-notice').count()===0,'successful empty differs from failed or retained state');
 }
 check(errors.length===0,'no runtime errors: '+errors.join(';'));
 check(external.length===0,'no external network: '+external.join(';'));
 console.log('M6_DASHBOARD_BROWSER_PASS '+mode+' '+JSON.stringify({checks,actualChrome:true,realCookie:true,widths:[1280,768,375,320],facts:'SQL-prepared imported/capacity/activity; production ingestion and sampler separately tested',transportFaults:mode==='recovery'}));
} finally {
 await context.close();await browser.close();console.log('M6_DASHBOARD_CHROME_CLEANUP_VERIFIED '+mode);
}
