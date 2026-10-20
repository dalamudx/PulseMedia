import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import ts from 'typescript';

const source = readFileSync(new URL('../src/features/configuration/liveLogs.ts', import.meta.url), 'utf8');
const compiled = ts.transpileModule(source, { compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.ESNext } }).outputText;
const logs = await import(`data:text/javascript;base64,${Buffer.from(compiled).toString('base64')}`);

const record = (level, line = 'msg', at = '2026-10-20T05:00:00Z') => ({ at, level, line });

test('parseLogRecord accepts well formed frames and rejects everything else', () => {
  assert.deepEqual(logs.parseLogRecord(JSON.stringify(record('WARN', 'disk low'))), record('WARN', 'disk low'));
  assert.deepEqual(logs.parseLogRecord(JSON.stringify(record('DEBUG'))), record('DEBUG'));

  const rejected = [
    'not json',
    'null',
    '[]',
    JSON.stringify({ level: 'TRACE', line: 'x' }),
    JSON.stringify({ level: 'INFO' }),
    JSON.stringify({ level: 'INFO', line: '   ' }),
    JSON.stringify({ level: 'INFO', line: 42 }),
    JSON.stringify({ line: 'no level' }),
    '',
  ];
  for (const payload of rejected) {
    assert.equal(logs.parseLogRecord(payload), null, payload);
  }
});

test('appendRecord filters below the minimum and bounds the buffer', () => {
  let buffer = [];
  buffer = logs.appendRecord(buffer, record('DEBUG', 'hidden'), 'INFO');
  assert.deepEqual(buffer, []);
  buffer = logs.appendRecord(buffer, record('INFO', 'kept'), 'INFO');
  buffer = logs.appendRecord(buffer, record('ERROR', 'kept too'), 'INFO');
  assert.deepEqual(buffer.map(r => r.line), ['kept', 'kept too']);

  let full = [];
  for (let i = 0; i < logs.maxLiveLogLines + 25; i++) full = logs.appendRecord(full, record('INFO', `line-${i}`), 'INFO');
  assert.equal(full.length, logs.maxLiveLogLines);
  assert.equal(full.at(-1).line, `line-${logs.maxLiveLogLines + 24}`);
  assert.equal(full[0].line, 'line-25');
});

test('applyMinimumLevel re-filters an existing buffer without reordering it', () => {
  const buffer = [record('DEBUG', 'd'), record('INFO', 'i'), record('WARN', 'w'), record('ERROR', 'e')];
  assert.deepEqual(logs.applyMinimumLevel(buffer, 'INFO').map(r => r.line), ['i', 'w', 'e']);
  assert.deepEqual(logs.applyMinimumLevel(buffer, 'ERROR').map(r => r.line), ['e']);
  assert.deepEqual(logs.applyMinimumLevel(buffer, 'DEBUG').map(r => r.line), ['d', 'i', 'w', 'e']);
});

test('sticksToBottom only holds when the viewer is already near the end', () => {
  assert.equal(logs.sticksToBottom(700, 1000, 300), true);
  assert.equal(logs.sticksToBottom(676, 1000, 300), true);
  assert.equal(logs.sticksToBottom(0, 1000, 300), false);
  assert.equal(logs.sticksToBottom(669, 1000, 300), false);
});

test('level rank orders DEBUG < INFO < WARN < ERROR and stream URL carries the filter', () => {
  const ranks = ['DEBUG', 'INFO', 'WARN', 'ERROR'].map(logs.levelRank);
  assert.deepEqual(ranks, [...ranks].sort((a, b) => a - b));
  assert.equal(new Set(ranks).size, 4);
  assert.equal(logs.logStreamURL('WARN'), '/api/v1/system/logs?level=WARN');
  assert.equal(logs.isLogLevel('error'), false);
  assert.equal(logs.isLogLevel('ERROR'), true);
});

test('formatLogTime renders a local clock string or nothing', () => {
  assert.equal(logs.formatLogTime(''), '');
  assert.equal(logs.formatLogTime('not-a-date'), '');
  assert.notEqual(logs.formatLogTime('2026-10-20T05:00:00Z'), '');
});
