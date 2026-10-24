import {test} from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import ts from 'typescript';
const text=readFileSync(new URL('../src/features/configuration/SeedingRuleInput.ts',import.meta.url),'utf8');
const code=ts.transpileModule(text,{compilerOptions:{target:ts.ScriptTarget.ES2022,module:ts.ModuleKind.ESNext}}).outputText;
const {seedingRuleInput:parse}=await import('data:text/javascript;base64,'+Buffer.from(code).toString('base64'));
test('seeding UI freezes retain-files and preserves only explicitly entered conditions',()=>{
 assert.deepEqual(parse('all','3600','',''),{mode:'all',delete_files:false,min_seeding_seconds:3600});
 assert.deepEqual(parse('any','','2.5','1024'),{mode:'any',delete_files:false,min_share_ratio:2.5,low_free_bytes:1024});
});
test('no threshold and invalid matching mode cannot authorize a save',()=>{assert.equal(parse('all','','',''),null);assert.equal(parse('ALL','1','',''),null);});
test('zero negative exponent fractional seconds and out-of-range times are rejected',()=>{
 for(const value of ['0','-1','1e3','1.5','315360001',' 60','01'])assert.equal(parse('all',value,'',''),null);
 assert.equal(parse('all','315360000','','').min_seeding_seconds,315360000);
});
test('native ratio is finite positive bounded decimal and never inferred from zero',()=>{
 for(const value of ['0','-1','NaN','Infinity','1e2','1000000.1','0x10'])assert.equal(parse('all','',value,''),null);
 assert.equal(parse('all','','0.5','').min_share_ratio,0.5);assert.equal(parse('all','','1000000','').min_share_ratio,1000000);
});
test('free-space threshold accepts exact safe integer bytes and rejects lossy values',()=>{
 assert.equal(parse('any','','','9007199254740991').low_free_bytes,Number.MAX_SAFE_INTEGER);
 for(const value of ['0','-1','1.5','9007199254740992','1e9'])assert.equal(parse('any','','',value),null);
});
