import {test,after} from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync,realpathSync} from 'node:fs';
import {stripTypeScriptTypes} from 'node:module';
import {createServer} from 'node:http';
import {pathToFileURL} from 'node:url';
import path from 'node:path';
// No application cookies, credentials, existing browser profiles, or external URLs.
const root=realpathSync(process.env.PULSE_REVIEW_TEST_ROOT);
if(!root.startsWith('/data/temp/'))throw Error('PULSE_REVIEW_TEST_ROOT must be a task-owned temporary directory');
for(const name of ['TMPDIR','TMP','TEMP']){const value=process.env[name];if(!value||realpathSync(value)!==root)throw Error(`${name} must equal the owned temporary root`)}
const modulePath=process.env.PULSE_REVIEW_PLAYWRIGHT_MODULE;
const dependencyRoot=realpathSync(new URL('../node_modules',import.meta.url));
if(!modulePath||!realpathSync(modulePath).startsWith(dependencyRoot+path.sep))throw Error('PULSE_REVIEW_PLAYWRIGHT_MODULE must use the installed pinned dependency');
const {chromium}=await import(pathToFileURL(modulePath));
const source=readFileSync(new URL('../src/features/configuration/singleEpisodeReviewRecovery.ts',import.meta.url),'utf8');
const server=createServer((req,res)=>{
 res.setHeader('Cache-Control','no-store');
 if(req.url.startsWith('/helper.js')){
  res.setHeader('Content-Type','text/javascript');let text=source;
  if(req.url.includes('mutant=denial'))text=text.replace('if(!isRetry&&[400,403,409].includes(reply.status))','if([400,403,409].includes(reply.status))');
  if(req.url.includes('mutant=fence'))text=text.replace('confirmed.set(id,confirmation);','storage.setItem(receiptKey(id),JSON.stringify(reply.data));\n  confirmed.set(id,confirmation);');
  res.end(stripTypeScriptTypes(text));
 }else{res.setHeader('Content-Type','text/html');res.end('<!doctype html><html lang="en"><title>Private review recovery fixture</title><body>Private review recovery fixture</body></html>')}
});
await new Promise(resolve=>server.listen(0,'127.0.0.1',resolve));
let browser;
after(async()=>{try{await browser?.close()}finally{await new Promise(resolve=>server.close(resolve))}});
browser=await chromium.launch({executablePath:process.env.PULSE_REVIEW_CHROME??'/usr/bin/google-chrome',headless:true,args:['--no-sandbox','--disable-dev-shm-usage','--disable-background-networking']});
async function probe(mode,mutant=''){
 const context=await browser.newContext();
 try{
  await context.route('**/*',route=>new URL(route.request().url()).hostname==='127.0.0.1'?route.continue():route.abort());
  const page=await context.newPage();await page.goto(`http://127.0.0.1:${server.address().port}`);
  return await page.evaluate(async({mode,mutant})=>{
   const m=await import(`/helper.js?mutant=${mutant}`);
   const sub='00000000-0000-4000-8000-000000000001';
   const command={key:'00000000-0000-4000-8000-000000000002',body:{candidate_id:'00000000-0000-4000-8000-000000000003',expected_input_stamp:'a'.repeat(64),confirmed:true,reason:'已人工核对当前资源的作品与单集身份'}};
   const receipt={id:'00000000-0000-4000-8000-000000000004',subscription_id:sub,candidate_id:command.body.candidate_id,unit_id:'00000000-0000-4000-8000-000000000005',input_stamp:command.body.expected_input_stamp,status:'reviewed_not_executable'};
   let posts=0;
   if(mode==='denial'){
    await m.sendEpisodeReview(sub,command,false,sessionStorage,async()=>{posts++;throw Error('commit response lost')});
    const restored=JSON.parse(sessionStorage.getItem(m.reviewCommandKey(sub)));
    const denial=await m.sendEpisodeReview(sub,restored,true,sessionStorage,async()=>{posts++;return {status:403}});
    const retained=sessionStorage.getItem(m.reviewCommandKey(sub))===JSON.stringify(command);
    if(!retained)return {retained,kind:denial.kind,posts};
    const result=await m.sendEpisodeReview(sub,restored,true,sessionStorage,async()=>{posts++;return {status:200,data:receipt}});
    return {retained,kind:result.kind,posts};
   }
   const originalSet=Storage.prototype.setItem,originalRemove=Storage.prototype.removeItem;
   let failing=true;
   Storage.prototype.setItem=function(k,v){if(failing&&mode!=='remove'&&k.includes('episode-review-receipt'))throw new DOMException('fixture quota','QuotaExceededError');return originalSet.call(this,k,v)};
   Storage.prototype.removeItem=function(k){if(failing&&mode==='remove')throw new DOMException('fixture denial','SecurityError');return originalRemove.call(this,k)};
   try{
    const controller=new AbortController();
    const result=await m.sendEpisodeReview(sub,command,false,sessionStorage,async()=>{posts++;if(mode==='abort')controller.abort();return {status:200,data:receipt}});
    // A new consumer importing the same module represents a panel remount.
    const remounted=await import(`/helper.js?mutant=${mutant}`);
    const fenced=!!remounted.confirmedEpisodeReview(sub,command);
    await remounted.sendEpisodeReview(sub,command,true,sessionStorage,async()=>{posts++;return {status:403}});
    failing=false;
    const recovered=await remounted.sendEpisodeReview(sub,command,true,sessionStorage,async()=>{posts++;return {status:403}});
    return {kind:result.kind,fenced,posts,persisted:recovered.confirmation?.persisted,removed:sessionStorage.getItem(m.reviewCommandKey(sub))===null,receiptSaved:sessionStorage.getItem(`pulse:episode-review-receipt:${sub}`)===JSON.stringify(receipt),aborted:controller.signal.aborted};
   }catch(error){return {error:error.name,posts,fenced:!!m.confirmedEpisodeReview(sub,command)}}finally{Storage.prototype.setItem=originalSet;Storage.prototype.removeItem=originalRemove}
  },{mode,mutant});
 }finally{await context.close()}
}
test('browser: lost commit / demotion / restored permission retains exact command',async()=>assert.deepEqual(await probe('denial'),{retained:true,kind:'confirmed',posts:3}));
for(const mode of ['set','remove','abort'])test(`browser: ${mode} failure repairs actual sessionStorage without another POST`,async()=>{
 const result=await probe(mode);assert.equal(result.kind,'confirmed');assert.equal(result.fenced,true);assert.equal(result.posts,1);assert.equal(result.persisted,true);assert.equal(result.removed,true);assert.equal(result.receiptSaved,true);assert.equal(result.aborted,mode==='abort');
});
test('browser negative control detects unconditional retry denial cleanup',async()=>assert.deepEqual(await probe('denial','denial'),{retained:false,kind:'rejected',posts:2}));
test('browser negative control detects storage preceding confirmation fence',async()=>{
 const result=await probe('set','fence');assert.equal(result.error,'QuotaExceededError');assert.equal(result.fenced,false);assert.equal(result.posts,1);
});

