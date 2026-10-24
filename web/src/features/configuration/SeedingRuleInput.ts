import type { components } from "@/api/schema";
export function seedingRuleInput(mode:string,seconds:string,ratio:string,bytes:string):components["schemas"]["SeedingRule"]|null{
 if(mode!=="any"&&mode!=="all")return null;
 const integer=(value:string,max:number)=>/^[1-9][0-9]*$/.test(value)&&Number.isSafeInteger(Number(value))&&Number(value)<=max;
 if(seconds!==""&&!integer(seconds,315360000)||bytes!==""&&!integer(bytes,Number.MAX_SAFE_INTEGER))return null;
 if(ratio!==""&&(!/^(?:0|[1-9][0-9]*)(?:\.[0-9]+)?$/.test(ratio)||!Number.isFinite(Number(ratio))||Number(ratio)<=0||Number(ratio)>1000000))return null;
 if(seconds===""&&ratio===""&&bytes==="")return null;
 return {mode,delete_files:false,...(seconds===""?{}:{min_seeding_seconds:Number(seconds)}),...(ratio===""?{}:{min_share_ratio:Number(ratio)}),...(bytes===""?{}:{low_free_bytes:Number(bytes)})};
}
