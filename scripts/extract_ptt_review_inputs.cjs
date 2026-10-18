#!/usr/bin/env node
'use strict'
// Restricted Python literal syntax, not a Python interpreter. Never eval/import
// provider code. Any expression or call skips its entire fixture list.
const fs = require('node:fs')
const path = require('node:path')
const crypto = require('node:crypto')
const COMMIT = '88429bb90acef55673f421f45038878809b1e577'
const digest = bytes => crypto.createHash('sha256').update(bytes).digest('hex')

class LiteralReader {
  constructor(text, index = 0) { this.text = text; this.index = index }
  space() {
    while (this.index < this.text.length) {
      if (/\s/.test(this.text[this.index])) { this.index++; continue }
      if (this.text[this.index] === '#') { while (this.index < this.text.length && this.text[this.index] !== '\n') this.index++; continue }
      break
    }
  }
  take(char) { this.space(); if (this.text[this.index] !== char) throw new Error('Nonliteral fixture'); this.index++ }
  string() {
    const quote = this.text[this.index++]
    if (this.text.slice(this.index, this.index + 2) === quote + quote) throw new Error('Multiline literal needs separate review')
    let result = ''
    while (this.index < this.text.length) {
      const c = this.text[this.index++]
      if (c === quote) return result
      if (c === '\n' || c === '\r') throw new Error('Unterminated literal')
      if (c !== '\\') { result += c; continue }
      const e = this.text[this.index++]
      const escapes = {n:'\n',r:'\r',t:'\t',b:'\b',f:'\f',v:'\v',a:'\x07','\\':'\\',"'":"'",'"':'"'}
      if (Object.hasOwn(escapes, e)) { result += escapes[e]; continue }
      if (e === '\n') continue
      if ('xuU'.includes(e)) {
        const width = e === 'x' ? 2 : e === 'u' ? 4 : 8
        const digits = this.text.slice(this.index, this.index + width)
        if (!new RegExp('^[a-fA-F0-9]{' + width + '}$').test(digits)) throw new Error('Invalid escape')
        const point = Number.parseInt(digits, 16)
        if (point > 0x10ffff || (point >= 0xd800 && point <= 0xdfff)) throw new Error('Unsupported Unicode escape')
        result += String.fromCodePoint(point); this.index += width; continue
      }
      if (/[0-7]/.test(e)) {
        const digits = e + (this.text.slice(this.index).match(/^[0-7]{0,2}/)?.[0] ?? '')
        this.index += digits.length - 1; result += String.fromCodePoint(Number.parseInt(digits, 8)); continue
      }
      // Python retains unknown escapes; do not reinterpret provider strings.
      result += '\\' + e
    }
    throw new Error('Unterminated literal')
  }
  read() {
    this.space(); const start = this.index; const c = this.text[this.index]
    if (c === '"' || c === "'") return {value:this.string(), start}
    if (c === '[' || c === '(') {
      this.index++; const end = c === '[' ? ']' : ')'; const children = []
      this.space()
      while (this.text[this.index] !== end) {
        children.push(this.read()); this.space()
        if (this.text[this.index] === end) break
        this.take(','); this.space()
      }
      this.take(end); return {value:children.map(x => x.value), start, children}
    }
    if (c === '{') {
      this.index++; const value = Object.create(null); this.space()
      while (this.text[this.index] !== '}') {
        const key = this.read().value
        if (typeof key !== 'string' || Object.hasOwn(value, key)) throw new Error('Invalid dictionary key')
        this.take(':'); value[key] = this.read().value; this.space()
        if (this.text[this.index] === '}') break
        this.take(','); this.space()
      }
      this.take('}'); return {value, start}
    }
    for (const [literal, value] of [['True',true],['False',false],['None',null]]) {
      if (this.text.slice(this.index).startsWith(literal) && !/\w/.test(this.text[this.index + literal.length] ?? '')) {
        this.index += literal.length; return {value, start}
      }
    }
    const number = this.text.slice(this.index).match(/^-?(?:\d+(?:\.\d*)?|\.\d+)(?:[eE][+-]?\d+)?/)
    if (number) {
      this.index += number[0].length; const value = Number(number[0])
      if (!Number.isFinite(value) || (Number.isInteger(value) && !Number.isSafeInteger(value))) throw new Error('Unsafe number')
      return {value, start}
    }
    throw new Error('Nonliteral expression')
  }
}

function extract(text, sourcePath) {
  const observations = [], skipped = []
  for (const match of text.matchAll(/^@pytest\.mark\.parametrize\s*\(/gm)) {
    const line = text.slice(0, match.index).split('\n').length
    const reader = new LiteralReader(text, match.index + match[0].length)
    let spec, rows
    try { spec = reader.read().value; reader.take(','); rows = reader.read(); reader.space(); if (![',',')'].includes(text[reader.index])) throw new Error('Expression after literal') }
    catch { skipped.push({path:sourcePath,line,reason:'nonliteral_parametrize'}); continue }
    if (typeof spec !== 'string') continue
    const columns = spec.split(',').map(x => x.trim())
    if (columns[0] !== 'release_name' || columns.length < 2) continue
    if (new Set(columns).size !== columns.length || columns.some(x => !/^[A-Za-z_]\w*$/.test(x))) throw new Error('Invalid original parameter names')
    const fn = text.slice(reader.index).match(/^def\s+(\w+)\s*\(/m)?.[1]
    if (!fn) throw new Error('Missing original test function')
    if (!rows.children) throw new Error('Literal fixture list required')
    for (const row of rows.children) {
      const value = row.value
      if (!Array.isArray(value) || value.length !== columns.length || typeof value[0] !== 'string' || !value[0].trim() || value[0].length > 1024) throw new Error('Invalid original fixture')
      if (/:\/\/|passkey=|apikey=|token=/i.test(value[0])) throw new Error('Sensitive/link fixture')
      observations.push([value[0], {source_path:sourcePath,source_line:text.slice(0,row.start).split('\n').length,test_function:fn,original_fields:Object.fromEntries(columns.slice(1).map((key,i) => [key,value[i+1]]))}])
    }
  }
  return {observations,skipped}
}

function build(root) {
  const rawManifest = fs.readFileSync(path.join(root,'source-manifest.json')), manifest = JSON.parse(rawManifest)
  if (manifest.repository !== 'dreulavelle/PTT' || manifest.commit !== COMMIT) throw new Error('Unreviewed origin')
  const license = fs.readFileSync(path.join(root,'LICENSE'))
  if (digest(license) !== manifest.license_sha256 || !license.includes('MIT License') || !license.includes('Copyright (c) 2024 Spoked')) throw new Error('Original MIT notice mismatch')
  const rows = new Map(), skipped = []
  for (const source of manifest.sources) {
    if (!/^tests\/test_[A-Za-z_]+\.py$/.test(source.path)) throw new Error('Unconfined original source path')
    const data = fs.readFileSync(path.join(root,path.basename(source.path)+'.source'))
    const blob = crypto.createHash('sha1').update(Buffer.concat([Buffer.from('blob '+data.length+'\0'),data])).digest('hex')
    if (digest(data) !== source.sha256 || blob !== source.git_blob_sha1) throw new Error('Immutable original changed')
    const result = extract(data.toString('utf8'),source.path); skipped.push(...result.skipped)
    for (const [name,observation] of result.observations) { if (!rows.has(name)) rows.set(name,[]); rows.get(name).push(observation) }
  }
  return {schema:'pulse.python-literal-fixtures.v1',source_id:'ptt-original-assertions',license:'MIT',provenance:{repository:manifest.repository,commit:COMMIT,manifest_sha256:digest(rawManifest),license_sha256:manifest.license_sha256},review_status:'unreviewed',formal_golden_acceptance:false,coverage_authority:false,label_scope:'Original literal Python test parameters only; not full coverage, measured file facts or PulseMedia output',unique_entries:rows.size,skipped_nonliteral:skipped,entries:[...rows].map(([release_name,observations]) => ({release_name,observations}))}
}

if (require.main === module) {
  const [flag, root, outputFlag, output] = process.argv.slice(2)
  if (flag !== '--raw-dir' || outputFlag !== '--output' || !root || !output || process.argv.length !== 6) throw new Error('Usage: extract_ptt_review_inputs.cjs --raw-dir DIR --output FILE')
  fs.writeFileSync(output,JSON.stringify(build(root),null,2)+'\n',{flag:'wx'})
}
module.exports = {extract,build,LiteralReader}
