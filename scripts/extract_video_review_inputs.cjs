#!/usr/bin/env node
'use strict'
// Parse syntax only. Never import, compile, eval or call provider test modules.
const fs = require('node:fs')
const path = require('node:path')
const crypto = require('node:crypto')
const ts = require('../web/node_modules/typescript')
const COMMIT = '84fece54bf097ddd2d7980523d00bb46126cf51f'
const REPOSITORY = 'scttcper/video-filename-parser'
const MANIFEST_SHA = '23f289ed8fff3b1e2f2d8f944cade27b8073c9702f09e14205857ed46b448485'
const LICENSE_SHA = 'da337cfa918334bb9d4c84dd8610485b9a4fecd85b6b373028951028deb2b252'
const digest = bytes => crypto.createHash('sha256').update(bytes).digest('hex')
const enumRoots = new Set(['AudioCodec','Channels','Language','QualityModifier','Resolution','Source','VideoCodec'])
function literal(node, source, depth = 0) {
  if (depth > 12 || node.getText(source).includes('\uFFFD')) throw Error('nonliteral_or_invalid_utf8')
  if (ts.isStringLiteral(node) || ts.isNoSubstitutionTemplateLiteral(node)) return node.text
  if (ts.isNumericLiteral(node)) { const n=Number(node.text);if(!Number.isFinite(n)||!Number.isSafeInteger(n))throw Error('unsafe_number');return n }
  if (node.kind === ts.SyntaxKind.TrueKeyword) return true
  if (node.kind === ts.SyntaxKind.FalseKeyword) return false
  if (node.kind === ts.SyntaxKind.NullKeyword) return null
  if (ts.isPrefixUnaryExpression(node) && node.operator === ts.SyntaxKind.MinusToken && ts.isNumericLiteral(node.operand)) return -literal(node.operand,source,depth+1)
  if (ts.isParenthesizedExpression(node)) return literal(node.expression,source,depth+1)
  if (ts.isArrayLiteralExpression(node)) return node.elements.map(x => literal(x,source,depth+1))
  if (ts.isPropertyAccessExpression(node) && ts.isIdentifier(node.expression) && enumRoots.has(node.expression.text) && /^[A-Z][A-Za-z0-9_]*$/.test(node.name.text)) return {symbol_ref:node.getText(source)}
  if (ts.isObjectLiteralExpression(node)) {
    const value=Object.create(null)
    for(const p of node.properties){
      if(!ts.isPropertyAssignment(p)||(!ts.isIdentifier(p.name)&&!ts.isStringLiteral(p.name)))throw Error('nonliteral_object')
      const k=p.name.text
      if(['__proto__','constructor','prototype'].includes(k)||Object.hasOwn(value,k))throw Error('unsafe_object_key')
      value[k]=literal(p.initializer,source,depth+1)
    }
    return value
  }
  throw Error('nonliteral_expression')
}
function extract(bytes, sourcePath) {
  const text=bytes.toString('utf8'),source=ts.createSourceFile(sourcePath,text,ts.ScriptTarget.Latest,true,ts.ScriptKind.TS)
  if(source.parseDiagnostics.length)throw Error('invalid_source_syntax: '+sourcePath)
  const declarations=new Map(), duplicates=new Set(), consumers=new Set(), observations=[],skipped=[]
  function walk(node){
    if(ts.isVariableDeclaration(node)&&ts.isIdentifier(node.name)&&node.initializer){if(declarations.has(node.name.text))duplicates.add(node.name.text);declarations.set(node.name.text,node.initializer)}
    if(ts.isForOfStatement(node)&&ts.isIdentifier(node.expression))consumers.add(node.expression.text)
    if(ts.isCallExpression(node)&&ts.isPropertyAccessExpression(node.expression)){
      const receiver=node.expression.expression,method=node.expression.name.text
      if(method==='forEach'&&ts.isIdentifier(receiver))consumers.add(receiver.text)
      if(method==='each'&&ts.isIdentifier(receiver)&&['it','test','describe'].includes(receiver.text))for(const arg of node.arguments)if(ts.isIdentifier(arg))consumers.add(arg.text)
    }
    ts.forEachChild(node,walk)
  }
  walk(source)
  for(const name of [...consumers].sort()){
    if(duplicates.has(name)){skipped.push({path:sourcePath,line:1,reason:'ambiguous_table_binding'});continue}
    const rows=declarations.get(name)
    if(!rows||!ts.isArrayLiteralExpression(rows))continue
    for(const row of rows.elements){
      if(!ts.isArrayLiteralExpression(row)||row.elements.length!==2||!ts.isStringLiteral(row.elements[0]))continue
      const line=source.getLineAndCharacterOfPosition(row.getStart(source)).line+1
      try{
        const release=literal(row.elements[0],source),expected=literal(row.elements[1],source)
        if(!release.trim()||release.length>1024||/:\/\/|passkey=|api_key=|apikey=|token=/i.test(release))throw Error('unsafe_input')
        const fields=expected && typeof expected==='object' && !Array.isArray(expected) && !Object.hasOwn(expected,'symbol_ref') ? expected : {expected}
        observations.push({release_name:release,source_path:sourcePath,source_line:line,test_table:name,original_fields:fields})
      }catch(e){skipped.push({path:sourcePath,line,reason:e.message})}
    }
  }
  return {observations,skipped}
}
function corpus(root){
  const manifestBytes=fs.readFileSync(path.join(root,'source-manifest.json')),manifest=JSON.parse(manifestBytes)
  if(digest(manifestBytes)!==MANIFEST_SHA)throw Error('manifest_hash_mismatch')
  if(manifest.repository!==REPOSITORY||manifest.commit!==COMMIT||manifest.license_sha256!==LICENSE_SHA)throw Error('unreviewed_source_pin')
  const license=fs.readFileSync(path.join(root,'LICENSE'))
  if(digest(license)!==LICENSE_SHA||!license.toString('utf8').includes('MIT License'))throw Error('license_mismatch')
  const entries=new Map(),skipped=[]
  if(!Array.isArray(manifest.sources)||!manifest.sources.length)throw Error('empty_source_manifest')
  const paths=new Set()
  for(const item of manifest.sources){
    if(!/^test\/[A-Za-z]+\.spec\.ts$/.test(item.path)||item.local_name!==path.basename(item.path)+'.source'||paths.has(item.path))throw Error('unsafe_source_path')
    paths.add(item.path)
    const bytes=fs.readFileSync(path.join(root,item.local_name))
    if(digest(bytes)!==item.sha256)throw Error('source_hash_mismatch: '+item.path)
    const parsed=extract(bytes,item.path);skipped.push(...parsed.skipped)
    for(const {release_name,...observation} of parsed.observations){
      if(!entries.has(release_name))entries.set(release_name,[])
      entries.get(release_name).push(observation)
    }
  }
  return {schema:'pulse.typescript-literal-fixtures.v1',source_id:'video-filename-parser-original-assertions',license:'MIT',provenance:{repository:REPOSITORY,commit:COMMIT,manifest_sha256:digest(manifestBytes),license_sha256:LICENSE_SHA},review_status:'unreviewed',formal_golden_acceptance:false,coverage_authority:false,label_scope:'Original literal TypeScript test assertions and unresolved enum references only; not full coverage, measured file facts or PulseMedia output',unique_entries:entries.size,skipped_nonliteral:skipped,entries:[...entries].map(([release_name,observations])=>({release_name,observations}))}
}
if(require.main===module){const [root]=process.argv.slice(2);if(!root)throw Error('Usage: extract_video_review_inputs.cjs SOURCE_ROOT');process.stdout.write(JSON.stringify(corpus(root),null,2)+'\n')}
module.exports={extract,corpus,COMMIT,REPOSITORY,LICENSE_SHA,MANIFEST_SHA}
