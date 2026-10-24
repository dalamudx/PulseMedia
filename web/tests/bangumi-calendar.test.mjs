import {test} from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import ts from 'typescript';
const root=new URL('../src/features/configuration/',import.meta.url);
const load=text=>import(`data:text/javascript;base64,${Buffer.from(ts.transpileModule(text,{compilerOptions:{target:ts.ScriptTarget.ES2022,module:ts.ModuleKind.ESNext}}).outputText).toString('base64')}#${crypto.randomUUID()}`);
const model=await load(readFileSync(new URL('bangumiCalendar.ts',root),'utf8'));
const {discoveryMediaItem}=await load(readFileSync(new URL('discoveryCatalog.ts',root),'utf8'));
const item={bangumi_id:'42',title:'葬送的芙莉莲',original_title:'葬送のフリーレン',first_air_date:'2023-09-29',poster_url:null,rating:null};
const calendar={execution_allowed:false,observed_at:'2026-10-23T00:00:00Z',days:Array.from({length:7},(_,i)=>({weekday:i+1,name:`day ${i}`,items:i===0?[item]:[]}))};
const tick=async()=>{for(let i=0;i<16;i++)await Promise.resolve();};
async function host(){
 let cursor=0,out,updates=0;const slots=[],effects=[],calls=[];
 const equal=(a,b)=>a&&b&&a.length===b.length&&a.every((x,i)=>Object.is(x,b[i]));
 const env={...model,discoveryMediaItem,
 useRef(initial){const i=cursor++;return slots[i]??(slots[i]={current:initial});},
 useState(initial){const i=cursor++;if(!slots[i])slots[i]={value:typeof initial==='function'?initial():initial};return [slots[i].value,next=>{updates++;slots[i].value=typeof next==='function'?next(slots[i].value):next;}];},
 useEffect(fn,deps){const i=cursor++,old=slots[i];if(!old||!equal(old.deps,deps))effects.push(()=>{old?.cleanup?.();slots[i]={deps,cleanup:fn()};});},
 api:{GET(path,options){const call={path,options};calls.push(call);return new Promise((resolve,reject)=>{call.reply=resolve;call.fail=()=>reject(Error('owned network failure'));});}},
 };
 const key='__bangumi_'+crypto.randomUUID().replaceAll('-','');globalThis[key]=env;let useBangumiCalendar;
 try{const source=readFileSync(new URL('useBangumiCalendar.ts',root),'utf8').replace(/^import[\s\S]*?;\n/gm,'');({useBangumiCalendar}=await load(`const {${Object.keys(env).join(',')}}=globalThis[${JSON.stringify(key)}];\n${source}`));}finally{delete globalThis[key];}
 const render=()=>{cursor=0;out=useBangumiCalendar();for(const effect of effects.splice(0))effect();return out;};
 const flush=async()=>{await tick();return render();};render();await flush();
 return {calls,flush,get out(){return out;},get updates(){return updates;},async dispose(){for(const slot of slots)slot?.cleanup?.();await tick();}};
}
async function ready(){const h=await host();h.calls[0].reply({data:calendar});await h.flush();return h;}
test('Bangumi bilingual NFKC matching preserves unknown dates and source identity',()=>{
 for(const query of ['芙莉莲','フリーレン',''])assert.equal(model.matchesBangumiName(item,query),true);
 assert.equal(model.matchesBangumiName({...item,title:'ＡＢＣ'},'abc'),true);assert.equal(model.matchesBangumiName(item,'other'),false);
 assert.equal(model.bangumiYear(item),2023);assert.equal(model.bangumiYear({...item,first_air_date:null}),undefined);
});
test('poster allowlist rejects foreign URLs and private query values',()=>{
 assert.equal(model.bangumiPoster('https://lain.bgm.tv/pic/cover/owned.jpg'),'https://lain.bgm.tv/pic/cover/owned.jpg');
 for(const url of ['http://lain.bgm.tv/a','https://evil.example/a','https://lain.bgm.tv/a?key=owned','https://lain.bgm.tv/a#key','https://lain.bgm.tv.evil/a',null])assert.equal(model.bangumiPoster(url),undefined);
});
test('calendar mount is a single read and explicit chase reads only TV candidates',async()=>{
 const h=await ready();try{assert.equal(h.calls.length,1);assert.equal(h.calls[0].path,'/api/v1/metadata/bangumi/calendar');assert.equal(h.out.calendar.execution_allowed,false);
 const pending=h.out.match(item);assert.equal(h.calls[1].path,'/api/v1/search/movies');assert.deepEqual(h.calls[1].options.params.query,{query:item.original_title,type:'tv',year:2023});
 h.calls[1].reply({data:{results:[{tmdb_id:7,title:'TV',media_type:'tv'},{tmdb_id:8,title:'Film',media_type:'movie'},{tmdb_id:0,title:'Bad',media_type:'tv'},{tmdb_id:7,title:'Repeat',media_type:'tv'}]}});await pending;await h.flush();assert.deepEqual(h.out.candidates.map(x=>x.tmdb_id),[7]);assert.equal(h.out.selected.bangumi_id,item.bangumi_id);
 }finally{await h.dispose();}
});
test('unknown premiere year is omitted and empty candidate success remains distinct from failure',async()=>{
 const h=await ready();try{const pending=h.out.match({...item,first_air_date:null});assert.deepEqual(h.calls[1].options.params.query,{query:item.original_title,type:'tv'});h.calls[1].reply({data:{results:[]}});await pending;await h.flush();assert.deepEqual(h.out.candidates,[]);assert.equal(h.out.matchError,'');
 const again=h.out.match(item);h.calls[2].fail();await again;await h.flush();assert.match(h.out.matchError,/未提交订阅/);assert.equal(h.out.matching,false);
 }finally{await h.dispose();}
});
test('clearing selection aborts the old lookup and ignores a late callback',async()=>{
 const h=await ready();try{const pending=h.out.match(item);h.out.clearMatch();await h.flush();assert.equal(h.calls[1].options.signal.aborted,true);h.calls[1].reply({data:{results:[{tmdb_id:7,title:'Old',media_type:'tv'}]}});await pending;await h.flush();assert.equal(h.out.selected,null);assert.deepEqual(h.out.candidates,[]);}finally{await h.dispose();}
});
test('failed refresh withdraws old data and prevents another candidate request',async()=>{
 const h=await ready();try{const pending=h.out.refresh();await h.flush();assert.equal(h.out.calendar,null);h.calls[1].reply({error:{code:'unavailable'}});await pending;await h.flush();assert.match(h.out.error,/失败/);await h.out.match(item);assert.equal(h.calls.length,2);assert.equal(h.out.selected,null);}finally{await h.dispose();}
});
test('unmount aborts both metadata and candidate reads without late state writes',async()=>{
 const h=await host();await h.dispose();const count=h.updates;assert.equal(h.calls[0].options.signal.aborted,true);h.calls[0].reply({data:calendar});await tick();assert.equal(h.updates,count);
 const second=await ready();const pending=second.out.match(item);await second.dispose();const updates=second.updates;assert.equal(second.calls[1].options.signal.aborted,true);second.calls[1].reply({data:{results:[]}});await pending;assert.equal(second.updates,updates);
});
