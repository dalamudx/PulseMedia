import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import ts from 'typescript';
import React from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { QRCodeSVG } from 'qrcode.react';
import jsQR from 'jsqr';

const start = Date.parse('2030-01-01T00:00:00Z');
const secret = 'ABCDEFGHIJKLMNOPQRSTUVWXYZ234567';
const uri = `otpauth://totp/PulseMedia%3A%E6%B5%8B%E8%AF%95?algorithm=SHA1&digits=6&issuer=PulseMedia&period=30&secret=${secret}`;
const fixture = (patch = {}) => ({ challenge_id: 'challenge-a', expires_at: new Date(start + 600_000).toISOString(), replayed: false, secret, otpauth_url: uri, ...patch });

// Match the existing rendered hook harness; keep the QR component and encoder real.
async function host(setup = fixture()) {
  let slots = [], effects = new Map(), cursor = 0, tree, dirty, mounted = true, now = start, nextTimer = 0;
  const timers = new Map();
  const equal = (a, b) => a && b && a.length === b.length && a.every((v, i) => Object.is(v, b[i]));
  const hooks = {
    React, QRCodeSVG,
    Date: class extends Date { static now() { return now; } },
    setTimeout(fn, delay) { const id = ++nextTimer; timers.set(id, { fn, due: now + delay }); return id; },
    clearTimeout(id) { timers.delete(id); },
    useState(value) {
      const i = cursor++;
      if (!(i in slots)) slots[i] = typeof value === 'function' ? value() : value;
      return [slots[i], next => { assert(mounted, 'no state updates after unmount'); slots[i] = typeof next === 'function' ? next(slots[i]) : next; dirty = true; }];
    },
    useEffect(fn, deps) {
      const i = cursor++, old = slots[i];
      if (!equal(old?.deps, deps)) effects.set(i, () => { old?.cleanup?.(); slots[i] = { deps, cleanup: fn() }; });
    },
  };
  const source = readFileSync(new URL('../src/features/auth/MFAEnrollmentSecret.tsx', import.meta.url), 'utf8');
  const ast = ts.createSourceFile('MFAEnrollmentSecret.tsx', source, ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX);
  let clean = source;
  for (const node of [...ast.statements.filter(ts.isImportDeclaration)].reverse()) clean = clean.slice(0, node.getFullStart()) + clean.slice(node.end);
  const key = `__mfa_enrollment_${crypto.randomUUID().replaceAll('-', '')}`;
  globalThis[key] = hooks;
  let component;
  try {
    const compiled = ts.transpileModule(`const {${Object.keys(hooks).join(',')}}=globalThis[${JSON.stringify(key)}];\n${clean}`, { compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.ESNext, jsx: ts.JsxEmit.React } }).outputText;
    component = (await import(`data:text/javascript;base64,${Buffer.from(compiled).toString('base64')}`)).MFAEnrollmentSecret;
  } finally { delete globalThis[key]; }
  const flatten = node => Array.isArray(node) ? node.flatMap(flatten) : node?.props ? [node, ...flatten(node.props.children)] : [];
  const text = node => Array.isArray(node) ? node.map(text).join('') : node?.props ? text(node.props.children) : String(node ?? '');
  const all = predicate => flatten(tree).filter(predicate);
  const render = () => {
    let passes = 0;
    do { dirty = false; cursor = 0; tree = component({ setup }); assert(++passes < 10, 'render converges'); } while (dirty);
    for (const effect of effects.values()) effect();
    effects.clear();
    return tree;
  };
  const button = name => { const node = all(n => n.type === 'button' && text(n) === name)[0]; assert(node, 'expected button'); return node; };
  const close = () => { for (const slot of slots) slot?.cleanup?.(); mounted = false; tree = null; assert.equal(timers.size, 0, 'timer cleaned'); };
  render();
  return {
    all, button, text, render, close, timers,
    markup: () => renderToStaticMarkup(tree),
    click(name) { button(name).props.onClick(); render(); },
    change(next) { setup = next; render(); },
    jump(ms) { now += ms; },
    advance(ms) { now += ms; for (const [id, timer] of [...timers]) if (timer.due <= now) { timers.delete(id); timer.fn(); } if (mounted) render(); },
  };
}

function assertHidden(h) {
  assert.equal(h.all(n => n.type === QRCodeSVG || n.type === 'input').length, 0);
  assert(!h.markup().includes(secret));
  assert(!h.markup().includes('otpauth:'));
}

// Rasterize only the real encoder's horizontal SVG runs, then independently decode.
function decodeSVG(markup) {
  const modules = Number(markup.match(/viewBox="0 0 (\d+) \d+"/)[1]);
  const path = markup.match(/<path fill="#000000" d="([^"]+)"/)[1];
  const scale = 4, size = modules * scale;
  const rgba = new Uint8ClampedArray(size * size * 4).fill(255);
  const runs = [...path.matchAll(/M(\d+)[ ,](\d+)\s*h(\d+)v1H\d+z/g)];
  assert(runs.length > 0, 'real SVG has module runs');
  for (const run of runs) {
    const [, x, y, width] = run.map(Number);
    assert(x >= 4 && y >= 4 && x + width <= modules - 4 && y < modules - 4, 'four module quiet zone');
    for (let row = y * scale; row < (y + 1) * scale; row++) for (let col = x * scale; col < (x + width) * scale; col++) {
      const i = (row * size + col) * 4; rgba[i] = rgba[i + 1] = rgba[i + 2] = 0;
    }
  }
  const decoded = jsQR(rgba, size, size);
  assert(decoded, 'independent QR decode succeeds');
  return decoded.data;
}

test('MFA enrollment starts hidden and independently toggles the real local SVG and manual field', async () => {
  const h = await host();
  try {
    assertHidden(h);
    assert.equal(h.button('显示绑定二维码').props.type, 'button');
    assert.equal(h.button('显示绑定密钥').props['aria-expanded'], false);
    h.click('显示绑定二维码');
    const qr = h.all(n => n.type === QRCodeSVG)[0];
    assert.equal(qr.props.role, 'img');
    assert.equal(qr.props['aria-label'], '双因素绑定二维码');
    assert.equal(qr.props.size, 256);
    assert.equal(qr.props.marginSize, 4);
    assert.equal(qr.props.bgColor, '#FFFFFF');
    assert.equal(qr.props.fgColor, '#000000');
    assert.equal(decodeSVG(h.markup()), uri);
    assert(!/<(?:img|image)\b|(?:href|src)=/.test(h.markup()));
    assert(!h.markup().includes('otpauth:'));
    h.click('显示绑定密钥');
    const field = h.all(n => n.type === 'input')[0];
    assert.equal(field.props.id, 'mfa-setup-secret');
    assert.equal(field.props.value, secret);
    assert.equal(field.props.readOnly, true);
    assert.equal(field.props.autoComplete, 'off');
    assert(h.all(n => n.type === 'label' && n.props.htmlFor === field.props.id && h.text(n) === '绑定密钥').length);
    h.click('隐藏绑定二维码');
    assert.equal(h.all(n => n.type === QRCodeSVG).length, 0);
    assert.equal(h.all(n => n.type === 'input').length, 1);
    h.click('隐藏绑定密钥');
    assertHidden(h);
  } finally { h.close(); }
});

test('MFA enrollment invalid, duplicate, mismatched and incomplete URI parameters never expose a QR', async t => {
  const invalid = [
    '', 'https://example.invalid/qr', uri.replace('/totp/', '/hotp/'),
    uri.replace('PulseMedia%3A', 'Other%3A'), uri.replace('issuer=PulseMedia', 'issuer=Other'),
    uri.replace('algorithm=SHA1', 'algorithm=SHA256'), uri.replace('digits=6', 'digits=8'), uri.replace('period=30', 'period=60'),
    uri.replace(secret, 'AAAAAAAAAAAAAAAA'), uri.replace('&digits=6', ''),
    uri.replace('PulseMedia%3A', '%ZZ'), uri.replace('totp/', 'totp:99/'),
    uri.replace('totp/', 'user@totp/'), `${uri}#fragment`, `${uri}#`, `${uri}&extra=1`,
    ...['algorithm=SHA1', 'digits=6', 'issuer=PulseMedia', 'period=30', `secret=${secret}`, `se%63ret=${secret}`].map(p => `${uri}&${p}`),
  ];
  for (let i = 0; i < invalid.length; i++) await t.test(`rejected URI case ${i + 1}`, async () => {
    const h = await host(fixture({ otpauth_url: invalid[i] }));
    try {
      assert.equal(h.all(n => n.type === 'button' && h.text(n).includes('二维码')).length, 0);
      h.click('显示绑定密钥');
      assert.equal(h.all(n => n.type === 'input')[0].props.value, secret);
      assert.equal(h.all(n => n.type === QRCodeSVG).length, 0);
    } finally { h.close(); }
  });
});

test('MFA enrollment missing first-response URI keeps manual entry; replay cannot present secrets', async () => {
  const h = await host(fixture({ otpauth_url: undefined }));
  try {
    assertHidden(h);
    h.click('显示绑定密钥');
    assert.equal(h.all(n => n.type === 'input')[0].props.value, secret);
    h.change(fixture({ replayed: true }));
    assertHidden(h);
    assert.equal(h.all(n => n.type === 'button').length, 0);
  } finally { h.close(); }
});

test('MFA enrollment expiry removes revealed QR and manual fields at the exact boundary', async () => {
  const h = await host();
  try {
    h.click('显示绑定二维码'); h.click('显示绑定密钥');
    h.advance(599_999);
    assert.equal(h.all(n => n.type === QRCodeSVG || n.type === 'input').length, 2);
    h.advance(1);
    assertHidden(h);
    assert.equal(h.all(n => n.type === 'button' || n.type === 'label').length, 0);
    assert(h.all(n => n.props.role === 'status').length);
    assert.equal(h.timers.size, 0);
  } finally { h.close(); }
});

test('MFA enrollment expired and invalid expiry fail closed without scheduling timers', async () => {
  for (const expires_at of [undefined, null, 0, '', 'not-a-date', '2030-02-30T00:00:00Z', '2030-01-01', '2030-01-01T99:00:00Z', '2030-01-01T24:00:00Z', new Date(start).toISOString()]) {
    const h = await host(fixture({ expires_at }));
    try { assertHidden(h); assert.equal(h.all(n => n.type === 'button').length, 0); assert.equal(h.timers.size, 0); }
    finally { h.close(); }
  }
});

test('MFA enrollment delayed expiry timer cannot authorize a reveal', async () => {
  const h = await host();
  try { h.jump(600_000); h.click('显示绑定二维码'); assertHidden(h); assert.equal(h.all(n => n.type === 'button').length, 0); }
  finally { h.close(); }
});

test('MFA enrollment changed secret, URI, expiry, or response resets visibility and timer before paint', async () => {
  const h = await host();
  try {
    for (const patch of [
      { secret: 'AAAAAAAAAAAAAAAA', otpauth_url: uri.replace(secret, 'AAAAAAAAAAAAAAAA') },
      { otpauth_url: uri.replace('%E6%B5%8B%E8%AF%95', 'another') },
      { expires_at: new Date(start + 300_000).toISOString() },
      { challenge_id: 'challenge-b' }, {},
    ]) {
      h.click('显示绑定二维码'); h.click('显示绑定密钥');
      const oldTimer = [...h.timers.keys()][0];
      h.change(fixture(patch));
      assertHidden(h);
      assert.equal(h.button('显示绑定密钥').props['aria-expanded'], false);
      assert.equal(h.button('显示绑定二维码').props['aria-expanded'], false);
      assert.equal(h.timers.has(oldTimer), false);
      assert.equal(h.timers.size, 1);
    }
    h.change(fixture({ expires_at: 'invalid' }));
    assertHidden(h); assert.equal(h.timers.size, 0);
  } finally { h.close(); }
});

test('MFA enrollment unmount clears the timer and remount never restores revealed state', async () => {
  const h = await host();
  h.click('显示绑定二维码'); h.click('显示绑定密钥');
  h.close();
  h.advance(600_000);
  const fresh = await host();
  try { assertHidden(fresh); } finally { fresh.close(); }
});
