import {test} from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import ts from 'typescript';
import React from 'react';

async function host(file, props={}) {
 const slots=[],effects=[],calls=[];let cursor=0,tree,user={id:'a',role:'admin'};
 const equal=(a,b)=>a&&b&&a.length===b.length&&a.every((v,i)=>Object.is(v,b[i]));
 const request=method=>(path,options)=>new Promise((resolve,reject)=>calls.push({method,path,options,resolve,reject,settled:false}));
 const h={React,api:{GET:request('GET'),PUT:request('PUT'),POST:request('POST')},useAuth:()=>({user}),
 useRef(v){const i=cursor++;return slots[i]??={current:v};},
 useState(v){const i=cursor++;if(!(i in slots))slots[i]=typeof v==='function'?v():v;return [slots[i],next=>{slots[i]=typeof next==='function'?next(slots[i]):next;}];},
 useCallback(fn,deps){const i=cursor++;if(!equal(slots[i]?.deps,deps))slots[i]={deps,value:fn};return slots[i].value;},
 useEffect(fn,deps){const i=cursor++,old=slots[i];if(!equal(old?.deps,deps))effects.push(()=>{old?.cleanup?.();slots[i]={deps,cleanup:fn()};});}};
 const source=readFileSync(new URL(`../src/features/configuration/${file}.${file.startsWith("use")?"ts":"tsx"}`,import.meta.url),'utf8');const ast=ts.createSourceFile('component.tsx',source,ts.ScriptTarget.Latest,true,ts.ScriptKind.TSX);let clean=source;for(const node of [...ast.statements.filter(ts.isImportDeclaration)].reverse())clean=clean.slice(0,node.getFullStart())+clean.slice(node.end);
 const name=`__classification_${crypto.randomUUID().replaceAll('-','')}`;globalThis[name]=h;let component;try{const compiled=ts.transpileModule(`const {${Object.keys(h).join(',')}}=globalThis[${JSON.stringify(name)}];\n${clean}`,{compilerOptions:{target:ts.ScriptTarget.ES2022,module:ts.ModuleKind.ESNext,jsx:ts.JsxEmit.React}}).outputText;component=(await import(`data:text/javascript;base64,${Buffer.from(compiled).toString('base64')}`))[file];}finally{delete globalThis[name];}
 const render=()=>{cursor=0;tree=component(props);for(const effect of effects.splice(0))effect();return tree;};
 const flatten=node=>Array.isArray(node)?node.flatMap(flatten):node&&typeof node==='object'&&node.props?[node,...flatten(node.props.children)]:[];
 const flush=async()=>{for(let i=0;i<20;i++)await Promise.resolve();render();};
 return {calls,render,flush,get tree(){return tree;},find(predicate){const node=flatten(tree).find(predicate);assert(node,'element exists');return node;},auth(next){user=next;render();},reply(index,status,data){const c=calls[index];assert(c&&!c.settled);c.settled=true;c.resolve({response:{status},data,error:status>=400?{message:'unsafe-secret'}:undefined});},fail(index){const c=calls[index];c.settled=true;c.reject(Error('unsafe-secret'));},async close(){for(const slot of slots)slot?.cleanup?.();for(const c of calls)if(!c.settled){c.settled=true;c.reject(Error('cleanup'));}await flush();}};
}
test('classification target requires current published preview and expires before creating',async()=>{
 const args={enabled:true,userID:'a',mediaType:'movie',tmdbID:91};const h=await host('useClassificationTarget',args);const originalNow=Date.now;try{h.render();const root={id:'root',name:'电影库',media_type:'movie',health:'online'};h.reply(0,200,{version:1,rules:[]});h.reply(1,200,{items:[root]});await h.flush();h.reply(2,200,{version:1,observed_at:new Date().toISOString(),preview_stamp:"a".repeat(64),result:{status:'matched',root_id:'root',matches:[{rule_id:'剧情'}]}});await h.flush();h.reply(3,200,{version:2,rules:[]});await h.flush();assert.equal(h.tree.ready,false);assert.equal(h.tree.getSelection()?.version,undefined);
 h.tree.refresh();h.render();h.reply(4,200,{version:2,rules:[]});h.reply(5,200,{items:[root]});await h.flush();h.reply(6,200,{version:2,observed_at:new Date().toISOString(),preview_stamp:"a".repeat(64),result:{status:'matched',root_id:'root',matches:[{rule_id:'剧情'}]}});await h.flush();h.reply(7,200,{version:2,rules:[]});await h.flush();assert.equal(h.tree.ready,true);assert.equal(h.tree.getSelection()?.version,2);const deadline=h.tree.expiresAt;Date.now=()=>deadline+1;assert.equal(h.tree.getSelection()?.version,undefined);h.render();assert.equal(h.tree.ready,false);assert(h.calls.every(c=>c.method==='GET'||c.path==='/api/v1/classification/previews'));
 args.userID='b';h.render();assert.equal(h.tree.ready,false);
 }finally{Date.now=originalNow;await h.close();}
});

const report={subscription_id:'s',observed_at:'2026-01-01T00:00:00Z',waiting:'no_round',next_step:'wait_for_next_evaluation',limited:false,rounds:[]};
test('execution panel only reads persisted snapshots and discards late account results',async()=>{
 const h=await host('SubscriptionExecutionPanel',{subscriptionID:'s'});try{h.render();assert.equal(h.calls.length,1);h.auth({id:'b',role:'member'});assert(h.calls[0].options.signal.aborted);h.reply(0,200,{...report,waiting:'unsafe-secret'});await h.flush();assert.equal(h.calls.length,2);h.reply(1,200,report);await h.flush();h.find(n=>n.type==='button').props.onClick();h.render();assert.equal(h.calls.length,3);h.reply(2,503);await h.flush();assert(h.find(n=>n.props.role==='alert'));assert(h.calls.every(c=>c.method==='GET'));}finally{await h.close();}
});
test('classification publication keeps original key after lost response and previews only saved version',async()=>{
 const storage=new Map();const oldStorage=globalThis.localStorage,oldDocument=globalThis.document;globalThis.localStorage={getItem:k=>storage.get(k)??null,setItem:(k,v)=>storage.set(k,v),removeItem:k=>storage.delete(k)};globalThis.document={cookie:'csrf_token=fixture'};
 const h=await host('ClassificationSettingsPanel');try{h.render();h.reply(0,200,{version:0,rules:[]});h.reply(1,200,{items:[]});await h.flush();const submit=()=>h.find(n=>n.type==='form').props.onSubmit({preventDefault(){}});const first=submit();await h.flush();assert.equal(h.calls[2].method,'PUT');const original=structuredClone(h.calls[2].options);h.fail(2);await first;await h.flush();assert.equal(storage.size,1);
 const retry=h.find(n=>n.type==='button'&&n.props.children==='核对原发布').props.onClick();await h.flush();assert.equal(h.calls[3].options.params.header['Idempotency-Key'],original.params.header['Idempotency-Key']);assert.deepEqual(h.calls[3].options.body,original.body);h.reply(3,200,{version:1,rules:[]});await h.flush();h.reply(4,200,{version:1,rules:[]});h.reply(5,200,{items:[]});await retry;await h.flush();assert.equal(storage.size,0);
 h.find(n=>n.props.id==='class-preview-media').props.onChange({target:{value:'media-uuid'}});h.render();const preview=h.find(n=>n.type==='button'&&n.props.children==='预览分类').props.onClick();await h.flush();assert.equal(h.calls[6].method,'POST');assert.deepEqual(h.calls[6].options.body,{version:1,media_id:'media-uuid'});h.reply(6,200,{version:1,observed_at:report.observed_at,result:{status:'insufficient_evidence',matches:[],reasons:['trusted_metadata_missing']}});await preview;await h.flush();assert.equal(h.calls.filter(c=>c.method==='PUT').length,2);
 }finally{await h.close();globalThis.localStorage=oldStorage;globalThis.document=oldDocument;}
});
