package httpapi_test

import "testing"

// Main selects this program in the existing owned native-App/PG/Chrome harness.
func TestM6MediaDetailActualChromeHTTPPG(t *testing.T) { m5DiscoveryBrowser(t, "details") }

const m6MediaDetailChromeProgram = `
import assert from 'node:assert/strict';
import {createRequire} from 'node:module';
import {readFile,stat} from 'node:fs/promises';
const load=createRequire(process.env.PULSE_M5_CREATION_REPO+'/web/package.json');
const {chromium}=load('playwright-core');
const base=process.env.PULSE_M5_DISCOVERY_BASE;
const profile=process.env.PULSE_M5_DISCOVERY_SCRATCH+'/chrome';
const context=await chromium.launchPersistentContext(profile,{executablePath:process.env.PULSE_M5_CHROME||'/usr/bin/google-chrome',headless:true,viewport:{width:1440,height:1000},args:['--remote-debugging-port=0','--no-sandbox','--disable-dev-shm-usage','--disable-background-networking','--disable-component-update','--no-first-run']});
const gates=[];
try {
 const port=Number((await readFile(profile+'/DevToolsActivePort','utf8')).split('\n')[0]);
 assert(port>0&&port!==9222,'owned Chrome must use ephemeral CDP');
 await context.addCookies([{name:'pulsemedia_session',value:process.env.PULSE_M5_DISCOVERY_SESSION,url:base,httpOnly:true,sameSite:'Strict'}]);
 await context.route('**/*',route=>new URL(route.request().url()).origin===base?route.continue():route.abort('blockedbyclient'));
 // Make late real responses observable even when the hook aborts its old request.
 await context.addInitScript(()=>{
  const original=window.fetch.bind(window);
  window.fetch=(input,init)=>{
   const url=new URL(typeof input==='string'?input:input.url,location.href);
   if(url.pathname.startsWith('/api/v1/media/tmdb/')){
    const signal=new AbortController().signal;
    return original(input instanceof Request?new Request(input,{signal}):input,{...init,signal});
   }
   return original(input,init);
  };
 });
 const page=await context.newPage();page.setDefaultTimeout(12000);
 const errors=[],requests=[],imageRequests=[];
 page.on('pageerror',error=>errors.push(error.message));
 page.on('request',request=>{
  const url=new URL(request.url());
  if(url.pathname.startsWith('/api/'))requests.push({path:url.pathname,method:request.method(),body:request.postData(),key:request.headers()['idempotency-key']});
  if(request.resourceType()==='image')imageRequests.push(request.url());
 });
 const button=(name,scope=page)=>scope.getByRole('button',{name,exact:true});
 const dialog=()=>page.getByRole('dialog');
 const submit=()=>dialog().locator('.media-detail-actions .btn-primary');
 const card=title=>page.locator('.media-card-wrap').filter({has:page.locator('.caption-title',{hasText:title})});
 const apiPath=(type,id,suffix='')=>'/api/v1/media/tmdb/'+type+'/'+id+suffix;
 const episodeKey=(sn,en)=>'S'+String(sn).padStart(2,'0')+'E'+String(en).padStart(2,'0');
 const posts=()=>requests.filter(r=>r.path==='/api/v1/subscriptions'&&r.method==='POST');
 const read=async path=>{const response=await context.request.get(base+path);assert.equal(response.status(),200,'real GET '+path);return response.json();};
 const evidence=()=>read('/owned-m5-discovery/evidence');
 const frame=()=>page.evaluate(()=>new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve))));
 async function bounded(promise,label){let timer;try{return await Promise.race([promise,new Promise((_,reject)=>{timer=setTimeout(()=>reject(Error('deadline: '+label)),12000);})]);}finally{clearTimeout(timer);}}
 function nextResponse(path,method='GET'){
  const pending=page.waitForResponse(response=>new URL(response.url()).pathname===path&&response.request().method()===method,{timeout:45000});
  void pending.catch(()=>{});return pending;
 }
 async function holdNext(path){
  let enter,release,finish,fail,once=true;
  const entered=new Promise(resolve=>{enter=resolve;}),held=new Promise(resolve=>{release=resolve;}),done=new Promise((resolve,reject)=>{finish=resolve;fail=reject;});
  void done.catch(()=>{});gates.push(release);
  const handler=async route=>{
   if(!once){await route.continue();return;}once=false;
   try{const actual=await route.fetch();assert.equal(actual.status(),200,'hold only a real successful response');enter();await held;await route.fulfill({response:actual});finish();}
   catch(error){fail(error);}
  };
  await page.route(base+path,handler);
  return {entered:()=>bounded(entered,'held real '+path),release:async()=>{release();await bounded(done,'release '+path);await page.unroute(base+path,handler);}};
 }
 async function fault(path,status,action){
  const handler=async route=>{
   const actual=await route.fetch();assert.equal(actual.status(),200,'fault must observe a live real service first');
   await route.fulfill({status,contentType:'application/problem+json',body:JSON.stringify({code:status===404?'NOT_FOUND':'METADATA_UNAVAILABLE',message:'owned transport failure'})});
  };
  await page.route(base+path,handler);
  try{const response=nextResponse(path);await action();assert.equal((await response).status(),status);}
  finally{await page.unroute(base+path,handler);}
 }
 async function open(media,keyboard=false){
  const source=card(media.title);await source.waitFor();
  if(keyboard){const trigger=source.getByTitle('一键追更订阅',{exact:true});await source.hover();await trigger.focus();await trigger.press('Enter');}
  else await source.locator('.caption-title').click();
  await dialog().waitFor();
 }
 async function ready(media){await dialog().getByRole('heading',{level:2,name:media.title,exact:true}).waitFor();await dialog().locator('.media-detail-overview').waitFor();}
 async function close(){await page.keyboard.press('Escape');await dialog().waitFor({state:'hidden'});}
 async function configure(){
  const root=dialog().getByLabel('目标媒体根',{exact:true});await root.waitFor();
  await root.locator('option[value]:not([value=""])').first().waitFor({state:'attached'});
  const roots=await root.locator('option').evaluateAll(options=>options.filter(o=>o.value&&!o.disabled).map(o=>({id:o.value,label:o.textContent})));
  assert(roots.length>0,'actual scanned root required');await root.selectOption(roots[0].id);
  const policy=dialog().getByLabel('质量与过滤策略',{exact:true});await policy.selectOption('default');
  const client=dialog().getByLabel('下载器',{exact:true});
  await client.locator('option').filter({hasText:'Owned discovery qB'}).waitFor({state:'attached'});
  const clientID=await client.locator('option').filter({hasText:'Owned discovery qB'}).getAttribute('value');assert(clientID);await client.selectOption(clientID);
  const indexer=dialog().getByRole('checkbox',{name:/Owned discovery source/});await indexer.waitFor();
  assert.equal(await indexer.isChecked(),false,'no implicit indexer binding');
  await indexer.check();assert.equal(await indexer.isChecked(),true);await indexer.uncheck();
  await page.waitForFunction(()=>document.querySelector('.media-detail-actions .btn-primary')?.disabled===false);
  return {root:roots[0].id,client:clientID,policy:await policy.inputValue()};
 }
 async function season(sn,observed){
  await dialog().getByLabel('查看季',{exact:true}).selectOption(String(sn));
  await dialog().getByText(episodeKey(sn,observed.episodes[0].episode_number)+' · '+observed.episodes[0].name,{exact:true}).waitFor();
  await dialog().getByText('实际返回 '+observed.episodes.length+' 集',{exact:false}).waitFor();
  assert.deepEqual(await dialog().locator('.media-detail-episode-copy strong').allTextContents(),[...observed.episodes].sort((a,b)=>a.episode_number-b.episode_number).map(e=>episodeKey(sn,e.episode_number)+' · '+e.name));
  for(const ep of observed.episodes){
   assert(ep.name?.trim(),'native episode name required');assert(ep.overview?.trim(),'native episode overview required');
   const row=dialog().locator('.media-detail-episode').filter({has:page.getByText(episodeKey(sn,ep.episode_number)+' · '+ep.name,{exact:true})});
   assert((await row.innerText()).includes(ep.overview));assert((await row.innerText()).includes(ep.air_date||'播出日期未知'));
  }
  assert.equal(await dialog().getByRole('button',{name:'选择 '+episodeKey(sn,2),exact:true}).count(),0,'never invent E02 from episode_count');
 }
 async function pressed(keys){
  assert.deepEqual(await dialog().locator('.media-detail-episode-toggle[aria-pressed="true"]').evaluateAll(nodes=>nodes.map(n=>n.getAttribute('aria-label').slice(3))),keys);
 }
 async function noSubmit(){assert(await submit().isDisabled(),'unconfirmed custom scope cannot submit');assert.equal(posts().length,0);}
 async function screenshot(name,width){
  await page.setViewportSize({width,height:width===1440?1000:812});await frame();
  const geometry=await dialog().evaluate(el=>{
   const rect=el.getBoundingClientRect(),scroll=el.querySelector('.media-detail-scroll');
   const controls=[...el.querySelectorAll('button,input,select')].filter(n=>n.getClientRects().length&&!n.closest('.media-detail-cast'));
   return {left:rect.left,right:rect.right,top:rect.top,bottom:rect.bottom,width:innerWidth,height:innerHeight,page:document.documentElement.scrollWidth,scroll:scroll.scrollWidth,client:scroll.clientWidth,controls:controls.map(n=>{const r=n.getBoundingClientRect();return {left:r.left,right:r.right};})};
  });
  assert(geometry.left>=0&&geometry.right<=width+1&&geometry.top>=0&&geometry.bottom<=geometry.height+1,'dialog inside viewport '+JSON.stringify(geometry));
  assert(geometry.page<=width+1&&geometry.scroll<=geometry.client+1,'no page/dialog horizontal overflow');
  assert(geometry.controls.every(r=>r.left>=geometry.left-1&&r.right<=geometry.right+1),'controls must fit dialog');
  await dialog().locator('.media-detail-scroll').evaluate(el=>{el.scrollTop=0;});
  const path=process.env.PULSE_M5_DISCOVERY_SCREENSHOTS+'/'+name+'.png';
  await page.screenshot({path,fullPage:true,animations:'disabled'});assert((await stat(path)).size>1000);
 }
 const before=await evidence();
 const movie=await read(apiPath('movie',550)),tv=await read(apiPath('tv',550)),other=await read(apiPath('tv',551));
 assert.equal(movie.tmdb_id,tv.tmdb_id);assert.notEqual(movie.title,tv.title);
 const movieCast=await read(apiPath('movie',550,'/credits')),tvCast=await read(apiPath('tv',550,'/credits')),emptyCast=await read(apiPath('tv',551,'/credits'));
 assert.equal(movieCast.media_type,'movie');assert.equal(tvCast.media_type,'tv');assert.deepEqual(emptyCast.cast,[],'native TV551 supplies a genuine empty cast');
 const missingPhoto=movieCast.cast.find(actor=>!actor.profile_url);assert(missingPhoto,'native movie actor with absent photo required');
 assert(movie.backdrop_url,'native movie backdrop_path required');assert(movie.overview?.trim());assert(tv.overview?.trim());
 assert(tvCast.cast.length>0);assert.notDeepEqual(movieCast.cast.map(a=>a.name),tvCast.cast.map(a=>a.name),'same-id namespace cast evidence must differ');
 const observed={};for(const sn of [1,2,3])observed[sn]=await read(apiPath('tv',550,'/seasons/'+sn));
 assert.deepEqual(observed[1].episodes.map(e=>e.episode_number).sort((a,b)=>a-b),[1,3]);
 assert.deepEqual(observed[2].episodes.map(e=>e.episode_number).sort((a,b)=>a-b),[1,3]);
 assert(observed[2].episodes.some(e=>!e.air_date),'native season two has unknown episode date');
 assert.deepEqual(observed[3].episodes.map(e=>e.episode_number),[7]);
 await page.goto(base+'/discover');await card(movie.title).waitFor();await card(tv.title).waitFor();await card(other.title).waitFor();
 await open(movie);await ready(movie);
 assert.equal(await dialog().locator('.media-detail-overview').innerText(),movie.overview);
 await dialog().getByText(missingPhoto.name,{exact:true}).waitFor();
 const actor=dialog().locator('.media-detail-actor').filter({has:page.getByText(missingPhoto.name,{exact:true})});await actor.getByRole('img',{name:'暂无照片',exact:true}).waitFor();
 await page.waitForFunction(()=>document.querySelector('.media-detail-artwork[role="img"]')!==null);
 assert(imageRequests.includes(movie.backdrop_url),'actual native backdrop URL attempted, blocked external image uses honest fallback');
 assert.equal(await dialog().getByLabel('查看季',{exact:true}).count(),0,'movie 550 must not show TV 550 seasons');
 for(const status of [503,404]){
  await fault(apiPath('movie',550,'/credits'),status,()=>button('刷新演员',dialog()).click());
  await dialog().getByRole('alert').filter({hasText:'演员信息读取失败'}).waitFor();
  assert.equal(await dialog().getByText('元数据源未提供演员。',{exact:true}).count(),0,'cast failure is not empty');
  const response=nextResponse(apiPath('movie',550,'/credits'));await button('重试演员信息',dialog()).click();assert.equal((await response).status(),200);await dialog().getByText(missingPhoto.name,{exact:true}).waitFor();
 }
 const sourceReady=await context.request.get(base+'/owned-m5-discovery/source-ready',{timeout:50000});assert.equal(sourceReady.status(),204);
 await button('检索资源',dialog()).click();
 await page.getByText('检索已完成。',{exact:true}).first().waitFor({timeout:35000});await page.locator('.release-item').first().waitFor();
 assert((await page.locator('.release-item').first().innerText()).includes('Native.trusted.movie.1999.1080p.BluRay.x264'),'detail search must reach the native Torznab source');
 assert.equal(posts().length,0);await button('关闭资源检索').click();
 await open(other);await ready(other);await dialog().getByText('元数据源未提供演员。',{exact:true}).waitFor();assert.equal(await dialog().locator('.media-detail-actor').count(),0);await close();
 // Hold two real movie responses across a switch to TV with the same numeric id.
 const movieDetailHold=await holdNext(apiPath('movie',550)),movieCastHold=await holdNext(apiPath('movie',550,'/credits'));
 await open(movie);await movieDetailHold.entered();await movieCastHold.entered();await noSubmit();await close();
 await open(tv);await ready(tv);await dialog().getByText(tvCast.cast[0].name,{exact:true}).waitFor();
 await movieDetailHold.release();await movieCastHold.release();await frame();
 assert.equal(await dialog().getByRole('heading',{level:2}).innerText(),tv.title);
 assert.equal(await dialog().locator('.media-detail-overview').innerText(),tv.overview);
 assert.equal(await dialog().getByText(missingPhoto.name,{exact:true}).count(),0,'late movie cast cannot enter TV namespace');
 const bindings=await configure();assert.equal(bindings.policy,'default');
 assert(await dialog().getByRole('radio',{name:'全剧追更',exact:true}).isChecked());
 assert.equal(await dialog().getByRole('checkbox',{name:'包含特别篇（S00）',exact:true}).isChecked(),false);
 await season(1,observed[1]);
 assert.deepEqual(await dialog().getByLabel('查看季',{exact:true}).locator('option').evaluateAll(options=>options.map(o=>Number(o.value))),tv.seasons.filter(s=>s.season_number>0).map(s=>s.season_number));
 const specials=dialog().getByRole('checkbox',{name:'包含特别篇（S00）',exact:true});await specials.check();
 assert.deepEqual(await dialog().getByLabel('查看季',{exact:true}).locator('option').evaluateAll(options=>options.map(o=>Number(o.value))),tv.seasons.map(s=>s.season_number));
 await specials.uncheck();assert.equal(await dialog().getByLabel('查看季',{exact:true}).locator('option[value="0"]').count(),0);
 await dialog().getByRole('radio',{name:'指定季与集',exact:true}).check();await noSubmit();
 const selectSeason=sn=>dialog().locator('[aria-label="订阅的季"]').getByRole('checkbox',{name:new RegExp('^S'+String(sn).padStart(2,'0')+' ·')});
 await selectSeason(1).check();await noSubmit();
 await button('选择 S01E01',dialog()).click();await button('选择 S01E03',dialog()).click({modifiers:['Shift']});await pressed(['S01E01','S01E03']);
 await button('选择 S01E01',dialog()).click();await button('选择 S01E03',dialog()).click({modifiers:['Shift']});await pressed([]);
 await button('从 S01E03 向后选',dialog()).click();await pressed(['S01E03']);
 await button('选择 S01E01',dialog()).click();await button('从上次点击集向后选',dialog()).click();await pressed(['S01E01','S01E03']);
 await button('反选',dialog()).click();await pressed([]);await noSubmit();
 await button('全选本季',dialog()).click();await pressed(['S01E01','S01E03']);await button('清空',dialog()).click();await button('选择 S01E03',dialog()).click();await pressed(['S01E03']);
 // A selected season with a pending real read must invalidate custom submission.
 const seasonHold=await holdNext(apiPath('tv',550,'/seasons/2'));
 await selectSeason(2).check();await dialog().getByLabel('查看季',{exact:true}).selectOption('2');await seasonHold.entered();
 await dialog().getByRole('status').filter({hasText:'正在读取本季完整剧集列表'}).waitFor();await noSubmit();
 assert.equal(await dialog().getByRole('button',{name:'选择 S02E02',exact:true}).count(),0);
 await seasonHold.release();await season(2,observed[2]);await noSubmit();
 await button('选择 S02E03',dialog()).click();await pressed(['S02E03']);
 await page.waitForFunction(()=>document.querySelector('.media-detail-actions .btn-primary')?.disabled===false);
 for(const status of [503,404]){
  await fault(apiPath('tv',550,'/seasons/2'),status,()=>button('刷新本季列表',dialog()).click());
  await dialog().getByRole('alert').filter({hasText:'本季完整剧集列表读取失败'}).waitFor();await noSubmit();
  assert.equal(await dialog().getByText('本季完整列表为空，没有可选择的剧集。',{exact:true}).count(),0,'season failure is not a genuine empty list');
  assert.equal(await dialog().locator('.media-detail-episode-toggle').count(),0,'failed observation cannot offer stale/fictitious episodes');
  const retry=nextResponse(apiPath('tv',550,'/seasons/2'));await button('重试本季列表',dialog()).click();assert.equal((await retry).status(),200);await season(2,observed[2]);await pressed(['S02E03']);
 }
 await season(3,observed[3]);assert.equal(await button('选择 S03E07',dialog()).isDisabled(),true);await selectSeason(3).check();await noSubmit();await button('全选本季',dialog()).click();await pressed(['S03E07']);
 await dialog().getByRole('radio',{name:'全剧追更',exact:true}).check();assert.equal(await dialog().locator('.media-detail-episode-toggle').count(),0);
 await dialog().getByRole('radio',{name:'指定季与集',exact:true}).check();await pressed(['S03E07']);
 // Closing and reopening the same identity must discard both old reads and selection.
 await season(2,observed[2]);const oldSeason=await holdNext(apiPath('tv',550,'/seasons/2'));
 await button('刷新本季列表',dialog()).click();await oldSeason.entered();await noSubmit();await close();
 await open(tv,true);await ready(tv);await configure();
 assert(await dialog().getByRole('radio',{name:'全剧追更',exact:true}).isChecked());
 await season(1,observed[1]);await dialog().getByRole('radio',{name:'指定季与集',exact:true}).check();
 await selectSeason(1).check();await button('选择 S01E03',dialog()).click();
 await oldSeason.release();await frame();await pressed(['S01E03']);assert.equal(await dialog().getByLabel('查看季',{exact:true}).inputValue(),'1');
 await season(2,observed[2]);assert.equal(await selectSeason(2).isChecked(),false);await pressed([]);await season(1,observed[1]);
 // Actual key events must remain in the dialog and Escape must return to its opener.
 await button('关闭影视详情',dialog()).focus();await page.keyboard.press('Shift+Tab');
 assert(await dialog().evaluate(el=>el.contains(document.activeElement)),'reverse Tab trapped');await page.keyboard.press('Tab');
 assert(await button('关闭影视详情',dialog()).evaluate(el=>el===document.activeElement),'forward Tab wraps to first control');
 await close();assert(await card(tv.title).getByTitle('一键追更订阅',{exact:true}).evaluate(el=>el===document.activeElement),'Escape restores actual keyboard trigger');
 // A detail read failure must not preserve a custom command as a writable fallback.
 await open(tv);await ready(tv);await configure();await season(1,observed[1]);await dialog().getByRole('radio',{name:'指定季与集',exact:true}).check();await selectSeason(1).check();await button('选择 S01E03',dialog()).click();
 for(const status of [503,404]){
  await fault(apiPath('tv',550),status,()=>button('刷新影视详情',dialog()).click());await dialog().getByRole('alert').filter({hasText:'影视详情读取失败'}).waitFor();await noSubmit();
  const retry=nextResponse(apiPath('tv',550));await button('重试影视详情',dialog()).click();assert.equal((await retry).status(),200);await ready(tv);
 }
 await season(2,observed[2]);await selectSeason(2).check();await button('选择 S02E03',dialog()).click();
 await season(3,observed[3]);await selectSeason(3).check();await button('全选本季',dialog()).click();
 await page.waitForFunction(()=>document.querySelector('.media-detail-actions .btn-primary')?.disabled===false);
 assert.equal(await dialog().getByLabel('目标媒体根',{exact:true}).inputValue(),bindings.root);
 assert.equal(await dialog().getByLabel('质量与过滤策略',{exact:true}).inputValue(),'default');assert.equal(await dialog().getByLabel('下载器',{exact:true}).inputValue(),bindings.client);
 assert.equal(await dialog().getByRole('checkbox',{name:/Owned discovery source/}).isChecked(),false);
 await screenshot('discovery-desktop',1440);await screenshot('discovery-375',375);await screenshot('discovery-320',320);
 await page.setViewportSize({width:1440,height:1000});
 // Commit one command through the existing parent journal, then lose its HTTP reply.
 let receipt,originalKey,originalBody;
 const createRoute=async route=>{
  if(route.request().method()!=='POST'){await route.continue();return;}
  assert.equal(receipt,undefined,'only one UI creation command');originalKey=route.request().headers()['idempotency-key'];originalBody=route.request().postDataJSON();assert(originalKey);
  const actual=await route.fetch();assert.equal(actual.status(),201,await actual.text());receipt=await actual.json();await route.abort('failed');
 };
 await page.route(base+'/api/v1/subscriptions',createRoute);
 await submit().click();await button('读取原订阅结果').waitFor();assert(receipt?.id);
 assert.equal(originalBody.tmdb_id,550);assert.equal(originalBody.media_type,'tv');assert.equal(originalBody.scope_type,'selected');
 const expected=['S01E03','S02E03','S03E07'];assert.deepEqual(originalBody.episodes,expected);assert.deepEqual(originalBody.seasons,[1,2,3]);assert.equal(originalBody.include_specials,false);
 assert.equal(originalBody.root_id,bindings.root);assert.equal(originalBody.quality_policy_id,'default');assert.equal(originalBody.downloader_id,bindings.client);assert.deepEqual(originalBody.indexer_ids,[]);
 const journal=await page.evaluate(()=>Object.entries(sessionStorage).find(([key])=>key.startsWith('pulse:subscription-create:v1:')));
 assert(journal,'lost response retains parent journal');assert.equal(JSON.parse(journal[1]).key,originalKey);assert.deepEqual(JSON.parse(journal[1]).body,originalBody);
 const replay=nextResponse('/api/v1/subscription-commands/'+encodeURIComponent(originalKey));
 await page.reload();const recovered=await replay;
 assert.equal(recovered.status(),200);assert.equal((await recovered.json()).id,receipt.id);
 await page.waitForURL(base+'/subscriptions?subscription='+receipt.id);
 await page.unroute(base+'/api/v1/subscriptions',createRoute);
 assert.equal(posts().length,1,'read-only replay must not send a second create command');
 const publicReceipt=await context.request.get(base+'/api/v1/subscription-commands/'+encodeURIComponent(originalKey));assert.equal(publicReceipt.status(),200);assert.equal((await publicReceipt.json()).id,receipt.id);
 const detail=await read('/api/v1/subscriptions/'+receipt.id);assert.equal(detail.scope_type,'selected');assert.equal(detail.root_id,bindings.root);assert.equal(detail.media_type,'tv');
 assert.deepEqual(detail.wanted_items.filter(item=>!item.is_skipped&&!item.retired).map(item=>item.unit_key).sort(),expected);
 assert(detail.wanted_items.every(item=>item.unit_key!=='S01E02'&&item.unit_key!=='S02E02'),'persisted Wanted must preserve sparse numbers');
 assert(await page.evaluate(key=>sessionStorage.getItem(key)===null,journal[0]),'confirmed original journal clears only after recovery');
 const after=await evidence();assert.equal(after.subscriptions,before.subscriptions+1);for(const key of ['tasks','jobs','reviews'])assert.equal(after[key],before[key],key+' unchanged');assert.deepEqual(after.operations,before.operations);
 assert.equal(requests.filter(r=>r.method==='POST'&&r.path==='/api/v1/release-searches').length,1,'one explicit native resource search');
 assert.equal(requests.filter(r=>r.method!=='GET'&&r.path!=='/api/v1/subscriptions'&&r.path!=='/api/v1/release-searches').length,0,'detail browsing and selected creation cannot create review/acquisition commands');
 assert.deepEqual(errors,[]);
 console.log('M6_MEDIA_DETAIL_PASS '+JSON.stringify({actualChrome:true,realCookieAppPG:true,nativeTMDB:true,nativeTorznab:true,nativeQB:true,ephemeralCDP:port,mode:'details',sparseWanted:expected,creation201:true,replayed200:true,screenshots:['discovery-desktop.png','discovery-375.png','discovery-320.png']}));
} finally {for(const release of gates)release();await context.close();}
`
