import assert from 'node:assert/strict';
import { mkdtemp, writeFile, rm } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { createServer } from 'vite';
import react from '@vitejs/plugin-react';
import { chromium } from 'playwright-core';
const web=path.resolve(path.dirname(fileURLToPath(import.meta.url)),'..');
const root=process.env.PULSE_TEST_TEMP_ROOT;assert.ok(root?.startsWith('/data/temp/'));
const owned=await mkdtemp(path.join(root,'external-batch-chrome-'));let server,browser;
try{
 await writeFile(path.join(owned,'index.html'),'<!doctype html><html lang="zh-CN"><meta charset="UTF-8"><meta name="viewport" content="width=device-width,initial-scale=1"><div id="root"></div><script type="module" src="/entry.tsx"></script></html>');
 await writeFile(path.join(owned,'entry.tsx'),'import React from "react";import {createRoot} from "react-dom/client";import {ExternalBatchImportPanel} from "@/features/configuration/ExternalBatchImportPanel";import "@/assets/main.css";import "@/assets/workspace.css";import "@/features/configuration/configuration.css";createRoot(document.getElementById("root")!).render(<ExternalBatchImportPanel/>);');
 await writeFile(path.join(owned,'auth.ts'),'import {useSyncExternalStore} from "react";let user={id:"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",role:"admin"};const listeners=new Set<()=>void>();(window as any).setBatchActor=(id:string,role:string)=>{user={id,role};for(const f of listeners)f()};export function useAuth(){return {user:useSyncExternalStore(f=>{listeners.add(f);return ()=>listeners.delete(f)},()=>user)}}');
 server=await createServer({configFile:false,root:owned,cacheDir:path.join(owned,'vite-cache'),plugins:[react()],resolve:{alias:[{find:'@/features/auth/AuthContext',replacement:path.join(owned,'auth.ts')},{find:'@',replacement:path.join(web,'src')},{find:'react-dom',replacement:path.join(web,'node_modules/react-dom')},{find:'react',replacement:path.join(web,'node_modules/react')}]},server:{host:'127.0.0.1',port:0,fs:{allow:[owned,web]}},optimizeDeps:{noDiscovery:true,include:['react','react-dom/client','react/jsx-runtime','react/jsx-dev-runtime']}});
 await server.listen();const origin=`http://127.0.0.1:${server.httpServer.address().port}`;
 browser=await chromium.launch({executablePath:process.env.PULSE_CHROME_BIN||'/usr/bin/google-chrome',headless:true,args:['--no-sandbox','--disable-dev-shm-usage']});
 const context=await browser.newContext({viewport:{width:1280,height:900}});await context.addCookies([{name:'csrf_token',value:'owned-csrf',url:origin}]);
 const page=await context.newPage();page.setDefaultTimeout(10000);const errors=[];page.on('pageerror',e=>errors.push(e.message));
 const scope='11111111-1111-4111-8111-111111111111',batchID='55555555-5555-4555-8555-555555555555';
 const files=[{id:'22222222-2222-4222-8222-222222222222',relative_path:'目录/任意长片名'.repeat(5)+'/A.mkv',revision:2},{id:'33333333-3333-4333-8333-333333333333',relative_path:'目录/任意长片名'.repeat(5)+'/B.mkv',revision:3}];
 const options=[7,8].map(n=>({canonical_unit_id:`44444444-4444-4444-8444-${String(n).padStart(12,'0')}`,media_metadata_version:5,unit_metadata_version:6,season:1,episode:n,tmdb_episode_id:55100+n,unit_key:`S01E0${n}`,title:'Series'}));
 const reviews=new Map(),status=[{confirmation:null,organization:null,progress:null},{confirmation:null,organization:null,progress:null}];let batch,ambiguous=true,createPosts=0,confirmPosts=0,orgPosts=0,reviewPosts=0;
 const ok=(route,value,code=200)=>route.fulfill({status:code,contentType:'application/json',body:JSON.stringify(value)});
 await page.route('**/api/v1/**',async route=>{
  const req=route.request(),url=new URL(req.url()),p=url.pathname,method=req.method();
  if(method==='POST')assert.equal(req.headers()['x-csrf-token'],'owned-csrf');
  if(p==='/api/v1/library/untracked-scopes')return ok(route,{items:[{id:scope,enabled:true,target_media_type:'tv',local_root:'/source',target_root_path:'/target'}],next_cursor:''});
  if(p.endsWith(scope))return ok(route,{config:{version:1}});
  if(p.endsWith('/discoveries'))return ok(route,{items:files,next_cursor:''});
  if(p.endsWith('/episode-options'))return ok(route,{items:options});
  if(p.endsWith('/reviews')&&method==='POST'){
   reviewPosts++;const input=req.postDataJSON(),file=p.split('/').at(-2),id=crypto.randomUUID(),option=options.find(o=>o.canonical_unit_id===input.canonical_unit_id);
   const review={request:{id,input,file_id:file},state:'ready',stale:false,material_hash:'a'.repeat(64),evidence:{complete:true,candidate:{key:'tmdb.tv:551',units:[{unit_key:option.unit_key,tmdb_episode_id:option.tmdb_episode_id}]}}};reviews.set(id,review);return ok(route,{request:{id}},202);
  }
  if(p.includes('/reviews/')){const review=reviews.get(p.split('/').at(-1));if(review.request.file_id===files[1].id&&ambiguous)return ok(route,{...review,state:'review_required',evidence:{complete:false,candidate:null}});return ok(route,review);}
  if(p.endsWith('/external-batches')&&method==='POST'){
   createPosts++;const body=req.postDataJSON();assert.equal(body.rows.length,2);assert.notEqual(body.rows[0].confirmation.review_id,body.rows[1].confirmation.review_id);
   batch={id:batchID,scope_id:scope,command_key:req.headers()['idempotency-key'],rows:body.rows.map(s=>({selection:s,file:files.find(f=>f.id===s.file_id),review:reviews.get(s.confirmation.review_id)}))};await route.abort('failed');return;
  }
  if(p.includes('/external-batch-commands/')||p.endsWith('/external-batches/'+batchID))return ok(route,{batch,status});
  if(p.endsWith('/confirm')){confirmPosts++;const row=Number(p.split('/').at(-2));assert.deepEqual(req.postDataJSON(),{confirmed:true});if(row===0)return ok(route,{},409);status[row].confirmation={id:'confirmed-row2'};return ok(route,status[row].confirmation);}
  if(p.endsWith('/organize')){orgPosts++;const row=Number(p.split('/').at(-2));status[row].organization={id:'organized-row2'};status[row].progress={state:'completed'};await route.abort('failed');return;}
  throw new Error(`unexpected request ${method} ${p}`);
 });
 await page.goto(origin);const panel=page.getByRole('region',{name:'批量外部文件导入'});
 await panel.getByRole('button',{name:'读取批量目录',exact:true}).click();await panel.getByLabel('批量目录',{exact:true}).selectOption(scope);await panel.getByRole('button',{name:'读取批量文件',exact:true}).click();
 await panel.locator('.external-batch-file input').nth(0).check();await panel.locator('.external-batch-file input').nth(1).check();await panel.getByLabel('批量 TMDB 剧集 ID').fill('551');await panel.getByRole('button',{name:'读取批量规范剧集'}).click();
 await panel.getByLabel('第1行规范单集').selectOption(options[0].canonical_unit_id);await panel.getByLabel('第2行规范单集').selectOption(options[0].canonical_unit_id);await panel.getByText('规范单集或目标重复，请逐行修改；不会按数量推断集号。').waitFor();assert.equal(await panel.getByRole('button',{name:'预览第1行',exact:true}).isDisabled(),true);
 await panel.getByLabel('第2行规范单集').selectOption(options[1].canonical_unit_id);await panel.getByLabel('第1行目标',{exact:true}).fill('Series/Season 01/Series.S01E07.mkv');await panel.getByLabel('第2行目标',{exact:true}).fill('Series/Season 01/Series.S01E08.mkv');
 for(const n of [1,2]){await panel.getByRole('button',{name:`预览第${n}行`,exact:true}).click();await panel.getByRole('button',{name:`读取第${n}行预览`,exact:true}).click();}
 await panel.getByText('存在歧义，需要人工复核').waitFor();assert.equal(await panel.getByRole('button',{name:'保存批量预览'}).isDisabled(),true);ambiguous=false;await panel.getByRole('button',{name:'读取第2行预览',exact:true}).click();
 await panel.getByRole('button',{name:'保存批量预览'}).click();await panel.getByRole('button',{name:'读取批次原回执'}).waitFor();await page.reload();await panel.getByRole('button',{name:'读取批次原回执'}).click();await panel.getByText('原批次逐行结果').waitFor();assert.equal(createPosts,1);assert.equal(reviewPosts,2);
 await panel.getByLabel('已核对第1行',{exact:true}).check();await panel.getByRole('button',{name:'确认第1行',exact:true}).click();await panel.getByRole('alert').filter({hasText:'已变化'}).waitFor();assert.equal(await panel.getByRole('button',{name:'整理第1行',exact:true}).isDisabled(),true);
 await panel.getByLabel('已核对第2行',{exact:true}).check();await panel.getByRole('button',{name:'确认第2行',exact:true}).click();await panel.getByText('已确认，尚未整理').waitFor();await panel.getByRole('button',{name:'整理第2行',exact:true}).click();await page.reload();await panel.getByRole('button',{name:'读取批次原回执'}).click();await panel.getByText('整理已完成',{exact:true}).waitFor();assert.equal(confirmPosts,2);assert.equal(orgPosts,1);assert.equal(await panel.getByRole('button',{name:'整理第2行',exact:true}).isDisabled(),true);
 for(const width of [1280,390,320]){await page.setViewportSize({width,height:900});assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth),false,`overflow ${width}`);if(width<=600){const rows=await panel.locator('.external-batch-scroll').evaluateAll(items=>items.map(el=>({width:el.clientWidth,scroll:el.scrollWidth})));assert.ok(rows.every(row=>row.scroll<=row.width+1),`horizontal row scroll ${width}`);}const boxes=await panel.getByRole('button').evaluateAll(buttons=>buttons.map(button=>{const r=button.getBoundingClientRect();return{x:r.x,right:r.right,width:r.width,height:r.height}}));assert.ok(boxes.every(box=>box.x>=0&&box.right<=width+1&&box.width>=44&&box.height>=44),`button visibility and target size ${width}`);if(process.env.PULSE_ACCEPTANCE_SCREENSHOT_DIR)await page.screenshot({path:path.join(process.env.PULSE_ACCEPTANCE_SCREENSHOT_DIR,`external-batch-${width}.png`),fullPage:true});}
 await page.evaluate(()=>window.setBatchActor('bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb','admin'));assert.equal(await panel.getByText('原批次逐行结果').count(),0);assert.equal(await panel.getByRole('button',{name:'读取批次原回执'}).count(),0);
 await page.evaluate(()=>window.setBatchActor('aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa','member'));assert.equal(await page.getByRole('region',{name:'批量外部文件导入'}).count(),0);assert.deepEqual(errors,[]);
 console.log('MP28 production Chrome PASS: two explicit rows, duplicate/ambiguous refusal, stale-row isolation, lost preview+organization response reload GET recovery, no automatic resend, account/role fence, 1280/390/320px; zero skips');
}finally{if(browser)await browser.close();if(server)await server.close();await rm(owned,{recursive:true,force:true});}
