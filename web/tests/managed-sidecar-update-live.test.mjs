import { test } from 'node:test';
import assert from 'node:assert/strict';
import { createRequire } from 'node:module';
import { mkdtemp, writeFile, readFile, rm } from 'node:fs/promises';
import { fileURLToPath } from 'node:url';
import { dirname, join, extname } from 'node:path';
import { createServer, request as httpRequest } from 'node:http';

// Go owns the actual PG/App/provider/files fixture and passes its Cookie only to
// this child process. The page uses the production component, auth and API client.
if (process.env.PULSE_MP29_LIVE_URL) test('production panel with real App Cookie and PostgreSQL', async t => {
 const web=dirname(dirname(fileURLToPath(import.meta.url)));
 const load=createRequire(join(web,'package.json'));
 const {build}=load('vite');const {chromium}=load('playwright-core');
 const upstream=new URL(process.env.PULSE_MP29_LIVE_URL);
 assert(['127.0.0.1','localhost'].includes(upstream.hostname));
 const fileId=process.env.PULSE_MP29_LIVE_FILE_ID,unitId=process.env.PULSE_MP29_LIVE_UNIT_ID,session=process.env.PULSE_MP29_LIVE_SESSION;
 const scenario=process.env.PULSE_MP29_LIVE_SCENARIO;
 assert(fileId && unitId && session && ['completed','held','unknown'].includes(scenario));
 assert(process.env.TMPDIR?.startsWith('/data/temp/'));
 const temp=await mkdtemp(join(process.env.TMPDIR,'mp29-live.'));
 let server,browser;
 try {
  await writeFile(join(temp,'index.html'),'<html lang="zh-CN"><head><meta name="viewport" content="width=device-width,initial-scale=1"></head><body><div id="root"></div><script type="module" src="/entry.tsx"></script></body></html>');
  await writeFile(join(temp,'entry.tsx'),`import React from ${JSON.stringify(join(web,'node_modules/react/index.js'))};import {createRoot} from ${JSON.stringify(join(web,'node_modules/react-dom/client.js'))};import {AuthProvider} from ${JSON.stringify(join(web,'src/features/auth/AuthContext.tsx'))};import {ManagedSidecarUpdatePanel} from ${JSON.stringify(join(web,'src/features/media-scrape/ManagedSidecarUpdatePanel.tsx'))};import ${JSON.stringify(join(web,'src/assets/main.css'))};import ${JSON.stringify(join(web,'src/assets/workspace.css'))};import ${JSON.stringify(join(web,'src/assets/redesign.css'))};createRoot(document.getElementById('root')).render(<AuthProvider><ManagedSidecarUpdatePanel fileId=${JSON.stringify(fileId)} unitId=${JSON.stringify(unitId)}/></AuthProvider>);`);
  await build({configFile:false,root:temp,cacheDir:join(temp,'cache'),logLevel:'error',resolve:{alias:{'@':join(web,'src'),react:join(web,'node_modules/react'),'react-dom':join(web,'node_modules/react-dom')},dedupe:['react','react-dom']},esbuild:{jsx:'automatic'},build:{outDir:join(temp,'dist'),emptyOutDir:true,minify:true}});
  server=createServer(async(req,res)=>{
   if(req.url.startsWith('/api/')){
    const call=httpRequest(new URL(req.url,upstream),{method:req.method,headers:req.headers},reply=>{res.writeHead(reply.statusCode,reply.headers);reply.pipe(res)});call.on('error',()=>res.writeHead(502).end());req.pipe(call);return;
   }
   try{const path=req.url==='/'?'/index.html':req.url;if(!/^\/(?:index\.html|assets\/[a-zA-Z0-9_.-]+)$/.test(path)){res.writeHead(404).end();return}const b=await readFile(join(temp,'dist',path));res.setHeader('Content-Type',extname(path)==='.js'?'application/javascript':extname(path)==='.css'?'text/css':'text/html');res.end(b)}catch{res.writeHead(404).end()}
  });await new Promise(resolve=>server.listen(0,'127.0.0.1',resolve));const base=`http://127.0.0.1:${server.address().port}`;
  browser=await chromium.launch({executablePath:process.env.CHROME_PATH||'/usr/bin/google-chrome',headless:true,args:['--no-sandbox','--disable-dev-shm-usage','--disk-cache-dir='+join(temp,'chrome-cache')]});
  const context=await browser.newContext({viewport:{width:1280,height:1000},serviceWorkers:'block'});
  await context.addCookies([{name:'pulsemedia_session',value:session,url:base,httpOnly:true,sameSite:'Strict'}]);
  const page=await context.newPage();page.setDefaultTimeout(15000);const errors=[],sent=[],statuses=[];
  page.on('pageerror',e=>errors.push(e.message));
  const commandPath=`/api/v1/library/files/${fileId}/sidecar-update-commands`,previewPath=`/api/v1/library/files/${fileId}/sidecar-update-previews`;
  page.on('request',r=>{const path=new URL(r.url()).pathname;if([previewPath,commandPath].includes(path))sent.push({path,method:r.method(),key:r.headers()['idempotency-key'],body:r.postDataJSON()})});
  page.on('response',r=>{const path=new URL(r.url()).pathname;if(path.startsWith(commandPath)||path===previewPath)statuses.push(r.status())});
  if(scenario==='unknown')await context.route('**'+commandPath,async route=>{const real=await route.fetch();assert.equal(real.status(),200);const body=await real.json();assert.equal(body.state,'completed');await route.abort('failed')});
  await page.goto(base,{waitUntil:'domcontentloaded'});const panel=page.getByRole('region',{name:'受管 NFO 与海报更新',exact:true});await panel.waitFor();assert.equal(sent.length,0);
  await panel.getByRole('button',{name:'预览 NFO 与海报更新',exact:true}).click();const confirm=panel.getByRole('button',{name:'确认更新 NFO 与海报',exact:true});
  if(scenario==='held'){await panel.getByText('本次更新已保留，请人工核对',{exact:true}).waitFor();assert.equal(await confirm.count(),0)}else{
   await confirm.waitFor();assert(await confirm.isDisabled());await panel.getByRole('checkbox').check();await confirm.click();
   if(scenario==='unknown'){await panel.getByRole('alert').waitFor();await page.reload()}
   await panel.getByText('更新已完成',{exact:true}).waitFor();const posts=sent.filter(r=>r.path===commandPath);assert.equal(posts.length,1);assert(posts[0].key);assert.equal(posts[0].body.confirmed,true);
   await Promise.all([page.waitForResponse(r=>new URL(r.url()).pathname===commandPath+'/'+posts[0].key&&r.status()===200),panel.getByRole('button',{name:'读取原键结果',exact:true}).click()]);assert.equal(sent.filter(r=>r.path===commandPath).length,1);
  }
  assert.deepEqual(sent.find(r=>r.path===previewPath).body,{unit_id:unitId});
  for(const width of [1280,375,320]){await page.setViewportSize({width,height:1000});assert(await panel.evaluate(root=>root.getBoundingClientRect().right<=innerWidth+1&&root.scrollWidth<=root.clientWidth+1));assert(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth+1))}
  assert.deepEqual(errors,[]);assert(statuses.every(v=>v===200));t.diagnostic(`MP29_LIVE_COOKIE_CHROME_PASS scenario=${scenario} preview=1 commands=${sent.filter(r=>r.path===commandPath).length} widths=1280,375,320`);
 }finally{await browser?.close();if(server)await new Promise(resolve=>server.close(resolve));await rm(temp,{recursive:true,force:true})}
});
