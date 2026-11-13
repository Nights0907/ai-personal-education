/* 沐斯 AI · 学习档案：读取 L0 基本信息 + L1 记忆索引 + 遗忘状态，渲染个性化档案。 */
"use strict";

const $ = (sel) => document.querySelector(sel);

function escapeHtml(s) {
  return String(s == null ? "" : s).replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
}

function toast(text) {
  const t = $("#toast");
  t.textContent = text;
  t.hidden = false;
  clearTimeout(toast._tm);
  toast._tm = setTimeout(() => { t.hidden = true; }, 2600);
}

/* 能力维度 / 知识点大类 中文名 */
const areaNames = {
  ALG: "代数与方程", FUNC: "函数与图象", SEQ: "数列", TRIG: "三角函数",
  VEC: "平面向量", SOLID: "立体几何", ANALYTIC: "解析几何",
  PROB: "概率与统计", CALC: "导数与微积分",
};
const abilityNames = {
  "AB-COMPUTE": "运算求解", "AB-REASON": "逻辑推理", "AB-MODEL": "数学建模",
  "AB-SPACE": "直观想象", "AB-DATA": "数据分析",
};
const masteryNames = {
  mastered: "已掌握", partial: "基本掌握", familiar: "熟悉中", unseen: "未掌握",
};
function areaName(code) { return areaNames[code] || code || "未知"; }
function abilityName(code) { return abilityNames[code] || code || ""; }
function masteryName(code) { return masteryNames[code] || code || "未知"; }
function observedMastery(it){
 if(it.evidence_status==='legacy')return '历史记录，待验证';
 if(it.assisted && it.mastery==='partial')return it.incorrect?'提示后仍需巩固':'提示后完成';
 return masteryName(it.mastery);
}

/* 遗忘指数 → 颜色档位 */
function rTier(r) {
  if (r >= 0.8) return "high";
  if (r >= 0.5) return "mid";
  return "low";
}

/* 知识点对应的题目链接（跳转主界面定位到该题） */
function questionLinks(ids) {
  const list = (ids || []).filter((id) => /^q_[0-9a-f]{32}$/.test(id));
  if (!list.length) return "";
  const chips = list.map((id, i) => {
    const short = id.slice(0, 10);
    return `<a class="q-link" href="/question.html?question_id=${encodeURIComponent(id)}" title="打开产生该知识点的题目">题目 ${i + 1} · ${escapeHtml(short)}</a>`;
  }).join("");
  return `<div class="q-links"><span class="q-links-label">相关题目</span>${chips}</div>`;
}

const API = {
  async get(path) {
    const res = await fetch(path, { cache: "no-store" });
    const data = await res.json().catch(() => ({}));
    if (!res.ok) throw new Error(data.error || "读取失败");
    return data;
  },
  profile() { return this.get("/api/v1/student/profile"); },
  indexes() { return this.get("/api/v1/student/memory/indexes"); },
  recall() { return this.get("/api/v1/student/memory/recall"); },
  recallPlan() { return this.get("/api/v1/student/recall-plan"); },
};

function fmtDate(ts) {
  if (!ts) return "—";
  const d = new Date(ts);
  if (isNaN(d)) return "—";
  const p = (n) => String(n).padStart(2, "0");
  return `${d.getFullYear()}/${p(d.getMonth() + 1)}/${p(d.getDate())}`;
}

/* ===== 渲染：基本信息 ===== */
function renderBasic(profile) {
  const b = profile.basic || {};
  const name = b.name || "学生";
  const grade = b.grade || "";
  const goals = b.learning_goals || [];
  const pref = b.explanation_preference || "";

  const initial = (name || "生").trim().slice(0, 1) || "生";
  $("#sideName").textContent = name;
  $("#sideAvatar").textContent = initial;
  $("#heroName").textContent = name;
  $("#heroAvatar").textContent = initial;
  $("#heroGrade").textContent = grade ? `年级：${grade}` : "未填写年级";
  $("#heroUpdated").textContent = profile.updated_at ? `更新于 ${fmtDate(profile.updated_at)}` : "";

  const items = [];
  if (grade) items.push({ label: "年级", value: escapeHtml(grade) });
  if (pref) items.push({ label: "讲解偏好", value: escapeHtml(pref) });
  if (goals.length) {
    items.push({
      label: "学习目标",
      value: goals.map((g) => `<span class="goal-tag">${escapeHtml(g)}</span>`).join(""),
    });
  }
  const grid = $("#basicGrid");
  if (!items.length) {
    grid.innerHTML = `<div class="pf-empty">暂未填写基本信息，可在学习中逐步完善</div>`;
    return;
  }
  grid.innerHTML = items.map((it) =>
    `<div class="basic-item"><div class="b-label">${it.label}</div><div class="b-value">${it.value}</div></div>`
  ).join("");
}

/* ===== 渲染：遗忘状态（复习提醒 + 掌握画像） ===== */
function renderRecall(plan) {
  const items = (plan.items || []).filter((it) => it.due);
  const dueList = $("#recallList");

  if (!items.length) {
    dueList.innerHTML = `<div class="pf-empty">暂无需要复习的知识点，保持得很好 👍</div>`;
  } else {
    dueList.innerHTML = items.map((it) => {
      const pct = Math.max(0, Math.min(100, Math.round(it.retrievability * 100)));
      const tier = rTier(it.retrievability);
      const days = it.last_seen ? `上次复习 ${fmtDate(it.last_seen)}` : "暂无记录";
      return `
        <div class="recall-item">
          <div class="recall-icon">↻</div>
          <div class="recall-body">
            <div class="recall-topic">${escapeHtml(it.topic)}</div>
            <div class="recall-meta">${escapeHtml(areaName(it.area))} · ${escapeHtml(observedMastery(it))} · <b>${days}</b>${it.error_count ? ` · 近期出错 ${it.error_count} 次` : ""}</div>
            ${questionLinks(it.question_ids)}
          </div>
          <div class="recall-r">
            <span class="r-val ${tier}">${pct}%</span>
            <div class="r-bar"><i style="width:${pct}%"></i></div>
            <span class="r-label">预计记忆保持度</span>
          </div>
        </div>`;
    }).join("");
  }

  // 掌握画像：全部知识点，按遗忘优先级（已由后端排序）
  renderMastery(plan.items || []);
}

function renderMastery(items) {
  const list = $("#masteryList");
  if (!items.length) {
    list.innerHTML = `<div class="pf-empty">暂无掌握数据，开始解题后这里会逐步生成</div>`;
    return;
  }
  list.innerHTML = items.map((it) => {
    const pct = Math.max(0, Math.min(100, Math.round(it.retrievability * 100)));
    const tier = rTier(it.retrievability);
    const barColor = tier === "high" ? "var(--green)" : tier === "mid" ? "var(--amber)" : "var(--accent)";
    const badge = `badge-${it.mastery}`;
    const ability = it.ability ? `<span> · ${escapeHtml(abilityName(it.ability))}</span>` : "";
    const links = questionLinks(it.question_ids);
    return `
      <div class="mastery-row">
        <div class="mastery-topic">
          <div class="m-name">${escapeHtml(it.topic)}</div>
          <div class="m-area">${escapeHtml(areaName(it.area))}${ability}</div>
          ${links}
        </div>
        <div class="mastery-r">
          <div class="r-bar"><i style="width:${pct}%;background:${barColor}"></i></div>
          <span class="r-num">${pct}%</span>
        </div>
        <span class="mastery-badge ${badge}">${escapeHtml(observedMastery(it))}</span>
        ${it.due ? `<span class="mastery-due">待复习</span>` : ""}
      </div>`;
  }).join("");
}

/* ===== 渲染：防遗忘计划状态 ===== */
function renderPlan(status) {
  const body = $("#planBody");
  if (!status || !status.has_plan) {
    body.innerHTML = `<div class="plan-empty">尚未生成防遗忘计划。当有知识点遗忘指数跌破阈值时，会自动为你建立计划。</div>`;
    return;
  }
  const lastRun = status.last_run_date || "尚未发送";
  body.innerHTML = `
    <div class="plan-meta">
      <span class="plan-tag">到期通知 · 主动领取</span>
      <span class="plan-tag">点击左上角铃铛开始复习</span>
      <span class="plan-tag">最近调度：${escapeHtml(lastRun)}</span>
    </div>
    ${(status.notifications || []).map(n=>`<p><a href="/?conversation_id=${encodeURIComponent(n.conversation_id)}">有一道复习题等待你作答 →</a></p>`).join('')}
    <div class="plan-counters">
      <div class="plan-counter pending"><div class="pc-value">${status.pending || 0}</div><div class="pc-label">待发送</div></div>
      <div class="plan-counter sent"><div class="pc-value">${status.sent || 0}</div><div class="pc-label">已发送</div></div>
      <div class="plan-counter reviewed"><div class="pc-value">${status.reviewed || 0}</div><div class="pc-label">已作答</div></div>
    </div>`;
}

/* ===== 加载全部数据 ===== */
async function load() {
  $("#profileStatus").textContent = "加载中…";
  try {
    const [profile, indexes, recall] = await Promise.all([
      API.profile(), API.indexes(), API.recall(),
    ]);
    renderBasic(profile);
    renderRecall(recall);

    // 防遗忘计划状态（独立拉取，失败不阻断其余板块）
    try {
      renderPlan(await API.recallPlan());
    } catch (_) {
      renderPlan(null);
    }

    // 统计
    const monthCount = (indexes.items || []).length;
    const itemCount = (recall.items || []).length;
    const dueCount = recall.due_count || 0;
    $("#statMonths").textContent = monthCount;
    $("#statAreas").textContent = itemCount;
    const dueEl = $("#statDue");
    dueEl.textContent = dueCount;
    dueEl.classList.toggle("due", dueCount > 0);

    $("#profileStatus").textContent = "已读取学习档案（后台作答提取可能仍在进行）";
  } catch (err) {
    $("#profileStatus").textContent = "部分数据暂不可用";
    const box = $("#profileError");
    box.hidden = false;
    box.textContent = `无法完整读取档案：${err.message || "请确认服务已启动、学生画像与长期记忆已配置"}。可返回对话继续学习，档案会随学习自动生成。`;
  }
}

load();
