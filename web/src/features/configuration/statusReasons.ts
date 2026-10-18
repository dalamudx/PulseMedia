const reasons: Record<string,string> = {
 shared_acquisition_available:"其它目标已有获取，正在核验是否可共享",shared_acquisition_unknown:"全局获取结果不明，保持保护等待复核",skipped:"目标已跳过",root_offline:"目标库离线，拥有状态未知",baseline_pending:"目标库基线扫描未完成",policy_unavailable:"质量策略版本不可用",
 subscription_paused:"订阅已暂停",subscription_archived:"订阅已归档",automation_unavailable:"此媒体类型尚未启用自动获取",
 no_indexers:"未绑定索引器",endpoints_offline:"所选索引器均不可用",endpoint_backoff:"所选端点正在限流或等待共享预算",no_downloader:"没有可用下载器",
 unit_retired:"媒体单元已退休",future_air_date:"尚未到播出日期",air_date_unknown:"播出日期尚未确认",reservation_active:"目标获取占位仍有效",reservation_unknown:"外部获取结果待对账，占位保留",
 quality_unknown:"文件质量或入库证据未确认",file_rejected:"目标库文件未通过当前允许规则",search_round_pending:"已有本版本搜索轮次，等待收口",round_finalization_pending:"搜索截止时间已到，等待持久收口",no_qualified_candidate:"最近本版本轮次无合格候选",missing:"目标库未记录符合条件的有效入库文件",
 status_loading:"正在读取当前数据库状态",status_unavailable:"当前状态读取失败，不能确认拥有"
};
export function safeReason(value:string, labels:Record<string,string>=reasons):string {
 const message=Object.hasOwn(labels,value)?labels[value]:Object.hasOwn(reasons,value)?reasons[value]:undefined;
 return typeof message==="string"?message:"当前状态需复核";
}
export function statusReason(reason:string):string{return safeReason(reason)}
