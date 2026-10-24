import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import ts from "typescript";
import React from "react";
import { renderToStaticMarkup } from "react-dom/server";

const root = new URL("../src/features/configuration/", import.meta.url);
const transpile = source => ts.transpileModule(source, { compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.ESNext, jsx: ts.JsxEmit.React } }).outputText;
const module = async file => import(`data:text/javascript;base64,${Buffer.from(transpile(readFileSync(new URL(file, root), "utf8"))).toString("base64")}`);
const commands = await module("webPushCommand.ts");
const { automationInputHash } = await module("automationSettingsCommand.ts");
const owner = "00000000-0000-4000-8000-000000000001", id = "00000000-0000-4000-8000-000000000002", version = "a".repeat(64);
const sub = { endpoint: "https://fcm.googleapis.com/private-device", p256dh: "private-p256dh", auth: "private-auth" };
const row = { id, status: "active", created_at: "2026-10-23T00:00:00Z", updated_at: "2026-10-23T00:00:00Z" };
const result = (status, data) => ({ response: { status, ok: status >= 200 && status < 300 }, data, error: status >= 400 ? { message: "private upstream error" } : undefined });
const flush = async () => { for (let i = 0; i < 8; i++) await new Promise(resolve => setImmediate(resolve)); };
async function host({ brokenStorage = false, writeFailure = false, seed } = {}) {
  const slots = [], effects = new Map(), calls = [], storage = new Map();
  if (seed) storage.set(commands.pushStorageKey(owner), seed);
  let cursor = 0, tree, enableCalls = 0, clearCalls = 0;
  globalThis.localStorage = { getItem(key) { if (brokenStorage) throw Error("unreadable"); return storage.get(key) ?? null; }, setItem(key, value) { if (writeFailure) throw Error("quota"); storage.set(key, value); }, removeItem(key) { storage.delete(key); } };
  globalThis.window = { addEventListener() {}, removeEventListener() {} };
  const transport = method => (path, options) => new Promise((resolve, reject) => calls.push({ method, path, options, resolve, reject }));
  const hooks = {
    React, api: { GET: transport("GET"), POST: transport("POST"), PUT: transport("PUT") }, useAuth: () => ({ user: { id: owner }, checking: false }),
    useRef(value) { const i = cursor++; return slots[i] ??= { current: value }; },
    useState(value) { const i = cursor++; if (!(i in slots)) slots[i] = typeof value === "function" ? value() : value; return [slots[i], next => { slots[i] = typeof next === "function" ? next(slots[i]) : next; }]; },
    useEffect(fn, deps) { const i = cursor++, previous = slots[i]; if (!previous || !deps.every((d, index) => Object.is(d, previous.deps[index]))) { previous?.cleanup?.(); slots[i] = { deps }; effects.set(i, fn); } },
    automationInputHash, csrfHeaders: () => ({}), browserPushAvailable: () => true,
    enableBrowserPush: async (_key, current) => { enableCalls++; if (!current()) throw Error(); return sub; },
    clearBrowserPush: async () => { clearCalls++; }, currentBrowserPush: async () => ({ pushManager: { getSubscription: async () => ({ toJSON: () => ({ endpoint: sub.endpoint, keys: { p256dh: sub.p256dh, auth: sub.auth } }) }) } }),
    ...commands,
  };
  let source = readFileSync(new URL("WebPushPanel.tsx", root), "utf8");
  const ast = ts.createSourceFile("panel.tsx", source, ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX);
  for (const node of ast.statements.filter(ts.isImportDeclaration).reverse()) source = source.slice(0, node.getFullStart()) + source.slice(node.end);
  const key = `__push_${crypto.randomUUID().replaceAll("-", "")}`;
  globalThis[key] = hooks;
  const loaded = await import(`data:text/javascript;base64,${Buffer.from(transpile(`const {${Object.keys(hooks).join(",")}}=globalThis[${JSON.stringify(key)}];\n${source}\nexport {BrowserPushSettings};`)).toString("base64")}`);
  delete globalThis[key];
  const render = () => { cursor = 0; tree = loaded.BrowserPushSettings({ owner }); const queued = [...effects]; effects.clear(); for (const [i, fn] of queued) slots[i].cleanup = fn(); return renderToStaticMarkup(tree); };
  function nodes(value, output = []) { if (React.isValidElement(value)) { output.push(value); for (const child of React.Children.toArray(value.props.children)) nodes(child, output); } return output; }
  const button = label => { render(); const found = nodes(tree).find(n => n.type === "button" && String(n.props.children) === label); assert.ok(found, `missing button ${label}`); return found; };
  const click = async label => { const b = button(label); assert.ok(!b.props.disabled, `disabled button ${label}`); const before = calls.length; b.props.onClick(); await flush(); render(); const deadline = Date.now() + 2000; while (calls.length === before && tree.props["aria-busy"] && Date.now() < deadline) { await flush(); render(); } };
  const read = async (enabled = true, devices = []) => { calls.find(c => c.path === "/api/v1/web-push/config" && !c.done).done = true; calls.find(c => c.path === "/api/v1/web-push/config" && c.done && !c.resolved).resolved = true; const c = [...calls].reverse().find(c => c.path === "/api/v1/web-push/config"); c.resolve(result(200, { enabled, public_key: "public", version })); [...calls].reverse().find(c => c.path === "/api/v1/web-push/devices" && c.method === "GET").resolve(result(200, devices)); await flush(); return render(); };
  render();
  return { calls, storage, render, button, click, read, count: () => ({ enableCalls, clearCalls }), stop() { for (const value of slots) value?.cleanup?.(); } };
}

test("Web Push recovery metadata excludes delivery capabilities and other accounts", () => {
  const command = { owner, id, key: crypto.randomUUID(), kind: "register", vapidVersion: version, hash: "b".repeat(64) };
  const raw = commands.serializePushCommand(command);
  assert.deepEqual(commands.parsePushCommand(raw, owner), command);
  assert.equal(commands.parsePushCommand(raw, id), null);
  assert.equal(commands.parsePushCommand(JSON.stringify({ ...command, endpoint: sub.endpoint }), owner), null);
  assert.equal(commands.parsePushCommand(JSON.stringify({ ...command, kind: "revoke" }), owner), null);
});
test("actual push controls read without requesting permission and stop after failed reads", async () => {
  const h = await host(); assert.equal(h.count().enableCalls, 0); await h.read(false);
  assert.equal(h.button("在此浏览器启用通知").props.disabled, true);
  await h.click("重新读取通知设置"); [...h.calls].reverse().find(c => c.path === "/api/v1/web-push/config").resolve(result(503)); [...h.calls].reverse().find(c => c.path === "/api/v1/web-push/devices").resolve(result(200, [])); await flush();
  assert.match(h.render(), /读取失败/); assert.equal(h.button("在此浏览器启用通知").props.disabled, true); h.stop();
});
test("push registration persists only recovery metadata before sending", async () => {
  const h = await host(); await h.read(); await h.click("在此浏览器启用通知");
  const save = h.calls.find(c => c.method === "POST"); assert.ok(save, h.render());
  const raw = h.storage.get(commands.pushStorageKey(owner)); assert.ok(raw); assert.ok(!raw.includes(sub.endpoint)); assert.ok(!raw.includes(sub.auth));
  save.resolve(result(201, { ...row, id: save.options.body.id })); await flush();
  assert.equal(h.storage.has(commands.pushStorageKey(owner)), false); assert.equal(h.count().enableCalls, 1); h.stop();
});
test("unreadable or unwritable recovery storage blocks HTTP writes", async () => {
  for (const option of [{ brokenStorage: true }, { writeFailure: true }]) {
    const h = await host(option); await h.read();
    if (option.brokenStorage) assert.equal(h.button("在此浏览器启用通知").props.disabled, true);
    else { await h.click("在此浏览器启用通知"); assert.equal(h.button("在此浏览器启用通知").props.disabled, true); }
    assert.equal(h.calls.some(c => c.method === "POST"), false); h.stop();
  }
});
test("lost registration receipt requires GET before explicit same-key retry", async () => {
  const h = await host(); await h.read(); await h.click("在此浏览器启用通知");
  const save = h.calls.find(c => c.method === "POST"); save.reject(Error("lost")); await flush();
  await h.click("查询原设备操作回执"); const lookup = h.calls.find(c => c.path.includes("/receipts/")); lookup.resolve(result(404)); await flush();
  await h.click("使用原键重试设备操作"); const retry = h.calls.filter(c => c.method === "POST")[1];
  assert.equal(retry.options.params.header["Idempotency-Key"], save.options.params.header["Idempotency-Key"]); assert.deepEqual(retry.options.body, save.options.body); h.stop();
});
test("reloaded registration restores its original device and key from browser subscription", async () => {
  const body = { id, vapid_version: version, subscription: sub }, command = { owner, id, key: crypto.randomUUID(), kind: "register", vapidVersion: version, hash: await automationInputHash(body) };
  const h = await host({ seed: commands.serializePushCommand(command) }); await h.read();
  await h.click("查询原设备操作回执"); h.calls.find(c => c.path.includes("/receipts/")).resolve(result(404)); await flush();
  await h.click("使用原键重试设备操作"); const save = h.calls.find(c => c.method === "POST"); assert.deepEqual(save.options.body, body); assert.equal(h.count().enableCalls, 0); h.stop();
});
test("account unmount withdraws readiness and rejects late permission results", async () => {
  const h = await host(); await h.read(); h.stop(); await h.click("在此浏览器启用通知");
  assert.equal(h.calls.some(c => c.method === "POST"), false);
});
