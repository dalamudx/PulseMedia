import assert from 'node:assert/strict';
import {chromium} from 'playwright-core';
const base=process.env.PULSE_COMPLETE_UI_BASE, sub=process.env.PULSE_COMPLETE_UI_SUB, session=process.env.PULSE_COMPLETE_UI_SESSION, control=process.env.PULSE_COMPLETE_UI_CONTROL;
if(!base||!sub||!session||!control)throw Error('owned native browser fixture required');
const browser=await chromium.launch({executablePath:'/usr/bin/google-chrome',headless:true,args:['--no-sandbox','--disable-dev-shm-usage','--disable-background-networking']});
try {
 const context=await browser.newContext({viewport:{width:1280,height:900}});
 await context.addCookies([{name:'pulsemedia_session',value:session,url:base,httpOnly:true,sameSite:'Strict'}]);
 const page=await context.newPage();page.setDefaultTimeout(15000);const errors=[];page.on('pageerror',e=>errors.push(e.message));
 async function waitUntil(check,description){const deadline=performance.now()+15000;while(performance.now()<deadline){if(await check()===true)return;await new Promise(resolve=>setTimeout(resolve,25));}throw Error(`Timed out: ${description}`);}
 async function open(){await page.goto(base+'/subscriptions');await page.getByRole('button',{name:'查看Mapping Browser Show订阅详情',exact:true}).click();await page.getByRole('button',{name:'查看编号映射',exact:true}).click();await page.getByRole('region',{name:'完整季覆盖审阅与获取',exact:true}).waitFor();}
 await open();
 const single=page.getByRole('region',{name:'真实资源单集审核',exact:true});
 await single.getByRole('textbox',{name:'单集资源关键词'}).fill('Mapping Browser Show');
 const search=page.waitForResponse(r=>r.url().includes('/api/v1/releases/search'));
 await single.getByRole('button',{name:'检索真实站点资源',exact:true}).click();
 const searched=await search;assert.equal(searched.status(),200);
 const select=single.getByRole('combobox',{name:'待审核站点资源'});await select.locator('option').nth(1).waitFor({state:'attached'});
 const panel=page.getByRole('region',{name:'完整季覆盖审阅与获取',exact:true});
 const results=(await searched.json()).results;const bound=results.find(r=>r.indexer_name==='coverage fixture'&&r.title.includes('S01E01E02'));const unbound=results.find(r=>r.indexer_name!=='coverage fixture'&&r.title.includes('S01E01E02'));assert.ok(bound);assert.ok(unbound);
 await select.selectOption(unbound.id);
 const deniedReply=page.waitForResponse(r=>r.url().endsWith(`/api/v1/subscriptions/${sub}/tv-coverage-candidate-reviews/preview`));
 await panel.getByRole('button',{name:'读取完整季覆盖',exact:true}).click();
 const denied=await deniedReply;assert.equal(denied.status(),200);const deniedBody=await denied.json();assert.equal(deniedBody.can_review,false);assert.deepEqual(deniedBody.rows,[]);
 await select.selectOption(bound.id);
 const previewReply=page.waitForResponse(r=>r.url().endsWith(`/api/v1/subscriptions/${sub}/tv-coverage-candidate-reviews/preview`));
 await panel.getByRole('button',{name:'读取完整季覆盖',exact:true}).click();
 const checked=await previewReply;assert.equal(checked.status(),200);const evidence=await checked.json();
 assert.equal(evidence.can_review,true,`complete coverage held: ${evidence.hold_reason}`);assert.deepEqual(evidence.rows.map(r=>r.unit_key),['S01E01','S01E02']);
 await panel.getByText('已明确对应 2 集，请逐项核对。',{exact:true}).waitFor().catch(async error=>{throw Error(`${error.message}; alerts=${JSON.stringify(await panel.getByRole('alert').allTextContents())}`)});
 assert.deepEqual(await panel.locator('tbody tr td:first-child').allTextContents(),['S01E01','S01E02']);
 const reviewPath=`/api/v1/subscriptions/${sub}/tv-coverage-candidate-reviews`, acquirePath=`/api/v1/subscriptions/${sub}/complete-tv-acquisitions`;
 const calls={review:[],acquire:[]};const first={review:true,acquire:true},dropErrors={},finishDrop={};
 const dropped=Object.fromEntries(['review','acquire'].map(kind=>[kind,new Promise(resolve=>{finishDrop[kind]=resolve;})]));
 const loseReply=(kind,status)=>async route=>{
  calls[kind].push({key:route.request().headers()['idempotency-key'],body:route.request().postData()});
  if(!first[kind]){await route.continue();return;}first[kind]=false;
  try{const response=await route.fetch();if(response.status()!==status)dropErrors[kind]=`native ${kind} returned HTTP ${response.status()}`;await route.abort('failed');}
  catch{dropErrors[kind]=`native ${kind} passthrough failed`;}
  finally{finishDrop[kind]();}
 };
 await page.route('**'+reviewPath,loseReply('review',200));
 await page.route('**'+acquirePath,loseReply('acquire',202));
 const ack=panel.getByRole('checkbox',{name:'已逐集核对完整普通季，仅保存覆盖审阅，不创建下载任务',exact:true});await ack.check();await panel.getByRole('button',{name:'确认保存完整覆盖审阅',exact:true}).click();
 await dropped.review;assert.equal(dropErrors.review,undefined);
 const reviewRetry=()=>panel.getByRole('button',{name:'恢复同一覆盖审阅命令',exact:true});await reviewRetry().waitFor();
 const changeRole=async action=>{const r=await page.request.post(base+control+'/'+action,{headers:{Origin:base}});assert.equal(r.status(),204);};
 await changeRole('demote');const deniedReview=page.waitForResponse(r=>r.url().endsWith(reviewPath)&&r.status()===403);await reviewRetry().click();await deniedReview;await reviewRetry().waitFor({state:'visible'});
 await changeRole('restore');await open();await reviewRetry().click();await panel.getByText(/覆盖审阅已保存，尚未授权获取/).waitFor();
 assert.equal(calls.review.length,3);assert.deepEqual(calls.review,[calls.review[0],calls.review[0],calls.review[0]]);
 const key=await page.evaluate(sub=>Object.keys(localStorage).find(k=>k.startsWith('pulse:complete-tv:v2:')&&k.endsWith(':'+sub)),sub);assert.ok(key);
 const other=await context.newPage();await other.goto(base+'/subscriptions');
 await other.evaluate(key=>new Promise(resolve=>{void navigator.locks.request(key,async()=>{resolve(true);await new Promise(done=>{window.releaseCoverageLock=done;});});}),key);
 await panel.getByRole('button',{name:'重新读取完整覆盖任务状态',exact:true}).click();
 await waitUntil(()=>page.evaluate(async key=>(await navigator.locks.query()).pending.some(l=>l.name===key),key),"refresh must wait for the held account lock");
 await other.evaluate(()=>window.releaseCoverageLock());await other.close();
 await panel.getByRole('button',{name:'读取完整季覆盖',exact:true}).click();await panel.getByText('已明确对应 2 集，请逐项核对。',{exact:true}).waitFor();
 await panel.getByRole('checkbox',{name:'确认获取以上全部目标并创建下载任务；实际发送仍需完整文件覆盖校验，下载完成不代表入库',exact:true}).check();await panel.getByRole('button',{name:'确认获取完整季覆盖',exact:true}).click();
 await dropped.acquire;assert.equal(dropErrors.acquire,undefined);
 const acquireRetry=()=>panel.getByRole('button',{name:'恢复同一完整覆盖获取命令',exact:true});await acquireRetry().waitFor();
 await waitUntil(async()=>{const response=await page.request.get(base+control+"/facts");assert.equal(response.status(),200);return (await response.json()).tracking===1;},"physical task must reach tracking before revocation");
 await changeRole('demote');const deniedAcquire=page.waitForResponse(r=>r.url().endsWith(acquirePath)&&r.status()===403);await acquireRetry().click();await deniedAcquire;await acquireRetry().waitFor({state:'visible'});
 await changeRole('restore');await open();await acquireRetry().click();await panel.getByText(/此审阅已有获取记录/).waitFor();
 assert.equal(calls.acquire.length,3);assert.deepEqual(calls.acquire,[calls.acquire[0],calls.acquire[0],calls.acquire[0]]);assert.notEqual(calls.review[0].key,calls.acquire[0].key);
 const writes=calls.review.length+calls.acquire.length;
 let failRead=true;await page.route(`**/api/v1/subscriptions/${sub}/status`,async route=>{if(failRead){failRead=false;await route.fulfill({status:503,contentType:'application/problem+json',body:JSON.stringify({type:'about:blank',title:'Unavailable',status:503})});}else await route.continue();});
 await panel.getByRole('button',{name:'重新读取完整覆盖任务状态',exact:true}).click();await panel.getByRole('alert').filter({hasText:/命令已确认/}).waitFor();await panel.getByRole('button',{name:'重新读取完整覆盖任务状态',exact:true}).click();
 await panel.getByText(/数据库观测/).waitFor();assert.equal(calls.review.length+calls.acquire.length,writes);
 await page.setViewportSize({width:390,height:844});assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),true);
 const stored=await page.evaluate(key=>JSON.parse(localStorage.getItem(key)),key);assert.equal(stored.entries.length,2);assert.ok(stored.entries.every(e=>e.phase==='confirmed'&&e.coverage.map(r=>r.unitKey).join(',')==='S01E01,S01E02'));
 assert.deepEqual(errors,[]);console.log(JSON.stringify({realCookie:true,reviewPosts:calls.review.length,acquirePosts:calls.acquire.length,reviewRecovery:true,acquisitionRecovery:true,crossTabLock:true,confirmedGetOnly:true,mobileOverflow:false}));
}finally{await browser.close();}
