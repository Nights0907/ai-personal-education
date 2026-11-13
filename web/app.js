/* 沐斯 AI：文字与拍照单题解答，正式回执驱动题目展示。 */
"use strict";

/* ================= 工具 ================= */
const $ = (sel) => document.querySelector(sel);
const uid = () => globalThis.crypto?.randomUUID?.() || Math.random().toString(36).slice(2, 10) + Date.now().toString(36);
const now = () => new Date();
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

function fmtTime(ts) {
  const d = new Date(ts);
  const p = (n) => String(n).padStart(2, "0");
  return `${d.getMonth() + 1}/${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`;
}

function escapeHtml(s) {
  return s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
}

/* 轻量 Markdown：标题 / 列表 / 加粗 / 行内码 / $$公式块$$ */
function mdLite(src) {
 if(globalThis.marked && globalThis.DOMPurify) {
  const html=DOMPurify.sanitize(marked.parse(String(src || ''),{gfm:true,breaks:false}),{FORBID_TAGS:['style','iframe','form'],FORBID_ATTR:['style']});
  const box=document.createElement('div');box.innerHTML=html;
  if(globalThis.renderMathInElement)renderMathInElement(box,{delimiters:[{left:'$$',right:'$$',display:true},{left:'\\[',right:'\\]',display:true},{left:'\\(',right:'\\)',display:false},{left:'$',right:'$',display:false}],throwOnError:false,trust:false});
  box.querySelectorAll('a').forEach(a=>{a.setAttribute('target','_blank');a.setAttribute('rel','noopener noreferrer');});
  return box.innerHTML;
 }

  const lines = escapeHtml(src).split("\n");
  const out = [];
  let listOpen = null;
  const closeList = () => { if (listOpen) { out.push(`</${listOpen}>`); listOpen = null; } };
  const inline = (s) => s
    .replace(/\*\*(.+?)\*\*/g, "<b>$1</b>")
    .replace(/`([^`]+)`/g, "<code>$1</code>");

  for (const raw of lines) {
    const line = raw.trimEnd();
    if (line.startsWith("$$")) {
      closeList();
      out.push(`<div class="formula">${line.slice(2).replace(/\$\$$/, "") || ""}</div>`);
      continue;
    }
    if (/^[-*]\s+/.test(line)) {
      if (listOpen !== "ul") { closeList(); out.push("<ul>"); listOpen = "ul"; }
      out.push(`<li>${inline(line.replace(/^[-*]\s+/, ""))}</li>`);
      continue;
    }
    if (/^\d+[.、]\s*/.test(line)) {
      if (listOpen !== "ol") { closeList(); out.push("<ol>"); listOpen = "ol"; }
      out.push(`<li>${inline(line.replace(/^\d+[.、]\s*/, ""))}</li>`);
      continue;
    }
    if (/^#{3,4}\s+/.test(line)) {
      closeList();
      out.push(`<h4>${inline(line.replace(/^#{3,4}\s+/, ""))}</h4>`);
      continue;
    }
    if (line === "") { closeList(); continue; }
    closeList();
    out.push(`<p>${inline(line)}</p>`);
  }
  closeList();
  return out.join("");
}

/* ================= 状态与持久化 ================= */
const STORE_KEY = "musi_web_submissions_v2";

const state = {
  sessions: [],        // { id, title, createdAt, messages: [] }
  activeId: null,
  problems: [],        // 我的题目 { id, stem, type, kp, time, versions }
  mode: "solve",       // solve | guide
  guideStep: {},       // sessionId -> 引导提示级别
};

function loadState() {
  try {
    const raw = localStorage.getItem(STORE_KEY);
    if (raw) Object.assign(state, JSON.parse(raw));
    // Empty sessions from older pages were local drafts, never issued IDs.
    for (const session of state.sessions) {
      if (!("draft" in session) && !session.messages?.length) session.draft = true;
    }
  } catch (_) { /* 忽略损坏数据 */ }
}
function saveState() {
  try { localStorage.setItem(STORE_KEY, JSON.stringify(state)); } catch (_) {}
}

/* ================= 原始提交 API ================= */
const API = {
  async request(path, options = {}, timeoutMs = 15000) {
    const controller = new AbortController();
    const timer = setTimeout(() => controller.abort(), timeoutMs);
    try {
      const response = await fetch(path, { ...options, signal: controller.signal, cache: "no-store" });
      const data = await response.json().catch(() => ({}));
      if (!response.ok) throw new Error(data.error || "暂时无法连接保存服务，请重试");
      return data;
    } finally { clearTimeout(timer); }
  },
  createConversation(requestID) {
    return this.request("/api/v1/conversations", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ client_request_id: requestID }) });
  },
  submit(sessionId, msg) {
    // The receipt is keyed by client_request_id and its hash covers the entry
    // mode, so submission must carry exactly the same fields as solve.
    const body = { query: msg.text, conversation_id: sessionId, client_request_id: msg.id, image_asset_id: msg.imageAssetID || undefined };
    if (msg.regenerateOf) body.regenerate_of = msg.regenerateOf;
    if (msg.mode) body.mode = msg.mode;
    if (msg.guideQuestionId) body.guide_question_id = msg.guideQuestionId;
    return this.request("/api/v1/messages", {
      method: "POST", headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
    });
  },
  async solve(sessionId, msg, onEvent) {
    const controller=new AbortController();const timer=setTimeout(()=>controller.abort(),210000);
    let reader;
    try {
      const body={query:msg.text,conversation_id:sessionId,client_request_id:msg.id,image_asset_id:msg.imageAssetID || undefined};
      if(msg.regenerateOf) body.regenerate_of=msg.regenerateOf;
      if(msg.mode) body.mode=msg.mode;
      const response=await fetch('/api/v1/agentic/chat',{method:'POST',headers:{'Content-Type':'application/json','Accept':'text/event-stream'},body:JSON.stringify(body),signal:controller.signal,cache:'no-store'});
      if(!response.ok){const data=await response.json().catch(()=>({}));throw new Error(data.error || '解题请求失败');}
      if(!response.headers.get('content-type')?.includes('text/event-stream') || !response.body)throw new Error('服务未返回解题事件流，请刷新后重试');
      reader=response.body.getReader();const decoder=new TextDecoder();let buffer='',official,guide,finished=false;
      const frame=(raw)=>{
        let name='message';const data=[];
        for(const line of raw.split(/\r?\n/)){if(line.startsWith('event:'))name=line.slice(6).trim();else if(line.startsWith('data:'))data.push(line.slice(5).replace(/^ /,''));}
        if(!data.length)return;const event=JSON.parse(data.join('\n'));
        if(name==='solution'){if(!event.committed)throw new Error('缺少正式提交回执');official=event;}
        if(name==='guide_start'){if(!event.question_id)throw new Error('引导启动回执无效');guide=event;}
        if(name==='error'){const e=new Error(event.error || '解题未完成');e.code=event.error_code;throw e;}
        if(name==='done'){if(event.status!=='succeeded' && event.status!=='guided')throw new Error('解题未完成，原题已保留');finished=true;}
        onEvent?.(name,event);
      };
      while(!finished){const {value,done}=await reader.read();buffer+=decoder.decode(value || new Uint8Array(),{stream:!done});if(buffer.length>2*1024*1024)throw new Error('解题事件超过大小限制');let match;while((match=/\r?\n\r?\n/.exec(buffer))){const raw=buffer.slice(0,match.index);buffer=buffer.slice(match.index+match[0].length);frame(raw);}if(done)break;}
      if(msg.mode==='guide'){if(!guide)throw new Error('连接已结束，尚未确认引导启动。请重试');return {guide};}
      if(!official)throw new Error('连接已结束，尚未确认解答保存。请用相同请求重试核对。');
      return {official};
    } finally {clearTimeout(timer);await reader?.cancel().catch(()=>{});}
  },
  async guideChat(sessionId, msg, guideQuestionId, onEvent) {
    const controller=new AbortController();const timer=setTimeout(()=>controller.abort(),120000);
    let reader;
    try {
      const response=await fetch('/api/v1/agentic/chat',{method:'POST',headers:{'Content-Type':'application/json','Accept':'text/event-stream'},body:JSON.stringify({query:msg.text,conversation_id:sessionId,client_request_id:msg.id,mode:msg.review?'review':'guide',guide_question_id:guideQuestionId,image_asset_id:msg.imageAssetID || undefined,regenerate_of:msg.regenerateOf || undefined}),signal:controller.signal,cache:'no-store'});
      if(!response.ok){const data=await response.json().catch(()=>({}));throw new Error(data.error || '引导请求失败');}
      if(!response.headers.get('content-type')?.includes('text/event-stream') || !response.body)throw new Error('服务未返回引导事件流，请刷新后重试');
      reader=response.body.getReader();const decoder=new TextDecoder();let buffer='',text='';
      const frame=(raw)=>{
        let name='message';const data=[];
        for(const line of raw.split(/\r?\n/)){if(line.startsWith('event:'))name=line.slice(6).trim();else if(line.startsWith('data:'))data.push(line.slice(5).replace(/^ /,''));}
        if(!data.length)return;const event=JSON.parse(data.join('\n'));
        if(name==='text'||name==='text_delta'){if(event.text){text=name==='text'?event.text:text+event.text;}}
        if(name==='error'){const e=new Error(event.error || '引导未完成');e.code=event.error_code;throw e;}
        onEvent?.(name,event);
      };
      while(true){const {value,done}=await reader.read();if(done)break;buffer+=decoder.decode(value||new Uint8Array(),{stream:true});if(buffer.length>2*1024*1024)throw new Error('引导事件超过大小限制');let match;while((match=/\r?\n\r?\n/.exec(buffer))){const raw=buffer.slice(0,match.index);buffer=buffer.slice(match.index+match[0].length);frame(raw);}}
      if(!text.trim())throw new Error('引导没有返回内容，请重试');
      return {text};
    } finally {clearTimeout(timer);await reader?.cancel().catch(()=>{});}
  },
  uploadImage(file) { const body=new FormData();body.append("image",file);return this.request("/api/v1/images",{method:"POST",body},30000); },
  questions() { return this.request("/api/v1/questions"); },
  question(id) { return this.request("/api/v1/questions/" + encodeURIComponent(id)); },
  list(before) { return this.request("/api/v1/messages?limit=100" + (before ? "&before=" + encodeURIComponent(before) : "")); },
};

/* ================= 渲染 ================= */
const chatInner = $("#chatInner");
const chatScroll = $("#chatScroll");

function el(html) {
  const t = document.createElement("template");
  t.innerHTML = html.trim();
  return t.content.firstElementChild;
}

function scrollBottom(force) {
  const near = chatScroll.scrollHeight - chatScroll.scrollTop - chatScroll.clientHeight < 140;
  if (near || force) chatScroll.scrollTop = chatScroll.scrollHeight;
}

function renderWelcome() {
  chatInner.innerHTML = "";
  chatInner.appendChild(el(`
    <div class="welcome">
      <div class="welcome-mark">沐</div>
      <h1>把不会的，变成会的。</h1>
      <p>一道题，一个新进步。发来你的数学题，我们一起理清思路。</p>
      <div class="welcome-cases">
        <button type="button" class="case-card" data-fill="解不等式：(x−1)/(x+2) > 0">
          <div class="c-tag">练一练 · 不等式</div>
          <div class="c-title">解分式不等式</div>
          <div class="c-desc">练习"同号转化"与定义域检验，含分类讨论。</div>
        </button>
        <button type="button" class="case-card" data-fill="已知 f(x) = x² − 4x + 3，求 f(x) 在 [0, 3] 上的最大值与最小值。">
          <div class="c-tag">练一练 · 函数</div>
          <div class="c-title">二次函数闭区间最值</div>
          <div class="c-desc">配方定对称轴，比较端点与顶点位置关系。</div>
        </button>
        <button type="button" class="case-card" data-fill="等差数列 {aₙ} 中，a₃ = 5，a₇ = 13，求通项 aₙ 及前 20 项和 S₂₀。">
          <div class="c-tag">练一练 · 数列</div>
          <div class="c-title">等差数列基本量</div>
          <div class="c-desc">方程思想求 a₁ 与 d，中项性质巧求和。</div>
        </button>

      </div>
    </div>
  `));
}

function submissionStatusHtml(msg) {
  // 引导学习中的作答消息：仅显示文本，不显示提交状态。
  if(msg.taskID) {
    const failed=msg.taskStatus==='FAILED';
    const label=failed?(msg.solveError || '任务暂未完成'):msg.taskStatus==='READY'?(msg.mode==='solve'?'讲解已保存，可随时回来复习。':msg.guide?'教师反馈已完成':'参考解答已准备，继续说说你的思路'):msg.guide?'作答已保存，等待参考解答就绪后核对':(msg.solveProgress || (msg.mode==='solve'?'AI教师解题中，请稍候~':'参考解答准备中，你可以先写思路'));
    return `<div class="submission-status ${failed?'failed':'saved'}" role="status">${escapeHtml(label)}${failed?` <button class="retry-submission" data-retry="${escapeHtml(msg.id)}" type="button">重试任务</button>`:''}</div>`;
  }
  if (msg.guide) return "";
  if (msg.saveStatus === "saved") {
    const detail = msg.solveStatus === "solving" ? `<div role="status">${escapeHtml(msg.solveProgress || "AI教师解题中，请稍候~")}</div>` : msg.solveStatus === "failed" ? `<div role="status">${escapeHtml(msg.solveError || "解题未完成，原文已保留。")} <button class="retry-submission" data-retry="${escapeHtml(msg.id)}" type="button">重试解题</button></div>` : msg.solveStatus === "completed" ? '<div>讲解已保存，可随时回来复习。</div>' : msg.solveStatus === "guided" ? '<div class="guide-status">题目已就绪，进入引导学习。说说你的思路，或直接开始解题，我会一步步引导你。</div>' : '<div>原题已保存。</div>';
    return `<div class="submission-status saved">✓ 原文已保存 <span class="submission-id">编号：${escapeHtml(msg.submission?.id || "")}</span>${detail}</div>`;
  }
  if (msg.saveStatus === "saving") return `<div class="submission-status" role="status">正在保存问题…</div>`;
  return `<div class="submission-status failed" role="status">尚未确认保存。${escapeHtml(msg.saveError || "可重试确认，原文已保留。")} <button class="retry-submission" data-retry="${escapeHtml(msg.id)}" type="button">重试保存</button></div>`;
}
function renderUserMessage(msg) {
  return el(`<div class="msg user" data-id="${escapeHtml(msg.id)}">
    <div class="msg-role"><span class="role-dot">我</span>我</div>
    <div class="msg-body">${msg.imageAssetID ? `<img class="question-image" data-zoom src="/api/v1/images/${encodeURIComponent(msg.imageAssetID)}/content" alt="题目原图">` : ""}<div class="query-text">${escapeHtml(msg.text)}</div>${submissionStatusHtml(msg)}</div>
  </div>`);
}

function updateLiveText(node, text) {
  text=String(text || '');
  const previous=node.textContent;
  if(previous===text)return;
  if(text.startsWith(previous) && node.childNodes.length===1 && node.firstChild.nodeType===3)node.firstChild.appendData(text.slice(previous.length));
  else node.textContent=text;
}
function renderSolveLive(msg) {
  const root=el(`<div class="msg assistant live-solve"><div class="msg-role"><span class="role-dot">师</span>沐斯教师</div><div class="msg-body"><div class="live-status" role="status"></div><div class="live-ocr" hidden><b>OCR 识别原文</b><div class="live-ocr-text live-plain"></div></div><div class="live-explanation live-plain" hidden></div><div class="live-draft" hidden><span class="live-badge">生成中草稿 · 待校验，尚未正式入库</span><section class="live-answer-section" hidden><h4>答案</h4><div class="live-answer live-plain"></div></section><section class="live-analysis-section" hidden><h4>解析</h4><div class="live-analysis live-plain"></div></section><section class="live-steps-section" hidden><h4>解题步骤</h4><div class="live-steps"></div></section></div><div class="live-issues" hidden><b>校验反馈</b><div class="live-issues-text live-plain"></div></div></div></div>`);
  patchSolveLive(root,msg);return root;
}
function patchSolveLive(root,msg) {
  const status=root.querySelector('.live-status');
  updateLiveText(status,msg.solveStatus==='failed' ? '本次解题未完成，原题已保留' : msg.solveProgress || '正在准备解题…');
  status.classList.toggle('failed',msg.solveStatus==='failed');
  const field=(selector,text,section=selector)=>{updateLiveText(root.querySelector(selector),text);root.querySelector(section).hidden=!text;};
  field('.live-ocr-text',msg.ocrText,'.live-ocr');
  const narrative=msg.streamDraft?.teacher_response || msg.streamText || '';
  const explanation=root.querySelector('.live-explanation'); explanation.hidden=!narrative;explanation.classList.add('markdown-body');explanation.classList.remove('live-plain');
  if(explanation.dataset.source!==narrative){explanation.innerHTML=mdLite(narrative);explanation.dataset.source=narrative;}

  const d=msg.streamDraft;const draft=root.querySelector('.live-draft');draft.hidden=true;
  if(d){
    field('.live-answer',(d.answer || []).map(a=>a.content || '').join('；'),'.live-answer-section');
    field('.live-analysis',d.analysis,'.live-analysis-section');
    const list=root.querySelector('.live-steps'),steps=d.steps || [];root.querySelector('.live-steps-section').hidden=!steps.length;
    steps.forEach((step,i)=>{let row=list.children[i];if(!row){row=el('<div class="live-step"><b></b><div class="live-plain"></div></div>');list.appendChild(row);}updateLiveText(row.querySelector('b'),`${i+1}. ${step.title || '解题步骤'}`);updateLiveText(row.querySelector('.live-plain'),step.content);});
    while(list.children.length>steps.length)list.lastElementChild.remove();
  }
  field('.live-issues-text',(msg.validationIssues || []).join('\n'),'.live-issues');
}

function renderAssistantShell(msg) {
  return el(`
    <div class="msg assistant" data-id="${msg.id}">
      <div class="msg-role"><span class="role-dot">师</span>沐斯教师</div>
      <div class="msg-body"></div>
    </div>
  `);
}

function renderPipeline(body, ev) {
  let box = body.querySelector(".pipeline");
  if (!box) {
    box = el(`<div class="pipeline"></div>`);
    body.appendChild(box);
  }
  box.innerHTML = ev.steps.map((s, i) => {
    let cls = "pipe-step";
    if (ev.failedStep === i) cls += " failed";
    else if (i < ev.active) cls += " done";
    else if (i === ev.active) cls += " running";
    return `<div class="${cls}"><span class="p-dot"></span>${s}</div>`;
  }).join("");
  scrollBottom();
}

function renderTextDelta(body, text) {
  let p = body.querySelector(".stream-text");
  if (!p) {
    p = el(`<div class="stream-text"></div>`);
    body.appendChild(p);
  }
  p.innerHTML = mdLite(text) + '<span class="cursor"></span>';
  scrollBottom();
}

function finalizeText(body, text) {
  const p = body.querySelector(".stream-text");
  if (p) { p.innerHTML = mdLite(text); return; }
  const div = el(`<div class="stream-text">${mdLite(text)}</div>`);
  body.appendChild(div);
}

function stableStreamingText(text){
 let end=text.length;for(const token of ['```','$$','\\[','\\(','$']){let positions=[];for(let i=0;(i=text.indexOf(token,i))>=0;i+=token.length){if(token==='$'&&(text[i-1]==='$'||text[i+1]==='$'||text[i-1]==='\\'))continue;positions.push(i);}if(token==='\\['||token==='\\('){const close=token==='\\['?'\\]':'\\)';if(text.lastIndexOf(token)>text.lastIndexOf(close))end=Math.min(end,text.lastIndexOf(token));}else if(positions.length%2)end=Math.min(end,positions.at(-1));}return text.slice(0,end);
}
function replyActionsHtml(){return `<div class="reply-actions"><button class="reply-icon" type="button" data-reply-action="copy" aria-label="复制回复" title="复制"><svg viewBox="0 0 24 24"><rect x="8" y="8" width="12" height="12" rx="2"/><path d="M16 8V4H4v12h4"/></svg></button><button class="reply-icon" type="button" data-reply-action="regen" aria-label="重新生成回复" title="重新生成"><svg viewBox="0 0 24 24"><path d="M20 7v5h-5M4 17v-5h5M6 7a8 8 0 0 1 13 0l1 2M4 15l1 2a8 8 0 0 0 13 0"/></svg></button></div>`;}
async function copyTeacherReply(text){try{await navigator.clipboard.writeText(text);toast('已复制');}catch(_){toast('复制失败，请检查浏览器剪贴板权限。');}}
function solutionCardHtml(payload, status) {
  const narrative=payload.teacherResponse || [payload.analysis,...payload.steps.map(s=>`**${s.title}**\n\n${s.detail}`),`因此，${payload.answer.overall}`].filter(Boolean).join('\n\n');
  return `<div class="teacher-response markdown-body">${mdLite(narrative)}</div>`;
}

function guideBubbleHtml(g) {
  return `
    <div class="guide-hint">
      <span class="hint-level">引导 · ${escapeHtml(g.level)}</span>
      ${mdLite(g.text)}
      ${g.done ? `<div style="margin-top:8px;font-size:12px;color:var(--amber)">本轮引导到此结束，可切换「直接解答」对照完整解答。</div>` : ""}
    </div>`;
}

// 引导学习模式的老师消息：带「引导」标签的普通文本气泡。
// A terminal storage receipt is execution data, never a teacher narrative.
function teacherNarrative(text,hideAnswer=false){
 const raw=String(text || '');let value;try{value=JSON.parse(raw.trim().replace(/^```(?:json)?\s*|\s*```$/g,''));}catch(_){return raw;}
 if(value && typeof value==='object' && value.committed===true && (value.question_id || value.solution_id))return hideAnswer?'':String(value.solution?.teacher_response || '讲解已保存，可打开题目查看。');
 return raw;
}
function guideMessageHtml(text) {
  return `<div class="guide-hint"><span class="hint-level">引导学习</span>${mdLite(teacherNarrative(text,true))}</div>`;
}

function renderSolutionCard(body, msg) {
  // 清除管线动画与流式文本
  body.querySelector(".pipeline")?.remove();
  body.querySelector(".stream-text")?.remove();
  body.insertAdjacentHTML("beforeend", solutionCardHtml(msg.solution, msg.status));
  bindSolutionActions(body, msg);
}

function renderGuideBubble(body, msg) {
  body.querySelector(".pipeline")?.remove();
  body.insertAdjacentHTML("beforeend", guideBubbleHtml(msg.guide));
}

function bindSolutionActions(body, msg) {
  body.querySelectorAll(".sol-btn").forEach((btn) => {
    btn.addEventListener("click", () => {
      const act = btn.dataset.act;
      if (act === "guide") {
        setMode("guide");
        toast("已切换到引导学习模式，告诉我你想从哪一步开始");
        $("#input").focus();
      } else if (act === "regen") {
        regenerateReply(msg.id);
      } else if (act === "report") {
        toast("反馈通道尚未接入，请先保留题目编号。");
      }
    });
  });
}

function toast(text) {
  const t = $("#toast");
  t.textContent = text;
  t.hidden = false;
  clearTimeout(toast._tm);
  toast._tm = setTimeout(() => { t.hidden = true; }, 2600);
}

/* ================= 会话渲染 ================= */
function activeSession() {
  return state.sessions.find((s) => s.id === state.activeId) || null;
}

function renderSessionList() {
  const current = activeSession();
  if(current){current.title=window.TeacherChrome.topicTitle(current.messages.find(m=>m.role==='user')?.text || current.title);$('#topbarTitle').textContent=current.title;}
  $("#btnConversationHistory").href = current && !current.draft ? `conversation.html?conversation_id=${encodeURIComponent(current.id)}` : "conversation.html";
  const list = $("#sessionList");
  list.innerHTML = "";
  if (!state.sessions.length) {
    list.innerHTML = `<div class="session-empty">暂无会话，点击上方「新对话」开始</div>`;
    return;
  }
  window.TeacherChrome.renderSessions(list,state.sessions.map(s=>({...s,active:s.id===state.activeId})),switchSession,deleteSession);
}

// Message nodes remain mounted across streaming updates, including photos.
let chatFollowTail=true;
chatScroll.addEventListener('scroll',()=>{chatFollowTail=chatScroll.scrollHeight-chatScroll.clientHeight-chatScroll.scrollTop<=32;},{passive:true});
function executionNodesHtml(msg){
 const nodes=msg.executionNodes || [];
 const paths={skill:'M4 4h6a3 3 0 0 1 3 3v13a4 4 0 0 0-4-2H4z M20 4h-4a3 3 0 0 0-3 3 M20 4v14h-3',tool:'m8 8-4 4 4 4m8-8 4 4-4 4m-3-10-2 20',storage:'M5 4h14v16H5z M8 4v6h8V4 M8 20v-6h8v6',memory:'M8 4a4 4 0 0 0-4 4v8a4 4 0 0 0 4 4m8-16a4 4 0 0 1 4 4v8a4 4 0 0 1-4 4 M8 8h8v8H8z M12 2v6m0 8v6 M2 12h6m8 0h6',context:'M20 7v5h-5 M4 17v-5h5 M5 8a8 8 0 0 1 13-3l2 2 M4 17l2 2a8 8 0 0 0 13-3'};
 const icon=kind=>`<svg viewBox="0 0 24 24" width="15" height="15" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="${paths[kind] || paths.tool}"/></svg>`;
 const labels={running:'进行中',completed:'已完成',failed:'未完成',pending:'等待处理',needs_revision:'需调整',recorded:'已调用'};
 const names={get_task_input:'读取题目',get_knowledge_catalog:'读取知识点目录',validate_solution:'检查解答格式',submit_solution:'提交并保存解答',finalize_solution:'保存解答',publish_guiding_question:'发布引导问题',evaluate_student_attempt:'核验学生作答',reveal_solution:'核对并展示解答',recognize_input_image:'识别题目图片',recognize_review_image:'识别学生作答',get_guide_outline:'读取引导提纲',get_review_question:'读取复习题目',read_file:'读取资料','math-solve':'读取解题指南','step-tutor':'加载逐步引导方法','review-recall':'加载复习检验方法','student-memory':'加载学习记忆指南'};
 const rows=nodes.map(n=>`<li class="execution-node ${escapeHtml(n.status)}"><span class="node-icon" aria-hidden="true">${icon(n.kind)}</span><span class="node-label">${escapeHtml(names[n.name] || (n.kind==='skill'?'加载教学技能':n.kind==='tool'?'使用辅助工具':n.name))}${n.kind==='tool' || n.kind==='skill'?`<span class="node-separator" aria-hidden="true">·</span><code title="${escapeHtml(n.name)}">${escapeHtml(n.name)}</code>`:''}</span><span class="node-state">${labels[n.status] || '已记录'}</span></li>`).join('');
 return rows+(nodes.length?(msg.activity || []).filter(item=>item.includes('对话整理') || item.includes('整理之前')):[...new Set(msg.activity || [])]).map(item=>`<li class="execution-note"><span aria-hidden="true">↻</span> ${escapeHtml(item)}</li>`).join('');
}

function renderMessages() {
  const session=activeSession();
  const modeSwitch=document.querySelector('#modeSwitch');
  if(modeSwitch){modeSwitch.hidden=!!session?.reviewQuestionId;let badge=document.querySelector('#feynmanMode');if(!badge){badge=document.createElement('span');badge.id='feynmanMode';badge.className='feynman-mode';badge.textContent='费曼验证 · 先复述，再核对';modeSwitch.after(badge);}badge.hidden=!session?.reviewQuestionId;}
  const changedSession=chatInner.dataset.sessionId!==(session?.id || '');
  const top=chatScroll.scrollTop,follow=changedSession || chatFollowTail;
  if(changedSession){chatInner.replaceChildren();chatInner.dataset.sessionId=session?.id || '';}
  if(!session || !session.messages.length){if(!chatInner.querySelector('.welcome'))renderWelcome();return;}
  chatInner.querySelector('.welcome')?.remove();
  const existing=new Map(Array.from(chatInner.children).map(node=>[node.dataset.key,node]));
  const wanted=[];
  const mount=(key,create)=>{let node=existing.get(key);if(!node){node=create();node.dataset.key=key;}wanted.push(node);return node;};
  for(const msg of session.messages){
    if(msg.role==='user'){
      const node=mount('user:'+msg.id,()=>renderUserMessage(msg));
      const status=submissionStatusHtml(msg);
      if(node._submissionHTML!==status){const prior=node.querySelector('.submission-status');if(status){const next=el(status);if(prior)prior.replaceWith(next);else node.querySelector('.msg-body').appendChild(next);}else prior?.remove();node._submissionHTML=status;}
      if(!msg.taskID && msg.solveStatus!=='completed' && (msg.solveStatus==='solving' || msg.streamText || msg.streamDraft || msg.ocrText)){
        const live=mount('live:'+msg.id,()=>renderSolveLive(msg));patchSolveLive(live,msg);
      }
    }else{
      const node=mount('assistant:'+msg.id,()=>renderAssistantShell(msg));
      const signature=JSON.stringify([msg.type,msg.text,msg.status,msg.solution,msg.guide,msg.delivery,msg.streaming,msg.activity,msg.activityPending,msg.executionNodes]);
      if(node._contentSignature!==signature){
        const body=node.querySelector('.msg-body');
        if(msg.type==='solution' && msg.solution){body.innerHTML='';renderSolutionCard(body,msg);}
        else if(msg.type==='guide'){body.innerHTML=(msg.delivery || session?.reviewQuestionId || msg.review)?`<div class="review-opening markdown-body">${msg.delivery?'<div class="review-task-label">◈ 防遗忘复习 · 费曼验证</div>':''}${mdLite(msg.streaming?stableStreamingText(msg.text || ''):msg.text || '')}</div>`:guideMessageHtml(msg.streaming?stableStreamingText(msg.text || ''):msg.text);}
        else{body.innerHTML=mdLite(teacherNarrative(msg.streaming?stableStreamingText(msg.text || ''):msg.text));}
        if(msg.activity?.length || msg.executionNodes?.length){
          const details=el(`<details class="teacher-activity"><summary><span class="activity-icon" aria-hidden="true">✦</span><span>${msg.activityPending?'正在准备讲解':'解题进度'}</span><span class="activity-toggle" aria-hidden="true">⌄</span></summary><ol>${executionNodesHtml(msg)}</ol></details>`);
          details.open=msg.activityOpen ?? msg.activityPending;
          details.addEventListener('toggle',()=>{msg.activityOpen=details.open;});
          body.prepend(details);
        }
        if(!msg.delivery){const actions=el(replyActionsHtml());actions.querySelector('[data-reply-action="copy"]').onclick=()=>copyTeacherReply(teacherNarrative(msg.text || msg.solution?.teacherResponse || ''));actions.querySelector('[data-reply-action="regen"]').onclick=()=>regenerateReply(msg.id);body.append(actions);}
        node._contentSignature=signature;
      }
    }
  }
  const keep=new Set(wanted);for(const node of Array.from(chatInner.children))if(!keep.has(node))node.remove();
  wanted.forEach((node,i)=>{if(chatInner.children[i]!==node)chatInner.insertBefore(node,chatInner.children[i] || null);});
  if(session?.replyVersions?.length){const last=[...chatInner.querySelectorAll('.msg.assistant')].at(-1)?.querySelector('.reply-actions');if(last){const nav=el(`<span class="reply-pages"><button aria-label="上一个回复版本">‹</button><span>${(session.replyVersion ?? session.replyVersions.length)+1} / ${session.replyVersionCount || session.replyVersions.length+1}</span><button aria-label="下一个回复版本">›</button></span>`);const current=session.replyVersion ?? session.replyVersions.length;const move=delta=>{const target=current+delta;if(target<0||target>=(session.replyVersionCount || session.replyVersions.length+1))return;session.replyVersions[current]=JSON.parse(JSON.stringify(session.messages));session.messages=JSON.parse(JSON.stringify(session.replyVersions[target]));session.replyVersion=target;saveState();renderMessages();};nav.querySelectorAll('button')[0].disabled=current===0;nav.querySelectorAll('button')[1].disabled=current>=(session.replyVersionCount || session.replyVersions.length+1)-1;nav.querySelectorAll('button')[0].onclick=()=>move(-1);nav.querySelectorAll('button')[1].onclick=()=>move(1);last.querySelector('.reply-pages')?.remove();last.append(nav);}}
  bindImageZoom();
  // No smooth scroll or scrollIntoView while streaming. Reading old content
  // preserves its position; following the tail is a single immediate update.
  chatScroll.scrollTop=follow ? chatScroll.scrollHeight : top;
  chatFollowTail=follow;
}

function bindImageZoom() {
  chatInner.querySelectorAll("img[data-zoom]").forEach((img) => {
    img.onclick = () => {
      const lb = el(`<div class="lightbox"><img src="${img.src}" alt=""></div>`);
      lb.onclick = () => lb.remove();
      document.body.appendChild(lb);
    };
  });
}

/* ================= 发送与事件流 ================= */
let sending = false;
let asyncTeaching = true;
let modeChosen = false;
const taskWatchers = new Set();
let inFlightRequestID = null;

const conversationCreations = new Map();
async function ensureServerConversation(session) {
  // Older local sessions may already have accepted messages. Keep their IDs.
  if (!session.draft) return;
  session.creationRequestID ||= session.id;
  saveState();
  const requestID=session.creationRequestID;
  let pending=conversationCreations.get(requestID);
  if(!pending){pending=API.createConversation(requestID);conversationCreations.set(requestID,pending);}
  let created;
  try{created=await pending;}finally{if(conversationCreations.get(requestID)===pending)conversationCreations.delete(requestID);}
  if(!session.draft)return;
  if (!/^c_[0-9a-f]{32}$/.test(created.conversation_id)) throw new Error("会话创建回执无效，请重试");
  const previous = session.id;
  session.id = created.conversation_id;
  session.draft = false;
  if (state.activeId === previous) state.activeId = session.id;
  state.guideStep[session.id] = state.guideStep[previous] || 0;
  delete state.guideStep[previous];
  saveState();
}

async function persistQuery(session, msg) {
  inFlightRequestID = msg.id;
  msg.saveStatus = "saving";
  msg.saveError = "";
  if (state.activeId === session.id) renderMessages();
  saveState();
  try {
    await ensureServerConversation(session);
    if(asyncTeaching && !msg.review) {await persistAsyncGuide(session,msg);return;}
    if (msg.guide || msg.review) { await persistGuide(session, msg); return; }
    const record = await API.submit(session.id, msg);
    // Only the server receipt establishes saved status. Keep the original text.
    if ((record.image_asset_id || "") !== (msg.imageAssetID || "") || record.query !== msg.text || record.client_request_id !== msg.id || !["RECEIVED", "ANSWERED"].includes(record.status)) throw new Error("保存回执与原始输入不一致，请重试确认");
    msg.serverMessageID = record.id;
    msg.submission = record;
    msg.saveStatus = "saved";
    msg.solveStatus = "solving"; msg.solveError = "";
    saveState(); if (state.activeId === session.id) renderMessages();
    msg.streamText="";msg.streamDraft=null;msg.validationIssues=[];msg.solveProgress="AI教师解题中，请稍候~";
    let paintTimer;
    const paint=()=>{if(state.activeId===session.id)renderMessages();};
    let result;
    try { result = await API.solve(session.id, msg, (name,event)=>{
      if(name==='progress'){msg.solveProgress=event.text;msg.solveStage=event.stage;}
      if(name==='text_delta')msg.streamText+=(event.text || "");
      if(name==='draft_reset')msg.nextDraftPending=true;
      if(name==='draft'){msg.streamDraft=event.data;msg.nextDraftPending=false;}
      if(name==='ocr')msg.ocrText=event.text;
      if(name==='validation'){msg.validationIssues=event.data?.issues || [];msg.solveProgress=event.data?.passed ? "正在为你保存讲解~" : "正在整理讲解~";}
      if(['progress','text_delta','draft','ocr','validation'].includes(name) && !paintTimer)paintTimer=setTimeout(()=>{paintTimer=null;paint();},80);
    }); } finally {clearTimeout(paintTimer);}

    if (msg.mode === "guide") {
      // 引导学习：答案已入库但不展示，进入引导。
      const g = result.guide;
      msg.saveStatus = "saved";
      msg.guideQuestionId = g.question_id;
      msg.guideStem = g.stem;
      session.guideQuestionId = g.question_id;
      msg.solveStatus = "guided";
      saveState(); if (state.activeId === session.id) renderMessages();
      await persistGuide(session, {id: msg.id + "-guide-start", text: "请从第一个问题开始引导我。", guide: true});
      return;
    }

    const official = result.official;
    if (!official.committed || !official.question_id || official.source_message_id !== record.id) throw new Error("解答提交回执不一致，请重试核对。");
    attachOfficialSolution(official);
    msg.solveStatus = "completed";
  } catch (err) {
    if (msg.submission) {
      msg.saveStatus = "saved"; msg.solveStatus = "failed";
      msg.solveError = err.name === "AbortError" ? "等待解答超时，请使用重试解题核对结果。" : err.message;
    } else {
    msg.saveStatus = "failed";
    msg.saveError = err.name === "AbortError" ? "等待保存回执超时，请重试确认。" : err instanceof TypeError ? "连接保存服务失败，请重试。" : err.message;
    }
  } finally {
    inFlightRequestID = null;
    saveState();
    if (state.activeId === session.id) renderMessages();
    renderSessionList();
  }
}

// persistGuide 处理引导会话中的学生作答：走 guide chat，模型一步步引导，
// 答对后呈现答案。guideChat 端点本身会保存原始输入并运行引导。
async function persistGuide(session, msg) {
  msg.saveStatus = "saved";
  saveState(); if (state.activeId === session.id) renderMessages();
  const guideQuestionId = msg.guideQuestionId || (msg.review ? session.reviewQuestionId : session.guideQuestionId);
  let text = "";
  let paintTimer;
  const paint = () => { if (state.activeId === session.id) renderMessages(); };
  try {
    const r = await API.guideChat(session.id, msg, guideQuestionId, (name, event) => {
      if(name==='accepted' && event.message_id) {msg.serverMessageID=event.message_id;msg.submission={id:event.message_id,turn_id:event.turn_id};}
      if (name === "text" || name === "text_delta") { if (event.text) { text += event.text; msg.streamText = text; } }
      if (!paintTimer) paintTimer = setTimeout(() => { paintTimer = null; paint(); }, 80);
    });
    text = r.text || text;
  } finally { clearTimeout(paintTimer); }
  const answer = { id: uid(), role: "assistant", type: msg.review?"text":"guide", review:!!msg.review,text };
  session.messages.push(answer);
  msg.solveStatus = "completed";
  saveState(); if (state.activeId === session.id) renderMessages();
}
async function send() {
  const input = $("#input");
  const originalText=input.value;
  if(activeSession()?.replyVersions?.length && activeSession().replyVersion<(activeSession().replyVersionCount || activeSession().replyVersions.length+1)-1){toast('当前查看历史版本，请切换到最新版本继续，或从此版本重新生成。');return;}
  const photoFile=attachedFile;
  const text = input.value.trim() ? input.value : attachedFile ? "[图片题目]" : "";
  if (!text.trim() || sending) return;
  if (Array.from(text).length > 20000) { toast("文字题目最多 20000 字"); return; }
  sending = true;
  chatFollowTail=true;
  updateSendState();
  const session = activeSession() || createSession(false);
  let imageAssetID;
  try {
    if (photoFile) { const asset=await API.uploadImage(photoFile);imageAssetID=asset.asset_id;if(!/^im_[0-9a-f]{32}$/.test(imageAssetID))throw new Error("图片保存回执无效"); }
  } catch(e) {sending=false;updateSendState();toast(e.message || "图片上传失败，请重试");return;}
  if (!session.messages.length) {
    session.title = window.TeacherChrome.topicTitle(text);
    if(state.activeId===session.id)$("#topbarTitle").textContent = session.title;
  }
  const msg = { id: uid(), role: "user", text, imageAssetID, saveStatus: "saving" };
  if (session.reviewQuestionId) { msg.review = true;msg.mode='review'; }
  else if (session.taskOverride === "solve") {msg.mode="solve";delete session.guideTaskID;delete session.guideQuestionId;}
  else if (session.guideTaskID || session.guideQuestionId || state.mode === "guide") {
    // 引导模式：若当前会话已进入引导（有 guideQuestionId），本次是引导作答；
    // 否则是引导学习的首次发题。
    if (session.guideTaskID || session.guideQuestionId) msg.guide = true;
    else msg.mode = "guide";
  }
  session.messages.push(msg);
  if(attachedFile===photoFile)clearAttachment();
  if(input.value===originalText)input.value = "";
  autoResize();
  updateSendState();
  try { await persistQuery(session, msg); }
  finally { sending = false; updateSendState(); }
}
async function retrySubmission(id) {
  if (sending) return;
  const session = activeSession();
  const msg = session?.messages.find(m => m.id === id && m.role === "user");
  if (!msg || (msg.saveStatus === "saved" && msg.solveStatus !== "failed")) return;
  sending = true;
  chatFollowTail=true;
  updateSendState();
  try {
    if(msg.taskID){const t=await API.request(`/api/v1/teaching/tasks/${encodeURIComponent(msg.taskID)}/retry`,{method:'POST'});applyTeachingTask(t,[]);watchTeachingTask(t.id);}
    else await persistQuery(session,msg);
  }
  finally { sending = false; updateSendState(); }
}
async function restoreSubmissions() {
  // Capture the startup session; do not switch away from a newly typed question.
  const initialId = state.activeId;
  const initialSession = activeSession();
  const initiallyEmpty = initialSession && !initialSession.messages.length;
  try {
    if(asyncTeaching) await restoreTeachingTasks();
    let before;
    const records = [];
    do {
      const page = await API.list(before);
      records.push(...page.items);
      before = page.next_cursor;
    } while (before);
    for (const record of records.reverse()) {
      if((state.deletedSessions || []).includes(record.conversation_id))continue;
      let session = state.sessions.find(s => s.id === record.conversation_id);
      if (!session) {
        session = { id: record.conversation_id, title: record.query.trim().slice(0, 24), createdAt: Date.parse(record.created_at), messages: [] };
        state.sessions.push(session);
      }
      if(session.replyVersions?.some(version=>version.some(m=>m.id===record.client_request_id)) && !session.messages.some(m=>m.id===record.client_request_id))continue;
      let msg = session.messages.find(m => m.id === record.client_request_id);
      if (!msg) { msg = { id: record.client_request_id, role: "user", text: record.query }; session.messages.push(msg); }
      if(record.mode==='review' && record.client_request_id.startsWith('recall-')){msg.role='assistant';msg.type='guide';msg.delivery=true;}
      msg.imageAssetID = record.image_asset_id;
      msg.serverMessageID = record.id;
      if(record.regenerate_of) msg.regenerateOf=record.regenerate_of;
      if (record.mode) msg.mode = record.mode;
      if (record.mode === "solve") {delete session.guideTaskID;delete session.guideQuestionId;delete session.reviewQuestionId;}
      if (record.mode === "guide" && record.guide_question_id) {session.guideQuestionId = record.guide_question_id;msg.guide=true;}
      if (record.mode === "review" && record.guide_question_id) {session.reviewQuestionId=record.guide_question_id;msg.review=true;}
      msg.submission = record;
      msg.saveStatus = "saved";
      msg.saveError = "";
    }
    for (const session of state.sessions) {
      for (const msg of session.messages) {
        if (msg.role === "user" && msg.saveStatus === "saving" && msg.id !== inFlightRequestID) {
          msg.saveStatus = "failed";
          msg.saveError = "上次尚未收到保存回执，请重试确认。";
        }
      }
    }
    if (records.length && initiallyEmpty && state.activeId === initialId && !initialSession.messages.length && !$("#input").value) state.activeId = records[records.length - 1].conversation_id;
    await restoreQuestions();
    const restored = await Promise.allSettled(state.sessions.filter(s => !s.draft).map(restoreTeacherMessages));
    if (restored.some(r => r.status === "rejected")) toast("部分教师回复暂时读取失败，已保留本地记录，请刷新重试。");
    saveState(); renderSessionList(); renderMessages();
    const requested=new URLSearchParams(location.search).get('conversation_id');if(requested && state.sessions.some(s=>s.id===requested)){state.activeId=requested;renderMessages();}
    if(!modeChosen){const active=activeSession();state.mode=active?.taskOverride || (active?.guideTaskID || active?.guideQuestionId ? 'guide':'solve');document.querySelectorAll('.mode-btn').forEach(b=>b.classList.toggle('active',b.dataset.mode===state.mode));}
    $("#topbarTitle").textContent = activeSession()?window.TeacherChrome.topicTitle(activeSession().messages.find(m=>m.role==='user')?.text || activeSession().title):"新对话";
  } catch (_) { toast("暂时无法读取已保存的问题；本地内容已保留，请确认服务已启动。"); }
}

// Read durable teacher replies, including turns already covered by a summary.
// Tool results are never rendered here: committed solutions have their own cards.
async function restoreTeacherMessages(session) {
  const stored = [],traces=[];
  let cursor = "";
  do {
    const page = await API.request(`/api/v1/conversations/${encodeURIComponent(session.id)}/messages?execution=1&limit=100${cursor ? "&cursor=" + encodeURIComponent(cursor) : ""}`);
    stored.push(...(page.messages || []));traces.push(...(page.execution_traces || []));
    cursor = page.next_cursor || "";
  } while (cursor);
  const executionByTurn=new Map();
  for(const trace of traces)for(const event of trace.events || []){if(event.type!=='execution_node')continue;const turn=event.data?.turn_id || trace.turn_id;if(!turn)continue;if(!executionByTurn.has(turn))executionByTurn.set(turn,new Map());executionByTurn.get(turn).set(event.data.id || event.id,event.data);}
  const replies = new Map();
  for (const item of stored) {
    if (item.role !== "assistant" || item.status !== "SEALED" || !item.in_active_chain) continue;
    const text = (item.content?.items || []).flatMap(m=>{
      if(m.role==='assistant' && !m.tool_calls?.length && typeof m.content==='string')return [teacherNarrative(m.content,session.messages.some(x=>x.submission?.turn_id===item.turn_id && x.mode==='guide'))];
      if(m.role==='tool' && m.tool_name==='publish_guiding_question'){try{const v=JSON.parse(m.content);return typeof v.question==='string'?[v.question]:[];}catch(_){return [];}}
      return [];
    }).filter(Boolean).join("\n\n");
    if (text) replies.set(item.turn_id, {id: item.message_id, serverMessageID: item.message_id, role: "assistant", type: session.messages.some(m => m.submission?.turn_id === item.turn_id && m.mode === "guide") ? "guide" : "text", text});
  }
  const messages = [];
  for (let i = 0; i < session.messages.length; i++) {
    const msg = session.messages[i];
    messages.push(msg);
    if (msg.role !== "user") continue;
    const reply = replies.get(msg.submission?.turn_id);
    if (!reply) continue;
    let end = i + 1;
    while (end < session.messages.length && session.messages[end].role !== "user") end++;
    const local = session.messages.slice(i + 1, end);
    // Official solution cards remain the single representation of solve turns.
    if (local.some(m => m.type === "solution")) continue;
    const prior=local.find(m=>m.executionNodes?.length || m.activity?.length);
    reply.executionNodes=[...(executionByTurn.get(msg.submission?.turn_id)?.values() || [])];
    if(!reply.executionNodes.length)reply.executionNodes=prior?.executionNodes || msg.executionNodes || [];
    reply.activity=prior?.activity || msg.activity || [];
    messages.push(reply);
    messages.push(...local.filter(m => m.type !== "guide" && m.type !== "text" && m.id !== reply.id));
    i = end - 1;
  }
  session.messages = messages;
}

function fileToDataUrl(file) {
  return new Promise((resolve, reject) => {
    const r = new FileReader();
    r.onload = () => resolve(r.result);
    r.onerror = reject;
    r.readAsDataURL(file);
  });
}

const knowledgeNames = {sets_logic:"集合与逻辑",functions_derivatives:"函数与导数",sequences:"数列",trigonometry:"三角函数",analytic_geometry:"平面解析几何",solid_geometry:"立体几何",probability_statistics:"概率与统计",equations_inequalities:"方程与不等式"};
function officialPayload(v) {
  const tags = v.solution.knowledge_points || [];
  return { questionId:v.question_id,stem:v.question.stem,type:({single_choice:"选择",multiple_choice:"选择",fill_blank:"填空",constructed_response:"解答"})[v.question.type] || "解答", answer:{overall:v.solution.answer.map(a => a.content).join("；"),subs:[]},teacherResponse:v.solution.teacher_response || "",analysis:v.solution.analysis,steps:v.solution.steps.map(s => ({id:s.step_id,title:s.title,detail:s.content,sub:s.subquestion_id})),kp:{main:knowledgeNames[tags.find(k => k.is_primary)?.code] || "高中数学",aux:tags.filter(k => !k.is_primary).map(k => knowledgeNames[k.code] || k.code)}};
}
function attachOfficialSolution(v) {
  if (!v.committed) throw new Error("没有正式提交回执");
  if((state.deletedSessions || []).includes(v.conversation_id))return;
  let session = state.sessions.find(s => s.id === v.conversation_id);
  if (!session) { session={id:v.conversation_id,title:v.question.stem.slice(0,24),createdAt:Date.parse(v.created_at),messages:[]};state.sessions.push(session); }
  const user=session.messages.find(m => m.serverMessageID===v.source_message_id || m.submission?.id===v.source_message_id);
  const payload=officialPayload(v);
  const problem={id:v.question_id,stem:payload.stem,type:payload.type,kp:payload.kp.main,time:Date.parse(v.created_at),versions:1};
  const problemAt=state.problems.findIndex(p=>p.id===v.question_id);if(problemAt>=0)state.problems[problemAt]=problem;else state.problems.unshift(problem);
  $("#problemCount").textContent=state.problems.length;
  if (user) {user.solveStatus="completed";user.solveError="";}
  // 引导学习会话：刷新后仍需知道当前引导的是哪道题。
  if (user && user.mode === "guide") {
    user.guideQuestionId=v.question_id;
    const sourceTime=Date.parse(v.created_at);
    if(session.taskOverride!=='solve' && (session.guideTaskID?user.taskID===session.guideTaskID:!session.guideSourceTime || sourceTime>=session.guideSourceTime)){session.guideQuestionId=v.question_id;session.guideSourceTime=sourceTime;}
    user.solveStatus = "guided";
    delete user.streamDraft;
    delete user.streamText;
    session.messages = session.messages.filter(m => m.solutionId !== v.solution_id);
    return;
  }
  if(user?.taskID){const existing=session.messages.find(m=>m.fromTask===user.taskID);if(existing){existing.text=v.solution.teacher_response || existing.text;return;}}
  let answer=session.messages.find(m => m.solutionId===v.solution_id);
  if (!answer) {answer={id:v.solution_id,solutionId:v.solution_id,role:"assistant",type:"solution",status:"saved"};const at=user ? session.messages.indexOf(user)+1 : session.messages.length;session.messages.splice(at,0,answer);}
  answer.solution=payload;answer.text=`题目编号：${v.question_id}` + (v.input_type === "image" ? `\nOCR 识别原文：${v.original_text}` : "");

}
async function restoreQuestions() {
  try { const data=await API.questions();state.problems=[];for(const v of data.items)attachOfficialSolution(v);state.problems.sort((a,b)=>b.time-a.time); }
  catch (_) { state.problems=state.problems.filter(p=>/^q_[0-9a-f]{32}$/.test(p.id)); }
  $("#problemCount").textContent=state.problems.length;
  // 档案页题目链接（?question_id=xxx）跳转后自动定位到对应题目。
  await openQuestionFromURL();
}

/* ================= 我的题目 ================= */
// 按题目编号打开对应会话并展示解答；供「我的题目」列表与档案页题目链接复用。
async function openQuestionById(id) {
  try {
    const official = await API.question(id);
    attachOfficialSolution(official);
    state.activeId = official.conversation_id;
    closeDrawer(); renderSessionList(); renderMessages(); saveState();
    $("#topbarTitle").textContent = activeSession()?.title || "题目详情";
    return true;
  } catch (err) {
    toast(err.message || "无法打开该题目");
    return false;
  }
}

// 读取 URL 参数（?question_id=xxx），在题目数据加载完成后自动定位到对应题目。
async function openQuestionFromURL() {
  const qid = new URLSearchParams(location.search).get("question_id");
  if (!qid || !/^q_[0-9a-f]{32}$/.test(qid)) return;
  await openQuestionById(qid);
  // 打开后清理地址栏参数，避免刷新重复定位。
  if (history.replaceState) {
    history.replaceState(null, "", location.pathname);
  }
}

function addProblem(payload) {
  state.problems.unshift({
    id: uid(),
    stem: payload.stem,
    type: payload.type,
    kp: payload.kp.main,
    time: Date.now(),
    versions: 1,
  });
  $("#problemCount").textContent = state.problems.length;
  saveState();
  if ($("#problemDrawer").classList.contains("open")) renderProblems();
}

function renderProblems() {
  const list = $("#problemList");
  const kw = $("#problemSearch").value.trim();
  const type = $("#problemTypeChips .chip.active").dataset.type;
  const items = state.problems.filter((p) =>
    (type === "all" || p.type === type) &&
    (!kw || p.stem.includes(kw) || p.kp.includes(kw))
  );
  if (!items.length) {
    list.innerHTML = `<div class="drawer-empty">${state.problems.length ? "没有符合条件的题目" : "解出的题目会自动收录到这里"}</div>`;
    return;
  }
  list.innerHTML = items.map((p) => `
    <div class="problem-item" data-id="${p.id}">
      <div class="pi-top">
        <span class="pi-type">${escapeHtml(p.type)}</span>
        <span class="pi-kp">${escapeHtml(p.kp)}</span>
        <span class="pi-time">${fmtTime(p.time)}</span>
      </div>
      <div class="pi-stem">${escapeHtml(p.stem)}</div>
      <div class="pi-versions">解答版本 <b>v${p.versions}</b> · 已保存</div>
    </div>
  `).join("");
  list.querySelectorAll(".problem-item").forEach((item) => {
    item.onclick = () => { window.location.href="/question.html?question_id="+encodeURIComponent(item.dataset.id); };
  });
}

/* ================= 会话管理 ================= */
function createSession(render = true) {
  const s = { id: uid(), draft: true, title: "新对话", createdAt: Date.now(), messages: [] };
  state.sessions.unshift(s);
  state.activeId = s.id;
  state.guideStep[s.id] = 0;
  if (render) { renderSessionList(); renderMessages(); }
  $("#topbarTitle").textContent = s.title;
  saveState();
  return s;
}

function switchSession(id) {
  state.activeId = id;
  renderSessionList();
  renderMessages();
  $("#topbarTitle").textContent = activeSession()?window.TeacherChrome.topicTitle(activeSession().messages.find(m=>m.role==='user')?.text || activeSession().title):"新对话";
  saveState();
  closeSidebarMobile();
}

async function deleteSession(id) {
  if(sending) {toast("请等待当前回复完成后再删除会话。");return;}
  const session=state.sessions.find(s=>s.id===id);if(!session || session.deleting)return;
  session.deleting=true;
  try {
    if(!session.draft) await API.request(`/api/v1/conversations/${encodeURIComponent(id)}`,{method:'DELETE'});
    state.deletedSessions ||= [];if(!state.deletedSessions.includes(id))state.deletedSessions.push(id);
    state.sessions=state.sessions.filter(s=>s.id!==id);
    if(state.activeId===id)state.activeId=state.sessions[0]?.id || null;
    const questionIds=new Set(session.messages.filter(m=>m.solution).map(m=>m.solution.questionId));
    state.problems=state.problems.filter(p=>!questionIds.has(p.id));
    $("#problemCount").textContent=state.problems.length;
    renderSessionList();renderMessages();$("#topbarTitle").textContent=activeSession()?.title || "新对话";saveState();
    toast("会话已删除。");
  } catch(e) {toast(e.message || "删除失败，请重试。");}
  finally {delete session.deleting;}
}

async function regenerateReply(id) {
  if(sending)return;
  const session=activeSession();if(!session)return;
  const at=session.messages.findIndex(m=>m.id===id);if(at<0)return;
  let source;
  for(let i=at-1;i>=0;i--)if(session.messages[i].role==='user'){source=session.messages[i];break;}
  if(!source){toast("未找到原始问题，无法重新生成。");return;}
  sending=true;updateSendState();
  try {
    let record=source.submission;
    if(!record?.id) {
      let before;
      do {const page=await API.request(`/api/v1/messages?limit=100&conversation_id=${encodeURIComponent(session.id)}${before?'&before='+encodeURIComponent(before):''}`);record=page.items.find(r=>r.client_request_id===source.id);before=page.next_cursor;}while(!record && before);
    }
    if(!record?.id)throw new Error("原始消息尚未保存，无法重新生成。");
    record=await API.request(`/api/v1/messages/${encodeURIComponent(record.id)}`);
    const msg={id:uid(),role:'user',text:record.query,imageAssetID:record.image_asset_id,mode:record.mode,guideQuestionId:record.guide_question_id,guide:record.mode==='guide' && !!record.guide_question_id,review:record.mode==='review',regenerateOf:record.id,saveStatus:'saving'};
    session.replyVersions=session.replyVersions || [];
    const selected=session.replyVersion ?? session.replyVersions.length;
    session.replyVersions[selected]=JSON.parse(JSON.stringify(session.messages));
    session.replyVersion=session.replyVersionCount || session.replyVersions.length;
    session.replyVersionCount=session.replyVersion+1;
    session.messages=session.messages.slice(0,session.messages.indexOf(source));
    session.messages.push(msg);chatFollowTail=true;saveState();renderMessages();
    await persistQuery(session,msg);
  }catch(e){toast(e.message || "重新生成失败，请重试。");}
  finally{sending=false;updateSendState();}
}

/* Durable guide tasks: subscribing never owns the lifetime of the teacher run. */
async function persistAsyncGuide(session,msg) {
  const parent=msg.guide && (!msg.guideQuestionId || msg.guideQuestionId===session.guideQuestionId) ? session.guideTaskID : undefined;
  const body={query:msg.text,conversation_id:session.id,client_request_id:msg.id,mode:msg.guide?'guide':(msg.mode || 'solve'),image_asset_id:msg.imageAssetID || undefined,regenerate_of:msg.regenerateOf || undefined,parent_task_id:parent || undefined};
  if(msg.guide && !parent)body.guide_question_id=msg.guideQuestionId || session.guideQuestionId;
  if(!msg.guide){session.taskOverride=body.mode;state.mode=body.mode;}
  const task=await API.request('/api/v1/teaching/tasks',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(body)});
  applyTeachingTask(task,[]);watchTeachingTask(task.id);
}
function applyTeachingTask(task,events) {
  if((state.deletedSessions || []).includes(task.conversation_id))return;
  let session=state.sessions.find(s=>s.id===task.conversation_id);
  if(!session){session={id:task.conversation_id,title:task.input.query.slice(0,24),createdAt:Date.parse(task.created_at),messages:[]};state.sessions.push(session);}
  let msg=session.messages.find(m=>m.id===task.input.client_request_id);
  if(!msg){msg={id:task.input.client_request_id,role:'user',text:task.input.query,imageAssetID:task.input.image_asset_id,mode:task.input.mode || 'guide'};session.messages.push(msg);}
  const direct=task.input.mode==='solve';
  const followup=!!task.parent_task_id || !!task.input.guide_question_id;
  msg.mode=task.input.mode || 'guide';msg.guide=followup;msg.taskID=task.id;msg.taskStatus=task.status;msg.saveStatus='saved';
  msg.solveStatus=task.status==='FAILED'?'failed':task.status==='READY'?((followup || direct)?'completed':'guided'):'solving';
  msg.solveError=task.error || '';msg.taskCreatedAt=task.created_at;
  const newer=!session.guideTaskCreatedAt || Date.parse(task.created_at)>=Date.parse(session.guideTaskCreatedAt);
  const overridden=session.taskOverride==='solve' && session.taskOverrideAt && session.taskOverrideAt>Date.parse(task.created_at);
  if(!direct && !followup && newer && !overridden){session.guideTaskID=task.id;session.guideTaskCreatedAt=task.created_at;}

  for(const event of events){msg.taskCursor=Math.max(msg.taskCursor || 0,event.id);
    if(event.type==='accepted'){msg.submission={id:event.data.message_id,turn_id:event.data.turn_id};msg.serverMessageID=event.data.message_id;msg.taskAssistantID=event.data.assistant_message_id;}
    if(event.type==='progress'){
      msg.solveProgress=event.data.text;msg.solveStage=event.data.stage;
      const labels={compacting:'正在整理之前的对话，完成后自动继续',context_ready:'对话整理完成，继续处理本次问题',preparing:'正在读取题目与学习资料',recognizing:'正在识别图片中的题干',generating:'正在准备适合你的讲解',validating:'正在整理答案与解题信息',saving:'正在保存本次讲解'};
      const label=labels[event.data.stage];
      if(label){msg.activity=msg.activity || [];if(!msg.activity.includes(label))msg.activity.push(label);msg.activity=msg.activity.slice(-12);}
    }
    if(event.type==='execution_node'){
     msg.executionNodes=msg.executionNodes || [];
     const value={...event.data,id:event.data.id || 'event-'+event.id};
     const at=msg.executionNodes.findIndex(n=>n.id===value.id);
     if(at>=0)msg.executionNodes[at]=value;else msg.executionNodes.push(value);
    }
    if(event.type==='text' || event.type==='stream_text')msg.taskText=event.data.text;
  }
  if(task.result?.source_message_id){msg.serverMessageID=task.result.source_message_id;msg.submission={id:task.result.source_message_id,turn_id:task.result.turn_id};}
  if(!direct && task.result?.question_id){msg.guideQuestionId=task.result.question_id;if(session.guideTaskID===task.id || session.guideTaskID===task.parent_task_id || !session.guideTaskID)session.guideQuestionId=task.result.question_id;}
  if(!followup && task.status==='READY' && task.result?.question_id && task.result.stem){
    const r=task.result;const problem={id:r.question_id,stem:r.stem,type:({single_choice:'选择',multiple_choice:'选择',fill_blank:'填空',constructed_response:'解答'})[r.question_type] || '解答',kp:knowledgeNames[r.knowledge_point] || '高中数学',time:Date.parse(r.created_at || task.created_at),versions:1};
    const at=state.problems.findIndex(p=>p.id===problem.id);if(at>=0)state.problems[at]=problem;else state.problems.unshift(problem);$('#problemCount').textContent=state.problems.length;
  }
  if(!followup && task.status==='FAILED' && task.result?.needs_input==='true' && session.guideTaskID===task.id){delete session.guideTaskID;delete session.guideQuestionId;session.taskOverride='guide';}
  const pending=!['READY','FAILED','CANCELLED'].includes(task.status);
  const answerText=teacherNarrative((direct || followup)?(task.result?.text || msg.taskText || ''):(task.opening || ''),!direct);
  const statusText=pending ? (task.error || msg.solveProgress || '请求已保存，AI教师准备中~') : task.status==='FAILED'?(task.error || '本次请求未完成，可重试'):'';
  if(answerText || statusText){
    const id=task.result?.assistant_message_id || msg.taskAssistantID || 'task-'+task.id;
    let answer=session.messages.find(m=>m.id===id || m.fromTask===task.id);
    if(!answer){answer={id,role:'assistant',type:(direct || task.input.mode==='review')?'text':'guide',review:task.input.mode==='review',fromTask:task.id};const at=session.messages.indexOf(msg)+1;session.messages.splice(at,0,answer);}
    answer.text=(task.status==='FAILED' ? '> '+statusText+'\n\n' : '')+answerText;answer.fromTask=task.id;
    answer.streaming=pending && !!msg.taskText;answer.executionNodes=msg.executionNodes || [];
    answer.activity=msg.activity?.length?msg.activity:['请求已保存，等待教师处理'];
    const activityPending=pending && !answerText;
    if(answer.activityPending!==activityPending){delete answer.activityOpen;}
    answer.activityPending=activityPending;
  }
  saveState();if(state.activeId===session.id)renderMessages();renderSessionList();
}
async function watchTeachingTask(id) {
  if(taskWatchers.has(id))return;taskWatchers.add(id);
  try {
    let failures=0;
    if(typeof EventSource==='function'){
      const completed=await new Promise(resolve=>{const session=state.sessions.find(s=>s.messages.some(m=>m.taskID===id));const msg=session?.messages.find(m=>m.taskID===id);if(!msg){resolve(true);return;}const feed=new EventSource(`/api/v1/teaching/tasks/${encodeURIComponent(id)}/stream?after=${msg.taskCursor || 0}`);feed.addEventListener('task',event=>{try{const page=JSON.parse(event.data);if(!state.sessions.includes(session)){feed.close();resolve(true);return;}applyTeachingTask(page.task,page.events || []);if(['READY','FAILED','CANCELLED'].includes(page.task.status)&&(page.events || []).length<200){feed.close();resolve(true);}}catch(_){feed.close();resolve(false);}});feed.onerror=()=>{feed.close();resolve(false);};});if(completed)return;
    }
    while(true){
      const session=state.sessions.find(s=>s.messages.some(m=>m.taskID===id));if(!session)return;
      const msg=session.messages.find(m=>m.taskID===id);
      if(!msg.executionNodes?.length)msg.taskCursor=0;
      try {
        const page=await API.request(`/api/v1/teaching/tasks/${encodeURIComponent(id)}?after=${msg.taskCursor || 0}`);
        if(!state.sessions.includes(session))return;
        applyTeachingTask(page.task,page.events || []);failures=0;
        if(['READY','FAILED','CANCELLED'].includes(page.task.status) && (page.events || []).length<200)return;
      }catch(e){failures++;msg.solveProgress='连接暂时中断，后台任务仍会继续；正在重新连接…';if(state.activeId===session.id)renderMessages();}
      await sleep(Math.min(10000,1000*(1+failures)));
    }
  }finally{taskWatchers.delete(id);}
}
async function restoreTeachingTasks(){
  const startup=activeSession();const initialId=state.activeId;const empty=startup && !startup.messages.length;
  const page=await API.request('/api/v1/teaching/tasks');
  for(const task of (page.items || []).slice().reverse()){applyTeachingTask(task,[]);watchTeachingTask(task.id);}
  if(empty && state.activeId===initialId && !$('#input').value && page.items?.length){const latest=page.items.find(t=>!(state.deletedSessions || []).includes(t.conversation_id));if(latest)state.activeId=latest.conversation_id;}
}

/* ================= 附件 ================= */
let attachedFile = null;

function setAttachment(file) {
  if (!["image/jpeg","image/png","image/webp"].includes(file.type)) { toast("一期仅支持图片格式（JPEG/PNG/WebP）"); return; }
  if (file.size > 5 * 1024 * 1024) { toast("图片不能超过 5MB"); return; }
  if($("#attachImg").src)URL.revokeObjectURL($("#attachImg").src);
  attachedFile = file;
  $("#attachPreview").hidden = false;
  $("#attachImg").src = URL.createObjectURL(file);
  $("#attachName").textContent = file.name;
  updateSendState();
}

function clearAttachment() {
  attachedFile = null;
  $("#attachPreview").hidden = true;
  if ($("#attachImg").src) URL.revokeObjectURL($("#attachImg").src);
  $("#fileInput").value = "";
  updateSendState();
}

/* ================= 输入区 ================= */
function autoResize() {
  const t = $("#input");
  t.style.height = "auto";
  t.style.height = Math.min(t.scrollHeight, 180) + "px";
}

function updateSendState() {
  const has = $("#input").value.trim() || attachedFile;
  $("#btnSend").disabled = !has || sending;
  const n = Array.from($("#input").value).length;
  $("#charHint").textContent = n > 0 ? `${n} / 20000 字` : "";
}

function setMode(mode) {
  if(activeSession()?.reviewQuestionId){toast('当前为费曼验证，请先复述解题过程；新题请新建对话。');return;}
  modeChosen=true;
  state.mode = mode;
  const session=activeSession();
  if(session){session.taskOverride=mode;session.taskOverrideAt=Date.now();}
  document.querySelectorAll(".mode-btn").forEach((b) =>
    b.classList.toggle("active", b.dataset.mode === mode));
  saveState();
}

/* ================= 抽屉与侧栏 ================= */
function openDrawer() { $("#problemDrawer").classList.add("open"); renderProblems(); }
function closeDrawer() { $("#problemDrawer").classList.remove("open"); }
function closeSidebarMobile() {
  $("#sidebar").classList.remove("open");
  $("#sidebarMask").classList.remove("show");
}

/* ================= 事件绑定 ================= */
function bindEvents() {
  $("#btnNewChat").onclick = async () => {
    const session=createSession();closeSidebarMobile();
    try{await ensureServerConversation(session);renderSessionList();}
    catch(_){toast("新对话尚未连接，发送问题时会重新尝试。");}
  };
  $("#btnMenu").onclick = () => {
    $("#sidebar").classList.toggle("open");
    $("#sidebarMask").classList.toggle("show");
  };
  $("#sidebarMask").onclick = closeSidebarMobile;

  document.querySelectorAll('[data-panel="problems"]').forEach((b) => b.onclick = openDrawer);
  document.querySelectorAll('[data-panel="profile"]').forEach((b) =>
    b.onclick = () => { window.location.href = "profile.html"; });

  $("#btnCloseDrawer").onclick = closeDrawer;
  $("#problemSearch").oninput = renderProblems;
  $("#problemTypeChips").addEventListener("click", (e) => {
    const chip = e.target.closest(".chip");
    if (!chip) return;
    $("#problemTypeChips .chip.active").classList.remove("active");
    chip.classList.add("active");
    renderProblems();
  });

  document.querySelectorAll(".mode-btn").forEach((b) => b.onclick = () => setMode(b.dataset.mode));

  $("#btnAttach").onclick = () => $("#fileInput").click();
  $("#fileInput").onchange = (e) => { if (e.target.files[0]) setAttachment(e.target.files[0]); };
  $("#attachRemove").onclick = clearAttachment;

  const input = $("#input");
  input.addEventListener("input", () => { autoResize(); updateSendState(); });
  input.addEventListener("keydown", (e) => {
    if (e.key === "Enter" && !e.shiftKey && !e.isComposing) {
      e.preventDefault();
      send();
    }
  });
  $("#btnSend").onclick = send;

  chatInner.addEventListener("click", (e) => {
    const retry = e.target.closest("[data-retry]");
    if (retry) { retrySubmission(retry.dataset.retry); return; }
    const card = e.target.closest(".case-card");
    if (card) {
      const input = $("#input");
      input.value = card.dataset.fill;
      autoResize();
      updateSendState();
      input.focus();
    }
  });
}

/* ================= 启动 ================= */
function init() {
  loadState();
  bindEvents();
  $("#problemCount").textContent = state.problems.length;
  state.mode=activeSession()?.taskOverride || (activeSession()?.guideTaskID || activeSession()?.guideQuestionId ? 'guide' : state.mode || 'solve');
  document.querySelectorAll('.mode-btn').forEach(b=>b.classList.toggle('active',b.dataset.mode===state.mode));
  if (!state.sessions.length) createSession(false);
  renderSessionList();
  renderMessages();
  if(new URLSearchParams(location.search).get("view")==="questions")openDrawer();
  $("#topbarTitle").textContent = activeSession()?window.TeacherChrome.topicTitle(activeSession().messages.find(m=>m.role==='user')?.text || activeSession().title):"新对话";
  updateSendState();
  $("#input").focus();
  restoreSubmissions();
  setInterval(pollReviewReminders,30000);
}
async function pollReviewReminders(){}

init();
