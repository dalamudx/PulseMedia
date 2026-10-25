import { test } from 'node:test';
import assert from 'node:assert/strict';
import { spawn, spawnSync } from 'node:child_process';
import { mkdtemp, mkdir, copyFile, writeFile, readFile, rm, access, symlink } from 'node:fs/promises';
import { join, resolve } from 'node:path';
import { setTimeout as delay } from 'node:timers/promises';
import net from 'node:net';

const repo = resolve(import.meta.dirname, '..');
const fake = `#!/usr/bin/env node
const fs = require('node:fs'), path = require('node:path'), cp = require('node:child_process');
const name = path.basename(process.argv[1]), args = process.argv.slice(2), state = process.env.TEST_STATE;
const mode = process.env.TEST_MODE;
fs.appendFileSync(path.join(state, 'calls'), JSON.stringify({name,args})+'\\n');
if (name === 'mktemp') {
 const r=cp.spawnSync('/usr/bin/mktemp',args,{encoding:'utf8'});
 if(r.status===0) fs.writeFileSync(path.join(state,'runtime'),r.stdout.trim());
 process.stdout.write(r.stdout||''); process.exit(r.status??1);
}
if (name === 'fuser' || name === 'psql') process.exit(97);
if (name === 'sleep') process.exit(0);
if (name === 'curl') { if(args.some(a=>a.endsWith('/transmission/rpc'))) process.stdout.write('409'); process.exit(0); }
if (name === 'podman') {
 const option=k=>args[args.indexOf(k)+1];
 if(args[0]==='run') {
  const kind=option('--name').includes('-qb-')?'qb':'tr';
  const id=(kind==='qb'?'a':'b').repeat(64), owner=option('--label').split('=')[1];
  fs.writeFileSync(option('--cidfile'),id);
  fs.writeFileSync(path.join(state,id),owner);
  if(mode==='partial-container-failure'&&kind==='tr') process.exit(9);
  process.stdout.write(id); process.exit(0);
 }
 if(args[0]==='inspect') { process.stdout.write(mode==='owner-mismatch'?'another-owner':fs.readFileSync(path.join(state,args.at(-1)),'utf8')); process.exit(0); }
 if(args[0]==='stop'||args[0]==='rm') process.exit(0);
 process.exit(96);
}
if(name==='go') {
 const output=args[args.indexOf('-o')+1];
 fs.writeFileSync(output, '#!/usr/bin/env node\\nconst fs=require("node:fs"),p=require("node:path");fs.writeFileSync(p.join(process.env.TEST_STATE,"air.address"),process.env.PULSE_HTTP_ADDR);fs.writeFileSync(p.join(process.env.TEST_STATE,"air.args"),JSON.stringify(process.argv.slice(2)));fs.writeFileSync(p.join(process.env.TEST_STATE,"air.child"),String(process.pid));const t=setInterval(()=>{},1000);process.on("SIGTERM",()=>{clearInterval(t);process.exit(0);});\\n', {mode:0o755});
 process.exit(0);
}
if(name==='air'||name==='pnpm') {
 fs.writeFileSync(path.join(state,name+'.pid'),String(process.pid));
 if(mode===name+'-failure') {
  const wait=()=>{if(fs.existsSync(path.join(state,(name==='air'?'pnpm':'air')+'.pid')))process.exit(7);else setTimeout(wait,10);};wait();
 } else {
  const child=cp.spawn(process.execPath,['-e',"setInterval(()=>{},1000)"],{stdio:'ignore'});
  fs.writeFileSync(path.join(state,name+'.child'),String(child.pid));
  const interval=setInterval(()=>{},1000);
  process.on('SIGTERM',()=>{clearInterval(interval);child.kill('SIGTERM');child.once('exit',()=>process.exit(0));if(child.exitCode!==null)process.exit(0);});
 }
} else process.exit(95);
`;

async function until(check, description) {
  const deadline = Date.now() + 15000;
  while (Date.now() < deadline) {
    if (await check()) return;
    await delay(20);
  }
  throw Error(`timed out: ${description}`);
}
async function exists(path) { try { await access(path); return true; } catch { return false; } }
async function alive(pid) { try { process.kill(Number(pid), 0); return true; } catch (e) { if(e.code === 'ESRCH') return false; throw e; } }
async function fixture(mode, body, vitePort = '17300') {
  const work = await mkdtemp('/data/temp/pulse-dev-test.');
  const bin = join(work, 'bin'), scripts = join(work, 'scripts');
  await Promise.all([mkdir(bin), mkdir(scripts), mkdir(join(work, 'web'))]);
  await mkdir(join(work, 'data', 'downloads'), {recursive:true});
  await writeFile(join(work, 'data', 'downloads', 'existing.media'), 'pre-existing-download');
  await Promise.all(['dev.sh', 'ensure-dev-downloaders.sh'].map(file => copyFile(join(repo, 'scripts', file), join(scripts, file))));
  await writeFile(join(scripts, 'ensure-dev-db.sh'), '#!/usr/bin/env bash\n[[ "$TEST_MODE" != db-failure ]]\n', {mode: 0o755});
  await copyFile(join(repo, '.air.toml'), join(work, '.air.toml'));
  await Promise.all(['podman', 'curl', 'air', 'pnpm', 'fuser', 'psql', 'mktemp', 'sleep', 'go'].map(name => writeFile(join(bin, name), fake, {mode: 0o755})));
  if (mode === 'real-air') {
    const realAir = spawnSync('which', ['air'], {encoding:'utf8'}).stdout.trim();
    assert.ok(realAir, 'mise-managed Air is required');
    await rm(join(bin, 'air'));
    await symlink(realAir, join(bin, 'air'));
  }
  const child = spawn('bash', [join(scripts, 'dev.sh')], { env: {...process.env, PATH: `${bin}:${process.env.PATH}`, TEST_STATE: work, TEST_MODE: mode,
    PULSE_DATA_DIR: join(work, 'data'), PULSE_DATABASE_URL: 'postgres://private-user:synthetic-secret@127.0.0.1/private-db?password=synthetic-query-secret',
    PULSE_HTTP_ADDR: '127.0.0.1:17888', PULSE_BACKEND_URL: 'http://127.0.0.1:17888', VITE_PORT: vitePort, PULSE_DEV_QB_PORT: '18085', PULSE_DEV_TR_PORT: '19091',
    PULSE_M4_QB_IMAGE: 'owned/qb@sha256:fixed-qb', PULSE_M4_TR_IMAGE: 'owned/tr@sha256:fixed-tr'}, stdio: ['ignore', 'pipe', 'pipe'] });
  let output = '';
  child.stdout.on('data', data => output += data);
  child.stderr.on('data', data => output += data);
  const done = new Promise((accept, reject) => { child.once('error', reject); child.once('close', (code, signal) => accept({code,signal})); });
  const state = {work, child, done, get output() { return output; },
    async runtime() { return (await readFile(join(work, 'runtime'), 'utf8')).trim(); },
    async calls() { return (await readFile(join(work, 'calls'), 'utf8')).trim().split('\n').map(line => JSON.parse(line)); },
    async ready() { await until(async () => await exists(join(work,'air.child')) && await exists(join(work,'pnpm.child')), 'both owned process trees'); }
  };
  try { await body(state); }
  finally {
    if(child.exitCode === null && child.signalCode === null) child.kill('SIGTERM');
    let deadline;
    try { await Promise.race([done, new Promise((_, reject) => { deadline = setTimeout(() => reject(Error('owned development process did not stop')), 10000); })]); }
    finally { clearTimeout(deadline); }
    for (const name of ['pnpm', 'air']) for (const suffix of ['pid','child']) {
      const path=join(work,`${name}.${suffix}`);
      if(await exists(path)) await until(async()=>!await alive(await readFile(path,'utf8')), `${name} ${suffix} stopped`);
    }
    if (await exists(join(work,'runtime'))) {
      const runtime=await state.runtime();
      assert.match(runtime,/^\/data\/temp\/pulse-dev\.[A-Za-z0-9]+$/);
      await rm(runtime,{recursive:true,force:true});
      assert.equal(await exists(runtime),false);
    }
    await rm(work,{recursive:true,force:true});
    assert.equal(await exists(work),false);
  }
}

for (const signal of ['SIGTERM','SIGINT']) test(`development ${signal} stops owned descendants, preserves unrelated listener and hides credentials`, async () => {
  const listener=net.createServer();await new Promise(resolve=>listener.listen(0,'127.0.0.1',resolve));
  try { await fixture('normal',async f=>{
    await f.ready(); f.child.kill(signal);
    const result=await f.done;assert.equal(result.code,signal==='SIGTERM'?143:130);
    assert.equal(await exists(await f.runtime()),false);
    assert.doesNotMatch(f.output,/synthetic-secret|synthetic-query-secret|private-user|postgres:\/\//);
    assert.match(f.output,/127\.0\.0\.1:17888/);
    const calls=await f.calls(),runs=calls.filter(c=>c.name==='podman'&&c.args[0]==='run');
    assert.equal(runs.length,2);
    assert.ok(runs[0].args.includes('127.0.0.1:18085:18085'));assert.ok(runs[1].args.includes('127.0.0.1:19091:9091'));
    assert.ok(runs.every(c=>c.args.at(-1).includes('@sha256:')));
    assert.equal(calls.filter(c=>c.name==='podman'&&c.args[0]==='stop').length,2);
    assert.equal(calls.filter(c=>c.name==='podman'&&c.args[0]==='rm').length,2);
    assert.ok(calls.filter(c=>c.name==='podman'&&['stop','rm'].includes(c.args[0])).every(c=>/^[ab]{64}$/.test(c.args.at(-1))));
    assert.equal(calls.some(c=>['fuser','psql'].includes(c.name)),false);
    const frontend=calls.find(c=>c.name==='pnpm');assert.ok(frontend.args.includes('--strictPort'));
    const backend=calls.find(c=>c.name==='air');assert.ok(backend.args.includes('--tmp_dir'));assert.ok(!backend.args.includes('--http-addr'));
    assert.equal(listener.listening,true);
    assert.equal(await readFile(join(f.work,'data','downloads','existing.media'),'utf8'),'pre-existing-download');
  }, String(listener.address().port)); } finally { await new Promise(resolve=>listener.close(resolve)); }
});

for (const mode of ['air-failure','pnpm-failure','partial-container-failure','db-failure']) test(`${mode} fails visibly and cleans only current startup`,async()=>{
  await fixture(mode,async f=>{
    const result=await f.done;assert.notEqual(result.code,0);
    assert.equal(await exists(await f.runtime()),false);
    const calls=await f.calls();assert.equal(calls.some(c=>['fuser','psql'].includes(c.name)),false);
    if(mode==='partial-container-failure') assert.equal(calls.filter(c=>c.name==='podman'&&c.args[0]==='rm').length,2);
    if(mode==='db-failure') assert.equal(calls.some(c=>c.name==='podman'),false);
  });
});

test('container owner mismatch refuses deletion and retains the original cleanup evidence',async()=>{
  await fixture('owner-mismatch',async f=>{
    await f.ready();f.child.kill('SIGTERM');assert.equal((await f.done).code,1);
    assert.equal(await exists(await f.runtime()),true);
    assert.match(f.output,/清理未完成/);
    assert.equal((await f.calls()).some(c=>c.name==='podman'&&['stop','rm'].includes(c.args[0])),false);
  });
});

test('actual mise-managed Air starts the generated application with the configured endpoint',async()=>{
  await fixture('real-air',async f=>{
    await f.ready();
    assert.equal(await readFile(join(f.work,'air.address'),'utf8'),'127.0.0.1:17888');
    assert.deepEqual(JSON.parse(await readFile(join(f.work,'air.args'),'utf8')),[]);
    f.child.kill('SIGTERM');assert.equal((await f.done).code,143);
    assert.equal(await exists(await f.runtime()),false);
  });
});
