const test = require('node:test')
const assert = require('node:assert/strict')
const fs = require('node:fs')
const path = require('node:path')
const {extract,build} = require('./extract_ptt_review_inputs.cjs')

test('exact independent static AST audit reproduction without Python runtime',()=>{
 const root = path.join(__dirname,'../testdata/releases/review-inputs')
 assert.deepEqual(JSON.parse(JSON.stringify(build(path.join(root,'ptt-original-assertions.sources')))),JSON.parse(fs.readFileSync(path.join(root,'ptt-original-assertions.json'))))
})
test('retains original fields/line/absent facts and never executes body',()=>{
 const {observations,skipped}=extract('@pytest.mark.parametrize("release_name, expected_episode", [\n ("Original.S02E03.mkv", [3]),\n ("Different.S02.mkv", []),\n])\ndef test_episode():\n raise RuntimeError("must never execute")\n','tests/test_episode.py')
 assert.deepEqual(skipped,[]);assert.equal(observations[0][1].source_line,2)
 assert.deepEqual(observations[0][1].original_fields,{expected_episode:[3]})
 assert.deepEqual(observations[1][1].original_fields,{expected_episode:[]})
})
test('skips entire computed list and no dynamic imports/calls execute',()=>{
 for (const expression of ['factory()','[("Original", list(range(1,4)))]',"__import__('os').system('must-not-run')",'[("Original", True)] + factory()']){
  const {observations,skipped}=extract('@pytest.mark.parametrize("release_name, expected", '+expression+')\ndef test_dynamic(): pass\n','tests/test_dynamic.py')
  assert.deepEqual(observations,[]);assert.equal(skipped[0].reason,'nonliteral_parametrize')
 }
})
test('boolean/absence/duplicate observations preserved',()=>{
 const r=extract('@pytest.mark.parametrize("release_name, expected_trash", [("Same", False), ("Same", None)])\ndef test_literal(): pass\n','tests/test_trash.py')
 assert.deepEqual(r.observations.map(x=>x[1].original_fields),[{expected_trash:false},{expected_trash:null}])
})
test('sensitive links refused and non-JSON expressions never become samples',()=>{
 assert.throws(()=>extract('@pytest.mark.parametrize("release_name, expected", [("https://site.invalid/file?passkey=secret", True)])\ndef test_bad():pass','tests/test_bad.py'),/Sensitive/)
 for (const value of ['{1,2}',"b'bytes'"]){assert.equal(extract('@pytest.mark.parametrize("release_name, expected", [("Original", '+value+')])\ndef test_bad():pass','tests/test_bad.py').observations.length,0)}
})
test('unrelated pattern tests do not enter release corpus',()=>{
 assert.deepEqual(extract('@pytest.mark.parametrize("pattern, expected", [("generated", True)])\ndef test_custom():pass','tests/test_custom.py').observations,[])
})
test('commented decorators never enter or alter source audit counts',()=>{
 const r=extract('# @pytest.mark.parametrize("release_name, expected", [("comment-only", True)])\n# def test_comment(): pass\n','tests/test_comment.py')
 assert.deepEqual(r,{observations:[],skipped:[]})
})

