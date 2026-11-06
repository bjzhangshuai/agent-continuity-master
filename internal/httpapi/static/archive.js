const archiveUI={taskID:"",after:0,jobs:[]};
const archiveStateLabel=s=>({queued:tr("待投递 / 排队","Queued"),processing:tr("云端操作中","Processing"),retry:tr("等待重试","Retry scheduled"),stored:tr("归档已持久化","Stored"),dead:tr("死信 · 需处理","Dead letter"),deleted:tr("归档副本已删除","Archive copy deleted")}[s]||s);
async function loadArchive(){
  const root=$("archive-content");root.replaceChildren();
  try{
    const config=await api("/v1/storage/config");
    const intro=portalCard(tr("上传 → 事务 Outbox → JetStream → Worker → 对象存储","Upload → transactional Outbox → JetStream → worker → object storage"));
    intro.append(el("p",tr("上传成功表示服务端已接收。只有“归档已持久化”才表示对象存储写入成功；重试不会重复记录归档用量。","Upload acceptance is not cloud completion. Stored confirms the object write; retries do not duplicate archive usage."),"muted"));root.append(intro);
    if(!config.enabled){intro.append(el("p",tr("对象归档未启用。原有附件服务仍可用。","Archival is disabled. Legacy attachments remain available.")));return;}
    intro.append(el("p",`NATS JetStream · ${config.replicas} ${tr("副本","replica(s)")} · ${config.max_attempts} ${tr("次自动尝试","automatic attempts")}`));
    intro.append(el("p",tr(`保留期 ${config.lifecycle?.minimum_retention_days||0} 天；缓存自动清理：${config.lifecycle?.cache_ttl_seconds?"已启用":"关闭"}；删除归档副本：${config.lifecycle?.allow_delete?"已启用（需确认）":"关闭"}。归档量、保留时长和成功回源量进入账单，新增三项默认零价格。`,`Retention: ${config.lifecycle?.minimum_retention_days||0} days. Cache eviction: ${config.lifecycle?.cache_ttl_seconds?"on":"off"}. Archive deletion: ${config.lifecycle?.allow_delete?"enabled with confirmation":"disabled"}. Archive, retention and origin-read meters have explicit zero default rates.`),"muted"));
    for(const target of config.targets)intro.append(pill(`${target.id} · ${target.provider}${target.provider==="filesystem"?tr("（本地演示，不是真实云）"," (local demo, not cloud)"):""}`,"neutral"));
    const tasks=(await api("/v1/tasks")).tasks||[];const controls=el("div","","card section-actions");const select=document.createElement("select");select.setAttribute("aria-label",tr("选择归档任务","Choose archive task"));
    for(const task of tasks){const option=el("option",`${task.title} · ${task.task_id}`);option.value=task.task_id;select.append(option);}
    if(!tasks.some(t=>t.task_id===archiveUI.taskID))archiveUI.taskID=state.task?.task_id&&tasks.some(t=>t.task_id===state.task.task_id)?state.task.task_id:tasks[0]?.task_id||"";
    select.value=archiveUI.taskID;select.addEventListener("change",()=>{archiveUI.taskID=select.value;archiveUI.after=0;loadArchive();});controls.append(select);
    const backfill=el("button",tr("补归档本任务历史内容（最多100条）","Archive historical content (up to 100)"));backfill.addEventListener("click",async()=>{if(!archiveUI.taskID)return;backfill.disabled=true;try{const result=await api(`/v1/tasks/${encodeURIComponent(archiveUI.taskID)}/storage-backfill`,{method:"POST"});toast(tr(`已入队 ${result.enqueued} 条`,`Queued ${result.enqueued}`));archiveUI.after=0;await loadArchive();}catch(e){toast(e.message);}finally{backfill.disabled=false;}});controls.append(backfill);root.append(controls);
    if(!archiveUI.taskID){root.append(el("p",tr("当前账号暂无可查看的任务。","No accessible tasks.")));return;}
    const result=await api(`/v1/tasks/${encodeURIComponent(archiveUI.taskID)}/storage-jobs?after=${archiveUI.after}`);archiveUI.jobs=result.jobs;
    const list=el("div","","card");root.append(list);
    if(!result.jobs.length)list.append(el("p",tr("暂无归档消息。上传新对话/附件后刷新；历史内容需手动补归档。","No messages. Upload new content and refresh, or explicitly backfill history."),"muted"));
    for(const job of result.jobs){const row=el("section","","feed-item");row.append(el("strong",`${job.kind==="event"?tr("对话事件","Conversation"):tr("附件","Attachment")} · ${job.resource_id}`),pill(archiveStateLabel(job.state),job.state==="stored"?"success":job.state==="dead"?"danger":"neutral"),el("p",`${job.target_id} · ${job.content_type} · ${job.size} bytes · ${tr("尝试","attempts")} ${job.attempts}`,"muted"));
      if(job.error_code)row.append(el("p",job.error_code,"payment-warning"));
      row.append(el("p",`${tr("操作","Operation")}: ${job.operation||"archive"} · ${tr("保留锁","Legal hold")}: ${job.legal_hold?tr("已锁定","held"):tr("未锁定","none")} · ${tr("云回执时间","Stored at")}: ${job.stored_at||"—"} · ${tr("保留至","Retain until")}: ${job.retain_until||"—"}`,"muted"));
      if(job.operation!=="delete"){
        const hold=el("button",job.legal_hold?tr("解除保留锁（超级管理员）","Release hold (super admin)"):tr("设置保留锁（管理员）","Place hold (admin)"));
        hold.addEventListener("click",async()=>{const reason=prompt(tr("输入操作原因，至少 8 个字符","Enter a reason, at least 8 characters"));if(!reason)return;try{await api(`/v1/tasks/${encodeURIComponent(job.task_id)}/storage-jobs/${job.id}/hold`,{method:"POST",body:{hold:!job.legal_hold,reason}});await loadArchive();}catch(e){toast(e.message);}});row.append(hold);
      }
      if(config.lifecycle?.allow_delete&&job.state==="stored"&&!job.legal_hold){
        const remove=el("button",tr("删除此归档副本（需哈希确认）","Delete archive copy (confirm hash)"));remove.addEventListener("click",async()=>{const confirm_hash=prompt(tr(`只删除归档副本，不删除原始对话；版本桶可能保留历史版本。输入内容哈希确认：\n${job.content_hash}`,`Deletes archive copy only, not source conversation. Versioned buckets may keep old versions. Type hash:\n${job.content_hash}`));if(confirm_hash!==job.content_hash)return;const reason=prompt(tr("输入删除原因，至少 8 个字符","Deletion reason, at least 8 characters"));if(!reason)return;try{await api(`/v1/tasks/${encodeURIComponent(job.task_id)}/storage-jobs/${job.id}/delete`,{method:"POST",body:{confirm_hash,reason}});await loadArchive();}catch(e){toast(e.message);}});row.append(remove);
      }
      if(job.state==="stored"){const download=el("button",tr("从对象存储下载并校验","Download verified archive"));download.addEventListener("click",async()=>{try{const headers={"X-Portal-Request":"1"};if(state.token)headers.Authorization=`Bearer ${state.token}`;const response=await fetch(`/v1/tasks/${encodeURIComponent(job.task_id)}/storage-jobs/${job.id}/content`,{headers,credentials:"same-origin"});if(!response.ok)throw Error((await response.json()).error.message);const url=URL.createObjectURL(await response.blob());const a=document.createElement("a");a.href=url;a.download=job.kind==="event"?`conversation-${job.id}.json`:`archive-${job.id}`;a.click();setTimeout(()=>URL.revokeObjectURL(url),1000);}catch(e){toast(e.message);}});row.append(download);}
      if(job.state==="dead"){const retry=el("button",tr("重投死信（需管理权限）","Replay dead letter (admin)"));retry.addEventListener("click",async()=>{try{await api(`/v1/tasks/${encodeURIComponent(job.task_id)}/storage-jobs/${job.id}/retry`,{method:"POST"});await loadArchive();}catch(e){toast(e.message);}});row.append(retry);}list.append(row);
    }
    if(result.has_more){const more=el("button",tr("下一页","Next page"));more.addEventListener("click",()=>{archiveUI.after=result.next_after;loadArchive();});root.append(more);}
    if(archiveUI.after){const first=el("button",tr("回到第一页","First page"));first.addEventListener("click",()=>{archiveUI.after=0;loadArchive();});root.append(first);}
  }catch(error){root.append(el("p",error.message,"card payment-warning"));}
}
panelLoaders["archive-panel"]=loadArchive;
$("archive-refresh").addEventListener("click",loadArchive);
