import assert from 'node:assert/strict';
import {spawn,execFileSync} from 'node:child_process';
import {createWriteStream,existsSync,readFileSync,readdirSync} from 'node:fs';
import {join} from 'node:path';
const [shard,owned]=process.argv.slice(2);
const scope=process.env.PULSE_M8_REGRESSION_TEST_NAMES?'FAILED_FOLLOWUP':'FULL';
const kinds=['base','usecase','runtime','http','postgres'];
assert(kinds.includes(shard)&&/^\/data\/temp\/pulse-m8-regression\.[A-Za-z0-9]+$/.test(owned));
const packages=execFileSync('go',['list','-f','{{.ImportPath}}|{{len .TestGoFiles}}|{{len .XTestGoFiles}}','./...'],{encoding:'utf8',maxBuffer:8*1024*1024}).trim().split('\n').map(line=>{const [name,a,b]=line.split('|');return {name,tests:Number(a)+Number(b)>0};});
function family(p){if(p.endsWith('/internal/httpapi'))return 'http';if(p.endsWith('/internal/store/postgres'))return 'postgres';if(p.includes('/internal/usecase/'))return 'usecase';if(p.includes('/internal/store/')||/\/internal\/app(?:\/|$)/.test(p))return 'runtime';return 'base';}
const partition=Object.fromEntries(kinds.map(k=>[k,packages.filter(p=>family(p.name)===k)]));
const all=kinds.flatMap(k=>partition[k].map(p=>p.name));assert.equal(new Set(all).size,packages.length);assert.equal(all.length,packages.length);
const selected=partition[shard];assert(selected.length>0);
console.log(`M8_REGRESSION_PACKAGE_COVERAGE shard=${shard} all_packages=${packages.length} all_with_tests=${packages.filter(p=>p.tests).length} selected=${selected.length} selected_with_tests=${selected.filter(p=>p.tests).length} partition_exact=true`);
const groups=[];
if(shard==='http'||shard==='postgres'){
 assert.equal(selected.length,1);
 let names=execFileSync('go',['test','-race','-p=1','-list','.',selected[0].name],{encoding:'utf8',maxBuffer:8*1024*1024}).split('\n').filter(s=>/^(Test|Example|Fuzz)[A-Za-z0-9_]*$/.test(s));
 assert(names.length>0&&new Set(names).size===names.length);
 if(scope==='FAILED_FOLLOWUP'){
  assert.equal(shard,'postgres');
  const wanted=process.env.PULSE_M8_REGRESSION_TEST_NAMES.split('|');
  assert(wanted.length>0&&wanted.every(n=>/^Test[A-Za-z0-9_]+$/.test(n))&&new Set(wanted).size===wanted.length);
  assert(wanted.every(n=>names.includes(n)),'unknown follow-up test');
  names=names.filter(n=>wanted.includes(n));
 }
 console.log(`M8_REGRESSION_SELECTION scope=${scope} shard=${shard} parents=${names.length}`);
 for(let i=0;i<8;i++){const tests=names.filter((_,n)=>n%8===i);if(tests.length)groups.push({label:String(i+1),pkgs:[selected[0].name],tests});}
 assert.equal(groups.flatMap(g=>g.tests).length,names.length);
 console.log(`M8_REGRESSION_TEST_COVERAGE shard=${shard} groups=${groups.length} planned_parents=${names.length} unique=true`);
}else groups.push({label:'all',pkgs:selected.map(p=>p.name)});
let passed=0,skipped=0,failed=0,parentPassed=0,parentSkipped=0,hadFailure=false;
for(const g of groups){
 const oldFiles=readdirSync(owned).filter(n=>n===`${shard}-${g.label}.jsonl`||n.startsWith(`${shard}-${g.label}-`)).sort();
 const prior=oldFiles.flatMap(n=>readFileSync(join(owned,n),'utf8').split('\n').flatMap(line=>{try{return [JSON.parse(line)];}catch{return [];}}));
 const complete=new Set(prior.filter(e=>!e.Test&&e.Action==='pass').map(e=>e.Package));
 if(g.tests&&g.tests.some(t=>!prior.some(e=>e.Test===t&&['pass','skip'].includes(e.Action))))complete.clear();
 const previous=new Map();for(const e of prior)if(e.Test&&complete.has(e.Package)&&['pass','skip'].includes(e.Action))previous.set(`${e.Package}/${e.Test}`,e);
 for(const e of previous.values()){if(e.Action==='pass'){passed++;if(!e.Test.includes('/'))parentPassed++;}else{skipped++;if(!e.Test.includes('/'))parentSkipped++;}}
 if(complete.size)console.log(`M8_REGRESSION_COMPLETED_PACKAGES_REUSED shard=${shard} group=${g.label} packages=${complete.size} tests=${previous.size} incomplete_packages_repeated=true`);
 g.pkgs=g.pkgs.filter(p=>!complete.has(p));
 if(!g.pkgs.length){console.log(`M8_REGRESSION_GROUP_DONE shard=${shard} group=${g.label} ok=true resumed_complete=true coverage_complete=true`);continue;}
 const log=createWriteStream(join(owned,`${shard}-${g.label}-${Date.now()}.jsonl`));
 let buffer='',race=false,parseErrors=0;const seen=new Set(),seenPkgs=new Set([...complete]),bad=new Set(),details=new Map();
 const args=['test','-race','-json','-count=1','-p=1',`-timeout=${shard==='runtime'?'45m':'20m'}`];if(g.tests)args.push('-run',`^(${g.tests.join('|')})$`);args.push(...g.pkgs);
 const started=Date.now();
 const code=await new Promise((resolve,reject)=>{
  const child=spawn('go',args,{stdio:['ignore','pipe','pipe']});child.stdout.setEncoding('utf8');child.stderr.setEncoding('utf8');
  child.stderr.on('data',s=>{log.write(s);if(s.includes('WARNING: DATA RACE'))race=true;});
  child.stdout.on('data',s=>{log.write(s);buffer+=s;let n;while((n=buffer.indexOf('\n'))>=0){const line=buffer.slice(0,n);buffer=buffer.slice(n+1);if(!line)continue;let e;try{e=JSON.parse(line);}catch{parseErrors++;continue;}if(e.Package)seenPkgs.add(e.Package);if(e.Test&&!e.Test.includes('/')&&['pass','skip','fail'].includes(e.Action))seen.add(e.Test);if(e.Output?.includes('WARNING: DATA RACE'))race=true;if(e.Output&&e.Test){let d=details.get(e.Test)||[];d.push(e.Output.trim());details.set(e.Test,d.slice(-6));}if(e.Action==='pass'&&e.Test){passed++;if(!e.Test.includes('/'))parentPassed++;}if(e.Action==='skip'&&e.Test){skipped++;if(!e.Test.includes('/'))parentSkipped++;}if(e.Action==='fail'){if(e.Test)failed++;bad.add(e.Test||e.Package);}}});
  child.once('error',reject);child.once('close',c=>log.end(()=>resolve(c??1)));
 });
 const missing=g.tests?g.tests.filter(t=>!seen.has(t)):g.pkgs.filter(p=>!seenPkgs.has(p));
 const ok=code===0&&!race&&bad.size===0&&missing.length===0&&parseErrors===0;
 if(!ok){hadFailure=true;console.log(`M8_REGRESSION_GROUP_FAILED shard=${shard} group=${g.label} exit=${code} races=${race} parse_errors=${parseErrors} missing=${missing.length}`);for(const t of bad){console.log(`FAILED_TEST ${t}`);for(const d of details.get(t)||[]){if(/_test\.go:\d+:/.test(d)){const safe=/https?:\/\/|postgres:\/\/|password|passkey|cookie|api.?key/i.test(d)?'[sensitive diagnostic omitted]':d.slice(0,600);console.log(`DIAGNOSTIC ${safe}`);}}}for(const t of missing)console.log(`NOT_EXECUTED ${t}`);}
 console.log(`M8_REGRESSION_GROUP_DONE shard=${shard} group=${g.label} ok=${ok} elapsed_seconds=${((Date.now()-started)/1000).toFixed(2)} passed_cumulative=${passed} skipped_cumulative=${skipped} failed_cumulative=${failed} coverage_complete=${missing.length===0}`);
}
console.log(`M8_REGRESSION_SHARD_${hadFailure?'FAILED':'PASS'} shard=${shard} passed=${passed} skipped=${skipped} failed=${failed} parents_passed=${parentPassed} parents_skipped=${parentSkipped} race=true PG=18.6 gated_skips_not_passed=true milestone=NOT_RELEASED`);
if(hadFailure)process.exitCode=1;
