import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import ts from "typescript";
import React from "react";

async function fixture() {
  const slots = [], effects = [], calls = []; let cursor = 0, tree, live = true;
  let user = { id: "account-a", role: "admin" };
  const host = {
    React,
    document: { cookie: "csrf_token=owned-csrf" },
    useAuth: () => ({ user }),
    fetch: (url, options) => new Promise((resolve, reject) => calls.push({ url, options, resolve, reject })),
    useRef(value) { const i = cursor++; return slots[i] ??= { current: value }; },
    useState(value) { const i = cursor++; if (!(i in slots)) slots[i] = value; return [slots[i], next => { assert(live, "late result mutated unmounted state"); slots[i] = typeof next === "function" ? next(slots[i]) : next; }]; },
    useEffect(fn, deps) { const i = cursor++; if (!slots[i] || !deps.every((v, n) => Object.is(v, slots[i].deps[n]))) effects.push(() => { slots[i]?.cleanup?.(); slots[i] = { deps, cleanup: fn() }; }); },
  };
  let source = readFileSync(new URL("../src/features/media-scrape/UntrackedCompanionsPanel.tsx", import.meta.url), "utf8");
  const ast = ts.createSourceFile("panel.tsx", source, ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX);
  for (const node of ast.statements.filter(ts.isImportDeclaration).reverse()) source = source.slice(0, node.getFullStart()) + source.slice(node.end);
  source += "\nexport { Companions as Fixture };";
  const key = `__mp20_${crypto.randomUUID().replaceAll("-", "")}`; globalThis[key] = host;
  let module;
  try {
    source = `const { ${Object.keys(host).join(",")} } = globalThis[${JSON.stringify(key)}];\n` + source;
    const compiled = ts.transpileModule(source, { compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.ESNext, jsx: ts.JsxEmit.React } }).outputText;
    module = await import(`data:text/javascript;base64,${Buffer.from(compiled).toString("base64")}`);
  } finally { delete globalThis[key]; }
  const text = n => typeof n === "string" || typeof n === "number" ? String(n) : React.Children.toArray(n?.props?.children).map(text).join("");
  function walk(n, pred) { if (!n || typeof n !== "object") return null; if (pred(n)) return n; for (const child of React.Children.toArray(n.props?.children)) { const found = walk(child, pred); if (found) return found; } return null; }
  function render() { cursor = 0; tree = module.Fixture({ operationId: "video-operation" }); for (const effect of effects.splice(0)) effect(); }
  async function flush() { for (let i = 0; i < 16; i++) await Promise.resolve(); if (live) render(); }
  const h = {
    calls, render, flush,
    button: label => walk(tree, n => n.type === "button" && text(n).includes(label)),
    find: pred => walk(tree, pred), text: () => text(tree),
    reply(index, data, status = 200) { calls[index].resolve({ ok: status < 400, status, json: async () => data }); },
    fail(index) { calls[index].reject(new Error("private transport detail")); },
    wrapper(next, operationId = "video-operation") { user = next; return module.UntrackedCompanionsPanel({ operationId }); },
    unmount() { for (const slot of slots) slot?.cleanup?.(); live = false; },
  };
  render(); await flush(); return h;
}
const candidate = { key: "a".repeat(64), source_relative_path: "Movie.2026.zh-Hans.srt", target_relative_path: "Movie (2026)/Movie.zh-Hans.srt" };
const item = state => ({ id: "attachment-a", operation_id: "video-operation", candidate, action: "copy", state, source_retained: true });
const view = (...items) => ({ video_state: "completed", items });

test("companions require explicit selection and execution, unknown writes only allow read before observation", async () => {
  const h = await fixture(); assert.equal(h.calls.length, 1); assert.equal(h.calls[0].options.method, "GET");
  h.reply(0, view()); await h.flush();
  const discovery = h.button("检查同目录字幕").props.onClick(); assert.equal(h.calls.length, 2); assert.equal(h.calls[1].options.method, "POST");
  assert.equal(h.calls[1].options.headers["X-CSRF-Token"], "owned-csrf", "bodyless discovery must honor Cookie CSRF");
  h.reply(1, { items: [candidate] }); await discovery; await h.flush();
  assert.equal(h.find(n => n.type === "input").props.checked, false);
  h.find(n => n.type === "input").props.onChange({ target: { checked: true } }); h.render();
  const selecting = h.button("确认所选").props.onClick(); assert.equal(h.calls.length, 3);
  assert.deepEqual(JSON.parse(h.calls[2].options.body), { candidate_keys: [candidate.key], action: "copy" });
  assert.ok(h.calls[2].options.headers["Idempotency-Key"]);
  h.fail(2); await selecting; await h.flush(); assert.equal(h.calls.length, 3); assert.match(h.text(), /不会自动重新提交/);
  assert.equal(h.button("检查同目录字幕").props.disabled, true);
  h.button("读取字幕结果").props.onClick(); h.reply(3, view(item("pending"))); await h.flush();
  h.button("整理此字幕").props.onClick(); assert.equal(h.calls.length, 5); assert.match(h.calls[4].url, /attachment-a\/execute$/);
  h.fail(4); await h.flush(); assert.equal(h.calls.length, 5); assert.match(h.text(), /视频入库结果不变/);
  assert.equal(h.button("整理此字幕").props.disabled, true);
  h.button("读取字幕结果").props.onClick(); h.reply(5, view(item("unknown"))); await h.flush();
  h.button("核对已有结果").props.onClick(); assert.equal(h.calls.length, 7);
  h.reply(6, item("completed")); await h.flush();
  assert.match(h.text(), /视频已入库/); assert.match(h.text(), /字幕已整理，源文件保留/); assert.equal(h.button("核对已有结果"), null);
  h.unmount();
});
test("companions isolate current account and operation and discard late requests", async () => {
  const h = await fixture();
  assert.equal(h.wrapper(null), null); assert.equal(h.wrapper({ id: "member", role: "member" }), null);
  const a = h.wrapper({ id: "a", role: "admin" }), b = h.wrapper({ id: "b", role: "admin" }), other = h.wrapper({ id: "b", role: "admin" }, "other");
  assert.notEqual(a.key, b.key); assert.notEqual(b.key, other.key);
  h.unmount(); assert.equal(h.calls[0].options.signal.aborted, true); h.reply(0, view(item("completed"))); await h.flush();
});
test("manual review and running companions never offer a fresh publication", async () => {
  for (const state of ["review_required", "running"]) {
    const h = await fixture(); h.reply(0, view(item(state))); await h.flush();
    assert.equal(h.button("整理此字幕"), null); assert.equal(h.button("核对已有结果"), null);
    assert.equal(h.calls.length, 1); h.unmount();
  }
});
test("empty companion discovery never marks the video missing", async () => {
  const h = await fixture(); h.reply(0, view()); await h.flush();
  h.button("检查同目录字幕").props.onClick(); h.reply(1, { items: [] }); await h.flush();
  assert.match(h.text(), /这不表示视频缺失/); assert.match(h.text(), /视频已入库/); assert.equal(h.calls.length, 2); h.unmount();
});
