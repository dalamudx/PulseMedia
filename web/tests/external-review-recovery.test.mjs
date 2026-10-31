import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import ts from "typescript";
import React from "react";

const account = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa";
const scope = "11111111-1111-4111-8111-111111111111";
const file = "22222222-2222-4222-8222-222222222222";
const operation = "33333333-3333-4333-8333-333333333333";
const journal = `pulse.external-file-review.v1:${account}`;
const bookmark = `pulse.external-file-organization.v1:${account}`;
const locator = { scope, file, operation };
const completed = { operation: { id: operation }, state: "completed", review_required: false };
const command = kind => ({ kind, scope, file, key: `original-${kind}`, attempted: true, body: kind === "organization" ? { confirmation_id: "44444444-4444-4444-8444-444444444444" } : { scope_version: 7, file_revision: 9, tmdb_id: 551, nested: { season: 1, episode: 7 } } });

// Execute the real TSX handlers with a deterministic hook scheduler and React elements.
// No browser, generated files, task-specific environment, or external test renderer required.
async function fixture({ values = new Map(), actor = { id: account, role: "admin" } } = {}) {
  const slots = [], effects = [], calls = [], mounts = [];
  let cursor = 0, tree, live = true, storageBroken = false;
  const host = {
    React,
    useAuth: () => ({ user: actor }),
    sessionStorage: {
      getItem: key => values.get(key) ?? null,
      setItem(key, value) { if (storageBroken) throw new Error("storage unavailable"); values.set(key, value); },
      removeItem: key => values.delete(key),
    },
    document: { cookie: "csrf_token=owned-csrf" },
    fetch: (url, options) => new Promise((resolve, reject) => calls.push({ url, options, resolve, reject })),
    useRef(value) { const i = cursor++; return slots[i] ??= { current: value }; },
    useState(value) {
      const i = cursor++;
      if (!(i in slots)) slots[i] = typeof value === "function" ? value() : value;
      return [slots[i], next => { assert(live, "late result mutated unmounted state"); slots[i] = typeof next === "function" ? next(slots[i]) : next; }];
    },
    useEffect(fn, deps) {
      const i = cursor++;
      if (!slots[i] || !deps.every((v, n) => Object.is(v, slots[i].deps[n]))) effects.push(() => { slots[i]?.cleanup?.(); slots[i] = { deps, cleanup: fn() }; });
    },
    UntrackedCompanionsPanel({ operationId }) { mounts.push(operationId); return React.createElement("aside", { "data-companions": operationId }, "字幕恢复入口"); },
  };
  let source = readFileSync(new URL("../src/features/configuration/ExternalFileReviewPanel.tsx", import.meta.url), "utf8");
  const ast = ts.createSourceFile("panel.tsx", source, ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX);
  for (const node of ast.statements.filter(ts.isImportDeclaration).reverse()) source = source.slice(0, node.getFullStart()) + source.slice(node.end);
  source += "\nexport { ExternalFileReviewEditor as Fixture };";
  const key = `__recovery_${crypto.randomUUID().replaceAll("-", "")}`;
  globalThis[key] = host;
  let module;
  try {
    source = `const { ${Object.keys(host).join(",")} } = globalThis[${JSON.stringify(key)}];\n` + source;
    const compiled = ts.transpileModule(source, { compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.ESNext, jsx: ts.JsxEmit.React } }).outputText;
    module = await import(`data:text/javascript;base64,${Buffer.from(compiled).toString("base64")}`);
  } finally { delete globalThis[key]; }
  const text = n => typeof n === "string" || typeof n === "number" ? String(n) : React.Children.toArray(n?.props?.children).map(text).join("");
  function walk(n, pred) {
    if (!n || typeof n !== "object") return null;
    if (pred(n)) return n;
    for (const child of React.Children.toArray(n.props?.children)) { const found = walk(child, pred); if (found) return found; }
    return null;
  }
  function render() {
    cursor = 0; tree = module.Fixture({ account: actor.id });
    for (const effect of effects.splice(0)) effect();
    const child = walk(tree, n => n.type === host.UntrackedCompanionsPanel);
    if (child) child.type(child.props);
  }
  const h = {
    values, calls, mounts, render,
    async flush() { for (let i = 0; i < 24; i++) await Promise.resolve(); if (live) render(); },
    button: label => walk(tree, n => n.type === "button" && text(n) === label),
    text: () => text(tree),
    click(label) { const b = h.button(label); assert(b, `missing button: ${label}`); assert(!b.props.disabled, `disabled button: ${label}`); b.props.onClick(); },
    reply(index, data = {}, status = 200) { calls[index].resolve({ ok: status < 400, status, json: async () => data }); },
    fail(index) { calls[index].reject(new Error("connection failed")); },
    breakStorage() { storageBroken = true; },
    wrapper(next) { actor = next; return module.ExternalFileReviewPanel(); },
    unmount() { for (const slot of slots) slot?.cleanup?.(); live = false; },
  };
  render(); await h.flush(); return h;
}
const stored = (p = command("organization")) => new Map([[journal, JSON.stringify(p)]]);

for (const kind of ["review", "confirmation", "organization"]) test(`${kind}: only explicit original-receipt 404 authorizes exact original POST`, async () => {
  const p = command(kind), h = await fixture({ values: stored(p) });
  assert.equal(h.calls.length, 0); assert.equal(h.button("使用原键提交"), null);
  h.click("读取原回执"); assert.equal(h.calls[0].options.method, "GET");
  assert.match(h.calls[0].url, new RegExp(`/${kind === "review" ? "review" : kind}-commands/${p.key}$`));
  h.reply(0, {}, 404); await h.flush();
  assert(h.button("使用原键提交")); assert.equal(JSON.parse(h.values.get(journal)).attempted, true);
  h.click("使用原键提交"); assert.equal(h.calls.length, 2); assert.equal(h.calls[1].options.method, "POST");
  assert.equal(h.calls[1].options.headers["Idempotency-Key"], p.key);
  assert.equal(h.calls[1].options.headers["X-CSRF-Token"], "owned-csrf");
  assert.equal(h.calls[1].options.body, JSON.stringify(p.body));
  h.fail(1); await h.flush();
  assert.equal(h.button("使用原键提交"), null); assert.equal(h.button("保存并提交原命令"), null);
  assert.equal(JSON.parse(h.values.get(journal)).attempted, true);
  h.unmount();
});

test("404 retry permission clears on another read and never survives remount", async () => {
  const values = stored(), h = await fixture({ values });
  h.click("读取原回执"); h.reply(0, {}, 404); await h.flush(); assert(h.button("使用原键提交"));
  h.click("读取原回执"); h.render(); assert.equal(h.button("使用原键提交"), null);
  h.reply(1, {}, 503); await h.flush(); assert.equal(h.button("使用原键提交"), null);
  h.click("读取原回执"); h.reply(2, {}, 404); await h.flush(); h.unmount();
  const fresh = await fixture({ values }); assert.equal(fresh.calls.length, 0); assert.equal(fresh.button("使用原键提交"), null); fresh.unmount();
});

for (const status of [400, 401, 403, 409, 429, 500, 503]) test(`receipt ${status} cannot authorize a POST`, async () => {
  const h = await fixture({ values: stored() });
  h.click("读取原回执"); h.reply(0, {}, status); await h.flush();
  assert.equal(h.button("使用原键提交"), null); assert.equal(h.calls.length, 1); assert(h.values.has(journal)); h.unmount();
});

test("transport failure and a late 404 cannot grant retry permission", async () => {
  const h = await fixture({ values: stored() }); h.click("读取原回执"); h.fail(0); await h.flush();
  assert.equal(h.button("使用原键提交"), null);
  h.click("读取原回执"); h.unmount(); assert.equal(h.calls[1].options.signal.aborted, true);
  h.reply(1, {}, 404); await h.flush(); assert(h.values.has(journal));
});

test("storage failure before 404-authorized retry stops before POST without claiming an unknown command was never sent", async () => {
  const h = await fixture({ values: stored() }); h.click("读取原回执"); h.reply(0, {}, 404); await h.flush();
  h.breakStorage(); h.click("使用原键提交"); await h.flush();
  assert.equal(h.calls.length, 1); assert.equal(h.button("使用原键提交"), null);
  assert.equal(h.button("保存并提交原命令"), null); assert.equal(JSON.parse(h.values.get(journal)).attempted, true); h.unmount();
});

test("accepted organization saves only locator; explicit GET after refresh restores completed video and mounts companions", async () => {
  const values = stored(), h = await fixture({ values });
  h.click("读取原回执"); h.reply(0, { operation: { id: operation } }); await h.flush();
  assert.equal(values.has(journal), false); assert.deepEqual(JSON.parse(values.get(bookmark)), locator); h.unmount();
  const fresh = await fixture({ values });
  assert.equal(fresh.calls.length, 0); assert.deepEqual(fresh.mounts, []);
  fresh.click("读取上次整理结果"); assert.equal(fresh.calls.length, 1);
  assert.equal(fresh.calls[0].url, `/api/v1/library/untracked-scopes/${scope}/discoveries/${file}/organizations/${operation}`);
  assert.equal(fresh.calls[0].options.method, "GET"); assert.equal(fresh.calls[0].options.body, undefined);
  fresh.reply(0, completed); await fresh.flush();
  assert.match(fresh.text(), /整理完成，已记录文件入库/); assert.deepEqual(fresh.mounts, [operation]);
  fresh.click("读取整理状态"); assert.equal(fresh.calls[1].options.method, "GET"); fresh.reply(1, completed); await fresh.flush(); fresh.unmount();
});

test("pending command takes precedence over saved organization; successful original receipt does not reuse an old completion", async () => {
  const values = stored(); values.set(bookmark, JSON.stringify(locator));
  const h = await fixture({ values }); assert.equal(h.button("读取上次整理结果"), null); assert.equal(h.calls.length, 0);
  h.click("读取原回执"); h.reply(0, { operation: { id: operation } }); await h.flush();
  assert(h.button("读取上次整理结果")); assert.deepEqual(h.mounts, []); h.unmount();
});

for (const status of [403, 404, 503]) test(`bookmark GET ${status} never grants command retry or mounts companions`, async () => {
  const h = await fixture({ values: new Map([[bookmark, JSON.stringify(locator)]]) });
  h.click("读取上次整理结果"); h.reply(0, {}, status); await h.flush();
  assert.equal(h.button("使用原键提交"), null); assert.deepEqual(h.mounts, []); assert.equal(h.calls.length, 1); h.unmount();
});

test("account/role wrapper isolates recovery and late completed GET cannot mutate state or storage", async () => {
  const values = new Map([[bookmark, JSON.stringify(locator)]]), h = await fixture({ values });
  assert.equal(h.wrapper(null), null); assert.equal(h.wrapper({ id: account, role: "member" }), null);
  const a = h.wrapper({ id: account, role: "admin" }), b = h.wrapper({ id: "other", role: "admin" }); assert.notEqual(a.key, b.key);
  // Simulate keyed editor teardown on an auth transition.
  h.wrapper({ id: account, role: "admin" }); h.click("读取上次整理结果"); h.unmount();
  h.reply(0, completed); await h.flush(); assert.deepEqual(h.mounts, []); assert.deepEqual([...values], [[bookmark, JSON.stringify(locator)]]);
  const other = await fixture({ values, actor: { id: "other", role: "admin" } });
  assert.equal(other.calls.length, 0); assert.equal(other.button("读取上次整理结果"), null); assert.equal(other.button("读取原回执"), null); other.unmount();
});

test("late accepted receipt cannot clear original journal or create a bookmark after logout", async () => {
  const values = stored(), h = await fixture({ values }); h.click("读取原回执"); h.unmount();
  h.reply(0, { operation: { id: operation } }); await h.flush();
  assert(values.has(journal)); assert.equal(values.has(bookmark), false);
});

test("late JSON body is fenced even when headers arrived before unmount", async () => {
  const h = await fixture({ values: new Map([[bookmark, JSON.stringify(locator)]]) });
  h.click("读取上次整理结果"); let finish;
  h.calls[0].resolve({ ok: true, status: 200, json: () => new Promise(resolve => { finish = resolve; }) });
  await h.flush(); assert.equal(typeof finish, "function"); h.unmount(); finish(completed); await h.flush(); assert.deepEqual(h.mounts, []);
});

test("invalid bookmarks are ignored and accepted snapshots never persist extra fields", async () => {
  for (const value of ["{", "null", "x".repeat(513), JSON.stringify({ ...locator, operation: "bad" })]) {
    const h = await fixture({ values: new Map([[bookmark, value]]) }); assert.equal(h.button("读取上次整理结果"), null); assert.equal(h.calls.length, 0); h.unmount();
  }
  const values = new Map([[bookmark, JSON.stringify({ ...locator, ignored: "extra" })]]), h = await fixture({ values });
  h.click("读取上次整理结果"); h.reply(0, completed); await h.flush(); assert.deepEqual(h.mounts, [operation]); h.unmount();
});
