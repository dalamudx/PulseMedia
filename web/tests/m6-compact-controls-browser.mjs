import assert from 'node:assert/strict';
import {createRequire} from 'node:module';
import {createHash} from 'node:crypto';
import {writeFile} from 'node:fs/promises';
import {join} from 'node:path';
const env=process.env,load=createRequire(env.PULSE_M6_COMPACT_CONTROLS_REPO+'/web/package.json');
const {chromium}=load('playwright-core');
const base=env.PULSE_M6_COMPACT_CONTROLS_BASE,shots=env.PULSE_M6_COMPACT_CONTROLS_SCREENSHOTS;
assert(base&&shots?.startsWith('/data/temp/')&&env.PULSE_M6_COMPACT_CONTROLS_BROWSER==='1');
assert.notEqual(new URL(env.PULSE_M6_COMPACT_CONTROLS_CDP).port,'9222');
const browser=await chromium.connectOverCDP(env.PULSE_M6_COMPACT_CONTROLS_CDP);
const context=await browser.newContext({viewport:{width:1280,height:1000},reducedMotion:'reduce',serviceWorkers:'block'});
const evidence=[],calls=[],errors=[],losses=[];
let phase='setup';
const digest=value=>createHash('sha256').update(JSON.stringify(value)).digest('hex');
let checks=0;
const check=(value,message)=>{assert(value,message);checks++;};
try {
 // The production request executes normally. Only delivery of its genuine success
 // is lost; this avoids replacing a durable unknown result with a forged 409/500.
 await context.addInitScript(()=>{
  const native=window.fetch.bind(window);
  window.__compactTransport={next:null,receipts:[]};
  window.fetch=async(input,init)=>{
   const path=new URL(typeof input==='string'?input:input.url,location.href).pathname;
   const method=init?.method??(input instanceof Request?input.method:'GET');
   const rule=window.__compactTransport.next;
   const lose=method==='POST'&&rule?.path===path;
   if(lose)window.__compactTransport.next=null;
   const response=await native(input,init);
   if(lose){
    window.__compactTransport.receipts.push({kind:rule.kind,status:response.status});
    if(response.status!==rule.status)throw new Error('Unexpected actual command status');
    // No secret response body is read, logged, or stored by the observer.
    await response.body?.cancel();
    throw new TypeError('Owned response delivery interrupted');
   }
   return response;
  };
 });
 await context.route('**/*',route=>new URL(route.request().url()).origin===base?route.continue():route.abort('blockedbyclient'));
 await context.addCookies([{name:'pulsemedia_session',value:env.PULSE_M6_COMPACT_CONTROLS_SESSION,url:base,httpOnly:true,sameSite:'Strict'}]);
 const page=await context.newPage();page.setDefaultTimeout(12000);
 page.on('pageerror',()=>errors.push('pageerror'));
 page.on('request',r=>{
  if(r.method()!=='POST')return;
  calls.push({path:new URL(r.url()).pathname,key:r.headers()['idempotency-key'],body:r.postData()});
 });
 const account=()=>page.locator('.account-tokens');
 const policy=()=>page.locator('section[aria-label="质量与过滤策略"]');
 const secret=()=>page.getByLabel('新令牌秘密',{exact:true});
 const tokenName='CompactToken'+ 'A'.repeat(108),lostName='LostReceiptX'+'B'.repeat(108);
 check(tokenName.length===120&&lostName.length===120,'legal 120 ASCII byte names');
 const stored=kind=>page.evaluate(kind=>{
  const storage=kind==='token'?localStorage:sessionStorage;
  const prefix=kind==='token'?'pulse.own-token-command.v1:':'pulsemedia.policy-command.v1:';
  const keys=Object.keys(storage).filter(k=>k.startsWith(prefix));
  return keys.length===1?JSON.parse(storage.getItem(keys[0])):null;
 },kind);
 const arm=(path,kind,status)=>page.evaluate(rule=>{window.__compactTransport.next=rule;},{path,kind,status});
 const lost=async(kind,status)=>{
  await page.waitForFunction(kind=>window.__compactTransport.receipts.some(r=>r.kind===kind),kind);
  const receipt=await page.evaluate(kind=>window.__compactTransport.receipts.find(r=>r.kind===kind),kind);
  check(receipt.status===status,'genuine success before lost '+kind+' response');
  losses.push(receipt);
 };
 const healthyStorage=async()=>{
  check(await page.evaluate(()=>{
   const value=document.querySelector('#token-secret')?.value;
   return [localStorage,sessionStorage].every(storage=>Object.values(storage).every(v=>!v.includes('pm_api_')&&(!value||!v.includes(value))));
  }),'secret must never enter either storage');
 };
 const original=(a,b,message)=>check(digest(a)===digest(b),message);
 const layout=async(root,state,kind=null)=>{
  phase=state;
  const pending=kind?await stored(kind):null;
  for(const width of [1280,768,375,320]){
   await page.setViewportSize({width,height:1000});
   await page.waitForFunction(width=>{
    const nav=document.getElementById('app-navigation'),menu=document.querySelector('.mobile-menu-btn');
    if(!nav||!menu||innerWidth!==width||nav.getAnimations().some(a=>a.playState==='running'))return false;
    return width<=1024?nav.inert&&menu.getAttribute('aria-expanded')==='false'&&nav.getBoundingClientRect().right<=1:!nav.inert&&nav.getBoundingClientRect().left>=-1;
   },width);
   await page.waitForFunction(selector=>{
    const root=document.querySelector(selector);if(!root)return false;
    const geometry=JSON.stringify([...root.querySelectorAll('button,p,h2,h3')].map(e=>{const r=e.getBoundingClientRect();return [r.x,r.y,r.width,r.height];}));
    const stable=window.__compactGeometry===geometry;window.__compactGeometry=geometry;return stable;
   },state.startsWith('policy')?'.policy-panel':'.account-tokens',{polling:'raf'});
   const geometry=await root.evaluate(root=>{
    const failures=[],controls=[];
    if(document.documentElement.scrollWidth>innerWidth+1)failures.push('viewport overflow');
    for(const el of root.querySelectorAll('button,h1,h2,h3,p,[role="alert"],[role="status"],.account-token-recovery,.account-token-secret')){
     const r=el.getBoundingClientRect();if(!r.width||!r.height||!el.getClientRects().length)continue;
     if(r.left< -1||r.right>innerWidth+1)failures.push(el.tagName+' outside viewport');
     if(el.scrollWidth>el.clientWidth+2||el.scrollHeight>el.clientHeight+2)failures.push(el.tagName+' clipped content');
     if(el.tagName==='BUTTON'){
      if(r.width<44||r.height<44)failures.push('control below 44px');
      const range=document.createRange();range.selectNodeContents(el);
      for(const text of range.getClientRects())if(text.width&&text.height&&(text.left<r.left-1||text.right>r.right+1||text.top<r.top-1||text.bottom>r.bottom+1))failures.push('button text clipped');
      controls.push({text:el.textContent.trim(),aria:el.getAttribute('aria-label'),width:r.width,height:r.height,disabled:el.disabled});
     }
    }
    return {failures,controls};
   });
   if(geometry.failures.length)console.error('M6_COMPACT_GEOMETRY '+JSON.stringify({state,width,failures:geometry.failures,controls:geometry.controls.map(({width,height,disabled})=>({width,height,disabled}))}));
   assert.deepEqual(geometry.failures,[],'layout '+state+' '+width);checks++;
   if(state==='policy-pending'){
    const button=root.getByRole('button',{name:'重新读取策略（保留未确认命令）',exact:true});
    check(await button.textContent()==='重新读取策略（保留未确认命令）','full original policy reload text');
    check(await button.isEnabled(),'pending policy reload enabled');
   }
   await healthyStorage();
   evidence.push({state,width,pending:pending?{action:pending.action??'publish',keySHA256:digest(pending.key),commandSHA256:digest(pending)}:null,controls:geometry.controls});
   await page.evaluate(()=>scrollTo(0,0));
   await page.screenshot({path:join(shots,state+'-'+width+'.png'),fullPage:true,mask:[secret()],maskColor:'#555555'});
  }
 };
 phase='initial-token-create';
 const tokenPath='/api/v1/auth/tokens';
 await page.goto(base+'/account/tokens',{waitUntil:'domcontentloaded'});
 await page.getByRole('heading',{name:'API 令牌',exact:true}).waitFor();
 await page.getByLabel('名称',{exact:true}).fill(tokenName);
 const firstCreate=page.waitForResponse(r=>new URL(r.url()).pathname===tokenPath&&r.request().method()==='POST');void firstCreate.catch(()=>{});
 await page.getByRole('button',{name:'创建令牌',exact:true}).click();
 const firstResponse=await firstCreate;check(firstResponse.status()===200,'genuine first token creation');
 const minted=await firstResponse.json();
 await secret().waitFor();
 check(await secret().getAttribute('type')==='password','first genuine secret is password');
 check(typeof minted.secret==='string'&&minted.secret.length>0&&await secret().inputValue()===minted.secret,'first secret matches genuine API response');
 await account().getByRole('heading',{name:tokenName+'：首次秘密',exact:true}).waitFor();
 await layout(account(),'token-secret');
 await page.getByRole('button',{name:'已保存，清除秘密',exact:true}).click();
 await arm(tokenPath,'create',200);
 await page.getByLabel('名称',{exact:true}).fill(lostName);
 await page.getByRole('button',{name:'创建令牌',exact:true}).click();
 await page.getByRole('button',{name:'恢复原请求',exact:true}).waitFor();
 await lost('create',200);
 const createPending=await stored('token');
 check(createPending?.action==='create'&&createPending.body.name===lostName,'actual create pending command');
 await layout(account(),'token-create-pending','token');
 await page.reload();await page.getByRole('button',{name:'恢复原请求',exact:true}).waitFor();
 original(createPending,await stored('token'),'reload preserves original create command');
 check(calls.length===2,'reload emitted no command');
 await page.getByRole('button',{name:'恢复原请求',exact:true}).click();
 await page.getByText('原令牌已确认，首次秘密无法恢复。若尚未保存，请撤销此令牌后重新创建。',{exact:true}).waitFor();
 check(await secret().count()===0,'lost first secret cannot be replayed');
 check(calls.length===3,'exactly one explicit create retry');original(calls[1],calls[2],'original create key and body');
 const row=account().locator('.account-token-list > li').filter({has:page.getByRole('heading',{name:lostName,exact:true})});
 const revoke=row.getByRole('button',{name:'撤销 '+lostName,exact:true});await revoke.waitFor();
 check(await revoke.textContent()==='撤销','short revoke visible text');
 check(await revoke.getAttribute('aria-label')==='撤销 '+lostName,'full revoke accessible name');
 await layout(account(),'token-revoke-control');
 await revoke.click();await row.getByRole('button',{name:'确认撤销',exact:true}).waitFor();
 check(calls.length===3,'confirmation required before revoke');
 await layout(account(),'token-revoke-confirm');
 const list=await (await context.request.get(base+tokenPath)).json();
 const target=list.items.find(t=>t.name===lostName);check(Boolean(target),'real created token found');
 await arm(tokenPath+'/'+target.id+'/revoke','revoke',200);
 await row.getByRole('button',{name:'确认撤销',exact:true}).click();
 await page.getByRole('button',{name:'恢复原请求',exact:true}).waitFor();
 await lost('revoke',200);
 const revokePending=await stored('token');
 check(revokePending?.action==='revoke'&&revokePending.name===lostName&&revokePending.id===target.id,'actual revoke pending command');
 await layout(account(),'token-revoke-pending','token');
 await page.reload();await page.getByRole('button',{name:'恢复原请求',exact:true}).waitFor();
 original(revokePending,await stored('token'),'reload preserves original revoke command');
 check(calls.length===4,'reload emitted no revoke');
 await page.getByRole('button',{name:'恢复原请求',exact:true}).click();
 await page.getByText('撤销已确认。该令牌不能再次启用。',{exact:true}).waitFor();
 await row.getByText(/已撤销/).waitFor();
 check(calls.length===5,'exactly one explicit revoke retry');original(calls[3],calls[4],'original revoke key and body');
 check(await row.getByRole('button',{name:'撤销 '+lostName,exact:true}).count()===0,'revoked token cannot be revoked twice');
 check(await stored('token')===null,'token pending cleared');
 const finalList=await (await context.request.get(base+tokenPath)).json();
 check(finalList.items.length===2&&finalList.items.every(t=>!Object.hasOwn(t,'secret')&&!Object.hasOwn(t,'token_hash')),'two tokens, no secret metadata');
 await page.goto(base+'/settings',{waitUntil:'domcontentloaded'});
 await page.getByRole('button',{name:'质量与过滤策略',exact:true}).click();
 const panel=policy();await panel.getByLabel('当前策略',{exact:true}).selectOption('default');
 const terms=panel.getByLabel('拒绝词（每行一个，标题包含匹配）',{exact:true});
 await terms.fill('CompactControlsBlocked');
 await arm('/api/v1/policies/default/revisions','publish',201);
 await panel.getByRole('button',{name:'发布新版本',exact:true}).click();
 await panel.getByRole('button',{name:'重试同一发布命令',exact:true}).waitFor();
 await lost('publish',201);
 const publishPending=await stored('policy');
 check(publishPending?.id==='default'&&!publishPending.create&&publishPending.body.expected_revision===1,'real versioned publish pending');
 check(publishPending.body.rules.reject_terms.includes('CompactControlsBlocked'),'original draft body retained');
 check(await terms.isDisabled(),'pending rules locked');
 await layout(panel,'policy-pending','policy');
 const countBefore=calls.length;
 await panel.getByRole('button',{name:'重新读取策略（保留未确认命令）',exact:true}).click();
 await panel.getByRole('status').filter({hasText:'发现未确认的策略命令'}).waitFor();
 await panel.getByRole('button',{name:'重试同一发布命令',exact:true}).waitFor({state:'visible'});
 original(publishPending,await stored('policy'),'reread preserves original policy key and body');
 check(calls.length===countBefore,'reread never publishes');
 check(await terms.inputValue()==='CompactControlsBlocked'&&await terms.isDisabled(),'reread preserves locked draft');
 await page.reload();await page.getByRole('button',{name:'质量与过滤策略',exact:true}).click();
 await policy().getByRole('button',{name:'重试同一发布命令',exact:true}).waitFor();
 original(publishPending,await stored('policy'),'page reload preserves original policy command');
 check(calls.length===6,'page reload never publishes');
 await policy().getByRole('button',{name:'重试同一发布命令',exact:true}).click();
 await policy().getByRole('status').filter({hasText:'版本 2 已发布'}).waitFor();
 check(calls.length===7,'exactly one explicit original publish retry');original(calls[5],calls[6],'original publish key and body');
 check(await stored('policy')===null,'policy pending cleared');
 const revision=await context.request.get(base+'/api/v1/policies/default/revisions/2');
 check(revision.status()===200,'real frozen second revision');
 const doc=await revision.json();check(doc.rules.reject_terms.includes('CompactControlsBlocked'),'actual published rule');
 check((await context.request.get(base+'/api/v1/policies/default/revisions/3')).status()===404,'retry creates no third revision');
 await layout(policy(),'policy-confirmed');
 check(losses.length===3,'three genuine committed responses lost');
 check(new Set(calls.map(c=>c.key)).size===4&&calls.every(c=>Boolean(c.key)),'four unique original commands');
 await healthyStorage();assert.deepEqual(errors,[]);
 await writeFile(join(shots,'controls-and-pending.json'),JSON.stringify({checks,SQL_display_only:false,secretMasked:true,losses,states:evidence},null,2),{mode:0o600});
 console.log('M6_COMPACT_CONTROLS_BROWSER_PASS '+JSON.stringify({checks,viewports:[1280,768,375,320],commands:4,explicitRetries:3,screenshots:evidence.length,secretMasked:true,secretStorage:false,SQL_display_only:false}));
} catch(error) {
 // Report only a known phase, exception class and this script's line number.
 const kind=error instanceof Error&&/^[A-Za-z]+$/.test(error.name)?error.name:'OtherError';
 const source=/m6-compact-controls-browser\.mjs:\d+:\d+/.exec(String(error?.stack??''))?.[0]??'unknown';
 console.error('M6_COMPACT_CONTROLS_BROWSER_FAILED phase='+phase+' type='+kind+' at='+source);
 process.exitCode=1;
} finally {await context.close();await browser.close();}
