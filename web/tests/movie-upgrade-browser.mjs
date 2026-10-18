import assert from 'node:assert/strict';
import {chromium} from 'playwright-core';
const base=process.env.PULSE_UPGRADE_UI_BASE, sub=process.env.PULSE_UPGRADE_UI_SUB, session=process.env.PULSE_UPGRADE_UI_SESSION, control=process.env.PULSE_UPGRADE_UI_CONTROL, file=process.env.PULSE_UPGRADE_UI_FILE, user=process.env.PULSE_UPGRADE_UI_USER;
if(!base||!sub||!session||!control||!file||!user)throw Error('explicit owned upgrade fixture required');
const selectedMode=process.env.PULSE_UPGRADE_UI_MODE??'stop_at_cutoff';
assert.ok(['stop_at_cutoff','replace_if_better'].includes(selectedMode));
assert.equal(new URL(base).hostname,'127.0.0.1');
const browser=await chromium.connectOverCDP(process.env.PULSE_UI_CDP_URL??'http://127.0.0.1:9222');
const context=await browser.newContext({viewport:{width:1440,height:1000}});
const errors=[], writes={mode:[],reservation:[],preparation:[],send:[],organizationAuthorization:[],organizationPreparation:[],organizationJob:[],organizationRetirement:[]};
const reads={requests:0,responses:0,failed:0};
const suffixes={mode:'upgrade-modes',reservation:'upgrade-reservations',preparation:'upgrade-preparations',send:'upgrade-sends',organizationAuthorization:'upgrade-organization-authorizations',organizationPreparation:'upgrade-organization-preparations',organizationJob:'upgrade-organization-jobs',organizationRetirement:'upgrade-retirements'};
const storageKey=`pulsemedia:movie-upgrade:v1:${user}:${sub}`;
let debugPage;
try {
 await context.addCookies([{name:'pulsemedia_session',value:session,url:base,httpOnly:true,sameSite:'Strict'}]);
 function observe(page){page.setDefaultTimeout(15000);page.on('pageerror',e=>errors.push(e.message));page.on('request',r=>{if(r.url().includes('/upgrade-previews'))reads.requests++;if(r.method()!=='POST')return;for(const[k,s]of Object.entries(suffixes))if(new URL(r.url()).pathname.endsWith('/'+s))writes[k].push({key:r.headers()['idempotency-key'],body:r.postData()});});page.on('response',r=>{if(r.url().includes('/upgrade-previews'))reads.responses++;});page.on('requestfailed',r=>{if(r.url().includes('/upgrade-previews'))reads.failed++;});}
 const page=await context.newPage();debugPage=page;observe(page);
 const panel=p=>p.getByRole('region',{name:'电影品质升级',exact:true});
 async function open(p){await p.goto(base+'/subscriptions');await p.getByRole('button',{name:'查看Fight Club订阅详情',exact:true}).click();await p.getByRole('button',{name:'管理品质升级',exact:true}).click();await panel(p).getByRole('button',{name:'刷新升级资源',exact:true}).waitFor();await panel(p).getByRole('status').filter({hasText:'正在读取升级资源'}).waitFor({state:'hidden'});}
 async function facts(){const r=await context.request.get(base+control+'/facts');assert.equal(r.status(),200);return r.json();}
 async function waitFor(check,message,timeout=20000){const until=performance.now()+timeout;while(performance.now()<until){if(await check())return;await new Promise(r=>setTimeout(r,50));}throw Error(message);}
 async function loseReply(p,kind){await p.route(`**/api/v1/subscriptions/${sub}/${suffixes[kind]}`,async route=>{const r=await route.fetch();if(r.status()!==202)console.log(JSON.stringify({stage:'unexpected_command_response',kind,status:r.status(),facts:await facts(),counts:Object.fromEntries(Object.entries(writes).map(([k,v])=>[k,v.length]))}));if(r.status()!==202&&kind==='reservation'){const body=route.request().postDataJSON();const fresh=await context.request.post(base+'/api/v1/subscriptions/'+sub+'/upgrade-previews',{headers:await route.request().allHeaders(),data:{current_file_id:body.current_file_id,candidate_id:body.candidate_id}});const v=await fresh.json();console.log(JSON.stringify({stage:'reservation_rejected',status:r.status(),freshStatus:fresh.status(),reason:v.reason,improvement:v.improvement,changedFields:Object.keys(body.expected).filter(k=>JSON.stringify(body.expected[k])!==JSON.stringify(v.version?.[k])),clientDigestEqual:body.expected_client_digest===v.expected_client_digest}));}assert.equal(r.status(),202);await route.fulfill({status:503,contentType:'application/json',body:'{"code":"fixture_lost_reply","message":"fixture lost reply"}'});},{times:1});}
 await open(page);const p=panel(page);
 assert.deepEqual(writes,{mode:[],reservation:[],preparation:[],send:[],organizationAuthorization:[],organizationPreparation:[],organizationJob:[],organizationRetirement:[]});
 async function confirmNewStage(kind, acknowledgement, button){
  const before=writes[kind].length;
  await waitFor(async()=>{
   if(writes[kind].length>before)return true;
   const ack=p.getByRole('checkbox',{name:acknowledgement,exact:true});
   const submit=p.getByRole('button',{name:button,exact:true});
   if(!await ack.isVisible()||!await ack.isEnabled())return false;
   try{
    await ack.check({timeout:1000});if(!await submit.isEnabled())return false;
    await submit.click({timeout:1000});return true;
   }catch(error){
    if(writes[kind].length>before||await page.evaluate(({key,kind})=>!!JSON.parse(localStorage.getItem(key)||'{}')[kind],{key:storageKey,kind}))return true;
    if(!await ack.isVisible()||!await ack.isEnabled()||!await ack.isChecked()||!await submit.isEnabled())return false;
    if(error?.name==='TimeoutError')return false;
    throw error;
   }
  },'new confirmation did not start');
 }
 await p.getByRole('combobox',{name:'升级模式',exact:true}).selectOption(selectedMode);
 await p.getByRole('checkbox',{name:'我确认设置此升级模式',exact:true}).check();
 const modeCheck=p.getByRole('checkbox',{name:'我确认设置此升级模式',exact:true}),modeButton=p.getByRole('button',{name:'确认升级模式',exact:true});
 await waitFor(async()=>{if(await p.getByText('状态已变化或连接已恢复，请刷新升级资源后重新确认。',{exact:true}).isVisible()){await p.getByRole('button',{name:'刷新升级资源',exact:true}).click();await p.getByRole('status').filter({hasText:'正在读取升级资源'}).waitFor({state:'hidden'});}await modeCheck.check();return modeButton.isEnabled();},'fresh mode confirmation unavailable');
 const [modeReply]=await Promise.all([page.waitForResponse(r=>new URL(r.url()).pathname.endsWith('/upgrade-modes')),p.getByRole('button',{name:'确认升级模式',exact:true}).click()]);assert.equal(modeReply.status(),202);
 assert.equal((await modeReply.json()).mode,selectedMode);
 await p.getByText('已核实：模式设置', {exact:false}).waitFor();
 const [refreshed]=await Promise.all([page.waitForResponse(r=>new URL(r.url()).pathname===`/api/v1/subscriptions/${sub}`&&r.request().method()==='GET'),p.getByRole('button',{name:'刷新升级资源',exact:true}).click()]);assert.equal(refreshed.status(),200);
 await p.getByRole('combobox',{name:'当前媒体库文件',exact:true}).selectOption(file);
 await p.getByRole('textbox',{name:'电影资源检索词',exact:true}).fill('Fight Club');
 const [search]=await Promise.all([page.waitForResponse(r=>new URL(r.url()).pathname==='/api/v1/releases/search'),p.getByRole('button',{name:'检索站点',exact:true}).click()]);assert.equal(search.status(),200);const candidates=(await search.json()).results;assert.ok(candidates.length>0);
 const candidate=candidates.find(c=>c.title.includes('2160p'));assert.ok(candidate);await p.getByRole('combobox',{name:'候选资源',exact:true}).selectOption(candidate.id);
 let release, ready;const barrier=new Promise(r=>{release=r;});const started=new Promise(r=>{ready=r;});
 await page.route(`**/api/v1/subscriptions/${sub}/upgrade-previews`,async route=>{const r=await route.fetch();ready();await barrier;await route.fulfill({response:r}).catch(()=>{});},{times:1});
 await p.getByRole('button',{name:'读取升级比较',exact:true}).click();await started;
 await p.getByRole('combobox',{name:'候选资源',exact:true}).selectOption('');release();
 await page.waitForTimeout(100);assert.equal(await p.getByRole('checkbox',{name:'我确认冻结此次文件和候选，保留原文件保护',exact:true}).count(),0);
 await p.getByRole('combobox',{name:'候选资源',exact:true}).selectOption(candidate.id);
 let preview;
 const readComparison=async()=>{
  const changed=p.getByText('状态已变化或连接已恢复，请刷新升级资源后重新确认。',{exact:true});
  if(await changed.isVisible()){await p.getByRole('button',{name:'刷新升级资源',exact:true}).click();await p.getByRole('status').filter({hasText:'正在读取升级资源'}).waitFor({state:'hidden'});await p.getByRole('combobox',{name:'候选资源',exact:true}).selectOption(candidate.id);}
  const failedBefore=reads.failed;
  const waiting=page.waitForResponse(r=>new URL(r.url()).pathname.endsWith('/upgrade-previews'),{timeout:5000}).then(response=>({response}),error=>({error}));
  let clickError;
  try{await p.getByRole('button',{name:'读取升级比较',exact:true}).click({timeout:5000});}catch(error){clickError=error;}
  const outcome=await waiting;
  if(clickError||outcome.error){if(reads.failed>failedBefore||await changed.isVisible())return false;throw clickError??outcome.error;}
  const comparison=outcome.response;assert.equal(comparison.status(),200);
  if(await comparison.finished())return false;
  try{preview=await comparison.json();}catch(error){if(comparison.request().failure()?.errorText==='net::ERR_ABORTED'||(String(error).includes('No data found for resource')&&await changed.isVisible()))return false;throw error;}
  if(preview.reason==='projection_input_changed'||preview.reason==='projection_expired')return false;
  assert.equal(preview.comparable,true);assert.equal(preview.improvement,true);return true;
 };
 await waitFor(readComparison,'real upgrade projection did not become comparable');
 if(selectedMode==='replace_if_better'){assert.ok(preview.current_score>=300,'fixture must already meet cutoff');assert.ok(preview.candidate_score>preview.current_score);}
 await p.getByText('候选品质严格优于当前文件。',{exact:true}).waitFor();
 const before=await facts();assert.equal(before.adds,0);assert.equal(before.tasks,0);assert.equal(before.fetches,0);
 await loseReply(page,'reservation');await waitFor(async()=>{const recovery=p.getByRole('button',{name:'恢复原冻结预留',exact:true});if(writes.reservation.length||await recovery.isVisible())return true;const ack=p.getByRole('checkbox',{name:'我确认冻结此次文件和候选，保留原文件保护',exact:true});if(!await ack.isVisible()){await readComparison();return false;}try{await ack.check({timeout:1500});const confirm=p.getByRole('button',{name:'确认冻结预留',exact:true});if(!await confirm.isEnabled())return false;await confirm.click({timeout:1000});return true;}catch(error){if(writes.reservation.length||await recovery.isVisible())return true;if(await p.getByText('状态已变化或连接已恢复，请刷新升级资源后重新确认。',{exact:true}).isVisible())return false;throw error;}},'no explicit reservation request after a current comparison');await p.getByRole('button',{name:'恢复原冻结预留',exact:true}).waitFor();
 assert.equal(writes.reservation.length,1);await open(page);await p.getByRole('button',{name:'恢复原冻结预留',exact:true}).waitFor();
 const second=await context.newPage();observe(second);await open(second);const q=panel(second);
 const recovered=context.waitForEvent('response',{predicate:r=>new URL(r.url()).pathname.endsWith('/upgrade-reservations')&&r.status()===202,timeout:15000}).then(response=>({response}),error=>({error}));
 await Promise.all([p,q].map(async region=>{
  const button=region.getByRole('button',{name:'恢复原冻结预留',exact:true});
  try{await button.click({timeout:1500});}catch(error){if(await region.getByText('冻结预留完成，尚不可发送',{exact:false}).isVisible())return;throw error;}
 }));
 const recoveredResult=await recovered;if(recoveredResult.error)throw recoveredResult.error;
 await waitFor(()=>Promise.resolve(writes.reservation.length===2),'cross-tab recovery sent duplicate POST');
 assert.equal(writes.reservation[0].key,writes.reservation[1].key);assert.equal(writes.reservation[0].body,writes.reservation[1].body);
 await second.close();
 const preparationAck=p.getByRole('checkbox',{name:'我确认取源并准备此次升级',exact:true}),verifyReservation=p.getByRole('button',{name:'核实原冻结预留回执',exact:true});
 await waitFor(async()=>await preparationAck.isVisible()||await verifyReservation.isVisible(),'first tab did not converge to stored original receipt');
 if(await verifyReservation.isVisible())await verifyReservation.click();
 await preparationAck.waitFor();
 for(const request of writes.reservation){assert.equal(request.key,writes.reservation[0].key);assert.equal(request.body,writes.reservation[0].body);}
 assert.equal((await facts()).adds,0);assert.equal((await facts()).fetches,0);
 await loseReply(page,'preparation');await confirmNewStage('preparation','我确认取源并准备此次升级','确认取源准备');await p.getByRole('button',{name:'恢复原取源准备',exact:true}).waitFor();
 assert.equal((await facts()).fetches,1);assert.equal((await facts()).adds,0);await open(page);
 await p.getByRole('button',{name:'恢复原取源准备',exact:true}).click();await p.getByRole('checkbox',{name:'我确认向下载器发送此次升级',exact:true}).waitFor();
 assert.equal(writes.preparation.length,2);assert.equal(writes.preparation[0].key,writes.preparation[1].key);assert.equal(writes.preparation[0].body,writes.preparation[1].body);assert.equal((await facts()).fetches,1);
 await page.evaluate(key=>{const original=Storage.prototype.setItem;window.__upgradeOriginalSet=original;Storage.prototype.setItem=function(k,v){if(k===key&&JSON.parse(v).send?.phase==='closed')throw new DOMException('fixture quota','QuotaExceededError');return original.call(this,k,v);};},storageKey);
 await confirmNewStage('send','我确认向下载器发送此次升级','确认发送升级');await p.getByRole('button',{name:'仅修复本地回执',exact:true}).waitFor();assert.equal(writes.send.length,1);
 await page.evaluate(key=>window.dispatchEvent(new StorageEvent('storage',{key,newValue:localStorage.getItem(key)})),storageKey);assert.equal(await p.getByRole('button',{name:'恢复原发送确认',exact:true}).count(),0);
 await page.evaluate(()=>{Storage.prototype.setItem=window.__upgradeOriginalSet;delete window.__upgradeOriginalSet;});
 await p.getByRole('button',{name:'仅修复本地回执',exact:true}).click();await p.getByRole('button',{name:'仅修复本地回执',exact:true}).waitFor({state:'hidden'});assert.equal(writes.send.length,1);
 await waitFor(async()=>{const f=await facts();return f.returned===1&&f.adds===1;},'native return not committed');const done=await facts();assert.equal(done.tasks,1);assert.equal(done.commands,1);assert.equal(done.prepared,1);assert.equal(done.fetches,2);assert.equal(done.original_unchanged,true);
 let raw=await page.evaluate(key=>localStorage.getItem(key),storageKey);assert.ok(raw);assert.ok(!/passkey|apikey|password|cookie|https?:\/\//i.test(raw));
 await open(page);assert.equal(writes.send.length,1);await p.getByText('浏览器已保存，待核实：发送确认',{exact:false}).waitFor();assert.equal(await p.getByRole('checkbox',{name:'我确认向下载器发送此次升级',exact:true}).count(),0);
 await p.getByRole('button',{name:'核实原发送确认回执',exact:true}).click();await p.getByText('已核实：发送确认',{exact:false}).waitFor();assert.equal(writes.send.length,2);assert.equal(writes.send[0].key,writes.send[1].key);assert.equal(writes.send[0].body,writes.send[1].body);
 await waitFor(()=>p.getByRole('button',{name:'读取整理复核',exact:true}).isVisible(),'completed native material did not become reviewable',60000);
 const material=p.getByRole('region',{name:'升级整理入库',exact:true}).getByText('当前品质分数',{exact:false});
 await waitFor(async()=>{
  if(await material.isVisible())return true;
  const button=p.getByRole('button',{name:'读取整理复核',exact:true});if(!await button.isEnabled())return false;
  const waiting=page.waitForResponse(r=>new URL(r.url()).pathname.endsWith('/upgrade-organization-previews'),{timeout:5000}).then(response=>({response}),error=>({error}));
  await button.click();const outcome=await waiting;
  if(outcome.error){if(await p.getByText('状态已变化或连接已恢复，请刷新升级资源后重新确认。',{exact:true}).isVisible())return false;throw outcome.error;}
  assert.equal(outcome.response.status(),200);if(await outcome.response.finished())return false;
  return material.isVisible();
 },'organization review did not survive current material read');
 await loseReply(page,'organizationAuthorization');
 await confirmNewStage('organizationAuthorization','我确认使用此完成材料授权整理','确认整理授权');
 await p.getByRole('button',{name:'恢复原整理授权',exact:true}).click();
 await p.getByRole('checkbox',{name:'我确认冻结此次硬链接整理计划',exact:true}).waitFor();
 await confirmNewStage('organizationPreparation','我确认冻结此次硬链接整理计划','冻结硬链接计划');
 await p.getByRole('checkbox',{name:'我确认执行此次整理并核验入库',exact:true}).waitFor();
 await confirmNewStage('organizationJob','我确认执行此次整理并核验入库','确认整理入库');
 await waitFor(async()=>{const f=await facts();return f.ingested===1&&f.linked===1&&f.hardlinked;},'native organization did not link and ingest',60000);
 assert.equal((await facts()).original_unchanged,true);assert.equal((await facts()).adds,1);
 await p.getByText('新文件已入库，旧版本等待受控退休。',{exact:true}).waitFor();
 assert.equal(writes.organizationAuthorization.length,2);assert.deepEqual(writes.organizationAuthorization[0],writes.organizationAuthorization[1]);
 await open(page);await p.getByText('新文件已入库，旧版本等待受控退休。',{exact:true}).waitFor();
 await p.getByRole('button',{name:'核实原整理确认回执',exact:true}).click();
 await p.getByText('已核实：整理确认',{exact:false}).waitFor();
 assert.equal(writes.organizationJob.length,2);assert.deepEqual(writes.organizationJob[0],writes.organizationJob[1]);
 raw=await page.evaluate(key=>localStorage.getItem(key),storageKey);assert.ok(!/passkey|apikey|password|cookie|https?:\/\//i.test(raw));
 await waitFor(()=>p.getByRole('button',{name:'读取旧版回收材料',exact:true}).isVisible(),'retirement review unavailable');
 const retirementMaterial=p.getByLabel('旧版回收材料',{exact:true});
 await waitFor(async()=>{
  if(await retirementMaterial.isVisible())return true;
  const button=p.getByRole('button',{name:'读取旧版回收材料',exact:true});if(!await button.isEnabled())return false;
  const waiting=page.waitForResponse(r=>new URL(r.url()).pathname.endsWith('/upgrade-retirement-previews'),{timeout:5000}).then(response=>({response}),error=>({error}));
  await button.click();const outcome=await waiting;if(outcome.error)return false;assert.equal(outcome.response.status(),200);if(await outcome.response.finished())return false;return retirementMaterial.isVisible();
 },'retirement material did not remain current');
 assert.ok(await retirementMaterial.getByText('至少保留 7 天',{exact:false}).isVisible());
 await page.setViewportSize({width:390,height:844});assert.equal(await retirementMaterial.evaluate(el=>el.scrollWidth<=el.clientWidth+1),true,'retirement material overflows mobile width');
 if(process.env.PULSE_UPGRADE_UI_SCREENSHOT)await p.getByRole('region',{name:'升级整理入库',exact:true}).screenshot({path:process.env.PULSE_UPGRADE_UI_SCREENSHOT+'.retirement-preview.png'});
 await loseReply(page,'organizationRetirement');await confirmNewStage('organizationRetirement','我确认将旧版本移入回收区','回收旧版本');
 await p.getByRole('button',{name:'恢复原旧版本回收',exact:true}).waitFor();await open(page);
 await p.getByRole('button',{name:'恢复原旧版本回收',exact:true}).click();
 await waitFor(async()=>{const f=await facts();return f.retired===1&&f.recycled&&f.old_path_missing&&f.seed_preserved&&f.hardlinked;},'controlled retirement did not preserve file identities',60000);
 await p.getByText('旧版本已移入回收区，本次升级完成。',{exact:true}).waitFor();
 assert.equal(writes.organizationRetirement.length,2);assert.deepEqual(writes.organizationRetirement[0],writes.organizationRetirement[1]);
 assert.equal((await facts()).original_unchanged,false);assert.equal((await facts()).adds,1);
 raw=await page.evaluate(key=>localStorage.getItem(key),storageKey);assert.ok(!/passkey|apikey|password|cookie|https?:\/\//i.test(raw));
 await page.setViewportSize({width:1440,height:1000});
 if(process.env.PULSE_UPGRADE_UI_SCREENSHOT)await page.screenshot({path:process.env.PULSE_UPGRADE_UI_SCREENSHOT+'.desktop.png',fullPage:true});
 await page.setViewportSize({width:390,height:844});assert.equal(await p.evaluate(el=>el.scrollWidth<=el.clientWidth+1),true,'panel overflows mobile width');
 if(process.env.PULSE_UPGRADE_UI_SCREENSHOT)await page.screenshot({path:process.env.PULSE_UPGRADE_UI_SCREENSHOT,fullPage:true});
 await page.evaluate(({key,raw})=>{const broken=JSON.parse(raw);broken.send.receipt.operation_id='10000000-0000-4000-8000-000000000999';localStorage.setItem(key,JSON.stringify(broken));},{key:storageKey,raw});await open(page);await p.getByRole('alert').filter({hasText:'本地确认记录无法读取'}).waitFor();assert.equal(writes.send.length,2);assert.equal(await p.getByRole('button',{name:'确认发送升级',exact:true}).count(),0);
 await page.evaluate(({key,raw})=>localStorage.setItem(key,raw),{key:storageKey,raw});
 assert.equal(errors.length,0,errors.join('\n'));assert.equal((await facts()).adds,1);
 console.log(JSON.stringify({result:'UPGRADE_WORKFLOW_BROWSER_PASSED',counts:Object.fromEntries(Object.entries(writes).map(([k,v])=>[k,v.length])),native_fetches:2,native_adds:1,original_preserved_until_retirement:true,retired:true,recycled:true,seed_preserved:true,page_errors:errors.length}));
}catch(error){if(debugPage&&!debugPage.isClosed())console.log(JSON.stringify({stage:'browser_failure',reads,writes:Object.fromEntries(Object.entries(writes).map(([k,v])=>[k,v.length])),errors,ui:await debugPage.evaluate(()=>({text:document.querySelector('[aria-label="电影品质升级"]')?.textContent?.slice(0,3000),controls:[...document.querySelectorAll('[aria-label="电影品质升级"] select,[aria-label="电影品质升级"] button')].map(x=>({text:x.tagName==='SELECT'?x.selectedOptions[0]?.textContent:x.textContent,disabled:x.disabled})),secure:isSecureContext,locks:typeof navigator.locks,records:Object.keys(localStorage).filter(k=>k.includes('upgrade')).map(k=>{try{const v=JSON.parse(localStorage.getItem(k));return Object.fromEntries(['mode','reservation','preparation','send'].filter(s=>v[s]).map(s=>[s,v[s].phase]));}catch{return {invalid:true};}})}))}));throw error;}finally{await context.close();await browser.close();}
