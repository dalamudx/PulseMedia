import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import ts from 'typescript';

const source = readFileSync(new URL('../src/features/configuration/indexerTemplates.ts', import.meta.url), 'utf8');
const compiled = ts.transpileModule(source, { compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.ESNext } }).outputText;
const { applyTemplateToDraft, groupTemplatesByProtocol } = await import(`data:text/javascript;base64,${Buffer.from(compiled).toString('base64')}`);

const sampleProwlarr = {
  id: "prowlarr",
  name: "Prowlarr (Torznab 聚合)",
  domain: "localhost:9696",
  protocol: "torznab",
  default_url: "http://localhost:9696/1/api?v=1",
  recommended_categories: [2000, 5000],
  category_mappings: [
    { id: 2000, name: "电影 (Movies)" },
    { id: 5000, name: "剧集 (TV)" }
  ],
  description: "Prowlarr 聚合器",
  credential_help: "需填写 API Key"
};

const sampleMTeam = {
  id: "mteam",
  name: "M-Team (馒头)",
  domain: "kp.m-team.cc",
  protocol: "nexusphp",
  default_url: "https://kp.m-team.cc/",
  recommended_categories: [401, 402, 403, 404, 405],
  category_mappings: [
    { id: 401, name: "电影" },
    { id: 402, name: "电视剧" },
    { id: 403, name: "综艺" },
    { id: 404, name: "纪录片" },
    { id: 405, name: "动画" }
  ],
  description: "国内老牌综合 PT 站",
  credential_help: "需填写登录 Cookie"
};

const sampleRSS = {
  id: "1337x",
  name: "1337x (RSS)",
  domain: "1337x.to",
  protocol: "rss",
  default_url: "https://1337x.to/rss/featured/",
  recommended_categories: [2000, 5000],
  category_mappings: [{ id: 2000, name: "Movies" }, { id: 5000, name: "TV" }],
  description: "公开 BT RSS",
  credential_help: "无需凭据"
};

test('applyTemplateToDraft populates fields when creating new site', () => {
  const empty = {
    name: "",
    protocol: "torznab",
    url: "",
    credential: "",
    categories: "",
    keepURL: true,
    clearCredential: false
  };

  const applied = applyTemplateToDraft(empty, sampleMTeam, false);
  assert.equal(applied.name, "M-Team (馒头)");
  assert.equal(applied.protocol, "nexusphp");
  assert.equal(applied.url, "https://kp.m-team.cc/");
  assert.equal(applied.categories, "401, 402, 403, 404, 405");
});

test('applyTemplateToDraft preserves URL when editing and keepURL is true', () => {
  const editingDraft = {
    name: "Old Site",
    protocol: "torznab",
    url: "https://custom-existing-domain.org/path",
    categories: "2000",
    keepURL: true
  };

  const applied = applyTemplateToDraft(editingDraft, sampleProwlarr, true);
  assert.equal(applied.name, "Prowlarr (Torznab 聚合)");
  assert.equal(applied.protocol, "torznab");
  assert.equal(applied.url, "https://custom-existing-domain.org/path"); // Preserved!
  assert.equal(applied.categories, "2000, 5000");
});

test('applyTemplateToDraft replaces URL when editing and keepURL is false', () => {
  const editingDraft = {
    name: "Old Site",
    protocol: "torznab",
    url: "https://custom-existing-domain.org/path",
    categories: "2000",
    keepURL: false
  };

  const applied = applyTemplateToDraft(editingDraft, sampleProwlarr, true);
  assert.equal(applied.name, "Prowlarr (Torznab 聚合)");
  assert.equal(applied.protocol, "torznab");
  assert.equal(applied.url, "http://localhost:9696/1/api?v=1"); // Replaced!
  assert.equal(applied.categories, "2000, 5000");
});

test('groupTemplatesByProtocol groups by protocol accurately', () => {
  const templates = [sampleProwlarr, sampleMTeam, sampleRSS];
  const groups = groupTemplatesByProtocol(templates);
  assert.equal(groups.torznab.length, 1);
  assert.equal(groups.torznab[0].id, "prowlarr");
  assert.equal(groups.nexusphp.length, 1);
  assert.equal(groups.nexusphp[0].id, "mteam");
  assert.equal(groups.rss.length, 1);
  assert.equal(groups.rss[0].id, "1337x");
});
