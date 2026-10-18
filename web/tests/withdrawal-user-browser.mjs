import assert from 'node:assert/strict';
import {chromium} from 'playwright-core';
const base=process.env.PULSE_WITHDRAW_BASE,session=process.env.PULSE_WITHDRAW_SESSION,op=process.env.PULSE_WITHDRAW_OPERATION,req=process.env.PULSE_WITHDRAW_REQUIREMENT;
if(!base||!session||!op||!req||process.env.PULSE_UI_FIXTURE!=='1')throw Error('owned fixture required');
const browser=await chromium.launch({executablePath:'/usr/bin/google-chrome',headless:true,args:['--no-sandbox','--disable-dev-shm-usage','--disable-background-networking','--disable-gpu']});
try{
 const context=await browser.newContext();const authorize=()=>context.addCookies([{name:'pulsemedia_session',value:session,url:base,httpOnly:true,sameSite:'Lax'}]);await authorize();
 const page=await context.newPage();page.setDefaultTimeout(12000);const errors=[];page.on('pageerror',e=>errors.push(e.message));
 const counts=async()=>{const r=await fetch(base+'/fixture/counts');assert.equal(r.status,200);return r.json()};
 const open=async()=>{await page.goto(base+'/activity');await page.getByLabel('操作 ID',{exact:true}).fill(op);await page.getByRole('button',{name:'查询 / 刷新',exact:true}).click();return page.getByRole('region',{name:'自身入库需求',exact:true})};
 let panel=await open();await panel.getByRole('button',{name:'撤销自身入库需求',exact:true}).waitFor();assert.equal(await panel.getByText('当前需求：待入库',{exact:true}).count(),1);const before=await counts();assert.deepEqual(before,{state:'pending',protected:1,receipts:0,foreign:1});
 assert.equal((await fetch(base+'/fixture/disable')).status,200);await panel.getByRole('button',{name:'撤销自身入库需求',exact:true}).click();const path=`/api/v1/operations/${op}/requirements/${req}/withdraw`;
 const denied=page.waitForResponse(r=>r.url().endsWith(path)&&r.request().method()==='POST');await panel.getByRole('button',{name:'确认撤销自身需求',exact:true}).click();assert.equal((await denied).status(),401);await page.getByLabel('操作 ID',{exact:true}).waitFor({state:'detached'});assert.deepEqual(await counts(),before);
 assert.equal((await fetch(base+'/fixture/restore')).status,200);await authorize();panel=await open();await panel.getByRole('button',{name:'撤销自身入库需求',exact:true}).waitFor();
 let first=true,key='';await page.route('**'+path,async route=>{const k=route.request().headers()['idempotency-key'];if(first){first=false;key=k;const r=await route.fetch();assert.equal(r.status(),200);assert.equal((await r.json()).physical_released,false);await route.abort('failed')}else{assert.equal(k,key);await route.continue()}});
 await panel.getByRole('button',{name:'撤销自身入库需求',exact:true}).click();await panel.getByRole('button',{name:'确认撤销自身需求',exact:true}).click();await panel.getByRole('alert').filter({hasText:'未确认'}).waitFor();await panel.getByRole('button',{name:'重试同一撤销命令',exact:true}).click();await panel.getByRole('status').filter({hasText:'物理保护保持'}).waitFor();await panel.getByText('当前需求：已撤销',{exact:true}).waitFor();assert.deepEqual(await counts(),{state:'released',protected:1,receipts:1,foreign:1});await page.unroute('**'+path);
 await page.setViewportSize({width:390,height:844});panel=await open();await panel.getByText('当前需求：已撤销',{exact:true}).waitFor();assert.deepEqual(await counts(),{state:'released',protected:1,receipts:1,foreign:1});
 assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth+1),true);const text=await page.locator('body').innerText();const storage=await page.evaluate(()=>JSON.stringify([Object.entries(localStorage),Object.entries(sessionStorage)]));assert(!text.includes(session)&&!storage.includes(session));assert.deepEqual(errors,[]);console.log('WITHDRAW_BROWSER_CURRENT_USER_AND_LOST_RECEIPT_PASS');await context.close();
}finally{await browser.close()}
