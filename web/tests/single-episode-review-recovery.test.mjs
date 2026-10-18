import {test} from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import {stripTypeScriptTypes} from 'node:module';
const source=readFileSync(new URL('../src/features/configuration/singleEpisodeReviewRecovery.ts',import.meta.url),'utf8');
async function load(text=source){return import(`data:text/javascript;base64,${Buffer.from(stripTypeScriptTypes(text)).toString('base64')}#${crypto.randomUUID()}`)}
const sub='00000000-0000-4000-8000-000000000001';
const command={key:'00000000-0000-4000-8000-000000000002',body:{candidate_id:'00000000-0000-4000-8000-000000000003',expected_input_stamp:'a'.repeat(64),confirmed:true,reason:'已人工核对当前资源的作品与单集身份'}};
const receipt={id:'00000000-0000-4000-8000-000000000004',subscription_id:sub,candidate_id:command.body.candidate_id,unit_id:'00000000-0000-4000-8000-000000000005',input_stamp:command.body.expected_input_stamp,status:'reviewed_not_executable'};
function storage(){const values=new Map();return {values,failSet:'',failRemove:false,getItem:k=>values.get(k)??null,setItem(k,v){if(k.includes(this.failSet)&&this.failSet)throw Error('storage unavailable');values.set(k,v)},removeItem(k){if(this.failRemove)throw Error('remove unavailable');values.delete(k)}}}
async function unknownDenialContract(m,status=403){
 const s=storage();let posts=0;const saved=structuredClone(command);
 assert.equal((await m.sendEpisodeReview(sub,command,false,s,async()=>{posts++;throw Error('committed; response lost')})).kind,'unknown');
 const restored=JSON.parse(s.getItem(m.reviewCommandKey(sub)));
 assert.equal((await m.sendEpisodeReview(sub,restored,true,s,async()=>{posts++;return {status}})).kind,'unknown');
 assert.deepEqual(JSON.parse(s.getItem(m.reviewCommandKey(sub))),saved);
 const restoredAgain=JSON.parse(s.getItem(m.reviewCommandKey(sub)));
 const result=await m.sendEpisodeReview(sub,restoredAgain,true,s,async()=>{posts++;assert.deepEqual(restoredAgain,saved);return {status:200,data:receipt}});
 assert.equal(result.kind,'confirmed');assert.equal(posts,3);assert.equal(s.getItem(m.reviewCommandKey(sub)),null);
}
async function confirmedStorageContract(m,failRemove=false){
 const s=storage();let posts=0;
 if(failRemove)s.failRemove=true;else s.failSet='episode-review-receipt';
 const result=await m.sendEpisodeReview(sub,command,false,s,async()=>{posts++;return {status:200,data:receipt}});
 assert.equal(result.kind,'confirmed');assert.equal(result.confirmation.persisted,false);
 assert.deepEqual(m.confirmedEpisodeReview(sub,command).receipt,receipt);
 assert.equal((await m.sendEpisodeReview(sub,command,true,s,async()=>{posts++;assert.fail('confirmed command POSTed again')})).kind,'confirmed');
 s.failRemove=false;s.failSet='';
 const repaired=await m.sendEpisodeReview(sub,command,true,s,async()=>assert.fail('storage recovery sent POST'));
 assert.equal(repaired.confirmation.persisted,true);assert.equal(posts,1);assert.equal(s.getItem(m.reviewCommandKey(sub)),null);
 assert.deepEqual(JSON.parse(s.getItem(`pulse:episode-review-receipt:${sub}`)),receipt);
}
for(const status of [400,403,409])test(`lost commit then retry ${status} retains exact key/body and recovers`,async()=>unknownDenialContract(await load(),status));
for(const status of [400,403,409])test(`first proven ${status} clears only new rejected command`,async()=>{
 const m=await load(),s=storage();assert.equal((await m.sendEpisodeReview(sub,command,false,s,async()=>({status}))).kind,'rejected');assert.equal(s.getItem(m.reviewCommandKey(sub)),null);assert.equal(m.confirmedEpisodeReview(sub),null);
});
for(const status of [401,404,503])test(`first ${status} conservatively preserves unknown command`,async()=>{
 const m=await load(),s=storage();assert.equal((await m.sendEpisodeReview(sub,command,false,s,async()=>({status}))).kind,'unknown');assert.deepEqual(JSON.parse(s.getItem(m.reviewCommandKey(sub))),command);
});
test('receipt setItem failure is fenced before storage and repairs without POST',async()=>confirmedStorageContract(await load()));
test('pending removeItem failure is fenced and repairs without POST',async()=>confirmedStorageContract(await load(),true));
test('pending persistence failure prevents first transport',async()=>{
 const m=await load(),s=storage();s.failSet='episode-review:';assert.equal((await m.sendEpisodeReview(sub,command,false,s,async()=>assert.fail('sent without persisted command'))).kind,'not_sent');
});
test('rejected-command cleanup failure retains original command',async()=>{
 const m=await load(),s=storage();s.failRemove=true;assert.equal((await m.sendEpisodeReview(sub,command,false,s,async()=>({status:403}))).kind,'unknown');assert.deepEqual(JSON.parse(s.getItem(m.reviewCommandKey(sub))),command);
});
test('valid late 200 after abort is captured and never resubmitted',async()=>{
 const m=await load(),s=storage(),c=new AbortController();let release;const reply=new Promise(resolve=>{release=resolve});
 const sending=m.sendEpisodeReview(sub,command,false,s,()=>reply);c.abort();s.failSet='episode-review-receipt';release({status:200,data:receipt});
 assert.equal(c.signal.aborted,true);assert.equal((await sending).kind,'confirmed');assert(m.confirmedEpisodeReview(sub,command));
 await m.sendEpisodeReview(sub,command,true,s,async()=>assert.fail('late success replayed'));
});
test('abort without a valid receipt remains unknown',async()=>{
 const m=await load(),s=storage();assert.equal((await m.sendEpisodeReview(sub,command,false,s,async()=>{throw new DOMException('cancelled','AbortError')})).kind,'unknown');assert.equal(m.confirmedEpisodeReview(sub),null);assert(s.getItem(m.reviewCommandKey(sub)));
});
test('200 receipt must match subscription, candidate, stamp, status and UUIDs',async()=>{
 for(const delta of [{subscription_id:command.key},{candidate_id:command.key},{input_stamp:'b'.repeat(64)},{status:'queued'},{id:'invalid'},{unit_id:'invalid'}]){
  const m=await load(),s=storage();assert.equal((await m.sendEpisodeReview(sub,command,false,s,async()=>({status:200,data:{...receipt,...delta}}))).kind,'unknown');assert.equal(m.confirmedEpisodeReview(sub),null);
 }
});
test('new command can submit after successful persistence; another subscription has no fence',async()=>{
 const m=await load(),s=storage();await m.sendEpisodeReview(sub,command,false,s,async()=>({status:200,data:receipt}));assert.equal(m.confirmedEpisodeReview(command.key),null);
 const next={...command,key:receipt.id};let posts=0;assert.equal((await m.sendEpisodeReview(sub,next,false,s,async()=>{posts++;return {status:403}})).kind,'rejected');assert.equal(posts,1);
});
test('negative control: old unconditional denial cleanup fails recovery contract',async()=>{
 const mutant=source.replace('if(!isRetry&&[400,403,409].includes(reply.status))','if([400,403,409].includes(reply.status))');assert.notEqual(mutant,source);
 await assert.rejects(unknownDenialContract(await load(mutant)),assert.AssertionError);
});
test('negative control: storage before in-memory fence fails confirmation contract',async()=>{
 const mutant=source.replace('confirmed.set(id,confirmation);','storage.setItem(receiptKey(id),JSON.stringify(reply.data));\n  confirmed.set(id,confirmation);');assert.notEqual(mutant,source);
 await assert.rejects(confirmedStorageContract(await load(mutant)),/storage unavailable/);
});
test('panel integration preserves exact CompleteTV import and mount, captures before abort, separates repair',()=>{
 const panel=readFileSync(new URL('../src/features/configuration/EpisodeCandidateReviewPanel.tsx',import.meta.url),'utf8');
 assert(panel.includes('import {CompleteTVCoveragePanel} from "./CompleteTVCoveragePanel";'));
 const mount=panel.match(/<CompleteTVCoveragePanel\s[^>]*\/>/)?.[0];assert(mount);
 for(const prop of ['subscriptionId={subscriptionId}','signature={signature}','candidateId={selected}','disabled={disabled||busy}'])assert(mount.includes(prop));
 const review=panel.slice(panel.indexOf(' async function review()'));
 assert(review.indexOf('await sendEpisodeReview(')<review.indexOf('if(c.signal.aborted)return;'));
 assert(review.indexOf('if(repairPending)')<review.indexOf('await sendEpisodeReview('));
 assert(panel.includes('setRepairPending(!persisted)'));assert(panel.includes('confirmedEpisodeReview(id,value)?null:value'));
});

test('older confirmed key stays fenced after a successor confirmation',async()=>{
 const m=await load(),s=storage();let posts=0;
 await m.sendEpisodeReview(sub,command,false,s,async()=>{posts++;return {status:200,data:receipt}});
 const next={...command,key:receipt.id};
 await m.sendEpisodeReview(sub,next,false,s,async()=>{posts++;return {status:200,data:{...receipt,id:command.key}}});
 await m.sendEpisodeReview(sub,command,true,s,async()=>assert.fail('older confirmed key replayed'));
 assert.equal(posts,2);
});

