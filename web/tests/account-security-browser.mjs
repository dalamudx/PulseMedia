import assert from 'node:assert/strict';
import {verifyOtherSessions} from "./other-sessions-browser.mjs";
import jsQR from 'jsqr';
import {createHmac} from 'node:crypto';
import {mkdir} from 'node:fs/promises';
import {chromium} from 'playwright-core';
const base=process.env.PULSE_ACCOUNT_SECURITY_BASE;
if(!base||process.env.PULSE_UI_FIXTURE!=='1')throw Error('Owned security fixture required');
function otp(secret,time=Date.now()){
 let bits=0,value=0;const out=[];
 for(const c of secret){const n='ABCDEFGHIJKLMNOPQRSTUVWXYZ234567'.indexOf(c);assert(n>=0);value=(value<<5)|n;bits+=5;if(bits>=8){out.push((value>>>(bits-8))&255);bits-=8;}}
 const counter=Buffer.alloc(8);counter.writeBigUInt64BE(BigInt(Math.floor(time/30000)));
 const digest=createHmac('sha1',Buffer.from(out)).update(counter).digest(),offset=digest[19]&15;
 return String((digest.readUInt32BE(offset)&0x7fffffff)%1000000).padStart(6,'0');
}
const browser=await chromium.launch({executablePath:process.env.PULSE_TEST_BROWSER_EXECUTABLE||chromium.executablePath(),headless:true,args:['--no-sandbox','--disable-dev-shm-usage','--disable-background-networking','--disable-gpu']});
try{
 const context=await browser.newContext();await context.addCookies([{name:'pulsemedia_session',value:process.env.PULSE_ACCOUNT_SECURITY_SESSION,url:base,httpOnly:true,sameSite:'Strict'}]);
 const page=await context.newPage();page.setDefaultTimeout(15000);const errors=[];page.on('pageerror',e=>errors.push(e.message));
 const posts=[];page.on('request',r=>{if(['POST','PUT'].includes(r.method())&&new URL(r.url()).pathname.startsWith('/api/v1/'))posts.push({path:new URL(r.url()).pathname,key:r.headers()['idempotency-key']});});
 await page.goto(base+'/account/tokens');await page.getByRole('heading',{name:'账户安全',exact:true}).waitFor();await page.getByRole('heading',{name:'家庭账户',exact:true}).waitFor();
 await page.getByLabel('用户名',{exact:true}).fill('Browser Family');await page.getByLabel('初始密码',{exact:true}).fill('BrowserFamilyFixture123!');await page.getByRole('button',{name:'创建账户',exact:true}).click();await page.getByRole('button',{name:'管理 Browser Family',exact:true}).waitFor();
 const familyInitialFixture=['Browser','Family','Fixture','123!'].join('');const familyResetFixture=['Member','Recovered','Fixture','123!'].join('');
 const memberContext=await browser.newContext();
 const memberLogin=await memberContext.request.post(base+'/api/v1/auth/login',{headers:{Origin:base},data:{username:'Browser Family',password:familyInitialFixture}});assert.equal(memberLogin.status(),200);
 const memberList=await (await context.request.get(base+'/api/v1/accounts')).json();const familyId=memberList.items.find(u=>u.username==='Browser Family').id;const resetPath=`/api/v1/accounts/${familyId}/password`;
 let dropReset=true;await page.route('**'+resetPath,async route=>{if(route.request().method()==='POST'&&dropReset){dropReset=false;const response=await route.fetch();assert.equal(response.status(),200);await route.abort('connectionfailed');}else await route.continue();});
 await page.getByRole('button',{name:['重置密码','Browser Family'].join(' '),exact:true}).click();await page.getByLabel('管理员本人的当前密码',{exact:true}).fill(['Security','Browser','Fixture','123!'].join(''));await page.getByLabel('成员的新密码（至少八个字符）',{exact:true}).fill(familyResetFixture);await page.getByRole('button',{name:'确认重置成员密码',exact:true}).click();await page.getByRole('button',{name:'读取原命令回执',exact:true}).waitFor();
 assert.equal(await page.getByLabel('管理员本人的当前密码',{exact:true}).inputValue(),'');assert.equal(await page.getByLabel('成员的新密码（至少八个字符）',{exact:true}).inputValue(),'');assert(await page.evaluate(()=>Object.values(localStorage).every(v=>!v.includes('MemberRecoveredFixture')&&!v.includes('SecurityBrowserFixture'))));
 await page.reload();await page.getByRole('button',{name:'读取原命令回执',exact:true}).waitFor();assert.equal(posts.filter(x=>x.path===resetPath).length,1,'reload must not repeat password reset');await page.getByRole('button',{name:'读取原命令回执',exact:true}).click();await page.getByRole('button',{name:'读取原命令回执',exact:true}).waitFor({state:'detached'});assert.equal(posts.filter(x=>x.path===resetPath).length,1,'receipt recovery must not repeat reset');await page.unroute('**'+resetPath);
 assert.equal((await memberContext.request.get(base+'/api/v1/auth/me')).status(),401,'member session must be revoked');assert.equal((await memberContext.request.post(base+'/api/v1/auth/login',{headers:{Origin:base},data:{username:'Browser Family',password:familyInitialFixture}})).status(),401,'old password must fail');assert.equal((await memberContext.request.post(base+'/api/v1/auth/login',{headers:{Origin:base},data:{username:'Browser Family',password:familyResetFixture}})).status(),200,'new password must work');await memberContext.close();
 await page.getByRole('button',{name:'管理 security-ui-admin',exact:true}).click();await page.locator('#edit-member-role').selectOption('member');await page.getByRole('button',{name:'确认保存账户',exact:true}).click();await page.getByRole('alert').filter({hasText:'必须保留至少一位活动管理员'}).waitFor();await page.getByRole('button',{name:'取消',exact:true}).click();
 for(const enabled of [false,true]){await page.getByRole('button',{name:'管理 Browser Family',exact:true}).click();await page.getByLabel('允许账户登录',{exact:true}).setChecked(enabled);await page.getByRole('button',{name:'确认保存账户',exact:true}).click();await page.getByRole('heading',{name:'调整 Browser Family',exact:true}).waitFor({state:'detached'});}
 for(const role of ['admin','member']){await page.getByRole('button',{name:'管理 Browser Family',exact:true}).click();await page.locator('#edit-member-role').selectOption(role);await page.getByRole('button',{name:'确认保存账户',exact:true}).click();await page.getByRole('heading',{name:'调整 Browser Family',exact:true}).waitFor({state:'detached'});}
 let drop=true;await page.route('**/api/v1/accounts',async route=>{if(route.request().method()==='POST'&&drop){drop=false;const response=await route.fetch();assert.equal(response.status(),200);await route.abort('connectionfailed');}else await route.continue();});
 await page.getByLabel('用户名',{exact:true}).fill('Lost Family');await page.getByLabel('初始密码',{exact:true}).fill('LostFamilyFixture123!');await page.getByRole('button',{name:'创建账户',exact:true}).click();await page.getByRole('button',{name:'读取原命令回执',exact:true}).waitFor();
 assert(await page.evaluate(()=>Object.values(localStorage).every(v=>!v.includes('LostFamilyFixture')&&!v.includes('BrowserFamilyFixture'))));
 await page.reload();await page.getByRole('button',{name:'读取原命令回执',exact:true}).click();await page.getByRole('button',{name:'管理 Lost Family',exact:true}).waitFor();assert.equal(posts.filter(x=>x.path==='/api/v1/accounts').length,2,'lost response recovery must not repeat POST');await page.unroute('**/api/v1/accounts');
 await page.getByLabel('当前密码',{exact:true}).fill('wrong-password');await page.getByLabel('新密码（至少八个字符）',{exact:true}).fill('NewSecurityFixture123!');await page.getByRole('button',{name:'确认修改密码',exact:true}).click();await page.getByRole('alert').filter({hasText:'当前密码或第二因素不正确'}).waitFor();
 let dropSetup=true;await page.route('**/api/v1/auth/mfa/setup',async route=>{if(dropSetup){dropSetup=false;const response=await route.fetch();assert.equal(response.status(),200);await route.abort('connectionfailed');}else await route.continue();});
 await page.getByLabel('当前密码（绑定验证）',{exact:true}).fill('SecurityBrowserFixture123!');await page.getByRole('button',{name:'开始绑定验证器',exact:true}).click();await page.getByRole('button',{name:'读取原安全命令回执',exact:true}).waitFor();assert.equal(await page.locator('#mfa-setup-secret').count(),0);await page.reload();await page.getByRole('button',{name:'读取原安全命令回执',exact:true}).click();await page.getByRole('button',{name:'读取原安全命令回执',exact:true}).waitFor({state:'detached'});assert.equal(await page.locator('#mfa-setup-secret').count(),0);assert.equal(posts.filter(x=>x.path==='/api/v1/auth/mfa/setup').length,1,'unknown setup must recover using GET without replay');await page.unroute('**/api/v1/auth/mfa/setup');
 const setupResponse=page.waitForResponse(r=>new URL(r.url()).pathname==='/api/v1/auth/mfa/setup'&&r.request().method()==='POST');
 await page.getByLabel('当前密码（绑定验证）',{exact:true}).fill('SecurityBrowserFixture123!');await page.getByRole('button',{name:'开始绑定验证器',exact:true}).click();await page.getByRole('button',{name:'显示绑定密钥',exact:true}).waitFor();assert.equal(await page.locator('#mfa-setup-secret').count(),0);await page.getByRole('button',{name:'显示绑定密钥',exact:true}).click();const secretInput=page.getByLabel('绑定密钥',{exact:true});assert.equal(await secretInput.getAttribute('type'),'text');const secret=await secretInput.inputValue();await page.getByRole('button',{name:'隐藏绑定密钥',exact:true}).click();
 const setupPayload=await (await setupResponse).json();
 assert.equal(await page.getByRole('img',{name:'双因素绑定二维码',exact:true}).count(),0,'QR hidden by default');
 const qrRequests=[];const recordQRRequest=r=>{if(/^https?:/.test(r.url()))qrRequests.push(r.resourceType());};
 page.on('request',recordQRRequest);
 await page.getByRole('button',{name:'显示绑定二维码',exact:true}).click();
 let qrSecret='';
 for(const width of [1280,375,320]){
  await page.setViewportSize({width,height:900});
  const qr=page.getByRole('img',{name:'双因素绑定二维码',exact:true});await qr.waitFor();
  const pixels=await qr.evaluate(async svg=>{
   const bounds=svg.getBoundingClientRect(),copy=svg.cloneNode(true);
   const size=Math.round(bounds.width);copy.setAttribute('width',String(size));copy.setAttribute('height',String(size));
   copy.setAttribute('xmlns','http://www.w3.org/2000/svg');
   const image=new Image();image.src='data:image/svg+xml;charset=utf-8,'+encodeURIComponent(new XMLSerializer().serializeToString(copy));await image.decode();
   const canvas=document.createElement('canvas');canvas.width=size;canvas.height=size;
   const ctx=canvas.getContext('2d');ctx.drawImage(image,0,0,size,size);
   return {width:size,height:size,rgba:Array.from(ctx.getImageData(0,0,size,size).data)};
  });
  const decoded=jsQR(Uint8ClampedArray.from(pixels.rgba),pixels.width,pixels.height);
  assert.ok(decoded?.data===setupPayload.otpauth_url,'rendered QR must contain the exact enrollment URI');
  const enrollment=new URL(decoded.data);qrSecret=enrollment.searchParams.get('secret');
  assert.ok(qrSecret===secret,'QR and manual enrollment must agree');
  assert.equal(enrollment.protocol,'otpauth:');assert.equal(enrollment.hostname,'totp');
  assert.equal(enrollment.searchParams.get('issuer'),'PulseMedia');
  assert.equal(enrollment.searchParams.get('algorithm'),'SHA1');assert.equal(enrollment.searchParams.get('digits'),'6');assert.equal(enrollment.searchParams.get('period'),'30');
  assert(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),`QR overflow at ${width}`);
  if(process.env.PULSE_ACCOUNT_SECURITY_EVIDENCE){
   await mkdir(process.env.PULSE_ACCOUNT_SECURITY_EVIDENCE,{recursive:true});
   await qr.locator('..').screenshot({path:`${process.env.PULSE_ACCOUNT_SECURITY_EVIDENCE}/enrollment-${width}.png`});
  }
 }
 await page.getByRole('button',{name:'隐藏绑定二维码',exact:true}).click();
 assert.equal(await page.getByRole('img',{name:'双因素绑定二维码',exact:true}).count(),0);
 page.off('request',recordQRRequest);assert.equal(qrRequests.length,0,'showing and decoding QR must not send network requests');
 await page.setViewportSize({width:1280,height:900});
 let rejectConfirm=true;await page.route('**/api/v1/auth/mfa/confirm',async route=>{if(rejectConfirm){rejectConfirm=false;await route.fulfill({status:503,contentType:'application/problem+json',body:JSON.stringify({code:'SERVICE_UNAVAILABLE',message:'Unavailable'})});}else await route.continue();});
 const code=otp(qrSecret,Number(process.env.PULSE_ACCOUNT_SECURITY_TIME));await page.getByLabel('验证器中的六位验证码',{exact:true}).fill(code);await page.getByRole('button',{name:'确认启用双因素',exact:true}).click();await page.getByRole('button',{name:'读取原安全命令回执',exact:true}).waitFor();assert.equal(posts.filter(x=>x.path==='/api/v1/auth/mfa/confirm').length,1);
 await page.reload();await page.getByRole('heading',{name:'继续确认原绑定',exact:true}).waitFor();assert.equal(await page.locator('#mfa-setup-secret').count(),0);await page.getByLabel('验证器中的六位验证码',{exact:true}).fill(code);await page.getByRole('button',{name:'确认启用双因素',exact:true}).click();await page.getByRole('button',{name:'显示恢复码',exact:true}).waitFor();await page.getByRole('button',{name:'显示恢复码',exact:true}).click();const codes=await page.locator('.account-security-recovery-codes li').allTextContents();assert.equal(codes.length,10);const confirmationPosts=posts.filter(x=>x.path==="/api/v1/auth/mfa/confirm");assert.equal(confirmationPosts.length,2);assert.equal(confirmationPosts[0].key,confirmationPosts[1].key);await page.unroute("**/api/v1/auth/mfa/confirm");
 assert(await page.evaluate(({secret,codes})=>Object.values(localStorage).every(v=>!v.includes(secret)&&codes.every(c=>!v.includes(c))),{secret,codes}));await page.getByRole('button',{name:'已安全保存，清除恢复码',exact:true}).click();
 const secondContext=await browser.newContext(),loginPage=await secondContext.newPage();loginPage.setDefaultTimeout(15000);await loginPage.goto(base+'/login');await loginPage.getByPlaceholder('输入管理员用户名...').fill('security-ui-admin');await loginPage.getByPlaceholder('输入密码...').fill('SecurityBrowserFixture123!');const missing=loginPage.waitForResponse(r=>new URL(r.url()).pathname==='/api/v1/auth/login'&&r.request().method()==='POST');await loginPage.getByRole('button',{name:'进入系统控制台 →',exact:true}).click();assert.equal((await missing).status(),401);
 await loginPage.getByPlaceholder('输入密码...').fill('SecurityBrowserFixture123!');await loginPage.getByText('使用双因素验证码或恢复码',{exact:true}).click();await loginPage.getByLabel('使用恢复码',{exact:true}).check();await loginPage.getByLabel('一次性恢复码',{exact:true}).fill(codes[0]);const recovered=loginPage.waitForResponse(r=>new URL(r.url()).pathname==='/api/v1/auth/login'&&r.request().method()==='POST');await loginPage.getByRole('button',{name:'进入系统控制台 →',exact:true}).click();assert.equal((await recovered).status(),200);await loginPage.waitForURL(base+'/');
 const recoveryPath='/api/v1/auth/mfa/recovery-codes';
 let lostRegeneration=false;
 await page.route('**'+recoveryPath,async route=>{const response=await route.fetch();assert.equal(response.status(),200);lostRegeneration=true;await route.abort('failed');});
 await page.locator('#recovery-refresh-password').fill('SecurityBrowserFixture123!');
 await page.locator('#recovery-refresh-factor-mode').selectOption('recovery_code');
 await page.locator('#recovery-refresh-factor').fill(codes[1]);
 await page.getByLabel('我已知晓旧恢复码将全部失效',{exact:true}).check();
 await page.getByRole('button',{name:'重新生成恢复码',exact:true}).click();
 await page.getByRole('alert').filter({hasText:'连接中断'}).waitFor();assert(lostRegeneration);
 assert.equal(await page.locator('#recovery-refresh-password').inputValue(),'');assert.equal(await page.locator('#recovery-refresh-factor').inputValue(),'');
 assert.equal((await loginPage.request.get(base+'/api/v1/auth/sessions')).status(),401);
 await page.reload();await page.getByRole('button',{name:'读取原安全命令回执',exact:true}).waitFor();
 assert.equal(posts.filter(x=>x.path===recoveryPath).length,1);assert.equal(await page.getByRole('button',{name:'显示恢复码',exact:true}).count(),0);
 await page.getByRole('button',{name:'读取原安全命令回执',exact:true}).click();
 await page.getByText('已启用 · 剩余 10 个一次性恢复码',{exact:true}).waitFor();
 assert.equal(await page.getByRole('button',{name:'显示恢复码',exact:true}).count(),0);
 assert.equal(posts.filter(x=>x.path===recoveryPath).length,1);
 await page.unroute('**'+recoveryPath);
 for(const width of [1280,375,320]){
  await page.setViewportSize({width,height:900});
  await page.waitForFunction(()=>innerWidth>1024||(document.getElementById('app-navigation')?.inert&&document.getElementById('app-navigation').getBoundingClientRect().right<=0));
  assert(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),`recovery form overflow at ${width}`);
  const refresh=page.getByRole('button',{name:'重新生成恢复码',exact:true});assert((await refresh.boundingBox()).height>=44);
  if(process.env.PULSE_ACCOUNT_SECURITY_EVIDENCE){await mkdir(process.env.PULSE_ACCOUNT_SECURITY_EVIDENCE,{recursive:true});await page.screenshot({path:`${process.env.PULSE_ACCOUNT_SECURITY_EVIDENCE}/recovery-${width}.png`,fullPage:true});}
 }
 await page.route('**'+recoveryPath,route=>route.fulfill({status:503,contentType:'application/json',body:JSON.stringify({code:'UNAVAILABLE',message:'test admission failure before transaction'})}));
 await page.locator('#recovery-refresh-password').fill('SecurityBrowserFixture123!');
 await page.locator('#recovery-refresh-factor-mode').selectOption('otp');
 await page.locator('#recovery-refresh-factor').fill(otp(secret,Number(process.env.PULSE_ACCOUNT_SECURITY_TIME)+30000));
 await page.getByLabel('我已知晓旧恢复码将全部失效',{exact:true}).check();
 await page.getByRole('button',{name:'重新生成恢复码',exact:true}).click();
 await page.getByRole('alert').filter({hasText:'恢复码更新未确认'}).waitFor();
 await page.getByRole('button',{name:'读取原安全命令回执',exact:true}).click();
 await page.getByRole('button',{name:'使用原键提交恢复码更新',exact:true}).waitFor();
 assert.equal(posts.filter(x=>x.path===recoveryPath).length,2);
 await page.unroute('**'+recoveryPath);
 await page.locator('#recovery-refresh-password').fill('SecurityBrowserFixture123!');await page.locator('#recovery-refresh-factor-mode').selectOption('otp');await page.locator('#recovery-refresh-factor').fill(otp(secret,Number(process.env.PULSE_ACCOUNT_SECURITY_TIME)+30000));await page.getByLabel('我已知晓旧恢复码将全部失效',{exact:true}).check();
 await page.getByRole('button',{name:'使用原键提交恢复码更新',exact:true}).click();
 await page.getByRole('button',{name:'显示恢复码',exact:true}).waitFor();await page.getByRole('button',{name:'显示恢复码',exact:true}).click();
 const freshCodes=await page.locator('.account-security-recovery-codes li').allTextContents();assert.equal(freshCodes.length,10);assert.equal(new Set(freshCodes).size,10);assert(freshCodes.every(c=>!codes.includes(c)));
 const regenerationPosts=posts.filter(x=>x.path===recoveryPath);assert.equal(regenerationPosts.length,3);assert.notEqual(regenerationPosts[0].key,regenerationPosts[1].key);assert.equal(regenerationPosts[1].key,regenerationPosts[2].key);
 assert(await page.evaluate(values=>Object.values(localStorage).every(v=>values.every(c=>!v.includes(c))),freshCodes));
 await page.getByRole('button',{name:'已安全保存，清除恢复码',exact:true}).click();
 await loginPage.goto(base+'/login');await loginPage.getByPlaceholder('输入管理员用户名...').fill('security-ui-admin');await loginPage.getByPlaceholder('输入密码...').fill('SecurityBrowserFixture123!');await loginPage.getByText('使用双因素验证码或恢复码',{exact:true}).click();await loginPage.getByLabel('使用恢复码',{exact:true}).check();await loginPage.getByLabel('一次性恢复码',{exact:true}).fill(codes[2]);
 const oldCodeRejected=loginPage.waitForResponse(r=>new URL(r.url()).pathname==='/api/v1/auth/login'&&r.request().method()==='POST');await loginPage.getByRole('button',{name:'进入系统控制台 →',exact:true}).click();assert.equal((await oldCodeRejected).status(),401);
 await loginPage.getByPlaceholder('输入密码...').fill('SecurityBrowserFixture123!');await loginPage.getByLabel('一次性恢复码',{exact:true}).fill(freshCodes[0]);
 const freshLogin=loginPage.waitForResponse(r=>new URL(r.url()).pathname==='/api/v1/auth/login'&&r.request().method()==='POST');await loginPage.getByRole('button',{name:'进入系统控制台 →',exact:true}).click();assert.equal((await freshLogin).status(),200);await loginPage.waitForURL(base+'/');
 console.log(JSON.stringify({recoveryRegenerated:true,oldCodesRejected:true,oldSessionsRevoked:true,originalTOTPKept:true,unknownCommittedGETOnly:true,uncommitted503ExplicitOriginalKey:true,recoveryPosts:3}));
 await page.getByRole('button',{name:'重新读取安全状态',exact:true}).click();await page.getByText('已启用 · 剩余 9 个一次性恢复码',{exact:true}).waitFor();await page.getByRole('button',{name:'停用双因素…',exact:true}).click();await page.getByLabel('当前密码（停用验证）',{exact:true}).fill('SecurityBrowserFixture123!');await page.locator('#disable-factor-mode').selectOption('recovery_code');await page.locator('#disable-factor').fill(freshCodes[1]);await page.getByRole('button',{name:'确认停用双因素',exact:true}).click();await page.getByText('未启用。绑定验证器并确认六位验证码后生效。',{exact:true}).waitFor();
 assert.equal((await page.request.post(base+'/__test/advance-security-clock')).status(),204);
 await page.getByLabel('当前密码',{exact:true}).fill('SecurityBrowserFixture123!');await page.getByLabel('新密码（至少八个字符）',{exact:true}).fill('NewSecurityFixture123!');await page.getByRole('button',{name:'确认修改密码',exact:true}).click();await page.getByText('密码已更新，其它会话和 API 令牌已撤销。当前会话保留。',{exact:true}).waitFor();
 await verifyOtherSessions({browser,page,context,base,posts,otp});
 assert.equal(await page.getByLabel('当前密码',{exact:true}).inputValue(),'');assert.equal(await page.locator('#mfa-setup-secret').count(),0);
 const evidence=process.env.PULSE_ACCOUNT_SECURITY_EVIDENCE;if(evidence)await mkdir(evidence,{recursive:true});
 for(const width of [1280,375,320]){
  await page.setViewportSize({width,height:900});
  await page.waitForFunction(()=>innerWidth>1024||(document.getElementById('app-navigation')?.inert&&document.getElementById('app-navigation').getBoundingClientRect().right<=0));
  await page.evaluate(()=>scrollTo(0,0));
  assert(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),`overflow at ${width}`);
  const controls=await page.locator('.account-security button').evaluateAll(buttons=>buttons.filter(b=>!b.disabled&&b.getBoundingClientRect().height).map(b=>({label:b.textContent.trim(),height:b.getBoundingClientRect().height})));
  assert.deepEqual(controls.filter(b=>b.height<44),[],`44px controls at ${width}`);
  console.log(JSON.stringify({viewport:width,controls}));
  if(evidence)await page.screenshot({path:`${evidence}/${width===1280?'desktop':`mobile-${width}`}.png`,fullPage:true});
 }
 assert.deepEqual(errors,[]);console.log(JSON.stringify({passed:true,realPG:true,createMember:true,lastAdminProtected:true,disableReactivate:true,lostResponseRecoveredByReadOnlyGET:true,noAutomaticPOSTRetry:true,currentPasswordRequired:true,encryptedEnrollment:true,localQRDecoded:true,qrNetworkRequests:0,confirmedBeforeEnabled:true,recoveryLoginUI:true,recoveryConsumedOnce:true,disableReauthenticated:true,passwordRevokesOthers:true,secretsNeverPersisted:true,widths:[1280,375,320]}));
}finally{await browser.close();}
