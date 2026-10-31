import assert from "node:assert/strict";
import { mkdtemp, rm } from "node:fs/promises";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { createServer } from "vite";
import { chromium } from "playwright-core";
const tempRoot=process.env.TMPDIR;
assert.ok(tempRoot?.startsWith("/data/temp/"),"Set TMPDIR to the authorized /data/temp directory");
const work=await mkdtemp(join(tempRoot,"tv-upgrade-browser-"));
const root=fileURLToPath(new URL("..",import.meta.url));
const id=n=>`00000000-0000-0000-0000-${n.toString(16).padStart(12,"0")}`;
const scope={unit_id:id(3),current_file_id:id(4),candidate_id:id(5)};
const actions=["reserve","prepare","send","observe","organization-preview","organization-confirm","organize","retirement-preview","retirement-confirm","retire"];
const labels=["冻结单集升级预留","取源准备","发送升级下载","观察原下载与文件清单","读取整理复核","确认并准备整理计划","执行整理并核验入库","读取旧版本退休复核","确认旧版本退休计划","执行旧版本退休"];
const statuses=["reserved_not_executable","prepared_not_sent","tracking","organization_review_required","organization_preview_not_executable","organization_confirmed_not_executable","imported_pending_retirement","retirement_preview_not_executable","retirement_confirmed_not_executable","retired"];
const versions=["preview:v1","preview:v1","preview:v1","preview:v1","organization:v2","organization:v2","organization:v2","retirement:v3","retirement:v3","retirement:v3"];
const commands=new Map(),requests=[];let lostSend=false,lostReserve=false;
const entry=`import React from'react';import{createRoot}from'react-dom/client';import{TVEpisodeUpgradePanel}from'/src/features/configuration/TVEpisodeUpgradePanel.tsx';createRoot(document.getElementById('root')).render(React.createElement(TVEpisodeUpgradePanel,${JSON.stringify({subscriptionId:id(2),unitId:scope.unit_id,currentFileId:scope.current_file_id,candidateId:scope.candidate_id})}));`;
const transport=`async function request(method,path,options){const scenario=new URLSearchParams(location.search).get('scenario');const response=await fetch(method==='GET'?'/mock?receipt='+encodeURIComponent(options.params.path.key)+'&scenario='+encodeURIComponent(scenario??''):'/mock',{method,...(method==='POST'?{body:JSON.stringify({path,body:options.body,key:options.headers?.['Idempotency-Key'],scenario})}:{}),signal:options.signal});const data=await response.json();return{response,data:response.ok?data:undefined,error:response.ok?undefined:data};}export const api={POST:(path,options)=>request('POST',path,options),GET:(path,options)=>request('GET',path,options)};`;
const auth=`export function useAuth(){return{user:{id:'${id(1)}',role:new URLSearchParams(location.search).has('member')?'member':'admin'},checking:false,statusError:null}}`;
let server,browser;
try{
 server=await createServer({configFile:false,root,cacheDir:join(work,"vite"),optimizeDeps:{noDiscovery:true,include:["react","react-dom/client","react/jsx-dev-runtime"]},resolve:{alias:[{find:"@/api/client",replacement:"\0mp22-client"},{find:"@/features/auth/AuthContext",replacement:"\0mp22-auth"}]},server:{host:"127.0.0.1",port:0},plugins:[{
  name:"mp22-fixture",resolveId(v){if(v.startsWith("\0mp22-"))return v;},load(v){return v==="\0mp22-entry"?entry:v==="\0mp22-auth"?auth:v==="\0mp22-client"?transport:undefined;},configureServer(s){s.middlewares.use(async(req,res,next)=>{
   if(req.url?.startsWith('/mp22')){res.setHeader('content-type','text/html');res.end('<!doctype html><html lang="zh"><meta name="viewport" content="width=device-width,initial-scale=1"><link rel="stylesheet" href="/src/assets/main.css"><div id="root"></div><script type="module" src="/@id/__x00__mp22-entry"></script></html>');return;}
   if(req.url?.startsWith('/mock?receipt=')){const query=new URL(req.url,'http://localhost').searchParams,key=query.get('receipt');assert.equal(req.method,'GET');assert.ok(commands.has(key));requests.push({method:'GET',key,scenario:query.get('scenario')});const receipt=commands.get(key);res.setHeader('content-type','application/json');res.end(JSON.stringify(receipt.status==='tracking'?{...receipt,status:'pending_requires_observation'}:receipt));return;}
   if(req.url!=="/mock")return next();
   try{let raw="";for await(const chunk of req)raw+=chunk;const r=JSON.parse(raw);requests.push(r);let data;
    assert.match(r.path,/^\/api\/v1\/subscriptions\/\{id\}\/tv-episode-upgrade-(previews|commands)$/);
    if(r.path.endsWith("-previews")){assert.deepEqual(r.body,scope);data={...scope,status:"preview_not_executable",version:versions[0],comparable:true,improvement:true,current_score:10,candidate_score:20,execution_allowed:false};}
    else{
     const index=actions.indexOf(r.body?.action);assert.ok(index>=0);assert.equal(r.body.confirmed,true);assert.ok(r.key);
     assert.deepEqual(Object.keys(r.body).sort(),(index===0?["action","unit_id","current_file_id","candidate_id","expected_version","confirmed"]:["action","unit_id","current_file_id","candidate_id","operation_id","expected_version","confirmed"]).sort());
     assert.equal(r.body.expected_version,index<=4?versions[0]:index<=7?versions[4]:versions[7]);
     assert.deepEqual(Object.fromEntries(Object.keys(scope).map(k=>[k,r.body[k]])),scope);if(index>0)assert.equal(r.body.operation_id,id(10));
     if(commands.has(r.key)){assert.equal(r.scenario,"reserve-lost");assert.equal(r.body?.action,"reserve");data=commands.get(r.key);}
     else{data={...scope,status:statuses[index],operation_id:id(10),version:versions[index],execution_allowed:false,...(index>=6?{new_file_id:id(11)}:{})};commands.set(r.key,data);}
     res.statusCode=202;
     if(r.body?.action==="send"&&!lostSend){lostSend=true;res.statusCode=503;data={code:"LOST_RESPONSE"};}
     if(r.body?.action==="reserve"&&r.scenario==="reserve-lost"&&!lostReserve){lostReserve=true;res.statusCode=503;data={code:"LOST_RESPONSE"};}
    }
    res.setHeader('content-type','application/json');res.end(JSON.stringify(data));
   }catch(error){res.statusCode=500;res.end(JSON.stringify({error:String(error)}));}
  });}
 }]});await server.listen();const address=server.httpServer.address();const url=`http://127.0.0.1:${address.port}/mp22`;
 browser=await chromium.launch({executablePath:process.env.CHROME_BIN||"/usr/bin/google-chrome",headless:true,args:["--no-sandbox","--disable-dev-shm-usage"]});
 const page=await browser.newPage();page.setDefaultTimeout(7000);const errors=[];page.on('pageerror',e=>errors.push(String(e)));
 await page.goto(url);await page.getByRole('heading',{name:'TV 单集品质升级'}).waitFor();assert.equal(requests.length,0,"mount must perform no requests");
 await page.getByRole('button',{name:'读取升级比较',exact:true}).click();
 for(let i=0;i<actions.length;i++){
  if([3,4,7].includes(i))await page.getByRole('button',{name:labels[i],exact:true}).click();
  else{await page.getByRole('checkbox',{name:`我确认：${labels[i]}`,exact:true}).check();await page.getByRole('button',{name:labels[i],exact:true}).click();}
  if(i===2){await page.getByRole('alert').waitFor();await page.reload();await page.getByRole('button',{name:'读取原命令回执',exact:true}).click();await page.getByText('原命令仍待观察，未获得下一步许可。',{exact:true}).waitFor();await page.getByText('发送不会重试。请观察原下载；无法确认时保留原保护并转人工处理。',{exact:true}).waitFor();assert.equal(await page.getByRole('button',{name:'发送升级下载',exact:true}).count(),0);}
  else await page.getByText('原命令回执已保存，请继续核对下一步。',{exact:true}).waitFor();
 }
 await page.getByText('旧版本已移入受控回收区。',{exact:true}).waitFor();
 assert.equal(requests.filter(r=>r.body?.action==="send").length,1);assert.equal(commands.size,10);
 assert.deepEqual(requests.filter(r=>r.body?.action).map(r=>r.body?.action),actions);
 for(const width of [1280,390,320]){await page.setViewportSize({width,height:850});assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth),false,`overflow at ${width}`);}
 const before=requests.length;await page.goto(url+'?member');await page.waitForLoadState('networkidle');assert.equal(await page.getByRole('heading',{name:'TV 单集品质升级'}).count(),0);assert.equal(requests.length,before);
 // A different context has no local history. Explicitly recover a lost reserve with its original key.
 const context=await browser.newContext(),recoverPage=await context.newPage();recoverPage.setDefaultTimeout(7000);
 await recoverPage.goto(url+'?scenario=reserve-lost');await recoverPage.getByRole('button',{name:'读取升级比较',exact:true}).click();await recoverPage.getByRole('checkbox',{name:'我确认：冻结单集升级预留',exact:true}).check();await recoverPage.getByRole('button',{name:'冻结单集升级预留',exact:true}).click();await recoverPage.getByRole('alert').waitFor();
 await recoverPage.reload();await recoverPage.getByRole('button',{name:'读取原命令回执',exact:true}).click();await recoverPage.getByRole('checkbox',{name:'我确认：取源准备',exact:true}).waitFor();
 const reserveRequests=requests.filter(r=>r.scenario==='reserve-lost'&&r.body?.action==='reserve');assert.equal(reserveRequests.length,1);const receiptRequests=requests.filter(r=>r.method==='GET'&&r.scenario==='reserve-lost');assert.equal(receiptRequests.length,1);assert.equal(reserveRequests[0].key,receiptRequests[0].key);await context.close();
 assert.deepEqual(errors,[]);console.log('PASS Chrome: two POST endpoints plus explicit GET receipts, exact action order and changing review versions, lost send + reload + observation, no resend, GET original-key reserve recovery and unresolved send permit, admin-only, 1280/390/320px, zero page errors');
}finally{await browser?.close();await server?.close();await rm(work,{recursive:true,force:true});}
