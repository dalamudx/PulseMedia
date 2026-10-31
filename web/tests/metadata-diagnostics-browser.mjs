import assert from 'node:assert/strict';
import {mkdtemp,writeFile,rm} from 'node:fs/promises';
import {fileURLToPath} from 'node:url';
import path from 'node:path';
import {createServer} from 'vite';
import react from '@vitejs/plugin-react';
import {chromium} from 'playwright-core';
const root=process.env.PULSE_DIAGNOSTIC_EVIDENCE;
assert(root?.startsWith('/data/temp/')&&path.resolve(root)===root,'owned test root required');
const owned=await mkdtemp(path.join(root,'diagnostic-chrome-'));
const web=fileURLToPath(new URL('../',import.meta.url));let server,browser,release;
try{
 const auth=path.join(owned,'auth.ts');
 await writeFile(auth,`import {useSyncExternalStore} from 'react';let state={user:{id:'account-a',role:'admin'},checking:false};const listeners=new Set();globalThis.switchDiagnosticAccount=next=>{state=next;for(const fn of listeners)fn()};export const useAuth=()=>useSyncExternalStore(fn=>{listeners.add(fn);return()=>listeners.delete(fn)},()=>state);`);
 await writeFile(path.join(owned,'index.html'),'<!doctype html><html lang="zh-CN"><meta name="viewport" content="width=device-width,initial-scale=1"><div id="root"></div><script type="module" src="/main.tsx"></script></html>');
 await writeFile(path.join(owned,'main.tsx'),`import React from 'react';import {createRoot} from 'react-dom/client';import {MetadataDiagnosticsPanel} from '/@fs${web}src/features/configuration/MetadataDiagnosticsPanel.tsx';import '/@fs${web}src/assets/main.css';import '/@fs${web}src/features/configuration/configuration.css';createRoot(document.getElementById('root')).render(<main style={{padding:16,minWidth:0}}><MetadataDiagnosticsPanel/></main>);`);
 server=await createServer({configFile:false,root:owned,cacheDir:path.join(owned,'cache'),plugins:[react()],resolve:{alias:[{find:'@/features/auth/AuthContext',replacement:auth},{find:'@',replacement:path.join(web,'src')},{find:'react',replacement:path.join(web,'node_modules/react')},{find:'react-dom',replacement:path.join(web,'node_modules/react-dom')}]},server:{host:'127.0.0.1',port:0,fs:{allow:[owned,web]}},logLevel:'error'});
 await server.listen();const base=`http://127.0.0.1:${server.httpServer.address().port}`;
 browser=await chromium.launch({executablePath:process.env.PULSE_TEST_BROWSER_EXECUTABLE||'/usr/bin/google-chrome',headless:true,args:['--no-sandbox','--disable-dev-shm-usage','--disable-background-networking']});
 const context=await browser.newContext({viewport:{width:320,height:900}});const page=await context.newPage();
 const calls=[],errors=[];let scenario='api',started;
 page.on('pageerror',e=>errors.push(e.message));
 await page.route('**/api/v1/**',async route=>{
  const request=route.request(),url=new URL(request.url());calls.push({method:request.method(),path:url.pathname,body:request.postDataJSON(),query:url.search});
  const mode=scenario;if(mode==='pending')await new Promise(resolve=>{release=resolve;started?.()});
  if(mode==='raw-error')return route.fulfill({status:503,contentType:'application/problem+json',body:JSON.stringify({message:'https://private.invalid?api_key=RAW_SECRET'})});
  const target=request.postDataJSON().target;
  const result={target,category:mode==='api'?'host_response':'available',elapsed_ms:17,host_responded:true,image_retrieved:target==='tmdb_image'};
  if(mode==='invalid')result.category='RAW_SECRET';
  try{await route.fulfill({status:200,contentType:'application/json',body:JSON.stringify(result)});}catch{ /* An account change aborts this owned request. */ }
 });
 await page.goto(base);const api=page.getByRole('button',{name:'测试 TMDB API',exact:true});const image=page.getByRole('button',{name:'测试 TMDB 图片',exact:true});await api.waitFor();
 assert.equal(calls.length,0,'mount must not issue requests');
 await api.click();await page.getByRole('status').getByText(/主机有响应，未验证服务可用/).waitFor();assert.equal(calls.length,1);
 scenario='image';await image.click();await page.getByRole('status').getByText(/图片已成功取回并校验/).waitFor();
 for(const [target,label] of [['fanart_api','Fanart API'],['tvdb_api','TVDB API']]){scenario='available';await page.getByRole('button',{name:`测试 ${label}`,exact:true}).click();await page.getByRole('status').getByText(`${label}：测试通过`,{exact:false}).waitFor();assert.equal(calls.at(-1).body.target,target);assert(!await page.getByRole('status').getByText(/图片已成功/).count());}
 for(const button of await page.getByRole('button').all()){const box=await button.boundingBox();assert(box&&box.height>=44&&box.width>=44);}
 assert(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),'320px overflow');
 for(const width of [320,390,1440]){await page.setViewportSize({width,height:900});assert(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),`overflow ${width}`);await page.screenshot({path:path.join(root,`metadata-diagnostics-${width}.png`),fullPage:true});}
 scenario='raw-error';await api.click();await page.getByRole('status').getByText('测试结果无法确认，请手动重试。').waitFor();assert(!(await page.locator('body').innerText()).includes('RAW_SECRET'));
 scenario='invalid';await image.click();await page.getByRole('status').getByText('测试结果无法确认，请手动重试。').waitFor();assert(!(await page.locator('body').innerText()).includes('RAW_SECRET'));
 for(const next of [{user:{id:'account-b',role:'admin'},checking:false},{user:{id:'account-b',role:'member'},checking:false},{user:null,checking:true}]){
  await page.evaluate(()=>globalThis.switchDiagnosticAccount({user:{id:'account-a',role:'admin'},checking:false}));await api.waitFor();
  scenario='pending';const reached=new Promise(resolve=>{started=resolve});await api.click();await reached;
  assert(await page.getByRole('button',{name:'测试 TMDB 图片',exact:true}).isDisabled());
  const before=calls.length;await page.evaluate(value=>globalThis.switchDiagnosticAccount(value),next);release?.();
  if(next.user?.role==='admin')await api.waitFor();else await api.waitFor({state:'detached'});
  assert.equal(await page.getByRole('status').getByText(/测试通过/).count(),0,'late account result leaked');assert.equal(calls.length,before,'account transition issued requests');
 }
 assert(calls.every(c=>c.method==='POST'&&c.path==='/api/v1/settings/metadata-diagnostics'&&c.query===''&&Object.keys(c.body).join()==='target'&&['tmdb_api','tmdb_image','fanart_api','tvdb_api'].includes(c.body.target)));
 assert.deepEqual(errors,[]);
 console.log(JSON.stringify({passed:true,realChrome:true,explicitOnly:true,fixedTargets:true,hostVsImage:true,safeErrors:true,lateAccountRoleCheckingFenced:true,widths:[320,390,1440],requests:calls.length}));
 await context.close();
}finally{release?.();if(browser)await browser.close();if(server)await server.close();await rm(owned,{recursive:true,force:true});}
