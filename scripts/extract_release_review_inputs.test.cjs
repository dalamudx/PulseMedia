const test = require('node:test')
const assert = require('node:assert/strict')
const { extract, literal } = require('./extract_release_review_inputs.cjs')

test('keeps first episode getter as original scalar, not a complete unit set', () => {
  const rows = extract('rust-ptn', 'let m = Metadata::from("series.s01e09e10.mkv").unwrap();\nassert_eq!(m.episode(), Some(9));\nassert_eq!(m.title(), "series");')
  assert.equal(rows.length, 1)
  assert.deepEqual(rows[0].observations[0].original_fields, { episode: 9, title: 'series' })
  assert.equal(Object.hasOwn(rows[0], 'expected'), false)
  assert.equal(Object.hasOwn(rows[0], 'unit_keys'), false)
})

test('does not attach a later nonliteral constructor assertion to old name', () => {
  const text = 'var torrent = new Torrent("Original 2024.mkv");\nAssert.AreEqual(2024, torrent.Year);\ntorrent = new Torrent(input);\nAssert.AreEqual("Changed", torrent.Title);'
  const rows = extract('titleparser', text)
  assert.equal(rows.length, 1)
  assert.deepEqual(rows[0].observations[0].original_fields, { Year: 2024 })
})

test('stops at arbitrary reassignment and distinguishes case variables', () => {
  const text = 'var original = new Torrent("Original 2024.mkv");\nAssert.AreEqual("Original", original.Title);\noriginal = makeTorrent();\nAssert.AreEqual("Changed", original.Title);\nvar other = new Torrent("Other 2023.mkv");\nAssert.AreEqual(2023, other.Year);'
  const rows = extract('titleparser', text)
  assert.deepEqual(rows[0].observations[0].original_fields, { Title: 'Original' })
  assert.deepEqual(rows[1].observations[0].original_fields, { Year: 2023 })
})

test('preserves repeated observations instead of silently merging labels', () => {
  const rows = extract('rust-ptn', 'let m = Metadata::from("series.mkv");\nassert_eq!(m.year(), None);\nlet m = Metadata::from("series.mkv");\nassert_eq!(m.episode(), Some(1));')
  assert.equal(rows.length, 1)
  assert.equal(rows[0].observations.length, 2)
  assert.deepEqual(rows[0].observations[0].original_fields, { year: null })
  assert.deepEqual(rows[0].observations[1].original_fields, { episode: 1 })
  assert.equal(rows[0].observations[1].source_line, 3)
})

test('rejects conflicting scalar assertions rather than choosing one', () => {
  assert.throws(() => extract('rust-ptn', 'let m = Metadata::from("series.mkv");assert_eq!(m.year(), Some(2023));assert_eq!(m.year(), Some(2024));'), /Conflicting scalar/)
})

test('does not execute dynamic fixture expressions or generate missing fields', () => {
  globalThis.__reviewAuditExecuted = false
  const rows = extract('titleparser', 'var ignored = new Torrent((globalThis.__reviewAuditExecuted = true));\nvar item = new Torrent("No title label.mkv");\nAssert.AreEqual(computeTitle(), item.Title);')
  assert.equal(globalThis.__reviewAuditExecuted, false)
  assert.equal(rows.length, 1)
  assert.deepEqual(rows[0].observations[0].original_fields, {})
  delete globalThis.__reviewAuditExecuted
})

test('accepts only finite inspected language escapes and safe integers', () => {
  assert.equal(literal('Some(2024)'), 2024)
  assert.equal(literal('None'), null)
  assert.equal(literal('"Marvel\\\'s"'), "Marvel's")
  assert.throws(() => literal('Some(9007199254740993)'), /Unsafe integer/)
  assert.throws(() => literal('dangerousFunction()'))
})
