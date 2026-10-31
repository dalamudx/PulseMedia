import assert from 'node:assert/strict';
import {mkdtemp,readFile,writeFile,rm} from 'node:fs/promises';
import {join,extname} from 'node:path';
import {fileURLToPath} from 'node:url';
import {createServer} from 'node:http';
import {build} from 'vite';
import {chromium} from 'playwright-core';

const temp=process.env.TMPDIR;assert.ok(temp?.startsWith('/data/temp/'));
const work=await mkdtemp(join(temp,'delivery-history-'));
const root=fileURLToPath(new URL('..',import.meta.url));
const id=n=>`00000000-0000-0000-0000-${String(n).padStart(12,'0')}`;
const path='/api/v1/integrations/deliveries';
const requests=[],contexts=[],errors=[],receipts=new Map();
let browser,server,account=1,role='admin',failPage=true,releasePage,queueSends=0,checks=0;
const row=n=>({id:id(n),event_id:id(n+1000),consumer:`notify:${id(800)}`,aggregate_type:'media',aggregate_id:`作品/${'很长的名称'.repeat(4)}/${id(n)}`,sequence:1,state:n===1?'dead':'pending',attempts:n===1?3:0,lease_token:0,updated_at:'2026-10-01T00:00:00Z',not_before:'2026-10-01T00:00:00Z',recipient_name:'家庭通知',duplicate_risk:'receiver_event_id_dedup_required'});
let rows=[];
try{
 const entry=join(work,'entry.tsx');
 await writeFile(entry,`import React from 'react';import{createRoot}from'react-dom/client';import{AuthProvider,useAuth}from'${root}/src/features/auth/AuthContext.tsx';import{DeliveryPanel}from'${root}/src/features/configuration/DeliveryPanel.tsx';import'${root}/src/assets/main.css';import'${root}/src/features/configuration/configuration.css';import'${root}/src/assets/redesign.css';import'${root}/src/assets/workspace.css';function Harness(){const{checkStatus}=useAuth();return React.createElement(React.Fragment,null,React.createElement('button',{onClick:async()=>{await fetch('/fixture-switch');await checkStatus();}},'切换账号'),React.createElement(DeliveryPanel))}createRoot(document.getElementById('root')).render(React.createElement(AuthProvider,null,React.createElement(Harness)));`);
 await build({root,configFile:false,logLevel:'error',define:{'process.env.NODE_ENV':JSON.stringify('production')},resolve:{alias:{'@':join(root,'src'),'react':join(root,'node_modules/react'),'react-dom':join(root,'node_modules/react-dom')}},esbuild:{jsx:'automatic'},build:{outDir:join(work,'dist'),emptyOutDir:true,lib:{entry,name:'DeliveryHistory',formats:['es'],fileName:()=> 'panel.js',cssFileName:'panel'},rollupOptions:{output:{assetFileNames:'[name][extname]'}}}});
 server=createServer(async(req,res)=>{try{
  const url=new URL(req.url,'http://owned.test');const scenario=new URL(req.headers.referer||'http://owned.test').searchParams.get('scenario')||'';
  const json=(value,status=200)=>{res.statusCode=status;res.setHeader('content-type','application/json');res.setHeader('cache-control','no-store');res.end(JSON.stringify(value))};
  if(url.pathname==='/fixture-switch'){account=2;json({});return}
  if(url.pathname==='/api/v1/auth/status'){json({initialized:true,current_user:{id:id(account+9000),username:`user${account}`,role}});return}
  if(url.pathname==='/api/v1/notification-channels'){json([]);return}
  if(url.pathname==='/api/v1/integrations/notification-batches'){const unsealed=scenario==='unsealed';json({items:[{id:id(700),media_id:id(701),channel_id:id(800),event_id:unsealed?null:id(1001),deadline:'2026-10-01T00:00:00Z',member_count:1,unit_count:1,file_count:1,root_count:1,recipient_name:'家庭通知',recipient_enabled:true,state:unsealed?'collecting':rows.find(d=>d.id===id(1))?.state||'dead'}],limit:100});return}
  if(url.pathname.startsWith(path)){
   const actor=account;const chunks=[];for await(const c of req)chunks.push(c);const raw=Buffer.concat(chunks).toString(),body=raw?JSON.parse(raw):undefined;
   const key=req.headers['idempotency-key'];requests.push({scenario,actor,method:req.method,query:url.search,pathname:url.pathname,key,body});
   if(req.method==='GET'&&url.pathname===path){
    const state=url.searchParams.get('state')||'',cursor=url.searchParams.get('cursor')||'',event=url.searchParams.get('event_id')||'';
    if(scenario==='batch-retry'&&event&&failPage){failPage=false;json({code:'UNAVAILABLE'},503);return}
    if(scenario==='late'&&cursor)await new Promise(resolve=>{releasePage=resolve});
    if(scenario==='retry'&&cursor&&failPage){failPage=false;json({code:'UNAVAILABLE'},503);return}
    const selected=(actor===2?[{...row(500),recipient_name:'另一个账号的通知'}]:rows).filter(d=>(!state||d.state===state)&&(!event||d.event_id===event));
    const token=Buffer.from(JSON.stringify({state,...(event?{event_id:event}:{}),after:100})).toString('base64url');if(cursor)assert.equal(cursor,token);
    const start=cursor?100:0;let page={items:selected.slice(start,start+100),limit:100,next_cursor:selected.length>start+100?token:''};
    if(scenario==='malformed')page.next_cursor='x'.repeat(513);
    if(scenario==='wrong-state'&&state==='dead')page.items=[row(2)];
    if(scenario==='post-wrong-event'&&event&&queueSends>0&&failPage){failPage=false;page.items=[row(2)]}
    if(scenario==='wrong-event'&&event)page.items=[row(2)];
    json(page);return
   }
   if(req.method==='POST'&&/\/(replay|skip)$/.test(url.pathname)){
    assert.equal(req.headers['x-csrf-token'],'fixture-csrf');assert.equal(actor,1);assert(key);
    const action=url.pathname.split('/').at(-1),delivery=rows.find(d=>d.id===url.pathname.split('/').at(-2));assert(delivery);
    if(receipts.has(key)){const saved=receipts.get(key);assert.deepEqual(saved.body,body);json(saved.receipt);return}
    assert.equal(delivery.state,'dead');assert.equal(body.expected_token,delivery.lease_token);assert.equal(body.expected_attempts,delivery.attempts);assert.equal(body.expected_updated_at,delivery.updated_at);
    if(action==='replay')assert.equal(body.accept_duplicate_risk,true);else assert.equal(body.accept_delivery_loss,true);
    queueSends++;delivery.state=action==='replay'?'pending':'skipped';delivery.lease_token++;delivery.updated_at='2026-10-02T00:00:00Z';
    const receipt={id:delivery.id,event_id:delivery.event_id,state:delivery.state,lease_token:delivery.lease_token,...(action==='skip'?{reason:body.reason,skipped_at:'2026-10-02T00:00:00Z'}:{})};
    receipts.set(key,{body,receipt});if(scenario==='lost-replay'||scenario==='lost-skip'){json({code:'LOST_RESPONSE'},503);return}json(receipt);return
   }
  }
  if(url.pathname==='/panel.js'||url.pathname.endsWith('.css')){res.setHeader('content-type',extname(url.pathname)==='.js'?'application/javascript':'text/css');res.end(await readFile(join(work,'dist',url.pathname.slice(1))));return}
  if(url.pathname.startsWith('/api/')){json({code:'UNEXPECTED'},404);return}
  res.setHeader('content-type','text/html');res.setHeader('set-cookie','csrf_token=fixture-csrf; SameSite=Strict; Path=/');res.end('<!doctype html><html lang="zh-CN"><meta name="viewport" content="width=device-width,initial-scale=1"><link rel="stylesheet" href="/panel.css"><style>body{margin:0;padding:16px}#root{max-width:1000px;margin:auto}</style><div id="root"></div><script type="module" src="/panel.js"></script></html>');
 }catch(error){errors.push(String(error));res.statusCode=500;res.end('{}')}});
 await new Promise(resolve=>server.listen(0,'127.0.0.1',resolve));const base=`http://127.0.0.1:${server.address().port}`;
 browser=await chromium.launch({executablePath:process.env.PULSE_TEST_BROWSER_EXECUTABLE||'/usr/bin/google-chrome',headless:true,args:['--no-sandbox','--disable-dev-shm-usage']});
 const fresh=async scenario=>{for(const c of contexts.splice(0))await c.close();account=1;role=scenario==='member'?'member':'admin';rows=Array.from({length:121},(_,i)=>row(121-i));failPage=true;receipts.clear();queueSends=0;const context=await browser.newContext();contexts.push(context);const page=await context.newPage();page.on('pageerror',error=>errors.push(String(error)));await page.goto(`${base}/?scenario=${scenario}`);return page};
 const panel=p=>p.getByRole('region',{name:'通知与投递管理',exact:true});
 const ready=async p=>{if(errors.length)throw Error(errors.join("\n"));try{await panel(p).getByRole("article").first().waitFor({timeout:5000})}catch(e){throw new Error(JSON.stringify({pageErrors:errors,requests:requests.map(r=>({method:r.method,path:r.pathname,query:r.query})),text:(await p.locator("body").innerText()).slice(0,800)})+"\n"+e)}};
 let p=await fresh('pagination');await ready(p);let section=panel(p);assert.equal(await section.getByRole('article').count(),100);
 await section.getByRole('button',{name:'下一页投递',exact:true}).click();await section.getByRole('article',{name:`投递 ${id(1)}`,exact:true}).waitFor();assert.equal(await section.getByRole('article').count(),21);checks++;
 await section.getByRole('article',{name:`投递 ${id(1)}`,exact:true}).getByRole('button',{name:'复核此投递重放',exact:true}).click();await section.getByRole('region',{name:'投递重放确认'}).waitFor();
 await section.getByRole('button',{name:'返回最新一页',exact:true}).click();await section.getByRole('article',{name:`投递 ${id(121)}`,exact:true}).waitFor();assert.equal(await section.getByRole('region',{name:'投递重放确认'}).count(),0);checks++;
 await section.getByLabel('按投递状态筛选',{exact:true}).selectOption('dead');await section.getByRole('article',{name:`投递 ${id(1)}`,exact:true}).waitFor();assert.equal(await section.getByRole('article').count(),1);assert.equal(new URLSearchParams(requests.at(-1).query).get('cursor'),null);checks++;
 for(const width of [1280,390,320]){await p.setViewportSize({width,height:1000});await section.getByLabel('按投递状态筛选',{exact:true}).evaluate(el=>el.parentElement.scrollIntoView({block:'start'}));assert.ok(await p.evaluate(()=>document.documentElement.scrollWidth<=innerWidth));assert.ok((await section.getByLabel('按投递状态筛选',{exact:true}).boundingBox()).height>=44);if(process.env.PULSE_DELIVERY_EVIDENCE)await p.screenshot({path:join(process.env.PULSE_DELIVERY_EVIDENCE,`delivery-history-${width}.png`)});}checks++;
 assert.equal(requests.filter(r=>r.scenario==='pagination'&&r.method==='POST').length,0);
 p=await fresh('retry');await ready(p);section=panel(p);await section.getByRole('button',{name:'下一页投递',exact:true}).click();await section.getByRole('alert').waitFor();assert.equal(await section.getByRole('article').count(),0);const failed=new URLSearchParams(requests.at(-1).query).get('cursor');await section.getByRole('button',{name:'读取最新投递状态',exact:true}).click();await section.getByRole('article',{name:`投递 ${id(1)}`,exact:true}).waitFor();assert.equal(new URLSearchParams(requests.at(-1).query).get('cursor'),failed);checks++;
 p=await fresh('batch-link');await ready(p);section=panel(p);await section.getByRole('button',{name:'查看原投递记录',exact:true}).click();await section.getByRole('article',{name:`投递 ${id(1)}`,exact:true}).waitFor();assert.equal(await section.getByRole('article').count(),1);assert.equal(new URLSearchParams(requests.at(-1).query).get('event_id'),id(1001));assert.equal(requests.filter(r=>r.scenario==='batch-link'&&r.method==='POST').length,0);checks++;
 for(const width of [1280,390,320]){
  await p.setViewportSize({width,height:1000});const batch=section.getByRole('region',{name:'媒体通知批次',exact:true});
  assert.ok(await p.evaluate(()=>document.documentElement.scrollWidth<=innerWidth));
  const button=batch.getByRole('button',{name:'查看原投递记录',exact:true});
  const sizing=await button.evaluate(el=>({width:innerWidth,height:el.getBoundingClientRect().height,minHeight:getComputedStyle(el).minHeight,transform:getComputedStyle(el).transform}));
  assert.ok(sizing.height>=44,JSON.stringify(sizing));
  if(process.env.PULSE_DELIVERY_EVIDENCE)await batch.screenshot({path:join(process.env.PULSE_DELIVERY_EVIDENCE,`batch-link-${width}.png`)});
 }checks++;
 await section.getByLabel('按投递状态筛选',{exact:true}).selectOption('pending');await section.getByText('本次读取没有此筛选的投递记录，不表示从未产生事件。',{exact:true}).waitFor();assert.equal(new URLSearchParams(requests.at(-1).query).get('event_id'),id(1001));await section.getByRole('button',{name:'显示全部投递',exact:true}).click();await section.getByRole('article',{name:`投递 ${id(121)}`,exact:true}).waitFor();assert.equal(new URLSearchParams(requests.at(-1).query).get('event_id'),null);assert.equal(await section.getByRole('article').count(),100);checks++;
 p=await fresh('batch-retry');await ready(p);section=panel(p);await section.getByRole('button',{name:'查看原投递记录',exact:true}).click();await section.getByRole('alert').waitFor();assert.equal(await section.getByRole('article').count(),0);await section.getByRole('button',{name:'读取最新投递状态',exact:true}).click();await section.getByRole('article',{name:`投递 ${id(1)}`,exact:true}).waitFor();assert.equal(new URLSearchParams(requests.at(-1).query).get('event_id'),id(1001));checks++;
 p=await fresh('post-wrong-event');await ready(p);section=panel(p);await section.getByRole('button',{name:'查看原投递记录',exact:true}).click();const original=section.getByRole('article',{name:`投递 ${id(1)}`,exact:true});await original.waitFor();await original.getByRole('button',{name:'复核此投递重放',exact:true}).click();const confirmation=section.getByRole('region',{name:'投递重放确认',exact:true});await confirmation.getByRole('checkbox').check();await confirmation.getByRole('button',{name:'确认重新排队原投递',exact:true}).click();await section.getByRole('alert').filter({hasText:'重放已确认，但状态读取失败'}).waitFor();assert.equal(await section.getByRole('article').count(),0);assert.equal(queueSends,1);await section.getByRole('button',{name:'读取最新投递状态',exact:true}).click();await original.waitFor();assert.equal(new URLSearchParams(requests.at(-1).query).get('event_id'),id(1001));assert.equal(requests.filter(r=>r.scenario==='post-wrong-event'&&r.method==='POST').length,1);checks++;
 p=await fresh('unsealed');await ready(p);section=panel(p);await section.getByText('尚未生成通知事件',{exact:true}).waitFor();assert.equal(await section.getByRole('button',{name:'查看原投递记录',exact:true}).count(),0);checks++;
 for(const action of ['replay','skip']){
  p=await fresh(`lost-${action}`);await ready(p);section=panel(p);await section.getByRole('button',{name:'查看原投递记录',exact:true}).click();await section.getByRole('status').filter({hasText:'仅查看所选通知批次'}).waitFor();await section.getByLabel('按投递状态筛选',{exact:true}).selectOption('dead');const target=section.getByRole('article',{name:`投递 ${id(1)}`,exact:true});await target.waitFor();await target.getByRole('button',{name:action==='replay'?'复核此投递重放':'复核审计跳过',exact:true}).click();const confirm=section.getByRole('region',{name:action==='replay'?'投递重放确认':'投递跳过确认',exact:true});await confirm.getByRole('checkbox').check();await confirm.getByRole('button',{name:action==='replay'?'确认重新排队原投递':'确认审计跳过原投递',exact:true}).click();await section.getByRole('alert').filter({hasText:'未确认'}).waitFor();assert.equal(await section.getByLabel('按投递状态筛选',{exact:true}).isDisabled(),true);
  assert.equal(await section.getByRole('button',{name:'查看原投递记录',exact:true}).isDisabled(),true);assert.equal(await section.getByRole('button',{name:'显示全部投递',exact:true}).isDisabled(),true);
  await section.getByRole('button',{name:'读取最新投递状态',exact:true}).click();await section.getByRole('status').filter({hasText:action==='replay'?'不能替代重放回执':'不能替代跳过回执'}).waitFor();await section.getByRole('button',{name:`重试同一投递${action==='replay'?'重放':'跳过'}命令`,exact:true}).click();await section.getByRole('status').filter({hasText:action==='replay'?'原投递已重新排队':'原投递已审计跳过'}).waitFor();assert.equal(queueSends,1);const sends=requests.filter(r=>r.scenario===`lost-${action}`&&r.method==='POST');assert.equal(sends.length,2);assert.equal(sends[0].key,sends[1].key);assert.deepEqual(sends[0].body,sends[1].body);checks++;
 }
 for(const scenario of ['malformed','wrong-state','wrong-event']){p=await fresh(scenario);section=panel(p);if(scenario==='wrong-state'){await ready(p);await section.getByLabel('按投递状态筛选',{exact:true}).selectOption('dead')}if(scenario==='wrong-event'){await ready(p);await section.getByRole('button',{name:'查看原投递记录',exact:true}).click()}await section.getByRole('alert').waitFor();assert.equal(await section.getByRole('article').count(),0);checks++;}
 p=await fresh('late');await ready(p);section=panel(p);const requested=p.waitForRequest(r=>r.url().includes('/integrations/deliveries?cursor='));await section.getByRole('button',{name:'下一页投递',exact:true}).click();await requested;const aborted=p.waitForEvent('requestfailed',{predicate:r=>r.url().includes('/integrations/deliveries?cursor=')});await p.getByRole('button',{name:'切换账号',exact:true}).click();await panel(p).getByRole('article').getByText('另一个账号的通知',{exact:true}).waitFor();await aborted;releasePage();assert.equal(await panel(p).getByRole('button',{name:'返回最新一页',exact:true}).count(),0);assert.equal(await panel(p).getByRole('article').count(),1);checks++;
 p=await fresh('member');await p.waitForLoadState('networkidle');assert.equal(await panel(p).count(),0);assert.equal(requests.filter(r=>r.scenario==='member').length,0);checks++;
 assert.deepEqual(errors,[]);console.log(`PASS delivery history production Chrome: ${checks} groups, 0 skipped, pagination121, server state, original-key replay/skip after lost response, same-page retry, account abort, malformed refusal, 1280/390/320px and zero page errors`);
}finally{releasePage?.();for(const context of contexts)await context.close();await browser?.close();if(server)await new Promise(resolve=>server.close(resolve));await rm(work,{recursive:true,force:true});}
