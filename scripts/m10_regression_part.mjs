import assert from 'node:assert/strict';
import {spawn, execFileSync} from 'node:child_process';
import {createHash} from 'node:crypto';

const [shard, partText] = process.argv.slice(2);
assert(['runtime','http','postgres'].includes(shard));
const match = /^(\d+)\/(\d+)$/.exec(partText ?? '');
assert(match, 'expected part/total');
const [, index, total] = match.map(Number);
assert(index >= 1 && index <= total && total <= 64);
function family(p) {
  if(p.endsWith('/internal/httpapi')) return 'http';
  if(p.endsWith('/internal/store/postgres')) return 'postgres';
  if(p.includes('/internal/usecase/')) return 'usecase';
  if(p.includes('/internal/store/') || /\/internal\/app(?:\/|$)/.test(p)) return 'runtime';
  return 'base';
}
const packages = execFileSync('go',['list','./...'],{encoding:'utf8',timeout:120000}).trim().split('\n').filter(p=>family(p)===shard).sort();
const manifests = packages.map(pkg=>({pkg,tests:execFileSync('go',['test','-race','-list','.',pkg],{encoding:'utf8',timeout:180000,maxBuffer:8*1024*1024}).split('\n').filter(s=>/^(Test|Example|Fuzz)[A-Za-z0-9_]*$/.test(s)).sort()}));
const all = manifests.flatMap(g=>g.tests.map(name=>`${g.pkg}/${name}`));
assert(all.length > 0 && new Set(all).size === all.length);
const digest = createHash('sha256').update(JSON.stringify(all)).digest('hex');
const selected = new Set(all.filter((_,i)=>i%total===index-1));
assert(selected.size>0);
console.log(`M10_PART_MANIFEST shard=${shard} part=${index}/${total} manifest=${digest} parents=${all.length} selected=${selected.size} partition=EXACT_MODULO`);
let passed=0, skipped=0, failed=false;
const completed = new Set();
let active;
function stop(signal) { if(active) {try{process.kill(-active.pid,signal);}catch(e){if(e.code!=='ESRCH')throw e;}} }
process.on('SIGTERM',()=>{stop('SIGTERM');process.exitCode=143;});
process.on('SIGINT',()=>{stop('SIGINT');process.exitCode=130;});
for(const g of manifests) {
  const tests=g.tests.filter(n=>selected.has(`${g.pkg}/${n}`));
  if(!tests.length)continue;
  let buffer='', race=false, parseErrors=0;
  const bad=new Set(), diagnostics=new Map();
  const packageTail=[];
  const exit=await new Promise((resolve,reject)=>{
    active=spawn('go',['test','-race','-json','-count=1','-p=1','-timeout=10m','-run',`^(${tests.join('|')})$`,g.pkg],{detached:true,stdio:['ignore','pipe','pipe']});
    const deadline=setTimeout(()=>stop('SIGKILL'),660000);
    const consume=line=>{
      if(!line)return;
      let e;try{e=JSON.parse(line);}catch{parseErrors++;return;}
      if(e.Output?.includes('WARNING: DATA RACE'))race=true;
      if(e.Output){packageTail.push(e.Output.trim());if(packageTail.length>160)packageTail.shift();}
      if(e.Test&&e.Output){const lines=diagnostics.get(e.Test)||[];lines.push(e.Output.trim());diagnostics.set(e.Test,lines.slice(-5));}
      if(e.Action==='fail')bad.add(e.Test||e.Package);
      if(e.Test&&!e.Test.includes('/')&&['pass','skip','fail'].includes(e.Action)){
        const key=`${e.Package}/${e.Test}`;
        assert(selected.has(key)&&!completed.has(key),`unexpected or duplicate parent ${key}`);completed.add(key);
        if(e.Action==='pass')passed++;else if(e.Action==='skip')skipped++;
      }
    };
    active.stdout.setEncoding('utf8');active.stdout.on('data',s=>{buffer+=s;let n;while((n=buffer.indexOf('\n'))>=0){consume(buffer.slice(0,n));buffer=buffer.slice(n+1);}});
    active.stderr.setEncoding('utf8');active.stderr.on('data',s=>{if(s.includes('WARNING: DATA RACE'))race=true;});
    active.once('error',reject);active.once('close',c=>{clearTimeout(deadline);consume(buffer);active=undefined;resolve(c??1);});
  });
  if(exit!==0||race||parseErrors||bad.size){
    failed=true;console.log(`M10_PART_PACKAGE_FAILED package=${g.pkg} exit=${exit} race=${race} parse_errors=${parseErrors}`);
    for(const line of packageTail)if(/panic: test timed out|_test\.go:\d+/.test(line)&&!/(?:https?:\/\/|postgres:\/\/|passkey|cookie|api.?key)/i.test(line))console.log(`PACKAGE_DIAGNOSTIC ${line.slice(0,800)}`);
    for(const test of bad){console.log(`FAILED_TEST ${test}`);for(const line of diagnostics.get(test)||[])if(/_test\.go:\d+:/.test(line))console.log(/https?:\/\/|postgres:\/\/|password|passkey|cookie|api.?key/i.test(line)?'DIAGNOSTIC [sensitive diagnostic omitted]':`DIAGNOSTIC ${line.slice(0,800)}`);}
  }
  if(process.exitCode)break;
}
const missing=[...selected].filter(n=>!completed.has(n));
for(const name of missing)console.log(`NOT_EXECUTED ${name}`);
failed ||= missing.length>0 || Boolean(process.exitCode);
console.log(`M10_PART_${failed?'FAILED':'PASS'} shard=${shard} part=${index}/${total} manifest=${digest} parents=${all.length} passed=${passed} skipped=${skipped} completed=${completed.size} selected=${selected.size} race=true gated_skips=SEPARATE`);
if(failed)process.exitCode=1;
