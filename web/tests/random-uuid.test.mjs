import {test} from 'node:test';
import assert from 'node:assert/strict';
import {webcrypto} from 'node:crypto';
import {readFileSync} from 'node:fs';
import {stripTypeScriptTypes} from 'node:module';
const source=readFileSync(new URL('../src/platform/randomUUID.ts',import.meta.url),'utf8');
const {ensureRandomUUID}=await import(`data:text/javascript,${encodeURIComponent(stripTypeScriptTypes(source))}`);

test('native randomUUID remains unchanged',()=>{
 const native=()=>webcrypto.randomUUID();const provider={randomUUID:native};ensureRandomUUID(provider);assert.equal(provider.randomUUID,native);
});
test('HTTP fallback uses Web Crypto and produces distinct canonical v4 UUIDs',()=>{
 const provider={getRandomValues:webcrypto.getRandomValues.bind(webcrypto)};ensureRandomUUID(provider);
 const ids=Array.from({length:512},()=>provider.randomUUID());assert.equal(new Set(ids).size,ids.length);
 for(const id of ids)assert.match(id,/^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/);
});
test('fallback sets UUID version and variant bits even for zero entropy bytes',()=>{
 let calls=0;const provider={getRandomValues(bytes){calls++;return bytes.fill(0);}};ensureRandomUUID(provider);
 assert.equal(provider.randomUUID(),'00000000-0000-4000-8000-000000000000');assert.equal(calls,1);
});
test('entropy failure never falls back to a weak request key',()=>{
 const provider={getRandomValues(){throw new Error('owned entropy failure');}};ensureRandomUUID(provider);
 assert.throws(()=>provider.randomUUID(),/owned entropy failure/);
});
