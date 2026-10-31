import assert from 'node:assert/strict';
import {mkdtemp,rm} from 'node:fs/promises';
import {fileURLToPath} from 'node:url';
import {createServer} from 'vite';
import {chromium} from 'playwright-core';

const root=fileURLToPath(new URL('..',import.meta.url));
const temporary=await mkdtemp('/data/temp/pulse-member-reset.Lbqw1v/browser.');
const originalTemp={TMPDIR:process.env.TMPDIR,TMP:process.env.TMP,TEMP:process.env.TEMP};
Object.assign(process.env,{TMPDIR:temporary,TMP:temporary,TEMP:temporary});
let server,browser;
const fixture=`import React from 'react';
import {createRoot} from 'react-dom/client';
import {AccountManagementPanel} from '/src/features/auth/AccountManagementPanel.tsx';
import '/src/assets/main.css';import '/src/assets/workspace.css';import '/src/assets/redesign.css';
createRoot(document.getElementById('root')).render(React.createElement(AccountManagementPanel));`;
const auth=`import {useSyncExternalStore} from 'react';let user={id:'admin-a',role:'admin'};const listeners=new Set();window.fixtureAccount=u=>{user=u;for(const listener of listeners)listener();};export function useAuth(){return {user:useSyncExternalStore(cb=>{listeners.add(cb);return()=>listeners.delete(cb);},()=>user),checkStatus:async()=>{}};}`;
const client=`async function request(method,path,options={}){for(const [k,v]of Object.entries(options.params?.path||{}))path=path.replace('{'+k+'}',encodeURIComponent(v));const response=await fetch(path,{method,headers:{'Content-Type':'application/json',...options.params?.header},body:options.body?JSON.stringify(options.body):undefined,signal:options.signal,cache:options.cache});let value;try{value=await response.json();}catch{}return {response,data:response.ok?value:undefined,error:response.ok?undefined:value};}export const api={GET:(p,o)=>request('GET',p,o),POST:(p,o)=>request('POST',p,o),PUT:(p,o)=>request('PUT',p,o)};`;
try{
 server=await createServer({root,configFile:false,envFile:false,esbuild:{jsx:'automatic'},cacheDir:temporary+'/vite',resolve:{alias:{'@':root+'/src'}},server:{host:'127.0.0.1',port:0},plugins:[{
  name:'member-reset-fixture',enforce:'pre',resolveId(id){if(id==='/__fixture.js')return '\0fixture-entry';if(id==='./AuthContext')return '\0fixture-auth';if(id==='@/api/client'||id===root+'/src/api/client')return '\0fixture-client';},
  load(id){if(id==='\0fixture-entry')return fixture;if(id==='\0fixture-auth')return auth;if(id==='\0fixture-client')return client;},
  configureServer(s){s.middlewares.use(async(req,res,next)=>{if(req.url==='/__member-reset'){res.setHeader('Content-Type','text/html');res.end(await s.transformIndexHtml('/__member-reset','<!doctype html><html><head><meta name="viewport" content="width=device-width, initial-scale=1"></head><body><main id="root" style="padding:16px;min-width:0"></main><script type="module" src="/__fixture.js"></script></body></html>'));}else next();});}
 }]});await server.listen();
 const address=server.httpServer.address(),base=`http://127.0.0.1:${address.port}`;
 browser=await chromium.launchPersistentContext(temporary+'/chrome',{executablePath:process.env.PULSE_TEST_BROWSER_EXECUTABLE||'/usr/bin/google-chrome',headless:true,args:['--no-sandbox','--disable-dev-shm-usage','--disable-background-networking'],viewport:{width:1440,height:1000}});
 const page=await browser.newPage();page.setDefaultTimeout(10000);const errors=[],posts=[];let fail=true,receipt=false;
 page.on('pageerror',error=>errors.push(error.message));
 const accounts=[{id:'admin-a',username:'My Admin',role:'admin',is_active:true},{id:'admin-b',username:'Other Admin',role:'admin',is_active:false},{id:'member-a',username:'Active Member',role:'member',is_active:true},{id:'member-b',username:'Disabled Member',role:'member',is_active:false}];
 await page.route('**/api/v1/**',async route=>{const request=route.request(),path=new URL(request.url()).pathname;
  if(path==='/api/v1/accounts'&&request.method()==='GET')return route.fulfill({json:{items:accounts}});
  if(path.startsWith('/api/v1/auth/security/commands/'))return route.fulfill({status:receipt?200:404,json:receipt?{action:'account.member_password.reset:member-b'}:{code:'NOT_FOUND'}});
  if(path.endsWith('/password')&&request.method()==='POST'){posts.push({path,key:request.headers()['idempotency-key'],body:request.postDataJSON()});if(fail)return route.abort('connectionfailed');return route.fulfill({json:{account_id:'member-b'}});}
  return route.fulfill({status:404,json:{code:'FIXTURE_UNEXPECTED'}});
 });
 await page.goto(base+'/__member-reset');await page.getByRole('button',{name:'重置密码 Disabled Member',exact:true}).waitFor();
 assert.equal(await page.getByRole('button',{name:/重置密码 .*Admin/}).count(),0);
 await page.getByRole('button',{name:'重置密码 Disabled Member',exact:true}).click();
 for(const width of [1440,375,320]){await page.setViewportSize({width,height:1000});await page.locator('#member-reset-new').scrollIntoViewIfNeeded();assert(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),`overflow ${width}`);const small=await page.locator('.account-security button,[id^=member-reset-]').evaluateAll(nodes=>nodes.filter(n=>n.getBoundingClientRect().height>0&&n.getBoundingClientRect().height<44).map(n=>n.id||n.textContent));assert.deepEqual(small,[],`44px controls ${width}`);console.log(JSON.stringify({width,overflow:false,minControlHeight:44}));}
 await page.locator('#member-reset-current').fill('Admin-fixture-123');await page.locator('#member-reset-new').fill('Member-fixture-456');await page.locator('#member-reset-factor').fill('123456');await page.getByRole('button',{name:'确认重置成员密码',exact:true}).click();await page.getByRole('alert').filter({hasText:'连接中断'}).waitFor();
 assert.equal(posts.length,1);assert.equal(posts[0].path,'/api/v1/accounts/member-b/password');for(const id of ['member-reset-current','member-reset-new','member-reset-factor'])assert.equal(await page.locator('#'+id).inputValue(),'');
 assert(await page.getByRole('button',{name:'管理 Active Member',exact:true}).isDisabled());assert(await page.getByRole('button',{name:'创建账户',exact:true}).isDisabled());
 await page.getByRole('button',{name:'读取原命令回执',exact:true}).click();await page.getByRole('alert').filter({hasText:'尚未读取到原回执'}).waitFor();assert.equal(posts.length,1);
 await page.locator('#member-reset-current').fill('Admin-fixture-123');await page.locator('#member-reset-new').fill('Member-fixture-456');await page.locator('#member-reset-factor').fill('123456');await page.getByRole('button',{name:'使用原键提交密码重置',exact:true}).click();await page.getByRole('alert').filter({hasText:'连接中断'}).waitFor();assert.equal(posts.length,2);assert.equal(posts[0].key,posts[1].key);assert.deepEqual(posts[0].body,posts[1].body);
 const journal=await page.evaluate(()=>Object.values(localStorage));assert.equal(journal.length,1);assert.deepEqual(Object.keys(JSON.parse(journal[0])).sort(),['action','key']);assert(!journal[0].includes('fixture-'));assert(!journal[0].includes('123456'));
 receipt=true;await page.getByRole('button',{name:'读取原命令回执',exact:true}).click();await page.locator('#member-reset-new').waitFor({state:'detached'});assert.equal(posts.length,2);assert.equal(await page.evaluate(()=>localStorage.length),0);
 await page.getByRole('button',{name:'重置密码 Active Member',exact:true}).click();await page.locator('#member-reset-current').fill('clear-this-fixture');await page.evaluate(()=>window.fixtureAccount({id:'admin-b',role:'admin'}));await page.locator('#member-reset-current').waitFor({state:'detached'});
 await page.getByRole('button',{name:'重置密码 Active Member',exact:true}).click();assert.equal(await page.locator('#member-reset-current').inputValue(),'');await page.evaluate(()=>window.fixtureAccount({id:'admin-b',role:'member'}));await page.getByRole('heading',{name:'家庭账户',exact:true}).waitFor({state:'detached'});
 assert.deepEqual(errors,[]);console.log(JSON.stringify({passed:true,controlledAPI:true,realBackend:false,originalKeyRetry:true,receiptGETOnly:true,disabledMember:true,secretStorage:false,identityClears:true}));
}finally{await browser?.close();await server?.close();await rm(temporary,{recursive:true,force:true});for(const [key,value]of Object.entries(originalTemp)){if(value===undefined)delete process.env[key];else process.env[key]=value;}}
