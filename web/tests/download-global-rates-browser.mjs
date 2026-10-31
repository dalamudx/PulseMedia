// Real Chrome + controlled HTTP; no PostgreSQL or live downloader daemon.
// Standalone acceptance: Main registers this in central CI.
import assert from 'node:assert/strict';
import { mkdtemp, writeFile, rm, realpath, stat } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { createServer } from 'vite';
import react from '@vitejs/plugin-react';
import { chromium } from 'playwright-core';

const supplied = process.env.PULSE_TEST_TEMP_ROOT;
assert(supplied && path.isAbsolute(supplied), 'Supply PULSE_TEST_TEMP_ROOT beneath /data/temp');
const root = await realpath(supplied);
assert(root.startsWith('/data/temp/') && (await stat(root)).isDirectory());
const executablePath = process.env.PULSE_TEST_BROWSER_EXECUTABLE || process.env.CHROME_BIN;
assert(executablePath, 'Supply PULSE_TEST_BROWSER_EXECUTABLE or CHROME_BIN');
for (const key of ['TMPDIR', 'TMP', 'TEMP']) process.env[key] = root;
const owned = await mkdtemp(path.join(root, 'download-global-rates-'));
const web = fileURLToPath(new URL('../', import.meta.url));
const endpoint = '/api/v1/download-clients/global-rates';
const report = { fixture: 'controlled HTTP; production panel and hook; no PG or live rTorrent', checks: [], widths: [], passed: false };
const requests = [], pageErrors = [], unexpected = [];
let server, context;
let mode = 'mixed';
function snapshot() {
  const now = Date.now(), observed_at = new Date(now).toISOString();
  const clients = ['qbittorrent', 'transmission', 'rtorrent'].map((client_type, index) => ({
    id: `00000000-0000-4000-8000-${String(index + 1).padStart(12, '0')}`,
    name: ['Q source', 'T source', 'R source'][index], client_type, state: 'observed', failure_code: '', observed_at,
    download_bytes_per_second: String((index + 1) * 1024), upload_bytes_per_second: String((index + 1) * 512),
  }));
  if (mode === 'unknown') {
    Object.assign(clients[2], { state: 'unknown', observed_at: null, download_bytes_per_second: null, upload_bytes_per_second: null,
      last_good: { observed_at: new Date(now - 60000).toISOString(), download_bytes_per_second: '3072', upload_bytes_per_second: '1536' } });
  }
  if (mode === 'expired') clients[2].observed_at = new Date(now - 46000).toISOString();
  const rates = { download_bytes_per_second: '6144', upload_bytes_per_second: '3072', download_known_clients: 3, upload_known_clients: 3 };
  const point = (offset, known) => ({ at: new Date(now + offset).toISOString(), ...rates,
    ...(known ? {} : { download_bytes_per_second: null, upload_bytes_per_second: null, download_known_clients: 2, upload_known_clients: 2 }) });
  // Non-null totals in partial responses must still be masked by current client evidence.
  return { enabled: true, observed_at, freshness_seconds: 45, ...rates, clients,
    series: [point(-20000, true), point(-10000, false), point(0, true)] };
}
try {
  await writeFile(path.join(owned, 'index.html'), '<!doctype html><html lang="zh-CN"><meta charset="UTF-8"><meta name="viewport" content="width=device-width,initial-scale=1"><div id="root"></div><script type="module" src="/fixture.tsx"></script></html>');
  await writeFile(path.join(owned, 'fixture.tsx'), `import React from 'react';import {createRoot} from 'react-dom/client';
import {MemoryRouter} from 'react-router-dom';
import {DownloadGlobalRatesPanel} from '@/features/dashboard/DownloadGlobalRatesPanel';
import '@/assets/main.css';import '@/assets/workspace.css';import '@/assets/redesign.css';
createRoot(document.getElementById('root')).render(<MemoryRouter><main style={{padding:16,minWidth:0}}><DownloadGlobalRatesPanel userId="00000000-0000-4000-8000-000000000001" role="admin"/></main></MemoryRouter>);`);
  server = await createServer({ configFile: false, root: owned, cacheDir: path.join(owned, 'vite-cache'), plugins: [react()],
    resolve: { alias: [
      { find: '@', replacement: path.join(web, 'src') },
      { find: /^react$/, replacement: path.join(web, 'node_modules/react') },
      { find: /^react\//, replacement: path.join(web, 'node_modules/react') + '/' },
      { find: /^react-dom\//, replacement: path.join(web, 'node_modules/react-dom') + '/' },
      { find: 'react-router-dom', replacement: path.join(web, 'node_modules/react-router-dom') },
    ] }, server: { host: '127.0.0.1', port: 0, fs: { allow: [owned, web] } }, logLevel: 'error' });
  await server.listen();
  const origin = `http://127.0.0.1:${server.httpServer.address().port}`;
  context = await chromium.launchPersistentContext(path.join(owned, 'chrome-profile'), {
    executablePath, headless: true, viewport: { width: 1440, height: 1000 },
    args: ['--no-sandbox', '--disable-dev-shm-usage', '--disable-background-networking'],
    env: { ...process.env, XDG_CACHE_HOME: path.join(owned, 'chrome-cache') },
  });
  report.chrome = context.browser().version();
  const page = context.pages()[0];
  page.setDefaultTimeout(10000);
  page.on('pageerror', error => pageErrors.push(error.message));
  await page.route('**/api/v1/**', async route => {
    const request = route.request(), url = new URL(request.url());
    requests.push({ method: request.method(), path: url.pathname, search: url.search });
    if (url.pathname !== endpoint || url.search || request.method() !== 'GET') {
      unexpected.push(request.url());
      return route.fulfill({ status: 500, body: '{}' });
    }
    return route.fulfill({ contentType: 'application/json', headers: { 'Cache-Control': 'no-store' }, body: JSON.stringify(snapshot()) });
  });
  await page.goto(origin);
  const panel = page.getByRole('region', { name: '全局传输速率', exact: true });
  const values = panel.locator('.global-rate-value');
  const rows = panel.locator('.global-rate-clients > li');
  const pass = name => report.checks.push(name);
  async function expectValues(expected) {
    await page.waitForFunction(expected => JSON.stringify([...document.querySelectorAll('.global-rate-value')].map(el => el.textContent)) === JSON.stringify(expected), expected);
    assert.deepEqual(await values.allTextContents(), expected);
    assert.equal(await panel.getByRole('alert').count(), 0, 'valid mixed response must not blank the panel');
  }
  async function refresh() {
    const response = page.waitForResponse(r => new URL(r.url()).pathname === endpoint);
    await panel.getByRole('button', { name: '刷新速率', exact: true }).click();
    await (await response).finished();
    await page.waitForFunction(() => document.querySelector('[aria-label="全局传输速率"]')?.getAttribute('aria-busy') === 'false');
  }
  await expectValues(['6.0 KiB/s', '3.0 KiB/s']);
  await panel.locator('summary').first().click();
  assert.equal(await rows.count(), 3);
  for (const [index, label] of ['qBittorrent', 'Transmission', 'rTorrent'].entries()) {
    assert.equal(await rows.nth(index).locator('strong').innerText(), ['Q source', 'T source', 'R source'][index]);
    await rows.nth(index).getByText(`${label} · 已有观测`, { exact: true }).waitFor();
    await rows.nth(index).getByText(`下载 ${index + 1}.0 KiB/s · 上传 ${['512 B/s', '1.0 KiB/s', '1.5 KiB/s'][index]}`, { exact: true }).waitFor();
  }
  assert.deepEqual(await panel.locator('.global-rate-chart .facts-note').allTextContents(), ['有效观测：3/3 个下载器', '有效观测：3/3 个下载器']);
  // Each isolated historical point stays separate across the unknown interval.
  assert.equal(await panel.locator('.global-rate-chart circle').count(), 4);
  assert.equal(await panel.locator('.global-rate-chart path:not(.global-rate-baseline)').count(), 0);
  await panel.locator('summary').nth(1).click();
  assert.equal(await panel.locator('tbody tr').count(), 3);
  assert.deepEqual(await panel.locator('tbody tr').nth(1).locator('td').allTextContents(), ['未知', '未知']);
  pass('mixed all3: correct labels, individual rates, aggregate and history gaps; no panel rejection');
  for (const state of ['mixed', 'unknown', 'expired']) {
    mode = state;
    await refresh();
    await expectValues(state === 'mixed' ? ['6.0 KiB/s', '3.0 KiB/s'] : ['未知', '未知']);
    if (state !== 'mixed') {
      await rows.nth(2).getByText('下载 未知 · 上传 未知', { exact: true }).waitFor();
      await rows.nth(2).getByText(`rTorrent · ${state === 'unknown' ? '观测未知' : '当前速率未知'}`, { exact: true }).waitFor();
      assert.deepEqual(await panel.locator('.global-rate-chart .facts-note').allTextContents(), ['有效观测：2/3 个下载器', '有效观测：2/3 个下载器']);
      await rows.nth(0).getByText('下载 1.0 KiB/s · 上传 512 B/s', { exact: true }).waitFor();
      if (state === 'unknown') await rows.nth(2).getByText(/最后成功：.*下载 3.0 KiB\/s · 上传 1.5 KiB\/s/).waitFor();
    }
    for (const width of [320, 390, 1440]) {
      await page.setViewportSize({ width, height: 1000 });
      const geometry = await panel.evaluate(el => ({
        documentWidth: document.documentElement.scrollWidth, panelWidth: el.scrollWidth, clientWidth: el.clientWidth,
        controls: [...el.querySelectorAll('button,summary,a[href]')].filter(el => el.getClientRects().length).map(el => {
          const box = el.getBoundingClientRect();
          return { text: el.textContent.trim(), width: box.width, height: box.height };
        }),
      }));
      report.widths.push({ state, width, ...geometry });
      assert(geometry.documentWidth <= width && geometry.panelWidth <= geometry.clientWidth + 1, `${state}/${width}: horizontal overflow`);
      assert(geometry.controls.length >= 3, 'measure refresh and both actionable summaries');
      assert(geometry.controls.every(control => control.width >= 44 && control.height >= 44), `${state}/${width}: control below 44px`);
    }
    pass(`${state}: rates and 320/390/1440 overflow/44px controls`);
  }
  assert.deepEqual(pageErrors, []);
  assert.deepEqual(unexpected, []);
  assert(requests.length >= 4 && requests.every(request => request.method === 'GET'));
  report.passed = true;
} catch (error) {
  report.error = String(error);
  throw error;
} finally {
  // Attempt every cleanup even if a browser/server close fails.
  const cleanup = await Promise.allSettled([
    context?.close(),
    (async () => { if (server) { server.httpServer?.closeAllConnections(); await server.close(); } })(),
  ]);
  await rm(owned, { recursive: true, force: true });
  report.cleanedFixture = owned;
  report.requests = requests;
  report.pageErrors = pageErrors;
  report.cleanupErrors = cleanup.filter(result => result.status === 'rejected').map(result => String(result.reason));
  report.passed &&= report.cleanupErrors.length === 0;
  await writeFile(path.join(root, 'download-global-rates-receipt.json'), JSON.stringify(report, null, 2) + '\n');
  console.log(JSON.stringify(report));
  assert.deepEqual(report.cleanupErrors, [], 'browser/fixture cleanup failed');
}
