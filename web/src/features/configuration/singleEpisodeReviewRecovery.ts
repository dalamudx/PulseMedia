import type {paths} from "@/api/client";

type Command=paths["/api/v1/subscriptions/{id}/episode-candidate-reviews"]["post"]["requestBody"]["content"]["application/json"];
export type ReviewReceipt=paths["/api/v1/subscriptions/{id}/episode-candidate-reviews"]["post"]["responses"]["200"]["content"]["application/json"];
export type PendingReview={key:string;body:Command};
type ConfirmedReview={command:PendingReview;receipt:ReviewReceipt;persisted:boolean};
type StoragePort=Pick<Storage,"setItem"|"removeItem">;
type Reply={status:number;data?:ReviewReceipt};
export type ReviewResult={kind:"not_sent"|"unknown"|"rejected"}|{kind:"confirmed";confirmation:ConfirmedReview};
const confirmed=new Map<string,ConfirmedReview>();
const confirmedCommands=new Map<string,ConfirmedReview>();
export const reviewCommandKey=(id:string)=>`pulse:episode-review:${id}`;
const receiptKey=(id:string)=>`pulse:episode-review-receipt:${id}`;

// This fence survives a panel unmount in the same page. It contains only the
// same safe UUID/stamp/receipt fields already persisted by the incumbent UI.
export function confirmedEpisodeReview(id:string,command?:PendingReview|null){
 return command?confirmedCommands.get(`${id}:${command.key}`)??null:confirmed.get(id)??null;
}
export function repairEpisodeReview(id:string,storage:StoragePort,command?:PendingReview):ConfirmedReview|null{
 const value=confirmedEpisodeReview(id,command);if(!value)return null;
 try{
  storage.setItem(receiptKey(id),JSON.stringify(value.receipt));
  storage.removeItem(reviewCommandKey(id));
  value.persisted=true;
 }catch{value.persisted=false}
 return value;
}
function validReceipt(id:string,command:PendingReview,value:ReviewReceipt|undefined):value is ReviewReceipt{
 const uuid=/^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
 return !!value&&value.subscription_id===id&&value.candidate_id===command.body.candidate_id&&value.input_stamp===command.body.expected_input_stamp&&value.status==="reviewed_not_executable"&&uuid.test(value.id)&&uuid.test(value.unit_id);
}

// One explicit review attempt; no retries, queues, or acquisition authority.
// A denial on an unknown retry cannot disprove an earlier commit: the server
// checks current account permissions before looking up the original receipt.
export async function sendEpisodeReview(id:string,command:PendingReview,isRetry:boolean,storage:StoragePort,send:()=>Promise<Reply>):Promise<ReviewResult>{
 const known=confirmedEpisodeReview(id,command);
 if(known)return {kind:"confirmed",confirmation:known.persisted?known:repairEpisodeReview(id,storage,command)!};
 try{storage.setItem(reviewCommandKey(id),JSON.stringify(command))}catch{return {kind:isRetry?"unknown":"not_sent"}}
 let reply:Reply;
 try{reply=await send()}catch{return {kind:"unknown"}}
 if(reply.status===200&&validReceipt(id,command,reply.data)){
  // Fence before any fallible storage access, and regardless of HTTP abort.
  const confirmation={command,receipt:reply.data,persisted:false};
  confirmed.set(id,confirmation);
  confirmedCommands.set(`${id}:${command.key}`,confirmation);
  repairEpisodeReview(id,storage);
  return {kind:"confirmed",confirmation};
 }
 if(!isRetry&&[400,403,409].includes(reply.status)){
  try{storage.removeItem(reviewCommandKey(id));return {kind:"rejected"}}catch{/* Keep the original command when cleanup is uncertain. */}
 }
 return {kind:"unknown"};
}

