import {test} from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import {stripTypeScriptTypes} from 'node:module';
const root=new URL('../src/features/configuration/',import.meta.url);
const source=readFileSync(new URL('interactiveReleaseSearchRecovery.ts',root),'utf8');
const load=text=>import(`data:text/javascript;base64,${Buffer.from(stripTypeScriptTypes(text)).toString('base64')}#${crypto.randomUUID()}`);
const m=await load(source);
const uuid=n=>`00000000-0000-4000-8000-${String(n).padStart(12,'0')}`;
const userId=uuid(1),otherUser=uuid(2),key=uuid(3),searchId=uuid(4),subscriptionId=uuid(5);
const record={version:1,userId,scope:'episode',key,action:'create',subscriptionId};
const receipt={id:searchId,query:'private title',subscription_id:subscriptionId,created_at:'2026-10-18T00:00:00Z',deadline:'2026-10-18T00:00:30Z',source_count:2,ranking_policy_id:'default',ranking_policy_revision:1};
function storage(){const values=new Map();return {values,getItem:k=>values.get(k)??null,setItem(k,v){values.set(k,v)}};}
const never=async()=>assert.fail('unexpected transport');
test('only metadata is persisted, including receipt capture; query and remote material stay in memory',async()=>{
 const s=storage();m.saveRecovery(s,{...record,query:'PRIVATE',download_url:'https://secret',results:[{title:'SECRET'}],credentials:'PASSKEY'});
 const saved=JSON.parse(s.getItem(m.recoveryKey(userId,'episode')));
 assert.deepEqual(saved,record);
 await m.searchCommand({record,storage:s,fresh:true,lookup:never,send:async()=>({status:201,data:receipt})});
 const raw=[...s.values.values()].join('');assert(!/PRIVATE|private|secret|query|credentials|results|download_url/.test(raw));
 assert.equal(JSON.parse(raw).searchId,searchId);
});
test('malformed, extra fields, wrong account and wrong scope recovery fail closed',()=>{
 assert.equal(m.parseRecovery(null,userId,'episode'),null);
 for(const raw of ['{','null','[]','42',JSON.stringify({...record,action:'delete'}),JSON.stringify({...record,key:'bad'}),JSON.stringify({...record,searchId:null}),JSON.stringify({...record,action:'cancel'}),JSON.stringify({...record,query:'secret'}),JSON.stringify({...record,userId:otherUser}),JSON.stringify({...record,scope:'other'})])assert.throws(()=>m.parseRecovery(raw,userId,'episode'));
 assert.notEqual(m.recoveryKey(userId,'episode'),m.recoveryKey(otherUser,'episode'));
});
for(const action of ['create','cancel'])test(`lost ${action} receipt survives logout and reload without repeat POST`,async()=>{
 const s=storage(),r={...record,action,...(action==='cancel'?{searchId}: {})};let posts=0,lookups=0;
 const send=async()=>{posts++;assert.deepEqual(JSON.parse(s.getItem(m.recoveryKey(userId,'episode'))),r);throw Error('response lost after commit');};
 assert.equal((await m.searchCommand({record:r,storage:s,fresh:true,lookup:never,send})).kind,'unknown');
 const restored=m.parseRecovery(s.getItem(m.recoveryKey(userId,'episode')),userId,'episode');
 assert.equal((await m.searchCommand({record:restored,storage:s,lookup:async()=>({status:401}),send})).kind,'auth');
 const result=await m.searchCommand({record:restored,storage:s,lookup:async()=>{lookups++;return {status:200,data:receipt};},send});
 assert.equal(result.kind,'confirmed');assert.equal(result.record.key,key);assert.equal(result.record.action,action);assert.equal(posts,1);assert.equal(lookups,1);
});
test('404 while original request is in flight never causes automatic resubmission',async()=>{
 const s=storage();let release,posts=0;const original=m.searchCommand({record,storage:s,fresh:true,lookup:never,send:()=>{posts++;return new Promise(resolve=>{release=resolve});}});
 const restored=m.parseRecovery(s.getItem(m.recoveryKey(userId,'episode')),userId,'episode');
 assert.equal((await m.searchCommand({record:restored,storage:s,lookup:async()=>({status:404}),send:never})).kind,'missing');
 release({status:201,data:receipt});assert.equal((await original).kind,'confirmed');assert.equal(posts,1);
});
test('explicit original-key retry performs receipt GET first, then POST only after 404',async()=>{
 const s=storage(),calls=[];
 const result=await m.searchCommand({record,storage:s,retryMissing:true,lookup:async()=>{calls.push(['GET',key]);return {status:404};},send:async()=>{calls.push(['POST',key]);return {status:201,data:receipt};}});
 assert.equal(result.kind,'confirmed');assert.deepEqual(calls,[['GET',key],['POST',key]]);
 for(const status of [401,403,409,503]){
  const x=await m.searchCommand({record,storage:s,retryMissing:true,lookup:async()=>({status}),send:never});assert.notEqual(x.kind,'confirmed');
 }
});
test('stable success is captured after owner abort even if receipt persistence fails',async()=>{
 const s=storage();let release;const c=new AbortController();
 const pending=m.searchCommand({record,storage:s,fresh:true,lookup:never,send:()=>new Promise(resolve=>{release=resolve})});
 c.abort();s.setItem=()=>{throw Error('storage failed');};release({status:201,data:receipt});
 const result=await pending;assert.equal(c.signal.aborted,true);assert.equal(result.kind,'confirmed');assert.equal(result.persisted,false);assert.equal(result.record.searchId,searchId);
});
test('persistence failure prevents any initial POST; mismatched receipt cannot close a command',async()=>{
 const s=storage();s.setItem=()=>{throw Error('denied');};
 assert.equal((await m.searchCommand({record,storage:s,fresh:true,lookup:never,send:never})).kind,'not_sent');
 for(const data of [{...receipt,id:'bad'},{...receipt,subscription_id:uuid(90)},{...receipt,media_id:uuid(91)}])assert.equal((await m.searchCommand({record,storage:storage(),fresh:true,lookup:never,send:async()=>({status:201,data})})).kind,'unknown');
});
test('running empty, complete empty, failure and incomplete observations have distinct user messages',()=>{
 const base={search:receipt,results:[],sources:[],partial:false,truncated:false,total_candidates:0};
 const messages=['running','succeeded','failed','partial','timeout','cancelled'].map(state=>m.snapshotMessage({...base,state},'ready'));
 assert.equal(new Set(messages).size,messages.length);
 assert.match(m.snapshotMessage({...base,state:'succeeded',truncated:true},'ready'),/不完整|上限/);
 assert.match(m.snapshotMessage({...base,state:'running'},'stale'),/过期/);
 assert.equal(m.searchStateLabel('__proto__'),'状态待确认');
});

// Deterministic hook host: drives actual hook requests/effects, with injected auth,
// transport, timers and React slots. No browser, server or duplicated reducer.
async function hookHost(){
 const slots=[],effects=[],timers=new Map();let cursor=0,timerId=0,user={id:userId},out;
 const pending=[],calls=[];const s=storage();
 const depsEqual=(a,b)=>a&&b&&a.length===b.length&&a.every((v,i)=>Object.is(v,b[i]));
 const host={...m,sessionStorage:s,crypto,useAuth:()=>({user}),
  useRef(value){const i=cursor++;return slots[i]??(slots[i]={current:value});},
  useState(initial){const i=cursor++;if(!slots[i])slots[i]={value:typeof initial==='function'?initial():initial};return [slots[i].value,value=>{slots[i].value=typeof value==='function'?value(slots[i].value):value;}];},
  useEffect(fn,deps){const i=cursor++;const old=slots[i];if(!old||!depsEqual(old.deps,deps)){effects.push(()=>{old?.cleanup?.();slots[i]={deps,cleanup:fn()};});}},
  setTimeout(fn){const id=++timerId;timers.set(id,fn);return id;},clearTimeout(id){timers.delete(id);},
  api:{GET(path,options){calls.push({method:'GET',path,options});return new Promise((resolve,reject)=>pending.push({path,resolve,reject}));},POST(path,options){calls.push({method:'POST',path,options});return new Promise((resolve,reject)=>pending.push({path,resolve,reject}));}},
 };
 const name=`__search_test_${crypto.randomUUID().replaceAll('-','')}`;globalThis[name]=host;
 const hookSource=readFileSync(new URL('useInteractiveReleaseSearch.ts',root),'utf8').replace(/^import .*;\n/gm,'');
 const prelude=`const {useEffect,useRef,useState,api,useAuth,isUUID,parseRecovery,recoveryKey,searchCommand,searchRunning,searchStateLabel,snapshotMessage,sessionStorage,crypto,setTimeout,clearTimeout}=globalThis[${JSON.stringify(name)}];\n`;
 const {useInteractiveReleaseSearch}=await load(prelude+hookSource);delete globalThis[name];
 const render=()=>{cursor=0;out=useInteractiveReleaseSearch({scope:'test'});for(const effect of effects.splice(0))effect();return out;};
 const flush=async()=>{for(let i=0;i<12;i++)await Promise.resolve();return render();};
 render();await flush();
 return {calls,pending,timers,s,render,flush,get out(){return out;},auth(value){user=value;return render();},reply(status,data){assert(pending.length);pending.shift().resolve({response:{status},data,error:status>=400?{}:undefined});},fail(){pending.shift().reject(Error('offline'));},tick(){assert.equal(timers.size,1);const [id,fn]=timers.entries().next().value;timers.delete(id);fn();},unmount(){for(const slot of slots)slot?.cleanup?.();}};
}
const globalReceipt={...receipt,subscription_id:undefined};
const snapshot=(state,results=[])=>({search:globalReceipt,state,results,sources:[{name:'fast',state:'succeeded',partial:false}],partial:false,truncated:false,total_candidates:results.length});
test('actual hook reveals first results while slow source runs, schedules only after reply, retains stale results and clears on denial',async()=>{
 const h=await hookHost();try{
  const starting=h.out.start('private title');assert.equal(h.calls.at(-1).method,'POST');assert.equal(h.timers.size,0);
  h.reply(201,globalReceipt);await starting;await h.flush();assert.equal(h.calls.at(-1).path,'/api/v1/release-searches/{id}');
  const candidate={id:uuid(7),title:'first',seeders:8};h.reply(200,snapshot('running',[candidate]));await h.flush();
  assert.deepEqual(h.out.results,[candidate]);assert.equal(h.out.loading,true);assert.equal(h.out.canAcquire,true);assert.equal(h.timers.size,1);
  h.tick();assert.equal(h.timers.size,0);assert.equal(h.pending.length,1);h.fail();await h.flush();
  assert.deepEqual(h.out.results,[candidate]);assert.equal(h.out.phase,'stale');assert.equal(h.out.canAcquire,false);assert.equal(h.timers.size,0);
  h.out.refresh();h.reply(403);await h.flush();assert.deepEqual(h.out.results,[]);assert.equal(h.out.phase,'auth');
  const deniedCalls=h.calls.length;h.out.refresh();assert.equal(h.calls.length,deniedCalls);
 }finally{h.unmount();}
});
test('actual hook fences late snapshots on account switch, recovers original create through GET across relogin and never cancels other account',async()=>{
 const h=await hookHost();try{
  const starting=h.out.start('private title');h.reply(201,globalReceipt);await starting;await h.flush();
  h.auth({id:otherUser});assert.deepEqual(h.out.results,[]);
  h.reply(200,snapshot('running',[{id:uuid(9),title:'old private result'}]));await h.flush();assert.deepEqual(h.out.results,[]);assert.equal(h.timers.size,0);
  h.auth({id:userId});await h.flush();assert.equal(h.calls.at(-1).path,'/api/v1/release-search-commands/{key}');
  h.reply(200,globalReceipt);await h.flush();h.reply(200,snapshot('succeeded'));await h.flush();assert.equal(h.out.phase,'ready');
  assert.equal(h.calls.filter(c=>c.method==='POST').length,1);assert.equal(h.calls.some(c=>c.path.endsWith('/cancel')),false);
 }finally{h.unmount();}
});
test('actual hook keeps unknown cancel key and reload recovery reads the same command without new POST',async()=>{
 const h=await hookHost();try{
  const starting=h.out.start('private title');h.reply(201,globalReceipt);await starting;await h.flush();h.reply(200,snapshot('running'));await h.flush();
  const cancelling=h.out.cancel();const cancelKey=h.calls.at(-1).options.params.header['Idempotency-Key'];h.fail();await cancelling;await h.flush();
  assert.equal(h.out.phase,'unknown');assert.equal(h.out.recovery.action,'cancel');assert.equal(h.out.canAcquire,false);
  const recovering=h.out.retryRecovery();assert.equal(h.calls.at(-1).options.params.path.key,cancelKey);assert.equal(h.calls.at(-1).options.params.query.action,'cancel');
  h.reply(200,{...globalReceipt,cancelled_at:receipt.created_at});await recovering;await h.flush();h.reply(200,snapshot('cancelled'));await h.flush();
  assert.equal(h.out.phase,'ready');assert.equal(h.out.canAcquire,false);assert.equal(h.calls.filter(c=>c.method==='POST').length,2);
 }finally{h.unmount();}
});
test('unknown create after relogin with no original input permits receipt lookup only, even after 404',async()=>{
 const h=await hookHost();try{
  const starting=h.out.start('original private title');const originalKey=h.calls.at(-1).options.params.header['Idempotency-Key'];h.fail();await starting;await h.flush();
  h.auth(null);h.auth({id:userId});await h.flush();assert.equal(h.calls.at(-1).options.params.path.key,originalKey);h.reply(404);await h.flush();
  assert.equal(h.out.phase,'missing');assert.equal(h.out.canRetryOriginal,false);
  const retry=h.out.retryOriginal();h.reply(404);await retry;await h.flush();assert.equal(h.calls.filter(c=>c.method==='POST').length,1);
  const next=h.out.start('different private title');assert.equal(h.calls.at(-1).method,'GET');h.reply(404);await next;await h.flush();assert.equal(h.calls.filter(c=>c.method==='POST').length,1);
 }finally{h.unmount();}
});
test('explicit retry uses original semantic input and original key, after another receipt lookup',async()=>{
 const h=await hookHost();try{
  const starting=h.out.start('original title');const first=h.calls.at(-1);h.fail();await starting;await h.flush();
  const lookup=h.out.retryRecovery();h.reply(404);await lookup;await h.flush();assert.equal(h.out.canRetryOriginal,true);
  const retry=h.out.retryOriginal();assert.equal(h.calls.at(-1).method,'GET');h.reply(404);await h.flush();
  const second=h.calls.at(-1);assert.equal(second.method,'POST');assert.deepEqual(second.options.body,first.options.body);assert.equal(second.options.params.header['Idempotency-Key'],first.options.params.header['Idempotency-Key']);
  h.reply(201,globalReceipt);await retry;await h.flush();h.reply(200,snapshot('succeeded'));await h.flush();
 }finally{h.unmount();}
});
