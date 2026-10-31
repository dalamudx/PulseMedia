// Standalone fixed-API Chrome acceptance, NOT real PostgreSQL acceptance.
// Not *.test.mjs: the default pure suite must not require task environment.
import assert from 'node:assert/strict';
import {mkdtemp,writeFile,rm,realpath,stat} from 'node:fs/promises';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
import {createServer} from 'vite';
import react from '@vitejs/plugin-react';
import {chromium} from 'playwright-core';
const supplied=process.env.PULSE_TEST_TEMP_ROOT;
assert(supplied&&path.isAbsolute(supplied),'supply an owned PULSE_TEST_TEMP_ROOT under /data/temp');
const root=await realpath(supplied);
assert(root.startsWith('/data/temp/')&&(await stat(root)).isDirectory());
for(const key of ['TMPDIR','TMP','TEMP'])process.env[key]=root;
const owned=await mkdtemp(path.join(root,'movie-discovery-'));
const web=fileURLToPath(new URL('../',import.meta.url));
const id=n=>`00000000-0000-4000-8000-${String(n).padStart(12,'0')}`;
const actor=id(1),subscription=id(2),file=id(3),candidate=id(4),search=id(5),library=id(6);
const title='固定电影.Movie.2026.2160p.WEB-DL.HEVC.'+'UnbrokenCandidate'.repeat(24)+'中文🎬';
const version={actor_id:actor,subscription_id:subscription,media_id:id(10),unit_id:id(11),root_id:library,intent_version:5,policy_id:'policy',policy_revision:7,input_stamp:'a'.repeat(64)};
const report={fixture:'fixed controlled API, NOT real PG',checks:[],widths:[],visualFailures:[]};
const calls=[],errors=[],aborts=[];
const writes=()=>calls.filter(c=>c.method!=='GET');
let server,browser,release;
try {
  const auth=path.join(owned,'auth.ts');
  await writeFile(auth,`import {useSyncExternalStore} from 'react';let state={user:{id:'${actor}',role:'admin'},checking:false};const listeners=new Set();globalThis.switchMovieAccount=next=>{state=next;for(const fn of listeners)fn()};export const useAuth=()=>useSyncExternalStore(fn=>{listeners.add(fn);return()=>listeners.delete(fn)},()=>state);`);
  const imports=['assets/main.css','assets/workspace.css','assets/redesign.css','features/configuration/configuration.css'].map(p=>`import '/@fs${web}src/${p}';`).join('\n');
  await writeFile(path.join(owned,'index.html'),'<!doctype html><html lang="zh-CN"><meta name="viewport" content="width=device-width,initial-scale=1"><div id="root"></div><script type="module" src="/fixture.tsx"></script></html>');
  await writeFile(path.join(owned,'fixture.tsx'),`import React from 'react';import {createRoot} from 'react-dom/client';
import {MovieDiscoveryPanel} from '/@fs${web}src/features/configuration/MovieDiscoveryPanel.tsx';
import {MovieUpgradePanel} from '/@fs${web}src/features/configuration/MovieUpgradePanel.tsx';
${imports}
const subscription={id:'${subscription}',media_type:'movie',title:'固定电影',root_id:'${library}',intent_version:5,quality_policy_revision:7};
createRoot(document.getElementById('root')).render(<main style={{padding:16,minWidth:0}}><p>受控 API 验收夹具 · 非真实 PG</p>{location.search.includes('parent')?<MovieUpgradePanel subscription={subscription} wantedId="" onUpdated={()=>{}}/>:<MovieDiscoveryPanel subscriptionId="${subscription}" intentVersion={5} policyRevision={7} onSelect={value=>{globalThis.movieSelection=value}}/>}</main>);`);
  // Register BEFORE Vite's SPA fallback. A late middleware would return HTML to
  // EventSource and manufacture activity invalidations that cancel parent reads.
  const streamPlugin={name:'controlled-activity-stream',configureServer(s){s.middlewares.use('/api/v1/stream',(_req,res)=>{res.writeHead(200,{'Content-Type':'text/event-stream','Cache-Control':'no-store'});res.write(': controlled fixture\n\n');});}};
  server=await createServer({configFile:false,root:owned,cacheDir:path.join(owned,'vite-cache'),plugins:[streamPlugin,react()],resolve:{alias:[{find:'@/features/auth/AuthContext',replacement:auth},{find:'@',replacement:path.join(web,'src')},{find:/^react$/,replacement:path.join(web,'node_modules/react')},{find:/^react\//,replacement:path.join(web,'node_modules/react')+'/'},{find:/^react-dom\//,replacement:path.join(web,'node_modules/react-dom')+'/'}]},server:{host:'127.0.0.1',port:0,fs:{allow:[owned,web]}},logLevel:'error'});
  await server.listen();const base=`http://127.0.0.1:${server.httpServer.address().port}`;
  browser=await chromium.launch({executablePath:process.env.PULSE_CHROME_PATH??'/usr/bin/google-chrome',headless:true,args:['--no-sandbox','--disable-dev-shm-usage','--disable-background-networking'],env:{...process.env,XDG_CACHE_HOME:path.join(owned,'chrome-cache')}});
  report.chrome=browser.version();
  const context=await browser.newContext({viewport:{width:1440,height:1000}}),page=await context.newPage();page.setDefaultTimeout(7000);
  const fixture={state:'succeeded',file,total:1,partial:false,improvement:true,intent:5,patch:{},hold:''};let reached;
  function response(p,method,body){
    if(p.endsWith('/movie-discovery'))return {subscription_id:subscription,search_id:search,current_file_id:fixture.file,state:fixture.state,execution_allowed:false};
    if(p===`/api/v1/release-searches/${search}`)return {search:{id:search,subscription_id:subscription,query:'固定电影',created_at:'2026-01-01T00:00:00Z',deadline:'2026-01-02T00:00:00Z',source_count:1,ranking_policy_id:'policy',ranking_policy_revision:7},state:fixture.state,sources:[],results:Array.from({length:fixture.total},(_,i)=>({id:i?id(100+i):candidate,title:i?'额外候选 '+i:title,size_bytes:1000,seeders:5,leechers:0,indexer_name:'固定站点'})),partial:fixture.partial,truncated:fixture.total>25,total_candidates:fixture.total,statistics_mode:'latest_persisted',sort_order:'quality_desc_seeders_desc_id_asc'};
    if(p.endsWith('/upgrade-previews')&&method==='POST')return {status:'preview_not_executable',execution_allowed:false,comparable:true,improvement:fixture.improvement,reason:fixture.improvement?'better':'not_better',current_file_id:body.current_file_id,candidate_id:body.candidate_id,current_fact_id:id(8),source_id:id(9),version,observed_at:'2026-01-01T00:00:00Z',expected_client_digest:'b'.repeat(64),current_score:300,candidate_score:fixture.improvement?500:300,...fixture.patch};
    if(p===`/api/v1/subscriptions/${subscription}`)return {id:subscription,media_type:'movie',title:'固定电影',root_id:library,intent_version:fixture.intent,quality_policy_revision:7};
    if(p==='/api/v1/library/mapping-options')return {files:[{id:file,root_id:library,root_name:'电影库',rel_path:'正确旧文件.mkv'},{id:id(12),root_id:library,root_name:'电影库',rel_path:'另一文件.mkv'}],limit:100};
    if(p.endsWith('/upgrade-modes')&&method==='POST'){fixture.intent=5;return {subscription_id:subscription,intent_version:5,mode:'replace_if_better'};}
    throw Error(`unexpected ${method} ${p}`);
  }
  page.on('pageerror',e=>errors.push(e.message));page.on('requestfailed',r=>aborts.push({path:new URL(r.url()).pathname,error:r.failure()?.errorText}));
  await page.route('**/api/v1/**',async route=>{
    const req=route.request(),p=new URL(req.url()).pathname;if(p==='/api/v1/stream')return route.continue();
    const method=req.method(),body=req.postData()?req.postDataJSON():null;calls.push({method,path:p,body});let data,code=200;
    try{data=response(p,method,body);if(p.endsWith('/upgrade-modes'))code=202;}catch(error){errors.push(String(error));data={};code=500;}
    if(fixture.hold&&p.endsWith(fixture.hold)){fixture.hold='';await new Promise(resolve=>{release=resolve;reached?.();});}
    await route.fulfill({status:code,contentType:'application/json',body:JSON.stringify(data)}).catch(error=>{if(!req.failure()&&!String(error).includes('closed'))throw error;});
  });
  const panel=page.getByRole('region',{name:'持续电影候选发现',exact:true});
  const button=name=>panel.getByRole('button',{name,exact:true});
  const compare=()=>button('比较此候选品质').first(),choose=()=>button('选择并进入升级确认');
  const pass=text=>report.checks.push(text);
  async function refresh(){await button('刷新发现结果').click();await page.waitForFunction(()=>!document.body.innerText.includes('正在读取发现状态'));}
  async function compareAndWait(target){const reply=page.waitForResponse(r=>new URL(r.url()).pathname.endsWith('/upgrade-previews'));await compare().click();await (await reply).finished();await target.waitFor();}
  await page.goto(base);await compare().waitFor();assert.equal(writes().length,0);assert.deepEqual(calls.map(c=>c.path),[`/api/v1/subscriptions/${subscription}/movie-discovery`,`/api/v1/release-searches/${search}`]);pass('GET-only startup reads existing search');
  await compareAndWait(choose());await choose().click();assert.deepEqual(await page.evaluate(()=>({id:movieSelection.candidate.id,file:movieSelection.currentFileId,title:movieSelection.candidate.title})),{id:candidate,file,title});assert.deepEqual(writes().map(c=>c.body),[{current_file_id:file,candidate_id:candidate}]);pass('explicit better comparison and exact candidate/file handoff');
  fixture.improvement=false;await compareAndWait(panel.getByText('当前证据未证明此候选可用于改善品质。',{exact:true}));assert.equal(await choose().count(),0);fixture.improvement=true;pass('same quality cannot choose');
  for(const patch of [{current_file_id:id(90)},{candidate_id:id(90)},{execution_allowed:true},{version:{...version,intent_version:4}},{version:{...version,policy_revision:6}}]){fixture.patch=patch;await compareAndWait(panel.getByRole('alert'));assert.equal(await choose().count(),0);}fixture.patch={};pass('file/candidate/intent/policy/execution mismatch cannot choose');
  await compareAndWait(choose());fixture.file=id(12);await refresh();assert.equal(await choose().count(),0);fixture.file=file;await refresh();pass('file change clears comparison');
  fixture.state='stale';fixture.file=undefined;await refresh();await panel.getByText('订阅或文件已改变，等待下次检查',{exact:true}).waitFor();assert.equal(await compare().count(),0);assert.equal(await choose().count(),0);fixture.state='succeeded';fixture.file=file;await refresh();pass('stale file evidence has no candidate choice');
  fixture.partial=true;fixture.total=30;await refresh();await panel.getByText('检索结果不完整，未列出不代表不存在。',{exact:true}).waitFor();await panel.getByText('显示前25条检索候选；可在现有站点检索中进一步查找。',{exact:true}).waitFor();assert.equal(await button('比较此候选品质').count(),25);pass('partial pagination copy and bounded 25-candidate list');
  fixture.total=1;await refresh();await compareAndWait(choose());
  for(const width of [1440,390,320]){
    await page.setViewportSize({width,height:1000});const m=await panel.evaluate(el=>({documentWidth:document.documentElement.scrollWidth,widgetWidth:el.scrollWidth,clientWidth:el.clientWidth,controls:[...el.querySelectorAll('button')].map(b=>({text:b.textContent,width:b.getBoundingClientRect().width,height:b.getBoundingClientRect().height}))}));
    report.widths.push({width,...m});await page.screenshot({path:path.join(root,`movie-discovery-${width}.png`),fullPage:false});
    if(m.documentWidth>width||m.widgetWidth>m.clientWidth+1)report.visualFailures.push({width,kind:'overflow'});if(m.controls.some(c=>c.width<44||c.height<44))report.visualFailures.push({width,kind:'touch-target',controls:m.controls});
  }
  // One real short timer tick, then advance Date alone beyond the polling budget.
  // The next real timer must stop scheduling. Pure tests check all 18 exact ticks.
  fixture.partial=false;fixture.state='running';await refresh();const before=writes().length,start=calls.length;
  const searchRead=()=>page.waitForResponse(r=>new URL(r.url()).pathname===`/api/v1/release-searches/${search}`);
  await (await searchRead()).finished();await page.clock.setFixedTime(new Date(Date.now()+40_000));await (await searchRead()).finished();
  const stopped=calls.length;await page.waitForTimeout(2400);assert.equal(calls.length,stopped);assert(stopped-start>=2&&stopped-start<=6);assert.equal(writes().length,before);fixture.state='succeeded';pass('real 2-second GET polling stops after elapsed budget; no writes');
  for(const held of ['/upgrade-previews','/movie-discovery'])for(const next of [{user:{id:id(20),role:'admin'},checking:false},{user:{id:actor,role:'member'},checking:false}]){
    await page.goto(base);await compare().waitFor();fixture.hold=held;const started=new Promise(resolve=>{reached=resolve;});if(held==='/upgrade-previews')await compare().click();else await button('刷新发现结果').click();
    await started;const aborted=page.waitForEvent('requestfailed',{predicate:r=>new URL(r.url()).pathname.endsWith(held)});await page.evaluate(value=>globalThis.switchMovieAccount(value),next);release();release=undefined;await aborted;
    if(next.user.role==='admin')await compare().waitFor();else await panel.waitFor({state:'detached'});assert.equal(await choose().count(),0);assert.equal(await page.evaluate(()=>globalThis.movieSelection),undefined);
  }pass('actor/role changes abort pending status and comparison; late responses fenced');
  // Actual parent component, including an explicit mode command, real selects and
  // its second comparison. Never substitute an onSelect callback for this check.
  fixture.intent=4;await page.goto(base+'?parent');await page.getByRole('button',{name:'管理品质升级',exact:true}).click();const parent=page.getByRole('region',{name:'电影品质升级',exact:true});
  await parent.getByRole('combobox',{name:'升级模式',exact:true}).selectOption('replace_if_better');await parent.getByRole('checkbox',{name:'我确认设置此升级模式',exact:true}).check();await parent.getByRole('button',{name:'确认升级模式',exact:true}).click();await parent.getByText('已收到服务器回执。',{exact:true}).waitFor();
  await parent.getByRole('button',{name:'刷新升级资源',exact:true}).click();await compare().waitFor();
  const boundary=writes().length;await compareAndWait(choose());await choose().click();assert.equal(await parent.getByRole('combobox',{name:'当前媒体库文件',exact:true}).inputValue(),file);assert.equal(await parent.getByRole('combobox',{name:'候选资源',exact:true}).inputValue(),candidate);assert.equal(await parent.getByRole('region',{name:'升级比较',exact:true}).count(),0);
  await parent.getByRole('button',{name:'读取升级比较',exact:true}).click();await parent.getByText('候选品质严格优于当前文件。',{exact:true}).waitFor();assert.deepEqual(writes().slice(boundary).map(c=>({path:c.path,body:c.body})),Array.from({length:2},()=>({path:`/api/v1/subscriptions/${subscription}/upgrade-previews`,body:{current_file_id:file,candidate_id:candidate}})));
  assert(await parent.getByRole('button',{name:'确认冻结预留',exact:true}).isDisabled());assert(!calls.some(c=>/upgrade-(reservations|preparations|sends)/.test(c.path)));pass('production parent receives exact candidate/file and re-compares without automatic reservation/preparation/send');
  assert.deepEqual(errors,[]);report.behaviorPassed=true;assert.deepEqual(report.visualFailures,[],'responsive acceptance failed; see receipt');pass('long title: no overflow and >=44px controls at 1440/390/320');report.passed=true;await context.close();
} catch(error) {report.passed=false;report.error=String(error);throw error;}
finally {
  release?.();if(browser)await browser.close();if(server){server.httpServer?.closeAllConnections();await server.close();}await rm(owned,{recursive:true,force:true});
  report.requests=calls.length;report.writes=writes();report.aborts=aborts;report.cleanedFixture=owned;await writeFile(path.join(root,'movie-discovery-receipt.json'),JSON.stringify(report,null,2)+'\n');console.log(JSON.stringify(report));
}
