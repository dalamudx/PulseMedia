import assert from 'node:assert/strict';
import { mkdtemp, writeFile, rm } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { createServer } from 'vite';
import react from '@vitejs/plugin-react';
import { chromium } from 'playwright-core';

const web = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const root = process.env.PULSE_TEST_TEMP_ROOT;
assert.ok(root && path.resolve(root).startsWith("/data/temp/"), "Explicit owned browser temp directory required");
const owned = await mkdtemp(path.join(root, 'external-review-ui-'));
let server, browser;
try {
  await writeFile(path.join(owned, 'index.html'), '<!doctype html><html lang="zh-CN"><meta charset="UTF-8"><meta name="viewport" content="width=device-width,initial-scale=1"><div id="root"></div><script type="module" src="/entry.tsx"></script></html>');
  await writeFile(path.join(owned, 'entry.tsx'), 'import React from "react";import {createRoot} from "react-dom/client";import {ExternalFileReviewPanel} from "@/features/configuration/ExternalFileReviewPanel";import "@/assets/main.css";import "@/assets/workspace.css";import "@/features/configuration/configuration.css";createRoot(document.getElementById("root")!).render(<ExternalFileReviewPanel/>);');
  await writeFile(path.join(owned, 'auth.ts'), 'import {useSyncExternalStore} from "react";let user={id:"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",role:"admin"};const listeners=new Set<()=>void>();(window as any).setMP19Actor=(id:string,role:string)=>{user={id,role};for(const fn of listeners)fn()};export function useAuth(){return {user:useSyncExternalStore(fn=>{listeners.add(fn);return ()=>listeners.delete(fn)},()=>user)}}');
  server = await createServer({ configFile: false, root: owned, cacheDir: path.join(owned, 'vite-cache'), plugins: [react()], resolve: { alias: [
    {find:'@/features/auth/AuthContext',replacement:path.join(owned,'auth.ts')},
    {find:'@',replacement:path.join(web,'src')},
    {find:'react-dom',replacement:path.join(web,'node_modules/react-dom')},
    {find:'react',replacement:path.join(web,'node_modules/react')},
  ] }, server:{host:'127.0.0.1',port:0,fs:{allow:[owned,web]}}, optimizeDeps:{noDiscovery:true,include:["react","react-dom/client","react/jsx-runtime","react/jsx-dev-runtime"]} });
  await server.listen();
  const address = server.httpServer.address();
  const origin = `http://127.0.0.1:${address.port}`;
  browser = await chromium.launch({ executablePath: process.env.PULSE_CHROME_BIN || '/usr/bin/google-chrome', headless: true, args:['--no-sandbox','--disable-dev-shm-usage'] });
  const context = await browser.newContext({viewport:{width:1280,height:900}});
  await context.addCookies([{name:'csrf_token',value:'owned-csrf',url:origin}]);
  const page = await context.newPage();
  page.setDefaultTimeout(10000);
  const pageErrors=[];page.on('pageerror',e=>pageErrors.push(e.message));
  const scope='11111111-1111-4111-8111-111111111111',file='22222222-2222-4222-8222-222222222222',unit='33333333-3333-4333-8333-333333333333',reviewID='44444444-4444-4444-8444-444444444444';
  const option={canonical_unit_id:unit,media_metadata_version:7,unit_metadata_version:9,season:1,episode:7,tmdb_episode_id:55107,unit_key:'S01E07',title:'Series'};
  const input={scope_version:1,file_revision:2,media_type:'tv',tmdb_id:551,...Object.fromEntries(Object.entries(option).filter(([key])=>!['unit_key','title'].includes(key)))};
  const candidate={key:'tmdb.tv:551',namespace:'tmdb.tv',tmdb_id:551,title:'Series',original_title:'Series',year:2026,units:[{unit_key:'S01E07',season:1,episode:7,tmdb_episode_id:55107,air_date:null}]};
  const review={request:{id:reviewID,scope_id:scope,file_id:file,input},state:'ready',stale:false,evidence:{complete:true,candidate},material_hash:'a'.repeat(64)};
  let reviewPosts=0,confirmPosts=0,organizationPosts=0,optionsCalls=0,unknown=false,stale=false,receiptUnavailable=false,lastKey='',lastBody;
  const requestReceipt={command_key:'fixture',request:review.request,execution_allowed:false};
  let confirmation={material:{id:'55555555-5555-4555-8555-555555555555',input:{target_relative_path:'Series/Season 01/Series.S01E07.mkv'}}};
  const organization={operation:{id:'66666666-6666-4666-8666-666666666666'}};
  const companionCandidate={key:'a'.repeat(64),source_relative_path:'[Anime] 任意名称 - 103.zh.ass',target_relative_path:'Series/Season 01/Series.S01E07.zh.ass'};
  let companionState=null,companionPosts=0,companionWrites=0;
  const companionsView=()=>({video_state:'completed',items:companionState?[{id:'77777777-7777-4777-8777-777777777777',candidate:companionCandidate,action:'copy',state:companionState,source_retained:true}]:[]});
  await page.route('**/api/v1/**',async route=>{
    const req=route.request(),url=new URL(req.url());
    const fulfill=(data,status=200)=>route.fulfill({status,contentType:'application/json',body:JSON.stringify(data)});
    if(url.pathname.startsWith('/api/v1/library/untracked-organizations/')){
      assert.equal(url.search,'');
      if(req.method()==='GET')return fulfill(companionsView());
      assert.equal(req.method(),'POST');assert.equal(req.headers()['x-csrf-token'],'owned-csrf');companionPosts++;
      if(url.pathname.endsWith('/discover')){assert.equal(req.postData(),null);return fulfill({items:[companionCandidate]})}
      if(url.pathname.endsWith('/companions')){assert.ok(req.headers()['idempotency-key']);assert.deepEqual(req.postDataJSON(),{candidate_keys:[companionCandidate.key],action:'copy'});companionState='pending';return fulfill(companionsView())}
      if(url.pathname.endsWith('/execute')){
        assert.equal(req.postData(),null);
        if(companionState==='pending'){companionWrites++;companionState='unknown';return route.abort('failed')}
        assert.equal(companionState,'unknown');companionState='completed';return fulfill(companionsView().items[0]);
      }
      throw new Error('Unexpected companion API');
    }
    if(url.pathname.endsWith('/episode-options')){optionsCalls++;assert.equal(url.searchParams.get('tmdb_id'),'551');assert.equal(url.searchParams.get('season'),'1');return fulfill({items:[option]})}
    if(url.pathname.endsWith('/discoveries'))return fulfill({items:[{id:file,scope_id:scope,scope_version:1,revision:2,relative_path:'[Anime] 任意名称 - 103.mkv'}],has_more:false,next_cursor:'',inventory_complete:false,execution_allowed:false});
    if(url.pathname.endsWith('/reviews')&&req.method()==='POST'){
      reviewPosts++;lastKey=req.headers()['idempotency-key'];lastBody=req.postDataJSON();assert.deepEqual(lastBody,input);
      assert.equal(req.headers()['x-csrf-token'],'owned-csrf','UI must honor production CSRF header');
      if(unknown)return route.abort('connectionreset');return fulfill({...requestReceipt,command_key:lastKey},202);
    }
    if(url.pathname.includes('/review-commands/')){assert.ok(url.pathname.endsWith(lastKey));return fulfill(receiptUnavailable?{}:{...requestReceipt,command_key:lastKey},receiptUnavailable?503:200)}
    if(url.pathname.endsWith('/reviews/'+reviewID))return fulfill({...review,stale});
    if(url.pathname.endsWith('/confirmations')&&req.method()==='POST'){confirmPosts++;const body=req.postDataJSON();assert.equal(body.review_id,reviewID);assert.equal(body.material_hash,'a'.repeat(64));assert.equal(body.action,'hardlink');confirmation={material:{...confirmation.material,input:body}};return fulfill(confirmation)}
    if(url.pathname.endsWith('/organizations')&&req.method()==='POST'){organizationPosts++;assert.equal(req.postDataJSON().confirmation_id,confirmation.material.id);return fulfill(organization,202)}
    if(url.pathname.includes('/organizations/'))return fulfill({operation:organization.operation,state:'completed',review_required:false});
    if(url.pathname===`/api/v1/library/untracked-scopes/${scope}`)return fulfill({config:{id:scope,enabled:true,version:1,target_media_type:'tv',local_root:'/owned/source',target_root_path:'/owned/target'}});
    if(url.pathname==='/api/v1/library/untracked-scopes')return fulfill({items:[{id:scope,version:1,enabled:true,local_root:'/owned/source',target_root_path:'/owned/target',target_media_type:'tv'}],next_cursor:'',has_more:false});
    throw new Error('Unexpected MP19 UI API '+req.method()+' '+url.pathname);
  });
  await page.goto(origin);
  await page.getByRole('button',{name:'读取已配置目录',exact:true}).click();
  await page.getByLabel('已配置目录',{exact:true}).selectOption(scope);
  await page.getByRole('button',{name:'读取目录文件',exact:true}).click();
  await page.getByLabel('观测文件').selectOption(file);
  await page.getByLabel('TMDB 剧集 ID').fill('551');
  await page.getByLabel('明确选择一个规范单集').check();
  assert.equal(await page.getByRole('button',{name:'生成识别预览',exact:true}).isDisabled(),true);
  await page.getByRole('button',{name:'读取规范剧集',exact:true}).click();
  await page.getByLabel('规范单集',{exact:true}).selectOption(unit);
  await page.getByRole('button',{name:'生成识别预览',exact:true}).click();
  await page.getByRole('button',{name:'读取识别预览',exact:true}).click();
  await page.getByRole('heading',{name:'识别预览',exact:true}).waitFor();
  assert.equal(await page.getByText('S01E07 · TMDB 单集 55107',{exact:true}).count(),1);
  await page.getByLabel('目标相对路径').fill('Series/Season 01/Series.S01E07.mkv');
  await page.getByLabel('我已核对该文件对应的媒体、规范集号与目标路径',{exact:true}).check();
  for(const viewport of [{width:1280,height:900},{width:390,height:844}]){
    await page.setViewportSize(viewport);
    assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth),false,'horizontal overflow');
    await page.screenshot({path:path.join(owned,`mp19-${viewport.width}.png`),fullPage:true});
  }
  await page.getByRole('button',{name:'确认识别与目标',exact:true}).click();
  assert.equal(confirmPosts,1);
  await page.getByRole('button',{name:'提交整理计划',exact:true}).click();
  await page.getByRole('button',{name:'读取整理状态',exact:true}).click();
  await page.getByText('整理完成，已记录文件入库。',{exact:true}).waitFor();
  assert.equal(reviewPosts,1);assert.equal(organizationPosts,1);assert.equal(optionsCalls,1);
  const companions=page.getByRole('region',{name:'随片字幕',exact:true});
  await companions.getByRole('button',{name:'检查同目录字幕',exact:true}).click();
  await companions.getByRole('checkbox').check();
  await companions.getByRole('button',{name:'确认所选字幕及目标',exact:true}).click();
  await companions.getByRole('button',{name:'整理此字幕',exact:true}).click();
  await page.waitForFunction(()=>document.querySelector('.untracked-companions')?.textContent?.includes('请先读取'));
  assert.equal(companionPosts,3);assert.equal(companionWrites,1);
  await companions.getByRole('button',{name:'读取字幕结果',exact:true}).click();
  await companions.getByRole('button',{name:'核对已有结果',exact:true}).click();
  await companions.getByText('字幕已整理，源文件保留 · 复制',{exact:true}).waitFor();
  assert.equal(companionPosts,4);assert.equal(companionWrites,1);
  for(const width of [1280,390,320]){
    await page.setViewportSize({width,height:900});
    assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth),false,'combined organizer overflow');
    const small=await companions.getByRole('button').evaluateAll(nodes=>nodes.some(node=>{const box=node.getBoundingClientRect();return box.width<44||box.height<44}));
    assert.equal(small,false,'companion target below44px');
  }
  // A new account must not inherit another account's evidence or queued command.
  await page.evaluate(()=>window.setMP19Actor('bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb','admin'));
  assert.equal(await page.getByLabel('已配置目录',{exact:true}).count(),0);
  assert.equal(await page.getByRole('button',{name:'读取目录文件',exact:true}).isDisabled(),true);
  await page.evaluate(()=>window.setMP19Actor('aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa','admin'));
  await page.getByRole('button',{name:'读取已配置目录',exact:true}).click();
  await page.getByLabel('已配置目录',{exact:true}).selectOption(scope);
  await page.getByRole('button',{name:'读取目录文件',exact:true}).click();
  await page.getByLabel('观测文件').selectOption(file);
  await page.getByLabel('TMDB 剧集 ID').fill('551');
  await page.getByLabel('明确选择一个规范单集').check();
  await page.getByRole('button',{name:'读取规范剧集',exact:true}).click();
  await page.getByLabel('规范单集',{exact:true}).selectOption(unit);
  unknown=true;
  const beforeStorageFailure=reviewPosts;
  await page.evaluate(()=>{
    window.mp19Keys=[];
    const random=crypto.randomUUID.bind(crypto);
    crypto.randomUUID=()=>{const key=random();window.mp19Keys.push(key);return key};
    window.mp19SetItem=Storage.prototype.setItem;
    Storage.prototype.setItem=function(key,value){if(key.startsWith('pulse.external-file-review.'))throw new DOMException('owned storage denial','SecurityError');return window.mp19SetItem.call(this,key,value)};
  });
  await page.getByRole('button',{name:'生成识别预览',exact:true}).click();
  await page.getByRole('button',{name:'保存并提交原命令',exact:true}).waitFor();
  assert.equal(reviewPosts,beforeStorageFailure,'storage failure must stop before POST');
  await page.evaluate(()=>{Storage.prototype.setItem=window.mp19SetItem});
  await page.getByRole('button',{name:'保存并提交原命令',exact:true}).click();
  await page.getByRole('button',{name:'读取原回执',exact:true}).waitFor();
  assert.deepEqual(await page.evaluate(()=>window.mp19Keys),[lastKey],'storage retry must preserve original key');
  const journal=await page.evaluate(()=>sessionStorage.getItem('pulse.external-file-review.v1:aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa'));
  assert.equal(JSON.parse(journal).key,lastKey);
  assert.deepEqual(JSON.parse(journal).body,input);
  const before=reviewPosts;
  receiptUnavailable=true;
  await page.getByRole('button',{name:'读取原回执',exact:true}).click();
  await page.getByRole('alert').waitFor();
  assert.equal(reviewPosts,before,'failed recovery must never resend');
  assert.equal(await page.evaluate(()=>sessionStorage.getItem('pulse.external-file-review.v1:aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa')),journal,'failed read changed original journal');
  receiptUnavailable=false;
  await page.reload();
  await page.getByRole('button',{name:'读取原回执',exact:true}).click();
  await page.getByRole('button',{name:'读取识别预览',exact:true}).waitFor();
  assert.equal(reviewPosts,before,'unknown result must not retry POST');
  stale=true;
  await page.getByRole('button',{name:'读取识别预览',exact:true}).click();
  await page.getByText('文件、目录范围或元数据已变化，请重新生成预览。',{exact:true}).waitFor();
  assert.equal(await page.getByRole('button',{name:'确认识别与目标',exact:true}).count(),0);
  await page.evaluate(()=>window.setMP19Actor('aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa','member'));
  assert.equal(await page.getByRole('region',{name:'外部文件识别与整理'}).count(),0,'revoked role retains UI');
  assert.deepEqual(pageErrors,[]);
  console.log('MP19_COMPONENT_BROWSER_PASS explicit_real_ids=true preview_confirm_organization=true unknown_no_resend=true reload_recovery=true account_role_fence=true stale_disabled=true csrf=true persistence_failclosed=true memory_original_retained=true failed_read_preserves_journal=true scope_picker=true desktop_mobile=true API=CONTROLLED_FIXTURE');
  await context.close();
} finally {
  await browser?.close();
  await server?.close();
  await rm(owned,{recursive:true,force:true});
}
