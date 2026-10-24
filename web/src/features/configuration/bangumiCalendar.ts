import type { components } from "@/api/schema";
export type BangumiItem = components["schemas"]["BangumiCalendarItem"];
export type BangumiCalendar = components["schemas"]["BangumiCalendarResponse"];
export const broadcastDays = ["周一", "周二", "周三", "周四", "周五", "周六", "周日"] as const;
export function matchesBangumiName(item:BangumiItem,query:string):boolean{
 const fold=(value:string)=>value.normalize("NFKC").toLocaleLowerCase("und");
 const term=fold(query.trim());return !term||fold(item.title).includes(term)||fold(item.original_title).includes(term);
}
export function bangumiYear(item:BangumiItem):number|undefined{
 return item.first_air_date&&/^\d{4}-\d{2}-\d{2}$/.test(item.first_air_date)?Number(item.first_air_date.slice(0,4)):undefined;
}
export function bangumiPoster(raw:string|null):string|undefined{
 if(!raw)return undefined;
 try{const url=new URL(raw);if(!url.href.startsWith("https://lain.bgm.tv/")||url.port||url.search||url.hash)return undefined;return url.href;}catch{return undefined;}
}
