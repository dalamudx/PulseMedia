import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import ts from "typescript";
import React from "react";

const files = {
  seerr: new URL("../src/features/configuration/SeerrSettingsPanel.tsx", import.meta.url),
  subtitle: new URL("../src/features/media-scrape/SubtitleCandidatesPanel.tsx", import.meta.url),
};
async function fixture(kind, props = {}) {
  const slots = [], effects = [], calls = [];
  let cursor = 0, tree, live = true;
  const same = (a, b) => a && b && a.length === b.length && a.every((v, i) => Object.is(v, b[i]));
  const transport = method => (path, options) => new Promise((resolve, reject) => calls.push({ method, path, options, resolve, reject, settled: false }));
  const host = {
    React,
    api: { GET: transport("GET"), PUT: transport("PUT"), POST: transport("POST") },
    useAuth: () => ({ user: { id: "account", role: "admin" } }),
    useRef(value) { const i = cursor++; return slots[i] ??= { current: value }; },
    useState(value) { const i = cursor++; if (!(i in slots)) slots[i] = typeof value === "function" ? value() : value; return [slots[i], next => { assert(live, "unmounted panel mutated state"); slots[i] = typeof next === "function" ? next(slots[i]) : next; }]; },
    useCallback(fn, deps) { const i = cursor++; if (!same(slots[i]?.deps, deps)) slots[i] = { deps, fn }; return slots[i].fn; },
    useEffect(fn, deps) { const i = cursor++, previous = slots[i]; if (!same(previous?.deps, deps)) effects.push(() => { previous?.cleanup?.(); slots[i] = { deps, cleanup: fn() }; }); },
  };
  let source = readFileSync(files[kind], "utf8");
  const ast = ts.createSourceFile("panel.tsx", source, ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX);
  for (const node of [...ast.statements.filter(ts.isImportDeclaration)].reverse()) source = source.slice(0, node.getFullStart()) + source.slice(node.end);
  source += `\nexport { ${kind === "seerr" ? "SeerrSettings" : "Candidates"} as Fixture };`;
  const key = `__mp_panels_${crypto.randomUUID().replaceAll("-", "")}`;
  globalThis[key] = host;
  let module;
  try {
    source = `const { ${Object.keys(host).join(",")} } = globalThis[${JSON.stringify(key)}];\n` + source;
    const compiled = ts.transpileModule(source, { compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.ESNext, jsx: ts.JsxEmit.React } }).outputText;
    module = await import(`data:text/javascript;base64,${Buffer.from(compiled).toString("base64")}`);
  } finally { delete globalThis[key]; }
  function render() { cursor = 0; tree = module.Fixture(kind === "seerr" ? { accountId: "account" } : { fileId: "file-a", ...props }); for (const effect of effects.splice(0)) effect(); return tree; }
  async function flush() { for (let i = 0; i < 16; i++) await Promise.resolve(); if (live) render(); }
  const walk = (node, predicate) => {
    if (!node || typeof node !== "object") return null;
    if (predicate(node)) return node;
    for (const child of React.Children.toArray(node.props?.children)) { const found = walk(child, predicate); if (found) return found; }
    return null;
  };
  const text = node => typeof node === "string" || typeof node === "number" ? String(node) : React.Children.toArray(node?.props?.children).map(text).join("");
  const h = {
    calls, flush, render,
    find: predicate => walk(tree, predicate),
    button: label => walk(tree, n => n.type === "button" && text(n).includes(label)),
    text: () => text(tree),
    reply(index, data, status = 200) { const c = calls[index]; assert(c && !c.settled); c.settled = true; c.resolve({ data: status < 400 ? data : undefined, error: status >= 400 ? { message: "private backend detail" } : undefined, response: { status } }); },
    fail(index) { const c = calls[index]; assert(c && !c.settled); c.settled = true; c.reject(Error("private network detail")); },
    unmount() { for (const slot of slots) slot?.cleanup?.(); live = false; },
  };
  render(); await flush(); return h;
}
const config = { enabled: false, version: 1, owner_id: "bound-owner", credential_present: true };
const state = (outcome, extra = {}) => ({ file_id: "file-a", mapping_revision: 9, outcome, candidates: [], ...extra });

test("Seerr saves frozen owner/version, clears credential and requires reread after unknown response", async () => {
  const h = await fixture("seerr");
  assert.equal(h.calls.length, 2); h.reply(0, config); h.reply(1, { items: [] }); await h.flush();
  assert.equal(h.find(n => n.props?.id === "seerr-owner").props.readOnly, true);
  h.find(n => n.props?.id === "seerr-credential").props.onChange({ target: { value: "dedicated-credential-no-storage-123456" } }); h.render();
  const saving = h.find(n => n.type === "form").props.onSubmit({ preventDefault() {} });
  assert.deepEqual(h.calls[2].options.body, { enabled: false, expected_version: 1, owner_id: "bound-owner", credential: "dedicated-credential-no-storage-123456" });
  h.fail(2); await saving; await h.flush();
  assert.equal(h.find(n => n.props?.id === "seerr-credential").props.value, "");
  assert.equal(h.button("保存 Seerr").props.disabled, true);
  assert(!h.text().includes("private network"));
  h.button("重新读取配置").props.onClick(); h.reply(3, { ...config, version: 2 }); h.reply(4, { items: [] }); await h.flush();
  assert.equal(h.button("保存 Seerr").props.disabled, false); h.unmount();
});

test("Subtitle panel derives current revision from GET and requires separate candidate selection and confirmation", async () => {
  const h = await fixture("subtitle");
  assert.equal(h.calls.length, 1); h.reply(0, state("not_queried")); await h.flush();
  h.button("查询字幕候选").props.onClick();
  assert.equal(h.calls[1].options.body.expected_mapping_revision, 9);
  h.reply(1, state("candidates", { query_id: "query-a", candidates: [{ id: "candidate-a", provider: "shooter", language_hint: "Han", format: "srt" }] })); await h.flush();
  assert.equal(h.calls.filter(c => c.path.endsWith("/confirm")).length, 0);
  assert.equal(h.button("确认安装").props.disabled, true);
  h.find(n => n.type === "input" && n.props.type === "radio").props.onChange(); h.render();
  h.button("确认安装").props.onClick();
  assert.equal(h.calls[2].options.params.path.candidate_id, "candidate-a");
  assert.equal(h.calls[2].options.body.expected_mapping_revision, 9);
  h.fail(2); await h.flush(); assert(h.text().includes("不会自动重试"));
  assert.equal(h.find(n => n.type === "fieldset").props.disabled, true);
  h.button("读取结果").props.onClick(); h.reply(3, state("confirmed")); await h.flush();
  assert(h.text().includes("字幕已安装")); assert.equal(h.button("确认安装"), null); h.unmount();
});

test("Subtitle candidates distinguish precise timing, zero start, multi-day ranges and missing summaries without requests", async () => {
  const h = await fixture("subtitle");
  const candidate = (id, summary) => ({ id, provider: "shooter", language_hint: "Han", format: "srt", ...(summary ? { summary } : {}) });
  h.reply(0, state("candidates", { candidates: [
    candidate("zero", { cue_count: 120, start_ms: 0, end_ms: 3_723_004 }),
    candidate("long", { cue_count: 99999, start_ms: 90_061_007, end_ms: 359_999_999 }),
    candidate("missing"),
  ] }));
  await h.flush();
  assert(h.text().includes("120 条字幕 · 字幕时间范围 00:00:00.000 – 01:02:03.004"));
  assert(h.text().includes("99999 条字幕 · 字幕时间范围 25:01:01.007 – 99:59:59.999"));
  assert(h.text().includes("字幕条数与时间范围不可用"));
  assert(h.text().includes("不代表视频时长或翻译质量"));
  assert.equal(h.calls.length, 1);
  assert.equal(h.button("确认安装").props.disabled, true);
  h.find(n => n.type === "input" && n.props.value === "long").props.onChange(); h.render();
  assert.equal(h.calls.length, 1);
  h.button("确认安装").props.onClick();
  assert.equal(h.calls.length, 2);
  assert.equal(h.calls[1].options.params.path.candidate_id, "long");
  assert.equal(h.calls[1].options.body.expected_mapping_revision, 9);
  h.reply(1, state("confirmed")); await h.flush(); h.unmount();
});

test("Subtitle summaries remain hidden behind the frozen mapping revision fence", async () => {
  const h = await fixture("subtitle", { frozenRevision: 8 });
  h.reply(0, state("candidates", { candidates: [{ id: "old", summary: { cue_count: 12, start_ms: 0, end_ms: 999 } }] }));
  await h.flush();
  assert(h.text().includes("旧文件版本"));
  assert(!h.text().includes("12 条字幕"));
  assert.equal(h.find(n => n.type === "input" && n.props.type === "radio"), null);
  assert.equal(h.calls.length, 1); h.unmount();
});

test("Unmounted panels ignore late configuration and candidate responses", async () => {
  const a = await fixture("seerr"); a.unmount(); a.reply(0, config); a.reply(1, { items: [] }); await a.flush();
  const b = await fixture("subtitle"); b.unmount(); b.reply(0, state("not_queried")); await b.flush();
});
