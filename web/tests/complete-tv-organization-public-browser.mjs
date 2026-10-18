import assert from 'node:assert/strict';
import {chromium} from 'playwright-core';
const base=process.env.PULSE_ORGANIZATION_UI_BASE,operation=process.env.PULSE_ORGANIZATION_UI_OPERATION,session=process.env.PULSE_ORGANIZATION_UI_SESSION,control=process.env.PULSE_ORGANIZATION_UI_CONTROL;
assert.ok(base&&operation&&session&&control,'explicit native fixture required');
async function waitResolved(page,check,arg){const until=Date.now()+30000;while(Date.now()<until){if(await page.evaluate(check,arg)===true)return;await new Promise(resolve=>setTimeout(resolve,25));}throw Error('Resolved boolean condition timed out: '+check.toString());}
const browser=await chromium.launch({executablePath:'/opt/google/chrome/chrome',headless:true,args:['--no-sandbox','--disable-dev-shm-usage','--disable-background-networking','--disable-gpu','--disable-software-rasterizer','--no-zygote','--disable-breakpad','--disable-crash-reporter']});
try{
 const context=await browser.newContext({viewport:{width:1280,height:900}});await context.addCookies([{name:'pulsemedia_session',value:session,url:base,httpOnly:true,sameSite:'Strict'}]);
 const page=await context.newPage();page.setDefaultTimeout(18000);const errors=[];page.on('pageerror',e=>errors.push(e.message));
 const panel=()=>page.getByRole('region',{name:'完整季整理确认',exact:true});
 async function open(){await page.goto(base+'/activity');const list=page.getByRole('region',{name:'持久操作列表'});await list.getByRole('button',{name:'全部',exact:true}).click();await list.locator('li').filter({hasText:operation}).getByRole('button').click();await panel().waitFor();}
 await open();
 await panel().getByText('第 1 季 · 2 个视频 · 1 个附件留在源目录',{exact:true}).waitFor();
 assert.deepEqual(await panel().locator('li strong').allTextContents(),['S01E01','S01E02']);
 assert.equal(await panel().locator('dd').count(),4);assert.equal(await panel().getByRole('button',{name:'确认完整季整理',exact:true}).isDisabled(),true);
 await page.screenshot({path:process.env.PULSE_TEST_TEMP_ROOT+'/organization-desktop.png',fullPage:true});
 const posts=[];let droppedResolve;const dropped=new Promise(r=>{droppedResolve=r;});let drop=true,failReceiptStorage=false;
 await page.route('**/api/v1/download-tasks/*/complete-tv-organizations',async route=>{
  const req=route.request();posts.push({key:req.headers()['idempotency-key'],body:req.postDataJSON(),url:req.url()});
  assert.deepEqual(Object.keys(req.postDataJSON()).sort(),['operation_id','tracking_job_id']);
  const response=await route.fetch();
  if(drop){drop=false;assert.equal(response.status(),202);await route.abort('failed');droppedResolve(true);return;}
  if(response.status()===202){failReceiptStorage=true;await page.evaluate(()=>{window.originalSetItem=Storage.prototype.setItem;Storage.prototype.setItem=function(k,v){if(k.startsWith('pulse.complete-tv-organization.v1:')&&JSON.parse(v).receipt)throw new Error('controlled storage failure');return window.originalSetItem.call(this,k,v);};});}
  await route.fulfill({response});
 });
 await page.setViewportSize({width:390,height:844});await waitResolved(page,()=>document.querySelector('.sidebar').getBoundingClientRect().right<=1&&document.documentElement.scrollWidth<=innerWidth);await panel().getByRole('checkbox').check();await page.screenshot({path:process.env.PULSE_TEST_TEMP_ROOT+'/organization-mobile-preview.png',fullPage:true});await panel().getByRole('button',{name:'确认完整季整理',exact:true}).click();assert.equal(await dropped,true);
 await waitResolved(page,async operation=>{const r=await fetch('/api/v1/operations/'+operation);if(!r.ok)return false;return (await r.json()).plan_status==='completed';},operation);
 const recovery=()=>panel().getByRole('button',{name:'用原键恢复整理确认',exact:true});await recovery().waitFor();
 async function role(action){const r=await page.request.post(control+'/'+action,{headers:{Origin:new URL(control).origin}});assert.equal(r.status(),204);}
 await role('demote');const denied=page.waitForResponse(r=>r.url().includes('/complete-tv-organizations')&&r.request().method()==='POST'&&r.status()===403);await recovery().click();await denied;await recovery().waitFor();
 await role('restore');await open();await recovery().click();await panel().getByText(/原确认回执：已排队/).waitFor();await panel().getByText(/本地回执保存失败/).waitFor();assert.equal(failReceiptStorage,true);
 assert.equal(posts.length,3);assert.deepEqual(posts,[posts[0],posts[0],posts[0]]);
 const journalKey=await page.evaluate(()=>Object.keys(localStorage).find(k=>k.startsWith('pulse.complete-tv-organization.v1:')));assert.ok(journalKey);
 const other=await context.newPage();await other.goto(base+'/activity');
 assert.equal(await other.evaluate(key=>new Promise(resolve=>{void navigator.locks.request(key,async()=>{window.releaseOrganizationLock=()=>{};resolve(true);await new Promise(done=>{window.releaseOrganizationLock=done;});});}),journalKey),true);
 await page.evaluate(()=>{Storage.prototype.setItem=window.originalSetItem;});const read=panel().getByRole('button',{name:'重新读取整理材料',exact:true});await read.evaluate(button=>button.click());
 await waitResolved(page,async key=>(await navigator.locks.query()).pending.some(v=>v.name===key),journalKey);
 await other.evaluate(()=>window.releaseOrganizationLock());await other.close();
 await page.waitForFunction(key=>Boolean(JSON.parse(localStorage.getItem(key)).receipt),journalKey);assert.equal(posts.length,3);
 await page.reload();await open();await panel().getByText(/原确认回执：已排队/).waitFor();assert.equal(posts.length,3);
 await waitResolved(page,async operation=>{const r=await fetch('/api/v1/operations/'+operation);if(!r.ok)return false;return (await r.json()).plan_status==='completed';},operation);
 await waitResolved(page,()=>Array.from(document.querySelectorAll('button')).some(b=>b.textContent==='重新读取整理材料'&&!b.disabled));assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth&&document.querySelector('.sidebar').getBoundingClientRect().right<=1),true);assert.deepEqual(errors,[]);await page.screenshot({path:process.env.PULSE_TEST_TEMP_ROOT+'/organization-mobile.png',fullPage:true});
 console.log(JSON.stringify({realCookie:true,activityDiscovery:true,exactEpisodes:['S01E01','S01E02'],posts:posts.length,originalKeyAfterLostResponse:true,later403RemainsPending:true,receiptStorageFailureReadOnlyRepair:true,crossTabLock:true,historicalQueuedActualCompleted:true,mobileOverflow:false}));
}finally{await browser.close();}
