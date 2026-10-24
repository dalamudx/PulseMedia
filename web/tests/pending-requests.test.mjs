import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import ts from 'typescript';
const source = path => readFileSync(new URL('../src/' + path, import.meta.url), 'utf8');
const tick = async () => { for (let i=0;i<24;i++) await Promise.resolve(); };
async function inject(text, values) {
 const key='__inbox_'+crypto.randomUUID().replaceAll('-','');globalThis[key]=values;
 const code=`const {${Object.keys(values).join(',')}}=globalThis[${JSON.stringify(key)}];\n`+text.replace(/^import[\s\S]*?;\n/gm,'');
 try { return await import('data:text/javascript;base64,'+Buffer.from(ts.transpileModule(code,{compilerOptions:{target:ts.ScriptTarget.ES2022,module:ts.ModuleKind.ESNext,jsx:ts.JsxEmit.React,jsxFactory:'h'}}).outputText).toString('base64')+'#'+crypto.randomUUID()); }
 finally {delete globalThis[key];}
}
const module = await inject(source('features/dashboard/usePendingRequests.ts'),{});
const ids = n => `00000000-0000-4000-8000-${String(n).padStart(12,'0')}`;
const A=ids(1),B=ids(2),stamp='2026-10-22T12:00:00Z';
const summary = count => ({pending_count:count,observed_at:stamp});
async function host(initial={}, requests=false) {
 const slots=[],effects=new Map(),calls=[],timers=new Map(),listeners=new Map();let cursor=0,serial=0,now=0,updates=0,out,props={userId:A,role:'admin',...initial};
 const equal=(a,b)=>a&&b&&a.length===b.length&&a.every((v,i)=>Object.is(v,b[i]));
 const eventTarget={addEventListener(k,fn){if(!listeners.has(k))listeners.set(k,new Set());listeners.get(k).add(fn);},removeEventListener(k,fn){listeners.get(k)?.delete(fn);}};
 const document={...eventTarget,visibilityState:'visible'};
 const api=Object.fromEntries(['GET','POST'].map(method=>[method,(path,options)=>new Promise((resolve,reject)=>{const call={method,path,options,settled:false,reply(data,status=200){assert(!call.settled);call.settled=true;resolve({data:status<400?data:undefined,response:{status,ok:status<400}});},fail(){if(!call.settled){call.settled=true;reject(Error('owned transport failure'));}}};calls.push(call);})]));
 const timer=(fn,ms,repeat)=>{const id=++serial;timers.set(id,{fn,ms,repeat,next:now+ms});return id;};
 const react={
  useRef(value){const i=cursor++;return slots[i]??(slots[i]={current:value});},
  useState(value){const i=cursor++;slots[i]??={value:typeof value==='function'?value():value};return [slots[i].value,next=>{updates++;slots[i].value=typeof next==='function'?next(slots[i].value):next;}];},
  useMemo(fn,deps){const i=cursor++,old=slots[i];if(!old||!equal(old.deps,deps))slots[i]={value:fn(),deps};return slots[i].value;},
  useCallback(fn,deps){return react.useMemo(()=>fn,deps);},
  useEffect(fn,deps){const i=cursor++,old=slots[i];if(!old||!equal(old.deps,deps))effects.set(i,()=>{old?.cleanup?.();slots[i]={deps,cleanup:fn()};});},
 };
 const journal=requests?await inject(source('features/media-requests/commandJournal.ts'),{}):{};
 const file=requests?'features/media-requests/useMediaRequests.ts':'features/dashboard/usePendingRequests.ts';
 const compiled=await inject(source(file),{...react,...journal,api,document,window:eventTarget,localStorage:{getItem(){return null;}},navigator:{locks:{request:async(_key,_options,fn)=>fn()}},performance:{now:()=>now},setTimeout:(fn,ms)=>timer(fn,ms,false),setInterval:(fn,ms)=>timer(fn,ms,true),clearTimeout:id=>timers.delete(id),clearInterval:id=>timers.delete(id)});
 const commit=()=>{const pending=[...effects.values()];effects.clear();for(const fn of pending)fn();};
 const render=(patch={},effectsNow=true)=>{props={...props,...patch};cursor=0;out=(requests?compiled.useMediaRequests:compiled.usePendingRequests)(props);if(effectsNow)commit();return out;};
 const flush=async()=>{await tick();return render();};
 const unmount=()=>{for(const slot of slots)slot?.cleanup?.();};
 render();await flush();
 return {calls,timers,render,flush,commit,unmount,get out(){return out;},get updates(){return updates;},
  advance(ms){now+=ms;for(const [id,v]of [...timers].sort((a,b)=>a[1].next-b[1].next)){if(!timers.has(id)||v.next>now)continue;if(v.repeat)v.next=now+v.ms;else timers.delete(id);v.fn();}return render();},
  visible(value){document.visibilityState=value?'visible':'hidden';for(const fn of listeners.get('visibilitychange')??[])fn();return render();},
  async dispose(){unmount();for(const call of calls)call.fail();await tick();assert.equal(timers.size,0);for(const group of listeners.values())assert.equal(group.size,0);},
 };
}
const listCall=h=>h.calls.find(c=>c.path==='/api/v1/media-requests');
test('exact inbox boundaries distinguish zero, decimal precision and malformed observations',()=>{
 for(const count of ['0','9007199254740993','9223372036854775807'])assert(module.validInboxSummary(summary(count)));
 for(const count of [0,null,'','00','01','-1','+1','1.0','1e3','9223372036854775808'])assert.equal(module.validInboxSummary(summary(count)),false);
 assert.equal(module.validInboxSummary({...summary('1'),observed_at:'2026'}),false);
});
test('actual admin Hook makes one no-store GET and preserves known zero',async()=>{const h=await host();try{assert.equal(h.out.count,null);assert.equal(h.calls.length,1);assert.equal(h.calls[0].method,'GET');assert.equal(h.calls[0].options.cache,'no-store');h.calls[0].reply(summary('0'));await h.flush();assert.equal(h.out.count,'0');assert.equal(h.out.error,'');}finally{await h.dispose();}});
test('members neither read a global inbox nor display an old administrator count',async()=>{const h=await host({role:'member'});try{assert.equal(h.calls.length,0);assert.equal(h.out.count,null);}finally{await h.dispose();}});
test('A-B-A responses are fenced before effect cleanup and do not restore old counts',async()=>{const h=await host();try{const first=h.calls[0];h.render({userId:B},false);h.render({userId:A},false);first.reply(summary('99'));await tick();h.commit();await h.flush();assert.equal(h.out.count,null);const latest=h.calls.at(-1);assert.notEqual(latest,first);latest.reply(summary('3'));await h.flush();assert.equal(h.out.count,'3');assert.equal(h.render({role:'member'},false).count,null);}finally{await h.dispose();}});
test('hidden pages abort and stop polling; expired counts stay unknown on resume',async()=>{const h=await host();try{h.calls[0].reply(summary('9'));await h.flush();h.visible(false);const count=h.calls.length;h.advance(60000);assert.equal(h.calls.length,count);h.visible(true);assert.equal(h.out.count,null);assert.equal(h.out.expired,true);h.calls.at(-1).reply(summary('0'));await h.flush();assert.equal(h.out.count,'0');}finally{await h.dispose();}});
test('a 403 clears old success, blocks automatic polling, and retains explicit read recovery',async()=>{const h=await host();try{h.calls[0].reply(summary('4'));await h.flush();const work=h.out.refresh();h.calls.at(-1).reply(undefined,403);await work;await h.flush();assert.equal(h.out.count,null);assert.match(h.out.error,/无权/);const count=h.calls.length;h.advance(30000);assert.equal(h.calls.length,count);const retry=h.out.refresh();h.calls.at(-1).reply(summary('2'));await retry;await h.flush();assert.equal(h.out.count,'2');}finally{await h.dispose();}});
test('malformed and failed attempts cannot reuse the previous count',async()=>{const h=await host();try{h.calls[0].reply(summary('8'));await h.flush();for(const data of [summary('01'),undefined]){const work=h.out.refresh();h.calls.at(-1).reply(data,data?200:503);await work;await h.flush();assert.equal(h.out.count,null);assert.match(h.out.error,/读取失败/);}}finally{await h.dispose();}});
test('timeout and cleanup ignore transport callbacks after their cancellation',async()=>{const h=await host();try{const first=h.calls[0];h.advance(10000);assert(first.options.signal.aborted);assert.equal(h.out.count,null);first.reply(summary('7'));await h.flush();assert.equal(h.out.count,null);void h.out.refresh();const last=h.calls.at(-1);h.unmount();const changes=h.updates;last.reply(summary('6'));await tick();assert.equal(h.updates,changes);assert(last.options.signal.aborted);}finally{await h.dispose();}});
test('filtered administrator reads use server pending state and reject terminal rows',async()=>{const h=await host({pendingOnly:true},true);try{const call=listCall(h);assert.deepEqual(call.options.params.query,{limit:25,state:'pending'});const row={id:ids(3),owner_id:B,intent:{media_type:'movie',tmdb_id:123,seasons:[],include_specials:false},state:'rejected',version:2,created_at:stamp,updated_at:stamp,reason:'reviewed',decided_by:A,decided_at:stamp};call.reply({items:[row]});await h.flush();assert.equal(h.out.items.length,0);assert.match(h.out.listError,/读取失败/);assert(h.calls.every(c=>c.method==='GET'));}finally{await h.dispose();}});
test('a member cannot request the admin filter through Hook options',async()=>{const h=await host({role:'member',pendingOnly:true},true);try{assert.deepEqual(listCall(h).options.params.query,{limit:25});listCall(h).reply({items:[]});await h.flush();assert.equal(h.out.loaded,true);assert.equal(h.out.items.length,0);}finally{await h.dispose();}});
test('actual panel links only to the pending queue and clearly separates unknown and zero',async()=>{
 const h=(type,props,...children)=>typeof type==='function'?type({...props,children}):({type,props:props??{},children});
 const flat=node=>node==null||node===false?[]:Array.isArray(node)?node.flatMap(flat):typeof node==='object'?[node,...flat(node.children)]:[node];
 let data={count:'0',busy:false,error:'',expired:false,refresh:async()=>true};
 const view=await inject(source('features/dashboard/PendingRequestsPanel.tsx'),{h,Link:({to,children})=>h('a',{href:to},children),usePendingRequests:()=>data});
 let tree=flat(view.PendingRequestsPanel({userId:A,role:'admin'}));assert(tree.some(n=>n?.props?.href==='/requests?state=pending'));assert(tree.includes('0'));assert(tree.includes('目前没有待审批请求。'));
 data={...data,count:null,error:'读取失败'};tree=flat(view.PendingRequestsPanel({userId:A,role:'admin'}));assert(tree.includes('—'));assert(!tree.includes('0'));assert.equal(view.PendingRequestsPanel({userId:A,role:'member'}),null);
});
