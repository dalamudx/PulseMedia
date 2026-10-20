import assert from 'node:assert/strict';
import {createRequire} from 'node:module';
import {join} from 'node:path';
const env=process.env,load=createRequire(env.PULSE_M7_MEDIA_SCRAPE_REPO+'/web/package.json');
const {chromium}=load('playwright-core');
const base=env.PULSE_M7_MEDIA_SCRAPE_BASE,shots=env.PULSE_M7_MEDIA_SCRAPE_SCREENSHOTS;
assert(base&&shots?.startsWith('/data/temp/'));
assert.notEqual(new URL(env.PULSE_M7_MEDIA_SCRAPE_CDP).port,'9222');
const browser=await chromium.connectOverCDP(env.PULSE_M7_MEDIA_SCRAPE_CDP);
const context=await browser.newContext({viewport:{width:1280,height:1000},reducedMotion:'reduce',serviceWorkers:'block'});
const errors=[],posts=[],gets=[];let receipt,releasePost,postDelivered;
const delivered=new Promise(resolve=>{postDelivered=resolve;});
try {
 // Delay a real old GET past an account change, ignoring abort only for this probe.
 await context.addInitScript(()=>{
  const native=window.fetch.bind(window);
  window.__scrapeTransport={hold:false,pending:false,release:null};
  window.fetch=async(input,init)=>{
   const url=new URL(typeof input==='string'?input:input.url,location.href),state=window.__scrapeTransport;
   const held=state.hold&&url.pathname==='/api/v1/media/scrape';
   if(held)state.hold=false;
   const request=held&&input instanceof Request?new Request(input,{signal:new AbortController().signal}):input;
   const response=await native(request,held?{...init,signal:new AbortController().signal}:init);
   if(held)await new Promise(resolve=>{state.pending=true;state.release=()=>{state.pending=false;resolve();};});
   return response;
  };
 });
 await context.route('**/*',async route=>{
  const request=route.request(),url=new URL(request.url());
  if(url.origin!==base)return route.abort('blockedbyclient');
  if(url.pathname==='/api/v1/media/scrape'&&request.method()==='POST'){
   posts.push({key:request.headers()['idempotency-key'],body:request.postDataJSON()});
   const response=await route.fetch();assert.equal(response.status(),200);receipt=await response.json();
   postDelivered();await new Promise(resolve=>{releasePost=resolve;});
   return route.abort('failed'); // committed production response is lost, never fabricated.
  }
  if(url.pathname.startsWith('/api/')&&request.method()!=='GET')throw Error('unexpected browser command '+url.pathname);
  if(url.pathname==='/api/v1/media/scrape')gets.push(url.searchParams.get('command_key'));
  return route.continue();
 });
 const cookie=async value=>{await context.clearCookies();await context.addCookies([{name:'pulsemedia_session',value,url:base,httpOnly:true,sameSite:'Strict'}]);};
 await cookie(env.PULSE_M7_MEDIA_SCRAPE_SESSION);
 const page=await context.newPage();page.setDefaultTimeout(10000);page.on('pageerror',e=>errors.push(e.message));
 const panel=()=>page.getByRole('region',{name:'文件刮削与元数据补齐',exact:true});
 const refresh=()=>panel().getByRole('button',{name:'刷新状态与回执',exact:true});
 const submit=()=>panel().getByRole('button',{name:'批量补齐 NFO 与海报',exact:true});
 const counts=()=>panel().getByRole('definition');
 const fixture=async action=>assert.equal((await context.request.post(base+'/__scrape/'+action)).status(),204);
 const ready=()=>panel().getByLabel('媒体库任务状态记录').waitFor();
 const screenshot=async(state,width)=>{
  await page.setViewportSize({width,height:1000});
  await page.waitForFunction(width=>{
   const nav=document.getElementById('app-navigation');
   return innerWidth===width&&(!nav||(!nav.getAnimations().some(a=>a.playState==='running')&&(width>1024||nav.inert)));
  },width);
  await page.waitForFunction(()=>{
   const root=document.querySelector('.media-scrape');if(!root)return false;
   const value=JSON.stringify([...root.querySelectorAll('button,p,dl,label')].map(e=>{const r=e.getBoundingClientRect();return [r.x,r.y,r.width,r.height];}));
   const stable=window.__scrapeGeometry===value;window.__scrapeGeometry=value;return stable;
  },null,{polling:'raf'});
  const failures=await panel().evaluate(root=>{
   const failures=[];
   if(document.documentElement.scrollWidth>innerWidth+1)failures.push('horizontal viewport overflow');
   for(const node of root.querySelectorAll('button,label,dl,p,code,h2')){
    const r=node.getBoundingClientRect();if(!r.width||!r.height)continue;
    if(r.left< -1||r.right>innerWidth+1||node.scrollWidth>node.clientWidth+2)failures.push(node.tagName+' overflow');
    if(node.tagName==='BUTTON'&&(r.width<44||r.height<44))failures.push('button below 44px');
   }return failures;
  });assert.deepEqual(failures,[],state+' '+width);
  await page.evaluate(()=>scrollTo(0,0));await page.screenshot({path:join(shots,state+'-'+width+'.png'),fullPage:true});
 };
 await page.goto(base+'/media',{waitUntil:'domcontentloaded'});await ready();
 const initial=await context.request.get(base+'/api/v1/media/scrape');assert.equal(initial.status(),200);
 const preview=(await initial.json()).preview;
 assert.equal(preview.total,3);assert.equal(preview.missing,1);assert.equal(preview.queued,1);assert.equal(preview.review,1);assert.equal(preview.complete,0);
 assert(await submit().isDisabled());assert.equal(posts.length,0);
 for(const width of [1280,375,320])await screenshot('filled',width);
 await panel().getByRole('checkbox').check();assert(await submit().isEnabled());await submit().click();
 await delivered;assert.equal(posts.length,1);assert(posts[0].key);assert.deepEqual(posts[0].body,{all:true});assert.equal(receipt.total,1);assert.equal(receipt.succeeded,0);
 assert(await submit().isDisabled());assert.equal(await counts().count(),0);
 for(const width of [1280,375,320])await screenshot('pending',width);
 releasePost();await panel().getByRole('alert').waitFor();assert.equal(await counts().count(),0);
 const pending=await page.evaluate(()=>Object.keys(localStorage).filter(k=>k.startsWith('pulse:media-scrape:pending:')).map(k=>JSON.parse(localStorage.getItem(k))));
 assert.equal(pending.length,1);assert.equal(pending[0].key,posts[0].key);
 await page.reload({waitUntil:'domcontentloaded'});await ready();await panel().getByText('批次编号：',{exact:false}).waitFor();
 assert((await panel().innerText()).includes(receipt.id));assert(gets.includes(posts[0].key));assert.equal(posts.length,1);
 await refresh().click();await ready();assert.equal(posts.length,1);
 for(const width of [1280,375,320])await screenshot('receipt',width);
 await fixture('storage-down');assert.equal((await context.request.get(base+'/api/v1/media/scrape')).status(),503);
 await refresh().click();await panel().getByRole('alert').waitFor();assert.equal(await counts().count(),0);assert.equal(await panel().locator('.media-scrape-receipt').count(),0);assert(await submit().isDisabled());assert(await panel().getByRole('checkbox').isDisabled());
 await fixture('storage-up');await refresh().click();await ready();
 await page.evaluate(()=>{window.__scrapeTransport.hold=true;});await refresh().click();await page.waitForFunction(()=>window.__scrapeTransport.pending);
 // Member session was issued by a genuine login in Go; refresh the real identity
 // without unmounting the entire app, so the panel's own abort/account fence runs.
 await cookie(env.PULSE_M7_MEDIA_SCRAPE_MEMBER_SESSION);
 assert.equal((await context.request.get(base+'/api/v1/media/scrape')).status(),403);
 await page.evaluate(()=>window.__scrapeAuth.refreshIdentity());
 await page.getByRole('group',{name:/当前用户：scrape-member（member）/}).waitFor();assert.equal(await panel().count(),0);
 await page.evaluate(()=>window.__scrapeTransport.release());
 await page.evaluate(()=>new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve))));assert.equal(await panel().count(),0);
 await page.reload({waitUntil:'domcontentloaded'});await page.getByRole('group',{name:/当前用户：scrape-member（member）/}).waitFor();assert.equal(await panel().count(),0);
 assert.equal(posts.length,1);assert.deepEqual(errors,[]);
 console.log('M7_MEDIA_SCRAPE_BROWSER_PASS '+JSON.stringify({actualCookie:true,actualApp:true,SQL_prepared:true,genuine_ingestion:false,filesystem_success:false,POST:1,lostResponse:true,recoveredOriginalReceipt:true,automaticReplay:false,actual503:true,actualMember403:true,lateAccountFence:true,screenshots:[1280,375,320]}));
}finally{releasePost?.();await context.close();await browser.close();}
