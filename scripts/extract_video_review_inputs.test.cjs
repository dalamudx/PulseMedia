const assert=require('node:assert/strict')
const test=require('node:test')
const fs=require('node:fs')
const path=require('node:path')
const {extract,corpus}=require('./extract_video_review_inputs.cjs')
function cases(row){return extract(Buffer.from(`import {it,expect} from 'vitest';const cases=[${row}];it.each(cases)('x',()=>{});`),'test/title.spec.ts')}
test('literal title/year and unresolved enum refs stay original, never execution authority',()=>{
 const got=cases(`['Movie 2020',{title:'Movie',year:'2020',sources:[Source.WEBDL],revision:{version:1,real:0},audioCodec:AudioCodec.EAC3}]`)
 assert.equal(got.skipped.length,0);assert.equal(got.observations[0].original_fields.year,'2020')
 assert.deepEqual(got.observations[0].original_fields.sources,[{symbol_ref:'Source.WEBDL'}])
 assert.deepEqual(got.observations[0].original_fields.audioCodec,{symbol_ref:'AudioCodec.EAC3'})
 assert.equal(got.observations[0].source_line,1)
})
test('calls, interpolation, spread, shorthand, unknown refs and prototype keys cannot be evaluated',()=>{
 for(const expected of [`sideEffect()`,`\`year\${sideEffect()}\``,`{...value}`,`{title}`,`Other.Allowed`,`{__proto__:{authority:true}}`,`{title:'A',title:'B'}`]){
  const got=cases(`['Movie 2020',${expected}]`);assert.equal(got.observations.length,0);assert.equal(got.skipped.length,1)
 }
 assert.equal(global.__pulse_source_executed,undefined)
 const got=cases(`['Movie 2020',(()=>{global.__pulse_source_executed=true;return {title:'Movie'}})()]`)
 assert.equal(got.observations.length,0);assert.equal(global.__pulse_source_executed,undefined)
})
test('malformed UTF8 stays in pinned bytes but never becomes replacement input',()=>{
 const bytes=Buffer.concat([Buffer.from(`const cases=[['Bad`),Buffer.from([0xA0]),Buffer.from(`',{title:'Bad'}],['Good',{title:'Good'}]];cases.forEach(()=>{});`)])
 const got=extract(bytes,'test/title.spec.ts');assert.deepEqual(got.observations.map(x=>x.release_name),['Good']);assert.equal(got.skipped.length,1)
})
test('unused arrays are not counted; getter scalars are partial assertions only',()=>{
 assert.equal(extract(Buffer.from(`const cases=[['unused','value']];`),'test/title.spec.ts').observations.length,0)
 const got=cases(`['Show S01',true]`);assert.equal(got.observations[0].original_fields.expected,true)
 for(const name of ['https://example.test/file?passkey=protected','Movie api_key=protected'])assert.equal(cases(`[${JSON.stringify(name)},false]`).observations.length,0)
})

test('scoped duplicate bindings and symbolic prototype access cannot select a different fixture',()=>{
 const raw=`const cases=[['First',{title:'First'}]];cases.forEach(()=>{});function nested(){const cases=[['Second',{title:'Second'}]];cases.forEach(()=>{});}`
 const got=extract(Buffer.from(raw),'test/title.spec.ts');assert.equal(got.observations.length,0);assert.equal(got.skipped[0].reason,'ambiguous_table_binding')
 assert.equal(cases(`['Bad',{sources:[Source.constructor]}]`).observations.length,0)
})
test('changing source, manifest or license cannot be re-signed by updating the output',()=>{
 const root=path.join(__dirname,'../testdata/releases/review-inputs/video-filename-parser-original-assertions.sources'),read=fs.readFileSync
 for(const file of ['source-manifest.json','LICENSE','title.spec.ts.source']){
  try{fs.readFileSync=function(p,...args){const b=read.call(fs,p,...args);return path.basename(String(p))===file?Buffer.concat([Buffer.isBuffer(b)?b:Buffer.from(b),Buffer.from(' ')]):b};assert.throws(()=>corpus(root),/manifest_hash_mismatch|license_mismatch|source_hash_mismatch/)}finally{fs.readFileSync=read}
 }
})

test('checked-in output reproduces pinned files/license exactly, still not golden',()=>{
 const root=path.join(__dirname,'../testdata/releases/review-inputs/video-filename-parser-original-assertions.sources')
 const got=corpus(root),stored=JSON.parse(fs.readFileSync(path.join(root,'../video-filename-parser-original-assertions.json')))
 assert.equal(JSON.stringify(got,null,2)+'\n',fs.readFileSync(path.join(root,'../video-filename-parser-original-assertions.json'),'utf8'));assert.equal(got.review_status,'unreviewed');assert.equal(got.formal_golden_acceptance,false);assert.equal(got.coverage_authority,false)
 assert.match(got.label_scope,/not full coverage/);assert.equal(got.unique_entries,got.entries.length)
 assert.equal(new Set(got.entries.map(x=>x.release_name)).size,got.entries.length)
 for(const e of got.entries)for(const o of e.observations){assert.match(o.source_path,/^test\//);assert.ok(o.source_line>0);assert.ok(o.test_table)}
})
