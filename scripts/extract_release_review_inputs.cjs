#!/usr/bin/env node
'use strict'

// Fixed literal-fixture audit only: never run provider code or call PulseMedia.
// Output is unreviewed data, not a golden-output generator or unit authority.
const fs = require('node:fs')
const path = require('node:path')
const crypto = require('node:crypto')
const sources = {
  'rust-ptn': {
    repository: 'IGI-111/torrent-name-parser',
    commit: '6f4f42c2c711c53ed787f535d5b8216e6f47c9c5',
    path: 'src/test.rs',
    source_sha256: '2b600d6f0a051cd651b9ff74a3b9d2e528b62ff755b59e0f53c06294eab47d1d',
    license_sha256: '555de1884458c8c87eb69a5be47724d7b8ea50a5a2ef73864c8bcfe1e40eb36c',
    unique_entries: 92,
  },
  titleparser: {
    repository: 'bhmahler/TorrentTitleParser',
    commit: '604b716c30c3338fbc1d73157f8000056a3b5132',
    path: 'TorrentTitleParser.Tests/TitleTests.cs',
    source_sha256: 'ba1eb5502901f0f369995fc9db6df5827e830042fbab9ce616cad348789b65f3',
    license_sha256: '3009a1211aea38b4e08a403c44cea0002fd531cc1906ed448e387c33c00a1475',
    unique_entries: 27,
  },
}
const stringLiteral = '"(?:[^"\\\\]|\\\\.)*"'
const digest = data => crypto.createHash('sha256').update(data).digest('hex')

function literal(raw) {
  let value = raw.trim()
  if (value === 'None') return null
  if (value.startsWith('Some(') && value.endsWith(')')) value = value.slice(5, -1)
  if (/^\d+$/.test(value)) {
    const number = Number(value)
    if (!Number.isSafeInteger(number)) throw new Error('Unsafe integer fixture')
    return number
  }
  // One inspected Rust fixture escapes apostrophe; this finite language escape
  // does not alter its original release string or infer any parser output.
  return JSON.parse(value.replaceAll('\\\'', '\''))
}

function extract(sourceID, text) {
  const factory = sourceID === 'rust-ptn' ? 'Metadata::from' : 'new\\s+Torrent'
  const constructors = [...text.matchAll(new RegExp(factory + '\\s*\\(', 'g'))]
  const rows = new Map()
  for (let index = 0; index < constructors.length; index++) {
    const start = constructors[index]
    const from = start.index + start[0].length
    const match = text.slice(from).match(new RegExp('^\\s*(' + stringLiteral + ')'))
    if (!match) continue // Nonliteral fixtures require separate manual review.
    const name = literal(match[1])
    const end = constructors[index + 1]?.index ?? text.length
    let segment = text.slice(from + match[0].length, end)
    const prefix = text.slice(Math.max(0, start.index - 120), start.index)
    const assignment = prefix.match(/\b([A-Za-z_]\w*)\s*=\s*$/)
    const labels = {}
    if (assignment) {
      const variable = assignment[1] // Finite identifier regex: safe regex input.
      const reassigned = segment.search(new RegExp('\\b' + variable + '\\s*=(?!=)'))
      if (reassigned >= 0) segment = segment.slice(0, reassigned)
      const pattern = sourceID === 'rust-ptn'
        ? 'assert_eq!\\(\\s*' + variable + '\\.([a-z_]+)\\(\\),\\s*(Some\\((?:' + stringLiteral + '|\\d+)\\)|None|' + stringLiteral + ')\\s*\\);'
        : 'Assert\\.AreEqual\\(\\s*(' + stringLiteral + '|\\d+)\\s*,\\s*' + variable + '\\.([A-Za-z_]+)\\s*\\);'
      for (const assertion of segment.matchAll(new RegExp(pattern, 'g'))) {
        const field = sourceID === 'rust-ptn' ? assertion[1] : assertion[2]
        const parsed = literal(sourceID === 'rust-ptn' ? assertion[2] : assertion[1])
        if (Object.hasOwn(labels, field) && labels[field] !== parsed) {
          throw new Error('Conflicting scalar assertion: ' + name + ': ' + field)
        }
        labels[field] = parsed
      }
    }
    const observation = {
      source_line: text.slice(0, start.index).split('\n').length,
      original_fields: labels,
    }
    if (!rows.has(name)) rows.set(name, [])
    rows.get(name).push(observation)
  }
  return [...rows].map(([release_name, observations]) => ({ release_name, observations }))
}

function main(argv) {
  if (argv.length !== 4 || argv[0] !== '--raw-dir' || argv[2] !== '--output-dir') {
    throw new Error('Usage: extract_release_review_inputs.cjs --raw-dir DIR --output-dir DIR')
  }
  const rawDir = argv[1]
  const outputDir = argv[3]
  fs.mkdirSync(outputDir, { recursive: true })
  for (const [source_id, provenance] of Object.entries(sources)) {
    const raw = fs.readFileSync(path.join(rawDir, source_id + '.source'))
    const notice = fs.readFileSync(path.join(rawDir, source_id + '.LICENSE'))
    if (digest(raw) !== provenance.source_sha256) throw new Error(source_id + ': source bytes changed')
    if (digest(notice) !== provenance.license_sha256) throw new Error(source_id + ': MIT notice changed')
    const entries = extract(source_id, raw.toString('utf8').replace(/^\uFEFF/, ''))
    if (entries.length !== provenance.unique_entries) throw new Error(source_id + ': extraction count changed')
    const data = {
      schema: 'pulse.release-review-input.v1', source_id, provenance, license: 'MIT',
      review_status: 'unreviewed', formal_golden_acceptance: false, coverage_authority: false,
      label_scope: 'Original scalar getters only; episode may be the first member, not full coverage. No confidence, reasons, canonical units or measurements are inferred.',
      entries,
    }
    fs.writeFileSync(path.join(outputDir, source_id + '.json'), JSON.stringify(data, null, 2) + '\n', { flag: 'wx' })
    fs.writeFileSync(path.join(outputDir, source_id + '.LICENSE'), notice, { flag: 'wx' })
    console.log(source_id, entries.length, 'unreviewed; zero execution authority')
  }
}

module.exports = { extract, literal, main }
if (require.main === module) main(process.argv.slice(2))
