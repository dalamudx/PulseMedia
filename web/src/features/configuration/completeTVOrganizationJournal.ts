import type {paths} from '@/api/client';
export type OrganizationPreview = paths['/api/v1/operations/{id}/complete-tv-organizations']['get']['responses']['200']['content']['application/json']['items'][number];
export type OrganizationReceipt = paths['/api/v1/download-tasks/{id}/complete-tv-organizations']['post']['responses']['202']['content']['application/json'];
export type OrganizationCommand = {task_id:string;operation_id:string;tracking_job_id:string;key:string};
export type OrganizationJournal = {version:1;account:string;operation:string;command:OrganizationCommand;receipt?:OrganizationReceipt};
const uuid=/^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;
const validID=(value:unknown):value is string=>typeof value==='string'&&uuid.test(value)&&value!=='00000000-0000-0000-0000-000000000000';
const record=(value:unknown):value is Record<string,unknown>=>Boolean(value)&&typeof value==='object'&&!Array.isArray(value);
const natural=(value:unknown):value is number=>typeof value==='number'&&Number.isSafeInteger(value)&&value>=0;
function relativePath(value:unknown):value is string{
 return typeof value==='string'&&value.length>0&&value.length<=4096&&!/^[a-z]:/i.test(value)&&!/[\\\u0000-\u001f\u007f]/.test(value)&&value.split('/').every(part=>part!==''&&part!=='.'&&part!=='..');
}
export function validCommand(value:unknown,operation:string):value is OrganizationCommand{
 return validID(operation)&&record(value)&&validID(value.task_id)&&value.operation_id===operation&&validID(value.tracking_job_id)&&validID(value.key);
}
function validPreview(value:unknown,operation:string):value is OrganizationPreview{
 if(!record(value)||!validID(value.task_id)||!validID(value.tracking_job_id)||value.operation_id!==operation||typeof value.can_confirm!=='boolean'||!natural(value.season)||value.season<1||!natural(value.attachment_count)||value.attachment_count>1000||typeof value.organization_state!=='string'||!value.organization_state||value.organization_state.length>64||!Array.isArray(value.videos)||value.videos.length<2||value.videos.length>1000||value.videos.length+value.attachment_count>1000)return false;
 const ids=new Set<string>(),keys=new Set<string>(),sources=new Set<string>(),targets=new Set<string>();
 for(const video of value.videos){
  if(!record(video)||!validID(video.unit_id)||typeof video.unit_key!=='string'||!relativePath(video.source_relative)||!relativePath(video.target_relative)||!natural(video.size_bytes)||video.size_bytes<1)return false;
  const key=/^S(\d{2,})E(\d{2,})$/.exec(video.unit_key);
  if(!key||Number(key[1])!==value.season||!Number.isSafeInteger(Number(key[2]))||Number(key[2])<1||video.unit_key!==`S${String(value.season).padStart(2,'0')}E${String(Number(key[2])).padStart(2,'0')}`||ids.has(video.unit_id)||keys.has(video.unit_key)||sources.has(video.source_relative)||targets.has(video.target_relative))return false;
  ids.add(video.unit_id);keys.add(video.unit_key);sources.add(video.source_relative);targets.add(video.target_relative);
 }
 return true;
}
export function validPreviewPage(value:unknown,operation:string):value is {items:OrganizationPreview[]}{
 return validID(operation)&&record(value)&&Array.isArray(value.items)&&value.items.length<=1&&value.items.every(item=>validPreview(item,operation));
}
export function validReceipt(value:unknown,task:string):value is OrganizationReceipt{
 if(!value||typeof value!=='object')return false;
 const r=value as OrganizationReceipt;
 return r.task_id===task&&validID(r.plan_id)&&validID(r.job_id)&&r.state==='queued';
}
function validJournal(value:unknown,account:string,operation:string):value is OrganizationJournal{
 return validID(account)&&record(value)&&value.version===1&&value.account===account&&value.operation===operation&&validCommand(value.command,operation)&&(value.receipt===undefined||validReceipt(value.receipt,value.command.task_id));
}
export function journalName(account:string,operation:string){return `pulse.complete-tv-organization.v1:${account}:${operation}`;}
export function readJournal(account:string,operation:string):OrganizationJournal|null{
 const raw=localStorage.getItem(journalName(account,operation));if(raw===null)return null;
 const j=JSON.parse(raw) as OrganizationJournal;
 if(!validJournal(j,account,operation))throw new Error('journal invalid');
 return j;
}
export function saveJournal(j:OrganizationJournal){
 if(!validJournal(j,j.account,j.operation))throw new Error('journal invalid');
 const name=journalName(j.account,j.operation),raw=JSON.stringify(j);
 localStorage.setItem(name,raw);if(localStorage.getItem(name)!==raw)throw new Error('journal not durable');
}
// Every journal read/modify/write, including receipt repair, shares this real lock.
// There is intentionally no localStorage-only fallback and no key replacement API.
export async function withOrganizationLock<T>(account:string,operation:string,signal:AbortSignal,work:()=>Promise<T>):Promise<T>{
 if(!navigator.locks?.request)throw new Error('locks unavailable');
 return navigator.locks.request(journalName(account,operation),{mode:'exclusive',signal},async()=>{if(signal.aborted)throw new Error('aborted');return work();});
}
