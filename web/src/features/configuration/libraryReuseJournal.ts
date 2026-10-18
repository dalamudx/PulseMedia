import type {paths} from '@/api/client';
export type ReuseCommand=paths['/api/v1/library/reuses']['post']['requestBody']['content']['application/json'];
export type ReuseReceipt=paths['/api/v1/library/reuses']['post']['responses']['202']['content']['application/json'];
export type ReuseJournal={version:1;account:string;subscription:string;key:string;body:ReuseCommand;receipt?:ReuseReceipt;rejected?:400|409};
const uuid=/^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;
const id=(v:unknown):v is string=>typeof v==='string'&&uuid.test(v)&&!/^0{8}-0{4}-0{4}-0{4}-0{12}$/.test(v);
export function validReceipt(value:unknown):value is ReuseReceipt {if(!value||typeof value!=='object')return false;const r=value as ReuseReceipt;return id(r.plan_id)&&id(r.job_id)&&r.state==='queued'}
export function journalName(account:string,subscription:string){return `pulse.library-reuse.v1:${account}:${subscription}`}
export function readJournal(account:string,subscription:string):ReuseJournal|null {
 const raw=localStorage.getItem(journalName(account,subscription));if(raw===null)return null;
 return validateJournal(JSON.parse(raw),account,subscription);
}
function validateJournal(value:unknown,account:string,subscription:string):ReuseJournal{
 const j=value as ReuseJournal,c=j?.body,t=c?.target;
 if(j?.version!==1||j.account!==account||j.subscription!==subscription||!id(account)||!id(subscription)||!id(j.key)||!c||!t||t.subscription_id!==subscription||!id(t.unit_id)||!id(t.root_id)||typeof t.policy_id!=='string'||!t.policy_id||!Number.isSafeInteger(t.policy_revision)||t.policy_revision<1||!id(c.source_file_id)||!Array.isArray(c.confirmed_unit_ids)||!c.confirmed_unit_ids.length||c.confirmed_unit_ids.length>20||!c.confirmed_unit_ids.every(id)||new Set(c.confirmed_unit_ids).size!==c.confirmed_unit_ids.length||!c.confirmed_unit_ids.includes(t.unit_id)||!['hardlink','copy','reflink'].includes(c.action)||typeof c.target_rel!=='string'||!c.target_rel||c.target_rel.length>1024||/[\\\u0000-\u001f\u007f]/.test(c.target_rel)||c.target_rel.split('/').some(p=>!p||p==='.'||p==='..')||!/^[0-9a-f]{64}$/.test(c.input_stamp)||c.confirmed!==true||(j.receipt!==undefined&&!validReceipt(j.receipt))||(j.rejected!==undefined&&j.rejected!==400&&j.rejected!==409)||(j.rejected!==undefined&&j.receipt!==undefined))throw new Error('journal invalid');
 return j;
}
export function saveJournal(j:ReuseJournal){validateJournal(j,j.account,j.subscription);const name=journalName(j.account,j.subscription),raw=JSON.stringify(j);localStorage.setItem(name,raw);if(localStorage.getItem(name)!==raw)throw new Error('journal not durable');readJournal(j.account,j.subscription)}
export async function withReuseLock<T>(account:string,subscription:string,signal:AbortSignal,work:()=>Promise<T>):Promise<T>{if(!navigator.locks?.request)throw new Error('locks unavailable');return navigator.locks.request(journalName(account,subscription),{mode:'exclusive',signal},async()=>{if(signal.aborted)throw new Error('aborted');return work()})}
