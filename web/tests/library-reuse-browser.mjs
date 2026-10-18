import assert from 'node:assert/strict';
import {chromium} from 'playwright-core';
const base=process.env.PULSE_REUSE_UI_BASE,session=process.env.PULSE_REUSE_UI_SESSION,kind=process.env.PULSE_REUSE_UI_KIND;
assert(base&&session&&process.env.PULSE_REUSE_UI_CDP);
const browser=await chromium.connectOverCDP(process.env.PULSE_REUSE_UI_CDP);
try{
 const context=await browser.newContext({viewport:{width:1280,height:960}});
 await context.addCookies([{name:'pulsemedia_session',value:session,url:base,httpOnly:true,sameSite:'Lax'}]);
 const page=await context.newPage();page.setDefaultTimeout(12000);const errors=[];page.on('pageerror',e=>errors.push(String(e)));
 const panel=()=>page.locator('.library-reuse');
 const open=async()=>{await page.locator('.sub-poster-card').first().click();await panel().locator('summary').click()};
 await page.goto(base+'/subscriptions');await open();
 let first=true,requests=0,original,receipt;
 await page.route(base+'/api/v1/library/reuses',async route=>{
  if(route.request().method()!=='POST'){await route.continue();return}
  requests++;const intent={key:route.request().headers()['idempotency-key'],body:route.request().postDataJSON()};
  if(!original)original=intent;else assert.deepEqual(intent,original);
  assert.equal(intent.body.confirmed_unit_ids.length,kind==='tv'?2:1);
  const response=await route.fetch();assert.equal(response.status(),202,await response.text());assert.equal(response.headers()['cache-control'],'private, no-store');receipt=await response.json();
  if(first){first=false;await route.abort('failed')}else await route.fulfill({response});
 });
 // Failed reads discard permissions; no candidate survives a partial batch read.
 await page.route(base+'/api/v1/library/reuse-previews',route=>route.fulfill({status:503,contentType:'application/json',body:'{"error":{"code":"UNAVAILABLE"}}'}),{times:1});
 await panel().getByRole('button',{name:'预览所选目标',exact:true}).click();await panel().getByRole('alert').waitFor();assert.equal(await panel().getByRole('radio').count(),0);
 await panel().getByRole('button',{name:'预览所选目标',exact:true}).click();await panel().getByRole('radio').first().waitFor();
 await panel().getByRole('radio').first().check();
 assert.equal(await panel().getByRole('button',{name:'确认复用并排队',exact:true}).isEnabled(),false);
 if(kind==='tv'){await panel().getByText('完整覆盖 2 个目标：S01E01、S01E02',{exact:true}).or(panel().getByText('完整覆盖 2 个目标：S01E02、S01E01',{exact:true})).waitFor()}
 const ack=()=>panel().getByLabel('确认以上全部覆盖和目标路径，保留源文件',{exact:true});
 await ack().check();await panel().getByLabel('文件动作',{exact:true}).selectOption('copy');assert.equal(await ack().isChecked(),false);
 await panel().getByLabel('目标相对路径',{exact:true}).fill(kind==='tv'?'Reuse Series/Season 01/Reuse.S01E01-E02.mkv':'Reused/Movie (2020).mkv');
 if(process.env.PULSE_REUSE_SCREENSHOT_DIR)await page.screenshot({path:`${process.env.PULSE_REUSE_SCREENSHOT_DIR}/${kind}.desktop.png`});
 await page.setViewportSize({width:390,height:844});await page.waitForFunction(()=>document.querySelector('.sidebar')?.getBoundingClientRect().right<=1);
 assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth),false);
 if(process.env.PULSE_REUSE_SCREENSHOT_DIR)await page.screenshot({path:`${process.env.PULSE_REUSE_SCREENSHOT_DIR}/${kind}.mobile.png`});
 await ack().check();
 if(process.env.PULSE_REUSE_SCREENSHOT_DIR)await page.screenshot({path:`${process.env.PULSE_REUSE_SCREENSHOT_DIR}/${kind}.mobile-form.png`});
 await panel().getByRole('button',{name:'确认复用并排队',exact:true}).click();await panel().getByRole('alert').filter({hasText:'尚未确认'}).waitFor();assert.equal(requests,1);
 await page.reload();await open();await panel().getByRole('button',{name:'恢复同一复用命令',exact:true}).click();await panel().getByText('复用已排队。刷新复用状态查看整理结果。',{exact:true}).waitFor();assert.equal(requests,2);
 for(let i=0;i<15;i++){
  await panel().getByRole('button',{name:'刷新复用状态',exact:true}).click();
  await page.waitForFunction(()=>!document.querySelector('.library-reuse button')?.disabled);
  if(await panel().getByText('已完整入库',{exact:true}).count())break;
  await page.waitForTimeout(500);
 }
 await panel().getByText('已完整入库',{exact:true}).waitFor();
 await panel().getByRole('button',{name:'预览所选目标',exact:true}).click();await panel().getByText('目标库已拥有',{exact:true}).first().waitFor();
 assert.equal(await panel().getByRole('radio').filter({has:page.locator(':enabled')}).count(),0);
 assert.equal(await panel().evaluate(el=>el.open),true);assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth),false);
 if(kind==='tv'){const progress=page.locator('.overall-lbl');await progress.filter({hasText:'2 / 2'}).waitFor()}
 const journals=await page.evaluate(()=>Object.keys(localStorage).filter(key=>key.startsWith('pulse.library-reuse.v1:')).map(key=>JSON.parse(localStorage.getItem(key))));
 assert.equal(journals.length,1);assert.deepEqual(journals[0].receipt,receipt);assert.equal(journals[0].key,original.key);assert.deepEqual(errors,[]);
 if(process.env.PULSE_REUSE_SCREENSHOT_DIR)await page.screenshot({path:`${process.env.PULSE_REUSE_SCREENSHOT_DIR}/${kind}.completed.png`});
 console.log(`LIBRARY_REUSE_BROWSER_PASSED kind=${kind} requests=${requests} complete_mapping=${kind==='tv'?2:1} original_key=true current_owned=true`);
 await context.close();
}finally{await browser.close()}
