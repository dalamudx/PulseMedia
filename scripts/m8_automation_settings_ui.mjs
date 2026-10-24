import assert from 'node:assert/strict';
import {createRequire} from 'node:module';
import {mkdir} from 'node:fs/promises';
const load=createRequire(process.env.PULSE_M8_AUTOMATION_UI_REPO+'/web/package.json');
const {chromium}=load('playwright-core');
const setup=JSON.parse(process.env.PULSE_M8_AUTOMATION_UI_SETUP);
const {base,owner,sourceID,movie,tv,downloader}=setup;
const shots=process.env.PULSE_M8_AUTOMATION_UI_SHOTS;
const configPath='/api/v1/sync/douban/config',checkinPath=`/api/v1/indexers/${sourceID}/checkin/config`;
let browser,context,page,phase='launch',sendMode='native',holdNextRead=false,heldRead=null;
const replies=[],puts=[],errors=[],gates=new Set();let checks=0;
const delay=ms=>new Promise(resolve=>setTimeout(resolve,ms));
async function until(check,label){const end=Date.now()+10000;while(Date.now()<end){if(await check()){checks++;return;}await delay(25);}throw Error(label);}
async function owned(path){const r=await page.request.post(base+'/owned/'+path);assert.equal(r.status(),204);}
function gate(){let mark,release;const ready=new Promise(r=>mark=r),wait=new Promise(r=>release=r);const g={ready,mark,wait,release(){release();gates.delete(g);}};gates.add(g);return g;}
async function settings(){await page.goto(base+'/settings',{waitUntil:'domcontentloaded'});await page.getByRole('button',{name:/豆瓣/}).click();const panel=page.getByRole('region',{name:'豆瓣想看同步配置'});await until(()=>panel.getByText(/尚未保存同步设置|已保存版本/).count().then(x=>x>0),'douban ready');return panel;}
async function noPending(panel){await until(()=>panel.getByRole('button',{name:'查询原保存回执',exact:true}).count().then(x=>x===0),'pending cleared');}
async function screenshot(panel,name,width){
 await page.setViewportSize({width,height:1000});
 if(await page.locator('.app-shell.mobile-drawer-open').count())await page.keyboard.press('Escape');
 await until(()=>page.locator('.app-shell.mobile-drawer-open').count().then(x=>x===0),'navigation closed '+width);
 await page.evaluate(async()=>{await Promise.allSettled(document.getAnimations().filter(a=>a.effect?.getTiming().iterations!==Infinity).map(a=>a.finished));window.scrollTo({top:0,left:0,behavior:'instant'});await new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve)));});
 await until(()=>page.evaluate(()=>scrollY===0&&document.documentElement.scrollWidth<=innerWidth+1),'top and viewport bounds '+width);
 const box=await panel.boundingBox();assert(box&&box.x>=-1&&box.x+box.width<=width+1,'panel within viewport '+width);
 await page.screenshot({path:shots+'/'+name+'.png',fullPage:true});
}
async function checkin(){await page.goto(base+'/sites',{waitUntil:'domcontentloaded'});const card=page.locator('.idx-card').filter({hasText:'Owned account statistics source'});const trigger=page.getByRole('button',{name:'定时签到设置与历史'});await until(()=>trigger.count().then(x=>x===1),'one site checkin trigger');await trigger.click();const panel=page.getByRole('region',{name:'站点定时签到配置'});await until(()=>panel.getByText(/尚未保存签到设置|已保存版本/).count().then(x=>x>0),'checkin ready');return panel;}
async function run(){
 browser=await chromium.launch({executablePath:process.env.PULSE_M5_CHROME||'/usr/bin/google-chrome',headless:true,args:['--no-sandbox','--disable-dev-shm-usage','--disable-background-networking'],timeout:15000});
 context=await browser.newContext({viewport:{width:1440,height:1000},serviceWorkers:'block'});
 await context.addCookies([{name:'pulsemedia_session',value:setup.cookie,url:base,httpOnly:true,sameSite:'Strict'}]);delete setup.cookie;
 page=await context.newPage();page.setDefaultTimeout(10000);page.on('pageerror',()=>errors.push('pageerror'));
 page.on('request',r=>{const u=new URL(r.url());if(u.origin===base&&r.method()==='PUT'&&[configPath,checkinPath].includes(u.pathname))puts.push({path:u.pathname,key:r.headers()['idempotency-key'],body:r.postDataJSON()});if(u.origin===base&&r.method()==='POST'&&u.pathname==='/api/v1/sync/douban')errors.push('legacy sync called');});
 for(const path of [configPath,checkinPath])await page.route('**'+path,async route=>{
  const req=route.request();
  if(req.method()==='GET'&&path===configPath&&holdNextRead){holdNextRead=false;const g=heldRead;try{const response=await route.fetch({maxRetries:0,timeout:10000});g.mark();await g.wait;await route.fulfill({response});}catch{}return;}
  if(req.method()!=='PUT')return route.continue();
  const mode=sendMode;sendMode='native';
  if(mode==='before')return route.abort('failed');
  if(mode==='revoke')await owned('demote');
  const response=await route.fetch({maxRetries:0,timeout:10000});
  replies.push({family:path===configPath?"douban":"checkin",status:response.status()});
  if(mode==='after'){assert.equal(response.status(),200);return route.abort('failed');}
  return route.fulfill({response});
 });
 await mkdir(shots,{recursive:true});
 phase='douban initial read and explicit bindings';let d=await settings();
 await d.getByLabel('豆瓣用户ID或主页唯一域名').fill('exherb');
 await d.getByLabel('电影目标根',{exact:true}).selectOption(movie);await d.getByLabel('剧集目标根',{exact:true}).selectOption(tv);
 await d.getByLabel('Owned account statistics source',{exact:true}).check();await d.getByLabel('下载器',{exact:true}).selectOption(downloader);await d.getByLabel('启用想看同步',{exact:true}).check();
 await d.getByRole('button',{name:'保存同步设置',exact:true}).click();await until(()=>d.getByText('已保存版本 1 · 已启用',{exact:true}).count().then(Boolean),'douban first saved');
 assert.deepEqual(puts[0].body.bindings,{indexer_ids:[sourceID],downloader_id:downloader});assert.equal(puts[0].body.movie_root_id,movie);assert.equal(puts[0].body.tv_root_id,tv);assert.equal(puts[0].body.expected_version,0);
 for(const width of [320,375,768,1440])await screenshot(d,'douban-'+width,width);
 phase='douban lost response reload original receipt';sendMode='after';await d.getByLabel('启用想看同步',{exact:true}).uncheck();await d.getByRole('button',{name:'保存同步设置',exact:true}).click();await until(()=>d.getByText(/保存结果尚未确认/).count().then(Boolean),'lost response retained');
 const lost=puts.at(-1),count=puts.length;
 const journal=await page.evaluate(({owner})=>JSON.parse(localStorage.getItem(`pulse.automation-settings.v1:${owner}:douban`)),{owner});assert.equal(journal.key,lost.key);assert.deepEqual(Object.keys(journal).sort(),['hash','key','owner','scope','version']);assert.equal(JSON.stringify(journal).includes('exherb'),false);
 d=await settings();assert.equal(puts.length,count);await d.getByRole('button',{name:'查询原保存回执',exact:true}).click();await noPending(d);assert.equal(puts.length,count);await until(()=>d.getByText('已保存版本 2 · 已停用',{exact:true}).count().then(Boolean),'original receipt version');
 phase='douban real concurrent version conflict read-only resolution';await owned('douban-conflict');await d.getByRole('button',{name:'保存同步设置',exact:true}).click();await until(()=>d.getByText(/保存被拒绝或配置版本变化/).count().then(Boolean),'real conflict');
 const conflictCount=puts.length;await d.getByRole('button',{name:'查询原保存回执',exact:true}).click();await d.getByRole('button',{name:'重新读取配置',exact:true}).click();await until(()=>d.getByText('已保存版本 3 · 已启用',{exact:true}).count().then(Boolean),'conflict current read');await d.getByRole('button',{name:'结束版本冲突并采用当前配置',exact:true}).click();await noPending(d);assert.equal(puts.length,conflictCount);
 phase='douban request not delivered explicit same-key retry';sendMode='before';await d.getByLabel('启用想看同步',{exact:true}).uncheck();await d.getByRole('button',{name:'保存同步设置',exact:true}).click();await until(()=>d.getByText(/保存结果尚未确认/).count().then(Boolean),'undelivered unknown');const absentKey=puts.at(-1).key;d=await settings();const beforeRetry=puts.length;await d.getByRole('button',{name:'查询原保存回执',exact:true}).click();await until(()=>d.getByText(/原请求暂无回执/).count().then(Boolean),'404 preserves key');assert.equal(puts.length,beforeRetry);
 await d.getByLabel('启用想看同步',{exact:true}).uncheck();await d.getByRole('button',{name:'使用原键保存相同内容',exact:true}).click();await noPending(d);assert.equal(puts.at(-1).key,absentKey);await until(()=>d.getByText('已保存版本 4 · 已停用',{exact:true}).count().then(Boolean),'same key retry saved');
 phase='real database read failure withdraws ready';await owned('douban-db/down');await d.getByRole('button',{name:'重新读取配置',exact:true}).click();await until(()=>d.getByText(/配置读取失败/).count().then(Boolean),'503 unknown');assert.equal(await d.getByLabel('豆瓣用户ID或主页唯一域名').isDisabled(),true);const failedReadCount=puts.length;await owned('douban-db/up');await d.getByRole('button',{name:'重新读取配置',exact:true}).click();await until(()=>d.getByText('已保存版本 4 · 已停用',{exact:true}).count().then(Boolean),'DB read restored');assert.equal(puts.length,failedReadCount);
 phase='storage write failure prevents submit';await page.evaluate(()=>{window.__ownedSetItem=Storage.prototype.setItem;Storage.prototype.setItem=function(k,v){if(k.startsWith('pulse.automation-settings.v1:'))throw new Error('owned storage unavailable');return window.__ownedSetItem.call(this,k,v);};});const storageCount=puts.length;await d.getByRole('button',{name:'保存同步设置',exact:true}).click();await until(()=>d.getByText(/无法持久保存恢复记录/).count().then(Boolean),'storage blocked');assert.equal(puts.length,storageCount);await page.evaluate(()=>{Storage.prototype.setItem=window.__ownedSetItem;delete window.__ownedSetItem;});
 phase='corrupt recovery record blocks edits';await page.evaluate(({owner})=>localStorage.setItem(`pulse.automation-settings.v1:${owner}:douban`,'{bad'),{owner});d=await settings();await until(()=>d.getByText(/恢复记录不可读/).count().then(Boolean),'corrupt journal');assert.equal(await d.getByLabel('豆瓣用户ID或主页唯一域名').isDisabled(),true);await page.evaluate(({owner})=>localStorage.removeItem(`pulse.automation-settings.v1:${owner}:douban`),{owner});await d.getByRole('button',{name:'重新读取恢复记录',exact:true}).click();await until(()=>d.getByLabel('豆瓣用户ID或主页唯一域名').isEnabled(),'journal repaired');
 phase='late old-account read cannot restore identity';await page.goto(base+'/settings',{waitUntil:'domcontentloaded'});heldRead=gate();holdNextRead=true;await page.getByRole('button',{name:/豆瓣/}).click();await heldRead.ready;await owned('login/other');d=await settings();await until(()=>d.getByLabel('豆瓣用户ID或主页唯一域名').inputValue().then(v=>v===''),'other account empty');heldRead.release();await delay(100);assert.equal(await d.getByLabel('豆瓣用户ID或主页唯一域名').inputValue(),'');await owned('login/original');d=await settings();await until(()=>d.getByLabel('豆瓣用户ID或主页唯一域名').inputValue().then(v=>v==='exherb'),'original identity restored by own read');
 phase='checkin explicit UID timezone and version';await page.getByRole('button',{name:'站点定时签到',exact:true}).click();await page.getByRole('button',{name:'定时签到设置与历史',exact:true}).click();let c=page.getByRole('region',{name:'站点定时签到配置'});await until(()=>c.getByText('尚未保存签到设置',{exact:true}).count().then(Boolean),'settings checkin ready');assert.equal(await c.getByLabel('站点时区',{exact:true}).inputValue(),'');await c.getByLabel('站点账号UID',{exact:true}).fill('42');await c.getByLabel('站点时区',{exact:true}).fill('Asia/Shanghai');await c.getByLabel('每日签到时间（站点时区）',{exact:true}).fill('09:00');await c.getByRole('button',{name:'保存签到设置',exact:true}).click();await until(()=>c.getByText(/已保存版本 1 · 已停用/).count().then(Boolean),'checkin saved');const signFirst=puts.at(-1);assert.equal(signFirst.body.expected_source_version,setup.sourceVersion);assert.equal(signFirst.body.expected_account_id,'42');assert.equal(signFirst.body.timezone,'Asia/Shanghai');assert.equal(await c.getByText('暂无已记录的签到，不代表当天未签到。',{exact:true}).count(),1);
 for(const width of [375,1440])await screenshot(c,'checkin-'+width,width);
 phase='checkin lost response original receipt only';sendMode='after';await c.getByLabel('启用每日签到',{exact:true}).check();await c.getByRole('button',{name:'保存签到设置',exact:true}).click();await until(()=>c.getByText(/保存结果尚未确认/).count().then(Boolean),'checkin lost response');const signLost=puts.at(-1).key;const signCount=puts.length;c=await checkin();assert.equal(puts.length,signCount);await c.getByRole('button',{name:'查询原保存回执',exact:true}).click();await noPending(c);assert.equal(puts.length,signCount);await until(()=>c.getByText(/已保存版本 2 · 已启用/).count().then(Boolean),'checkin original saved');assert.notEqual(signFirst.key,signLost);
 phase='source configuration fencing and conflict after reload';await owned('source-change');await c.getByRole('button',{name:'保存签到设置',exact:true}).click();await until(()=>c.getByText(/保存被拒绝或配置版本变化/).count().then(Boolean),'stale source refused');c=await checkin();await c.getByRole('button',{name:'查询原保存回执',exact:true}).click();await until(()=>c.getByRole('button',{name:'结束版本冲突并采用当前配置',exact:true}).isEnabled(),'source conflict readable');const sourceCount=puts.length;await c.getByRole('button',{name:'结束版本冲突并采用当前配置',exact:true}).click();await noPending(c);assert.equal(puts.length,sourceCount);assert.equal(await c.getByLabel('启用每日签到',{exact:true}).isChecked(),false);await screenshot(c,'checkin-source-change-375',375);
 phase='permission loss retains original key and hides administrator controls';sendMode='revoke';await c.getByRole('button',{name:'保存签到设置',exact:true}).click();await until(()=>c.getByText(/保存被拒绝或配置版本变化/).count().then(Boolean),'permission refusal');const deniedKey=puts.at(-1).key;await c.getByRole('button',{name:'查询原保存回执',exact:true}).click();await until(()=>c.getByText(/原回执读取或恢复记录清理失败/).count().then(Boolean),'forbidden receipt retained');const deniedJournal=await page.evaluate(({owner,sourceID})=>JSON.parse(localStorage.getItem(`pulse.automation-settings.v1:${owner}:checkin:${sourceID}`)),{owner,sourceID});assert.equal(deniedJournal.key,deniedKey);assert.equal(deniedJournal.rejected,true);await page.reload({waitUntil:'domcontentloaded'});await until(()=>page.getByRole('button',{name:'定时签到设置与历史'}).count().then(n=>n===0),'member cannot configure checkin');const forbidden=await page.request.get(base+checkinPath);assert.equal(forbidden.status(),403);
 assert.deepEqual(errors,[]);assert.ok(checks>=25);console.log(`M8_AUTOMATION_UI_BROWSER_PASS checks=${checks} Cookie=REAL config_transactions=REAL lost_receipt=2 original_keys=true 404_no_automatic_PUT=true version_conflict=REAL source_fence=REAL DB_503=REAL storage_fail_closed=true account_isolation=true role_403=true widths=320_375_768_1440`);
}
let failure,timer,rejectSignal;
const signal=new Promise((_,reject)=>{rejectSignal=reject;});
const interrupted=()=>rejectSignal(new Error('owned browser interrupted'));
process.once('SIGINT',interrupted);process.once('SIGTERM',interrupted);
try{await Promise.race([run(),signal,new Promise((_,reject)=>{timer=setTimeout(()=>reject(new Error('browser deadline')),180000);})]);}catch(e){failure=e;console.error(`M8_AUTOMATION_UI_BROWSER_FAILED phase=${phase} error=${e.message} last_statuses=${JSON.stringify(replies.slice(-6))}`);}
finally{clearTimeout(timer);for(const g of gates)g.release();try{if(context)await context.close();if(browser)await browser.close();assert(!browser?.isConnected());console.log('M8_AUTOMATION_UI_CHROME_CLEANUP_VERIFIED');}catch{failure=true;console.error('M8_AUTOMATION_UI_CLEANUP_FAILED');}process.removeListener('SIGINT',interrupted);process.removeListener('SIGTERM',interrupted);}
if(failure)process.exitCode=1;
