import assert from "node:assert/strict";
import { mkdtemp, writeFile, rm } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import path from "node:path";
import { createServer } from "vite";
import react from "@vitejs/plugin-react";
import { chromium } from "playwright-core";

const evidence = process.env.PULSE_MP_EVIDENCE;
if (!evidence?.startsWith("/data/temp/") || path.resolve(evidence) !== evidence) throw Error("Owned MP evidence directory required");
const web = fileURLToPath(new URL("../", import.meta.url));
const owned = await mkdtemp(path.join(evidence, "panel-browser-"));
let server, browser;
try {
  const auth = path.join(owned, "auth.ts");
  await writeFile(auth, `import {useSyncExternalStore} from 'react';let user={id:"01960000-0000-7000-8000-000000000001",role:"admin"};const listeners=new Set();globalThis.ownedAccountChange=(next)=>{user=next;for(const notify of listeners)notify()};export const useAuth=()=>({user:useSyncExternalStore(notify=>{listeners.add(notify);return()=>listeners.delete(notify)},()=>user)});`);
  await writeFile(path.join(owned, "index.html"), '<!doctype html><html lang="zh-CN"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><div id="root"></div><script type="module" src="/main.tsx"></script></html>');
  await writeFile(path.join(owned, "main.tsx"), `import React from 'react';import{createRoot}from'react-dom/client';import{SeerrSettingsPanel}from'/@fs${web}src/features/configuration/SeerrSettingsPanel.tsx';import{SubtitleCandidatesPanel}from'/@fs${web}src/features/media-scrape/SubtitleCandidatesPanel.tsx';import '/@fs${web}src/assets/main.css';import '/@fs${web}src/features/configuration/configuration.css';createRoot(document.getElementById('root')!).render(<main style={{maxWidth:900,margin:'24px auto',padding:16}}><SeerrSettingsPanel/><SubtitleCandidatesPanel fileId="01960000-0000-7000-8000-000000000002"/></main>);`);
  server = await createServer({ configFile: false, root: owned, cacheDir: path.join(owned, "cache"), plugins: [react()], resolve: { alias: [{ find: "@/features/auth/AuthContext", replacement: auth }, { find: "@", replacement: path.join(web, "src") }, {find:"react",replacement:path.join(web,"node_modules/react")},{find:"react-dom",replacement:path.join(web,"node_modules/react-dom")}] }, server: { host: "127.0.0.1", port: 0, fs: { allow: [owned, web] } }, logLevel: "error" });
  await server.listen(); const address = server.httpServer.address(); const base = `http://127.0.0.1:${address.port}`;
  browser = await chromium.launch({ executablePath: "/usr/bin/google-chrome", headless: true, args: ["--no-sandbox", "--disable-dev-shm-usage", "--disable-background-networking"] });
  const context = await browser.newContext(); const page = await context.newPage(); page.setDefaultTimeout(15000);
  const errors = []; page.on("pageerror", e => errors.push(e.message));
  let config = { enabled: false, version: 0, credential_present: false }, queries = 0, confirmations = 0, writes = 0, dropSave=false,dropConfirmation=false;
  const file = "01960000-0000-7000-8000-000000000002", candidate = "01960000-0000-7000-8000-000000000003";
  const candidateState = outcome => ({ file_id: file, mapping_revision: 7, outcome, candidates: outcome === "candidates" ? [
    { id: candidate, provider: "shooter", language_hint: "Han", format: "srt", summary: { cue_count: 120, start_ms: 0, end_ms: 3_723_004 } },
    { id: `${candidate}-long`, provider: "shooter", language_hint: "Han", format: "srt", summary: { cue_count: 99999, start_ms: 90_061_007, end_ms: 359_999_999 } },
    { id: `${candidate}-missing`, provider: "shooter", language_hint: "Han", format: "srt" },
  ] : [] });
  const requests = []; let selectedCandidate = candidate;
  let state = candidateState("not_queried");
  await page.route("**/api/v1/**", async route => {
    const request = route.request(), target = new URL(request.url()).pathname;
    requests.push(`${request.method()} ${target}`);
    let body;
    if (target === "/api/v1/settings/seerr") {
      if (request.method() === "PUT") { writes++; const input = request.postDataJSON(); assert.equal(input.expected_version, config.version); assert.equal(input.owner_id, "01960000-0000-7000-8000-000000000001"); assert(input.credential.length >= 32); config = { enabled: input.enabled, version: config.version+1, credential_present: true, owner_id: input.owner_id }; if(dropSave){dropSave=false;await route.abort('failed');return;} }
      body = config;
    } else if (target === "/api/v1/seerr/receipts") body = { items: [] };
    else if (target.endsWith("/queries")) { queries++; state = candidateState(queries === 1 ? "provider_error" : queries === 2 ? "not_found" : "candidates"); body = state; assert.equal(request.postDataJSON().expected_mapping_revision, 7); }
    else if (target.endsWith("/confirm")) { confirmations++; assert(target.includes(selectedCandidate)); assert.equal(request.postDataJSON().expected_mapping_revision, 7); state = candidateState("confirmed"); body = state; if(dropConfirmation){dropConfirmation=false;await route.abort('failed');return;} }
    else if (request.method() === "GET" && target === `/api/v1/library/files/${file}/subtitle-candidates`) body = state;
    else throw Error(`Unexpected API request: ${request.method()} ${target}`);
    await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(body) });
  });
  await page.goto(base); await page.getByText("未启用 · 未设置凭据", { exact: true }).waitFor();
  await page.getByLabel("专用 Webhook 凭据 *", { exact: true }).fill("synthetic-owned-browser-credential-123456");
  await page.getByLabel("启用接收已批准的求片通知").check(); await page.getByRole("button", { name: "保存 Seerr 配置", exact: true }).click();
  await page.getByText("Seerr 配置已保存。", { exact: true }).waitFor(); assert.equal(writes, 1); assert.equal(await page.locator("#seerr-credential").inputValue(), "");
  assert.equal(await page.evaluate(() => JSON.stringify({ ...localStorage, ...sessionStorage }).includes("synthetic-owned-browser")), false);
  await page.getByRole("button", { name: "查询字幕候选", exact: true }).click(); await page.getByText("Shooter 查询失败，可重新查询", { exact: true }).waitFor();
  await page.getByRole("button", { name: "重新查询字幕候选", exact: true }).click(); await page.getByText("Shooter 未找到中文 SRT，可稍后重新查询", { exact: true }).waitFor();
  await page.getByRole("button", { name: "重新查询字幕候选", exact: true }).click(); await page.getByText("请选择要安装的字幕", { exact: true }).waitFor();
  assert.equal(confirmations, 0); assert(await page.getByRole("button", { name: "确认安装所选字幕" }).isDisabled());
  assert.equal(requests.length, 7);
  await page.getByText("120 条字幕 · 字幕时间范围 00:00:00.000 – 01:02:03.004", { exact: true }).waitFor();
  await page.getByText("99999 条字幕 · 字幕时间范围 25:01:01.007 – 99:59:59.999", { exact: true }).waitFor();
  await page.getByText("字幕条数与时间范围不可用", { exact: true }).waitFor();
  assert((await page.locator('.subtitle-candidates-panel').innerText()).includes("不代表视频时长或翻译质量"));
  for (const [name, width] of [["desktop", 1440], ["mobile", 390], ["narrow", 320]]) {
    await page.setViewportSize({ width, height: 1000 });
    assert(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), `${name} overflow`);
    for (const option of await page.locator('.subtitle-candidate-option').all()) {
      assert(await option.evaluate(el => el.scrollWidth <= el.clientWidth), `${name} candidate overflow`);
    }
    if (process.env.PULSE_MP_CAPTURE !== "0") await page.screenshot({ path: path.join(evidence, `panels-${name}.png`), fullPage: true });
  }
  assert.equal(requests.length, 7);
  selectedCandidate = `${candidate}-long`; await page.getByRole("radio").nth(1).check(); assert.equal(requests.length, 7); await page.getByRole("button", { name: "确认安装所选字幕" }).click(); await page.getByText("字幕已安装", { exact: true }).waitFor(); assert.equal(confirmations, 1); assert.equal(queries, 3); assert.deepEqual(errors, []);
  dropSave=true;await page.locator('#seerr-credential').fill('synthetic-second-browser-credential-123456');await page.getByRole('button',{name:'保存 Seerr 配置',exact:true}).click();await page.getByText('保存结果尚未确认，请重新读取配置状态。不会自动重试。',{exact:true}).waitFor();assert.equal(writes,2);assert(await page.getByRole('button',{name:'保存 Seerr 配置',exact:true}).isDisabled());await page.getByRole('button',{name:'重新读取配置与接收记录'}).click();await page.getByText('已启用 · 已设置专用凭据',{exact:true}).waitFor();assert.equal(writes,2);assert.equal(await page.locator('#seerr-credential').inputValue(),'');
  await page.getByRole('button',{name:'重新查询字幕候选',exact:true}).click();selectedCandidate = `${candidate}-missing`;await page.getByRole('radio').nth(2).check();dropConfirmation=true;await page.getByRole('button',{name:'确认安装所选字幕'}).click();await page.getByText('安装结果尚未确认，请读取结果。不会自动重试。',{exact:true}).waitFor();assert.equal(confirmations,2);assert(await page.getByRole('button',{name:'确认安装所选字幕'}).isDisabled());await page.getByRole('button',{name:'读取结果',exact:true}).click();await page.getByText('字幕已安装',{exact:true}).waitFor();assert.equal(confirmations,2);
  await page.locator('#seerr-credential').fill('old-account-unsaved-credential-123456');await page.evaluate(()=>globalThis.ownedAccountChange({id:'01960000-0000-7000-8000-000000000004',role:'member'}));await page.getByRole('heading',{name:'Seerr 求片接入'}).waitFor({state:'detached'});assert.equal(await page.getByRole('heading',{name:'中文字幕'}).count(),0);await page.evaluate(()=>globalThis.ownedAccountChange({id:'01960000-0000-7000-8000-000000000005',role:'admin'}));await page.getByText('已启用 · 已设置专用凭据',{exact:true}).waitFor();assert.equal(await page.locator('#seerr-credential').inputValue(),'');assert.equal(await page.getByRole('radio').count(),0);assert.equal(writes,2);assert.equal(confirmations,2);assert.deepEqual(errors,[]);
  const requestCounts = Object.fromEntries([...new Set(requests)].map(key => [key, requests.filter(value => value === key).length]));
  assert.deepEqual(requestCounts, {
    "GET /api/v1/settings/seerr": 3,
    "GET /api/v1/seerr/receipts": 3,
    [`GET /api/v1/library/files/${file}/subtitle-candidates`]: 3,
    "PUT /api/v1/settings/seerr": 2,
    [`POST /api/v1/library/files/${file}/subtitle-candidates/queries`]: 4,
    [`POST /api/v1/library/files/${file}/subtitle-candidates/${candidate}-long/confirm`]: 1,
    [`POST /api/v1/library/files/${file}/subtitle-candidates/${candidate}-missing/confirm`]: 1,
  });
  console.log(JSON.stringify({ candidateSummaries: true, zeroStart: true, maxSRTTimestamp: true, missingSummary: true, narrow: 320, exactAPIRequests: requests.length, passed: true, realChrome: true, controlledAPI: true, desktop: 1440, mobile: 390, seerrDefaultDisabled: true, noSecretPersistence: true, retryDistinctOutcomes: true, explicitConfirmation: true,unknownNoAutoRetry:true,accountChangeClearsDrafts:true }));
  await context.close();
} finally { if (browser) await browser.close(); if (server) await server.close(); await rm(owned, { recursive: true, force: true }); }
