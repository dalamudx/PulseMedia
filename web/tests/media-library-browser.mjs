import assert from 'node:assert/strict';
import {createRequire} from 'node:module';
import {join} from 'node:path';
const env=process.env,load=createRequire(env.PULSE_M6_MEDIA_LIBRARY_REPO+'/web/package.json');
const {chromium}=load('playwright-core');
const base=env.PULSE_M6_MEDIA_LIBRARY_BASE,shots=env.PULSE_M6_MEDIA_LIBRARY_SCREENSHOTS;
assert(base&&shots?.startsWith('/data/temp/'));
assert.notEqual(new URL(env.PULSE_M6_MEDIA_LIBRARY_CDP).port,'9222');
const browser=await chromium.connectOverCDP(env.PULSE_M6_MEDIA_LIBRARY_CDP);
const context=await browser.newContext({viewport:{width:1280,height:1000},reducedMotion:'reduce',serviceWorkers:'block'});
let checks=0;
const errors=[],writes=[],catalog=[];
const check=(value,message)=>{assert(value,message);checks++;};
try {
 await context.addInitScript(()=>{
  const native=window.fetch.bind(window),rules=[],pending=[],receipts=[];
  window.__libraryTransport={rules,pending,receipts,release(id){const i=pending.findIndex(x=>x.id===id);if(i<0)throw Error('missing held '+id);pending.splice(i,1)[0].release();}};
  window.fetch=async(input,init)=>{
   const url=new URL(typeof input==='string'?input:input.url,location.href);
   const isCatalog=url.pathname==='/api/v1/library/catalog',rule=isCatalog?rules.shift():null;
   // Keep the actual request and response; only delivery is delayed and abort ignored.
   const request=rule&&input instanceof Request?new Request(input,{signal:new AbortController().signal}):input;
   const response=await native(request,rule?{...init,signal:new AbortController().signal}:init);
   if(isCatalog)receipts.push({status:response.status,id:rule?.id});
   if(rule)await new Promise(release=>pending.push({id:rule.id,release}));
   return response;
  };
 });
 await context.route('**/*',async route=>{
  const request=route.request(),url=new URL(request.url());
  if(url.origin!==base)return route.abort('blockedbyclient');
  if(url.pathname.startsWith('/api/')&&request.method()!=='GET')writes.push(url.pathname);
  return route.continue();
 });
 const cookie=async value=>{await context.clearCookies();await context.addCookies([{name:'pulsemedia_session',value,url:base,httpOnly:true,sameSite:'Strict'}]);};
 await cookie(env.PULSE_M6_MEDIA_LIBRARY_SESSION);
 const page=await context.newPage();page.setDefaultTimeout(10000);page.on('pageerror',e=>errors.push(e.message));
 page.on('response',response=>{if(new URL(response.url()).pathname==='/api/v1/library/catalog')catalog.push(response.status());});
 const media=()=>page.locator('.media-container');
 const refresh=()=>media().getByRole('button',{name:'重新读取媒体库',exact:true});
 const loaded=()=>media().getByText('当前已加载 4 条文件单元记录。',{exact:true}).waitFor();
 const fixture=async action=>{const response=await context.request.post(base+'/__library/'+action);assert.equal(response.status(),204,'fixture '+action);};
 const arm=ids=>page.evaluate(ids=>window.__libraryTransport.rules.push(...ids.map(id=>({id}))),ids);
 const held=id=>page.waitForFunction(id=>window.__libraryTransport.pending.some(x=>x.id===id),id);
 const release=id=>page.evaluate(id=>window.__libraryTransport.release(id),id);
 const drained=()=>page.evaluate(()=>new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve))));
 const identity=()=>page.evaluate(()=>window.__libraryAuth.refreshIdentity());
 const noRows=async message=>{await drained();check(await media().locator('details,section').count()===0,message);check(!(await media().innerText()).includes('Library Display'),'old title '+message);};
 await page.goto(base+'/media',{waitUntil:'domcontentloaded'});await loaded();
 const actual=await context.request.get(base+'/api/v1/library/catalog');assert.equal(actual.status(),200);
 const data=await actual.json();assert.equal(data.items.length,4);
 for(const entry of data.items){assert.equal(entry.imported,false);assert.equal(entry.confirmed,false);assert.equal(entry.size_bytes,'9223372036854775807');assert(entry.relative_path.length>220);assert(entry.root_name.length>100);assert(!JSON.stringify(entry).includes('/data/temp/'));}
 check(data.items.filter(x=>x.media_type==='tv').length===3,'real sparse TV rows');
 await media().getByRole('heading',{name:/^Library Display Movie/}).waitFor();
 check(await media().getByRole('heading',{name:/^Library Display Series/}).count()===0,'movie filter');
 await media().locator('summary').first().click();
 await media().getByText(/已关联，未入库 · [0-9,]+ 字节/).waitFor();
 const countBefore=catalog.length;
 await media().getByLabel('筛选已加载文件').fill('no-such-loaded-file');
 await media().getByText('当前已加载文件中没有匹配项。',{exact:true}).waitFor();
 await media().getByLabel('筛选已加载文件').fill('movie.2160p');
 await media().getByRole('heading',{name:/^Library Display Movie/}).waitFor();
 await media().getByLabel('筛选已加载文件').fill('');
 check(catalog.length===countBefore,'local loaded filter emitted catalog GET');
 await media().getByRole('button',{name:'剧集',exact:true}).click();
 await media().getByRole('heading',{name:/^Library Display Series/}).waitFor();
 await media().locator('summary').filter({hasText:'第 1 季 · 当前页 2 条文件记录'}).click();
 const episodes=await media().locator('summary').allTextContents();
 check(episodes.findIndex(x=>x.startsWith('S01E02'))<episodes.findIndex(x=>x.startsWith('S01E10')),'numeric episode order');
 await refresh().click();await loaded();
 check(await media().locator('details[open]').count()>=1,'refresh lost expanded season');
 await media().getByRole('button',{name:'文件',exact:true}).click();
 check(await media().locator(':scope > details').count()===4,'files filter');
 // All paths and root labels are exposed at each viewport for real layout checks.
 for(const width of [1280,768,375,320]){
  await page.setViewportSize({width,height:1000});
  await page.waitForFunction(width=>{
   const nav=document.getElementById('app-navigation'),menu=document.querySelector('.mobile-menu-btn');
   if(!nav||!menu||innerWidth!==width||nav.getAnimations().some(a=>a.playState==='running'))return false;
   return width<=1024?nav.inert&&menu.getAttribute('aria-expanded')==='false'&&nav.getBoundingClientRect().right<=1:!nav.inert&&nav.getBoundingClientRect().left>=-1;
  },width);
  for(const view of ['movie','tv','files']){
   await media().getByRole('button',{name:{movie:'电影',tv:'剧集',files:'文件'}[view],exact:true}).click();
   await media().locator('details').evaluateAll(nodes=>nodes.forEach(node=>{node.open=true;}));
   await drained();
   // Geometric stability across animation frames, no fixed screenshot sleep.
   await page.waitForFunction(()=>{
    const root=document.querySelector('.media-container');if(!root)return false;
    const geometry=JSON.stringify([...root.querySelectorAll('button,summary,p,h2')].map(e=>{const r=e.getBoundingClientRect();return [r.x,r.y,r.width,r.height];}));
    const stable=window.__libraryGeometry===geometry;window.__libraryGeometry=geometry;return stable;
   },null,{polling:'raf'});
   const failures=await media().evaluate(root=>{
    const failures=[];
    if(document.documentElement.scrollWidth>innerWidth+1)failures.push('viewport overflow');
    for(const element of root.querySelectorAll('button,input,section,details,summary,p,h1,h2,label')){
     const r=element.getBoundingClientRect();if(!r.width||!r.height||element.getClientRects().length===0)continue;
     if(r.left< -1||r.right>innerWidth+1)failures.push(element.tagName+' outside viewport');
     if(element.scrollWidth>element.clientWidth+2)failures.push(element.tagName+' text/path overflow');
     if(element.tagName==='BUTTON'&&(r.width<44||r.height<44))failures.push('control below 44px');
    }
    const button=root.querySelector('.page-header-actions button'),svg=button?.querySelector('svg');
    if(!button||!svg)failures.push('refresh icon missing');
    else {
     const range=document.createRange();range.selectNodeContents(button);
     const textRects=[...range.getClientRects()].filter(r=>r.width>0&&r.height>0);
     const text=document.createRange();const node=[...button.childNodes].find(n=>n.nodeType===Node.TEXT_NODE&&n.textContent.trim());
     if(!node)failures.push('refresh text missing');
     else {text.selectNodeContents(node);if(text.getClientRects().length!==1)failures.push('refresh text wrapped');const a=text.getBoundingClientRect(),b=svg.getBoundingClientRect();if(Math.abs((a.top+a.height/2)-(b.top+b.height/2))>5)failures.push('refresh icon/text not on same line');}
     if(!textRects.length)failures.push('refresh invisible');
    }
    return failures;
   });
   assert.deepEqual(failures,[],'layout '+view+' '+width);checks++;
   await page.evaluate(()=>scrollTo(0,0));
   await page.screenshot({path:join(shots,view+'-'+width+'.png'),fullPage:true});
  }
 }
 await fixture('check');
 await fixture('storage-down');
 assert.equal((await context.request.get(base+'/api/v1/library/catalog')).status(),503);
 await refresh().click();await media().getByRole('alert').waitFor();await noRows('503 stale cleanup');
 await fixture('storage-up');await refresh().click();await loaded();await fixture('check');
 // Actual old GET is held across identity change; fresh GET also held so it cannot mask a stale flash.
 for(const mode of ['account','role']){
  await arm([mode+'-old']);await refresh().click();await held(mode+'-old');
  await arm([mode+'-fresh']);
  if(mode==='account')await cookie(env.PULSE_M6_MEDIA_LIBRARY_MEMBER_SESSION);else await fixture('demote');
  await identity();await held(mode+'-fresh');
  await page.getByRole('group',{name:mode==='account'?/当前用户：library-member（member）/:/当前用户：creation-admin（member）/}).waitFor();
  await noRows(mode+' before old delivery');
  await release(mode+'-old');await noRows(mode+' old response resurrected');
  await release(mode+'-fresh');await loaded();
  if(mode==='account')await cookie(env.PULSE_M6_MEDIA_LIBRARY_SESSION);else await fixture('restore');
  await identity();await loaded();
 }
 await fixture('deny');
 assert.equal((await context.request.get(base+'/api/v1/library/catalog')).status(),403);
 await refresh().click();await media().getByRole('alert').waitFor();await noRows('production no-read-token 403 cleanup');
 await fixture('allow');await refresh().click();await loaded();await fixture('check');
 await fixture('revoke');
 assert.equal((await context.request.get(base+'/api/v1/library/catalog')).status(),401);
 await refresh().click();await page.getByRole('heading',{name:'登录 PulseMedia',exact:true}).waitFor();
 check(await media().count()===0,'401 retained private library');
 assert.deepEqual(writes,[],'library browser issued an API command');assert.deepEqual(errors,[]);await fixture('check');
 check(catalog.includes(503)&&catalog.includes(403)&&catalog.includes(401),'actual HTTP failure receipts missing');
 console.log('M6_MEDIA_LIBRARY_BROWSER_PASS '+JSON.stringify({checks,realApp:true,Cookie:true,PG18_6:true,SQL_display_only:true,genuine_ingestion:false,linked_not_imported:true,heldActualOldAndFreshGET:true,accountAndRoleFence:true,actual503:true,actual403:'production scoped token',actual401:true,extraBusinessWrites:0,screenshots:[1280,768,375,320]}));
}finally{await context.close();await browser.close();}
