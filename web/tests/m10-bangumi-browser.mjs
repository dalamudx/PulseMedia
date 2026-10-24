import assert from 'node:assert/strict';
import {chromium} from 'playwright-core';
import {join} from 'node:path';
import {setTimeout as delay} from 'node:timers/promises';
const setup=JSON.parse(process.env.PULSE_M10_BANGUMI_SETUP),base=setup.base,shots=process.env.PULSE_M10_BANGUMI_SHOTS;
const browser=await chromium.connectOverCDP(process.env.PULSE_M10_BANGUMI_CDP),contexts=[];let checks=0;
const check=(value,message)=>{assert(value,message);checks++;};
const context=await browser.newContext({viewport:{width:1440,height:1000},colorScheme:'dark',reducedMotion:'reduce',serviceWorkers:'block'});contexts.push(context);
await context.addCookies([{name:'pulsemedia_session',value:setup.admin,url:base,httpOnly:true,sameSite:'Lax'}]);
let failure=false,writes=0;const errors=[];
await context.route('**/*',async route=>{const r=route.request(),url=new URL(r.url());if(url.origin!==base){await route.abort();return};if(r.method()!=='GET')writes++;if(failure&&url.pathname==='/api/v1/metadata/bangumi/calendar'){await route.fulfill({status:503,contentType:'application/json',body:'{"code":"UNAVAILABLE"}'});return};await route.continue()});
const page=await context.newPage();page.on('pageerror',e=>errors.push(e.message));
const panel=()=>page.getByRole('region',{name:'Bangumi动漫新番周历',exact:true});
const open=async()=>{await page.goto(base+'/discover');await page.getByRole('tab',{name:'动漫新番周历',exact:true}).click();await panel().getByRole('button',{name:'刷新周历',exact:true}).waitFor();await panel().getByRole('button',{name:'周一',exact:true}).click();await panel().getByRole('heading',{name:'周历测试番剧',exact:true}).waitFor()};
try{
 await open();check(writes===0,'calendar made a write');check(await panel().innerText().then(s=>s.includes('首播 2023-09-29')&&s.includes('Bangumi评分 8.9')),'premiere/rating missing');
 await panel().getByRole('button',{name:'周日',exact:true}).click();await panel().getByRole('heading',{name:'首播未知的新番',exact:true}).waitFor();check(await panel().innerText().then(s=>s.includes('首播日期未知')&&s.includes('Bangumi暂无评分')),'unknown metadata invented');
 await panel().getByRole('button',{name:'整周',exact:true}).click();const filter=panel().getByLabel('中日名称筛选',{exact:true});await filter.fill('カレンダー');check(await panel().getByRole('heading',{name:'周历测试番剧',exact:true}).count()===1,'Japanese filter missing');check(await panel().getByRole('heading',{name:'首播未知的新番',exact:true}).count()===0,'filter retained wrong work');await filter.fill('不匹配');await panel().getByRole('heading',{name:'当前筛选没有番剧',exact:true}).waitFor();check(writes===0,'filter made a write');await filter.fill('');await panel().getByRole('button',{name:'周一',exact:true}).click();
 for(const width of [1440,768,375,320]){
  await page.setViewportSize({width,height:1000});await panel().scrollIntoViewIfNeeded();await page.evaluate(()=>Promise.all(document.getAnimations().filter(a=>a instanceof CSSTransition).map(a=>a.finished.catch(()=>{}))));
  check(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),'page overflow '+width);
  const outside=await panel().evaluate(root=>{const edge=root.getBoundingClientRect();return [...root.querySelectorAll('button,input,article')].filter(e=>{const r=e.getBoundingClientRect();return r.left<edge.left-1||r.right>edge.right+1}).length});check(outside===0,'clipped controls '+width);
  const box=await panel().getByRole('button',{name:'追番 · 选择作品',exact:true}).boundingBox();check(box&&box.width>=44&&box.height>=44,'small chase button '+width);
  await panel().screenshot({path:join(shots,`bangumi-${width}.png`),animations:'disabled',style:'.topbar,.sidebar{visibility:hidden!important}'});
 }
 await page.setViewportSize({width:1440,height:1000});await panel().getByRole('button',{name:'追番 · 选择作品',exact:true}).click();const candidates=panel().getByRole('region',{name:'TMDB追番候选',exact:true});await candidates.getByRole('button',{name:'查看详情并追番',exact:true}).waitFor();check(writes===0,'lookup auto-created subscription');check(await candidates.innerText().then(s=>s.includes('TMDB 42')),'namespace candidate missing');
 await candidates.getByRole('button',{name:'查看详情并追番',exact:true}).click();const dialog=page.getByRole('dialog');await dialog.getByRole('button',{name:'保存剧集订阅',exact:true}).waitFor();await dialog.getByLabel('目标媒体根',{exact:true}).selectOption(setup.root);const save=dialog.getByRole('button',{name:'保存剧集订阅',exact:true});await save.waitFor();
 const deadline=Date.now()+10000;while(await save.isDisabled()&&Date.now()<deadline)await delay(50);check(await save.isEnabled(),'review did not become ready: '+await dialog.innerText());await dialog.screenshot({path:join(shots,'bangumi-review.png'),animations:'disabled'});await save.click();await page.waitForURL('**/subscriptions?subscription=*');check(writes===1,'subscribe not exactly once');
 await open();failure=true;await panel().getByRole('button',{name:'刷新周历',exact:true}).click();await panel().getByRole('alert').filter({hasText:'周历读取失败'}).waitFor();check(await panel().getByRole('button',{name:'追番 · 选择作品',exact:true}).count()===0,'failed read retained action');await panel().screenshot({path:join(shots,'bangumi-unavailable.png'),animations:'disabled',style:'.topbar,.sidebar{visibility:hidden!important}'});failure=false;
 await context.addCookies([{name:'pulsemedia_session',value:setup.member,url:base,httpOnly:true,sameSite:'Lax'}]);await open();check(await panel().getByRole('region',{name:'TMDB追番候选',exact:true}).count()===0,'member retained previous selection');await context.addCookies([{name:'pulsemedia_session',value:setup.admin,url:base,httpOnly:true,sameSite:'Lax'}]);await open();check(await panel().getByRole('region',{name:'TMDB追番候选',exact:true}).count()===0,'ABA retained candidate');check(writes===1,'account switch emitted mutation');check(errors.length===0,'page errors '+errors.join(','));
 console.log(`M10_BANGUMI_CHROME_PASS checks=${checks} screenshots=6 viewports=1440_768_375_320 Cookie=REAL subscribe=ONE TMDB=EXPLICIT namespace=SEPARATE unknown=PRESERVED`);
}finally{for(const ctx of contexts.reverse())await ctx.close();await browser.close()}
