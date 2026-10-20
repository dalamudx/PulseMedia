import assert from 'node:assert/strict';
import {createRequire} from 'node:module';
import {stat} from 'node:fs/promises';
const load = createRequire(process.env.PULSE_M7_LIVE_LOGS_REPO + '/web/package.json');
const {chromium} = load('playwright-core');

const base = process.env.PULSE_M7_LIVE_LOGS_BASE;
const shots = process.env.PULSE_M7_LIVE_LOGS_SCREENSHOTS;
const session = process.env.PULSE_M7_LIVE_LOGS_SESSION;
const marker = process.env.PULSE_M7_LIVE_LOGS_MARKER;
const forbidden = process.env.PULSE_M7_LIVE_LOGS_SECRET;

const browser = await chromium.launch({executablePath: process.env.PULSE_M7_LIVE_LOGS_CHROME, args: ['--no-sandbox']});
const context = await browser.newContext({viewport: {width: 1280, height: 1000}});
await context.addCookies([{name: 'pulsemedia_session', value: session, url: base}]);
const page = await context.newPage();
const errors = [];
page.on('pageerror', error => errors.push(String(error)));
const requests = [];
page.on('request', request => requests.push({method: request.method(), url: request.url()}));

async function shot(name, width) {
  await page.setViewportSize({width, height: width === 375 ? 812 : 1000});
  if (width === 375) {
    const backdrop = page.locator('.mobile-drawer-backdrop');
    if (await backdrop.isVisible()) await backdrop.click({position: {x: 320, y: 160}});
    await page.waitForFunction(() => document.querySelector('.sidebar').getBoundingClientRect().right <= 1);
  }
  assert(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1), 'no horizontal overflow at ' + width);
  const path = shots + '/' + name + '-' + width + '.png';
  await page.screenshot({path, fullPage: true, animations: 'disabled'});
  assert((await stat(path)).size > 1000);
}

try {
  await page.goto(base + '/settings');
  await page.getByRole('button', {name: '实时日志流'}).click();

  const panel = page.getByRole('log', {name: '实时系统日志'});
  await panel.waitFor();
  await page.getByText('实时推送中', {exact: true}).waitFor({timeout: 20000});

  // The Go fixture published one WARN marker before this browser connected; it
  // must be replayed, already redacted.
  const markerLine = panel.locator('.log-line', {hasText: marker});
  await markerLine.first().waitFor({timeout: 20000});
  const text = await markerLine.first().innerText();
  assert(text.includes('WARN'), 'level badge missing: ' + text);
  assert(!text.includes(forbidden), 'credential leaked into the viewer: ' + text);
  assert(text.includes('api_key=***'), 'credential query value not masked: ' + text);
  assert(text.includes('user:***@'), 'userinfo value not masked: ' + text);

  const clipped = await page.evaluate(() => {
    const box = document.querySelector('.terminal-box');
    return box ? box.scrollWidth - box.clientWidth : 0;
  });
  assert(clipped <= 1, 'log lines are clipped horizontally by ' + clipped + 'px');

  await shot('live-logs', 1280);
  await shot('live-logs', 375);
  await page.setViewportSize({width: 1280, height: 1000});

  // Level filter re-filters the current buffer and reconnects the stream.
  const level = page.getByLabel('等级过滤');
  await level.selectOption('ERROR');
  await page.waitForFunction(
    marker => ![...document.querySelectorAll('.log-line')].some(line => line.textContent.includes(marker)),
    marker,
  );
  const streamed = requests.filter(r => new URL(r.url).searchParams.get('level') === 'ERROR');
  assert(streamed.length >= 1, 'level change did not reopen the stream');

  // Pause keeps the connection open but stops appending.
  await page.getByRole('button', {name: '暂停滚动'}).click();
  await page.getByText('已暂停自动滚动，新日志仍会到达但暂不追加显示。', {exact: true}).waitFor();
  const frozen = await panel.locator('.log-line').count();
  await page.waitForTimeout(300);
  assert.equal(await panel.locator('.log-line').count(), frozen, 'paused viewer appended lines');

  // Clear empties the viewer without touching the server.
  await page.getByRole('button', {name: '清空屏幕'}).click();
  await page.waitForFunction(() => document.querySelectorAll('.log-line').length === 0);

  await shot('live-logs-empty', 375);
  const writes = requests.filter(r => r.method !== 'GET');
  assert.equal(writes.length, 0, 'log viewing performed a write: ' + JSON.stringify(writes));
  assert.deepEqual(errors, []);
  console.log('M7_LIVE_LOGS_BROWSER_PASS ' + JSON.stringify({
    actualChrome: true, realCookieAppPG: true, redacted: true, levelFilter: true, paused: true, cleared: true, writes: 0,
  }));
} finally {
  await context.close();
  await browser.close();
}
