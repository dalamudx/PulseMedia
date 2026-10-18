import assert from 'node:assert/strict';
import {chromium} from 'playwright-core';
const base=process.env.PULSE_PAGE_BROWSER_BASE;
if(!base||process.env.PULSE_UI_FIXTURE!=='1')throw Error('Owned page fixture required');
const browser=await chromium.launch({executablePath:'/usr/bin/google-chrome',headless:true,args:['--no-sandbox','--disable-dev-shm-usage','--disable-background-networking']});
try{
 const context=await browser.newContext();
 await context.addCookies([{name:'pulsemedia_session',value:process.env.PULSE_PAGE_BROWSER_SESSION,url:base,httpOnly:true,sameSite:'Strict'}]);
 const page=await context.newPage();page.setDefaultTimeout(15000);const errors=[];page.on('pageerror',e=>errors.push(e.message));
 await page.goto(base+'/discover');
 const open=page.getByRole('button',{name:'🔍 检索站点种子',exact:true});
 for(const [status,count,partial] of [[503,0,false],[200,1,false],[503,0,false],[200,1,true],[200,0,true],[200,0,false],[200,1,false],[503,0,false],[200,1,false],[200,51,true],[200,50,true]]){
  const response=page.waitForResponse(r=>r.url().includes('/api/v1/releases/search?'));
  await open.click();const receipt=await response;assert.equal(receipt.status(),status);
  const data=await receipt.json();assert(!JSON.stringify(data).includes('synthetic-page-browser-key'));
  const dialog=page.getByRole('dialog');await dialog.waitFor();
  if(status===200){
   assert.equal(data.results.length,count);assert.equal(data.partial,partial);
   assert.equal(await dialog.locator('.release-item').count(),count);
   if(partial){await page.setViewportSize({width:390,height:844});await dialog.getByRole('status').filter({hasText:'当前结果不完整'}).waitFor();}
   if(count){await dialog.getByText('Fight.Club.1999.1080p.BluRay.x264-GROUP',{exact:true}).first().waitFor();}
   else if(partial){await dialog.getByText('当前结果不完整，尚不能判断是否有匹配资源。',{exact:true}).waitFor();}
   else{await dialog.getByText('未在已配置站点找到匹配的种子资源。',{exact:true}).waitFor();}
   if(!partial)assert.equal(await dialog.getByRole('alert').count(),0);
  }else{await dialog.getByRole('alert').filter({hasText:'不能判定没有资源'}).waitFor();}
  if(!count){assert.equal(await dialog.locator('.release-item').count(),0);assert.equal(await dialog.getByText('Fight.Club.1999.1080p.BluRay.x264-GROUP',{exact:true}).count(),0)}
  assert(!(await dialog.innerText()).includes('synthetic-page-browser-key'));
  await dialog.getByRole('button',{name:'关闭资源检索',exact:true}).click();
 }
 assert(!(await page.evaluate(()=>JSON.stringify({local:{...localStorage},session:{...sessionStorage}}))).includes('synthetic-page-browser-key'));
 assert.deepEqual(errors,[]);await context.close();
 console.log('PAGE_BROWSER_NATIVE_BOUNDED_PAGES_PARTIAL_CONFIG_CHANGE_RETRY_NO_SECRET_PASS');
}finally{await browser.close();}
