import assert from 'node:assert/strict';
import {chromium} from 'playwright-core';

const base=process.env.PULSE_REVISION_UI_BASE;
const op=process.env.PULSE_REVISION_UI_OP;
const plan=process.env.PULSE_REVISION_UI_PLAN;
const session=process.env.PULSE_REVISION_UI_SESSION;
const action=process.env.PULSE_REVISION_UI_ACTION??'hardlink';
const target=process.env.PULSE_REVISION_UI_TARGET??'Edited/Movie revised.mkv';
const shot=process.env.PULSE_REVISION_UI_SCREENSHOT;
const labels={hardlink:'硬链接',copy:'复制',reflink:'克隆（reflink）',move:'移动'};
assert(base&&op&&plan&&session,'owned App/browser fixture required');
assert(Object.hasOwn(labels,action),'unsupported fixture action');
let browser;
for(let i=0;i<40;i++) {
 try {browser=await chromium.connectOverCDP(process.env.PULSE_REVISION_UI_CDP??'http://127.0.0.1:9222');break}
 catch {await new Promise(resolve=>setTimeout(resolve,100))}
}
assert(browser,'owned Chrome not available');
try {
 const context=await browser.newContext({viewport:{width:1280,height:880}});
 await context.addCookies([{name:'pulsemedia_session',value:session,url:base,httpOnly:true,sameSite:'Lax'}]);
 const page=await context.newPage();
 page.setDefaultTimeout(12000);
 const errors=[];
 page.on('pageerror',error=>errors.push(String(error)));
 await page.goto(base+'/activity');
 const panel=()=>page.getByRole('region',{name:'修订整理目标'});
 const lookup=async()=>{
  await page.getByLabel('操作 ID').fill(op);
  await page.getByRole('button',{name:'查询 / 刷新',exact:true}).click();
  await panel().waitFor();
 };
 await lookup();
 let lost=true,requests=0,original,receipt;
 await page.route(base+'/api/v1/transfer-plans/'+plan+'/revision',async route=>{
  const request=route.request();
  requests++;
  const intent={key:request.headers()['idempotency-key'],body:request.postDataJSON()};
  assert.equal(intent.body.action,action);
  if(!original)original=intent;else assert.deepEqual(intent,original);
  const response=await route.fetch();
  assert.equal(response.status(),202);
  assert.equal(response.headers()['cache-control'],'private, no-store');
  receipt=await response.json();
  if(lost){lost=false;await route.abort('failed')}else await route.fulfill({response});
 });
 const actionInput=()=>panel().getByLabel('文件动作');
 const confirm=()=>panel().getByRole('button',{name:'确认修订并排队',exact:true});
 const ack=()=>panel().getByLabel('确认使用所选动作和新目标，保留原计划历史',{exact:true});
 const preview=async()=>{
  await panel().getByRole('button',{name:'预览修订目标',exact:true}).click();
  await panel().getByText('原目标不存在，源身份一致，可以确认新目标。',{exact:true}).waitFor();
 };
 await panel().getByLabel('目标相对路径',{exact:true}).fill(target);
 await actionInput().selectOption(action);
 await preview();
 await ack().check();
 await actionInput().selectOption(action==='hardlink'?'copy':'hardlink');
 assert.equal(await confirm().isEnabled(),false);
 assert.equal(await ack().isChecked(),false);
 await actionInput().selectOption(action);
 await preview();
 await panel().getByLabel('目标相对路径',{exact:true}).fill('Edited/Other.mkv');
 assert.equal(await confirm().isEnabled(),false);
 await panel().getByLabel('目标相对路径',{exact:true}).fill(target);
 await preview();
 await panel().getByText(/^原目标：/).waitFor();
 if(process.env.PULSE_REVISION_UI_UNIT) {
  await panel().getByText(`原单集：${process.env.PULSE_REVISION_UI_UNIT}`,{exact:true}).waitFor();
 }
 await panel().getByText(`新目标：${target}`,{exact:true}).waitFor();
 await panel().getByText(/^源文件：/).filter({hasText:labels[action]}).waitFor();
 if(shot)await page.screenshot({path:shot+'.desktop.png',fullPage:true});
 await page.setViewportSize({width:390,height:844});
 await page.waitForFunction(()=>document.querySelector('.sidebar')?.getBoundingClientRect().right<=1);
 assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth),false);
 if(shot)await panel().screenshot({path:shot+'.preview.png',animations:'disabled'});
 await ack().check();
 await confirm().click();
 await panel().getByRole('alert').filter({hasText:'修订回执未确认'}).waitFor();
 assert.equal(requests,1);
 await page.reload();
 await lookup();
 assert.equal(await actionInput().inputValue(),action);
 assert.equal(await actionInput().isDisabled(),true);
 assert.equal(await panel().getByLabel('目标相对路径',{exact:true}).isDisabled(),true);
 await panel().getByRole('button',{name:'恢复同一修订命令',exact:true}).click();
 await panel().getByText(/^新计划 .* 已排队/).waitFor();
 assert.equal(requests,2);
 assert.equal(receipt.previous_plan_id,plan);
 assert.notEqual(receipt.plan_id,plan);
 assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth),false);
 assert.equal(await page.evaluate(()=>Object.keys(sessionStorage).some(key=>key.startsWith('pulse:transfer-revision:'))),false);
 const text=await page.locator('body').innerText();
 assert(!text.includes(session));
 assert.deepEqual(errors,[]);
 if(shot)await page.screenshot({path:shot+'.complete.png',fullPage:true});
 console.log(JSON.stringify({key:original.key,receipt,body:original.body}));
 await context.close();
}finally {await browser.close()}
