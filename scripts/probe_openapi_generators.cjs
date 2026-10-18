#!/usr/bin/env node
// Exercise pinned generators with real nullable, UUID and strict-handler artifacts.
const fs=require('node:fs');
const path=require('node:path');
const {spawnSync}=require('node:child_process');
const root=path.resolve(__dirname,'..');
fs.mkdirSync('/data/temp',{recursive:true});
const owned=fs.mkdtempSync('/data/temp/pulse-openapi-probe.');
const env={...process.env,TMPDIR:owned,TMP:owned,TEMP:owned,GOTMPDIR:path.join(owned,'go-build')};
fs.mkdirSync(env.GOTMPDIR);
function run(command,args,cwd=root){const r=spawnSync(command,args,{cwd,env,encoding:'utf8',timeout:60000,maxBuffer:1024*1024});if(r.error||r.status!==0)throw new Error(`${command} failed: ${(r.error?.message||r.stderr||r.stdout).slice(0,1000)}`);return r;}
function spec(version){return {openapi:version,info:{title:'Generator compatibility probe',version:'1.0.0'},paths:{'/api/v1/probe':{get:{operationId:'getProbe',responses:{'200':{description:'Nullable UUID payload',content:{'application/json':{schema:{$ref:'#/components/schemas/ProbeResponse'}}}}}}}},components:{schemas:{ProbeResponse:{type:'object',required:['id','note'],properties:{id:{type:'string',format:'uuid'},note:version==='3.0.3'?{type:'string',nullable:true}:{type:['string','null']}}}}}};}
function probe(version){const dir=path.join(owned,version);fs.mkdirSync(dir);const source=path.join(dir,'probe.json');fs.writeFileSync(source,JSON.stringify(spec(version),null,2));
 let go=false,ts=false,goFailure,tsFailure;
 try {const result=run('oapi-codegen',['-package','probe','-generate','models,std-http-server,strict-server',source]);if(!/StrictServerInterface/.test(result.stdout)||! /Note\s+\*string/.test(result.stdout))throw new Error('strict interface or nullable string missing');fs.writeFileSync(path.join(dir,'api.gen.go'),result.stdout);for(const name of ['go.mod','go.sum'])fs.copyFileSync(path.join(root,name),path.join(dir,name));fs.writeFileSync(path.join(dir,'nullable_test.go'),`package probe\nimport("encoding/json";"testing")\nfunc TestRequiredNullableRoundtrip(t *testing.T){var p ProbeResponse;if err:=json.Unmarshal([]byte(\`{"id":"550e8400-e29b-41d4-a716-446655440000","note":null}\`),&p);err!=nil{t.Fatal(err)};b,err:=json.Marshal(p);if err!=nil{t.Fatal(err)};var fields map[string]any;if err=json.Unmarshal(b,&fields);err!=nil{t.Fatal(err)};if note,ok:=fields["note"];!ok||note!=nil{t.Fatal("required nullable lost")};if fields["id"]!="550e8400-e29b-41d4-a716-446655440000"{t.Fatal("UUID changed")}}\n`);run('go',['test','-mod=readonly','-count=1','./...'],dir);go=true;
 }catch(e){goFailure=e.message;}
 try {run('pnpm',['exec','openapi-typescript',source,'--output',path.join(dir,'schema.d.ts')],path.join(root,'web'));fs.writeFileSync(path.join(dir,'client.ts'),`import type {components,paths} from './schema';\nconst good: components['schemas']['ProbeResponse']={id:'550e8400-e29b-41d4-a716-446655440000',note:null};\n// @ts-expect-error a number must not satisfy a nullable string\nconst bad: components['schemas']['ProbeResponse']={id:'x',note:3};\ntype Response=paths['/api/v1/probe']['get']['responses'][200]['content']['application/json'];\nconst response:Response=good;\n`);run('pnpm',['exec','tsc','--strict','--noEmit','--skipLibCheck','--target','ES2022','--module','ESNext','--moduleResolution','Bundler',path.join(dir,'client.ts')],path.join(root,'web'));ts=true;
 }catch(e){tsFailure=e.message;}
 return {version,goStrictNullableAndUUID:go,tsNullableAndNegativeTypeCheck:ts,...(goFailure?{goFailure}:{}),...(tsFailure?{tsFailure}:{})};}
try {const versions={go:run('oapi-codegen',['--version']).stdout.trim(),ts:run('pnpm',['exec','openapi-typescript','--version'],path.join(root,'web')).stdout.trim()};const results=['3.0.3','3.1.0'].map(probe);console.log(JSON.stringify({versions,selected:'3.0.3',results},null,2));if(!results[0].goStrictNullableAndUUID||!results[0].tsNullableAndNegativeTypeCheck)process.exitCode=1;
}finally{fs.rmSync(owned,{recursive:true,force:true});if(fs.existsSync(owned))throw new Error('owned probe cleanup failed');console.log('OWNED_OPENAPI_PROBE_CLEANUP_VERIFIED');}
