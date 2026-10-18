import { test, before, after } from 'node:test';
import assert from 'node:assert/strict';
import { mkdir, writeFile, rm, symlink, realpath } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { createServer } from 'vite';
import react from '@vitejs/plugin-react';
import { chromium } from 'playwright-core';
const root=process.env.PULSE_CHOICE_TEST_ROOT;
if(!root || !root.startsWith('/data/temp/pulse-m3-03-choice-ui.') || path.resolve(root)!==root)throw Error('explicit owned private root required');
for(const key of ['TMPDIR','TMP','TEMP'])if(process.env[key]!==path.join(root,'runtime'))throw Error('temporary paths must use owned runtime');
const web=fileURLToPath(new URL('..',import.meta.url));
if(!(await realpath(web)).startsWith(root+'/'))throw Error('test source must be private');
const runtime=path.join(root,'runtime','browser-fixture');
const id=n=>`00000000-0000-4000-8000-${String(n).padStart(12,'0')}`;
const sub=id(2),actor=id(1),stamp='a'.repeat(64),material='b'.repeat(64);
const version={media_id:id(20),intent_version:1,media_version:1,policy_id:id(21),policy_revision:1,scope_stamp:'c'.repeat(64),configuration_stamp:'d'.repeat(64),selection_policy_stamp:'e'.repeat(64)};
let server,browser,base;
before(async()=>{
 await mkdir(runtime,{recursive:true});await symlink(path.join(web,'node_modules'),path.join(runtime,'node_modules'),'dir');
 await writeFile(path.join(runtime,'index.html'),'<!doctype html><html lang="zh-CN"><meta charset="UTF-8"><title>只读比较隔离夹具</title><div id="root"></div><script type="module" src="/fixture.tsx"></script></html>');
 await writeFile(path.join(runtime,'fixture.tsx'),`
import React,{useState} from 'react';import{createRoot}from'react-dom/client';
import{TVSelectionPreviewPanel}from'@/features/configuration/TVSelectionPreviewPanel';
import{EpisodeCandidateReviewPanel}from'@/features/configuration/EpisodeCandidateReviewPanel';
import{AuthProvider,useAuth}from'@/features/auth/AuthContext';
function AuthBridge({children}){window.choiceAuthCheck=useAuth().checkStatus;return children}
import '@/features/configuration/configuration.css';import '@/assets/main.css';
const scope={userId:'${actor}',subscriptionId:'${sub}',signature:'v1'};
const rows=[{...scope,kind:'pack',candidateId:'${id(103)}',reviewId:'${id(3)}',season:2,unitKeys:['S02E03','S02E04'],title:'固定夹具：作品第二季完整包'},
{...scope,kind:'episode',candidateId:'${id(104)}',reviewId:'${id(4)}',season:2,unitKeys:['S02E03'],title:'固定夹具：作品第二季第三集'},
{...scope,kind:'episode',candidateId:'${id(105)}',reviewId:'${id(5)}',season:2,unitKeys:['S02E04'],title:'固定夹具：作品第二季第四集'}];
function App(){const[s,set]=useState(scope),[items,update]=useState(rows);window.choiceScope=set;window.choiceRows=update;
return <main className="config-editor" style={{maxWidth:900,margin:'0 auto',padding:24}}><p>隔离浏览器夹具 · 非真实 PG / Cookie / 下载器证据</p>{location.search.includes('integration')?<AuthProvider><AuthBridge><EpisodeCandidateReviewPanel subscriptionId={s.subscriptionId} signature={s.signature}/></AuthBridge></AuthProvider>:<TVSelectionPreviewPanel {...s} alternatives={items}/>}</main>};
createRoot(document.getElementById('root')!).render(<App/>);`);
 server=await createServer({configFile:false,configLoader:'native',root:runtime,envDir:runtime,cacheDir:path.join(runtime,'cache'),plugins:[react()],resolve:{alias:{'@':path.join(web,'src')}},server:{host:'127.0.0.1',port:0,fs:{allow:[root]}}});await server.listen();base=`http://127.0.0.1:${server.httpServer.address().port}`;
 browser=await chromium.launch({executablePath:'/opt/google/chrome/chrome',headless:true,args:['--no-sandbox','--disable-dev-shm-usage','--disable-background-networking'],timeout:30000});
});
after(async()=>{try{await browser?.close()}finally{await server?.close();await rm(runtime,{recursive:true,force:true})}});
async function pageFixture(integration=false){
 const context=await browser.newContext({viewport:{width:1440,height:1000}});let status=200,delay=null,unsafe=false,authActor=actor,reason='quality_then_stable_id',excluded=[];const requests=[];
 await context.route('**/*',async route=>{
  const req=route.request(),url=new URL(req.url());if(url.hostname!=='127.0.0.1')return route.abort();if(!url.pathname.startsWith('/api/'))return route.continue();
  requests.push({url:url.pathname,method:req.method(),body:req.postDataJSON(),headers:req.headers()});
  const reply=(body,code=200)=>route.fulfill({status:code,contentType:'application/json',body:JSON.stringify(body)});
  if(url.pathname.endsWith('/tv-selection-preview')){
   const body=req.postDataJSON();const singles=body.pack_policy.mode==='prefer_episodes'||body.packs.length===0;const candidates=singles?body.episodes:body.packs;
   const output={status:'preview_not_executable',execution_allowed:unsafe,version,decision:{kind:candidates.length?(singles?'episodes':'pack'):'hold',candidate_ids:candidates.map(r=>r.candidate_id),reason,score:0},excluded};
   if(delay){const wait=delay;delay=null;await wait;}return reply(status===200?output:{message:'fixture failure'},status);
  }
  if(url.pathname.endsWith('/auth/status'))return reply({initialized:true,current_user:{id:authActor,username:'fixture',role:'admin'}});
  if(url.pathname.endsWith('/releases/search'))return reply({partial:false,results:[103,104,105].map((n,i)=>({id:id(n),title:['固定夹具完整包','固定夹具第三集','固定夹具第四集'][i],indexer_name:'隔离站点',size_bytes:100}))});
  if(url.pathname.endsWith('/tv-coverage-candidate-reviews/preview'))return reply({subscription_id:sub,candidate_id:id(103),input_stamp:stamp,material_stamp:material,rows:[3,4].map(n=>({unit_id:id(200+n),unit_key:`S02E0${n}`,source_number:n})),can_review:true,execution_allowed:false,hold_reason:''});
  if(url.pathname.endsWith('/episode-mapping/explain-release')){const n=Number(req.postDataJSON().candidate_id.slice(-12));const ep=n===105?4:3;return reply({subscription_id:sub,candidate_id:id(n),candidate_input_stamp:stamp,source_current:true,coverage_known:true,identity_decision:'allow',quality_decision:'allow',reason:'standard_coverage_only',rows:[{unit_id:id(200+ep),unit_key:`S02E0${ep}`,unit_version:1}],mapping_revision:1,intent_version:1,observed_at:new Date().toISOString()})}
  if(url.pathname.endsWith('/episode-candidate-reviews')){const n=Number(req.postDataJSON().candidate_id.slice(-12));return reply({id:id(n-100),subscription_id:sub,candidate_id:id(n),unit_id:id(n===105?204:203),input_stamp:stamp,status:'reviewed_not_executable'})}
  if(url.pathname.endsWith('/status'))return reply({id:sub,intent_version:1,policy_revision:1,items:[],blockers:[],observed_at:new Date().toISOString()});
  if(url.pathname===`/api/v1/subscriptions/${sub}`)return reply({id:sub,intent_version:1,quality_policy_revision:1,root_name:'隔离媒体库'});
  throw Error(`unexpected API ${req.method()} ${url.pathname}`);
 });
 if(integration)await context.addInitScript(({sub,actor,stamp,material,id3,id103,id203,id204})=>{
  localStorage.setItem(`pulse:complete-tv:v2:${actor}:${sub}`,JSON.stringify({version:2,entries:[{kind:'review',materialStamp:material,userId:actor,subscriptionId:sub,coverage:[{unitId:id203,unitKey:'S02E03'},{unitId:id204,unitKey:'S02E04'}],key:'00000000-0000-4000-8000-000000000080',signature:'v1',candidateId:id103,phase:'confirmed',body:{candidate_id:id103,expected_input_stamp:stamp,confirmed:true,reason:'固定人工审阅夹具'},receipt:{id:id3,subscription_id:sub,candidate_id:id103,input_stamp:stamp,material_stamp:material,status:'reviewed_not_executable'}}]}));
  // Deliberately unbound legacy cache must never populate comparison.
  sessionStorage.setItem(`pulse:episode-review-receipt:${sub}`,JSON.stringify({id:'00000000-0000-4000-8000-000000000099',subscription_id:sub,candidate_id:'00000000-0000-4000-8000-000000000199',unit_id:id203,input_stamp:stamp,status:'reviewed_not_executable'}));
 },{sub,actor,stamp,material,id3:id(3),id103:id(103),id203:id(203),id204:id(204)});
 const page=await context.newPage();page.setDefaultTimeout(7000);await page.goto(base+(integration?'?integration':''));
 return {context,page,requests,setAuthActor:n=>authActor=n,setStatus:n=>status=n,setReason:n=>reason=n,setExcluded:n=>excluded=n,setUnsafe:()=>unsafe=true,defer:()=>{let release;delay=new Promise(r=>release=r);return release}};
}
const panel=p=>p.getByRole('region',{name:'包与单集只读比较'});
async function choose(page){const p=panel(page);await p.getByLabel('比较季',{exact:true}).selectOption('2');await p.getByLabel('本次比较偏好',{exact:true}).selectOption('none');await p.getByLabel('缺失比例门槛',{exact:true}).fill('0.5');await p.getByRole('checkbox').first().check();return p}
async function read(p){await p.getByRole('button',{name:'读取最新比较',exact:true}).click();await p.getByText('仅预览，不可执行。不会创建占位、下载或整理任务。',{exact:true}).waitFor()}
const shown=p=>p.getByText('仅预览，不可执行。不会创建占位、下载或整理任务。',{exact:true});
test('Chrome: explicit fields, four modes, ID-only body, version read, no storage or command side effects',async()=>{
 const f=await pageFixture();try{const p=panel(f.page);assert.equal(await p.getByLabel('比较季',{exact:true}).inputValue(),'');assert.equal(await p.getByLabel('本次比较偏好').inputValue(),'');assert.equal(await p.getByLabel('缺失比例门槛',{exact:true}).inputValue(),'');assert.equal(await p.getByRole('button',{name:'读取最新比较',exact:true}).isDisabled(),true);
 await choose(f.page);for(const mode of ['none','prefer_pack','prefer_episodes','require_pack']){await p.getByLabel('本次比较偏好').selectOption(mode);await read(p)}
 await p.getByRole('button',{name:'按本次版本重新读取'}).click();await shown(p).waitFor();assert.deepEqual(f.requests.at(-1).body.expected,version);
 assert(f.requests.every(r=>r.url.endsWith('/tv-selection-preview')&&r.method==='POST'&&!r.headers['idempotency-key']));for(const r of f.requests)assert.deepEqual(Object.keys(r.body).sort(),['episodes',...(r.body.expected?['expected']:[]),'pack_policy','packs','season'].sort());
 assert.deepEqual(await f.page.evaluate(()=>[localStorage.length,sessionStorage.length]),[0,0]);assert.equal(await p.getByRole('button').count(),2);
 }finally{await f.context.close()}
});
for(const status of [401,403,409,503])test(`Chrome: ${status} clears result and explicit retry is only the read endpoint`,async()=>{
 const f=await pageFixture();try{const p=await choose(f.page);await read(p);f.setStatus(status);await p.getByRole('button',{name:'按本次版本重新读取'}).click();await p.getByRole('alert').waitFor();assert.equal(await shown(p).count(),0);assert.equal(await p.getByRole('button',{name:'按本次版本重新读取'}).isDisabled(),true);f.setStatus(200);await read(p);assert.equal(f.requests.at(-1).body.expected,undefined);assert(f.requests.every(r=>r.url.endsWith('/tv-selection-preview')))}finally{await f.context.close()}
});
test('Chrome: late success cannot restore result after reference change and new read',async()=>{
 const f=await pageFixture();try{const p=await choose(f.page);const release=f.defer();const observed=f.page.waitForRequest(r=>r.url().endsWith('/tv-selection-preview'));await p.getByRole('button',{name:'读取最新比较',exact:true}).click();await observed;await p.getByRole('checkbox').nth(1).check();release();await read(p);assert.equal(f.requests.at(-1).body.episodes.length,1);assert.equal(await shown(p).count(),1)}finally{await f.context.close()}
});
for(const field of ['userId','subscriptionId','signature'])test(`Chrome: ${field} change hides result and old references synchronously`,async()=>{
 const f=await pageFixture();try{const p=await choose(f.page);await read(p);await f.page.evaluate(({field,value})=>window.choiceScope(s=>({...s,[field]:value})),{field,value:field==='signature'?'v2':id(99)});await f.page.waitForFunction(()=>!document.body.textContent.includes('仅预览，不可执行。'));assert.equal(await p.getByRole('checkbox').count(),0);assert.equal(await p.getByLabel('比较季',{exact:true}).inputValue(),'')}finally{await f.context.close()}
});
test('Chrome: late success after account switch cannot publish a result',async()=>{
 const f=await pageFixture();try{const p=await choose(f.page);const release=f.defer();const observed=f.page.waitForRequest(r=>r.url().endsWith('/tv-selection-preview'));await p.getByRole('button',{name:'读取最新比较',exact:true}).click();await observed;await f.page.evaluate(id=>window.choiceScope(s=>({...s,userId:id})),id(99));release();await f.page.waitForFunction(()=>document.querySelector('[aria-label="比较季"]')?.querySelectorAll('option').length===1);assert.equal(await shown(p).count(),0);assert.equal(await p.getByRole('checkbox').count(),0)}finally{await f.context.close()}
});
test('Chrome: removed review and executable malformed response clear result',async()=>{
 const f=await pageFixture();try{const p=await choose(f.page);await read(p);await f.page.evaluate(()=>window.choiceRows(rows=>rows.slice(1)));await f.page.waitForFunction(()=>!document.body.textContent.includes('仅预览，不可执行。'));assert.equal(await p.getByRole('button',{name:'读取最新比较',exact:true}).isDisabled(),true);await p.getByRole('checkbox').first().check();f.setUnsafe();await p.getByRole('button',{name:'读取最新比较',exact:true}).click();await p.getByRole('alert').waitFor();assert.equal(await shown(p).count(),0)}finally{await f.context.close()}
});
test('Chrome integration: saved owner-bound pack + independently successful singles; legacy cache excluded; compare performs zero commands',async()=>{
 const f=await pageFixture(true);try{const page=f.page,p=panel(page);await p.waitFor();assert.equal(await p.getByRole('checkbox').count(),0);
 await page.getByLabel('单集资源关键词').fill('固定测试');await page.getByRole('button',{name:'检索真实站点资源',exact:true}).click();await page.getByLabel('待审核站点资源').selectOption(id(103));await page.getByRole('button',{name:'读取完整季覆盖',exact:true}).click();
 await page.waitForFunction(()=>document.querySelector('[aria-label="比较季"]')?.querySelectorAll('option').length===2);
 for(const candidate of [104,105]){await page.getByLabel('待审核站点资源').selectOption(id(candidate));await page.getByRole('button',{name:'读取当前候选证据',exact:true}).click();const ack=page.getByRole('checkbox',{name:'已核对当前资源确属此作品及单集，确认只保存审核，不创建任务'});await ack.check();await page.getByRole('button',{name:'保存当前单集审核',exact:true}).click();await page.waitForFunction(n=>document.body.textContent.includes(`审核记录 00000000-0000-4000-8000-${String(n-100).padStart(12,'0')} 已保存`),candidate)}
 await choose(page);assert.equal(await p.getByRole('checkbox').count(),3);await p.getByRole('checkbox').nth(1).check();await p.getByRole('checkbox').nth(2).check();const before=f.requests.length;await read(p);assert.equal(f.requests.length,before+1);assert.equal(f.requests.at(-1).body.packs.length,1);assert.equal(f.requests.at(-1).body.episodes.length,2);assert(!f.requests.some(r=>r.url.endsWith('/complete-tv-acquisitions')||r.url.endsWith('/episode-acquisitions')));
 await page.setViewportSize({width:1440,height:1000});await p.screenshot({path:path.join(root,'desktop.png')});await page.setViewportSize({width:390,height:844});await p.screenshot({path:path.join(root,'mobile.png')});assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),true);
 assert.equal(f.requests.filter(r=>r.url.endsWith('/tv-coverage-candidate-reviews')).length,0);
 await page.getByLabel('待审核站点资源').selectOption(id(103));assert.equal(await shown(p).count(),0);
 // Actual AuthContext transition remounts the collector; an unbound single receipt remains in legacy storage.
 f.setAuthActor(id(99));await page.evaluate(()=>window.choiceAuthCheck());await p.waitFor();assert.equal(await p.getByRole('checkbox').count(),0);assert.equal(await p.getByLabel('比较季',{exact:true}).inputValue(),'');
 }finally{await f.context.close()}
});

for(const reason of ['__proto__','constructor','toString'])test(`Chrome: inherited reason ${reason} renders safe text without crashing`,async()=>{
 const f=await pageFixture();try{const errors=[];f.page.on('pageerror',e=>errors.push(e.message));const p=await choose(f.page);f.setReason(reason);await read(p);await p.getByText(/：当前状态需复核$/).waitFor();assert.deepEqual(errors,[])}finally{await f.context.close()}
});
for(const reason of [{toString:null},null])test(`Chrome: malformed exclusion ${JSON.stringify(reason)} clears the old comparison`,async()=>{
 const f=await pageFixture();try{const errors=[];f.page.on('pageerror',e=>errors.push(e.message));const p=await choose(f.page);await read(p);f.setExcluded([{candidate_id:id(103),reason}]);await p.getByRole('button',{name:'按本次版本重新读取'}).click();await p.getByRole('alert').waitFor();assert.equal(await shown(p).count(),0);assert.deepEqual(errors,[])}finally{await f.context.close()}
});

for(const kind of ['episode','pack'])for(const reason of ['__proto__','constructor','toString'])test(`Chrome: adjacent ${kind} reason ${reason} remains safe`,async()=>{
 const f=await pageFixture(true);try{
  const errors=[];f.page.on('pageerror',e=>errors.push(e.message));
  const single=f.page.getByRole('region',{name:'真实资源单集审核',exact:true});await single.getByLabel('单集资源关键词').fill('固定夹具');await single.getByRole('button',{name:'检索真实站点资源',exact:true}).click();await single.getByLabel('待审核站点资源').selectOption(id(kind==='episode'?104:103));
  if(kind==='episode'){
   await f.context.route('**/api/v1/subscriptions/*/episode-mapping/explain-release',route=>route.fulfill({status:200,contentType:'application/json',body:JSON.stringify({subscription_id:sub,candidate_id:id(104),candidate_input_stamp:stamp,source_current:true,coverage_known:true,identity_decision:'allow',quality_decision:'allow',reason,rows:[{unit_id:id(203),unit_key:'S02E03',unit_version:1}],mapping_revision:1,intent_version:1,observed_at:'2026-10-16T00:00:00Z'})}));
   await single.getByRole('button',{name:'读取当前候选证据',exact:true}).click();await single.getByText('来源与覆盖：当前状态需复核',{exact:true}).waitFor();
  }else{
   await f.context.route('**/api/v1/subscriptions/*/tv-coverage-candidate-reviews/preview',route=>route.fulfill({status:200,contentType:'application/json',body:JSON.stringify({subscription_id:sub,candidate_id:id(103),input_stamp:stamp,material_stamp:material,rows:[{unit_id:id(203),unit_key:'S02E03',source_number:3},{unit_id:id(204),unit_key:'S02E04',source_number:4}],can_review:false,execution_allowed:false,hold_reason:reason})}));
   const pack=single.getByRole('region',{name:'完整季覆盖审阅与获取',exact:true});await pack.getByRole('button',{name:'读取完整季覆盖',exact:true}).click();await pack.getByText('尚不能确认完整覆盖：当前状态需复核',{exact:true}).waitFor();
  }
  assert.deepEqual(errors,[]);
 }finally{await f.context.close()}
});
