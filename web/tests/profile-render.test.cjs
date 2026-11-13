const test=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs');
const path=require('node:path');
const vm=require('node:vm');
const {parseHTML}=require('linkedom');

// 模拟后端三个接口的真实返回
const profileData={student_id:'profile-demo',revision:1,basic:{name:'小明',grade:'高一',learning_goals:['攻克函数与导数','提升立体几何空间想象'],explanation_preference:'希望讲解步骤详细，多用画图辅助理解'},updated_at:'2026-11-02T10:00:00Z'};
const indexesData={items:[{month:'2026-11',content:{keywords:['一元二次方程'],turn_count:3}}]};
const recallData={student_id:'profile-demo',due_count:3,derived_at:'2026-11-04T02:10:08Z',items:[
  {area:'FUNC',topic:'二次函数最值',mastery:'familiar',ability:'AB-REASON',error_count:2,last_seen:'2026-09-26T08:00:00Z',retrievability:0.002,due:true,question_ids:['q_00000000000000000000000000000003']},
  {area:'ALG',topic:'一元二次方程求根',mastery:'partial',ability:'AB-COMPUTE',error_count:1,last_seen:'2026-09-26T08:00:00Z',retrievability:0.005,due:true,question_ids:['q_00000000000000000000000000000001']},
  {area:'ALG',topic:'因式分解',mastery:'mastered',ability:'AB-COMPUTE',error_count:0,last_seen:'2026-10-31T09:00:00Z',retrievability:0.772,due:true,question_ids:['q_00000000000000000000000000000002','q_00000000000000000000000000000004']},
]};
const recallPlanData={has_plan:true,daily_quota:1,send_hour:19,last_run_date:'2026-11-04',pending:2,sent:1,reviewed:3};

function page(){
  const {window,document}=parseHTML(fs.readFileSync(path.join(__dirname,'../profile.html'),'utf8'));
  const routes={
    '/api/v1/messages?limit=100':()=>Promise.resolve({ok:true,json:()=>Promise.resolve({items:[]})}),
    '/api/v1/student/profile':()=>Promise.resolve({ok:true,json:()=>Promise.resolve(profileData)}),
    '/api/v1/student/memory/indexes':()=>Promise.resolve({ok:true,json:()=>Promise.resolve(indexesData)}),
    '/api/v1/student/memory/recall':()=>Promise.resolve({ok:true,json:()=>Promise.resolve(recallData)}),
    '/api/v1/student/recall-plan':()=>Promise.resolve({ok:true,json:()=>Promise.resolve(recallPlanData)}),
  };
  const fetchMock=(url)=>{ if(!routes[url])return Promise.reject(Error('unexpected:'+url)); return routes[url](); };
  const context=vm.createContext({document,window,console,setTimeout,clearTimeout,fetch:fetchMock,Promise,location:{pathname:'/profile.html'}});
  vm.runInContext(fs.readFileSync(path.join(__dirname,'../chrome.js'),'utf8'),context);
  const source=fs.readFileSync(path.join(__dirname,'../profile.js'),'utf8');
  vm.runInContext(source,context);
  return {window,document,context};
}

test('profile renders basic info, recall reminders and mastery list',async()=>{
  const p=page();
  // 等待微任务（load() 是 async）
  await new Promise(r=>setTimeout(r,0));
  await new Promise(r=>setTimeout(r,0));

  // 基本信息
  assert.equal(p.document.querySelector('#heroName').textContent,'小明');
  assert.equal(p.document.querySelector('#sideName').textContent,'小明');
  assert.equal(p.document.querySelector('#profileError').hidden,true);
  assert.ok(p.document.querySelector('#heroGrade').textContent.includes('高一'));
  assert.equal(p.document.querySelectorAll('.goal-tag').length,2);

  // 统计
  assert.equal(p.document.querySelector('#statAreas').textContent,'3');
  assert.equal(p.document.querySelector('#statDue').textContent,'3');
  assert.equal(p.document.querySelector('#statMonths').textContent,'1');

  // 复习提醒：3 个 due 项
  const recallItems=p.document.querySelectorAll('#recallList .recall-item');
  assert.equal(recallItems.length,3);

  // 掌握画像：3 个知识点，按遗忘程度排序（二次函数最值 最遗忘在前）
  const masteryRows=p.document.querySelectorAll('#masteryList .mastery-row');
  assert.equal(masteryRows.length,3);
  assert.equal(masteryRows[0].querySelector('.m-name').textContent,'二次函数最值');
  assert.ok(masteryRows[0].querySelector('.mastery-due'),'最遗忘项应标记待复习');

  // 掌握档位 badge
  assert.ok(masteryRows[1].querySelector('.badge-partial'),'partial 应渲染 badge');
  assert.ok(masteryRows[2].querySelector('.badge-mastered'),'mastered 应渲染 badge');

  // 题目链接：每个知识点渲染对应 question_ids 的跳转链接
  assert.equal(masteryRows[0].querySelectorAll('.q-link').length,1,'二次函数最值应有 1 个题目链接');
  assert.equal(masteryRows[0].querySelector('.q-link').getAttribute('href'),'/question.html?question_id=q_00000000000000000000000000000003');
  assert.equal(masteryRows[2].querySelectorAll('.q-link').length,2,'因式分解应有 2 个题目链接');
  // 复习提醒列表同样渲染题目链接
  assert.ok(recallItems[0].querySelector('.q-link'),'复习提醒项应包含题目链接');

  // 防遗忘计划状态
  assert.equal(p.document.querySelectorAll('#planBody .plan-counter').length,3,'应有 3 个计数器');
  assert.equal(p.document.querySelector('#planBody .plan-counter.pending .pc-value').textContent,'2');
  assert.equal(p.document.querySelector('#planBody .plan-counter.sent .pc-value').textContent,'1');
  assert.equal(p.document.querySelector('#planBody .plan-counter.reviewed .pc-value').textContent,'3');
});

test('profile renders empty recall-plan state',async()=>{
  const {window,document}=parseHTML(fs.readFileSync(path.join(__dirname,'../profile.html'),'utf8'));
  const routes={
    '/api/v1/student/profile':()=>Promise.resolve({ok:true,json:()=>Promise.resolve(profileData)}),
    '/api/v1/student/memory/indexes':()=>Promise.resolve({ok:true,json:()=>Promise.resolve(indexesData)}),
    '/api/v1/student/memory/recall':()=>Promise.resolve({ok:true,json:()=>Promise.resolve(recallData)}),
    '/api/v1/student/recall-plan':()=>Promise.resolve({ok:true,json:()=>Promise.resolve({has_plan:false})}),
  };
  const context=vm.createContext({document,window,console,setTimeout,clearTimeout,fetch:(u)=>routes[u](),Promise});
  vm.runInContext(fs.readFileSync(path.join(__dirname,'../profile.js'),'utf8'),context);
  await new Promise(r=>setTimeout(r,0));
  await new Promise(r=>setTimeout(r,0));
  await new Promise(r=>setTimeout(r,0));

  assert.ok(document.querySelector('#planBody .plan-empty'),'无计划应显示空状态');
});

test('profile renders empty states gracefully',async()=>{
  const {window,document}=parseHTML(fs.readFileSync(path.join(__dirname,'../profile.html'),'utf8'));
  const routes={
    '/api/v1/student/profile':()=>Promise.resolve({ok:true,json:()=>Promise.resolve({student_id:'x',revision:0,basic:{name:'',grade:'',learning_goals:[],explanation_preference:''},updated_at:null})}),
    '/api/v1/student/memory/indexes':()=>Promise.resolve({ok:true,json:()=>Promise.resolve({items:[]})}),
    '/api/v1/student/memory/recall':()=>Promise.resolve({ok:true,json:()=>Promise.resolve({student_id:'x',items:[],due_count:0,derived_at:''})}),
    '/api/v1/student/recall-plan':()=>Promise.resolve({ok:true,json:()=>Promise.resolve({has_plan:false})}),
  };
  const context=vm.createContext({document,window,console,setTimeout,clearTimeout,fetch:(u)=>routes[u](),Promise});
  vm.runInContext(fs.readFileSync(path.join(__dirname,'../profile.js'),'utf8'),context);
  await new Promise(r=>setTimeout(r,0));
  await new Promise(r=>setTimeout(r,0));

  assert.ok(p=>p,1);
  assert.ok(document.querySelector('#recallList .pf-empty'),'无 due 项应显示空状态');
  assert.ok(document.querySelector('#masteryList .pf-empty'),'无掌握数据应显示空状态');
  assert.equal(document.querySelector('#statDue').textContent,'0');
});

test('profile handles API failure with error notice',async()=>{
  const {window,document}=parseHTML(fs.readFileSync(path.join(__dirname,'../profile.html'),'utf8'));
  const context=vm.createContext({document,window,console,setTimeout,clearTimeout,fetch:()=>Promise.reject(Error('network')),Promise});
  vm.runInContext(fs.readFileSync(path.join(__dirname,'../profile.js'),'utf8'),context);
  await new Promise(r=>setTimeout(r,0));
  await new Promise(r=>setTimeout(r,0));

  assert.equal(document.querySelector('#profileError').hidden,false,'失败应显示错误提示');
  assert.ok(document.querySelector('#profileError').textContent.includes('无法完整读取档案'));
});
