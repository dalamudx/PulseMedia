import assert from 'node:assert/strict';
import {chromium} from 'playwright-core';
const base=process.env.PULSE_MEMBER_PRIVACY_BASE,id=process.env.PULSE_MEMBER_PRIVACY_ID;
if(!base||!id||process.env.PULSE_UI_FIXTURE!=='1')throw Error('Owned privacy fixture required');
const browser=await chromium.launch({executablePath:'/usr/bin/google-chrome',headless:true,args:['--no-sandbox','--disable-dev-shm-usage','--disable-background-networking','--disable-gpu']});
try{
 const context=await browser.newContext();
 await context.addCookies([{name:'pulsemedia_session',value:process.env.PULSE_MEMBER_PRIVACY_SESSION,url:base,httpOnly:true,sameSite:'Lax'}]);
 const page=await context.newPage();page.setDefaultTimeout(15000);
 const errors=[];page.on('pageerror',e=>errors.push(e.message));
 let writes=0;page.on('request',r=>{if(new URL(r.url()).pathname.startsWith('/api/')&&!['GET','HEAD'].includes(r.method()))writes++});
 await page.goto(base+'/subscriptions');
 const preflight=await page.evaluate(async()=>{
  const auth=await fetch('/api/v1/auth/status').then(r=>r.json());
  const response=await fetch('/api/v1/subscriptions');const list=await response.json();
  return {initialized:auth.initialized,authenticated:!!auth.current_user,listStatus:response.status,titles:list.items?.map(x=>x.title),location:location.pathname,body:document.body.innerText.slice(0,250)};
 });
 assert.equal(preflight.initialized,true,JSON.stringify(preflight));assert.equal(preflight.authenticated,true,JSON.stringify(preflight));
 assert.equal(preflight.listStatus,200,JSON.stringify(preflight));assert(preflight.titles?.includes('Fight Club'),JSON.stringify(preflight));
 const card=page.locator('.sub-poster-card').filter({hasText:'Fight Club'});await card.waitFor();await card.click();
 const dialog=page.getByRole('dialog');await dialog.waitFor();
 await dialog.getByText(process.env.PULSE_MEMBER_PRIVACY_ROOT_NAME,{exact:true}).waitFor();
 assert(!(await page.locator('body').innerText()).includes(process.env.PULSE_MEMBER_PRIVACY_ROOT_PATH));
 const check=async()=>page.evaluate(async(id)=>{
  const list=await fetch('/api/v1/subscriptions').then(r=>r.json());
  const detail=await fetch('/api/v1/subscriptions/'+id).then(r=>r.json());
  const status=await fetch('/api/v1/subscriptions/'+id+'/status');
  const settings=await fetch('/api/v1/settings');
  return {list,detail,status:status.status,settings:settings.status};
 },id);
 for(let n=0;n<2;n++){
  const result=await check();assert.equal(result.status,200);assert.equal(result.settings,403);
  const row=result.list.items.find(r=>r.id===id);assert(row);assert.equal(row.root_name,process.env.PULSE_MEMBER_PRIVACY_ROOT_NAME);
  assert(!Object.hasOwn(row,'root_path'));assert(!Object.hasOwn(result.detail,'root_path'));
  if(n===0){await page.reload();await card.waitFor();}
 }
 assert.equal(writes,0);assert.deepEqual(errors,[]);
 console.log(JSON.stringify({passed:true,realPG:true,memberListDetailRefresh:true,rootNameVisible:true,rawPathOmitted:true,statusAvailable:true,settingsForbidden:true,noWrites:true}));
}finally{await browser.close()}
