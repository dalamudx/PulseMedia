import {useEffect,useRef,useState} from 'react';
import {api} from '@/api/client';
import {persistTestCommand,readTestCommand,testCommandKey,type TestCommand} from './notificationTestCommand';

type Props={account:string;channel:{id:string;name:string;updated_at:string;is_enabled:boolean};disabled:boolean};
export function NotificationTestButton({account,channel,disabled}:Props){
 const storageKey=testCommandKey(account,channel.id);
 const [pending,setPending]=useState<TestCommand|null>(null),[blocked,setBlocked]=useState(true),[busy,setBusy]=useState(false),[message,setMessage]=useState(''),[failed,setFailed]=useState(false);
 const owner=useRef<AbortController|null>(null);
 useEffect(()=>{try{setPending(readTestCommand(localStorage.getItem(storageKey),channel.id));setBlocked(false)}catch{setMessage('本机测试命令记录不可读取，已停止推送。恢复记录后重新加载。');setFailed(true)}return()=>owner.current?.abort()},[storageKey,channel.id]);
 async function send(){
  if(owner.current||blocked||disabled||(!pending&&!channel.is_enabled))return;
  let command:TestCommand;
  try{
   const saved=readTestCommand(localStorage.getItem(storageKey),channel.id);
   if(pending&&saved?.key!==pending.key)throw new Error('record changed');
   command=saved??{id:channel.id,key:crypto.randomUUID(),body:{expected_updated_at:channel.updated_at}};
   persistTestCommand(localStorage,storageKey,command);
  }catch{setBlocked(true);setMessage('原命令未能可靠保存，未发送测试。请恢复浏览器存储后重新加载。');setFailed(true);return}
  setPending(command);setBusy(true);setMessage('');setFailed(false);
  const controller=new AbortController();owner.current=controller;
  try{
   const r=await api.POST('/api/v1/notification-channels/{id}/test',{signal:controller.signal,params:{path:{id:command.id},header:{'Idempotency-Key':command.key}},body:command.body});
   if(controller.signal.aborted)return;
   if(r.error||!r.data||r.data.status!=='queued'||r.data.channel_id!==command.id){
    setFailed(true);
    if([400,403,404,409].includes(r.response.status)){try{if(localStorage.getItem(storageKey)!==JSON.stringify(command))throw new Error('record changed');localStorage.removeItem(storageKey);setPending(null)}catch{}setBlocked(true);setMessage('测试权限或通道版本冲突，已停止推送；读取最新通道后复核。')}
    else setMessage('测试排队结果未确认；重试只恢复同一原命令，不会另建投递。');
    return;
   }
   try{if(localStorage.getItem(storageKey)!==JSON.stringify(command))throw new Error('record changed');localStorage.removeItem(storageKey);if(localStorage.getItem(storageKey)!==null)throw new Error('record retained');setPending(null)}catch{setMessage('测试已排队；本机记录未清除，继续操作仅恢复原命令。请在投递记录确认送达。');return}
   setMessage('测试已排队，请在投递记录确认送达；排队不代表已送达。');
  }catch{if(!controller.signal.aborted){setFailed(true);setMessage('测试排队结果未确认；重试保持原命令，不会另建投递。')}}
  finally{if(owner.current===controller)owner.current=null;if(!controller.signal.aborted)setBusy(false)}
 }
 return <div><button type="button" className="btn-secondary channel-state-button" aria-label={`${pending?'重试同一测试通知':'发送测试通知'} ${channel.name}`} disabled={busy||blocked||disabled||(!pending&&!channel.is_enabled)} onClick={()=>void send()}>{busy?'正在确认测试…':pending?'重试同一测试通知':'发送测试通知'}</button>{message&&<p className="config-description" role={failed?'alert':'status'}>{message}</p>}</div>;
}
