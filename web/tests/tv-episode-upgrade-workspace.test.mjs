import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import ts from 'typescript';
const source = await readFile(new URL('../src/features/configuration/tvEpisodeUpgradeSelection.ts', import.meta.url), 'utf8');
const output = ts.transpileModule(source, { compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.ESNext } }).outputText;
const { selectionKey, parseLocators, saveLocator, singleMappedUnit, sameSubscription } = await import(`data:text/javascript;base64,${Buffer.from(output).toString('base64')}`);
const id = n => `00000000-0000-0000-0000-${n.toString(16).padStart(12, '0')}`;
const sub = { id: id(2), media_id: id(3), root_id: id(4), media_type: 'tv', intent_version: 1 };
const file = { id: id(5), root_id: id(4), rel_path: 'Show/S01E03.mkv', root_name: '剧集库' };
const unit = { id: id(6), media_id: id(3), unit_key: 'S01E03', retired: false, metadata_version: 1 };
const mapping = { file_id: file.id, root_id: file.root_id, rel_path: file.rel_path, media_id: sub.media_id, can_correct: true, hold_reason: '', associations: [{ active: true, unit }] };
const locator = { unitId: unit.id, currentFileId: file.id, candidateId: id(7), rootId: sub.root_id, mediaId: sub.media_id };
test('canonical single active association accepts sparse real unit keys and ignores historical associations', () => {
  assert.deepEqual(singleMappedUnit(sub, file, mapping), unit);
  assert.deepEqual(singleMappedUnit(sub, file, { ...mapping, associations: [...mapping.associations, { active: false, unit: { ...unit, id: id(9), retired: true } }] }), unit);
});
test('multiple active associations, foreign units, retired units, unknown or held files never authorize selection', () => {
  const bad = [
    { associations: [] }, { associations: [...mapping.associations, { active: true, unit: { ...unit, id: id(9) } }] },
    { associations: [...mapping.associations, { active: true, unit: { ...unit, id: id(9), retired: true } }] },
    { associations: [{ active: true, unit: { ...unit, media_id: id(9) } }] },
    { associations: [{ active: true, unit: { ...unit, retired: true } }] },
    { can_correct: false }, { hold_reason: 'file_retired' }, { hold_reason: 'root_unavailable_or_baseline_pending' },
    { root_id: id(9) }, { media_id: id(9) }, { file_id: id(9) }, { rel_path: 'replaced.mkv' },
  ];
  for (const change of bad) assert.equal(singleMappedUnit(sub, file, { ...mapping, ...change }), null, JSON.stringify(change));
  assert.equal(singleMappedUnit(sub, { ...file, root_id: id(9) }, mapping), null);
  assert.equal(singleMappedUnit({ ...sub, media_type: 'movie' }, file, mapping), null);
});
test('subscription identity, media, root and intent changes invalidate fresh selection', () => {
  assert.equal(sameSubscription(sub, { ...sub }), true);
  for (const change of [{ id: id(8) }, { media_id: id(8) }, { root_id: id(8) }, { intent_version: 2 }, { media_type: 'movie' }]) assert.equal(sameSubscription(sub, { ...sub, ...change }), false);
});
test('actor/subscription session keys isolate locators; persistence projects only IDs and preserves older scopes', () => {
  assert.notEqual(selectionKey(id(1), sub.id), selectionKey(id(8), sub.id));
  assert.notEqual(selectionKey(id(1), sub.id), selectionKey(id(1), id(8)));
  const values = new Map(), storage = { getItem: k => values.get(k) ?? null, setItem: (k, v) => values.set(k, v) };
  const key = selectionKey(id(1), sub.id);
  saveLocator(storage, key, { ...locator, title: 'private title', url: 'secret link' });
  assert.deepEqual(parseLocators(storage.getItem(key)), [locator]);
  saveLocator(storage, key, { ...locator, candidateId: id(8) });
  assert.equal(parseLocators(storage.getItem(key)).length, 2);
  saveLocator(storage, key, locator);
  assert.equal(parseLocators(storage.getItem(key)).length, 2);
  assert.deepEqual(parseLocators(storage.getItem(key))[0], locator);
  assert.throws(() => saveLocator({ ...storage, setItem() { throw Error('denied'); } }, key, locator));
});
test('corrupt, oversized, extra-field and non-ID records fail closed without evicting history', () => {
  assert.deepEqual(parseLocators(null), []);
  for (const raw of ['{', '{}', JSON.stringify([{ ...locator, url: 'secret' }]), JSON.stringify([{ ...locator, candidateId: 'typed text' }]), ' '.repeat(32769), JSON.stringify(Array(33).fill(locator))]) assert.throws(() => parseLocators(raw));
  assert.throws(() => selectionKey('bad', sub.id));
  let raw = JSON.stringify(Array.from({ length: 32 }, (_, n) => ({ ...locator, candidateId: id(100 + n) })));
  const original = raw;
  assert.throws(() => saveLocator({ getItem: () => raw, setItem: (_, v) => { raw = v; } }, 'fixture', locator));
  assert.equal(raw, original);
});
