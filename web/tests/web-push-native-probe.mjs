import { chromium } from "playwright-core";
import { createServer } from "node:http";
import { readFileSync, realpathSync } from "node:fs";
import { createECDH } from "node:crypto";
import { join } from "node:path";
import assert from "node:assert/strict";
const work = process.env.TMPDIR;
assert(work && realpathSync(work) === work && work.startsWith("/data/temp/"), "owned temp required");
const worker = readFileSync(new URL("../public/pulse-push-sw.js", import.meta.url));
const server = createServer((req, res) => {
  res.setHeader("Cache-Control", "no-store");
  if (req.url === "/pulse-push-sw.js") { res.setHeader("Content-Type", "text/javascript"); res.end(worker); }
  else { res.setHeader("Content-Type", "text/html"); res.end("<!doctype html><html><title>PulseMedia owned push probe</title><button>Enable notifications</button></html>"); }
});
await new Promise(resolve => server.listen(0, "127.0.0.1", resolve));
const base = `http://127.0.0.1:${server.address().port}`;
let context;
try {
  context = await chromium.launchPersistentContext(join(work, "chrome-profile"), { executablePath: "/opt/google/chrome/chrome", headless: true, ignoreDefaultArgs: ["--disable-background-networking"], args: ["--no-sandbox", "--disable-dev-shm-usage", "--no-first-run"], viewport: { width: 1024, height: 768 } });
  await context.grantPermissions(["notifications"], { origin: base });
  const page = await context.newPage(), cdp = await context.newCDPSession(page), registrations = new Map();
  cdp.on("ServiceWorker.workerRegistrationUpdated", event => { for (const registration of event.registrations) registrations.set(registration.scopeURL, registration.registrationId); });
  await cdp.send("ServiceWorker.enable");
  await page.goto(base);
  await page.evaluate(async () => { await navigator.serviceWorker.register("/pulse-push-sw.js"); await navigator.serviceWorker.ready; });
  assert.equal(await page.evaluate(() => Notification.permission), "granted");
  const key = createECDH("prime256v1"); key.generateKeys();
  const subscription = await page.evaluate(async publicKey => {
    const key = Uint8Array.from(atob(publicKey), c => c.charCodeAt(0));
    return await Promise.race([
      navigator.serviceWorker.ready.then(registration => registration.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: key })).then(value => ({ status: "registered", endpointHost: new URL(value.endpoint).hostname, hasKeys: !!value.getKey("p256dh") && !!value.getKey("auth") })).catch(error => ({ status: "failed", name: error.name, message: String(error.message).slice(0, 180) })),
      new Promise(resolve => setTimeout(() => resolve({ status: "timeout" }), 25000)),
    ]);
  }, key.getPublicKey().toString("base64"));
  const registrationId = registrations.get(base + "/");
  assert(registrationId, "native worker registration absent");
  const payload = { title: "Private fixture title", body: "Private fixture account", tag: "pulsemedia:00000000-0000-4000-8000-000000000001", url: "/" };
  await cdp.send("ServiceWorker.deliverPushMessage", { origin: base, registrationId, data: JSON.stringify(payload) });
  await page.waitForFunction(async () => (await (await navigator.serviceWorker.ready).getNotifications()).length === 1, { timeout: 10000 });
  const notifications = await page.evaluate(async () => (await (await navigator.serviceWorker.ready).getNotifications()).map(n => ({ title: n.title, body: n.body, tag: n.tag, path: n.data?.path })));
  assert.equal(notifications[0].title, "媒体已入库"); assert.equal(notifications[0].tag, payload.tag); assert.equal(notifications[0].path, "/");
  assert(!JSON.stringify(notifications).includes("Private"));
  await page.evaluate(async () => { for (const n of await (await navigator.serviceWorker.ready).getNotifications()) n.close(); const subscription = await (await navigator.serviceWorker.ready).pushManager.getSubscription(); if (subscription) await subscription.unsubscribe(); });
  console.log(`M11_PUSH_NATIVE_CHROME_PROBE ${JSON.stringify({ permission: "granted", serviceWorker: "native", pushEvent: "CDP_TEST_EVENT", notification: "ENUMERATED_REAL", accountContent: "GENERIC_ONLY", subscription })}`);
} finally {
  if (context) await context.close();
  await new Promise(resolve => server.close(resolve));
}
