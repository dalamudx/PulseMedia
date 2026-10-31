// Visual and interaction regression only: API fixtures do not exercise backend authorization.
import assert from 'node:assert/strict';
import {join} from 'node:path';
import {chromium} from 'playwright-core';

const base=process.env.PULSE_DESIGN_BASE;
const shots=process.env.PULSE_DESIGN_SHOTS;
assert(base&&['127.0.0.1','localhost'].includes(new URL(base).hostname),'local frontend required');
assert(shots?.startsWith('/data/temp/'),'task-owned screenshot directory required');
const browser=await chromium.connectOverCDP(process.env.PULSE_DESIGN_CDP??'http://127.0.0.1:9222');
const context=await browser.newContext({viewport:{width:1440,height:1000},reducedMotion:'reduce',serviceWorkers:'block'});
const now=new Date().toISOString();
const movies=['星际穿越','沙丘：第二部','奥本海默','寻梦环游记','流浪地球2','宇宙探索编辑部'].map((title,i)=>({tmdb_id:100+i,title,year:2020+i,media_type:'movie',rating:8.2+i/10,genres:['剧情'],overview:'用于界面回归的演示影视条目。',poster_url:`https://image.tmdb.org/t/p/w342/Design_${i}.svg`}));
let failFeed=false;
const writes=[];
let checks=0;
const check=(value,label)=>{assert(value,label);checks++;};
const navLabels=['仪表盘','探索发现','影视订阅','家庭请求','媒体库','下载活动','历史整理','站点管理','服务设置','API 令牌'];
try {
  await context.route('https://image.tmdb.org/**',route=>{
    const i=Number(route.request().url().match(/Design_(\d+)/)?.[1]??0);
    const color=['#344b50','#8d6c42','#353432','#4d6f64','#516276','#665044'][i%6];
    return route.fulfill({contentType:'image/svg+xml',body:`<svg xmlns="http://www.w3.org/2000/svg" width="342" height="513"><rect width="342" height="513" fill="${color}"/><text x="24" y="410" fill="white" font-size="25" font-family="sans-serif">${movies[i%6].title}</text><text x="24" y="450" fill="white" font-size="13">界面验证 · 演示海报</text></svg>`});
  });
  await context.route('**/api/v1/**',route=>{
    const url=new URL(route.request().url()),path=url.pathname;
    if(!['GET','HEAD','OPTIONS'].includes(route.request().method()))writes.push(path);
    let data=null;
    if(path==='/api/v1/auth/status')data={initialized:true,current_user:{id:'11111111-1111-4111-8111-111111111111',username:'design-fixture',role:'admin'}};
    else if(path==='/api/v1/library/overview')data={observed_at:now,counts:{movies:'128',tv_shows:'36',episodes:'742',files:'863'},roots:[{root_id:'root-fixture',name:'电影收藏',media_type:'movie',health:'online',availability:'available',capacity_state:'fresh',total_bytes:'8796093022208',available_bytes:'3298534883328',sampled_at:now}],recent:[]};
    else if(path==='/api/v1/dashboard/activity')data={observed_at:now,more:false,items:[{operation_id:'op-fixture',title:'星际穿越',status:'downloading',progress:.64,progress_source:'native_percent',updated_at:now}]};
    else if(path==='/api/v1/download-clients/global-rates')data={enabled:false,observed_at:now,freshness_seconds:60,clients:[],series:[]};
    else if(path==='/api/v1/media-requests/summary')data={pending_count:'2',observed_at:now};
    else if(path==='/api/v1/discover'&&!failFeed)data={results:movies,page:1,total_pages:1};
    else if(['/api/v1/subscriptions','/api/v1/indexers','/api/v1/media-requests','/api/v1/library/catalog'].includes(path))data={items:[],more:false,observed_at:now};
    return route.fulfill({status:data?200:503,json:data??{type:'about:blank',status:503,title:'界面夹具：来源暂不可用',code:'UNAVAILABLE',message:'界面夹具：来源暂不可用'}});
  });
  const page=await context.newPage();page.setDefaultTimeout(10000);
  const errors=[];page.on('pageerror',e=>errors.push(e.message));
  await page.goto(base,{waitUntil:'networkidle'});
  const navigation=page.getByRole('navigation',{name:'主导航'});
  await navigation.waitFor();
  check(await navigation.getByRole('link').count()===10,'all ten navigation destinations');
  await page.getByRole('button',{name:'切换浅色主题',exact:true}).click();
  check(await page.evaluate(()=>document.documentElement.dataset.theme)==='light','light theme applied');
  await page.reload({waitUntil:'networkidle'});
  check(await page.evaluate(()=>document.documentElement.dataset.theme)==='light','theme persists after reload');
  await page.screenshot({path:join(shots,'dashboard-light.png'),fullPage:true});
  await page.getByRole('button',{name:'收起侧边栏',exact:true}).click();
  check(await page.locator('.sidebar').evaluate(el=>el.getBoundingClientRect().width)===68,'collapsed desktop width');
  for(const label of navLabels)check(await navigation.getByRole('link',{name:label,exact:true}).count()===1,'collapsed link '+label);
  await page.getByRole('button',{name:'展开侧边栏',exact:true}).click();
  const routes=['/discover','/subscriptions','/activity','/media','/sites','/settings','/requests','/reorganize','/account/tokens'];
  for(const path of routes){
    await page.goto(base+path,{waitUntil:'networkidle'});
    check(await page.locator('#main-content').innerText()!=='','content exists for '+path);
    check(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),'desktop overflow '+path);
  }
  await page.goto(base+'/discover',{waitUntil:'networkidle'});
  check(await page.locator('.media-card-wrap').count()===6,'fixture catalogue rendered');
  await page.getByRole('button',{name:'查看《星际穿越》详情',exact:true}).focus();
  await page.keyboard.press('Enter');
  await page.getByRole('dialog').waitFor();
  check(await page.getByRole('dialog').count()===1,'keyboard opens media details');
  await page.keyboard.press('Escape');
  await page.locator('.media-card-wrap').first().getByRole('button',{name:'搜种',exact:true}).waitFor();
  await page.screenshot({path:join(shots,'discover-light.png'),fullPage:true});
  for(const width of [1024,768,390,320]){
    await page.setViewportSize({width,height:900});
    check(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),'discover no overflow at '+width);
    const menu=page.getByRole('button',{name:'打开导航菜单',exact:true});
    await menu.click();
    const drawer=page.getByRole('dialog',{name:'导航菜单'});await drawer.waitFor();
    check(await page.locator('.main-wrapper').evaluate(el=>el.inert),'drawer isolates content '+width);
    check(await drawer.getByRole('link').count()===10,'drawer full navigation '+width);
    await page.keyboard.press('Shift+Tab');
    check(await drawer.getByRole('link',{name:'API 令牌',exact:true}).evaluate(el=>el===document.activeElement),'drawer focus wraps '+width);
    await page.keyboard.press('Escape');
    check(await menu.evaluate(el=>el===document.activeElement),'drawer restores focus '+width);
    check(await page.locator('.sidebar').evaluate(el=>el.inert),'closed drawer inert '+width);
    const action=page.getByRole('button',{name:'订阅《星际穿越》',exact:true});
    check(await action.isVisible(),'card actions visible '+width);
    check(await action.evaluate(el=>el.getBoundingClientRect().height)>=36,'card action target '+width);
    if(width===390)await page.screenshot({path:join(shots,'discover-mobile.png'),fullPage:true});
  }
  for(const path of ['/','/subscriptions','/activity','/media','/sites','/settings']){
    await page.goto(base+path,{waitUntil:'networkidle'});
    check(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),'mobile no overflow '+path);
  }
  await page.setViewportSize({width:1440,height:1000});
  await page.getByRole('button',{name:'切换深色主题',exact:true}).click();
  check(await page.evaluate(()=>getComputedStyle(document.documentElement).colorScheme)==='dark','dark controls match theme');
  await page.goto(base+'/discover',{waitUntil:'networkidle'});
  await page.screenshot({path:join(shots,'discover-dark.png'),fullPage:true});
  failFeed=true;await page.reload({waitUntil:'networkidle'});
  check(await page.getByRole('alert').filter({hasText:'推荐读取失败，请检查元数据配置或重新加载。'}).isVisible(),'failed feed shows error');
  check(await page.locator('.media-card-wrap').count()===0,'failed initial feed does not show fake media');
  check(writes.length===0,'navigation and theme must not issue business commands: '+JSON.stringify(writes));
  check(errors.length===0,'runtime errors: '+errors.join('; '));
  console.log('WORKSPACE_DESIGN_PASS '+JSON.stringify({checks,widths:[1440,1024,768,390,320],routes:10,api:'fixture',keyboard:true,themes:true,businessWrites:writes.length}));
} finally {await context.close();await browser.close();console.log('WORKSPACE_DESIGN_CONTEXT_CLEANED');}
