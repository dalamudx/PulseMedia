import assert from 'node:assert/strict';
import {createRequire} from 'node:module';
import {join} from 'node:path';
import {setTimeout as delay} from 'node:timers/promises';
const env=process.env,load=createRequire(env.PULSE_M6_OPERATION_REPO+'/web/package.json');
const {chromium}=load('playwright-core');
const base=env.PULSE_M6_OPERATION_BASE,shots=env.PULSE_M6_OPERATION_SCREENSHOTS;
const current=env.PULSE_M6_OPERATION_CURRENT,cancelOp=env.PULSE_M6_OPERATION_CANCEL,privateOp=env.PULSE_M6_OPERATION_PRIVATE;
assert(base&&shots?.startsWith('/data/temp/')&&current&&cancelOp&&privateOp);
assert.notEqual(new URL(env.PULSE_M6_OPERATION_CDP).port,'9222');
const browser=await chromium.connectOverCDP(env.PULSE_M6_OPERATION_CDP);
const context=await browser.newContext({viewport:{width:1280,height:1000},reducedMotion:'reduce',serviceWorkers:'block'});
const receipts=[],writes=[],errors=[];
let checks=0,dropReply=false;
function check(value,message){assert(value,message);checks++;}
try {
 await context.addInitScript(()=>{
  const native=window.fetch.bind(window),rules={},pending=[],receipts=[],starts=[];
  window.__operationTransport={rules,pending,receipts,starts,release(lane){const i=pending.findIndex(x=>x.lane===lane);if(i<0)throw Error('missing held '+lane);pending.splice(i,1)[0].release();}};
  window.fetch=async(input,init)=>{
   const url=new URL(typeof input==='string'?input:input.url,location.href);
   const lane=url.pathname.endsWith('/timeline')?'timeline':/^\/api\/v1\/operations\/[^/]+$/.test(url.pathname)?'detail':url.pathname==='/api/v1/operations'?'list':url.pathname==='/api/v1/auth/status'?'auth':null;
   const rule=lane?rules[lane]:null;if(lane){delete rules[lane];starts.push({lane,url:url.pathname+url.search});}
   const request=rule?.ignoreAbort&&input instanceof Request?new Request(input,{signal:new AbortController().signal}):input;
   const response=await native(request,rule?.ignoreAbort?{...init,signal:new AbortController().signal}:init);
   if(lane)receipts.push({lane,status:response.status,url:url.pathname+url.search});
   if(rule?.hold)await new Promise(release=>pending.push({lane,release}));
   return response;
  };
 });
 await context.route('**/*',async route=>{
  const request=route.request(),url=new URL(request.url());
  if(url.origin!==base)return route.abort('blockedbyclient');
  if(url.pathname.startsWith('/api/')&&request.method()!=='GET'){
   writes.push({method:request.method(),path:url.pathname,key:request.headers()['idempotency-key'],body:request.postData()});
   if(dropReply&&url.pathname.endsWith('/withdraw')){
    dropReply=false;
    const response=await route.fetch();assert.equal(response.status(),200);
    const data=await response.json();assert.equal(data.physical_released,false);assert.equal(data.state,'released');
    receipts.push({lostCommittedWithdrawal:data});
    return route.abort('failed');
   }
  }
  return route.continue();
 });
 const cookie=async value=>{await context.clearCookies();await context.addCookies([{name:'pulsemedia_session',value,url:base,httpOnly:true,sameSite:'Strict'}]);};
 await cookie(env.PULSE_M6_OPERATION_SESSION);
 const page=await context.newPage();page.setDefaultTimeout(10000);page.on('pageerror',e=>errors.push(e.message));
 page.on('response',async response=>{const url=new URL(response.url());if(url.origin===base&&url.pathname.startsWith('/api/v1/operations'))receipts.push({path:url.pathname,query:url.search,status:response.status()});});
 const fixture=async action=>{const response=await context.request.post(base+'/__operation/'+action,{timeout:45000});assert.equal(response.status(),204,'fixture '+action);};
 const inbox=()=>page.getByRole('region',{name:'持久操作列表',exact:true});
 const detail=()=>page.getByRole('region',{name:'操作详情',exact:true});
 const timeline=()=>page.getByRole('region',{name:'整理步骤',exact:true});
 const telemetry=()=>detail().getByRole('region',{name:'下载观测',exact:true});
 const actions=()=>page.getByRole('group',{name:'操作处理',exact:true});
 const arm=rules=>page.evaluate(rules=>Object.assign(window.__operationTransport.rules,rules),rules);
 const held=lanes=>page.waitForFunction(lanes=>lanes.every(lane=>window.__operationTransport.pending.some(x=>x.lane===lane)),lanes);
 const release=lane=>page.evaluate(lane=>window.__operationTransport.release(lane),lane);
 const ready=()=>page.waitForFunction(()=>{const f=document.querySelector('fieldset[aria-label="操作处理"]');return f&&!f.disabled;});
 const refresh=()=>page.getByRole('button',{name:'查询 / 刷新',exact:true});
 async function select(id){await page.locator('#operation-id').fill(id);await refresh().click();await detail().getByRole('heading',{name:'操作 '+id,exact:true}).waitFor();await ready();}
 async function freshWith(rules){await refresh().evaluate((button,rules)=>button.addEventListener('click',()=>Object.assign(window.__operationTransport.rules,rules),{once:true,capture:true}),rules);await refresh().click();}
 async function safe(){const text=await page.locator('.operation-activity').innerText();for(const secret of ['/owned-private/','private/','private failure detail','FOREIGN-PRIVATE-FILENAME','fixture-not-used'])check(!text.includes(secret),'private value leaked: '+secret);}
 async function readCount(lane){return page.evaluate(lane=>window.__operationTransport.receipts.filter(x=>x.lane===lane).length,lane);}
 await page.goto(base+'/activity',{waitUntil:'domcontentloaded'});
 await inbox().locator('.operation-inbox-list > li').first().waitFor();
 for(const label of ['待处理','全部','进行中']){
  await page.getByRole('group',{name:'操作范围'}).getByRole('button',{name:label,exact:true}).click();
  await page.waitForFunction(()=>document.querySelector('.operation-inbox')?.getAttribute('aria-busy')==='false');
  check(await inbox().locator('.operation-inbox-list > li').count()===(label==='待处理'?2:3),'scope '+label+' actual stored rows');
 }
 // Select through the real read-only list button, using UUID text to identify its card.
 await inbox().locator('.operation-inbox-list > li').filter({hasText:current}).getByRole('button').click();
 await ready();check(writes.length===0,'list/selection emitted a command');
 await telemetry().getByText('25%',{exact:true}).waitFor();
 await telemetry().getByText('8388607.9 TiB/s',{exact:true}).waitFor();
 const actual=await context.request.get(base+'/api/v1/operations/'+current);assert.equal(actual.status(),200);
 const data=await actual.json();assert.equal(data.telemetry.download_rate_bytes_per_second,'9223372036854775807');assert.equal(data.telemetry.progress_source,'native_percent');assert.equal(data.telemetry.progress,0.25);
 check(data.telemetry.progress!==0.99,'cumulative downloaded bytes were used as progress');
 await detail().getByText('下载跟踪',{exact:true}).first().waitFor();
 check(await timeline().locator('ol > li').count()===50,'initial keyset page must contain 50 steps');
 await timeline().getByRole('button',{name:'加载更多步骤',exact:true}).click();
 await page.waitForFunction(()=>document.querySelectorAll('.operation-live-steps > li').length===125);
 const names=await timeline().locator('ol > li strong').allTextContents();
 check(names.every((name,i)=>name.startsWith('episode-'+String(i).padStart(3,'0')+'-')),'stored timeline ordering or basename changed');
 check(new Set(names).size===125,'duplicate timeline steps');
 check(receipts.some(x=>x.path?.endsWith('/timeline')&&x.query.includes('cursor=')),'manual cursor GET missing');
 await safe();await fixture('check');
 // Timed native-counter display transition is an explicit stored fixture control.
 await fixture('zero');await telemetry().getByText('0 B/s',{exact:true}).waitFor({timeout:12000});await telemetry().getByText('50%',{exact:true}).waitFor();
 await fixture('stale');await refresh().click();await ready();await telemetry().getByText('观测已过期',{exact:false}).first().waitFor();
 await fixture('unknown');await refresh().click();await ready();await telemetry().getByText('速度未知',{exact:true}).waitFor();
 await fixture('large');await refresh().click();await ready();
 // Background paired GETs keep a pending manual confirmation stable.
 const requirements=()=>page.getByRole('region',{name:'自身入库需求',exact:true});
 await requirements().getByRole('button',{name:'撤销自身入库需求',exact:true}).click();
 const confirmation=requirements().getByRole('button',{name:'确认撤销自身需求',exact:true});
 await confirmation.waitFor();const backgroundBefore=await readCount('detail');
 await arm({detail:{hold:true},timeline:{hold:true}});await held(['detail','timeline']);
 check(await confirmation.isEnabled(),'5s background read disabled in-progress confirmation');
 check(await readCount('detail')>backgroundBefore,'background detail GET missing');
 await release('detail');await release('timeline');await ready();
 await requirements().getByRole('button',{name:'保留需求',exact:true}).click();
 // Explicit refresh invalidates command permission until both genuine reads return.
 await freshWith({detail:{hold:true},timeline:{hold:true}});await held(['detail','timeline']);
 check(await actions().evaluate(fieldset=>fieldset.disabled),'explicit refresh left actions enabled');
 check(await requirements().getByRole('button',{name:'撤销自身入库需求',exact:true}).isDisabled(),'explicit refresh did not disable actual command button');
 await release('detail');check(await actions().evaluate(fieldset=>fieldset.disabled),'unpaired timeline left actions enabled');await release('timeline');await ready();
 // Transport abort after a real GET must surface an error and allow display retry.
 let failTimeline=true;
 await page.route('**/api/v1/operations/'+current+'/timeline*',async route=>{
  if(failTimeline){failTimeline=false;const response=await route.fetch();assert.equal(response.status(),200);return route.abort('failed');}return route.continue();
 });
 await refresh().click();await timeline().getByRole('alert').waitFor();check(await actions().evaluate(fieldset=>fieldset.disabled),'failed timeline enabled approval actions');
 await timeline().getByRole('button',{name:'重新读取整理步骤',exact:true}).click();await ready();await page.unroute('**/api/v1/operations/'+current+'/timeline*');
 // Real storage fault: temporarily hide only the owned fixture observation table.
 await fixture('storage-down');
 const unavailable=await context.request.get(base+'/api/v1/operations/'+current);assert.equal(unavailable.status(),503);
 await refresh().click();await detail().getByRole('status').filter({hasText:'读取尚未确认'}).waitFor();check(await actions().evaluate(fieldset=>fieldset.disabled),'storage 503 retained command permission');
 await fixture('storage-up');await refresh().click();await ready();await fixture('check');
 // Override only the browser visibility input; GET counter behavior stays real.
 const cdp=await context.newCDPSession(page);
 await cdp.send('Emulation.setFocusEmulationEnabled',{enabled:true});
 await page.evaluate(()=>{window.__operationVisibility=Object.getOwnPropertyDescriptor(Document.prototype,'visibilityState');Object.defineProperty(document,'visibilityState',{configurable:true,get:()=> 'hidden'});document.dispatchEvent(new Event('visibilitychange'));});
 const hiddenBefore=await readCount('detail');
 const hiddenStarts=await page.evaluate(()=>window.__operationTransport.starts.filter(x=>x.lane==='detail').length);
 await delay(5600);
 check(await page.evaluate(()=>window.__operationTransport.starts.filter(x=>x.lane==='detail').length)===hiddenStarts,'hidden page initiated detail queries');
 const visibleBefore=await readCount('detail');
 await page.evaluate(()=>{delete document.visibilityState;document.dispatchEvent(new Event('visibilitychange'));});
 await page.waitForFunction(n=>window.__operationTransport.receipts.filter(x=>x.lane==='detail').length>n,visibleBefore);await ready();
 check(writes.length===0,'read-only phase wrote business commands');await fixture('check');
 // Actual list/detail/timeline screenshots, with settled compact navigation geometry.
 for(const width of [1280,768,375,320]){
  await page.setViewportSize({width,height:1000});
  await page.waitForFunction(width=>{const nav=document.getElementById('app-navigation'),menu=document.querySelector('.mobile-menu-btn');if(!nav||!menu||innerWidth!==width||nav.getAnimations().some(a=>a.playState==='running'))return false;return width<=1024?nav.inert&&menu.getAttribute('aria-expanded')==='false'&&nav.getBoundingClientRect().right<=1:!nav.inert&&nav.getBoundingClientRect().left>=-1;},width);
  const failures=await page.locator('.operation-activity').evaluate(root=>{
   const out=[];if(document.documentElement.scrollWidth>innerWidth+1)out.push('viewport overflow');
   for(const element of root.querySelectorAll('button,input,.op-card,.operation-inbox-list > li,.operation-live-steps > li,strong,.operation-live-meta')){
    const r=element.getBoundingClientRect();if(!r.width||!r.height)continue;
    if(r.left < -1||r.right>innerWidth+1)out.push(element.tagName+' outside viewport');
    if(element.tagName==='BUTTON'&&(r.width<44||r.height<44))out.push('button below 44px '+element.textContent);
    if(element.scrollWidth>element.clientWidth+2&&getComputedStyle(element).overflowX==='visible')out.push('text overflow '+element.tagName);
   }return out;
  });assert.deepEqual(failures,[],'bounds '+width);checks++;
  await inbox().scrollIntoViewIfNeeded();await page.screenshot({path:join(shots,'list-'+width+'.png')});
  await detail().getByRole('heading',{name:'操作 '+current,exact:true}).scrollIntoViewIfNeeded();await page.screenshot({path:join(shots,'detail-'+width+'.png')});
  await timeline().locator('li').first().scrollIntoViewIfNeeded();await page.screenshot({path:join(shots,'timeline-'+width+'.png')});
 }
 await safe();await page.setViewportSize({width:1280,height:1000});
 // Explicit command phase: original panel, actual commit, lost reply, same-key recovery.
 await select(cancelOp);await requirements().getByRole('button',{name:'撤销自身入库需求',exact:true}).click();dropReply=true;
 await requirements().getByRole('button',{name:'确认撤销自身需求',exact:true}).click();
 const retry=requirements().getByRole('button',{name:'重试同一撤销命令',exact:true});await retry.waitFor();
 await requirements().getByRole('alert').waitFor();await delay(5600);check(writes.length===1,'unknown cancellation automatically repeated');
 await ready();await retry.click();await requirements().getByRole('status').filter({hasText:'物理保护保持'}).waitFor();
 check(writes.length===2,'expected exactly explicit send and original-key recovery');assert.deepEqual(writes[0],writes[1]);check(Boolean(writes[0].key),'original key missing');
 await fixture('command');await fixture('check');await fixture('background-observed');await select(current);
 await telemetry().getByText('速度未知',{exact:true}).waitFor();await timeline().getByText('计划状态：已创建',{exact:true}).waitFor();await fixture('check');
 // Held real response deliberately ignores abort, then switches to the actual member Cookie.
 await select(current);await freshWith({detail:{hold:true,ignoreAbort:true},timeline:{hold:true,ignoreAbort:true}});await held(['detail','timeline']);
 await cookie(env.PULSE_M6_OPERATION_MEMBER_SESSION);await page.evaluate(()=>window.__operationAuth.checkStatus());
 await page.waitForFunction(()=>!document.querySelector('[aria-label="操作详情"]'));
 await release('detail');await release('timeline');await delay(100);
 check(await detail().count()===0,'late old-actor detail returned');check(!(await page.locator('body').innerText()).includes(current),'old actor UUID survived switch');
 const denied=await context.request.get(base+'/api/v1/operations/'+current);assert.equal(denied.status(),403);
 const deniedTimeline=await context.request.get(base+'/api/v1/operations/'+current+'/timeline');assert.equal(deniedTimeline.status(),403);
 await select(privateOp);await timeline().getByText('FOREIGN-PRIVATE-FILENAME.mkv',{exact:true}).waitFor();
 // Role change is actual App auth data, not an injected HTTP status.
 await cookie(env.PULSE_M6_OPERATION_SESSION);await page.evaluate(()=>window.__operationAuth.checkStatus());await select(privateOp);await timeline().getByText('FOREIGN-PRIVATE-FILENAME.mkv',{exact:true}).waitFor();
 await fixture('demote');const roleDenied=await context.request.get(base+'/api/v1/operations/'+privateOp+'/timeline');assert.equal(roleDenied.status(),403);
 await page.evaluate(()=>window.__operationAuth.checkStatus());await page.getByRole('group',{name:/^当前用户：.+（member）$/}).waitFor();
 check(await detail().count()===0,'role change retained previous operation detail');
 check(!(await page.locator('body').innerText()).includes('FOREIGN-PRIVATE-FILENAME'),'old role private timeline survived');await fixture('restore');
 await page.evaluate(()=>window.__operationAuth.checkStatus());await select(current);
 await fixture('revoke');const unauthorized=await context.request.get(base+'/api/v1/operations/'+current);assert.equal(unauthorized.status(),401);
 await page.waitForURL('**/login',{timeout:12000});check(await detail().count()===0,'401 retained private operation');
 check(writes.length===2,'actor changes or GETs emitted a command');assert.deepEqual(errors,[]);await fixture('check');
 console.log('M6_OPERATION_ACTIVITY_BROWSER_PASS '+JSON.stringify({realApp:true,Cookie:true,PG18_6:true,checks,sqlDisplayFixture:true,nativeAcquisition:false,verifiedBytesNotFabricated:true,exactBigInt:true,rateZeroStaleUnknown:true,timelineStoredSteps:125,manualKeyset:true,backgroundConfirmationStable:true,hiddenCounterCheck:true,heldIgnoredAbortActorFence:true,actual401:true,actual403:true,transportAbortRetry:true,realStorage503:true,explicitWithdrawRequests:writes.length,distinctCommandKeys:new Set(writes.map(x=>x.key)).size,unknownTaskProtection:true,extraBusinessWrites:0,Add:0,screenshots:[1280,768,375,320]}));
} finally {await context.close();await browser.close();}
