import assert from 'node:assert/strict';
import {chromium} from 'playwright-core';
import {join} from 'node:path';
import {setTimeout as delay} from 'node:timers/promises';
const setup=JSON.parse(process.env.PULSE_M10_NOTIFICATION_SETUP),base=setup.base,shots=process.env.PULSE_M10_NOTIFICATION_SHOTS;
const browser=await chromium.connectOverCDP(process.env.PULSE_M10_NOTIFICATION_CDP),contexts=[],external=[],errors=[];let checks=0;
const check=(value,message)=>{assert(value,message);checks++;};
const poll=async(fn,message)=>{const end=Date.now()+12000;while(Date.now()<end){if(await fn())return;await delay(50)}throw Error(message)};
const context=await browser.newContext({viewport:{width:1440,height:1000},colorScheme:'dark',reducedMotion:'reduce',serviceWorkers:'block'});contexts.push(context);
await context.addCookies([{name:'pulsemedia_session',value:setup.admin,url:base,httpOnly:true,sameSite:'Lax'}]);
let lost=false,writes=[],failRead=false;
await context.route('**/*',async route=>{
 const r=route.request(),u=new URL(r.url());
 if(u.origin!==base){external.push(u.origin);await route.abort();return}
 if(failRead&&r.method()==='GET'&&u.pathname==='/api/v1/notification-channels'){await route.fulfill({status:503,contentType:'application/json',body:'{"code":"UNAVAILABLE"}'});return}
 if(r.method()==='POST'&&u.pathname.startsWith('/api/v1/notification-channels')){
  writes.push({path:u.pathname,key:r.headers()['idempotency-key'],body:r.postData()});
  if(!lost&&u.pathname===`/api/v1/notification-channels/${setup.channel}/test`){lost=true;const response=await route.fetch({maxRedirects:0,maxRetries:0});assert.equal(response.status(),202);await route.abort('failed');return}
 }
 await route.continue();
});
const page=await context.newPage();page.on('pageerror',e=>errors.push(e.message));
const panel=()=>page.getByRole('region',{name:'通知通道配置',exact:true});
const open=async()=>{await page.goto(base+'/settings');await page.getByRole('button',{name:'通知与投递',exact:true}).click();await panel().getByRole('button',{name:'读取最新通知通道',exact:true}).waitFor()};
try{
 await open();const row=()=>panel().getByRole('listitem',{name:`通知通道 ${setup.channel}`,exact:true});
 await row().getByRole('button',{name:'发送测试通知 Owned WeChat',exact:true}).click();
 await row().getByRole('alert').filter({hasText:'测试排队结果未确认'}).waitFor();check(writes.length===1,'uncertain test automatically retried');
 const original=writes[0];check(original.key&&JSON.parse(original.body).expected_updated_at,'missing original stamp/key');
 check(await page.evaluate(()=>JSON.stringify({...localStorage,...sessionStorage}).includes('owned-private'))===false,'secret persisted');
 await open();await row().getByRole('button',{name:'重试同一测试通知 Owned WeChat',exact:true}).click();
 await row().getByRole('status').filter({hasText:'测试已排队，请在投递记录确认送达'}).waitFor();check(writes.length===2&&writes[1].key===original.key&&writes[1].body===original.body,'reload changed original command');
 check(await row().innerText().then(t=>t.includes('排队不代表已送达')),'queued shown as delivered');
 const form=panel().getByRole('form',{name:'添加独立收件人',exact:true});
 check(await form.getByRole('option').count()===6,'missing notification protocols');
 for(const kind of ['wechat','dingtalk','feishu','bark']){await form.getByRole('combobox',{name:'通知协议',exact:true}).selectOption(kind);check(await form.getByLabel('机器人 HTTPS URL（保存后不回显）',{exact:true}).count()===1,'missing robot URL');if(kind==='feishu')check(await form.getByLabel('已有飞书图片键（可选）',{exact:true}).count()===1,'missing image key');else check(await form.getByLabel('TMDB w342 公开封面 URL（可选）',{exact:true}).count()===1,'missing public image field')}
 await form.getByLabel('通道名称',{exact:true}).fill('Chrome Bark');await form.getByLabel('机器人 HTTPS URL（保存后不回显）',{exact:true}).fill('https://push.example/owned-private-browser');await form.getByLabel('TMDB w342 公开封面 URL（可选）',{exact:true}).fill('https://image.tmdb.org/t/p/w342/browser.jpg');
 await form.getByRole('button',{name:'保存加密通知通道',exact:true}).click();
 await panel().getByRole('status').filter({hasText:'通道配置已加密保存'}).waitFor();
 const bark=()=>panel().getByRole('listitem').filter({hasText:'Chrome Bark · bark'});
 await bark().waitFor();check(writes.length===3,'unexpected channel writes');check(await form.getByLabel('机器人 HTTPS URL（保存后不回显）',{exact:true}).inputValue()==='','saved URL echoed');
 const barkID=await bark().getAttribute('aria-label').then(t=>t.replace('通知通道 ',''));
 await page.evaluate(()=>{const original=Storage.prototype.setItem;window.__ownedStorageOriginal=original;Storage.prototype.setItem=function(key,value){if(key.startsWith('pulsemedia:notification-test:'))throw Error('owned quota');return original.call(this,key,value)}});
 await bark().getByRole('button',{name:'发送测试通知 Chrome Bark',exact:true}).click();await bark().getByRole('alert').filter({hasText:'原命令未能可靠保存'}).waitFor();check(writes.length===3,'storage failure sent test');
 await page.evaluate(()=>{Storage.prototype.setItem=window.__ownedStorageOriginal});await open();
 await bark().getByRole('button',{name:'发送测试通知 Chrome Bark',exact:true}).click();await bark().getByRole('status').filter({hasText:'测试已排队'}).waitFor();check(writes.length===4,'test not queued once');
 for(const width of [1440,768,375,320]){
  await page.setViewportSize({width,height:1000});if(width<1024)await poll(async()=>page.evaluate(()=>document.querySelector('.sidebar')?.getBoundingClientRect().right<=1),'navigation did not close');
  await panel().scrollIntoViewIfNeeded();await page.evaluate(()=>Promise.all(document.getAnimations().filter(a=>a instanceof CSSTransition).map(a=>a.finished.catch(()=>{}))));await page.evaluate(()=>scrollTo(0,0));
  check(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),'horizontal overflow '+width);
  const frame=await panel().evaluate(root=>{const r=root.getBoundingClientRect();return {left:r.left,right:r.right,width:r.width,viewport:innerWidth,scrollX}});check(frame.left>=-1&&frame.right<=frame.viewport+1,'panel outside viewport '+JSON.stringify(frame));
  const outside=await panel().evaluate(root=>{const edge=root.getBoundingClientRect();return [...root.querySelectorAll('button,input,select,fieldset,.config-heading,li')].map(e=>({label:e.getAttribute('aria-label')||e.tagName,left:e.getBoundingClientRect().left,right:e.getBoundingClientRect().right})).filter(e=>e.left<edge.left-1||e.right>edge.right+1)});check(outside.length===0,'clipped panel controls '+width+' '+JSON.stringify(outside));
  for(const label of ['发送测试通知 Owned WeChat','发送测试通知 Chrome Bark']){const box=await panel().getByRole('button',{name:label,exact:true}).boundingBox();check(box&&box.width>=44&&box.height>=44,'small touch target '+width)}
  await panel().screenshot({path:join(shots,`notification-${width}.png`),animations:'disabled',style:'.topbar,.sidebar{visibility:hidden!important}'});
 }
 await page.setViewportSize({width:1440,height:1000});await form.getByRole('combobox',{name:'通知协议',exact:true}).selectOption('feishu');await form.screenshot({path:join(shots,'notification-feishu.png'),animations:'disabled',style:'.topbar,.sidebar{visibility:hidden!important}'});
 failRead=true;await panel().getByRole('button',{name:'读取最新通知通道',exact:true}).click();await panel().getByRole('alert').filter({hasText:'通知通道读取失败'}).waitFor();check(await panel().getByRole('listitem').count()===0,'failed read retained stale test actions');check(await form.getByRole('button',{name:'保存加密通知通道',exact:true}).isDisabled(),'failed read allowed save');await panel().screenshot({path:join(shots,'notification-unavailable.png'),animations:'disabled',style:'.topbar,.sidebar{visibility:hidden!important}'});failRead=false;
 await context.addCookies([{name:'pulsemedia_session',value:setup.member,url:base,httpOnly:true,sameSite:'Lax'}]);await page.goto(base+'/settings');check(await page.getByRole('button',{name:'通知与投递',exact:true}).count()===0,'member exposed notification management');check(await panel().count()===0,'member retained previous account data');
 await context.addCookies([{name:'pulsemedia_session',value:setup.admin,url:base,httpOnly:true,sameSite:'Lax'}]);await open();await bark().waitFor();check(await bark().count()===1,'A-B-A did not restore current account');
 check(await page.evaluate(()=>Object.keys(localStorage).filter(k=>k.startsWith('pulsemedia:notification-test:')).length)===0,'confirmed test receipt not cleared');check(errors.length===0&&external.length===0,'page errors or outside requests');
 console.log(`M10_NOTIFICATION_CHROME_PASS checks=${checks} screenshots=6 viewports=1440_768_375_320 original=RELOAD_SAME_KEY storage_failure=NO_POST Cookie=REAL queued=NOT_DELIVERED account=ISOLATED bark=${barkID}`);
}finally{for(const item of contexts.reverse())await item.close();await browser.close()}
