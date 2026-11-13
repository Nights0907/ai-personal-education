"use strict";

const $ = id => document.getElementById(id);
const view = { data: null, messages: [], filter: "all", query: "", cursor: "", controller: null, request: 0, busy: false, preview: null };
const validId = value => /^[A-Za-z0-9_-]{1,128}$/.test(value);
const number = value => Number(value || 0).toLocaleString("zh-CN");
const text = (id, value) => { $(id).textContent = value; };
const element = (tag, className, content) => {
  const node = document.createElement(tag);
  if (className) node.className = className;
  if (content !== undefined) node.textContent = content;
  return node;
};
const time = value => {
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? "—" : new Intl.DateTimeFormat("zh-CN", { month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit", hour12: false }).format(date);
};

async function get(path, signal) {
  const response = await fetch(path, { signal, cache: "no-store" });
  const data = await response.json().catch(() => ({}));
  if (!response.ok) {
    const messages = { 404: "没有找到这个会话。请检查编号，或从左侧选择已经保存的会话。", 409: "会话有新消息或摘要已更新，请刷新会话后继续读取。", 403: "暂时无法访问这个会话。" };
    const error = new Error(messages[response.status] || data.error || "暂时无法读取记录，请稍后重试。");
    error.status = response.status;
    throw error;
  }
  return data;
}

function notice(message, isError = false) {
  text("notice", message);
  $("notice").hidden = !message;
  $("notice").classList.toggle("error", isError);
}

function empty(title, description) {
  const box = element("div", "empty-state");
  box.append(element("div", "empty-mark", "✦"), element("h3", "", title), element("p", "", description));
  $("messageList").replaceChildren(box);
}

function messageText(message) {
  if (message.role === "user" && typeof message.original_input?.content === "string") return message.original_input.content;
  const items = Array.isArray(message.content?.items) ? message.content.items : [];
  const direct = items.filter(item => item && item.role === message.role && typeof item.content === "string").map(item => item.content).filter(Boolean).join("\n\n");
  for (const item of items) {
    if (item.role !== "tool" || item.tool_name !== "finalize_solution") continue;
    try { const v = JSON.parse(item.content); if(v.committed && v.solution) return `题目编号：${v.question_id}\n\n答案：${v.solution.answer.map(a => a.content).join("；")}\n\n解析：${v.solution.analysis}\n\n解题步骤：\n${v.solution.steps.map(s => `${s.ordinal}. ${s.title}\n${s.content}`).join("\n\n")}`; } catch (_) {}
  }
  return direct;
}

function highlightPromptTags(pre,content){
 const value=String(content || '');let last=0;const stack=[pre];
 for(const match of value.matchAll(/<[^>\n]+>/g)){
  let body=value.slice(last,match.index).trim().replace(/&(#(?:x[0-9a-f]+|[0-9]+)|quot|apos|lt|gt|amp);/gi,(_,entity)=>{const names={quot:'"',apos:"'",lt:'<',gt:'>',amp:'&'};if(entity[0]!=='#')return names[entity.toLowerCase()];const n=entity[1].toLowerCase()==='x'?parseInt(entity.slice(2),16):parseInt(entity.slice(1),10);return n>0&&n<=0x10ffff?String.fromCodePoint(n):_;});
  if(body){try{body=JSON.stringify(JSON.parse(body),null,2);}catch(_){}stack.at(-1).append(document.createTextNode(body+'\n'));}
  const tag=match[0],name=tag.match(/^<\/?([\w-]+)/)?.[1];
  if(name && !tag.startsWith('</') && !tag.endsWith('/>')){const box=element('span','prompt-block');box.dataset.tone=String([...name].reduce((n,c)=>n+c.charCodeAt(0),0)%4);stack.at(-1).append(box);stack.push(box);}
  stack.at(-1).append(element('span','prompt-tag',tag));
  if(!tag.startsWith('</'))stack.at(-1).append(document.createTextNode('\n'));
  if(tag.startsWith('</') && stack.length>1)stack.pop();last=match.index+tag.length;
 }
 stack.at(-1).append(document.createTextNode(value.slice(last).replace(/^[ \t]*\n[ \t]*/, '')));
}
async function editSharedPrompt(){
 let dialog=document.querySelector('#promptEditor');if(dialog)dialog.remove();
 dialog=element('dialog','prompt-editor');dialog.id='promptEditor';dialog.append(element('h2','','编辑共享静态提示词'),element('p','muted','保存后对所有学生的下一轮请求生效；当前历史原文保持不变。'));
 const input=element('textarea');input.setAttribute('aria-label','共享静态提示词');const status=element('p','muted','正在加载当前版本…'),save=element('button','button primary','保存并应用'),close=element('button','button subtle','取消');save.disabled=true;
 close.onclick=()=>dialog.close();dialog.append(input,status,save,close);document.body.append(dialog);dialog.showModal();let revision;
 try{const data=await get('/api/v1/teacher/system-prompt');input.value=data.content;revision=data.revision;status.textContent='请保留必需的标签结构。';save.disabled=false;}catch(e){status.textContent=e.message;}
 save.onclick=async()=>{save.disabled=true;try{const response=await fetch('/api/v1/teacher/system-prompt',{method:'PUT',headers:{'Content-Type':'application/json'},body:JSON.stringify({content:input.value,revision})});const data=await response.json();if(!response.ok)throw Error(data.error || '保存失败');revision=data.revision;status.textContent='已保存，下一轮请求使用新版本。';}catch(e){status.textContent=e.message;}finally{save.disabled=false;}};
}

function payloadDetails(title,value){
 const details=element('details','execution-payload');details.append(element('summary','',title));
 details.addEventListener('toggle',()=>{if(details.open && !details.querySelector('pre')){
  let content=value;
  if(!title.startsWith('System Prompt') && !title.startsWith('Dynamic Context')){try{content=JSON.stringify(typeof value==='string'?JSON.parse(value):value,null,2);}catch(_){}}
  const pre=element('pre');
  if(title.startsWith('System Prompt') || title.startsWith('Dynamic Context'))highlightPromptTags(pre,content);else pre.textContent=content || '（空结果）';
  details.append(pre);
 }});return details;
}

function renderMessages(){
 if(!view.data)return;
 const groups=new Map();
 for(const message of view.messages){if(!groups.has(message.turn_id))groups.set(message.turn_id,[]);groups.get(message.turn_id).push(message);}
 const fragment=document.createDocumentFragment();let shown=0;
 for(const [turnId,messages] of groups){
  const traces=(view.data.execution_traces || []).filter(t=>t.turn_id===turnId || t.events?.some(e=>e.type==='accepted' && e.data.turn_id===turnId));
  const calls=new Map();let prompt='',sealed=false;
  for(const message of messages){
   if(message.role==='user')prompt=messageText(message);if(message.role==='assistant' && message.status==='SEALED')sealed=true;
   for(const item of message.content?.items || []){
    for(const call of item.tool_calls || []){const f=call.function || {};if(!calls.has(call.id))calls.set(call.id,{id:call.id,name:f.name,args:f.arguments,status:'未取得返回结果'});}
    if(item.role==='tool'){
     const id=item.tool_call_id;let call=calls.get(id);if(!call){call={id,name:item.tool_name,status:'已记录返回结果'};calls.set(id,call);}call.result=item.content;call.status='已记录返回结果';
    }
   }
  }
  for(const trace of traces)for(const event of trace.events || []){
   if(event.type!=='execution_node' || !['tool','skill'].includes(event.data.kind))continue;
   const d=event.data;let call=calls.get(d.id) || {id:d.id};call={...call,name:d.name,kind:d.kind,status:({running:'调用中',completed:'已完成',needs_revision:'需修改',failed:'失败',recorded:'已记录'})[d.status] || d.status,args:d.arguments_json ?? call.args,result:d.result_json ?? call.result,elapsed:d.elapsed_ms ?? call.elapsed,hash:d.content_hash ?? call.hash,at:d.at,run:d.run_id};calls.set(d.id,call);
  }
  if(view.filter==='covered' && !messages.some(m=>m.covered_by_current_compact))continue;
  const card=element('article','message-card execution-turn');
  const head=element('div','message-head');head.append(element('strong','',`第 ${++shown} 轮 · 智能体执行记录`),element('time','message-date',time(messages[0].created_at)),element('span','coverage-badge',traces.at(-1)?.status || (sealed?'已封口':'处理中 / 未完成')));card.append(head);
  const prompts=messages.flatMap(m=>m.system_prompts || []);
  if(prompts.length)card.append(payloadDetails('System Prompt · 静态规则原文', [...new Set(prompts)].join('\n\n')));else card.append(element('p','muted','本轮未保存 system prompt 原文（历史记录不以当前提示词替代）。'));
  const dynamics=messages.map(m=>m.dynamic_context).filter(Boolean);
  if(dynamics.length)card.append(payloadDetails('Dynamic Context · 技能目录 / L1记忆 / 本轮任务',[...new Set(dynamics)].join('\n\n')));else card.append(element('p','muted','本轮未独立保存动态上下文（历史记录保持原样）。'));
  card.append(element('p','execution-request',prompt || '当前页未包含本轮原始输入'),element('small','muted',`轮次 ${turnId}`));
  const list=element('ol','execution-list');
  for(const call of calls.values()){
   let kind=call.kind,name=call.name;
   if(name==='read_file'){try{const path=JSON.parse(call.args).path;if(/^\/skills\/[^/]+\/SKILL.md$/.test(path)){kind='skill';name=path.split('/')[2];}}catch(_){}}
   const row=element('li','execution-record');const label=element('div','execution-label');label.append(document.createTextNode(`${kind==='skill'?'◇ 加载技能':'⌘ 调用工具'} · `),element('span','execution-name',name || '未标注工具'),document.createTextNode(` · ${call.status}${call.elapsed!==undefined?' · '+call.elapsed+' ms':''}`));row.append(label);
   if(call.args!==undefined)row.append(payloadDetails('Tool Call · 调用参数',call.args));
   if(call.result!==undefined)row.append(payloadDetails('Tool Result · 返回结果',call.result));
   if(call.hash)row.append(element('small','muted','技能内容哈希：'+call.hash));
   if(call.run)row.append(element('small','muted','执行尝试：'+call.run));
   list.append(row);
  }
  if(!calls.size)list.append(element('li','muted','本轮暂无可读取的工具调用；可能是纯对话、记录尚未封口，或历史事件未保存。'));
  for(const trace of traces){
   for(const event of trace.events || [])if(event.type==='execution_node' && ['memory','storage'].includes(event.data.kind))list.append(element('li','execution-record',`${event.data.kind==='memory'?'◈':'▣'} ${event.data.name} · ${event.data.status==='completed'?'已完成':'等待处理'}`));
   if(trace.error)list.append(element('li','error','执行说明：'+trace.error));
   if(trace.events_may_have_more)list.append(element('li','muted','该任务事件超过200条，当前为部分执行事件；工具正文以保存消息为准。'));
  }
  card.append(list,element('p','muted','历史缺失的调用时间、耗时和技能版本不作推测。仅展示执行记录，不包含模型内部思维链。'));fragment.append(card);
 }
 text('messageCount',`已加载 ${groups.size} 个轮次 · 显示 ${shown} 个`);text('loadedNote',`已读取 ${view.messages.length} / ${view.data.total_messages} 条底层记录`);$('loadMore').hidden=!view.cursor;
 if(!shown){empty('暂无匹配的执行记录','选择会话或调整搜索条件。');return;}$('messageList').replaceChildren(fragment);
}

function renderSummary(summary, historical = false) {
  const body = $("summaryBody");
  body.replaceChildren();
  const revision = Number(summary?.Revision || 0);
  text("summaryBadge", revision ? `v${revision}${historical ? " · 预览" : " · 当前"}` : "暂无摘要");
  if (!revision) {
    body.append(element("div", "summary-symbol", "⌁"), element("p", "muted", "尚未发生记忆压缩。消息完整保存在数据库中，达到条件后才会生成摘要。"));
    return;
  }
  const structured = summary.Structured && typeof summary.Structured === "object" ? summary.Structured : {};
  body.append(element("p", "summary-overview", structured.overview || summary.Content || "该版本未提供文字概述。"));
  for (const [key, title] of [["student_statements", "学生提问与自述"], ["teaching_actions", "已讲内容"], ["pending_questions", "待解决问题"], ["uncertainties", "需进一步核实"]]) {
    const entries = Array.isArray(structured[key]) ? structured[key] : [];
    if (!entries.length) continue;
    const section = element("section", "summary-section");
    const list = element("ul");
    for (const item of entries) {
      if (typeof item?.text !== "string") continue;
      const li = element("li", "", item.text);
      if (Array.isArray(item.source_turn_ids)) li.title = `来源轮次：${item.source_turn_ids.join("、")}`;
      list.append(li);
    }
    section.append(element("h3", "", title), list);
    body.append(section);
  }
  body.append(element("div", "summary-footnote", historical ? "正在预览历史版本。对话上的覆盖标记仍以当前生效摘要为准。" : `覆盖至轮次游标 ${summary.ThroughCursor}。原始问答可在左侧完整查看，摘要不会替代数据库中的原文。`));
}

function renderOverview() {
  const data = view.data;
  text("totalMessages", number(data.total_messages));
  text("coveredMessages", number(data.compacted_messages));
  text("contextMessages", number(data.context_messages));
  text("compactVersion", `v${data.current_summary?.Revision || 0}`);
  text("compactNote", data.compact_versions ? `已保存 ${number(data.compact_versions)} 个压缩版本` : "尚未达到压缩条件");
  text("identityConversation", data.conversation_id);
  text("identityBranch", data.current_branch_id);
  text("identityThrough", data.current_summary?.ThroughCursor || "尚未压缩");
  $("copyId").disabled = false;
  $("refreshButton").disabled = false;
  const policy = data.policy || {};
  const budget = Math.max(0, Number(policy.max_input_tokens || 0) - Number(policy.output_reserve || 0));
  const usage = data.last_model_usage || {};
  const hasUsage = Number(usage.input_tokens) > 0;
  const provider = usage.source === "provider";
  text("usageSource", hasUsage ? (provider ? "模型返回用量" : "本地保守估算") : "暂无模型调用");
  text("usageNumber", hasUsage ? number(usage.input_tokens) : "—");
  text("usageLimit", `/ ${number(budget)} token`);
  const percentage = budget && hasUsage ? Math.min(100, usage.input_tokens / budget * 100) : 0;
  $("usageFill").style.width = `${percentage}%`;
  $("usageFill").classList.toggle("warning", percentage >= Number(policy.trigger_percent || 80));
  const threshold = Number(policy.trigger_percent || 80);
  $("usageThreshold").style.left = `${Math.max(0, Math.min(100, threshold))}%`;
  text("thresholdLabel", `${threshold}% 触发线`);
  const policyNote = data.policy_source === "default" && hasUsage ? "该历史轮次未记录预算，当前显示默认预算。" : "";
  const stale = hasUsage && Number(usage.summary_revision || 0) !== Number(data.current_summary?.Revision || 0);
  text("usageDescription", !hasUsage ? `仅保存问题还不会产生模型用量。默认达到 ${number(data.trigger_tokens)} token 且有可压缩历史时，后台生成摘要。` : `${policyNote}${stale ? "这是压缩前的用量记录，下一次对话后会更新。" : "这是最近成功轮次的完整输入用量，包含提示词与历史。"}触发阈值 ${number(data.trigger_tokens)} token；装配安全检查采用保守估算，可能提前压缩。`);
  const current = data.current_summary || {};
  const versions = Array.isArray(data.recent_compacts) ? data.recent_compacts : [];
  const options = [current, ...versions.filter(summary => summary.CompactID !== current.CompactID)].filter(summary => summary.Revision).map(summary => ({ summary, current: !!current.Revision && summary.CompactID === current.CompactID }));
  $("summaryVersion").replaceChildren();
  if (!current.Revision && options.length) { const placeholder = element("option", "", "当前分支尚无摘要"); placeholder.value = ""; $("summaryVersion").append(placeholder); }
  options.forEach((entry, index) => {
    const summary = entry.summary;
    const option = element("option", "", `v${summary.Revision}${entry.current ? " · 当前生效" : ` · 历史版本 ${String(summary.CompactID || "").slice(-6)}`}`);
    option.value = String(index);
    $("summaryVersion").append(option);
  });
  view.preview = options;
  $("versionControl").hidden = !options.length;
  text("versionHint", data.compact_versions > 20 ? "这里显示最近 20 个版本，数据库保留全部版本。" : "查看旧摘要不会修改当前会话。");
  renderSummary(current);
  text("updatedAt", `更新于 ${time(new Date())}`);
}

function clearOverview() {
  for (const id of ["totalMessages", "coveredMessages", "contextMessages", "compactVersion", "identityConversation", "identityBranch", "identityThrough", "usageNumber", "usageLimit"]) text(id, "—");
  text("compactNote", "等待查询结果"); text("messageCount", "正在读取真实记录"); text("loadedNote", "");
  text("usageSource", "暂无记录"); text("usageDescription", "显示最近成功对话的输入用量，不代表当前请求的实时测量。");
  $("usageFill").style.width = "0%";
  $("versionControl").hidden = true; $("loadMore").hidden = true; $("copyId").disabled = true;
  text("summaryBadge", "等待查询");
  $("summaryBody").replaceChildren(element("p", "muted", "完成查询后，这里会显示当前生效的历史摘要。"));
  text("updatedAt", "查询尚未完成");
}

async function loadConversation(id, append = false) {
  if (!validId(id)) { notice("请输入有效的会话编号，仅支持字母、数字、下划线和短横线。", true); return; }
  if (append && (view.busy || !view.cursor)) return;
  if (!append && view.query !== id) {
    view.filter = "all";
    for (const item of document.querySelectorAll("[data-filter]")) { const active = item.dataset.filter === "all"; item.classList.toggle("active", active); item.setAttribute("aria-pressed", String(active)); }
  }
  view.controller?.abort();
  const controller = new AbortController(); view.controller = controller;
  const request = ++view.request;
  const cursor = append ? view.cursor : "";
  view.busy = true;
  $("queryButton").disabled = true; $("loadMore").disabled = true;
  if (!append) {
    view.data = null; view.messages = []; view.cursor = ""; view.query = id;
    clearOverview(); empty("正在读取会话", "原始消息与摘要正在从数据库读取…");
  }
  notice("");
  const timer = setTimeout(() => controller.abort(), 15000);
  try {
    const data = await get(`/api/v1/conversations/${encodeURIComponent(id)}/messages?execution=1&limit=100${cursor ? `&cursor=${encodeURIComponent(cursor)}` : ""}`, controller.signal);
    if (request !== view.request) return;
    if (!Array.isArray(data.messages) || data.conversation_id !== id) throw new Error("会话回执格式不正确，请稍后重试。");
    view.data = data;
    view.messages = append ? [...view.messages, ...data.messages] : data.messages;
    view.cursor = data.next_cursor || "";
    $("conversationId").value = id;
    const url = new URL(location.href); url.searchParams.set("conversation_id", id); history.replaceState(null, "", url);
    for (const item of document.querySelectorAll("#recentSessions .session-item")) item.classList.toggle("active", item.dataset.id === id);
    renderOverview(); renderMessages();
  } catch (error) {
    if (request !== view.request) return;
    notice(error.name === "AbortError" ? "读取超时，请点击查询重试。" : error.message, true);
    if (!append) { empty("暂时无法读取这段会话", "请检查会话编号，或稍后重试。已有消息不会受此影响。"); text("messageCount", "查询未完成"); }
    if (error.status === 409) { view.cursor = ""; $("loadMore").hidden = true; }
  } finally {
    clearTimeout(timer);
    if (request === view.request) {
      view.busy = false; $("queryButton").disabled = false; $("loadMore").disabled = false;
      $("refreshButton").disabled = !view.query;
    }
  }
}

async function loadRecent() {
  const controller = new AbortController(); const timer = setTimeout(() => controller.abort(), 15000);
  try {
    const data = {items:await window.TeacherChrome.loadSessions()};
    const sessions = new Map();
    for (const message of Array.isArray(data.items) ? data.items : []) {
      if (validId(message.conversation_id) && !sessions.has(message.conversation_id)) sessions.set(message.conversation_id, message);
    }
    window.TeacherChrome.renderSessions($("recentSessions"),window.TeacherChrome.sessionsFromMessages(data.items).map(s=>({...s,active:s.id===view.query})),loadConversation);
    if(!view.query && sessions.size)await loadConversation(sessions.keys().next().value);
  } catch { $("recentSessions").replaceChildren(element("p", "rail-note", "最近会话暂不可用，你仍可输入编号查询。")); }
  finally { clearTimeout(timer); }
}

$("queryForm").addEventListener("submit", event => { event.preventDefault(); loadConversation($("conversationId").value.trim()); });
$("refreshButton").addEventListener("click", () => loadConversation(view.query));
$("loadMore").addEventListener("click", () => loadConversation(view.query, true));
$("editLatestPrompt").addEventListener("click", editSharedPrompt);
for (const button of document.querySelectorAll("[data-filter]")) button.addEventListener("click", () => {
  view.filter = button.dataset.filter;
  for (const item of document.querySelectorAll("[data-filter]")) { item.classList.toggle("active", item === button); item.setAttribute("aria-pressed", String(item === button)); }
  renderMessages();
});
$("summaryVersion").addEventListener("change", () => {
  if ($("summaryVersion").value === "") { renderSummary(view.data.current_summary); return; }
  const entry = view.preview?.[Number($("summaryVersion").value)];
  if (entry) renderSummary(entry.summary, !entry.current);
});
let toastTimer;
$("copyId").addEventListener("click", async () => {
  try { await navigator.clipboard.writeText(view.data.conversation_id); text("toast", "会话编号已复制"); }
  catch { text("toast", "复制未成功，可在会话信息中选中编号复制。"); }
  $("toast").hidden = false; clearTimeout(toastTimer); toastTimer = setTimeout(() => { $("toast").hidden = true; }, 2500);
});
const initialId = new URLSearchParams(location.search).get("conversation_id");
if (initialId) { $("conversationId").value = initialId; loadConversation(initialId); }
loadRecent();
