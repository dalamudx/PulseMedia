'use strict';
// Static provenance/data verification only. No upstream module is imported or run.
const fs=require('node:fs'),path=require('node:path'),crypto=require('node:crypto'),assert=require('node:assert/strict');
const dir=__dirname,root=path.resolve(dir,'../../../../');
const hash=b=>crypto.createHash('sha256').update(b).digest('hex');
const manifest=JSON.parse(fs.readFileSync(path.join(dir,'manifest.json')));
const inventoryBytes=fs.readFileSync(path.join(dir,'inventory.json'));
const inventory=JSON.parse(inventoryBytes);
assert.equal(hash(inventoryBytes),manifest.inventory_sha256);
for(const obj of [manifest,inventory,...manifest.sources]){
 assert.equal(obj.review_status,'unreviewed');assert.equal(obj.reviewed_entries,0);
 for(const k of ['accepted','authority','formal_golden_acceptance','coverage_authority','canonical_unit_authorization'])assert.equal(obj[k],false);
}
function decodeLiteral(s){if(s[0]==='"')return JSON.parse(s);return s.slice(1,-1).replace(/\\(u[0-9a-fA-F]{4}|x[0-9a-fA-F]{2}|[\s\S])/g,(_,c)=>c[0]==='u'?String.fromCharCode(parseInt(c.slice(1),16)):c[0]==='x'?String.fromCharCode(parseInt(c.slice(1),16)):({n:'\n',r:'\r',t:'\t',b:'\b',f:'\f',v:'\v'}[c]??c));}
const quote=/(?:"(?:\\.|[^"\\])*"|'(?:\\.|[^'\\])*')/g;
const sources=new Map();
for(const source of manifest.sources){
 assert.match(source.commit,/^[a-f0-9]{40}$/);assert.equal(source.license,'MIT');
 for(const file of source.files){const b=fs.readFileSync(path.join(dir,file.preserved));assert.equal(hash(b),file.sha256);assert.equal(b.length,file.bytes);}
 const license=fs.readFileSync(path.join(dir,source.source_id+'.LICENSE'),'utf8');assert.match(license,/Permission is hereby granted/);assert.match(license,/Copyright/);
 sources.set(source.source_id,fs.readFileSync(path.join(dir,source.source_id+'.test.source'),'utf8').split('\n'));
}
for(const row of inventory.rows){
 const lines=sources.get(row.source_id);assert.ok(lines);assert.equal(row.review_status,'unreviewed');
 for(const [field,lineKey] of [['input','input_line'],['title','title_line']]){
  const values=[...lines[row[lineKey]-1].matchAll(quote)].map(m=>decodeLiteral(m[0]));assert.ok(values.includes(row[field]),row.source_id+':'+row[lineKey]+':'+field);
 }
}
assert.equal(inventory.rows.length,126);assert.equal(new Set(inventory.rows.map(r=>r.input)).size,126);
assert.deepEqual(Object.fromEntries(manifest.sources.map(s=>[s.source_id,inventory.rows.filter(r=>r.source_id===s.source_id).length])),{'hellberg-release':85,'singers-scene-release':14,'husar-movie-title':24,'esparza-dlshow':3});
// Reproduce the frozen baseline without guessing input keys: exact serialized
// tokens and complete decoded JSON values/keys. Full source text also supplies
// a conservative occurrence cross-check independent of a lexical scanner.
let baselineDrift=[];let overlaps=[];
for(const snap of inventory.existing_snapshots){
 const b=fs.readFileSync(path.join(root,snap.path));if(hash(b)!==snap.sha256){baselineDrift.push(snap.path);continue;}
 const text=b.toString('utf8');const vals=new Set();
 const collect=x=>{if(typeof x==='string')vals.add(x);else if(Array.isArray(x))x.forEach(collect);else if(x&&typeof x==='object')for(const[k,v]of Object.entries(x)){vals.add(k);collect(v);}};
 if(snap.path.endsWith('.json')){try{collect(JSON.parse(text));}catch{}}
 if(snap.path.endsWith('.jsonl'))for(const l of text.split('\n')){try{collect(JSON.parse(l));}catch{}}
 for(const row of inventory.rows){const raw=row.input;const tokens=[JSON.stringify(raw),"'"+raw.replace(/\\/g,'\\\\').replace(/'/g,"\\'")+"'"];
 if(vals.has(raw)||tokens.some(t=>text.includes(t))||text.includes(raw))overlaps.push({source:row.source_id,input:raw,file:snap.path});}
}
assert.equal(overlaps.length,0,'Unexpected exact or raw-text baseline overlap');
assert.equal(manifest.upstream_executed,false);assert.equal(manifest.capacity_is_gold,false);
console.log(JSON.stringify({verified:true,preserved_files:manifest.sources.reduce((n,s)=>n+s.files.length,0),literal_title_rows:126,exact_baseline_overlaps:0,baseline_snapshot_files:inventory.existing_snapshots.length,baselineDrift},null,2));
if(baselineDrift.length)process.exitCode=2;
