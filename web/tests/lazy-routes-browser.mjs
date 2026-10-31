import assert from 'node:assert/strict';
import {mkdtemp,readFile,rm} from 'node:fs/promises';
import {join,extname} from 'node:path';
import {fileURLToPath} from 'node:url';
import {createServer} from 'node:http';
import {build} from 'vite';
import react from '@vitejs/plugin-react';
import {chromium} from 'playwright-core';

assert.ok(process.env.TMPDIR?.startsWith('/data/temp/'),'owned TMPDIR required');
const work=await mkdtemp(join(process.env.TMPDIR,'lazy-routes-'));
const root=fileURLToPath(new URL('..',import.meta.url)),assets=join(work,'web');
const admin={id:'00000000-0000-0000-0000-000000000001',username:'fixture-admin',role:'admin'};
const member={id:'00000000-0000-0000-0000-000000000002',username:'fixture-member',role:'member'};
let user=admin,browser,server,releaseAudit;
const writes=[],auditReads=[];
try {
 await build({configFile:false,envFile:false,root,plugins:[react()],logLevel:'error',cacheDir:join(work,'cache'),resolve:{alias:{'@':join(root,'src')}},build:{outDir:assets,emptyOutDir:true,manifest:true}});
 const manifest=JSON.parse(await readFile(join(assets,'.vite/manifest.json'),'utf8'));
 const entry=Object.values(manifest).find(item=>item.isEntry);
 assert.ok(entry&&entry.dynamicImports.length>=12,'routes must be separate dynamic imports');
 assert.ok((await readFile(join(assets,entry.file))).length<400000,'entry JS budget exceeded');
 server=createServer(async(req,res)=>{
  try {
   const url=new URL(req.url,'http://localhost');
   const json=(value,status=200)=>{res.writeHead(status,{'content-type':'application/json'});res.end(JSON.stringify(value));};
   if(url.pathname.startsWith('/api/')){
    if(req.method!=='GET')writes.push(url.pathname);
    if(url.pathname==='/api/v1/auth/status'){json({initialized:true,current_user:user});return;}
    if(url.pathname==='/api/v1/auth/logout'){user=null;json({});return;}
    if(url.pathname==='/api/v1/auth/login'){for await(const _ of req){};user=member;json({user});return;}
    if(url.pathname==='/api/v1/system/audit')auditReads.push(user?.role);
    if(url.pathname==='/api/v1/settings'){json({});return;}
    if(url.pathname==='/api/v1/system/status'){json({version:'fixture',db_ready:true,data_dir:'fixture',log_level:'info'});return;}
    json({code:'UNAVAILABLE'},503);return;
   }
   const target=(url.pathname.startsWith('/assets/')||url.pathname==='/theme.js'||url.pathname==='/pulse-push-sw.js')?join(assets,url.pathname):join(assets,'index.html');
   const content=await readFile(target),type={'.js':'text/javascript','.css':'text/css','.html':'text/html'}[extname(target)]||'application/octet-stream';
   res.writeHead(200,{'content-type':type});res.end(content);
  }catch {res.writeHead(404);res.end();}
 });
 await new Promise(resolve=>server.listen(0,'127.0.0.1',resolve));
 const base=`http://127.0.0.1:${server.address().port}`;
 browser=await chromium.launch({executablePath:process.env.PULSE_TEST_BROWSER_EXECUTABLE||'/usr/bin/google-chrome',headless:true,args:['--no-sandbox','--disable-dev-shm-usage','--disable-background-networking']});
 const context=await browser.newContext({serviceWorkers:'block',viewport:{width:1280,height:900}});
 await context.route('**/*',route=>route.request().url().startsWith(base)?route.continue():route.abort());
 const page=await context.newPage(),requests=[],errors=[];
 page.setDefaultTimeout(15000);page.on('request',r=>requests.push(r.url()));page.on('pageerror',e=>errors.push(e.message));
 await page.goto(base+'/account/tokens');
 await page.getByRole('heading',{name:'API 令牌',exact:true}).waitFor();
 assert.ok(!requests.some(url=>/SettingsView-|ReorganizeView-|DiscoverView-/.test(url)),'unvisited routes were fetched');
 await page.getByRole('link',{name:'服务设置',exact:true}).click();
 await page.getByRole('button',{name:'系统操作审计',exact:true}).waitFor();
 assert.ok(requests.some(url=>url.includes('SettingsView-')));
 assert.ok(!requests.some(url=>url.includes('AuditTimelinePanel-')),'inactive settings tab was fetched');
 const gate=new Promise(resolve=>{releaseAudit=resolve;});
 const auditPattern=/AuditTimelinePanel-[^/]+[.]js/;
 await context.route(auditPattern,async route=>{await gate;await route.continue().catch(()=>{});});
 const pending=page.waitForRequest(request=>auditPattern.test(request.url()));
 await page.getByRole('button',{name:'系统操作审计',exact:true}).click();await pending;
 await page.getByRole('status').filter({hasText:'正在加载'}).waitFor();
 await page.getByRole('button',{name:'退出登录',exact:true}).click();
 await page.getByRole('heading',{name:'登录 PulseMedia',exact:true}).waitFor();
 releaseAudit();await page.waitForLoadState('networkidle');
 assert.equal(await page.locator('.app-shell').count(),0,'late admin module restored private shell');
 assert.equal(auditReads.length,0,'late admin module performed a read');
 await page.getByPlaceholder('输入管理员用户名...').fill('fixture-member');
 await page.getByPlaceholder('输入密码...').fill('owned-fixture');
 await page.locator('form button[type=submit]').click();await page.locator('.app-shell').waitFor();
 await page.getByRole('link',{name:'服务设置',exact:true}).click();
 await page.getByRole('button',{name:'TMDB 元数据',exact:true}).waitFor();
 assert.equal(await page.getByRole('button',{name:'系统操作审计',exact:true}).count(),0,'member inherited admin tab');
 assert.equal(auditReads.length,0);
 assert.deepEqual(errors,[]);
 await context.close();user=admin;
 const failed=await browser.newContext({serviceWorkers:'block',viewport:{width:320,height:900}});
 let abortChunk=true;
 await failed.route('**/*',async route=>{
  if(!route.request().url().startsWith(base)){await route.abort();return;}
  if(abortChunk&&/SettingsView-[^/]+[.]js/.test(route.request().url())){await route.abort();return;}
  await route.continue();
 });
 const failedPage=await failed.newPage();failedPage.setDefaultTimeout(15000);
 await failedPage.goto(base+'/settings');
 await failedPage.getByRole('alert').filter({hasText:'页面加载失败'}).waitFor();
 assert.ok(await failedPage.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),'loading error overflow');
 abortChunk=false;
 await failedPage.getByRole('button',{name:'重新加载页面',exact:true}).click();
 await failedPage.getByRole('button',{name:'系统操作审计',exact:true}).waitFor();
 assert.deepEqual(writes,['/api/v1/auth/logout','/api/v1/auth/login'],'module loading sent a business command');
 console.log('LAZY_ROUTES_CHROME_PASS routes=12 entry_budget=400000 loading_failure=1 original_reload=1 identity_isolation=1 business_writes=0');
}finally{
 releaseAudit?.();
 await browser?.close();
 if(server){server.closeAllConnections();await new Promise(resolve=>server.close(resolve));}
 await rm(work,{recursive:true,force:true});
}
