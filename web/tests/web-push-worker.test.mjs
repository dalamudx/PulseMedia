import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import vm from "node:vm";

const source = readFileSync(new URL("../public/pulse-push-sw.js", import.meta.url), "utf8");
function worker() {
  const handlers = {}, shown = [], opened = [];
  const self = {
    location: { origin: "https://pulse.example" },
    addEventListener(name, fn) { handlers[name] = fn; },
    skipWaiting: async () => {},
    clients: { claim: async () => {}, matchAll: async () => [], openWindow: async url => { opened.push(url); } },
    registration: { showNotification: async (...args) => { shown.push(args); }, getNotifications: async () => [] },
  };
  vm.runInNewContext(source, { self, URL });
  return { self, handlers, shown, opened };
}
const payload = { title: "Private media title", body: "Private account details", tag: "pulsemedia:00000000-0000-4000-8000-000000000001", url: "/" };
async function fire(fn, event = {}) { let promise; fn({ ...event, waitUntil(value) { promise = value; } }); if (promise) await promise; }

test("notification worker shows only generic text with stable event tag", async () => {
  const w = worker();
  await fire(w.handlers.push, { data: { json: () => payload } });
  assert.equal(w.shown.length, 1);
  const [title, options] = w.shown[0];
  assert.equal(title, "媒体已入库");
  assert.equal(options.tag, payload.tag);
  assert.equal(options.data.path, "/");
  assert.ok(!JSON.stringify(w.shown).includes("Private"));
  assert.equal(w.handlers.fetch, undefined);
});
test("notification worker rejects malformed, arbitrary or oversized payloads", async () => {
  const w = worker();
  for (const value of [null, [], {}, { ...payload, url: "https://evil.example" }, { ...payload, title: "x".repeat(161) }, { ...payload, body: "x".repeat(1801) }, { ...payload, tag: "wrong" }, { ...payload, endpoint: "private capability" }]) {
    await fire(w.handlers.push, { data: { json: () => value } });
  }
  await fire(w.handlers.push, { data: { json() { throw new Error("bad JSON"); } } });
  assert.equal(w.shown.length, 0);
});
test("notification click ignores injected target and always opens this origin", async () => {
  const w = worker(); let closed = 0;
  await fire(w.handlers.notificationclick, { notification: { data: { path: "https://evil.example" }, close() { closed++; } } });
  assert.equal(closed, 1);
  assert.deepEqual(w.opened, ["https://pulse.example/"]);
});
test("notification click navigates and focuses an existing local client", async () => {
  const w = worker(), visited = [];
  w.self.clients.matchAll = async () => [{ url: "https://external.example/", navigate() { assert.fail("external navigation"); } }, { url: "https://pulse.example/settings", async navigate(url) { visited.push(url); }, async focus() { visited.push("focused"); } }];
  await fire(w.handlers.notificationclick, { notification: { close() {} } });
  assert.deepEqual(visited, ["https://pulse.example/", "focused"]);
  assert.equal(w.opened.length, 0);
});
test("account cleanup accepts only messages from this origin", async () => {
  const w = worker(); let closed = 0;
  w.self.registration.getNotifications = async () => [{ close() { closed++; } }];
  await fire(w.handlers.message, { data: { type: "pulsemedia:clear-notifications" }, source: { url: "https://evil.example" } });
  assert.equal(closed, 0);
  await fire(w.handlers.message, { data: { type: "pulsemedia:clear-notifications" }, source: { url: "https://pulse.example/settings" } });
  assert.equal(closed, 1);
});
