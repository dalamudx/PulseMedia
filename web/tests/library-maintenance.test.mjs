import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import ts from 'typescript';
const source = (await readFile(new URL('../src/features/library/libraryMaintenanceCommand.ts', import.meta.url), 'utf8')).replace("import { api } from '@/api/client';", 'const api = {};');
const output = ts.transpileModule(source, { compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.ESNext } }).outputText;
const {runCommand, readJournal, storageKey, relativePathOK, validateReceipt} = await import(`data:text/javascript;base64,${Buffer.from(output).toString('base64')}`);
const id = n => `00000000-0000-0000-0000-${String(n).padStart(12, '0')}`;
function fixture(action='reorganize') {
 const account=id(1),fileID=id(2),resource=action==='reorganize'?`file:${fileID}`:`recycle:movie:${id(3)}`, values=new Map();let tail=Promise.resolve(),posts=0;
 const storage={getItem:k=>values.get(k)??null,setItem:(k,v)=>values.set(k,v)};
 const locks={request(_key,fn){const p=tail.then(fn);tail=p.catch(()=>{});return p;}};
 const body=action==='reorganize'?{action,file_id:fileID,target_relative_path:'Movies/New.mkv',expected_version:'v1',confirmed:true}:{action,recycle_id:`movie:${id(3)}`,expected_version:'v1',confirmed:true};
 const receipt={command_id:id(4),action,status:'completed',file_id:action==='restore'?id(5):fileID,version:body.expected_version,execution_allowed:false};
 const options={account,fileID,resource,storage,locks,body,live:()=>true,saved(){},async transport(entry,sending){assert.equal(readJournal(storage,account,resource).key,entry.key);assert.equal(sending,true);posts++;return receipt;}};
 return {options,receipt,storage,values,body,get posts(){return posts;}};
}
test('relative target rejects traversal, absolute paths, control characters and empty segments',()=>{for(const v of ['', '/a.mkv','../a.mkv','a/../b','a//b','a\\b','C:/a','a\n.mkv','a/./b'])assert.equal(relativePathOK(v),false,v);assert.equal(relativePathOK('电影/片名 (2026)/片名.mkv'),true);});
test('all three actions save identity before transport and preserve exact bodies',async()=>{for(const action of ['reorganize','restore','cleanup']){const f=fixture(action);await runCommand(f.options);const j=readJournal(f.storage,f.options.account,f.options.resource);assert.deepEqual(j.body,f.body);assert.deepEqual(j.receipt,f.receipt);assert.equal(f.posts,1);if(action!=='reorganize')assert.equal('target_relative_path' in j.body,false);}});
test('lost POST and GET 404 preserve original key and never resend',async()=>{const f=fixture();let posts=0;await assert.rejects(runCommand({...f.options,transport:async()=>{posts++;throw Error('lost');}}));const original=readJournal(f.storage,f.options.account,f.options.resource);await runCommand(f.options);assert.equal(f.posts,0);await assert.rejects(runCommand({...f.options,body:undefined,transport:async(e,send)=>{assert.equal(send,false);assert.equal(e.key,original.key);throw Error('404');}}));assert.equal(readJournal(f.storage,f.options.account,f.options.resource).key,original.key);assert.equal(posts,1);await runCommand({...f.options,body:undefined,transport:async(e,send)=>{assert.equal(send,false);assert.equal(e.key,original.key);return f.receipt;}});assert.equal(readJournal(f.storage,f.options.account,f.options.resource).receipt.status,'completed');});
test('unknown receipt blocks replacement POST and accepts only original GET',async()=>{const f=fixture();await runCommand({...f.options,transport:async()=>({...f.receipt,status:'unknown_requires_review'})});await runCommand(f.options);assert.equal(f.posts,0);await runCommand({...f.options,body:undefined,transport:async(_,send)=>{assert.equal(send,false);return f.receipt;}});assert.equal(readJournal(f.storage,f.options.account,f.options.resource).receipt.status,'completed');});
test('two concurrent tabs submit one physical POST under Web Locks',async()=>{const f=fixture();await Promise.all([runCommand(f.options),runCommand(f.options)]);assert.equal(f.posts,1);});
test('missing Web Locks and failed pre-send storage cause zero sends',async()=>{for(const options of [{locks:undefined},{storage:{getItem:()=>null,setItem(){throw Error('quota');}}}]){const f=fixture();await assert.rejects(runCommand({...f.options,...options}));assert.equal(f.posts,0);}});
test('failed receipt save preserves unresolved journal and does not permit replacement',async()=>{const f=fixture();let writes=0;const storage={...f.storage,setItem(k,v){if(++writes===2)throw Error('quota');f.storage.setItem(k,v);}};await assert.rejects(runCommand({...f.options,storage}));assert.equal(f.posts,1);assert.equal(readJournal(f.storage,f.options.account,f.options.resource).receipt,undefined);await runCommand(f.options);assert.equal(f.posts,1);});
test('corrupt or copied account/resource journal blocks writes',async()=>{for(const mode of ['json','account','resource','body']){const f=fixture();await runCommand(f.options);const key=storageKey(f.options.account,f.options.resource),j=readJournal(f.storage,f.options.account,f.options.resource);if(mode==='json')f.values.set(key,'{bad');else{if(mode==='body')j.body.confirmed=false;else j[mode]='other';f.values.set(key,JSON.stringify(j));}await assert.rejects(runCommand(f.options));assert.equal(f.posts,1);}});
test('receipt validates action, file, status, version and command identity',async()=>{const f=fixture();await runCommand(f.options);const j=readJournal(f.storage,f.options.account,f.options.resource);for(const change of [{action:'cleanup'},{file_id:id(9)},{status:'queued'},{version:''},{version:'other-valid-version'},{execution_allowed:true},{command_id:id(9)}])assert.throws(()=>validateReceipt({...f.receipt,...change},j));});
test('unmounted or queued stale account cannot initiate a write',async()=>{const f=fixture();await runCommand({...f.options,live:()=>false});assert.equal(f.posts,0);assert.equal(f.values.size,0);});
test('account and resource storage keys remain isolated',()=>{assert.notEqual(storageKey(id(1),'file:a'),storageKey(id(2),'file:a'));assert.notEqual(storageKey(id(1),'file:a'),storageKey(id(1),'recycle:a'));});
for (const action of ['reorganize', 'restore', 'cleanup']) {
 test(`${action} binds receipt version and file identity for completed and unknown outcomes`, () => {
  const f = fixture(action), entry = { body: f.body, fileID: f.options.fileID };
  for (const status of ['completed', 'unknown_requires_review']) {
   const receipt = { ...f.receipt, status, file_id: action === 'restore' && status === 'completed' ? id(5) : entry.fileID };
   assert.deepEqual(validateReceipt(receipt, entry), receipt);
   for (const version of ['', 'v2', 'other-valid-version']) assert.throws(() => validateReceipt({ ...receipt, version }, entry));
   const invalidIDs = action === 'restore' && status === 'completed' ? [entry.fileID, id(0), 'not-a-uuid', '', null] : [id(5), id(0), 'not-a-uuid'];
   for (const file_id of invalidIDs) assert.throws(() => validateReceipt({ ...receipt, file_id }, entry));
  }
 });
}
test('restore unknown recovers a fresh file identity by GET with one POST across reload', async () => {
 const f = fixture('restore'); let posts = 0, gets = 0;
 await runCommand({ ...f.options, transport: async (_, sending) => { assert.equal(sending, true); posts++; return { ...f.receipt, status: 'unknown_requires_review', file_id: f.options.fileID }; } });
 const original = readJournal(f.storage, f.options.account, f.options.resource);
 await runCommand({ ...f.options, transport: async () => { throw Error('second POST'); } });
 const recovered = await runCommand({ ...f.options, body: undefined, transport: async (entry, sending) => { assert.equal(sending, false); assert.equal(entry.key, original.key); assert.equal(entry.receipt.file_id, f.options.fileID); gets++; return f.receipt; } });
 assert.deepEqual(recovered, f.receipt);
 const persisted = readJournal(f.storage, f.options.account, f.options.resource);
 assert.equal(persisted.fileID, f.options.fileID); assert.equal(persisted.receipt.file_id, id(5));
 assert.equal(posts, 1); assert.equal(gets, 1);
 await runCommand({ ...f.options, body: undefined, transport: async () => { throw Error('completed command must stay closed'); } });
});
test('restore recovery rejects changed command/action/version and preserves unknown journal', async () => {
 for (const change of [{ command_id: id(99) }, { action: 'cleanup', file_id: id(2) }, { version: 'different-valid-version' }, { file_id: id(2) }, { file_id: id(0) }]) {
  const f = fixture('restore');
  await runCommand({ ...f.options, transport: async () => ({ ...f.receipt, status: 'unknown_requires_review', file_id: f.options.fileID }) });
  const original = readJournal(f.storage, f.options.account, f.options.resource);
  await assert.rejects(runCommand({ ...f.options, body: undefined, transport: async (_, sending) => { assert.equal(sending, false); return { ...f.receipt, ...change }; } }));
  assert.deepEqual(readJournal(f.storage, f.options.account, f.options.resource), original);
 }
});
test('saved receipts must themselves be valid and completed restore identity cannot change or regress', async () => {
 const f = fixture('restore'); await runCommand(f.options);
 const entry = readJournal(f.storage, f.options.account, f.options.resource);
 assert.deepEqual(validateReceipt(f.receipt, entry), f.receipt);
 for (const change of [{ file_id: id(6) }, { status: 'unknown_requires_review', file_id: entry.fileID }]) assert.throws(() => validateReceipt({ ...f.receipt, ...change }, entry));
 for (const change of [{ action: 'cleanup' }, { version: 'v2' }, { file_id: entry.fileID }, { command_id: 'bad' }]) {
  const corrupt = { ...entry, receipt: { ...entry.receipt, ...change } };
  assert.throws(() => validateReceipt(f.receipt, corrupt));
  f.storage.setItem(storageKey(entry.account, entry.resource), JSON.stringify(corrupt));
  assert.throws(() => readJournal(f.storage, entry.account, entry.resource));
 }
});
