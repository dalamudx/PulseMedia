import assert from 'node:assert/strict';
import { chromium } from 'playwright-core';
import { join, isAbsolute, resolve } from 'node:path';
import { mkdirSync, realpathSync } from 'node:fs';
import { setTimeout as delay } from 'node:timers/promises';

const env = process.env;
assert(env.PULSE_M9_REQUEST_SETUP && env.PULSE_M9_REQUEST_CDP, 'owned fixture environment required');
const setup = JSON.parse(env.PULSE_M9_REQUEST_SETUP);
const base = setup.base, shots = env.PULSE_M9_REQUEST_SCREENSHOTS;
assert(shots && isAbsolute(shots) && resolve(shots).startsWith('/data/temp/'), 'owned screenshot path required');
assert.equal(realpathSync(shots), resolve(shots), 'screenshot symlinks forbidden');
assert.equal(new URL(base).hostname, '127.0.0.1');
assert.equal(new URL(env.PULSE_M9_REQUEST_CDP).hostname, '127.0.0.1');
assert.notEqual(new URL(env.PULSE_M9_REQUEST_CDP).port, '9222', 'global browser forbidden');
mkdirSync(shots, { recursive: true });
const browser = await chromium.connectOverCDP(env.PULSE_M9_REQUEST_CDP);
const contexts = [], external = [], errors = [], writes = [], lost = [];
const optionPaths = new Set(['/api/v1/library-roots', '/api/v1/indexers', '/api/v1/download-clients', '/api/v1/policies']);
const memberOptionReads = [];
let checks = 0;
const check = (value, message) => { assert(value, message); checks++; };
const exactKeys = (body, names) => check(JSON.stringify(Object.keys(body).sort()) === JSON.stringify([...names].sort()), 'command JSON has unexpected fields');
const poll = async (predicate, message) => {
  const deadline = Date.now() + 15000;
  while (Date.now() < deadline) { if (await predicate()) return; await delay(50); }
  throw Error(message);
};
const root = page => page.locator('[aria-labelledby="requests-title"]');
const row = (page, id) => root(page).locator(`[data-request-id="${id}"]`);
const createForm = page => root(page).locator('form[aria-label="提交媒体请求"]');
const receiptButton = page => root(page).getByRole('button', { name: '读取原回执', exact: true });
const commandPanel = page => root(page).locator('[aria-labelledby="requests-command-title"]');
const journal = page => page.evaluate(() => Object.entries(localStorage).filter(([key]) => key.startsWith('pulsemedia.media-requests.v1.')));
const counters = async () => {
  const response = await contexts[0].request.get(base + '/__m9_requests/counters');
  assert.equal(response.status(), 200, 'full snapshot/counter guard failed');
  return response.json();
};
const control = async action => {
  const response = await contexts[0].request.post(base + '/__m9_requests/' + action);
  assert.equal(response.status(), 204, 'owned database role transition failed');
};
const newPage = async (session, member) => {
  const context = await browser.newContext({ viewport: { width: 1440, height: 960 }, colorScheme: 'dark', reducedMotion: 'reduce', serviceWorkers: 'block' });
  contexts.push(context);
  await context.addCookies([{ name: 'pulsemedia_session', value: session, url: base, httpOnly: true, sameSite: 'Lax' }]);
  const fault = { lose: null, getStatus: null };
  await context.route('**/*', async route => {
    const request = route.request(), url = new URL(request.url());
    let handled = false;
    try {
      if (url.origin !== base) { external.push(url.origin); handled = true; await route.abort(); return; }
      if (request.method() === 'POST' && fault.lose === url.pathname) {
        fault.lose = null;
        // Send the original authenticated command exactly once. The server commits
        // its actual transaction; only delivery of the successful response is lost.
        const response = await route.fetch({ maxRedirects: 0, maxRetries: 0 });
        lost.push({ path: url.pathname, status: response.status(), key: request.headers()['idempotency-key'], body: await response.json() });
        handled = true;
        await route.abort('failed');
        return;
      }
      if (request.method() === 'GET' && fault.getStatus && /\/commands\/|\/approval-commands\//.test(url.pathname)) {
        const status = fault.getStatus; fault.getStatus = null;
        handled = true;
        await route.fulfill({ status, contentType: 'application/problem+json', body: JSON.stringify({ code: 'UNAVAILABLE', message: 'Owned receipt transport fault' }) });
        return;
      }
      handled = true;
      await route.continue();
    } catch {
      errors.push('owned route handling failed');
      if (!handled) { handled = true; await route.abort(); }
    }
  });
  context.on('request', request => {
    const url = new URL(request.url());
    if (url.origin !== base) return;
    if (member && request.method() === 'GET' && optionPaths.has(url.pathname)) memberOptionReads.push(url.pathname);
    if (url.pathname.startsWith('/api/') && !['GET', 'HEAD', 'OPTIONS'].includes(request.method())) {
      writes.push({ path: url.pathname, method: request.method(), key: request.headers()['idempotency-key'], body: request.postDataJSON() });
    }
  });
  const page = await context.newPage();
  page.setDefaultTimeout(15000);
  page.on('pageerror', () => errors.push('browser page error'));
  return { page, fault, context };
};
const navigate = async (page, query = '') => {
  await page.goto(base + '/requests' + query, { waitUntil: 'domcontentloaded' });
  await root(page).getByRole('heading', { name: '媒体请求', exact: true }).waitFor();
  await page.waitForFunction(() => document.querySelector('[aria-labelledby="requests-list-title"]')?.getAttribute('aria-busy') === 'false');
};
const refresh = async page => {
  await Promise.all([
    page.waitForResponse(response => new URL(response.url()).pathname === '/api/v1/media-requests' && response.request().method() === 'GET'),
    root(page).getByRole('button', { name: '刷新当前页', exact: true }).click(),
  ]);
  await page.waitForFunction(() => document.querySelector('[aria-labelledby="requests-list-title"]')?.getAttribute('aria-busy') === 'false');
};
const confirmed = async page => {
  await commandPanel(page).getByText(/原.*回执已确认/).waitFor();
  check((await journal(page)).length === 0, 'verified receipt must clear the original journal');
};
const recover = async page => {
  await receiptButton(page).click();
  await confirmed(page);
};
const openDetails = async (page, id) => {
  const details = row(page, id).locator('details');
  if (!(await details.evaluate(node => node.open))) await details.locator('summary').click();
};
const capture = async (page, name) => {
  const heading = root(page).getByRole('heading', { name: '媒体请求', exact: true });
  await heading.evaluate(node => node.scrollIntoView({ block: 'center', behavior: 'instant' }));
  const box = await heading.boundingBox(), header = await page.locator('header.topbar').boundingBox();
  check(box && box.y >= (header ? header.y + header.height : 0), 'request title obscured by fixed header');
  await root(page).screenshot({ path: join(shots, name + '.png'), style: 'header.topbar { visibility: hidden !important; }' });
};
const createMovie = async (page, id) => {
  await navigate(page, '?media_type=movie&tmdb_id=' + id);
  await createForm(page).getByRole('button', { name: '提交请求', exact: true }).click();
  await confirmed(page);
  await refresh(page);
  const result = await page.evaluate(async id => {
    const response = await fetch('/api/v1/media-requests?limit=25', { cache: 'no-store' });
    if (response.status !== 200) throw Error('owned request list failed');
    return (await response.json()).items.find(item => item.intent.tmdb_id === id);
  }, id);
  check(result?.state === 'pending' && result.version === 1, 'movie submission must remain pending v1');
  return result.id;
};

try {
  const member = await newPage(setup.sessionA, true);
  const page = member.page;
  const start = await counters();
  await navigate(page, '?media_type=tv&tmdb_id=1399&title=' + encodeURIComponent('发现页作品 · ' + '非常长的显示名称'.repeat(20)));
  check(await createForm(page).getByRole('combobox', { name: /^媒体类型/ }).inputValue() === 'tv', 'discovery handoff did not prefill TV');
  check(await createForm(page).getByLabel('TMDB 编号', { exact: true }).inputValue() === '1399', 'discovery handoff lost TMDB identity');
  await createForm(page).getByLabel('季列表（可选）', { exact: true }).fill('1');
  member.fault.lose = '/api/v1/media-requests';
  await createForm(page).getByRole('button', { name: '提交请求', exact: true }).click();
  await receiptButton(page).waitFor();
  await poll(async () => lost.length === 1 && !(await commandPanel(page).getAttribute('aria-busy') === 'true'), 'lost submit did not settle');
  const originalSubmit = lost[0];
  check(originalSubmit.status === 201 && originalSubmit.body.request.state === 'pending' && originalSubmit.body.request.version === 1, 'lost submission must be a real committed pending-v1 receipt');
  const tvID = originalSubmit.body.request.id;
  const savedSubmit = await journal(page);
  check(savedSubmit.length === 1 && JSON.parse(savedSubmit[0][1]).key === originalSubmit.key, 'lost submit changed the stable key');
  check(await createForm(page).locator('fieldset').evaluate(node => node.disabled), 'unknown result must disable native write fieldset');
  check(await createForm(page).getByRole('button', { name: '提交请求', exact: true }).evaluate(node => node.disabled), 'unknown result must disable submit button');
  const submitted = await counters();
  check(submitted.writes === 1 && submitted.successes === 1 && submitted.metadata === 0, 'submit fetched metadata or replayed');
  for (const table of ['subscription', 'wanted_item']) check(submitted.counts[table] === start.counts[table], 'pending submit created subscription/Wanted');
  await page.reload({ waitUntil: 'domcontentloaded' });
  await receiptButton(page).waitFor();
  await row(page, tvID).waitFor();
  check((await counters()).writes === 1, 'reload automatically resent submission');
  check(JSON.stringify(await journal(page)) === JSON.stringify(savedSubmit), 'reload altered original journal');
  member.fault.getStatus = 503;
  await receiptButton(page).click();
  await commandPanel(page).getByRole('alert').waitFor();
  check(JSON.stringify(await journal(page)) === JSON.stringify(savedSubmit), '503 receipt lookup discarded original key');
  check(await root(page).getByRole('button', { name: '以原键和原内容重试', exact: true }).count() === 0, '503 offered unsafe retry');
  await recover(page);
  await refresh(page);
  check((await counters()).writes === 1 && (await counters()).metadata === 0, 'GET recovery caused a write or metadata call');
  check(await row(page, tvID).getByText('待审核', { exact: true }).count() === 1, 'restored pending request not visible');
  check(await root(page).locator('form[aria-label^="批准请求 "]').count() === 0, 'member sees actual approval controls');
  check(await root(page).getByRole('button', { name: '拒绝请求', exact: true }).count() === 0, 'member sees reject button');
  check(await root(page).locator('nav[aria-label="请求分页"]').count() === 1, 'request pagination landmark missing');
  for (const width of [1440, 768, 375, 320]) {
    await page.setViewportSize({ width, height: 960 });
    const layout = await root(page).evaluate(node => ({ pageWidth: document.documentElement.scrollWidth, elements: [...node.querySelectorAll('h1,h2,h3,p,dd,select,input,textarea,button,summary')].map(el => { const r = el.getBoundingClientRect(); return { width: r.width, left: r.left, right: r.right }; }) }));
    check(layout.pageWidth <= width + 1, `request page overflows at ${width}`);
    check(layout.elements.every(el => !el.width || (el.left >= -1 && el.right <= width + 1)), `request content overflows at ${width}`);
    await capture(page, 'media-requests-' + width);
  }
  await page.setViewportSize({ width: 1440, height: 960 });
  const movieWithdraw = await createMovie(page, 550);
  const movieReject = await createMovie(page, 551);
  check((await counters()).metadata === 0, 'request-only movies fetched upstream metadata');

  const other = await newPage(setup.sessionB, true);
  await navigate(other.page);
  await root(other.page).getByText('此页没有可见请求。', { exact: true }).waitFor();
  const scope = await other.page.evaluate(async id => {
    const response = await fetch('/api/v1/media-requests/' + id, { cache: 'no-store' });
    return { status: response.status, body: await response.text() };
  }, tvID);
  check(scope.status === 403 && !scope.body.includes(tvID), 'member B read member A request');
  check(await root(other.page).locator('[data-request-id]').count() === 0, 'member B list leaked another owner');
  check(await root(other.page).locator('form[aria-label^="批准请求 "]').count() === 0, 'member B sees actual approval form');

  const admin = await newPage(setup.adminSession, false);
  const adminPage = admin.page;
  await navigate(adminPage);
  await row(adminPage, movieWithdraw).waitFor();
  await openDetails(adminPage, movieWithdraw);
  const staleClose = row(adminPage, movieWithdraw).locator(`form[aria-label="关闭请求 ${movieWithdraw}"]`);
  await staleClose.getByLabel('原因（拒绝时必填）', { exact: true }).fill('Stale administrator decision');
  // Leave the administrator's rendered version at v1 while the owner withdraws.
  await openDetails(page, movieWithdraw);
  await row(page, movieWithdraw).getByRole('button', { name: '撤回本人请求', exact: true }).click();
  await confirmed(page);
  await refresh(page);
  check(await row(page, movieWithdraw).getByText('已撤回', { exact: true }).count() === 1, 'owner withdrawal did not become terminal');
  await staleClose.getByRole('button', { name: '拒绝请求', exact: true }).click();
  await root(adminPage).getByRole('button', { name: '读取当前请求', exact: true }).waitFor();
  const conflictWrites = (await counters()).writes;
  await root(adminPage).getByRole('button', { name: '读取当前请求', exact: true }).click();
  await commandPanel(adminPage).getByText(/当前状态：已撤回 · 版本 2/).waitFor();
  await root(adminPage).getByRole('button', { name: '结束冲突（仅本地）', exact: true }).click();
  await commandPanel(adminPage).getByText('本地冲突已结束。请刷新后重新选择操作。', { exact: true }).waitFor();
  check((await counters()).writes === conflictWrites && (await journal(adminPage)).length === 0, 'conflict read/finish sent another POST');
  await refresh(adminPage);
  check(await row(adminPage, movieWithdraw).locator('details').count() === 0, 'terminal request still offers reopening controls');
  await openDetails(adminPage, movieReject);
  const rejectForm = row(adminPage, movieReject).locator(`form[aria-label="关闭请求 ${movieReject}"]`);
  await rejectForm.getByLabel('原因（拒绝时必填）', { exact: true }).fill('Owned explicit administrator rejection');
  await rejectForm.getByRole('button', { name: '拒绝请求', exact: true }).click();
  await confirmed(adminPage);
  await refresh(adminPage);
  check(await row(adminPage, movieReject).getByText('已拒绝', { exact: true }).count() === 1, 'administrator rejection missing');
  check(await row(adminPage, movieReject).locator('details').count() === 0, 'rejected request can reopen');

  await openDetails(adminPage, tvID);
  const approval = row(adminPage, tvID).locator(`form[aria-label="批准请求 ${tvID}"]`);
  await approval.getByRole('combobox', { name: /^在线目标根/ }).selectOption(setup.tvRoot);
  await approval.getByRole('combobox', { name: /^质量策略/ }).selectOption('default');
  await approval.getByRole('combobox', { name: /^下载器/ }).selectOption(setup.downloader);
  check(await approval.getByRole('checkbox').count() === 1, 'expected exactly one real indexer option');
  await approval.getByRole('checkbox').check();
  check(await approval.getByRole('button', { name: '批准并创建订阅', exact: true }).evaluate(node => !node.disabled && !node.matches(':disabled')), 'real approval selections did not enable submit');
  await capture(adminPage, 'media-requests-admin');
  admin.fault.lose = '/api/v1/media-requests/' + tvID + '/approve';
  await approval.getByRole('button', { name: '批准并创建订阅', exact: true }).click();
  await receiptButton(adminPage).waitFor();
  await poll(async () => lost.length === 2 && await commandPanel(adminPage).getAttribute('aria-busy') === 'false', 'lost approval did not settle');
  const approved = lost[1];
  check(approved.status === 201 && approved.body.request_id === tvID && approved.body.subscription.user_id === setup.memberA, 'approval did not atomically create the member subscription');
  check(approved.body.subscription.wanted_items.length === 2, 'approval did not expose two native sparse Wanted items');
  const savedApproval = await journal(adminPage), approvalWrites = (await counters()).writes;
  check(savedApproval.length === 1 && JSON.parse(savedApproval[0][1]).key === approved.key, 'lost approval failed to retain exact key');
  await adminPage.reload({ waitUntil: 'domcontentloaded' });
  await receiptButton(adminPage).waitFor();
  await row(adminPage, tvID).getByText('已批准', { exact: true }).waitFor();
  check((await counters()).writes === approvalWrites, 'reload resent successful approval');
  await capture(adminPage, 'media-requests-recovery');
  await control('demote');
  const actualRole = await adminPage.evaluate(async () => (await (await fetch('/api/v1/auth/status', { cache: 'no-store' })).json()).current_user.role);
  check(actualRole === 'member', 'real cookie status ignored database demotion');
  await receiptButton(adminPage).click();
  await commandPanel(adminPage).getByRole('alert').waitFor();
  check(JSON.stringify(await journal(adminPage)) === JSON.stringify(savedApproval), '403 discarded unknown approval key');
  check(await root(adminPage).getByRole('button', { name: '以原键和原内容重试', exact: true }).count() === 0, '403 offered automatic write recovery');
  await control('restore');
  await adminPage.evaluate(() => window.__m9RequestsAuth.checkStatus());
  await receiptButton(adminPage).waitFor();
  await recover(adminPage);
  await refresh(adminPage);
  check((await row(adminPage, tvID).innerText()).includes('此状态不表示下载成功'), 'approved UI misrepresents download success');
  check((await counters()).writes === approvalWrites && (await counters()).metadata === 2, 'approval recovery wrote or repeated metadata');
  const original = await page.evaluate(async key => {
    const response = await fetch('/api/v1/media-requests/commands/' + key, { cache: 'no-store' });
    return { status: response.status, body: await response.json() };
  }, originalSubmit.key);
  check(original.status === 200 && original.body.request.state === 'pending' && original.body.request.version === 1 && !original.body.request.subscription_id, 'current approval overwrote original pending-v1 submission receipt');
  await refresh(page);
  check(await row(page, tvID).getByText('已批准', { exact: true }).count() === 1, 'member current list did not show approved v2');
  check(await row(page, tvID).getByText('2', { exact: true }).count() >= 1, 'current approved version missing');

  // A failed durable journal write must happen before any HTTP POST. Restore the
  // browser's native method before closing its context; no server fault is used.
  await navigate(page, '?media_type=movie&tmdb_id=999');
  const beforeStorageFailure = (await counters()).writes;
  await page.evaluate(() => {
    window.__m9RestoreStorage = Storage.prototype.setItem;
    Storage.prototype.setItem = function (key, value) {
      if (key.startsWith('pulsemedia.media-requests.v1.')) throw new DOMException('Owned quota fault', 'QuotaExceededError');
      return window.__m9RestoreStorage.call(this, key, value);
    };
  });
  await createForm(page).getByRole('button', { name: '提交请求', exact: true }).click();
  await commandPanel(page).getByRole('alert').waitFor();
  check((await counters()).writes === beforeStorageFailure, 'journal storage failure sent a POST');
  check(await createForm(page).locator('fieldset').evaluate(node => node.disabled), 'journal storage failure did not lock new writes');
  await page.evaluate(() => { Storage.prototype.setItem = window.__m9RestoreStorage; delete window.__m9RestoreStorage; });

  const end = await counters();
  check(end.writes === 7 && end.successes === 6 && end.metadata === 2 && end.unexpected === 0, 'unexpected native/write ledger');
  check(writes.length === 7 && writes.every(write => write.method === 'POST' && write.key), 'unexpected API mutation or missing idempotency key');
  const createWrites = writes.filter(write => write.path === '/api/v1/media-requests');
  check(createWrites.length === 3 && new Set(createWrites.map(write => write.key)).size === 3, 'submission duplicated an original key');
  for (const write of createWrites) exactKeys(write.body, ['media_type', 'tmdb_id', 'seasons', 'include_specials']);
  for (const write of writes.filter(write => write.path.endsWith('/close'))) exactKeys(write.body, ['expected_version', 'action', 'reason']);
  const approvalWritesSeen = writes.filter(write => write.path.endsWith('/approve'));
  check(approvalWritesSeen.length === 1, 'successful approval POST was resent');
  exactKeys(approvalWritesSeen[0].body, ['expected_version', 'root_id', 'quality_policy_id', 'indexer_ids', 'downloader_id']);
  check(approvalWritesSeen[0].body.indexer_ids[0] === setup.indexer, 'form changed the configured indexer');
  check(memberOptionReads.length === 0, 'member fetched administrator-only options');
  check(external.length === 0, 'unexpected external network request');
  check(errors.length === 0, 'browser or route error');
  console.log(`M9_REQUEST_CHROME_PASS checks=${checks} viewports=1440_768_375_320 screenshots=6 Cookie=REAL lost_submit=GET_RECOVERY lost_approval=GET_RECOVERY approval=REAL_FORM scope=REAL_DB conflict=READ_ONLY role_revocation=REAL_DB source_calls=0 writes=7 successful=6 snapshots=GO_FULL_NON_AUTH_TABLES`);
} finally {
  for (const context of contexts.reverse()) await context.close();
  await browser.close();
}
