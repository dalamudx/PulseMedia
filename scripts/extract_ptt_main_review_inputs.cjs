'use strict'
// Read only inert Python literals. Never import, evaluate or run provider code.
const fs = require('node:fs')
const path = require('node:path')
const crypto = require('node:crypto')
const {LiteralReader, build: originalBuild} = require('./extract_ptt_review_inputs.cjs')
const digest = b => crypto.createHash('sha256').update(b).digest('hex')
function boundary(text, start) {
  const stack = []; let quote = '', comment = false
  for (let i=start; i<text.length; i++) {
    const c=text[i]
    if (comment) { if(c==='\n') comment=false; continue }
    if (quote) { if(c==='\\') i++; else if(c===quote) quote=''; continue }
    if(c==='#') {comment=true; continue}
    if(c==='"'||c==="'") { if(text.slice(i,i+3)===c.repeat(3)) throw new Error('Triple literal requires separate review'); quote=c;continue }
    if(!stack.length&&(c===','||c===']')) return i
    if('([{'.includes(c)) stack.push(c)
    else if(')]}'.includes(c)) {if(!stack.length||'([{'.indexOf(stack.pop())!==')]}'.indexOf(c)) throw new Error('Unbalanced fixture')}
  }
  throw new Error('Incomplete fixture table')
}
function extractMain(text) {
  const matches=[...text.matchAll(/^@pytest\.mark\.parametrize\s*\(/gm)]
  if(matches.length!==1) throw new Error('Exactly one active main fixture required')
  const reader=new LiteralReader(text,matches[0].index+matches[0][0].length)
  if(reader.read().value!=='release_name, expected_output') throw new Error('Unexpected assertion columns')
  reader.take(','); reader.take('[')
  const observations=[],skipped=[]
  while(true) {
    reader.space(); if(text[reader.index]===']') break
    const start=reader.index, end=boundary(text,start), rowText=text.slice(start,end)
    let value
    try {const row=new LiteralReader(rowText); value=row.read().value;row.space();if(row.index!==rowText.length)throw new Error('Expression after literal')}
    catch {skipped.push({path:'tests/test_main.py',line:text.slice(0,start).split('\n').length,reason:'nonliteral_or_duplicate_dictionary_row'}); reader.index=end; if(text[end]===',')reader.index++;continue}
    if(!Array.isArray(value)||value.length!==2||typeof value[0]!=='string'||!value[0].trim()||value[0].length>1024||!value[1]||Array.isArray(value[1])||typeof value[1]!=='object')throw new Error('Unexpected main fixture row')
    if(/:\/\/|passkey=|apikey=|token=/i.test(value[0]))throw new Error('Sensitive fixture refused')
    observations.push([value[0],{source_path:'tests/test_main.py',source_line:text.slice(0,start).split('\n').length,test_function:'test_random_releases_parse',original_fields:{expected_output:value[1]}}])
    reader.index=end;if(text[end]===',')reader.index++
  }
  reader.take(']');reader.take(')')
  if(!/^\s*def test_random_releases_parse\(parser, release_name, expected_output\):\s*assert parser\.parse\(release_name\) == expected_output\s*(?:#|$)/.test(text.slice(reader.index)))throw new Error('Original full assertion missing')
  return {observations,skipped}
}
function build(root) {
  const existing=originalBuild(root) // Verify every original blob, manifest and MIT notice.
  const manifest=JSON.parse(fs.readFileSync(path.join(root,'source-manifest.json')))
  const src=manifest.sources.find(s=>s.path==='tests/test_main.py')
  if(!src)throw new Error('Pinned main source missing')
  const raw=fs.readFileSync(path.join(root,'test_main.py.source'))
  if(digest(raw)!==src.sha256)throw new Error('Original source changed')
  const {observations,skipped}=extractMain(raw.toString('utf8')), rows=new Map()
  for(const [name,o]of observations){if(!rows.has(name))rows.set(name,[]);rows.get(name).push(o)}
  return {schema:'pulse.python-literal-fixtures.v1',source_id:'ptt-main-original-assertions',license:'MIT',provenance:existing.provenance,review_status:'unreviewed',formal_golden_acceptance:false,coverage_authority:false,label_scope:'Original static full assertion dictionaries only; not full coverage, measured file facts or PulseMedia output',unique_entries:rows.size,skipped_nonliteral:skipped,entries:[...rows].map(([release_name,observations])=>({release_name,observations}))}
}
if(require.main===module){const [flag,root,outflag,out]=process.argv.slice(2);if(flag!=='--raw-dir'||outflag!=='--output'||process.argv.length!==6)throw new Error('Explicit raw directory and output required');fs.writeFileSync(out,JSON.stringify(build(root),null,2)+'\n',{flag:'wx'})}
module.exports={extractMain,build}
