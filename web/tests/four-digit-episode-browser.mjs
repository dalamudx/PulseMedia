import assert from 'node:assert/strict';
import {chromium} from 'playwright-core';

const base = process.env.PULSE_REVIEW_UI_BASE;
const id = process.env.PULSE_REVIEW_UI_SUB;
const probe = process.env.PULSE_REVIEW_UI_PROBE;
if (!base || !id || !probe) throw Error('disposable real-data fixture required');
const browser = await chromium.launch({executablePath: '/usr/bin/google-chrome', headless: true, args: ['--no-sandbox', '--disable-dev-shm-usage', '--disable-background-networking', '--disable-gpu']});
try {
 const page = await browser.newPage();
 page.setDefaultTimeout(12000);
 const errors = [];
 let writes = 0;
 page.on('pageerror', error => errors.push(error.message));
 page.on('request', request => {
  if (request.method() === 'POST' && /\/(episode-candidate-reviews|episode-acquisitions)(?:\?|$)/.test(request.url())) writes++;
 });
 await page.goto(base);
 assert.equal(await page.evaluate(async () => (await fetch('/api/v1/auth/login', {method: 'POST', headers: {'Content-Type': 'application/json'}, body: JSON.stringify({username: 'admin', password: 'AdminPass12345!'})})).status), 200);
 await page.goto(`${base}/subscriptions`);
 await page.getByRole('button', {name: '查看Mapping Browser Show订阅详情', exact: true}).click();
 await page.getByRole('button', {name: '查看编号映射', exact: true}).click();
 const panel = page.getByRole('region', {name: '真实资源单集审核'});
 await panel.getByRole('textbox', {name: '单集资源关键词'}).fill('Mapping Browser Show');
 const search = page.waitForResponse(response => response.url().includes('/api/v1/releases/search'));
 await panel.getByRole('button', {name: '检索真实站点资源'}).click();
 assert.equal((await search).status(), 200);
 const select = panel.getByRole('combobox', {name: '待审核站点资源'});
 await select.locator('option').nth(1).waitFor({state: 'attached'});
 await select.selectOption(await select.locator('option').nth(1).getAttribute('value'));
 await panel.getByRole('button', {name: '读取当前候选证据'}).click();
 await panel.getByText(/S01E01 · 单元版本/).waitFor();
 const acknowledgement = panel.getByRole('checkbox', {name: '已核对当前资源确属此作品及单集，确认只保存审核，不创建任务'});
 assert.equal(await acknowledgement.isDisabled(), false);
 await acknowledgement.check();
 assert.equal(await page.evaluate(async endpoint => (await fetch(endpoint, {method: 'POST'})).status, probe), 204);
 const current = page.waitForResponse(response => response.url().includes('/episode-mapping/explain-release'));
 await panel.getByRole('button', {name: '读取当前候选证据'}).click();
 const response = await current;
 assert.equal(response.status(), 200);
 const evidence = await response.json();
 assert.equal(evidence.reason, 'canonical_unit_unknown');
 assert.equal(evidence.coverage_known, false);
 assert.equal(evidence.execution_allowed, false);
 assert.deepEqual(evidence.rows, []);
 await page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))));
 assert.equal(await acknowledgement.isDisabled(), true);
 assert.equal(await panel.getByRole('button', {name: '保存当前单集审核'}).isDisabled(), true);
 assert.equal(await panel.getByText(/S01E01 · 单元版本/).count(), 0);
 assert.equal(await page.evaluate(id => sessionStorage.getItem(`pulse:episode-review:${id}`), id), null);
 assert.equal(await page.evaluate(id => sessionStorage.getItem(`pulse:episode-acquisition:${id}`), id), null);
 assert.equal(writes, 0);
 assert.deepEqual(errors, []);
 console.log(JSON.stringify({passed: true, realHTTPSourceAndPG: true, highExplicitUnitNotInferred: true, oldUnitCleared: true, reviewDisabled: true, noReviewOrAcquisitionCommand: true, fixtureDownloaderOnly: true}));
} finally {
 await browser.close();
}
