import test from 'node:test';
import assert from 'node:assert/strict';
import {readFile} from 'node:fs/promises';
import vm from 'node:vm';
import ts from 'typescript';

// Execute the production hook, with deterministic React hooks and clock. No browser,
// task environment, filesystem fixtures or database is required by the pure suite.
const source = await readFile(new URL('../src/features/configuration/useMovieDiscovery.ts', import.meta.url), 'utf8');
const compiled = ts.transpileModule(source, {compilerOptions:{module:ts.ModuleKind.CommonJS,target:ts.ScriptTarget.ES2022}}).outputText;
const status = (patch={}) => ({subscription_id:'sub-a',search_id:'search-a',current_file_id:'file-a',state:'succeeded',execution_allowed:false,...patch});
const snapshot = (patch={}) => ({search:{id:'search-a',subscription_id:'sub-a'},results:[{id:'candidate-a',title:'A movie'}],...patch});
function deferred(){let resolve;const promise=new Promise(r=>{resolve=r;});return {promise,resolve};}
function harness(respond=async path=>({data:path.includes('movie-discovery')?status():snapshot()})) {
  const slots=[], effects=[], timers=new Map(), calls=[];
  let cursor=0, dirty=false, value, subscription='sub-a', activity=0, now=0, timerId=0;
  const react={
    useState(initial){const i=cursor++;if(!(i in slots))slots[i]=typeof initial==='function'?initial():initial;return [slots[i],next=>{slots[i]=typeof next==='function'?next(slots[i]):next;dirty=true;}];},
    useRef(initial){const i=cursor++;return slots[i]??(slots[i]={current:initial});},
    useEffect(run,deps){const i=cursor++;if(!slots[i]||deps.some((d,n)=>d!==slots[i].deps[n]))effects.push(()=>{slots[i]?.cleanup?.();slots[i]={deps,cleanup:run()};});},
  };
  const exports={};
  vm.runInNewContext(compiled,{exports,require(name){if(name==='react')return react;if(name==='@/api/client')return {api:{GET:async(path,options)=>{calls.push({path,...options});return respond(path,options);},POST(){throw Error('unexpected POST');}}};if(name==='./useActivityInvalidation')return {useActivityInvalidation:()=>({version:activity})};throw Error(name);},AbortController,Date:{now:()=>now},setTimeout(fn,delay){const id=++timerId;timers.set(id,{fn,at:now+delay,delay});return id;},clearTimeout:id=>timers.delete(id)});
  function render(){cursor=0;dirty=false;value=exports.useMovieDiscovery(subscription);while(effects.length)effects.shift()();}
  async function flush(){for(let i=0;i<12;i++){await Promise.resolve();if(dirty)render();}}
  return {calls,timers,get value(){return value;},async mount(){render();await flush();},flush,async switchTo(id){subscription=id;render();await flush();},async invalidate(){activity++;render();await flush();},async advance(){assert.equal(timers.size,1);const [id,t]=timers.entries().next().value;timers.delete(id);now=t.at;t.fn();await flush();},async refresh(){value.refresh();await flush();},unmount(){for(const slot of slots)slot?.cleanup?.();}};
}
test('startup reads only discovery and its existing search, preserving candidate/file identity',async()=>{
  const h=harness();await h.mount();assert.equal(h.value.status.current_file_id,'file-a');assert.equal(h.value.snapshot.results[0].id,'candidate-a');assert.deepEqual(h.calls.map(c=>c.path),['/api/v1/subscriptions/{id}/movie-discovery','/api/v1/release-searches/{id}']);assert.equal(h.calls[1].params.path.id,'search-a');assert(h.calls.every(c=>c.cache==='no-store'&&c.signal instanceof AbortSignal));assert.equal(h.timers.size,0);h.unmount();
});
test('no search is fabricated when status lacks a file or search identity',async()=>{
  for(const patch of [{search_id:undefined},{current_file_id:undefined}]){const h=harness(async()=>({data:status(patch)}));await h.mount();assert.equal(h.calls.length,1);assert.equal(h.value.snapshot,null);h.unmount();}
});
test('running polling is serial, every two seconds, bounded to 36 seconds and read-only',async()=>{
  const h=harness(async path=>({data:path.includes('movie-discovery')?status({state:'running'}):snapshot()}));await h.mount();let ticks=0;while(h.timers.size){assert.equal([...h.timers.values()][0].delay,2000);assert(++ticks<=18);await h.advance();}assert.equal(ticks,18);assert.equal(h.calls.length,38);assert(h.calls.every(c=>c.path.includes('movie-discovery')||c.path.includes('release-searches')));h.unmount();
});
test('identity mismatch, executable response and search-scope mismatch clear all prior evidence',async()=>{
  for(const bad of [{data:status({subscription_id:'foreign'})},{data:status({execution_allowed:true})},{error:{status:403}}]){let invalid=false;const h=harness(async path=>invalid?bad:{data:path.includes('movie-discovery')?status():snapshot()});await h.mount();invalid=true;await h.refresh();assert.equal(h.value.status,null);assert.equal(h.value.snapshot,null);assert(h.value.error);h.unmount();}
  const h=harness(async path=>({data:path.includes('movie-discovery')?status():snapshot({search:{id:'search-a',subscription_id:'foreign'}})}));await h.mount();assert.equal(h.value.snapshot,null);assert(h.value.error);h.unmount();
});
test('subscription switch aborts in-flight status and ignores an abort-insensitive late response',async()=>{
  const pending=deferred();const h=harness(async(_path,o)=>o.params.path.id==='sub-a'?pending.promise:{data:status({subscription_id:'sub-b',search_id:undefined})});await h.mount();const signal=h.calls[0].signal;await h.switchTo('sub-b');assert(signal.aborted);pending.resolve({data:status()});await h.flush();assert.equal(h.value.status.subscription_id,'sub-b');assert.equal(h.calls.length,2);h.unmount();
});
test('invalidation aborts pending search and prevents its late candidate from returning',async()=>{
  const pending=deferred();let searches=0;const h=harness(async path=>path.includes('movie-discovery')?{data:status()}:++searches===1?pending.promise:{data:snapshot({results:[]})});await h.mount();const signal=h.calls[1].signal;await h.invalidate();assert(signal.aborted);pending.resolve({data:snapshot()});await h.flush();assert.deepEqual(h.value.snapshot.results,[]);h.unmount();
});
test('unmount aborts the active read and clears the next polling timer',async()=>{
  const h=harness(async path=>({data:path.includes('movie-discovery')?status({state:'running'}):snapshot()}));await h.mount();assert.equal(h.timers.size,1);h.unmount();assert.equal(h.timers.size,0);assert(h.calls.every(c=>c.signal.aborted));
});
