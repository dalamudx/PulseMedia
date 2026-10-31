// Controlled API fixture for production Workspace handoff guards; real PG/App is tested separately.
import assert from 'node:assert/strict';
import { mkdtemp, rm } from 'node:fs/promises';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { createServer } from 'vite';
import { chromium } from 'playwright-core';
const tempRoot = process.env.PULSE_TEST_TEMP_ROOT;
assert(tempRoot?.startsWith('/data/temp/'));
const work = await mkdtemp(join(tempRoot, 'workspace-guards-'));
const root = fileURLToPath(new URL('..', import.meta.url));
const id = n => `00000000-0000-0000-0000-${n.toString(16).padStart(12,'0')}`;
const sub = { id:id(2),user_id:id(1),media_id:id(3),root_id:id(4),title:'Discovery handoff',media_type:'tv',root_name:'TV',intent_version:1,quality_policy_id:'default',quality_policy_revision:1,state:'active' };
const file = {id:id(5),root_id:id(4),root_name:'TV',rel_path:'Show/S01E03.mkv'};
const unit = {id:id(6),media_id:id(3),unit_key:'S01E03',retired:false,metadata_version:1};
const candidate = {id:id(7),title:'Show.S01E03.2160p.WEB-DL',indexer_name:'fixture',size_bytes:1234,seeders:5,leechers:0};
const search = {id:id(8),subscription_id:sub.id,media_id:sub.media_id,query:'Show S01E03',created_at:new Date().toISOString(),deadline:new Date(Date.now()+30000).toISOString(),source_count:1,ranking_policy_id:'default',ranking_policy_revision:1};
let discoveredFile = file.id, shared = false;
const requests = [], errors = [];
const streams = new Set();
let holdComparison = false, comparisonStarted, releaseComparison;
const entry = `import React from'react';import{createRoot}from'react-dom/client';import{TVEpisodeUpgradeWorkspace}from'/src/features/configuration/TVEpisodeUpgradeWorkspace.tsx';createRoot(document.getElementById('root')).render(React.createElement(TVEpisodeUpgradeWorkspace,{subscription:${JSON.stringify(sub)}}));`;
const auth = `export function useAuth(){return{user:{id:'${id(1)}',role:'admin'},checking:false,statusError:null}}`;
const client = `async function call(method,path,options={}){const r=await fetch('/mock',{method:'POST',body:JSON.stringify({method,path,params:options.params,body:options.body}),signal:options.signal});return{response:r,data:r.ok?await r.json():undefined,error:r.ok?undefined:{code:'fixture'}}}export const api={GET:(p,o)=>call('GET',p,o),POST:(p,o)=>call('POST',p,o)};`;
let server,browser;
try {
 server=await createServer({configFile:false,root,cacheDir:join(work,'vite'),optimizeDeps:{noDiscovery:true,include:['react','react-dom/client','react/jsx-dev-runtime']},resolve:{alias:[{find:'@/api/client',replacement:'\0guards-client'},{find:'@/features/auth/AuthContext',replacement:'\0guards-auth'},{find:'@',replacement:join(root,'src')}]},server:{host:'127.0.0.1',port:0},plugins:[{name:'workspace-guards',resolveId(id){if(id.startsWith('\0guards-'))return id;},load(id){return id==='\0guards-entry'?entry:id==='\0guards-auth'?auth:id==='\0guards-client'?client:undefined;},configureServer(vite){vite.middlewares.use(async(req,res,next)=>{
  if(req.url==='/fixture'){res.setHeader('Content-Type','text/html');res.end('<!doctype html><html lang="zh"><meta name="viewport" content="width=device-width,initial-scale=1"><link rel="stylesheet" href="/src/assets/main.css"><div id="root"></div><script type="module" src="/@id/__x00__guards-entry"></script></html>');return;}
  if(req.url==='/api/v1/stream'){res.setHeader('Content-Type','text/event-stream');res.setHeader('Cache-Control','no-cache');res.write(': ready\n\n');streams.add(res);res.on('close',()=>streams.delete(res));return;}
  if(req.url!=='/mock')return next();
  try{let raw='';for await(const c of req)raw+=c;const r=JSON.parse(raw);requests.push(r);let data;
   if(r.path==='/api/v1/subscriptions/{id}')data=sub;
   else if(r.path==='/api/v1/library/mapping-options')data={files:[file],media:[],files_has_more:false,files_next_cursor:'',limit:100};
   else if(r.path==='/api/v1/library/files/{id}/mapping')data={file_id:file.id,root_id:file.root_id,rel_path:file.rel_path,media_id:sub.media_id,can_correct:true,hold_reason:'',associations:[{active:true,unit},...(shared?[{active:true,unit:{...unit,id:id(9)}}]:[])]};
   else if(r.path==='/api/v1/subscriptions/{id}/tv-episode-discovery/{unit_id}')data={subscription_id:sub.id,unit_id:unit.id,current_file_id:discoveredFile,search_id:search.id,state:'succeeded',execution_allowed:false};
   else if(r.path==='/api/v1/release-searches/{id}')data={search,state:'succeeded',sources:[],results:[candidate],partial:false,truncated:false,total_candidates:1,statistics_mode:'latest_persisted',sort_order:'quality_desc_seeders_desc_id_asc'};
   else if(r.path==='/api/v1/subscriptions/{id}/tv-episode-upgrade-previews'){if(holdComparison){const gate=new Promise(resolve=>{releaseComparison=resolve});comparisonStarted?.();await gate;}data={...r.body,status:'preview_not_executable',version:'guard:v1',comparable:true,improvement:true,execution_allowed:false};}
   else throw Error('Unexpected request '+JSON.stringify(r));
   res.setHeader('Content-Type','application/json');res.end(JSON.stringify(data));
  }catch(e){errors.push(String(e));res.statusCode=500;res.end('{}');}
 });}}]});
 await server.listen();browser=await chromium.launch({executablePath:process.env.CHROME_BIN||'/usr/bin/google-chrome',headless:true,args:['--no-sandbox','--disable-dev-shm-usage'],downloadsPath:join(work,'downloads')});
 const page=await browser.newPage();page.setDefaultTimeout(10000);page.on('pageerror',e=>errors.push(String(e)));
 const button=name=>page.getByRole('button',{name,exact:true});
 async function open(){await page.goto(`http://127.0.0.1:${server.httpServer.address().port}/fixture`);await button('读取当前文件与订阅').click();await page.getByLabel('当前文件',{exact:true}).selectOption(file.id);await button('核验标准集映射').click();await button('比较本集候选品质').waitFor();}
 // A discovery tied to a different current file must never cross into the selected scope.
 discoveredFile=id(10);await open();await button('比较本集候选品质').click();await button('选择并进入本集升级确认').click();await page.getByRole('alert').waitFor();assert.equal(await page.getByRole('heading',{name:'TV 单集品质升级',exact:true}).count(),0);
 assert.equal(requests.filter(r=>r.path.includes('commands')).length,0);
 // Fresh mapping is authoritative even after a successful explicit comparison.
 discoveredFile=file.id;await open();await button('比较本集候选品质').click();await button('选择并进入本集升级确认').waitFor();shared=true;await button('选择并进入本集升级确认').click();await page.getByRole('alert').waitFor();assert.equal(await page.getByRole('heading',{name:'TV 单集品质升级',exact:true}).count(),0);
 // Eligible exact selection enters the existing panel without creating any phase command.
 shared=false;await open();const before=requests.filter(r=>r.method==='POST').length;await button('比较本集候选品质').click();await button('选择并进入本集升级确认').click();await page.getByRole('heading',{name:'TV 单集品质升级',exact:true}).waitFor();assert.equal(requests.filter(r=>r.method==='POST').length,before+1);
 assert.equal(requests.filter(r=>r.path.includes('commands')).length,0);await button('读取升级比较').click();await page.getByText('候选严格优于旧文件。比较结果尚未授权下载。',{exact:true}).waitFor();
 // Same search and file keep an in-flight comparison through a real SSE reset.
 holdComparison=true;discoveredFile=file.id;await open();
 async function refreshDuringComparison(nextFile){
  const started=new Promise(resolve=>{comparisonStarted=resolve});
  await button('比较本集候选品质').click();await started;
  discoveredFile=nextFile;
  const refreshed=page.waitForResponse(response=>response.url().endsWith('/mock')&&response.request().postDataJSON()?.path==='/api/v1/release-searches/{id}');
  assert(streams.size>0);for(const stream of streams)stream.write('event: reset\ndata: {}\n\n');
  await(await refreshed).finished();await page.evaluate(()=>new Promise(requestAnimationFrame));
  releaseComparison();
 }
 await refreshDuringComparison(file.id);
 await button('选择并进入本集升级确认').waitFor();
 // A changed current file cancels and discards the original comparison response.
 const cancelled=page.waitForEvent('requestfailed',{predicate:request=>request.url().endsWith('/mock')&&request.postDataJSON()?.path==='/api/v1/subscriptions/{id}/tv-episode-upgrade-previews'});
 await open();await refreshDuringComparison(id(10));
 assert.match((await cancelled).failure()?.errorText??'',/ABORTED/);
 await page.evaluate(()=>new Promise(requestAnimationFrame));
 assert.equal(await button('选择并进入本集升级确认').count(),0);
 assert.equal(requests.filter(r=>r.path.includes('commands')).length,0);
 console.log('PASS comparison refresh guards: same-scope SSE preserves comparison; changed-file SSE discards late result');
 assert.deepEqual(errors,[]);console.log('PASS production Workspace controlled API: exact discovered file/unit, fresh shared-file rejection, existing TV panel recompare, zero automatic phase commands');
}finally{await browser?.close();await server?.close();await rm(work,{recursive:true,force:true});}
