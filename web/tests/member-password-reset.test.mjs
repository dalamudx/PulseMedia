import {test} from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import ts from 'typescript';
import React from 'react';

test('member reset restored journal uses GET only and blocks create and other targets',async()=>{
 const h=await host();try{const key=crypto.randomUUID();h.storage.set('pulse.account-security.v1:admin-a:members',JSON.stringify({key,action:'account.member_password.reset:member-b'}));await h.ready();assert.equal(h.calls.length,1);assert(h.button('创建账户').props);await h.submit('创建成员');assert.equal(h.calls.length,1);assert.equal(h.button('重置密码 Active Member').props.disabled,true);h.open('Disabled Member');assert.equal(h.field('member-reset-current').props.value,'');assert.equal(h.field('member-reset-new').props.value,'');const recovered=h.button('读取原命令回执').props.onClick();await h.flush();assert.equal(h.calls[1].method,'GET');assert.equal(h.calls[1].options.params.path.command_key,key);h.reply(1,200,{action:'account.member_password.reset:member-b'});await h.flush();h.reply(2,200,{items:accounts});await recovered;await h.flush();assert.equal(h.storage.size,0);assert(h.calls.every(c=>c.method==='GET'));}finally{await h.close();}
});

test('member reset remains blocked by a restored create command',async()=>{
 const h=await host();try{h.storage.set('pulse.account-security.v1:admin-a:members',JSON.stringify({key:crypto.randomUUID(),action:'account.create'}));await h.ready();assert.equal(h.button('重置密码 Active Member').props.disabled,true);h.open();h.fill();await h.submit('重置成员密码：');assert.equal(h.calls.length,1);}finally{await h.close();}
});

const accounts = [
 {id:'admin-a',username:'My Admin',role:'admin',is_active:true},
 {id:'admin-b',username:'Other Admin',role:'admin',is_active:true},
 {id:'member-a',username:'Active Member',role:'member',is_active:true},
 {id:'member-b',username:'Disabled Member',role:'member',is_active:false},
];
async function host() {
 let slots=[],effects=[],cursor=0,tree,key,user={id:'admin-a',role:'admin'};
 const calls=[],storage=new Map(),writes=[];
 const originals={localStorage:globalThis.localStorage,window:globalThis.window};
 globalThis.localStorage={getItem:k=>storage.get(k)??null,setItem:(k,v)=>{storage.set(k,v);writes.push({key:k,value:v});},removeItem:k=>storage.delete(k)};
 globalThis.window={addEventListener(){},removeEventListener(){}};
 const equal=(a,b)=>a&&b&&a.length===b.length&&a.every((v,i)=>Object.is(v,b[i]));
 const request=method=>(path,options)=>new Promise((resolve,reject)=>calls.push({method,path,options,resolve,reject,settled:false}));
 const h={React,api:{GET:request('GET'),PUT:request('PUT'),POST:request('POST')},useAuth:()=>({user,checkStatus:async()=>{}}),
  useRef(v){const i=cursor++;return slots[i]??={current:v};},
  useState(v){const i=cursor++;if(!(i in slots))slots[i]=typeof v==='function'?v():v;const owned=slots;return [slots[i],next=>{owned[i]=typeof next==='function'?next(owned[i]):next;}];},
  useEffect(fn,deps){const i=cursor++,old=slots[i];if(!equal(old?.deps,deps))effects.push(()=>{old?.cleanup?.();slots[i]={deps,cleanup:fn()};});}};
 async function load(file,symbol){
  const source=readFileSync(new URL(`../src/features/auth/${file}`,import.meta.url),'utf8');
  const ast=ts.createSourceFile(file,source,ts.ScriptTarget.Latest,true,ts.ScriptKind.TSX);let clean=source;
  for(const node of [...ast.statements.filter(ts.isImportDeclaration)].reverse())clean=clean.slice(0,node.getFullStart())+clean.slice(node.end);
  const name=`__member_reset_${crypto.randomUUID().replaceAll('-','')}`;globalThis[name]=h;
  try{const compiled=ts.transpileModule(`const {${Object.keys(h).join(',')}}=globalThis[${JSON.stringify(name)}];\n${clean}`,{compilerOptions:{target:ts.ScriptTarget.ES2022,module:ts.ModuleKind.ESNext,jsx:ts.JsxEmit.React}}).outputText;
   return (await import(`data:text/javascript;base64,${Buffer.from(compiled).toString('base64')}`))[symbol];
  }finally{delete globalThis[name];}
 }
 h.useAccountSecurityCommand=await load('useAccountSecurityCommand.ts','useAccountSecurityCommand');
 const panel=await load('AccountManagementPanel.tsx','AccountManagementPanel');
 const cleanup=()=>{for(const slot of slots)slot?.cleanup?.();slots=[];effects=[];};
 const render=()=>{const outer=panel({});const next=outer?.key??null;if(key!==next){cleanup();key=next;}cursor=0;tree=outer?outer.type(outer.props):null;for(const effect of effects.splice(0))effect();return tree;};
 const flatten=node=>Array.isArray(node)?node.flatMap(flatten):node&&typeof node==='object'&&node.props?[node,...flatten(node.props.children)]:[];
 const text=node=>Array.isArray(node)?node.map(text).join(''):node&&typeof node==='object'?text(node.props?.children):String(node??'');
 const all=predicate=>flatten(tree).filter(predicate);
 const find=predicate=>{const node=all(predicate)[0];assert(node,'element exists');return node;};
 const flush=async()=>{for(let i=0;i<20;i++)await Promise.resolve();render();};
 const button=name=>find(n=>n.type==='button'&&text(n)===name);
 const field=id=>find(n=>n.props.id===id);
 const change=(id,value)=>{field(id).props.onChange({target:{value}});render();};
 const submit=label=>find(n=>n.type==='form'&&text(n).includes(label)).props.onSubmit({preventDefault(){}});
 const reply=(index,status,data)=>{const c=calls[index];assert(c&&!c.settled);c.settled=true;c.resolve({response:{status},data:status<400?data:undefined,error:status>=400?{code:'FIXTURE_ERROR'}:undefined});};
 return {calls,storage,writes,render,flush,all,text,button,field,change,submit,reply,
  auth(next){user=next;render();render();},
  fail(index){calls[index].settled=true;calls[index].reject(Error('fixture lost reply'));},
  async ready(){render();render();reply(0,200,{items:accounts});await flush();},
  open(name='Active Member'){button(`重置密码 ${name}`).props.onClick();render();},
  fill(mode='otp'){change('member-reset-current','AdminPassword123!');change('member-reset-new','MemberPassword456!');change('member-reset-mode',mode);change('member-reset-factor',mode==='otp'?'123456':'recovery-fixture');},
  async close(){cleanup();for(const c of calls)if(!c.settled){c.settled=true;c.reject(Error('fixture cleanup'));}for(let i=0;i<20;i++)await Promise.resolve();Object.assign(globalThis,originals);},
 };
}

test('member reset targets active and disabled members only; never self or another admin',async()=>{
 const h=await host();try{await h.ready();const buttons=h.all(n=>n.type==='button'&&h.text(n).startsWith('重置密码 '));assert.deepEqual(buttons.map(h.text),['重置密码 Active Member','重置密码 Disabled Member']);h.open('Disabled Member');h.fill('recovery_code');const sent=h.submit('重置成员密码：');await h.flush();const c=h.calls[1];assert.equal(c.method,'POST');assert.equal(c.path,'/api/v1/accounts/{id}/password');assert.equal(c.options.params.path.id,'member-b');assert.deepEqual(c.options.body,{current_password:'AdminPassword123!',new_password:'MemberPassword456!',recovery_code:'recovery-fixture'});assert(!('is_active' in c.options.body));assert.equal(h.field('member-reset-current').props.value,'');assert.equal(h.field('member-reset-new').props.value,'');assert.equal(h.field('member-reset-factor').props.value,'');h.reply(1,200,{account_id:'member-b'});await h.flush();h.reply(2,200,{items:accounts});await sent;await h.flush();assert.equal(h.all(n=>n.props.id==='member-reset-new').length,0);assert(h.all(n=>n.props.role==='status').some(n=>h.text(n).includes('双因素与停用状态保持不变')));assert.equal(h.storage.size,0);}finally{await h.close();}
});

test('member reset lost reply keeps original key, recovers by GET only, and blocks other commands',async()=>{
 const h=await host();try{await h.ready();h.open();h.fill();const sent=h.submit('重置成员密码：');await h.flush();const first=h.calls[1];const journal=JSON.parse([...h.storage.values()][0]);assert.equal(journal.action,'account.member_password.reset:member-a');assert.deepEqual(Object.keys(journal).sort(),['action','key']);h.fail(1);await sent;await h.flush();assert.equal(h.calls.filter(c=>c.method==='POST').length,1);await h.submit('重置成员密码：');assert.equal(h.calls.length,2);
 assert.equal(h.button('管理 Active Member').props.disabled,true);assert.equal(h.button('重置密码 Disabled Member').props.disabled,true);assert(h.all(n=>n.type==='fieldset'&&n.props.disabled).length>=1);await h.submit('创建成员');assert.equal(h.calls.length,2);
 const recovery=h.button('读取原命令回执').props.onClick();await h.flush();assert.equal(h.calls[2].method,'GET');assert.equal(h.calls[2].path,'/api/v1/auth/security/commands/{command_key}');assert.equal(h.calls[2].options.params.path.command_key,first.options.params.header['Idempotency-Key']);h.reply(2,404);await recovery;await h.flush();assert.equal(h.storage.size,1);
 h.fill();const retry=h.submit('重置成员密码：');await h.flush();assert.equal(h.calls[3].options.params.header['Idempotency-Key'],first.options.params.header['Idempotency-Key']);assert.deepEqual(h.calls[3].options.body,first.options.body);h.fail(3);await retry;await h.flush();const recovered=h.button('读取原命令回执').props.onClick();await h.flush();h.reply(4,200,{action:journal.action});await h.flush();h.reply(5,200,{items:accounts});await recovered;await h.flush();assert.equal(h.storage.size,0);assert.equal(h.calls.filter(c=>c.method==='POST').length,2);
 for(const write of h.writes){assert(!/AdminPassword|MemberPassword|123456|recovery-fixture/.test(write.value));}
 }finally{await h.close();}
});

test('member reset binds the target before send and clears secrets on switching and closing',async()=>{
 const h=await host();try{await h.ready();h.open();h.fill();h.open('Disabled Member');for(const id of ['member-reset-current','member-reset-new','member-reset-factor'])assert.equal(h.field(id).props.value,'');h.fill();const sent=h.submit('重置成员密码：');await h.flush();assert.equal(h.calls[1].options.params.path.id,'member-b');assert.equal(h.button('重置密码 Active Member').props.disabled,true);h.fail(1);await sent;await h.flush();h.button('取消密码重置').props.onClick();h.render();h.open('Disabled Member');assert.equal(h.field('member-reset-new').props.value,'');assert.equal(h.calls.length,2);assert.equal(h.button('使用原键提交密码重置').props.type,'submit');}finally{await h.close();}
});

test('member reset account switch discards late success and retains original account journal',async()=>{
 const h=await host();try{await h.ready();h.open();h.fill();const sent=h.submit('重置成员密码：');await h.flush();h.auth({id:'admin-b',role:'admin'});assert.equal(h.calls.length,3);h.reply(1,200,{account_id:'member-a'});await sent;await h.flush();assert.equal(h.calls.length,3);h.reply(2,200,{items:accounts});await h.flush();assert.equal(h.all(n=>n.props.id==='member-reset-current').length,0);assert(!h.all(n=>n.props.role==='status').some(n=>h.text(n).includes('成员密码已重置')));assert(h.storage.has('pulse.account-security.v1:admin-a:members'));h.open();assert.equal(h.field('member-reset-new').props.value,'');}finally{await h.close();}
});

test('member reset role loss clears form and ignores late replies even for the same account',async()=>{
 const h=await host();try{await h.ready();h.open();h.fill();const sent=h.submit('重置成员密码：');await h.flush();h.auth({id:'admin-a',role:'member'});assert.equal(h.all(()=>true).length,0);h.reply(1,200,{account_id:'member-a'});await sent;await h.flush();assert.equal(h.calls.length,2);h.auth({id:'admin-a',role:'admin'});assert.equal(h.calls.length,3);h.reply(2,200,{items:accounts});await h.flush();h.open();assert.equal(h.field('member-reset-new').props.value,'');assert.equal(h.button('使用原键提交密码重置').props.type,'submit');}finally{await h.close();}
});

test('member reset shares update journal conflicts, including restored pending commands',async()=>{
 const h=await host();try{h.storage.set('pulse.account-security.v1:admin-a:members',JSON.stringify({key:crypto.randomUUID(),action:'account.update:member-a'}));await h.ready();assert.equal(h.button('重置密码 Active Member').props.disabled,true);h.button('管理 Active Member').props.onClick();h.render();h.open();h.fill();await h.submit('重置成员密码：');assert.equal(h.calls.length,1);assert(h.all(n=>n.type==='fieldset'&&n.props.disabled).length>=2);}finally{await h.close();}
});
