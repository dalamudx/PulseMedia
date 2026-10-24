import assert from "node:assert/strict";
import { chromium } from "playwright-core";
import { join } from "node:path";
const setup = JSON.parse(process.env.PULSE_M11_PUSH_SETUP), base = setup.base, shots = process.env.PULSE_M11_PUSH_SHOTS;
const browser = await chromium.connectOverCDP(process.env.PULSE_M11_PUSH_CDP);
// The Go fixture launched this fresh, task-owned regular Chrome profile.
// Chrome intentionally disallows Push subscriptions in off-the-record contexts.
const context = browser.contexts()[0];
assert(context, "owned persistent Chrome context required");
await context.clearCookies();
let checks = 0, lost = false, failRead = false;
const check = (value, message) => { assert(value, message); checks++; };
const writes = [], errors = [];
await context.grantPermissions(["notifications"], { origin: base });
await context.addCookies([{ name: "pulsemedia_session", value: setup.admin, url: base, httpOnly: true, sameSite: "Lax" }]);
await context.route("**/*", async route => {
  const request = route.request(), url = new URL(request.url());
  if (url.origin !== base) { await route.abort(); return; }
  if (failRead && url.pathname === "/api/v1/web-push/config") { await route.fulfill({ status: 503, contentType: "application/json", body: '{"code":"UNAVAILABLE"}' }); return; }
  if (request.method() === "POST" && url.pathname === "/api/v1/web-push/devices") {
    writes.push({ key: request.headers()["idempotency-key"] });
    if (!lost) { lost = true; const response = await route.fetch({ maxRetries: 0, maxRedirects: 0 }); check(response.status() === 201, "native browser registration was rejected"); await route.abort("failed"); return; }
  }
  await route.continue();
});
const page = await context.newPage(); await page.setViewportSize({ width: 1440, height: 1050 }); await page.emulateMedia({ colorScheme: "dark", reducedMotion: "reduce" }); page.setDefaultTimeout(25000); page.on("pageerror", error => errors.push(error.message));
const panel = () => page.getByRole("region", { name: "浏览器通知", exact: true });
const status = text => panel().getByRole("status").filter({ hasText: text }).waitFor();
const open = async () => { await page.goto(base + "/settings"); await page.getByRole("button", { name: "浏览器通知", exact: true }).click(); await status("服务器已启用浏览器推送"); };
const settle = () => page.evaluate(() => Promise.all(document.getAnimations().filter(a => a instanceof CSSTransition).map(a => a.finished.catch(() => {}))));
async function capture(name) {
  await panel().scrollIntoViewIfNeeded(); await settle();
  check(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1), `page clipped at ${name}`);
  const controls = await panel().getByRole("button").evaluateAll(nodes => nodes.filter(node => node.getBoundingClientRect().width > 0).map(node => { const r = node.getBoundingClientRect(); return { x: r.x, right: r.right, height: r.height }; }));
  check(controls.every(c => c.x >= -1 && c.right <= page.viewportSize().width + 1 && c.height >= 43), `notification control clipped or below touch target at ${name}`);
  await panel().screenshot({ path: join(shots, `${name}.png`) });
}
try {
  await open(); check(writes.length === 0, "reading settings wrote a device");
  check(await page.evaluate(() => Notification.permission) === "granted", "native notification permission unavailable");
  await page.evaluate(() => { const original = Storage.prototype.setItem; window.__ownedPushStorage = original; Storage.prototype.setItem = function(key, value) { if (key.startsWith("pulse.webpush.command.v1:")) throw Error("owned quota"); return original.call(this, key, value); }; });
  await panel().getByRole("button", { name: "在此浏览器启用通知", exact: true }).click();
  await page.waitForFunction(() => { const region = document.querySelector('[aria-label="浏览器通知"]'); return region?.textContent.includes("恢复记录保存失败") || region?.textContent.includes("浏览器推送订阅失败") || region?.textContent.includes("通知订阅尚未就绪"); }, undefined, { timeout: 90000 });
  check((await panel().textContent()).includes("恢复记录保存失败"), "native subscription stopped before storage: " + await panel().textContent());
  check(writes.length === 0, "storage failure issued registration");
  check(await panel().getByRole("button", { name: "在此浏览器启用通知", exact: true }).isDisabled(), "storage failure did not lock writes");
  await page.evaluate(() => { Storage.prototype.setItem = window.__ownedPushStorage; });
  await panel().getByRole("button", { name: "重新读取恢复记录", exact: true }).click();
  await panel().getByRole("button", { name: "在此浏览器启用通知", exact: true }).click();
  await status("操作结果未确认"); check(writes.length === 1, "lost receipt triggered automatic replay");
  check(await page.evaluate(() => !JSON.stringify({ ...localStorage, ...sessionStorage }).includes("fcm.googleapis.com")), "browser capability persisted in storage");
  const native = await page.evaluate(async () => { const s = await (await navigator.serviceWorker.ready).pushManager.getSubscription(); return { host: s ? new URL(s.endpoint).hostname : null, hasKeys: !!s?.getKey("p256dh") && !!s?.getKey("auth") }; });
  check(native.host === "fcm.googleapis.com" && native.hasKeys, "registration did not use native browser keys");
  await page.reload(); await page.getByRole("button", { name: "浏览器通知", exact: true }).click();
  await panel().getByRole("button", { name: "查询原设备操作回执", exact: true }).click(); await status("已读取当前通知设置");
  await panel().getByText("已登记设备", { exact: true }).waitFor(); check(writes.length === 1, "receipt recovery wrote another device");
  const response = await context.request.post(base + "/owned/send", { headers: { "X-Owned-Control": setup.control }, timeout: 30000 });
  check(response.status() === 200, "native VAPID send was not accepted by FCM");
  const sent = await response.json(); check(sent.accepted === true, "send receipt was not recorded");
  await page.waitForFunction(async () => (await (await navigator.serviceWorker.ready).getNotifications()).some(n => n.tag.startsWith("pulsemedia:")), undefined, { timeout: 45000 });
  const displayed = await page.evaluate(async () => (await (await navigator.serviceWorker.ready).getNotifications()).map(n => ({ title: n.title, body: n.body, tag: n.tag, path: n.data?.path })));
  check(displayed.length === 1 && displayed[0].tag === "pulsemedia:" + sent.event_id && displayed[0].title === "媒体已入库" && displayed[0].path === "/", "native FCM notification missing or retargeted");
  check(!JSON.stringify(displayed).includes("OwnedBrowser"), "media details leaked in notification");
  for (const width of [1440, 768, 375, 320]) { await page.setViewportSize({ width, height: 1050 }); await capture(`push-${width}`); }
  failRead = true; await panel().getByRole("button", { name: "重新读取通知设置", exact: true }).click(); await status("读取失败");
  check(await panel().getByRole("button", { name: "在此浏览器启用通知", exact: true }).isDisabled(), "failed read retained save readiness");
  check(await panel().getByText("已登记设备", { exact: true }).count() === 0, "failed read retained old device data"); await capture("push-unavailable");
  failRead = false; await panel().getByRole("button", { name: "重新读取通知设置", exact: true }).click(); await status("已读取当前通知设置");
  await page.setViewportSize({ width: 1440, height: 1050 });
  await page.getByTitle("退出登录").click(); await page.waitForURL("**/login");
  check(await page.evaluate(async () => { const registration = await navigator.serviceWorker.getRegistration("/"); return !registration || (await registration.pushManager.getSubscription()) === null; }), "logout kept browser subscribed");
  await page.getByPlaceholder("输入管理员用户名...").fill("push-member"); await page.locator('input[type="password"]').fill("Owned-Push-Member-123!"); await page.locator("form button[type=submit]").click();
  await page.waitForURL(url => !url.pathname.startsWith("/login")); await open();
  await panel().getByText("当前账号没有浏览器通知设备。", { exact: true }).waitFor();
  check(await panel().getByText("已登记设备", { exact: true }).count() === 0, "account switch retained original user's device");
  check(writes.length === 1, "account switch registered silently"); await capture("push-member");
  check(errors.length === 0, "browser runtime error");
  console.log(`M11_PUSH_CHROME_PASS checks=${checks} subscription=NATIVE_FCM server=NATIVE_VAPID event=NETWORK browser=REAL_NOTIFICATION screenshots=6 viewports=1440_768_375_320 recovery=ORIGINAL_KEY storage=NO_CAPABILITY logout=UNSUBSCRIBED account=ISOLATED`);
} finally { await context.close(); await browser.close(); }
