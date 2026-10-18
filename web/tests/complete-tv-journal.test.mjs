import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import ts from 'typescript';
const source = readFileSync(new URL('../src/features/configuration/completeTVJournal.ts', import.meta.url), 'utf8');
const js = ts.transpileModule(source, { compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.ESNext } }).outputText;
const { journalKey, readJournal, saveEntry, sendEntry } = await import(`data:text/javascript;base64,${Buffer.from(js).toString('base64')}`);
const sub = '00000000-0000-4000-8000-000000000001';
const candidate = '00000000-0000-4000-8000-000000000002';
const userId='00000000-0000-4000-8000-000000000010';
const coverage=[{unitId:'00000000-0000-4000-8000-000000000011',unitKey:'S02E03'}];
const key = journalKey(userId, sub);
const input = 'a'.repeat(64), material = 'b'.repeat(64);
function storage() { const values = new Map(); return { getItem: k => values.get(k) ?? null, setItem: (k,v) => values.set(k,v) }; }
function review() { return { materialStamp: material, userId, subscriptionId:sub, coverage, key: '00000000-0000-4000-8000-000000000003', signature: 'intent-7', candidateId: candidate, phase: 'pending', kind: 'review', body: { candidate_id: candidate, expected_input_stamp: input, confirmed: true, reason: '逐项人工核对' } }; }
const reviewReceipt = { id: '00000000-0000-4000-8000-000000000004', subscription_id: sub, candidate_id: candidate, input_stamp: input, material_stamp: material, status: 'reviewed_not_executable' };
function acquire() { return { materialStamp: material, userId, subscriptionId:sub, coverage, key: '00000000-0000-4000-8000-000000000005', signature: 'intent-7', candidateId: candidate, phase: 'pending', kind: 'acquire', body: { review_id: reviewReceipt.id, expected_input_stamp: input, expected_material_stamp: material, confirmed: true } }; }
const queued = { operation_id: '00000000-0000-4000-8000-000000000006', job_id: '00000000-0000-4000-8000-000000000007', reservations: ['00000000-0000-4000-8000-000000000008'], source_candidate_id: candidate, status: 'queued' };

test('freeze before transport and recover exact original body/key after reload', async () => {
  const s=storage(), command=review(); let sent;
  await assert.rejects(sendEntry(s,key,command,async e=>{sent=structuredClone(e); assert.deepEqual(readJournal(s,key).entries,[command]); throw Error('lost response');},()=>{}));
  const restored=readJournal(s,key).entries[0];
  const changed={...restored,signature:'intent-9',body:{...restored.body,expected_input_stamp:'c'.repeat(64)}};
  await sendEntry(s,key,changed,async e=>{assert.deepEqual(e,sent);return {status:200,data:reviewReceipt}},()=>{});
  assert.equal(readJournal(s,key).entries[0].phase,'confirmed');
});
test('confirmed review survives refresh; no replay on read failure',async()=>{
  const s=storage(); let posts=0;
  await sendEntry(s,key,review(),async()=>{posts++;return {status:200,data:reviewReceipt}},()=>{});
  await assert.rejects(async()=>{throw Error('GET failed')});
  await sendEntry(s,key,review(),async()=>{posts++;throw Error('must not send')},()=>{});
  assert.equal(posts,1); assert.equal(readJournal(s,key).entries[0].receipt.status,'reviewed_not_executable');
});
test('review and acquisition retain separate original commands and keys',async()=>{
  const s=storage(); await sendEntry(s,key,review(),async()=>({status:200,data:reviewReceipt}),()=>{});
  await sendEntry(s,key,acquire(),async e=>{assert.notEqual(e.key,review().key);assert.equal(e.body.expected_material_stamp,material);return {status:202,data:queued}},()=>{});
  assert.deepEqual(readJournal(s,key).entries.map(e=>e.kind),['review','acquire']);
});
for(const status of [400,404,409]) test(`HTTP ${status} closes only rejected command; cannot replay`,async()=>{
  const s=storage(); await sendEntry(s,key,review(),async()=>({status}),()=>{});
  assert.equal(readJournal(s,key).entries[0].phase,'rejected');
  await sendEntry(s,key,review(),async()=>assert.fail('terminal command replayed'),()=>{});
});
for(const status of [401,403,503]) test(`HTTP ${status} preserves unknown command across logout/login`,async()=>{
  const s=storage();await assert.rejects(sendEntry(s,key,acquire(),async()=>({status}),()=>{}));
  assert.deepEqual(readJournal(s,key).entries,[acquire()]);
  assert.deepEqual(readJournal(s,journalKey('bob',sub)).entries,[]);
  await sendEntry(s,key,acquire(),async()=>({status:202,data:queued}),()=>{});
  assert.equal(readJournal(s,key).entries[0].phase,'confirmed');
});
test('abort after send leaves original command recoverable',async()=>{
  const s=storage(); await assert.rejects(sendEntry(s,key,acquire(),async()=>{throw new DOMException('aborted','AbortError')},()=>{}));
  assert.deepEqual(readJournal(s,key).entries,[acquire()]);
});
test('late success writes captured original account, never current account',async()=>{
  const s=storage(); let release;const response=new Promise(resolve=>{release=resolve});
  const pending=sendEntry(s,key,acquire(),()=>response,()=>{});
  release({status:202,data:queued});await pending;
  assert.equal(readJournal(s,key).entries[0].phase,'confirmed');
  assert.deepEqual(readJournal(s,journalKey('bob',sub)).entries,[]);
});
test('legacy reserved_not_executable receipt preserved without upgrade POST',async()=>{
  const s=storage(); await sendEntry(s,key,acquire(),async()=>({status:202,data:{...queued,status:'reserved_not_executable'}}),()=>{});
  await sendEntry(s,key,acquire(),async()=>assert.fail('legacy receipt replayed'),()=>{});
  assert.equal(readJournal(s,key).entries[0].receipt.status,'reserved_not_executable');
});
test('new key cannot replace pending command after selection/version change',()=>{
  const s=storage();saveEntry(s,key,review());
  assert.throws(()=>saveEntry(s,key,{...acquire(),signature:'intent-8'}));
  assert.deepEqual(readJournal(s,key).entries,[review()]);
});
test('new key cannot duplicate confirmed acquisition of same review',async()=>{
  const s=storage();await sendEntry(s,key,acquire(),async()=>({status:202,data:queued}),()=>{});
  assert.throws(()=>saveEntry(s,key,{...acquire(),key:'00000000-0000-4000-8000-000000000009'}));
});
test('frozen body and terminal receipts cannot be rewritten',async()=>{
  const s=storage();saveEntry(s,key,review());
  assert.throws(()=>saveEntry(s,key,{...review(),body:{...review().body,reason:'changed'}}));
  await sendEntry(s,key,review(),async()=>({status:200,data:reviewReceipt}),()=>{});
  assert.throws(()=>saveEntry(s,key,review()));
});
test('unavailable storage prevents transport',async()=>{
  let calls=0;const s={getItem:()=>null,setItem:()=>{throw Error('quota')}};
  await assert.rejects(sendEntry(s,key,review(),async()=>{calls++;return {status:200,data:reviewReceipt}},()=>{}));
  assert.equal(calls,0);
});
test('corrupt or unknown-schema storage prevents new transport',async()=>{
  for(const raw of ['{','null','{"version":9,"entries":[]}','{"version":1,"entries":[{}]}']){
    const s=storage();s.setItem(key,raw);
    await assert.rejects(sendEntry(s,key,review(),async()=>assert.fail('corrupt recovery ignored'),()=>{}));
  }
});
test('storage failure after success exposes in-memory terminal fence before error',async()=>{
  const backing=storage();let writes=0,confirmed;
  const s={getItem:backing.getItem,setItem:(k,v)=>{if(++writes>1)throw Error('quota');backing.setItem(k,v)}};
  await assert.rejects(sendEntry(s,key,acquire(),async()=>({status:202,data:queued}),e=>{confirmed=e}));
  assert.equal(confirmed.phase,'confirmed');assert.equal(confirmed.receipt.status,'queued');
  // Saving this fence on the read-only recovery path does not call transport.
  saveEntry(backing,key,confirmed);assert.equal(readJournal(backing,key).entries[0].phase,'confirmed');
});
test('invalid success receipt cannot claim confirmed coverage',async()=>{
  const s=storage();await assert.rejects(sendEntry(s,key,review(),async()=>({status:200,data:{...reviewReceipt,candidate_id:sub}}),()=>assert.fail('invalid receipt accepted')));
  assert.equal(readJournal(s,key).entries[0].phase,'pending');
});
test('different subscription has independent journal',()=>{
  const s=storage();saveEntry(s,key,review());assert.deepEqual(readJournal(s,journalKey(userId,candidate)).entries,[]);
});

