import assert from 'node:assert/strict';
import {existsSync} from 'node:fs';
import {join} from 'node:path';
import {setTimeout as delay} from 'node:timers/promises';
import {chromium} from 'playwright-core';

const env=process.env, base=env.PULSE_M5_ENDPOINT_BASE, native=env.PULSE_M5_ENDPOINT_NATIVE;
const mode=env.PULSE_M5_ENDPOINT_MODE, shots=env.PULSE_M5_ENDPOINT_SCREENSHOTS;
assert(base&&native&&shots?.startsWith('/data/temp/')&&['states','recovery'].includes(mode),'owned fixture required');
let checks=0;
function check(value,message){assert(value,message);checks++;}
function equal(actual,expected,message){check(actual===expected,message);}
async function bounded(promise){
 let timer;
 try{return await Promise.race([promise,new Promise((_,reject)=>{timer=setTimeout(()=>reject(Error('owned browser synchronization timed out')),12000);})]);}
 finally{clearTimeout(timer);}
}
const browser=await chromium.connectOverCDP(env.PULSE_M5_ENDPOINT_CDP);
const context=await browser.newContext({viewport:{width:1280,height:900},serviceWorkers:'block'});
let page;
try{
 await context.addCookies([{name:'pulsemedia_session',value:env.PULSE_M5_ENDPOINT_SESSION,url:base,httpOnly:true,sameSite:'Lax'}]);
 page=await context.newPage(); page.setDefaultTimeout(12000);
 const errors=[], requests=[], responses=[];
 page.on('pageerror',()=>errors.push('pageerror'));
 page.on('request',r=>{if(/\/api\/v1\/(download-clients|media-servers)/.test(r.url()))requests.push({url:r.url(),method:r.method()});});
 page.on('response',r=>{
  if(/\/api\/v1\/(download-clients|media-servers)/.test(r.url()))responses.push((async()=>{
   const text=await r.text().catch(()=>null); if(text===null)return;
   check(!['m5-secret-one','m5-secret-two','m5-hidden-user','m5-hidden-pass','m5-hidden-query'].some(secret=>text.includes(secret)),'endpoint response contains a credential');
   check(!/encrypted|"password"|"api_key"/i.test(text),'endpoint response contains secret fields');
  })());
 });
 page.on('dialog',dialog=>dialog.accept());
 const families=[{family:'download_client',path:'download-clients',tab:'下载器配置',label:'下载器',protocols:['qbittorrent','transmission']},{family:'media_server',path:'media-servers',tab:'媒体服务器',label:'媒体服务器',protocols:['jellyfin','emby']}];
 const api=f=>base+'/api/v1/'+f.path;
 const field=(f,name)=>page.locator('#endpoint-'+f.family+'-'+name);
 const panel=f=>page.locator('section.card').filter({has:page.locator('#endpoint-'+f.family+'-title')});
 const row=(f,name)=>panel(f).locator('.integration-row').filter({has:page.getByText(name,{exact:true})});
 const submit=f=>panel(f).locator('button[type="submit"]');
 const count=(f,method)=>requests.filter(r=>r.method===method&&r.url.startsWith(api(f))).length;
 async function ready(f){await page.waitForFunction(selector=>{const el=document.querySelector(selector);return el&&!el.matches(":disabled");},'#endpoint-'+f.family+'-name');}
 async function blocked(f){check(await field(f,'name').isDisabled(),'draft must be disabled');check(await submit(f).isDisabled(),'save must be disabled');
  const buttons=panel(f).locator('.integration-row button');for(let i=0;i<await buttons.count();i++)check(await buttons.nth(i).isDisabled(),'stale row command must be disabled');}
 async function control(action,f,id,extra={}){
  const status=await page.evaluate(async input=>{const r=await fetch('/__m5/control',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(input)});return r.status;},{Action:action,Family:f?.family,ID:id,...extra});
  equal(status,204,'owned fixture assertion: '+action);
 }
 async function select(f){await page.getByRole('button',{name:f.tab,exact:true}).click();await ready(f);}
 async function fill(f,protocol,name,revision='one',secret=true){
  await field(f,'type').selectOption(protocol);await field(f,'name').fill(name);await field(f,'url').fill((revision==='two'?env.PULSE_M5_ENDPOINT_NATIVE_TWO:native)+'/'+protocol+'/'+revision);
  if(f.family==='download_client')await field(f,'username').fill('m5-user-'+revision);
  await field(f,'secret').fill(secret?'m5-secret-'+revision:'');
  check(!await field(f,'secret').getAttribute('required'),'credential may be blank');
 }
 async function mutate(f,method,click,status){
  const response=page.waitForResponse(r=>r.url().startsWith(api(f))&&r.request().method()===method);
  const [r]=await Promise.all([response,click()]);equal(r.status(),status,'real endpoint mutation status');return r;
 }
 async function save(f,method='PUT'){
  const r=await mutate(f,method,()=>submit(f).click(),method==='POST'?201:200);
  const item=await r.json();await ready(f);return item;
 }
 async function probe(f,name,status=200,alive=true){
  const r=await mutate(f,'POST',()=>row(f,name).getByRole('button',{name:'测试连接：'+name,exact:true}).click(),status);
  if(status===200){const body=await r.json();equal(body.alive,alive,'native probe alive classification');await row(f,name).getByText(alive?'连通正常':'服务未连通',{exact:true}).waitFor();}
  else await row(f,name).getByRole('alert').filter({hasText:'测试失败'}).waitFor();
  await ready(f);
 }
 async function layout(f,width){
  await page.setViewportSize({width,height:900});
  await page.evaluate(()=>Promise.all(document.getAnimations().filter(a=>a.effect?.getTiming().iterations!==Infinity).map(a=>a.finished.catch(()=>{}))));
  const result=await panel(f).evaluate(el=>({overflow:document.documentElement.scrollWidth>innerWidth+1,controls:[...el.querySelectorAll('input,select,button')].filter(e=>e.getClientRects().length).map(e=>({inside:e.getBoundingClientRect().left>=0&&e.getBoundingClientRect().right<=innerWidth+1,label:e.matches('input,select')?!!e.labels?.length:!!e.textContent.trim(),clipped:e.matches("button")&&e.scrollWidth>e.clientWidth+1}))}));
  check(!result.overflow,'page horizontal overflow at '+width);
  check(result.controls.length>=5,'visible form controls');
  check(result.controls.every(c=>c.inside&&c.label&&!c.clipped),'controls fit viewport and have labels at '+width);
 }
 async function screenshot(f,name){
  await page.evaluate(()=>{for(const el of document.querySelectorAll("*"))if(el.scrollTop)el.scrollTop=0;});
  await layout(f,1280);await page.screenshot({path:join(shots,name+'-desktop.png'),fullPage:true});
  await layout(f,375);await page.screenshot({path:join(shots,name+'-375.png'),fullPage:true});
  if(env.PULSE_M5_ENDPOINT_SCREENSHOT_REVIEW==='1'){
   console.log('M5_ENDPOINT_SCREENSHOTS_READY '+join(shots,name));
   const deadline=Date.now()+90000;
   while(!existsSync(join(shots,name+'-inspected'))){assert(Date.now()<deadline,'screenshot review deadline');await delay(250);}
  }
  await page.setViewportSize({width:1280,height:900});
 }
 async function heldList(f){
  let entered,release,completed;
  const seen=new Promise(resolve=>{entered=resolve;}), gate=new Promise(resolve=>{release=resolve;}), done=new Promise(resolve=>{completed=resolve;});
  let used=false;
  const handler=async route=>{
   if(route.request().method()!=='GET'||used)return route.continue();
   used=true;entered();
   try{
    const status=await gate;
    if(status===200)await route.continue();
    else await route.fulfill({status,contentType:'application/json',body:'{"message":"owned list unavailable"}'});
   }finally{completed();}
  };
  await page.route(api(f),handler);
  return {seen:()=>bounded(seen),finish:async(status=503)=>{release(status);if(used)await bounded(done);await page.unroute(api(f),handler);}};
 }
 async function reread(f){await panel(f).locator('.config-heading button').click();await ready(f);}
 await page.goto(base+'/settings');
 if(mode==='states'){
  for(const f of families){
   await select(f);await layout(f,375);await layout(f,1280);
   if(f.family==='download_client'){
    const before=requests.filter(r=>r.method!=='GET').length;
    for(const [type,message] of [['transmission','暂不支持手工单集、整季包和电影升级'],['rtorrent','暂不支持自动追剧'],['qbittorrent','整季包和电影升级均有对应入口']]){
     await field(f,'type').selectOption(type);
     const notice=page.locator('#endpoint-download_client-capabilities');
     check((await notice.innerText()).includes(message),'selected download capability: '+type);
     equal(await field(f,'type').getAttribute('aria-describedby'),'endpoint-download_client-capabilities','capability accessible association');
     await layout(f,375);await layout(f,1280);
    }
    equal(requests.filter(r=>r.method!=='GET').length,before,'capability selection sends no command');
   }
   // Legacy safe URL projection and encrypted bytes survive an empty-secret edit.
   const legacy=f.family==='download_client'?'m5-hidden':'m5-legacy';
   await row(f,legacy).getByRole('button',{name:'编辑',exact:true}).click();
   const list=await page.evaluate(async path=>(await(await fetch(path,{cache:'no-store'})).json()).items,api(f));
   const old=list.find(item=>item.name===legacy);check(!!old?.updated_at,'real selected version');
   await control('snapshot',f,old.id);
   if(f.family==='download_client')check(decodeURIComponent(await field(f,'url').inputValue()).includes('***'),'hidden raw URL projects a placeholder');
   else {equal(await field(f,'type').inputValue(),'plex','existing Plex remains editable');check((await field(f,'type').locator('option:checked').textContent()).includes('已有配置'),'existing Plex label');}
   equal(await field(f,'secret').inputValue(),'','edit does not prefill credential');
   await field(f,'name').fill(legacy+' retained');
   const retained=await save(f);check(retained.updated_at!==old.updated_at,'edit advances real version');await control('retained',f,old.id);
   for(const protocol of f.protocols){
    const name='m5-'+protocol;
    if(f.family==='media_server')check(!await field(f,'type').locator('option[value="plex"]').count(),'new form does not offer Plex');
    await fill(f,protocol,name);await layout(f,375);await page.setViewportSize({width:1280,height:900});
    const item=await save(f,'POST');await probe(f,name);
    await control('snapshot',f,item.id);
    await row(f,name).getByRole('button',{name:'编辑',exact:true}).click();
    equal(await field(f,'secret').inputValue(),'','new edit credential blank');
    await field(f,'name').fill(name+' retained');await save(f);await control('retained',f,item.id);
    await row(f,name+' retained').getByRole('button',{name:'编辑',exact:true}).click();
    await fill(f,protocol,name+' changed','two');await save(f);await probe(f,name+' changed');
    await control('changed',f,item.id,{Protocol:protocol});await control('native-verified',f,item.id,{Protocol:protocol});
    // The same browser distinguishes native offline from storage/decryption failure.
    await control('native-fail');await probe(f,name+' changed',200,false);await control('native-ok');
    await control('snapshot',f,item.id);await control('corrupt',f,item.id);await probe(f,name+' changed',503);await control('restore',f,item.id);await control('native-verified',f,item.id,{Protocol:protocol});
    if(protocol===f.protocols[0])await screenshot(f,'states-'+f.family);
    await mutate(f,'DELETE',()=>row(f,name+' changed').getByRole('button',{name:'删除',exact:true}).click(),204);await ready(f);
    equal(await row(f,name+' changed').count(),0,'deleted row disappears');await control('deleted',f,item.id);
   }
   // A legitimate blank credential is valid on creation too.
   await fill(f,f.protocols[0],'m5-blank-'+f.family,'one',false);const blank=await save(f,'POST');
   await mutate(f,'DELETE',()=>row(f,blank.name).getByRole('button',{name:'删除',exact:true}).click(),204);await ready(f);await control('deleted',f,blank.id);
  }
 }else{
  for(const f of families){
   await select(f);const protocol=f.protocols[0],name='m5-recovery-'+f.family;
   await fill(f,protocol,name);const item=await save(f,'POST');
   await row(f,name).getByRole('button',{name:'编辑',exact:true}).click();await field(f,'name').fill('m5-held-draft');
   let hold=await heldList(f);
   try{
    await panel(f).getByRole('button',{name:'刷新列表',exact:true}).click();await hold.seen();await blocked(f);
    check(!await panel(f).getByText(/暂无配置/).count(),'held GET is not empty state');
   }finally{await hold.finish();}
   await panel(f).getByRole('alert').filter({hasText:'配置列表读取失败'}).waitFor();await blocked(f);
   check(!await panel(f).getByText(/暂无配置/).count(),'failed GET is not empty state');
   await reread(f);equal(await field(f,'name').inputValue(),'m5-held-draft','failed GET retains draft');
   // Save is acknowledged, then its follow-up GET hangs and fails.
   const before=count(f,'PUT');hold=await heldList(f);
   try{
    await mutate(f,'PUT',()=>submit(f).click(),200);await hold.seen();await blocked(f);
    await panel(f).getByRole('status').filter({hasText:'已保存'}).waitFor();
   }finally{await hold.finish();}
   await panel(f).getByRole('alert').filter({hasText:'已保存，但列表刷新失败'}).waitFor();await blocked(f);
   equal(count(f,'PUT'),before+1,'failed follow-up GET must not resubmit');await reread(f);
   await row(f,'m5-held-draft').getByRole('button',{name:'编辑',exact:true}).click();
   await field(f,'name').fill('m5-conflict-draft');await field(f,'secret').fill('m5-secret-two');
   await control('conflict',f,item.id);
   await mutate(f,'PUT',()=>submit(f).click(),409);await blocked(f);
   equal(await field(f,'name').inputValue(),'m5-conflict-draft','409 retains draft');
   check(await field(f,'secret').inputValue()==='m5-secret-two','409 preserves draft secret until explicit reread');
   const conflictCount=count(f,'PUT');await screenshot(f,'recovery-'+f.family);
   await reread(f);equal(await field(f,'name').inputValue(),'m5-concurrent','explicit reread drops stale draft');
   equal(await field(f,'secret').inputValue(),'','explicit reread clears secret');
   await field(f,'name').fill('m5-version-recovered');await save(f);equal(count(f,'PUT'),conflictCount+1,'fresh version permits one write');
   // Commit a real POST, discard only its ACK, and require manual fresh GET.
   await fill(f,protocol,'m5-lost-ack-'+f.family);
   let intercepted=false;
   const lost=async route=>{
    if(route.request().method()!=='POST'||intercepted)return route.continue();intercepted=true;
    const result=await route.fetch({maxRetries:0});equal(result.status(),201,'lost ACK followed a real committed POST');await route.abort('failed');
   };
   await page.route(api(f),lost);
   const posts=count(f,'POST'),gets=count(f,'GET');await submit(f).click();
   await panel(f).getByRole('alert').filter({hasText:'操作结果未知'}).waitFor();await blocked(f);
   equal(count(f,'POST'),posts+1,'lost ACK sends one POST');
   await page.unroute(api(f),lost);
   equal(count(f,'GET'),gets,'lost ACK does not automatically read or replay');
   await reread(f);await row(f,'m5-lost-ack-'+f.family).waitFor();
   equal(count(f,'POST'),posts+1,'manual verification does not repeat POST');
   check(count(f,'GET')>gets,'user explicitly obtains a fresh list');
   await panel(f).getByRole('status').filter({hasText:'请核对配置'}).waitFor();
   // Browser-visible permission failures gate further writes.
   for(const status of [403,401]){
    await row(f,'m5-version-recovered').getByRole('button',{name:'编辑',exact:true}).click();await field(f,'name').fill('m5-denied-draft');
    const deny=route=>route.request().method()==='PUT'?route.fulfill({status,contentType:'application/json',body:'{"message":"owned denied"}'}):route.continue();
    await page.route(api(f)+'/*',deny);const writes=count(f,'PUT');await mutate(f,'PUT',()=>submit(f).click(),status);
    if(status===403){
     await blocked(f);equal(count(f,'PUT'),writes+1,'forbidden write has no automatic extra command');
     await page.unroute(api(f)+'/*',deny);await reread(f);
     await panel(f).getByRole('button',{name:'取消编辑',exact:true}).click();
    }else{
     await page.waitForFunction(selector=>!document.querySelector(selector),'#endpoint-'+f.family+'-name');
     equal(await page.locator('.integration-row button').count(),0,'401 removes all endpoint commands');
     equal(count(f,'PUT'),writes+1,'expired session does not issue extra commands');
     await page.unroute(api(f)+'/*',deny);
     await page.goto(base+'/settings');await select(f);
    }
   }
  }
 }
 await Promise.all(responses);
 const storage=await page.evaluate(async()=>({local:JSON.stringify(localStorage),session:JSON.stringify(sessionStorage),caches:await caches.keys()}));
 check(!JSON.stringify(storage).includes('m5-secret-'),'browser storage contains no endpoint credentials');
 equal(storage.caches.length,0,'no endpoint response cache');equal(errors.length,0,'zero page errors');
 console.log(JSON.stringify({result:'M5_ENDPOINT_'+mode.toUpperCase()+'_PASS',checks,protocols:mode==='states'?4:2,viewport_widths:[1280,375],real_cookie:true,real_App:true,real_native_HTTP:true}));
}finally{
 await context.close();await browser.close();
}