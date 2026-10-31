import assert from "node:assert/strict";
import { mkdtemp, rm } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { createServer } from "vite";
import react from "@vitejs/plugin-react";
import { chromium } from "playwright-core";

const { MP31_BACKEND: backend, MP31_SUBSCRIPTION: subscriptionId, MP31_UNIT: unitId, MP31_PASSWORD: password, PULSE_TEST_TEMP_ROOT: tempRoot } = process.env;
assert(backend && subscriptionId && unitId && password && typeof tempRoot === "string" && path.resolve(tempRoot).startsWith("/data/temp/"));
const work = await mkdtemp(path.join(tempRoot, "chrome-"));
const root = fileURLToPath(new URL("..", import.meta.url));
// The entry composes real production components. No auth/API modules are mocked.
const entry = `import React,{useState,useEffect}from'react';import{createRoot}from'react-dom/client';import{AuthProvider}from'/src/features/auth/AuthContext.tsx';import{TVEpisodeUpgradeWorkspace}from'/src/features/configuration/TVEpisodeUpgradeWorkspace.tsx';function Screen(){const[s,set]=useState(null);useEffect(()=>{fetch('/api/v1/subscriptions/${subscriptionId}').then(r=>r.ok?r.json():null).then(set)},[]);return s?React.createElement(TVEpisodeUpgradeWorkspace,{subscription:s}):null}createRoot(document.getElementById('root')).render(React.createElement(AuthProvider,null,React.createElement(Screen)));`;
let server, browser;
try {
 server = await createServer({ configFile: false, root, cacheDir: path.join(work,"vite"), optimizeDeps: { noDiscovery: true, include: ["react","react-dom/client","react/jsx-dev-runtime"] }, plugins: [react(), { name: "mp31-real-entry", resolveId(id) { if(id === "virtual:mp31") return "\0mp31.jsx"; }, load(id) { if(id === "\0mp31.jsx") return entry; }, configureServer(vite) { vite.middlewares.use(async(req,res,next) => { if(req.url?.startsWith("/mp31")) { res.setHeader("Content-Type","text/html"); res.end(await vite.transformIndexHtml(req.url,'<!doctype html><html lang="zh"><meta name="viewport" content="width=device-width,initial-scale=1"><link rel="stylesheet" href="/src/assets/main.css"><div id="root"></div><script type="module" src="/@id/__x00__mp31.jsx"></script></html>')); } else next(); }); } }], resolve: { alias: { "@": path.join(root,"src") } }, server: { host:"127.0.0.1",port:0,proxy: { "/api": { target: backend, changeOrigin: true, configure(proxy) { proxy.on("proxyReq",(request,req) => { if(req.headers.origin) request.setHeader("Origin",backend); }); } } } } });
 await server.listen(); const base = `http://127.0.0.1:${server.httpServer.address().port}`;
 browser = await chromium.launch({ executablePath: process.env.CHROME_BIN || "/usr/bin/google-chrome", headless:true, args:["--no-sandbox","--disable-dev-shm-usage"], downloadsPath:path.join(work,"downloads") });
 const context = await browser.newContext(); const page = await context.newPage(); page.setDefaultTimeout(12000);
 const errors=[],requests=[];page.on("pageerror",e=>errors.push(String(e)));page.on("request",r=>{if(new URL(r.url()).pathname.startsWith("/api/"))requests.push({method:r.method(),path:new URL(r.url()).pathname,body:r.postDataJSON()});});
 page.on("requestfailed",r=>{if(new URL(r.url()).pathname.endsWith("/tv-episode-upgrade-previews")) console.log("MP31_PREVIEW_REQUEST_FAILED",r.failure()?.errorText)});
 await page.goto(base+"/mp31");
 const login=await page.evaluate(async({password})=>{const r=await fetch('/api/v1/auth/login',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({username:'creation-owner',password})});return r.status;},{password});assert.equal(login,200);
 await page.reload();
 const ready=await page.evaluate(async({subscriptionId})=>{const s=await fetch('/api/v1/subscriptions/'+subscriptionId);const value=await s.json();const a=await fetch('/api/v1/auth/me');const auth=await a.json();return{subscriptionStatus:s.status,subscriptionKeys:Object.keys(value),mediaType:value.media_type,authStatus:a.status,role:auth.user?.role??auth.role};},{subscriptionId});
 console.log('MP31_WORKSPACE_BOOT',JSON.stringify(ready));
 assert.equal(ready.subscriptionStatus,200);assert.equal(ready.mediaType,'tv');
 await page.getByRole('button',{name:'读取当前文件与订阅',exact:true}).click().catch(async error=>{const status=await page.evaluate(async()=>{const r=await fetch('/api/v1/auth/status');const v=await r.json();return{http:r.status,initialized:v.initialized,currentRole:v.current_user?.role,keys:Object.keys(v)}});console.log('MP31_BOOT_FAILURE',JSON.stringify({status,errors,body:(await page.locator('body').innerText()).slice(0,300),requests:requests.map(r=>r.method+' '+r.path)}));throw error;});
 const currentFileId=await page.evaluate(async({subscriptionId,unitId})=>{const r=await fetch('/api/v1/subscriptions/'+subscriptionId+'/tv-episode-discovery/'+unitId);return (await r.json()).current_file_id;},{subscriptionId,unitId});
 assert(currentFileId);await page.getByLabel('当前文件',{exact:true}).selectOption(currentFileId);
 await page.getByRole('button',{name:'核验标准集映射',exact:true}).click();
 const panel=page.getByRole("region",{name:"持续剧集候选发现",exact:true});
 const compares=panel.getByRole("button",{name:"比较本集候选品质",exact:true});await compares.nth(50).waitFor();assert.equal(await compares.count(),51);
 assert.equal(requests.filter(r=>r.method==='POST'&&!r.path.endsWith('/auth/login')).length,0);
 const [comparisonResponse]=await Promise.all([page.waitForResponse(response=>new URL(response.url()).pathname.endsWith('/tv-episode-upgrade-previews')),panel.getByRole('listitem').filter({hasText:'2160p'}).getByRole('button',{name:'比较本集候选品质',exact:true}).click({timeout:6000})]).catch(async error=>{console.log('MP31_COMPARISON_FAILURE',JSON.stringify({errors,controls:await compares.evaluateAll(buttons=>buttons.map(b=>({disabled:b.disabled,text:b.closest('li')?.innerText.slice(0,160)}))),posts:requests.filter(r=>r.method==='POST').map(r=>r.path)}));throw error;});const comparison=await comparisonResponse.json();console.log('MP31_COMPARISON',JSON.stringify({http:comparisonResponse.status(),code:comparison.code,comparable:comparison.comparable,improvement:comparison.improvement,reason:comparison.reason}));assert.equal(comparisonResponse.status(),200);await panel.getByText("当前证据表明此候选可改善本集品质。",{exact:true}).waitFor();
 const writes=requests.filter(r=>r.method==='POST'&&!r.path.endsWith('/auth/login'));assert.equal(writes.length,1);assert(writes[0].path.endsWith('/tv-episode-upgrade-previews'));assert.equal(writes[0].body.unit_id,unitId);
 await panel.getByRole("button",{name:"选择并进入本集升级确认",exact:true}).click();
 await page.getByRole("heading",{name:"TV 单集品质升级",exact:true}).waitFor();await page.getByRole("button",{name:"读取升级比较",exact:true}).click();
 await page.getByText("候选严格优于旧文件。比较结果尚未授权下载。",{exact:true}).waitFor();
 assert.equal(requests.filter(r=>r.path.endsWith('/tv-episode-upgrade-commands')).length,0);
 for(const width of [1440,390,320]) { await page.setViewportSize({width,height:1000}); await page.screenshot({path:path.join(process.env.PULSE_ACCEPTANCE_SCREENSHOT_DIR || tempRoot,`mp31-${width}.png`),fullPage:true}); assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth),false,`overflow at ${width}`); const controls=await page.getByRole("button").evaluateAll(buttons=>buttons.map(b=>({w:b.getBoundingClientRect().width,h:b.getBoundingClientRect().height})));assert(controls.every(b=>b.w>=44&&b.h>=44)); }
 await page.getByRole('button',{name:'读取当前文件与订阅',exact:true}).click();
 await page.getByLabel('当前文件',{exact:true}).selectOption(currentFileId);await page.getByRole('button',{name:'核验标准集映射',exact:true}).click();
 await compares.nth(50).waitFor();await page.getByRole('button',{name:'读取当前文件与订阅',exact:true}).click();
 await page.getByLabel('当前文件',{exact:true}).selectOption(currentFileId);await page.getByRole('button',{name:'核验标准集映射',exact:true}).click();
 await compares.nth(50).waitFor();await panel.getByRole('button',{name:'刷新本集发现结果',exact:true}).click();await compares.nth(50).waitFor();assert.equal(await panel.getByRole('button',{name:'选择并进入本集升级确认',exact:true}).count(),0);
 await panel.getByRole('listitem').filter({hasText:'1080p'}).first().getByRole('button',{name:'比较本集候选品质',exact:true}).click();await panel.getByText('当前证据未证明此候选可改善本集品质。',{exact:true}).waitFor();assert.equal(await panel.getByRole('button',{name:'选择并进入本集升级确认',exact:true}).count(),0);
 assert.deepEqual(errors,[]); console.log('PASS Chrome actual production components + real Cookie/PG App: 51 candidates, page-two explicit TV Preview + existing TV panel recompare, same-quality rejection, refresh clears selection, zero automatic commands, 1440/390/320px, zero page errors, zero skips');
 await context.close();
} finally { await browser?.close(); await server?.close(); await rm(work,{recursive:true,force:true}); }
