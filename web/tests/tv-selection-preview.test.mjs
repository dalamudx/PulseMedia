import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import ts from 'typescript';
const src = readFileSync(new URL('../src/features/configuration/tvSelectionPreview.ts', import.meta.url), 'utf8');
const js = ts.transpileModule(src, {compilerOptions:{target:ts.ScriptTarget.ES2022,module:ts.ModuleKind.ESNext}}).outputText;
const m = await import(`data:text/javascript;base64,${Buffer.from(js).toString('base64')}`);
const id = n => `00000000-0000-4000-8000-${String(n).padStart(12,'0')}`;
const scope={userId:id(1),subscriptionId:id(2),signature:'intent1'};
const receipt=(n=3)=>({id:id(n),candidate_id:id(n+100),subscription_id:scope.subscriptionId,status:'reviewed_not_executable'});
const alt=(n=3,kind='episode',keys=['S02E03'])=>m.reviewAlternative(scope,kind,receipt(n),keys,'固定独立夹具资源');
const req=()=>m.selectionRequest(scope,[alt()],[id(3)],'2','none','0.5');
test('all four explicit modes preserve IDs and ratio without UI evidence or command fields',()=>{
 for(const [mode] of m.selectionModes){const body=m.selectionRequest(scope,[alt(),alt(4,'pack',['S02E03','S02E04'])],[id(3),id(4)],'2',mode,'0.5');assert.deepEqual(Object.keys(body).sort(),['episodes','pack_policy','packs','season']);assert.deepEqual(body.episodes,[{candidate_id:id(103),review_id:id(3)}]);assert.deepEqual(body.packs,[{candidate_id:id(104),review_id:id(4)}]);assert.deepEqual(body.pack_policy,{mode,min_missing_ratio:0.5})}
});
for(const [field,value] of [['season',''],['season','0'],['season','1000'],['season','2.5'],['mode',''],['mode','auto'],['ratio',''],['ratio','NaN'],['ratio','Infinity'],['ratio','-0.1'],['ratio','1.1']]) test(`reject invalid explicit ${field}=${value}`,()=>{
 const fields={season:'2',mode:'none',ratio:'0.5',[field]:value};assert.equal(m.selectionRequest(scope,[alt()],[id(3)],fields.season,fields.mode,fields.ratio),null);
});
for(const field of ['userId','subscriptionId','signature']) test(`references cannot cross ${field}`,()=>{
 assert.equal(m.selectionRequest({...scope,[field]:'other'},[alt()],[id(3)],'2','none','0.5'),null);
});
test('reject stale/unknown review reference, wrong season and duplicates',()=>{
 for(const ids of [[id(8)],[id(3),id(3)],[]])assert.equal(m.selectionRequest(scope,[alt()],ids,'2','none','0.5'),null);
 assert.equal(m.selectionRequest(scope,[alt()],[id(3)],'1','none','0.5'),null);
});
test('review admission requires receipt scope and exact readable normal-season unit labels',()=>{
 for(const keys of [[],['S00E01'],['S02E00'],['S02E03','S03E04'],['S02E03','S02E03'],['invented']])assert.equal(alt(3,'pack',keys),null);
 assert.equal(alt(3,'episode',['S02E03','S02E04']),null);
 assert.equal(m.reviewAlternative(scope,'pack',{...receipt(),subscription_id:id(9)},['S02E03'],'x'),null);
 assert.equal(m.reviewAlternative(scope,'pack',{...receipt(),status:'queued'},['S02E03'],'x'),null);
});
test('collection deduplicates, replaces candidate reviews, bounds at 64 and drops old scope',()=>{
 let rows=[];for(let i=3;i<75;i++)rows=m.collectReview(rows,alt(i));assert.equal(rows.length,64);
 assert.equal(m.collectReview(rows,rows[0]),rows);
 const changed={...rows[0],reviewId:id(999)};rows=m.collectReview(rows,changed);assert.equal(rows.length,64);assert.equal(rows.at(-1).reviewId,id(999));
 assert.deepEqual(m.collectReview(rows,{...alt(),userId:id(9)}),[{...alt(),userId:id(9)}]);
});
test('cancellation and epoch reject responses even when transport ignores AbortSignal',async()=>{
 const gate=new m.SelectionReadGate();let resolve;const pending=new Promise(r=>resolve=r);const first=gate.begin();const late=pending.then(()=>gate.current(first));
 const second=gate.begin();assert.equal(first.signal.aborted,true);resolve();assert.equal(await late,false);assert.equal(gate.current(second),true);gate.invalidate();assert.equal(gate.current(second),false);
});
test('optional expected is precisely the server version, never derived evidence',()=>{
 const expected={media_id:id(5),intent_version:2,media_version:4,policy_id:'p',policy_revision:7,scope_stamp:'a',configuration_stamp:'b',selection_policy_stamp:'c'};
 assert.deepEqual(m.selectionRequest(scope,[alt()],[id(3)],'2','none','0.5',expected),{...req(),expected});
});
test('executable, foreign-candidate, duplicate or malformed results fail closed',()=>{
 const good={status:'preview_not_executable',execution_allowed:false,version:{media_id:id(20),intent_version:1,media_version:1,policy_id:'p',policy_revision:1,scope_stamp:'a'.repeat(64),configuration_stamp:'b'.repeat(64),selection_policy_stamp:'c'.repeat(64)},decision:{kind:'episodes',candidate_ids:[id(103)],reason:'episode_preference'},excluded:[]};assert.equal(m.validSelectionResponse(good,req()),true);
 assert.equal(m.validSelectionResponse(good,{...req(),expected:{...good.version,intent_version:2}}),false);
 for(const bad of [{...good,version:{}},{...good,execution_allowed:true},{...good,status:'queued'},{...good,decision:{...good.decision,candidate_ids:[id(999)]}},{...good,decision:{...good.decision,candidate_ids:[id(103),id(103)]}},{...good,decision:{...good.decision,kind:'hold'}},{...good,excluded:[{candidate_id:id(999)}]}])assert.equal(m.validSelectionResponse(bad,req()),false);
});
for(const status of [401,403,409,503])test(`${status} has explicit read recovery with old result cleared`,()=>{assert.match(m.selectionFailure(status),/旧比较已清除/);assert.match(m.selectionFailure(status),/不会提交任何获取命令/)});

for(const reason of [undefined,null,{toString:null},17])test(`reject non-string excluded reason ${JSON.stringify(reason)}`,()=>{
 const response={status:'preview_not_executable',execution_allowed:false,version:{media_id:id(20),intent_version:1,media_version:1,policy_id:'p',policy_revision:1,scope_stamp:'a'.repeat(64),configuration_stamp:'b'.repeat(64),selection_policy_stamp:'c'.repeat(64)},decision:{kind:'hold',candidate_ids:[],reason:'no_eligible_alternative'},excluded:[{candidate_id:id(103),reason}]};
 assert.equal(m.validSelectionResponse(response,req()),false);
});
