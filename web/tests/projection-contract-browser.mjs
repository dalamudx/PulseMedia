import assert from 'node:assert/strict';
import { chromium } from 'playwright-core';
const base = process.env.PULSE_CONTRACT_UI_BASE;
const sub = process.env.PULSE_CONTRACT_UI_SUB;
const probe = process.env.PULSE_CONTRACT_UI_PROBE;
if (process.env.PULSE_UI_FIXTURE !== '1' || !base || !sub || !probe) throw Error('Explicit isolated fixture required');
const browser = await chromium.launch({ executablePath: '/usr/bin/google-chrome', headless: true, args: ['--no-sandbox', '--disable-dev-shm-usage', '--disable-background-networking', '--disable-gpu'] });
try {
 const page = await browser.newPage();
 page.setDefaultTimeout(12000);
 const errors=[]; page.on('pageerror', e=>errors.push(e.message));
 await page.goto(base);
 assert.equal(await page.evaluate(async()=> (await fetch('/api/v1/auth/login',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({username:'admin',password:'AdminPass12345!'})})).status),200);
 async function list(count) {
  await page.goto(`${base}/subscriptions`);
  await page.getByRole('button',{name:'查看Mapping Browser Show订阅详情',exact:true}).waitFor();
  await page.locator('.caption-progress-tag').filter({hasText:`投影 ${count}/2 集`}).waitFor();
  const detail=await page.evaluate(async id=>(await (await fetch(`/api/v1/subscriptions/${id}`)).json()),sub);
  assert.equal(detail.wanted_items.filter(x=>x.is_satisfied).length,count);
  assert.equal(detail.wanted_items.find(x=>x.unit_key==='S01E02').is_satisfied,false);
 }
 async function showActual() {
  await page.getByRole('button',{name:'查看Mapping Browser Show订阅详情',exact:true}).click();
  await page.getByRole('dialog').getByText('当前目标: 本次确认满足 1 / 2 集',{exact:true}).waitFor();
  const tiles=page.locator('.episode-tile-card');
  await tiles.filter({hasText:'S01E01'}).getByText('已入库 (imported)',{exact:true}).waitFor();
  await tiles.filter({hasText:'S01E02'}).getByText('文件质量或入库证据未确认',{exact:true}).first().waitFor();
  assert.equal(await page.locator('.episode-tile-card.tile-satisfied').count(),1);
 }
 async function mutate(action) {
  assert.equal(await page.evaluate(async ({probe,action})=>(await fetch(probe,{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({action})})).status,{probe,action}),204);
 }
 await list(1); await showActual();
 await mutate('legacy-write');
 await list(0);
 // Source facts did not change: authoritative detail still confirms the real
 // versioned file, even though the old cached projection now has no authority.
 await showActual();
 const statusPath=`/api/v1/subscriptions/${sub}/status`;
 await page.route(`**${statusPath}`,route=>route.fulfill({status:503,contentType:'application/json',body:'{"code":"UNAVAILABLE"}'}));
 await page.reload();
 await page.getByRole('button',{name:'查看Mapping Browser Show订阅详情',exact:true}).click();
 await page.getByRole('dialog').getByText('当前目标: 本次确认满足 0 / 2 集',{exact:true}).waitFor();
 assert.equal(await page.locator('.episode-tile-card.tile-satisfied').count(),0);
 await page.unroute(`**${statusPath}`);
 await list(0); await showActual();
 await mutate('recompute');
 await list(1); await showActual();
 await page.setViewportSize({width:390,height:844});
 await page.waitForFunction(()=>document.documentElement.scrollWidth<=innerWidth);
 assert.deepEqual(errors,[]);
 console.log('PASS: actual PG projection 1/2 -> old-write cache 0/2 -> reproject 1/2; authoritative detail retains only valid fact, old quality unknown, status503 clears green, no new work or fact re-signing');
} finally { await browser.close(); }
