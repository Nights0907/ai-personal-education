"use strict";
const $=s=>document.querySelector(s);
const esc=s=>String(s ?? '').replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;').replace(/"/g,'&quot;');
function md(text){return DOMPurify.sanitize(marked.parse(String(text || '')),{FORBID_TAGS:['style','iframe','form'],FORBID_ATTR:['style']});}
function renderQuestion(v){
 const q=v.question,s=v.solution;
 const names={single_choice:'单选题',multiple_choice:'多选题',fill_blank:'填空题',constructed_response:'解答题'};
 const areas={sets_logic:'集合与逻辑',functions_derivatives:'函数与导数',sequences:'数列',trigonometry:'三角函数',analytic_geometry:'平面解析几何',solid_geometry:'立体几何',probability_statistics:'概率与统计',equations_inequalities:'方程与不等式'};
 $('#question').innerHTML=`<h1>题目展示</h1><div class="meta"><span class="tag">${esc(names[q.type] || q.type)}</span><span class="tag">${esc(({easy:'基础',medium:'中等',hard:'较难'})[q.difficulty] || '未标注难度')}</span><span>${esc(v.created_at?new Date(v.created_at).toLocaleString('zh-CN'): '')}</span><span>${v.committed?'已保存':'尚未保存'}</span></div>
 <section><h2>题目</h2>${md(q.stem)}${(q.options || []).map(o=>`<div><b>${esc(o.key)}.</b> ${md(o.text)}</div>`).join('')}${(q.subquestions || []).map(o=>`<div><b>（${esc(o.id)}）</b>${md(o.text)}</div>`).join('')}</section>
 <section id="imageSection" hidden><h2>题目原图</h2><img id="sourceImage" alt="题目原图"></section>
 ${s.teacher_response?`<section><h2>教师讲解</h2>${md(s.teacher_response)}</section>`:''}
 <section><h2>答案</h2>${(s.answer || []).map(a=>`<div>${a.subquestion_id?`<b>小问 ${esc(a.subquestion_id)}</b>`:''}${md(a.content)}</div>`).join('')}</section>
 <section><h2>解析</h2>${md(s.analysis)}</section><section><h2>解题步骤</h2>${(s.steps || []).map((step,i)=>`<h3>${i+1}. ${esc(step.title)}</h3>${md(step.content)}`).join('')}</section>
 <section><h2>知识点</h2><div class="meta">${(s.knowledge_points || []).map(k=>`<span class="tag">${k.is_primary?'主 · ':''}${esc(areas[k.code] || k.code)}</span>`).join('')}</div></section>
 <section><h2>${v.input_type==='image'?'识别题干原文':'提交原文'}</h2><div class="source">${esc(v.original_text)}</div></section>
 <section><details><summary>题目记录信息</summary><div class="meta">题目编号：${esc(v.question_id)}<br>解答编号：${esc(v.solution_id)}<br>生成模型：${esc(v.model_ref || '未记录')}<br>检查状态：${esc(({structure_validated:'格式检查通过',model_review_passed:'模型复核通过（历史记录）'})[v.review_status] || '未标注')}</div></details></section>`;
 renderMathInElement($('#question'),{delimiters:[{left:'$$',right:'$$',display:true},{left:'\\[',right:'\\]',display:true},{left:'\\(',right:'\\)',display:false},{left:'$',right:'$',display:false}],throwOnError:false,trust:false});
 $('#question').querySelectorAll('a').forEach(a=>{a.rel='noopener noreferrer';});
 $('#question').hidden=false;$('#status').hidden=true;
}
async function loadQuestion(){
 const id=new URLSearchParams(location.search).get('question_id');$('#retry').hidden=true;$('#status').hidden=false;$('#status').className='';$('#status').textContent='正在读取题目…';$('#question').hidden=true;
 try{
 if(!/^q_[0-9a-f]{32}$/.test(id || ''))throw Error('题目编号无效，请从学习档案重新选择题目。');
 const response=await fetch('/api/v1/questions/'+encodeURIComponent(id),{cache:'no-store'});const v=await response.json();if(!response.ok)throw Error(v.error || '题目暂时无法读取');renderQuestion(v);
 if(v.input_type==='image' && v.source_message_id){try{const r=await fetch('/api/v1/messages/'+encodeURIComponent(v.source_message_id),{cache:'no-store'});if(r.ok){const source=await r.json();if(/^im_[0-9a-f]{32}$/.test(source.image_asset_id || '')){$('#sourceImage').src='/api/v1/images/'+encodeURIComponent(source.image_asset_id)+'/content';$('#imageSection').hidden=false;}}}catch(_){}}
 }catch(e){$('#status').textContent=e.message || '读取失败，请重试';$('#status').className='error';$('#retry').hidden=false;}
}
$('#retry').addEventListener('click',loadQuestion);loadQuestion();
