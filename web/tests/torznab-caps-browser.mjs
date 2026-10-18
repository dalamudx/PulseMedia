import assert from 'node:assert/strict';
import {chromium} from 'playwright-core';
const base=process.env.PULSE_CAPS_BROWSER_BASE;
if(!base||process.env.PULSE_UI_FIXTURE!=='1')throw Error('Owned caps fixture required');
const browser=await chromium.launch({executablePath:'/usr/bin/google-chrome',headless:true,args:['--no-sandbox','--disable-dev-shm-usage','--disable-background-networking']});
try{
 const context=await browser.newContext();
 await context.addCookies([{name:'pulsemedia_session',value:process.env.PULSE_CAPS_BROWSER_SESSION,url:base,httpOnly:true,sameSite:'Strict'}]);
 const page=await context.newPage();page.setDefaultTimeout(15000);
 const errors=[];page.on('pageerror',e=>errors.push(e.message));
 await page.goto(base+'/sites');
 const row=name=>page.locator('.indexer-row').filter({has:page.locator('strong',{hasText:new RegExp('^'+name+'$')})});
 for(const name of ['unsupported','bad-key','valid']){
  await row(name).getByText('尚未确认',{exact:true}).waitFor();
  const response=page.waitForResponse(r=>r.request().method()==='POST'&&r.url().endsWith('/test'));
  await row(name).getByRole('button',{name:'测试连接',exact:true}).click();
  const receipt=await response;assert.equal(receipt.status(),200);const data=await receipt.json();assert.equal(data.alive,name==='valid');
  assert(!JSON.stringify(data).includes('synthetic-caps-browser-key'));
  await row(name).getByText(name==='valid'?'最近观测在线':'最近观测离线',{exact:true}).waitFor();
  await page.getByText(name==='valid'?'站点连通正常':'站点不可用',{exact:true}).last().waitFor();
  assert.match(await row(name).innerText(),name==='valid'?/探针成功/:/探针失败，按退避重试/);
 }
 await page.reload();
 for(const name of ['unsupported','bad-key','valid'])await row(name).getByText(name==='valid'?'最近观测在线':'最近观测离线',{exact:true}).waitFor();
 await page.setViewportSize({width:390,height:844});
 assert.equal(await row('valid').getByRole('button',{name:'测试连接',exact:true}).isVisible(),true);
 assert(!(await page.locator('body').innerText()).includes('synthetic-caps-browser-key'));
 assert(!(await page.evaluate(()=>JSON.stringify({local:{...localStorage},session:{...sessionStorage}}))).includes('synthetic-caps-browser-key'));
 assert.deepEqual(errors,[]);
 console.log('CAPS_BROWSER_NATIVE_FAILURE_SUCCESS_REFRESH_MOBILE_NO_SECRET_PASS');
 await context.close();
}finally{await browser.close();}
