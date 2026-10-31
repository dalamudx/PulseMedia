import assert from 'node:assert/strict';
import {randomUUID} from 'node:crypto';
import {mkdir} from 'node:fs/promises';

export async function verifyOtherSessions({browser,page,context,base,posts,otp}) {
 const path='/api/v1/auth/sessions/revoke-others';
 const password=['New','Security','Fixture','123!'].join('');
 const owned=[];
 const post=(url,data,key=randomUUID())=>context.request.post(base+url,{headers:{Origin:base,'Idempotency-Key':key},data});
 const step=await page.request.post(base+'/__test/advance-security-clock');
 assert.equal(step.status(),204);
 const now=Number(step.headers()['x-fixture-time']);assert(now>0);
 const begun=await post('/api/v1/auth/mfa/setup',{current_password:password});assert.equal(begun.status(),200);
 const setup=await begun.json();
 const confirmed=await post('/api/v1/auth/mfa/confirm',{challenge_id:setup.challenge_id,otp:otp(setup.secret,now)});assert.equal(confirmed.status(),200);
 const codes=(await confirmed.json()).recovery_codes;assert.equal(codes.length,10);
 async function login(proof){
  const ctx=await browser.newContext();owned.push(ctx);
  const response=await ctx.request.post(base+'/api/v1/auth/login',{headers:{Origin:base},data:{username:'security-ui-admin',password,...proof}});
  assert.equal(response.status(),200,'fixture secondary login');return ctx;
 }
 async function fill(proof){
  await page.locator('#other-sessions-password').fill(password);
  await page.locator('#other-sessions-factor-mode').selectOption('recovery_code');
  await page.locator('#other-sessions-factor').fill(proof);
  await page.locator('#other-sessions-confirm').check();
 }
 async function refresh(){await page.getByRole('button',{name:'重新读取安全状态',exact:true}).click();}
 const count=()=>posts.filter(p=>p.path===path);
 const readReceipt=()=>page.getByRole('button',{name:'读取原安全命令回执',exact:true});
 try{
  const old=await login({recovery_code:codes[0]});
  const tokenResponse=await post('/api/v1/auth/tokens',{name:'Session retention fixture'});assert.equal(tokenResponse.status(),200);
  const token=(await tokenResponse.json()).secret;assert.equal(typeof token,'string');
  const tokenContext=await browser.newContext();owned.push(tokenContext);
  const tokenStatus=async()=>(await tokenContext.request.get(base+'/api/v1/auth/me',{headers:{Authorization:`Bearer ${token}`}})).status();
  assert.equal(await tokenStatus(),200);
  const original=(await(await context.request.get(base+'/api/v1/auth/sessions')).json()).items.find(s=>s.current).id;
  await refresh();await page.getByRole('button',{name:'撤销其它会话…',exact:true}).click();
  const form=page.getByRole('form',{name:'撤销其它会话',exact:true});
  for(const width of [1280,375,320]){
   await page.setViewportSize({width,height:900});
   await form.evaluate(node=>node.scrollIntoView({block:"center"}));
   assert(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),`session form overflow ${width}`);
   assert.deepEqual(await form.locator('button').evaluateAll(xs=>xs.filter(x=>!x.disabled&&x.getBoundingClientRect().height<44).map(x=>x.textContent)),[]);
   if(process.env.PULSE_ACCOUNT_SECURITY_EVIDENCE){await mkdir(process.env.PULSE_ACCOUNT_SECURITY_EVIDENCE,{recursive:true});await form.screenshot({path:`${process.env.PULSE_ACCOUNT_SECURITY_EVIDENCE}/other-sessions-${width}.png`});}
  }
  await page.setViewportSize({width:1280,height:900});
  let lost=false;
  await page.route('**'+path,async route=>{
   if(!lost&&route.request().method()==='POST'){lost=true;const response=await route.fetch();assert.equal(response.status(),200);await route.abort('connectionfailed');}
   else await route.continue();
  });
  await fill(codes[1]);await page.getByRole('button',{name:'确认撤销其它会话',exact:true}).click();
  await page.getByRole('alert').filter({hasText:'连接中断'}).waitFor();
  assert.equal(count().length,1);assert(lost);
  assert.equal(await page.locator('#other-sessions-password').inputValue(),'');
  assert.equal(await page.locator('#other-sessions-factor').inputValue(),'');
  assert.equal((await old.request.get(base+'/api/v1/auth/me')).status(),401);
  assert.equal((await context.request.get(base+'/api/v1/auth/me')).status(),200);
  assert.equal(await tokenStatus(),200);
  const later=await login({recovery_code:codes[2]});
  await page.reload();await readReceipt().click();await readReceipt().waitFor({state:'detached'});
  assert.equal(count().length,1,'committed lost response recovers by GET');
  assert.equal((await later.request.get(base+'/api/v1/auth/me')).status(),200);
  const replay=await post(path,{current_password:password,recovery_code:codes[1]},count()[0].key);assert.equal(replay.status(),200);
  const receipt=await replay.json();assert.equal(receipt.replayed,true);assert.equal(receipt.session_id,original);
  assert.equal((await later.request.get(base+'/api/v1/auth/me')).status(),200,'replay must preserve later logins');
  await page.unroute('**'+path);
  await refresh();await page.getByRole('button',{name:'撤销其它会话…',exact:true}).click();
  let failed=false;
  await page.route('**'+path,async route=>{
   if(!failed&&route.request().method()==='POST'){failed=true;await route.fulfill({status:503,contentType:'application/json',body:JSON.stringify({code:'UNAVAILABLE',message:'Fixture connection unavailable'})});}
   else await route.continue();
  });
  await fill(codes[3]);await page.getByRole('button',{name:'确认撤销其它会话',exact:true}).click();
  await readReceipt().click();
  const retry=page.getByRole('button',{name:'使用原键撤销其它会话',exact:true});await retry.waitFor();
  assert.equal(count().length,2,'no automatic retry after missing receipt');
  await fill(codes[3]);await retry.click();
  await readReceipt().waitFor({state:'detached'});
  assert.equal(count().length,3);assert.notEqual(count()[0].key,count()[1].key);assert.equal(count()[1].key,count()[2].key);
  assert.equal((await later.request.get(base+'/api/v1/auth/me')).status(),401);
  assert.equal((await context.request.get(base+'/api/v1/auth/me')).status(),200);
  assert.equal(await tokenStatus(),200);
  assert.equal((await(await context.request.get(base+'/api/v1/auth/mfa')).json()).enabled,true);
  await login({otp:otp(setup.secret,now+30000)});
  assert(await page.evaluate(values=>Object.values(localStorage).every(v=>values.every(value=>!v.includes(value))),[password,token,setup.secret,...codes]));
  console.log(JSON.stringify({otherSessionsRevoked:true,currentSessionKept:true,apiTokensKept:true,totpKept:true,committedGETOnly:true,laterLoginSurvivesReplay:true,uncommittedExplicitOriginalKey:true,uiPosts:3,widths:[1280,375,320]}));
 }finally{
  await page.unroute('**'+path);
  await Promise.all(owned.map(ctx=>ctx.close()));
 }
}
