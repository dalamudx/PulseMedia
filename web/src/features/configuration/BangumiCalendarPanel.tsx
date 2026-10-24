import { useId, useState } from "react";
import { CalendarDays, RefreshCw } from "lucide-react";
import { useAuth } from "@/features/auth/AuthContext";
import type { MediaItem } from "@/components/cards/MediaCard";
import { useBangumiCalendar } from "./useBangumiCalendar";
import { bangumiPoster, broadcastDays, matchesBangumiName, type BangumiItem } from "./bangumiCalendar";
import "./bangumi-calendar.css";

export function BangumiCalendarPanel({onSelect}:{onSelect:(media:MediaItem)=>void}){
 const {user}=useAuth();if(!user)return null;
 return <CalendarPanel key={`${user.id}:${user.role}`} onSelect={onSelect}/>;
}
function Poster({item}:{item:BangumiItem}){
 const [failed,setFailed]=useState(false),src=bangumiPoster(item.poster_url);
 return src&&!failed?<img src={src} alt="" loading="lazy" decoding="async" referrerPolicy="no-referrer" onError={()=>setFailed(true)}/>:<div className="bangumi-poster-empty" aria-label="暂无封面"><CalendarDays size={24}/></div>;
}
function CalendarPanel({onSelect}:{onSelect:(media:MediaItem)=>void}){
 const model=useBangumiCalendar(),queryId=useId();
 const [weekday,setWeekday]=useState(()=>((new Date().getDay()+6)%7)+1),[query,setQuery]=useState("");
 const items=(model.calendar?.days??[]).filter(day=>weekday===0||day.weekday===weekday).flatMap(day=>day.items.filter(item=>matchesBangumiName(item,query)).map(item=>({item,day:day.weekday})));
 const chooseDay=(day:number)=>{model.clearMatch();setWeekday(day);};
 return <section className="bangumi-calendar" aria-label="Bangumi动漫新番周历">
  <div className="bangumi-heading"><div><h3>动漫新番周历</h3><p className="text-muted">按Bangumi放送星期浏览。首播日期属于作品，不代表单集播出日。</p></div><button type="button" className="btn-secondary" disabled={model.loading} onClick={()=>void model.refresh()}><RefreshCw size={16}/>{model.loading?"读取中…":"刷新周历"}</button></div>
  <div className="bangumi-weekdays" role="group" aria-label="放送星期">
   <button type="button" className={`tag-btn ${weekday===0?"tag-btn-active":""}`} aria-pressed={weekday===0} onClick={()=>chooseDay(0)}>整周</button>
   {broadcastDays.map((label,i)=><button key={label} type="button" className={`tag-btn ${weekday===i+1?"tag-btn-active":""}`} aria-pressed={weekday===i+1} onClick={()=>chooseDay(i+1)}>{label}</button>)}
  </div>
  <div className="bangumi-name-filter"><label htmlFor={queryId}>中日名称筛选</label><input id={queryId} className="form-input" value={query} maxLength={256} placeholder="输入中文名或原名" onChange={e=>{model.clearMatch();setQuery(e.target.value);}}/></div>
  {model.error?<div role="alert" className="config-error">{model.error}<button type="button" className="btn-secondary" onClick={()=>void model.refresh()}>重新读取周历</button></div>:model.loading?<p role="status">正在读取Bangumi周历…</p>:<>
   <p className="text-muted" role="status">{weekday===0?"整周":broadcastDays[weekday-1]} · {items.length}部作品</p>
   {items.length===0?<div className="empty-state" role="status"><h3>当前筛选没有番剧</h3><p>可切换星期或调整中日名称；周历没有推断缺少的放送信息。</p></div>:<div className="bangumi-list">{items.map(({item,day})=><article className="bangumi-work" key={`${day}:${item.bangumi_id}`}>
    <div className="bangumi-poster"><Poster item={item}/></div><div className="bangumi-work-content"><h4>{item.title||item.original_title}</h4>{item.original_title&&item.original_title!==item.title&&<p className="text-muted" lang="ja">{item.original_title}</p>}<p>{broadcastDays[day-1]} · {item.first_air_date?`首播 ${item.first_air_date}`:"首播日期未知"}</p><p className="text-muted">{item.rating===null?"Bangumi暂无评分":`Bangumi评分 ${item.rating.toFixed(1)}`}</p><button type="button" className="btn-secondary" disabled={model.matching} onClick={()=>void model.match(item)}>追番 · 选择作品</button></div>
   </article>)}</div>}
  </>}
  {model.selected&&<section className="bangumi-candidates" aria-label="TMDB追番候选"><div className="bangumi-heading"><h3>为《{model.selected.title||model.selected.original_title}》选择作品</h3><button type="button" className="btn-secondary" onClick={model.clearMatch}>收起候选</button></div><p className="text-muted">请核对TMDB作品，再选择季集与订阅配置。当前操作尚未提交订阅。</p>
   {model.matching?<p role="status">正在读取TMDB电视剧候选…</p>:model.matchError?<div role="alert" className="config-error">{model.matchError}<button type="button" className="btn-secondary" onClick={()=>model.selected&&void model.match(model.selected)}>重试候选读取</button></div>:model.candidates.length===0?<p role="status">暂无TMDB电视剧候选，可在影视搜索中尝试其他名称。</p>:<ul className="bangumi-candidate-list">{model.candidates.map(item=><li key={item.tmdb_id}><div><strong>{item.title}</strong><p className="text-muted">{item.year??"年份未知"} · TMDB {item.tmdb_id}</p></div><button type="button" className="btn-primary" onClick={()=>onSelect(item)}>查看详情并追番</button></li>)}</ul>}
  </section>}
 </section>;
}
