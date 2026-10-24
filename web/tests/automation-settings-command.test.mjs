import {test} from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import {stripTypeScriptTypes} from 'node:module';
const source=readFileSync(new URL('../src/features/configuration/automationSettingsCommand.ts',import.meta.url),'utf8');
const {automationStorageKey,parseAutomationCommand,serializeAutomationCommand,automationInputHash}=await import(`data:text/javascript;base64,${Buffer.from(stripTypeScriptTypes(source)).toString('base64')}`);
const owner='00000000-0000-4000-8000-000000000001',other='00000000-0000-4000-8000-000000000002',key='00000000-0000-4000-8000-000000000003';
const record={key,owner,scope:'douban',version:0,hash:'1'.repeat(64)};
test('journal allowlist excludes full input, UID and secret',()=>{
 const text=serializeAutomationCommand({...record,body:{douban_uid:'private-user',timezone:'private-zone',password:'secret'}});
 assert.deepEqual(JSON.parse(text),record);assert.doesNotMatch(text,/private-user|private-zone|password|secret|body/);
 assert.deepEqual(parseAutomationCommand(text,owner,'douban'),record);
});
test('owner and endpoint scopes never restore another pending command',()=>{
 const text=serializeAutomationCommand(record);
 assert.equal(parseAutomationCommand(text,other,'douban'),null);assert.equal(parseAutomationCommand(text,owner,'checkin:source'),null);
 assert.notEqual(automationStorageKey(owner,'douban'),automationStorageKey(other,'douban'));
 assert.notEqual(automationStorageKey(owner,'checkin:a'),automationStorageKey(owner,'checkin:b'));
});
test('corrupted or unsafe versions and injected fields fail closed',()=>{
 for(const bad of ['{','null','[]',' '.repeat(1025),JSON.stringify({...record,version:-1}),JSON.stringify({...record,version:1.5}),JSON.stringify({...record,version:Number.MAX_SAFE_INTEGER+1}),JSON.stringify({...record,key:'bad'}),JSON.stringify({...record,hash:'bad'}),JSON.stringify({...record,sourceVersion:0}),JSON.stringify({...record,sourceVersion:1.1}),JSON.stringify({...record,body:{uid:'private'}}),JSON.stringify({...record,rejected:false})])assert.equal(parseAutomationCommand(bad,owner,'douban'),null);
});
test('definite refusal marker and source version survive reload without request body',()=>{
 const expected={...record,sourceVersion:8,rejected:true};assert.deepEqual(parseAutomationCommand(serializeAutomationCommand(expected),owner,'douban'),expected);
});
test('retry hash ignores property order but preserves exact values and explicit clearing',async()=>{
 const body={expected_version:3,douban_uid:'actor',enabled:true,bindings:{indexer_ids:['a','b'],downloader_id:'c'}};
 assert.equal(await automationInputHash(body),await automationInputHash({bindings:{downloader_id:'c',indexer_ids:['a','b']},enabled:true,douban_uid:'actor',expected_version:3}));
 for(const changed of [{...body,expected_version:4},{...body,douban_uid:'different'},{...body,enabled:false},{...body,bindings:{indexer_ids:[]}},{...body,bindings:undefined}])assert.notEqual(await automationInputHash(body),await automationInputHash(changed));
 assert.equal(await automationInputHash({a:undefined,b:1}),await automationInputHash({b:1}));
});
