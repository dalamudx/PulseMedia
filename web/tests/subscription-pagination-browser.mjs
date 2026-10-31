import assert from 'node:assert/strict';
import {mkdtemp,readFile,rm} from 'node:fs/promises';
import {join,extname} from 'node:path';
import {fileURLToPath} from 'node:url';
import {createServer} from 'node:http';
import {build} from 'vite';
import react from '@vitejs/plugin-react';
import {chromium} from 'playwright-core';

assert.ok(process.env.TMPDIR?.startsWith('/data/temp/'));
const work=await mkdtemp(join(process.env.TMPDIR,'subscription-pages-'));
const root=fileURLToPath(new URL('..',import.meta.url)),assets=join(work,'web');
const id=n=>`00000000-0000-0000-0000-${String(n).padStart(12,'0')}`;
const admin={id:id(1),username:'page-admin',role:'admin'},member={id:id(2),username:'page-member',role:'member'};
let user=admin,browser,server,failList=false,releaseSlow,markSlow;
const slowStarted=new Promise(resolve=>{markSlow=resolve});
const reads=[],writes=[],errors=[];
const item=(n,owner=admin)=>({id:id(100+n),user_id:owner.id,media_id:id(1000+n),root_id:id(5000),quality_policy_id:'default',quality_policy_revision:1,intent_version:1,state:'active',scope_type:'all',title:`${owner.role==='admin'?'Series':'Member'} ${n}`,media_type:'tv',tmdb_id:n,root_name:'TV',total_episodes:100,satisfied_episodes:25});
const rows=Array.from({length:51},(_,i)=>item(i+1));
try{
 await build({configFile:false,envFile:false,root,plugins:[react()],logLevel:'error',cacheDir:join(work,'cache'),resolve:{alias:{'@':join(root,'src')}},build:{outDir:assets,emptyOutDir:true}});
 server=createServer(async(req,res)=>{try{
  const u=new URL(req.url,'http://localhost');const json=(body,status=200)=>{res.writeHead(status,{'content-type':'application/json','cache-control':'private, no-store'});res.end(JSON.stringify(body))};
  if(u.pathname.startsWith('/api/')){
   if(req.method!=='GET')writes.push(u.pathname);else reads.push(u.pathname+u.search);
   if(u.pathname==='/api/v1/auth/status'){json({initialized:true,current_user:user});return}
   if(u.pathname==='/api/v1/auth/logout'){user=null;json({});return}
   if(u.pathname==='/api/v1/auth/login'){for await(const _ of req){};user=member;json({user});return}
   if(u.pathname==='/api/v1/subscriptions'){
    if(failList){json({code:'UNAVAILABLE'},503);return}
    const offset=Number(u.searchParams.get('offset')||0),limit=Number(u.searchParams.get('limit')||50);
    assert.ok(limit<=100);
    if(u.searchParams.get('view')==='calendar'){
     const start=u.searchParams.get('start'),end=u.searchParams.get('end');assert.ok(start&&end&&(Date.parse(end)-Date.parse(start))/86400000<=41);
     const unknown=u.searchParams.get('unknown_dates')==='true';
     const episode={id:id(7000+offset+(unknown?500:0)),unit_id:id(8000+offset+(unknown?500:0)),unit_key:`S01E${offset+1}`,is_skipped:false,is_satisfied:false,...(!unknown?{air_date:start}:{})};
     json({items:unknown&&offset>0?[]:[{...item(1,user||member),wanted_items:[episode]}],total:unknown&&offset>0?0:101,movie_count:0,tv_count:0});return;
    }
    const search=u.searchParams.get('search')||'';
    const source=user?.role==='admin'?rows:[item(1,member)];
    if(search==='slow'){await new Promise(resolve=>{releaseSlow=resolve;markSlow()});json({items:[{...item(99),title:'STALE RESULT'}],total:1,movie_count:0,tv_count:1});return}
    const matched=source.filter(row=>row.title.includes(search));
    json({items:matched.slice(offset,offset+limit),total:matched.length,movie_count:0,tv_count:matched.length});return;
   }
   const detail=u.pathname.match(/^\/api\/v1\/subscriptions\/([^/]+)$/);
   if(detail){const row=rows.find(item=>item.id===detail[1]);json({...row,wanted_items:[]});return}
   json({code:'UNAVAILABLE'},503);return;
  }
  const file=u.pathname.startsWith('/assets/')||u.pathname==='/theme.js'?join(assets,u.pathname):join(assets,'index.html');
  res.writeHead(200,{'content-type':{'.js':'text/javascript','.css':'text/css','.html':'text/html'}[extname(file)]||'application/octet-stream'});res.end(await readFile(file));
 }catch(err){res.writeHead(500);res.end();errors.push(err.message)}});
 await new Promise(resolve=>server.listen(0,'127.0.0.1',resolve));const base=`http://127.0.0.1:${server.address().port}`;
 browser=await chromium.launch({executablePath:process.env.PULSE_TEST_BROWSER_EXECUTABLE||'/usr/bin/google-chrome',headless:true,args:['--no-sandbox','--disable-dev-shm-usage','--disable-background-networking']});
 const context=await browser.newContext({serviceWorkers:'block',viewport:{width:1280,height:900}});
 await context.route('**/*',route=>route.request().url().startsWith(base)?route.continue():route.abort());
 const page=await context.newPage();page.setDefaultTimeout(15000);page.on('pageerror',e=>errors.push(e.message));
 await page.goto(base+'/subscriptions');await page.getByRole('button',{name:'全部订阅 (51)',exact:true}).waitFor();
 assert.equal(await page.locator('.sub-poster-card').count(),50);
 assert.equal(reads.filter(path=>/^\/api\/v1\/subscriptions\/[^/?]+$/.test(path)).length,0,'summary eagerly read details');
 await page.getByRole('button',{name:'下一页',exact:true}).click();await page.getByRole('button',{name:'查看Series 51订阅详情',exact:true}).waitFor();assert.equal(await page.locator('.sub-poster-card').count(),1);
 const detailResponse=page.waitForResponse(r=>new URL(r.url()).pathname===`/api/v1/subscriptions/${id(151)}`);await page.getByRole('button',{name:'查看Series 51订阅详情',exact:true}).click();await detailResponse;
 await page.goto(base+'/subscriptions');await page.getByRole('button',{name:'全部订阅 (51)',exact:true}).waitFor();
 const search=page.getByPlaceholder('搜索已订阅影片...');
 await search.fill('slow');await slowStarted;
 await search.fill('Series 51');await page.getByRole('button',{name:'全部订阅 (1)',exact:true}).waitFor();releaseSlow?.();await page.waitForLoadState('networkidle');assert.equal(await page.getByText('STALE RESULT',{exact:true}).count(),0);
 failList=true;await search.fill('missing');await page.getByRole('alert').filter({hasText:'订阅列表读取失败'}).waitFor();assert.equal(await page.locator('.sub-poster-card').count(),0);await page.getByRole('button',{name:'全部订阅 (—)',exact:true}).waitFor();
 failList=false;await page.getByRole('button',{name:'重新读取订阅列表',exact:true}).click();await page.getByRole('button',{name:'全部订阅 (0)',exact:true}).waitFor();
 await page.getByRole('button',{name:'追剧日历',exact:true}).click();await page.getByRole('heading',{name:'播出日期未知（共 101 集）',exact:true}).waitFor();
 await page.getByRole('button',{name:'下一页排期',exact:true}).click();await page.getByRole('navigation',{name:'排期分页',exact:true}).getByText('第 2 页',{exact:false}).waitFor();
 await page.getByRole('button',{name:'下一页未知日期',exact:true}).click();await page.getByRole('button',{name:'下一页未知日期',exact:true}).waitFor();await page.waitForLoadState('networkidle');assert.ok(reads.some(path=>path.includes('offset=100')&&path.includes('unknown_dates=true')));
 await page.getByRole('button',{name:'上一页未知日期',exact:true}).click();await page.getByRole('heading',{name:'播出日期未知（共 101 集）',exact:true}).waitFor();
 for(const width of [1280,768,375,320]){await page.setViewportSize({width,height:900});assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth+1),`calendar overflow ${width}`)}
 await page.setViewportSize({width:1280,height:900});
 await page.getByRole('button',{name:'退出登录',exact:true}).click();await page.getByRole('heading',{name:'登录 PulseMedia',exact:true}).waitFor();
 await page.getByPlaceholder('输入管理员用户名...').fill('page-member');await page.getByPlaceholder('输入密码...').fill('owned-fixture');await page.locator('form button[type=submit]').click();await page.locator('.app-shell').waitFor();
 await page.goto(base+'/subscriptions');await page.getByRole('button',{name:'查看Member 1订阅详情',exact:true}).waitFor();assert.equal(await page.getByRole('button',{name:'查看Series 51订阅详情',exact:true}).count(),0);
 assert.deepEqual(writes,['/api/v1/auth/logout','/api/v1/auth/login']);assert.deepEqual(errors,[]);
 console.log('SUBSCRIPTION_PAGES_CHROME_PASS pagination=1 detail_on_demand=1 stale_search=1 error_recovery=1 calendar_pages=2 identity=1 widths=1280,768,375,320 business_writes=0');
}finally{releaseSlow?.();await browser?.close();if(server){server.closeAllConnections();await new Promise(resolve=>server.close(resolve))}await rm(work,{recursive:true,force:true})}
