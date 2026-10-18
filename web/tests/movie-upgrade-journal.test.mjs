import test from "node:test";
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import ts from "typescript";

const source = await readFile(new URL("../src/features/configuration/movieUpgradeJournal.ts", import.meta.url), "utf8");
const { outputText } = ts.transpileModule(source, {
  compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.ESNext },
});
const api = await import(`data:text/javascript;base64,${Buffer.from(outputText).toString("base64")}`);
const { journalKey, readJournal, persistJournal, runEntry } = api;
const id = n => `00000000-0000-0000-0000-${n.toString(16).padStart(12, "0")}`;
const user = id(1), sub = id(2), digest = "a".repeat(64);
const fresh = () => ({ version: 1, userId: user, subscriptionId: sub });
const entry = (key = id(3)) => ({
  kind: "mode", key, body: { expected_intent: 1, mode: "stop_at_cutoff", confirmed: true }, phase: "pending",
});
const modeReceipt = { subscription_id: sub, intent_version: 2, mode: "stop_at_cutoff" };
function fixture() {
  const values = new Map();
  let tail = Promise.resolve();
  const storage = {
    fail: false, writes: 0, getItem: key => values.get(key) ?? null,
    setItem(key, value) { this.writes++; if (this.fail) throw Error("disk"); values.set(key, value); },
  };
  const locks = { request(_key, work) {
    const result = tail.then(work); tail = result.catch(() => {}); return result;
  } };
  const options = (journal = fresh(), e = entry()) => ({
    storage, locks, journal, entry: e, userId: user, subscriptionId: sub, onJournal() {},
    transport: async () => ({ status: 202, data: modeReceipt }),
  });
  return { values, storage, locks, options };
}
test("rejects malformed graphs, unknown fields, prototypes and copied scopes before transport", async () => {
  const f = fixture(); let calls = 0;
  const bad = [
    { ...fresh(), extra: true }, { ...fresh(), userId: id(90) },
    { ...fresh(), mode: { ...entry(), body: { ...entry().body, extra: true } } },
    { ...fresh(), preparation: { kind: "preparation", key: id(9), phase: "pending",
      body: { operation_id: id(8), confirmed: true } } },
    Object.assign(Object.create({ polluted: true }), fresh()),
    JSON.parse(JSON.stringify(fresh()).replace('"version":1', '"version":1,"__proto__":{}')),
    { ...fresh(), reservation: { kind: "reservation", key: id(9), phase: "pending", body: {
      current_file_id: id(10), candidate_id: id(11), confirmed: true, expected_client_digest: digest,
      expected: { actor_id: id(99), subscription_id: sub, media_id: id(12), unit_id: id(13),
        root_id: id(14), intent_version: 1, policy_id: "default", policy_revision: 1, input_stamp: digest },
    } } },
  ];
  for (const journal of bad) await assert.rejects(runEntry({
    ...f.options(journal), transport: async () => { calls++; throw Error("unexpected"); },
  }));
  assert.equal(calls, 0); assert.equal(f.storage.writes, 0);
  f.values.set(journalKey(user, sub), JSON.stringify({ ...fresh(), subscriptionId: id(99) }));
  assert.throws(() => readJournal(f.storage, user, sub));
});
test("missing locks rejects before persistence or POST", async () => {
  const f = fixture();
  await assert.rejects(runEntry({ ...f.options(), locks: undefined }));
  assert.equal(f.storage.writes, 0);
});
test("403, 500 and lost responses retain original pending key/body; exact replay succeeds", async () => {
  for (const status of [403, 500, null]) {
    const f = fixture(), journal = fresh(), original = entry();
    await assert.rejects(runEntry({ ...f.options(journal, original), transport: async () => {
      if (status === null) throw Error("lost"); return { status, data: null };
    } }));
    assert.deepEqual(readJournal(f.storage, user, sub).mode, original);
    await assert.rejects(runEntry(f.options(fresh(), entry(id(77)))));
    await runEntry({ ...f.options(fresh(), original), transport: async sent => {
      assert.deepEqual(sent, original); return { status: 202, data: modeReceipt };
    } });
  }
});
test("concurrent different keys allow only one transport; saved same-key receipt is historical", async () => {
  const f = fixture(); let calls = 0;
  const transport = async () => { calls++; await Promise.resolve(); return { status: 202, data: modeReceipt }; };
  const results = await Promise.allSettled([id(3), id(4)].map(key =>
    runEntry({ ...f.options(fresh(), entry(key)), transport })));
  assert.equal(results.filter(r => r.status === "fulfilled").length, 1); assert.equal(calls, 1);
  assert.deepEqual(await runEntry({ ...f.options(), transport }), modeReceipt); assert.equal(calls, 1);
});
test("memory closes before failed storage close; repair and subsequent run never POST again", async () => {
  const f = fixture(), journal = fresh(); let calls = 0, known;
  const opts = { ...f.options(journal), transport: async () => { calls++; return { status: 202, data: modeReceipt }; },
    onJournal(closed) { known = closed; assert.equal(journal.mode.phase, "closed"); f.storage.fail = true; } };
  await assert.rejects(runEntry(opts), /disk/); assert.equal(known.mode.phase, "closed");
  assert.equal(readJournal(f.storage, user, sub).mode.phase, "pending");
  f.storage.fail = false; await persistJournal(f.storage, f.locks, known, user, sub);
  await runEntry(opts); assert.equal(calls, 1);
});
test("rejects invalid transport and stored receipt bindings", async () => {
  const f = fixture();
  await assert.rejects(runEntry({ ...f.options(), transport: async () =>
    ({ status: 202, data: { ...modeReceipt, subscription_id: id(99) } }) }));
  const journal = { ...fresh(), mode: { ...entry(), phase: "closed", receipt: { ...modeReceipt, extra: true } } };
  f.values.set(journalKey(user, sub), JSON.stringify(journal));
  assert.throws(() => readJournal(f.storage, user, sub));
});

const operation = id(20), observation = id(21), task = id(22), authorization = id(23), preparation = id(24);
const time = "2026-10-17T20:00:00Z";
const chain = [
  ["reservation", {current_file_id:id(10),candidate_id:id(11),expected_client_digest:digest,confirmed:true,expected:{actor_id:user,subscription_id:sub,media_id:id(12),unit_id:id(13),root_id:id(14),intent_version:2,policy_id:"default",policy_revision:1,input_stamp:digest}}, {operation_id:operation,reservation_id:id(25),status:"reserved_not_executable",execution_allowed:false}],
  ["preparation", {operation_id:operation,confirmed:true}, {operation_id:operation,task_id:task,raw_sha256:digest,status:"prepared_not_sent",execution_allowed:false}],
  ["send", {operation_id:operation,task_id:task,confirmed:true}, {operation_id:operation,task_id:task,command_id:id(26),job_id:id(27),queued_at:time,status:"queued",execution_allowed:false}],
  ["organizationAuthorization", {operation_id:operation,observation_id:observation,input_stamp:digest,confirmed:true}, {operation_id:operation,observation_id:observation,task_id:task,authorization_id:authorization,authorized_at:time,status:"organization_authorized_not_executable",execution_allowed:false}],
  ["organizationPreparation", {operation_id:operation,authorization_id:authorization,confirmed:true}, {operation_id:operation,authorization_id:authorization,task_id:task,preparation_id:preparation,plan_id:id(28),step_id:id(29),prepared_at:time,status:"organization_prepared_not_executable",execution_allowed:false}],
  ["organizationJob", {operation_id:operation,preparation_id:preparation,confirmed:true}, {operation_id:operation,preparation_id:preparation,command_id:id(30),job_id:id(31),queued_at:time,status:"queued",execution_allowed:false}],
  ["organizationRetirement", {operation_id:operation,input_stamp:digest,confirmed:true}, {operation_id:operation,command_id:id(32),queued_at:time,status:"retirement_queued",execution_allowed:false}],
];
test("organization stages retain uncertain commands and restore exact receipts after reload", async () => {
  const f = fixture(); const journal = fresh();
  await runEntry(f.options(journal));
  for (let i=0;i<chain.length;i++) {
    const [kind,body,receipt] = chain[i];
    const e = {kind,key:id(40+i),body,phase:"pending"};
    const options = {...f.options(journal,e),transport:async()=>({status:202,data:receipt})};
    if (kind.startsWith("organization")) {
      await assert.rejects(runEntry({...options,transport:async()=>{throw Error("lost");}}));
      assert.deepEqual(readJournal(f.storage,user,sub)[kind],e);
      await assert.rejects(runEntry({...options,transport:async()=>({status:202,data:{...receipt,operation_id:id(99)}})}));
      assert.equal(readJournal(f.storage,user,sub)[kind].phase,"pending");
    }
    await runEntry(options);
    const reloaded = readJournal(f.storage,user,sub);
    assert.deepEqual(reloaded[kind].receipt,receipt);
    let replayed = 0;
    await runEntry({...options,journal:reloaded,replayClosed:true,transport:async sent=>{replayed++;assert.deepEqual(sent,e);return{status:202,data:receipt};}});
    assert.equal(replayed,1);
  }
});
test("organization graphs reject skipped stages, copied IDs and malformed receipts before POST", async () => {
  const complete = {...fresh(),mode:{...entry(),phase:"closed",receipt:modeReceipt}};
  chain.forEach(([kind,body,receipt],i)=>{complete[kind]={kind,key:id(40+i),body,phase:"closed",receipt};});
  const mutations = [
    j=>{delete j.send;},
    j=>{j.organizationAuthorization.body.operation_id=id(99);j.organizationAuthorization.receipt.operation_id=id(99);},
    j=>{j.organizationPreparation.body.authorization_id=id(99);j.organizationPreparation.receipt.authorization_id=id(99);},
    j=>{j.organizationJob.body.preparation_id=id(99);j.organizationJob.receipt.preparation_id=id(99);},
    j=>{j.organizationAuthorization.receipt.observation_id=id(99);},
    j=>{j.organizationPreparation.receipt.execution_allowed=true;},
    j=>{j.organizationJob.receipt.queued_at="2026-02-30T20:00:00Z";},
    j=>{j.organizationAuthorization.body.input_stamp="invalid";},
    j=>{delete j.organizationJob;},
    j=>{j.organizationRetirement.body.operation_id=id(99);j.organizationRetirement.receipt.operation_id=id(99);},
    j=>{j.organizationRetirement.body.input_stamp="invalid";},
  ];
  for (const mutate of mutations) {
    const f=fixture(),j=structuredClone(complete);mutate(j);let calls=0;
    await assert.rejects(runEntry({...f.options(j),transport:async()=>{calls++;throw Error("unexpected");}}));
    assert.equal(calls,0);assert.equal(f.storage.writes,0);
  }
});

test("replacement mode preserves its request through lost response and rejects an old-mode receipt", async () => {
 const f=fixture();const e={...entry(),body:{...entry().body,mode:"replace_if_better"}};
 const options=f.options(fresh(),e),receipt={...modeReceipt,mode:e.body.mode};
 await assert.rejects(runEntry({...options,transport:async()=>{throw Error("lost response");}}));
 assert.deepEqual(readJournal(f.storage,user,sub).mode,e);
 await assert.rejects(runEntry({...options,transport:async()=>({status:202,data:modeReceipt})}));
 assert.equal(readJournal(f.storage,user,sub).mode.phase,"pending");
 await runEntry({...options,transport:async sent=>{assert.deepEqual(sent.body,e.body);return{status:202,data:receipt};}});
 const reloaded=readJournal(f.storage,user,sub);assert.deepEqual(reloaded.mode.receipt,receipt);
 let calls=0;
 await runEntry({...options,journal:reloaded,replayClosed:true,transport:async sent=>{calls++;assert.deepEqual(sent.body,e.body);assert.equal(sent.key,e.key);return{status:202,data:receipt};}});
 assert.equal(calls,1);
});
