import test from 'node:test';
import assert from 'node:assert/strict';
import {readFile} from 'node:fs/promises';
import ts from 'typescript';
const source=await readFile(new URL('../src/features/configuration/completeTVOrganizationJournal.ts',import.meta.url),'utf8');
const {outputText}=ts.transpileModule(source,{compilerOptions:{module:ts.ModuleKind.ESNext,target:ts.ScriptTarget.ES2022}});
const journal=await import('data:text/javascript;base64,'+Buffer.from(outputText).toString('base64'));
const account='aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa',operation='11111111-1111-4111-8111-111111111111';
const command={operation_id:operation,task_id:'22222222-2222-4222-8222-222222222222',tracking_job_id:'33333333-3333-4333-8333-333333333333',key:'44444444-4444-4444-8444-444444444444'};
const pending={version:1,account,operation,command};
for(const field of ['task_id','operation_id','tracking_job_id','key'])test('invalid '+field+' cannot replace durable original',()=>{
 const entries=new Map([[journal.journalName(account,operation),JSON.stringify(pending)]]);let writes=0;
 globalThis.localStorage={getItem:key=>entries.get(key)??null,setItem:(key,value)=>{writes++;entries.set(key,value);}};
 const damaged={...pending,command:{...command,[field]:'bad'}};
 assert.throws(()=>journal.saveJournal(damaged),/journal invalid/);assert.equal(writes,0);assert.deepEqual(journal.readJournal(account,operation),pending);
});
test('valid command remains byte-identical through receipt repair',()=>{
 const entries=new Map();globalThis.localStorage={getItem:key=>entries.get(key)??null,setItem:(key,value)=>entries.set(key,value)};
 journal.saveJournal(pending);const closed={...pending,receipt:{task_id:command.task_id,plan_id:'55555555-5555-4555-8555-555555555555',job_id:'66666666-6666-4666-8666-666666666666',state:'queued'}};
 journal.saveJournal(closed);assert.deepEqual(journal.readJournal(account,operation),closed);assert.equal(JSON.stringify(journal.readJournal(account,operation).command),JSON.stringify(command));
});

for(const suffix of ['\n','\r','\u2028','\u2029'])test('line terminator '+JSON.stringify(suffix)+' is rejected in every command UUID',()=>{
 const entries=new Map([[journal.journalName(account,operation),JSON.stringify(pending)]]);let writes=0;
 globalThis.localStorage={getItem:key=>entries.get(key)??null,setItem:(key,value)=>{writes++;entries.set(key,value);}};
 for(const field of ['task_id','operation_id','tracking_job_id','key'])assert.throws(()=>journal.saveJournal({...pending,command:{...command,[field]:command[field]+suffix}}),/journal invalid/);
 assert.equal(writes,0);assert.deepEqual(journal.readJournal(account,operation),pending);
});

test('naming versions remain bound to the original command and survive receipt repair',()=>{
 const entries=new Map();globalThis.localStorage={getItem:key=>entries.get(key)??null,setItem:(key,value)=>entries.set(key,value)};
 for(const naming_version of [0,7]){
  const original={...pending,command:{...command,naming_version}};
  journal.saveJournal(original);
  assert.deepEqual(journal.readJournal(account,operation),original);
  const closed={...original,receipt:{task_id:command.task_id,plan_id:'55555555-5555-4555-8555-555555555555',job_id:'66666666-6666-4666-8666-666666666666',state:'queued'}};
  journal.saveJournal(closed);assert.deepEqual(journal.readJournal(account,operation).command,original.command);
 }
});
for(const naming_version of [-1,1.5,null,'7',Number.MAX_SAFE_INTEGER+1])test('invalid naming version '+JSON.stringify(naming_version)+' cannot replace original journal',()=>{
 const entries=new Map([[journal.journalName(account,operation),JSON.stringify(pending)]]);let writes=0;
 globalThis.localStorage={getItem:key=>entries.get(key)??null,setItem:(key,value)=>{writes++;entries.set(key,value)}};
 assert.throws(()=>journal.saveJournal({...pending,command:{...command,naming_version}}),/journal invalid/);
 assert.equal(writes,0);assert.deepEqual(journal.readJournal(account,operation),pending);
});
