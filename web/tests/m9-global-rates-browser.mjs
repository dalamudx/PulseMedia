import assert from 'node:assert/strict';
import { chromium } from 'playwright-core';
import { join } from 'node:path';
import { mkdirSync } from 'node:fs';

const env = process.env, base = env.PULSE_M9_RATE_BASE, shots = env.PULSE_M9_RATE_SCREENSHOTS;
assert(base && env.PULSE_M9_RATE_SESSION && env.PULSE_M9_RATE_MEMBER_SESSION && env.PULSE_M9_RATE_CDP && shots?.startsWith('/data/temp/'), 'owned native browser fixture required');
assert.notEqual(new URL(env.PULSE_M9_RATE_CDP).port, '9222');
mkdirSync(shots, { recursive: true });
const browser = await chromium.connectOverCDP(env.PULSE_M9_RATE_CDP);
const context = await browser.newContext({ viewport: { width: 1440, height: 960 }, colorScheme: 'dark', reducedMotion: 'reduce', serviceWorkers: 'block' });
let checks = 0;
const check = (value, message) => { assert(value, message); checks++; };
try {
  await context.addInitScript(() => {
    const native = window.fetch.bind(window);
    const transport = { rule: null, pending: [], receipts: [], release() { const item = this.pending.shift(); if (!item) throw Error('no held rate response'); item.release(); } };
    window.__m9RatesTransport = transport;
    window.fetch = async (input, init) => {
      const url = new URL(typeof input === 'string' ? input : input.url, location.href);
      if (url.pathname !== '/api/v1/download-clients/global-rates') return native(input, init);
      const rule = transport.rule; transport.rule = null;
      const signal = new AbortController().signal;
      const request = rule?.ignoreAbort && input instanceof Request ? new Request(input, { signal }) : input;
      const response = await native(request, rule?.ignoreAbort ? { ...init, signal } : init);
      transport.receipts.push({ status: response.status, data: await response.clone().json() });
      if (rule?.hold) await new Promise(release => transport.pending.push({ release }));
      if (rule?.status) return new Response(JSON.stringify({ code: 'UNAVAILABLE', message: 'owned transport fault' }), { status: rule.status, headers: { 'Content-Type': 'application/problem+json' } });
      return response;
    };
  });
  const external = [], writes = [], errors = [];
  await context.route('**/*', route => { const url = new URL(route.request().url()); if (url.origin === base) return route.continue(); external.push(url.origin); return route.abort(); });
  const cookie = value => context.addCookies([{ name: 'pulsemedia_session', value, url: base, httpOnly: true, sameSite: 'Lax' }]);
  await cookie(env.PULSE_M9_RATE_SESSION);
  const page = await context.newPage(); page.setDefaultTimeout(15000);
  // Browser-only clock advancement tests local expiry; database time remains real.
  await page.clock.install();
  page.on('pageerror', error => errors.push(error.message));
  page.on('request', request => { const url = new URL(request.url()); if (url.origin === base && url.pathname.startsWith('/api/') && request.method() !== 'GET') writes.push(url.pathname); });
  await page.goto(base, { waitUntil: 'domcontentloaded' });
  const panel = () => page.getByRole('region', { name: '全局传输速率', exact: true });
  const refresh = () => panel().getByRole('button', { name: '刷新速率', exact: true });
  const ready = async () => { await panel().locator('.global-rate-download .global-rate-value').filter({ hasText: '576.0 KiB/s' }).waitFor(); await refresh().waitFor(); await page.waitForFunction(() => !document.querySelector('.global-rate-refresh')?.disabled); };
  const counters = async () => (await context.request.get(base + '/__m9_rates/counters')).json();
  const control = async mode => { const result = await context.request.post(base + '/__m9_rates/' + mode); assert.equal(result.status(), 204, 'owned native transition failed'); };
  const arm = rule => page.evaluate(rule => { window.__m9RatesTransport.rule = rule; }, rule);
  const capture = async name => {
    const heading = panel().getByRole('heading', { name: '全局传输速率', exact: true });
    await heading.evaluate(node => node.scrollIntoView({ block: 'center', behavior: 'instant' }));
    const headingBox = await heading.boundingBox(), headerBox = await page.locator('header.topbar').boundingBox();
    check(headingBox && headingBox.y >= (headerBox ? headerBox.y + headerBox.height : 0), 'card heading is obscured by the fixed header');
    // The independent component image excludes the fixed page chrome only
    // during capture; all layout and visibility assertions use the real page.
    await panel().screenshot({ path: join(shots, name), style: 'header.topbar { visibility: hidden !important; }' });
  };
  check(await panel().locator('svg path:not(.global-rate-baseline)').count() >= 2, 'real separated native samples did not form direction waveforms');
  await ready();
  check(await panel().getByRole('img').count() === 2, 'both named direction curves must render');
  check(await panel().getByText('128.0 KiB/s', { exact: true }).count() >= 1, 'global upload must come from native statistics');
  await panel().getByText('下载器观测（2）', { exact: true }).click();
  check((await panel().innerText()).includes('上传 0 B/s'), 'known idle upload was changed to unknown');
  check((await panel().innerText()).includes('最后成功'), 'historical observation provenance missing');
  await panel().getByText('历史观测数据', { exact: true }).click();
  check(await panel().getByRole('table').isVisible(), 'keyboard-readable history missing');
  await panel().getByText('历史观测数据', { exact: true }).click();
  const before = await counters();
  await refresh().click(); await ready();
  check(JSON.stringify(await counters()) === JSON.stringify(before), 'browser refresh made a native call');
  for (const width of [1440, 768, 375, 320]) {
    await page.setViewportSize({ width, height: 960 });
    await page.evaluate(() => Promise.all(document.getAnimations().filter(a => a.effect?.getTiming().iterations !== Infinity).map(a => a.finished.catch(() => {}))));
    const bounds = await panel().evaluate(node => ({ width: innerWidth, page: document.documentElement.scrollWidth, elements: [...node.querySelectorAll('h2,h3,strong,p,svg,summary,button')].map(el => { const box = el.getBoundingClientRect(); return { tag: el.tagName, left: box.left, right: box.right, width: box.width, height: box.height }; }) }));
    check(bounds.page <= width + 1, `page overflows at ${width}`);
    check(bounds.elements.every(el => el.width === 0 || (el.left >= -1 && el.right <= width + 1)), `rate content overflows at ${width}: ${JSON.stringify(bounds.elements.filter(el => el.left < -1 || el.right > width + 1))}`);
    check(bounds.elements.filter(el => el.tag === 'BUTTON' || el.tag === 'SUMMARY').every(el => el.height >= 44), `touch targets below 44px at ${width}`);
    await capture(`global-rates-${width}.png`);
  }
  await control('failure'); await refresh().click();
  await page.waitForFunction(() => [...document.querySelectorAll('.global-rate-value')].every(el => el.textContent === '未知'));
  check(await panel().getByText('最近采集失败', { exact: false }).count() === 2, 'native failure state absent');
  check((await panel().innerText()).includes('最后成功'), 'native failure erased last-good history');
  await capture('global-rates-failure.png');
  await control('success'); await refresh().click(); await ready();
  await arm({ hold: true, ignoreAbort: true });
  await page.clock.fastForward(46000);
  await page.waitForFunction(() => window.__m9RatesTransport.pending.length === 1);
  await page.waitForFunction(() => [...document.querySelectorAll('.global-rate-value')].every(el => el.textContent === '未知'));
  check((await panel().innerText()).includes('当前速率未知'), 'local expiry retained a live claim');
  await capture('global-rates-expired.png');
  // Release the in-flight read; recovery uses its actual authenticated response.
  await page.evaluate(() => window.__m9RatesTransport.release()); await ready();
  await arm({ status: 503 }); await refresh().click(); await panel().getByRole('alert').waitFor();
  check(await panel().getByRole('img').count() === 0, 'read failure retained live curves');
  await refresh().click(); await ready();
  await arm({ hold: true, ignoreAbort: true }); await refresh().click(); await page.waitForFunction(() => window.__m9RatesTransport.pending.length === 1);
  await cookie(env.PULSE_M9_RATE_MEMBER_SESSION); await page.evaluate(() => window.__m9RatesAuth.checkStatus()); await panel().waitFor({ state: 'detached' });
  await page.evaluate(() => window.__m9RatesTransport.release());
  check(await panel().count() === 0, 'late admin response crossed accounts');
  const memberReads = await page.evaluate(() => window.__m9RatesTransport.receipts.length);
  await page.clock.fastForward(10000);
  check(await page.evaluate(() => window.__m9RatesTransport.receipts.length) === memberReads, 'member polled administrator-only rates');
  await cookie(env.PULSE_M9_RATE_SESSION); await page.evaluate(() => window.__m9RatesAuth.checkStatus()); await ready();
  const after = await counters();
  check(after.reads === 8 && after.unexpected === 0, 'page made extra native calls or a mutating method');
  check(writes.length === 0, 'homepage issued an API write'); check(errors.length === 0, `page errors: ${errors.join(';')}`); check(external.length === 0, 'unexpected external browsing request');
  console.log(`M9_GLOBAL_RATE_CHROME_PASS checks=${checks} viewports=1440_768_375_320 source=REAL_NATIVE_GETS business_writes=0 failure=REAL_NATIVE local_expiry=BROWSER_CLOCK identity=REAL_COOKIES snapshots=GO_FULL_TABLES`);
} finally { await context.close(); await browser.close(); }