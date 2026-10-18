// Standalone controls use the real built Activity UI with deterministic HTTP fixtures.
// These checks are deliberately separate from Cookie/PG/App product acceptance.
import assert from 'node:assert/strict';
import {createServer} from 'node:http';
import {readFile} from 'node:fs/promises';
import {fileURLToPath} from 'node:url';
import {join,extname} from 'node:path';
import {chromium} from 'playwright-core';
const actorA='aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa',actorB='bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb';
const op='11111111-1111-4111-8111-111111111111',op2='22222222-2222-4222-8222-222222222222',task='33333333-3333-4333-8333-333333333333',tracking='44444444-4444-4444-8444-444444444444';
const receipt={task_id:task,plan_id:'55555555-5555-4555-8555-555555555555',job_id:'66666666-6666-4666-8666-666666666666',state:'queued'};
const item=id=>({task_id:task,operation_id:id,tracking_job_id:tracking,season:1,attachment_count:1,organization_state:'review_required',can_confirm:true,videos:[{unit_id:'77777777-7777-4777-8777-777777777777',unit_key:id===op?'S01E01':'S01E02',source_relative:'Show/Show.S01E01.mkv',target_relative:'Show (2020)/Season 01/Show - S01E01.mkv',size_bytes:512},{unit_id:'88888888-8888-4888-8888-888888888888',unit_key:id===op?'S01E02':'S01E03',source_relative:'Show/Show.S01E02.mkv',target_relative:'Show (2020)/Season 01/Show - S01E02.mkv',size_bytes:512}]});
let state;function reset(){state={actor:actorA,previewCode:200,detailCode:200,postCode:503,posts:[],release:null,hold:false,streams:new Set()};}reset();
const dist=fileURLToPath(new URL('../dist/',import.meta.url));
const server=createServer(async(req,res)=>{
 const path=new URL(req.url,'http://localhost').pathname;res.setHeader('Cache-Control','no-store');
 function json(code,data){res.statusCode=code;res.setHeader('Content-Type',code>=400?'application/problem+json':'application/json');res.end(JSON.stringify(data));}
 if(path==='/api/v1/stream'){const streams=state.streams;streams.add(res);res.on('close',()=>streams.delete(res));res.setHeader('Content-Type','text/event-stream');res.write(': ready\n\n');return;}
 if(path==='/api/v1/auth/status')return json(200,{initialized:true,current_user:{id:state.actor,username:'control-admin',role:'admin'}});
 if(path==='/api/v1/operations')return json(200,{items:[op,op2].map(operation_id=>({operation_id,title:operation_id===op?'Control One':'Control Two',status:'waiting_organization',created_at:'2026-01-01T00:00:00Z',cancellation_requested:false}))});
 if([op,op2].some(id=>path==='/api/v1/operations/'+id))return json(state.detailCode,{operation_id:path.split('/').at(-1),status:'completed',download_status:'completed',cancellation_requested:false,facts:[]});
 if(req.method==='GET'&&path.endsWith('/complete-tv-organizations')){const normal={items:[item(path.split('/')[4])]};return json(state.previewCode,state.previewCode===200?(state.previewTransform?state.previewTransform(normal):normal):{type:'about:blank',title:'Unavailable',status:state.previewCode});}
 if(req.method==='POST'&&path.endsWith('/complete-tv-organizations')){
  let raw='';for await(const data of req)raw+=data;state.posts.push({key:req.headers['idempotency-key'],body:JSON.parse(raw),path});
  if(state.hold)await new Promise(resolve=>{state.release=resolve;});
  return json(state.postCode,state.postCode===202?receipt:{type:'about:blank',title:'Unconfirmed',status:state.postCode});
 }
 if(path.endsWith('/requirements'))return json(200,{items:[]});
 if(path.startsWith('/api/'))return json(503,{type:'about:blank',title:'Unavailable',status:503});
 try{const file=join(dist,extname(path)?path:'index.html');const data=await readFile(file);res.setHeader('Content-Type',({'.js':'application/javascript','.css':'text/css','.html':'text/html','.svg':'image/svg+xml'})[extname(file)]??'application/octet-stream');res.end(data);}catch{res.statusCode=404;res.end();}
});
await new Promise(resolve=>server.listen(0,'127.0.0.1',resolve));const base='http://127.0.0.1:'+server.address().port;
const browser=await chromium.launch({executablePath:'/opt/google/chrome/chrome',headless:true,args:['--no-sandbox','--disable-dev-shm-usage','--disable-background-networking','--disable-gpu','--disable-software-rasterizer','--no-zygote','--disable-breakpad','--disable-crash-reporter']});
async function waitResolved(page,check,arg){const until=Date.now()+30000;while(Date.now()<until){if(await page.evaluate(check,arg)===true)return;await new Promise(resolve=>setTimeout(resolve,25));}throw Error('Resolved boolean condition timed out: '+check.toString());}
const results=[];
async function scenario(name,run){if(process.env.PULSE_ORGANIZATION_UI_CONTROL_FILTER&&!name.includes(process.env.PULSE_ORGANIZATION_UI_CONTROL_FILTER))return;reset();const context=await browser.newContext({viewport:{width:1280,height:900}});try{await run(context);results.push({name,passed:true});console.log(JSON.stringify({name,passed:true}));}catch(error){console.error(JSON.stringify({failed:name,posts:state.posts.length,panels:await Promise.all(context.pages().map(p=>p.locator('.complete-tv-organization').innerText().catch(()=>'<detached>')))}));throw error;}finally{state.release?.();await context.close();}}
const panel=page=>page.getByRole('region',{name:'完整季整理确认',exact:true});
async function open(page,title='Control One'){await page.goto(base+'/activity');await page.getByRole('region',{name:'持久操作列表'}).getByRole('button',{name:new RegExp('^'+title)}).click();await panel(page).waitFor();}
async function ready(page){await panel(page).getByRole('checkbox').waitFor();await panel(page).getByRole('checkbox').check();assert.equal(await panel(page).getByRole('button',{name:'确认完整季整理',exact:true}).isEnabled(),true);}
const retry=page=>panel(page).getByRole('button',{name:'用原键恢复整理确认',exact:true});
try{
 const malformed=[
  ['bad-task',v=>{v.items[0].task_id='bad';}],
  ['zero-tracking',v=>{v.items[0].tracking_job_id='00000000-0000-0000-0000-000000000000';}],
  ['string-confirm',v=>{v.items[0].can_confirm='true';}],
  ['null-videos',v=>{v.items[0].videos=null;}],
  ['object-video',v=>{v.items[0].videos[0]={unit_id:{}};}],
  ['bad-season',v=>{v.items[0].season=0;}],
  ['bad-unit',v=>{v.items[0].videos[0].unit_key='S02E01';}],
  ['absolute-source',v=>{v.items[0].videos[0].source_relative='/private/source.mkv';}],
  ['escaped-target',v=>{v.items[0].videos[0].target_relative='../escape.mkv';}],
  ['string-size',v=>{v.items[0].videos[0].size_bytes='512';}],
  ['object-state',v=>{v.items[0].organization_state={};}],
  ['negative-attachments',v=>{v.items[0].attachment_count=-1;}],
  ['duplicate-videos',v=>{v.items[0].videos.push({...v.items[0].videos[0]});}],
  ['too-many-items',v=>{v.items.push({...v.items[0]});}],
 ];
 for(const [name,mutate] of malformed)await scenario('malformed preview '+name+' creates no command',async context=>{
  state.previewTransform=v=>{mutate(v);return v;};const page=await context.newPage();const errors=[];page.on('pageerror',e=>errors.push(e.message));await open(page);
  await panel(page).getByRole('alert').waitFor();assert.equal(await panel(page).getByRole('checkbox').count(),0);assert.equal(state.posts.length,0);
  assert.equal(await page.evaluate(()=>Object.keys(localStorage).filter(k=>k.startsWith('pulse.complete-tv-organization.v1:')).length),0);assert.deepEqual(errors,[]);
  state.previewTransform=null;await panel(page).getByRole('button',{name:'重新读取整理材料',exact:true}).click();await panel(page).getByRole('checkbox').waitFor();assert.equal(state.posts.length,0);
 });
 await scenario('malformed preview preserves original unknown command',async context=>{
  const page=await context.newPage();await open(page);await ready(page);await panel(page).getByRole('button',{name:'确认完整季整理',exact:true}).click();await retry(page).waitFor();
  const original=await page.evaluate(()=>Object.entries(localStorage).find(([key])=>key.startsWith('pulse.complete-tv-organization.v1:')));
  state.previewTransform=v=>{v.items[0].task_id='bad';return v;};await panel(page).getByRole('button',{name:'重新读取整理材料',exact:true}).click();await panel(page).getByRole('alert').waitFor();await retry(page).waitFor();
  assert.deepEqual(await page.evaluate(()=>Object.entries(localStorage).find(([key])=>key.startsWith('pulse.complete-tv-organization.v1:'))),original);assert.equal(state.posts.length,1);
  state.previewTransform=null;await page.reload();await open(page);await retry(page).waitFor();assert.equal(state.posts.length,1);
 });
 await scenario('storage failure before send creates no command',async context=>{
  const page=await context.newPage();await open(page);await ready(page);
  await page.evaluate(()=>{Storage.prototype.setItem=function(){throw new Error('blocked');};});await panel(page).getByRole('button',{name:'确认完整季整理',exact:true}).click();await panel(page).getByRole('alert').waitFor();assert.equal(state.posts.length,0);
 });
 await scenario('missing Web Locks fails closed',async context=>{
  await context.addInitScript(()=>Object.defineProperty(navigator,'locks',{value:undefined}));const page=await context.newPage();await open(page);await panel(page).getByText('当前浏览器不支持跨标签锁，整理确认已停用。',{exact:true}).waitFor();assert.equal(await panel(page).getByRole('button',{name:'确认完整季整理',exact:true}).count(),0);assert.equal(state.posts.length,0);
 });
 await scenario('initial operation context failure allows only original recovery',async context=>{
  const page=await context.newPage();await open(page);await ready(page);await panel(page).getByRole('button',{name:'确认完整季整理',exact:true}).click();await retry(page).waitFor();state.detailCode=503;await open(page);await retry(page).waitFor();assert.equal(await panel(page).getByRole('checkbox').count(),0);await retry(page).click();await panel(page).getByRole('alert').waitFor();assert.equal(state.posts.length,2);assert.deepEqual(state.posts[1],state.posts[0]);
 });
 await scenario('preview 401 clears account content without command',async context=>{
  const page=await context.newPage();await open(page);await ready(page);state.previewCode=401;await panel(page).getByRole('button',{name:'重新读取整理材料',exact:true}).click();await panel(page).waitFor({state:'detached'});assert.equal(state.posts.length,0);
 });
 for(const status of [403,409,503])await scenario('preview '+status+' clears old evidence',async context=>{
  const page=await context.newPage();await open(page);await ready(page);state.previewCode=status;await panel(page).getByRole('button',{name:'重新读取整理材料',exact:true}).click();await panel(page).getByRole('alert').waitFor();assert.equal(await panel(page).getByRole('checkbox').count(),0);assert.equal(await panel(page).getByRole('button',{name:'确认完整季整理',exact:true}).count(),0);assert.equal(state.posts.length,0);
 });
 await scenario('unknown then 403 409 503 401 and refresh retain original key',async context=>{
  const page=await context.newPage();await open(page);await ready(page);await panel(page).getByRole('button',{name:'确认完整季整理',exact:true}).click();await retry(page).waitFor();
  for(const code of [403,409,503]){state.postCode=code;await retry(page).click();await panel(page).getByRole('alert').waitFor();await retry(page).waitFor();}
  state.postCode=401;const denied=page.waitForResponse(r=>r.request().method()==='POST'&&r.status()===401);await retry(page).click();await denied;await panel(page).waitFor({state:'detached'});
  state.postCode=503;await open(page);await retry(page).waitFor();await retry(page).click();await panel(page).getByRole('alert').waitFor();assert.equal(state.posts.length,6);assert.ok(state.posts.every(p=>JSON.stringify(p)===JSON.stringify(state.posts[0])));
  state.actor=actorB;await open(page);await panel(page).getByRole('checkbox').waitFor();assert.equal(await retry(page).count(),0);assert.equal(await panel(page).getByText(/原确认回执/).count(),0);assert.equal(state.posts.length,6);
 });
 await scenario('cross-tab submit serializes and second tab reads original receipt',async context=>{
  const a=await context.newPage(),b=await context.newPage();await open(a);await open(b);await ready(a);await ready(b);state.postCode=202;state.hold=true;
  await panel(a).getByRole('button',{name:'确认完整季整理',exact:true}).click();await a.waitForFunction(()=>Object.keys(localStorage).some(k=>k.startsWith('pulse.complete-tv-organization.v1:')));
  await panel(b).getByRole('button',{name:'确认完整季整理',exact:true}).click();await waitResolved(b,async()=>(await navigator.locks.query()).pending.some(l=>l.name.startsWith('pulse.complete-tv-organization.v1:')));
  assert.equal(state.posts.length,1);assert.ok(state.release);state.release();await panel(a).getByText(/原确认回执：已排队/).waitFor();await panel(b).getByText(/原确认回执：已排队/).waitFor();assert.equal(state.posts.length,1);state.actor=actorB;await open(b);await panel(b).getByRole('checkbox').waitFor();assert.equal(await panel(b).getByText(/原确认回执/).count(),0);
 });
 await scenario('ignored abort late preview cannot rebind operation and mobile stays usable',async context=>{
  await context.addInitScript(({op})=>{const original=window.fetch;window.fetch=async function(input,init){const request=new Request(input,init);if(request.url.endsWith('/operations/'+op+'/complete-tv-organizations')){const response=await original(new Request(request,{signal:new AbortController().signal}));await new Promise(resolve=>{window.releaseLatePreview=resolve;});return response;}return original(input,init);};},{op});
  const page=await context.newPage();await open(page);await page.waitForFunction(()=>typeof window.releaseLatePreview==='function');
  await page.getByRole('region',{name:'持久操作列表'}).getByRole('button',{name:/^Control Two/}).click();await panel(page).getByText('S01E02',{exact:true}).waitFor();await page.evaluate(()=>window.releaseLatePreview());await page.waitForFunction(()=>document.querySelector('.complete-tv-organization strong')?.textContent==='S01E02');assert.equal(await panel(page).getByText('S01E01',{exact:true}).count(),0);assert.equal(state.posts.length,0);
  await page.setViewportSize({width:390,height:844});assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),true);
 });
 await scenario('ignored abort late POST receipt cannot leak into new operation',async context=>{
  await context.addInitScript(()=>{const original=window.fetch;window.fetch=async function(input,init){const request=new Request(input,init);if(request.method==='POST'&&request.url.endsWith('/complete-tv-organizations')){const response=await original(new Request(request,{signal:new AbortController().signal}));await new Promise(resolve=>{window.releaseLatePost=resolve;});return response;}return original(input,init);};});
  const page=await context.newPage();await open(page);await ready(page);state.postCode=202;await panel(page).getByRole('button',{name:'确认完整季整理',exact:true}).click();await page.waitForFunction(()=>typeof window.releaseLatePost==='function');await page.getByRole('region',{name:'持久操作列表'}).getByRole('button',{name:/^Control Two/}).click();await panel(page).getByText('S01E02',{exact:true}).waitFor();await page.evaluate(()=>window.releaseLatePost());await page.waitForFunction(()=>document.querySelector('.complete-tv-organization strong')?.textContent==='S01E02');assert.equal(await panel(page).getByText(/原确认回执/).count(),0);assert.equal(state.posts.length,1);
 });
 await scenario('stream reset during original POST defers refresh and exits busy without resend',async context=>{
  const page=await context.newPage();await open(page);await ready(page);state.postCode=202;state.hold=true;
  const sent=page.waitForRequest(r=>r.method()==='POST'&&r.url().endsWith('/complete-tv-organizations'));await panel(page).getByRole('button',{name:'确认完整季整理',exact:true}).click();await sent;
  const refreshed=page.waitForResponse(r=>r.request().method()==='GET'&&r.url()===base+'/api/v1/operations/'+op);assert.ok(state.streams.size);for(const stream of state.streams)stream.write('event: reset\ndata: {}\n\n');await refreshed;
  assert.equal(state.posts.length,1);assert.ok(state.release);state.release();await panel(page).getByText(/原确认回执：已排队/).waitFor();await waitResolved(page,()=>!document.querySelector('.complete-tv-organization button').disabled);
  assert.equal(state.posts.length,1);assert.equal(await panel(page).getByRole('button',{name:'用原键恢复整理确认',exact:true}).count(),0);assert.equal(await panel(page).getByRole('checkbox').count(),0);
 });
 console.log(JSON.stringify({standalone:true,productAcceptance:false,results},null,2));
}finally{await browser.close();server.closeAllConnections();await new Promise(resolve=>server.close(resolve));}
