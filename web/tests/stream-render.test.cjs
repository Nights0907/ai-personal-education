const test=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs');
const path=require('node:path');
const vm=require('node:vm');
const {parseHTML}=require('linkedom');

function page(){
  const {window,document}=parseHTML(fs.readFileSync(path.join(__dirname,'../index.html'),'utf8'));
  const scroll=document.querySelector('#chatScroll');let top=0;
  Object.defineProperties(scroll,{clientHeight:{get:()=>300},scrollHeight:{get:()=>1500+document.querySelector('#chatInner').textContent.length},scrollTop:{get:()=>top,set:v=>{top=Math.max(0,Math.min(v,scroll.scrollHeight-scroll.clientHeight));}}});
  const context=vm.createContext({document,window,console,setTimeout,clearTimeout,TextDecoder,Uint8Array,AbortController,ReadableStream,localStorage:{getItem:()=>null,setItem:()=>{}},URL,location:{pathname:'/'},Event:window.Event,fetch:url=>{if(url==='/api/v1/student/profile')return Promise.resolve({json:async()=>({basic:{}})});throw Error('unexpected network')}});
  vm.runInContext(fs.readFileSync(path.join(__dirname,'../chrome.js'),'utf8'),context);
  const source=fs.readFileSync(path.join(__dirname,'../app.js'),'utf8').replace(/\ninit\(\);\s*$/,'');vm.runInContext(source,context);
  const run=code=>vm.runInContext(code,context);
  run(`asyncTeaching=false;var testMsg={id:'user1',role:'user',text:'题目',imageAssetID:'im_test',saveStatus:'saved',submission:{id:'message1'},solveStatus:'solving',streamDraft:{answer:[{content:'x=2'}],analysis:'开始分析',steps:[{title:'移项',content:'2x'}]}};var testSession={id:'session1',messages:[testMsg]};state.sessions=[testSession];state.activeId='session1';renderMessages();`);
  return {window,document,scroll,run,context};
}

test('stream reuses message, photo, answer and step text nodes',()=>{
  const p=page();const user=p.document.querySelector('.msg.user'),image=p.document.querySelector('.question-image'),live=p.document.querySelector('.live-solve');const analysis=live.querySelector('.live-analysis');const text=analysis.firstChild;const step=live.querySelector('.live-step');
  for(let i=0;i<80;i++)p.run(`testMsg.streamDraft.analysis+='，继续分析';testMsg.streamDraft.steps[0].content+='+';renderMessages();`);
  assert.equal(p.document.querySelector('.msg.user'),user);assert.equal(p.document.querySelector('.question-image'),image);assert.equal(p.document.querySelector('.live-solve'),live);assert.equal(live.querySelector('.live-analysis').firstChild,text);assert.equal(live.querySelector('.live-step'),step);
  assert.equal(p.document.querySelectorAll('.live-step').length,1);
});

test('scrolling up stops tail following; returning to bottom resumes it',()=>{
  const p=page();p.scroll.scrollTop=27;p.scroll.dispatchEvent(new p.window.Event('scroll'));
  for(let i=0;i<25;i++)p.run(`testMsg.streamDraft.analysis+='新的内容';renderMessages();`);
  assert.equal(p.scroll.scrollTop,27);
  p.scroll.scrollTop=p.scroll.scrollHeight;p.scroll.dispatchEvent(new p.window.Event('scroll'));
  p.run(`testMsg.streamDraft.analysis+='继续输出';renderMessages();`);
  assert.equal(p.scroll.scrollTop,p.scroll.scrollHeight-p.scroll.clientHeight);
});

test('unchanged snapshots do not replace text; candidate revision preserves message node',()=>{
  const p=page();const live=p.document.querySelector('.live-solve'),text=live.querySelector('.live-analysis').firstChild;
  p.run('renderMessages();renderMessages();');assert.equal(live.querySelector('.live-analysis').firstChild,text);
  p.run(`testMsg.streamDraft={analysis:'修订后的解析',steps:[{title:'重新分析',content:'新的步骤'}]};renderMessages();`);
  assert.equal(p.document.querySelector('.live-solve'),live);assert.equal(live.querySelector('.live-analysis').textContent,'修订后的解析');assert.equal(p.document.querySelectorAll('.live-solve').length,1);
});

test('final commit preserves reading position and never scrolls into view',()=>{
  const p=page();let forced=0;p.window.HTMLElement.prototype.scrollIntoView=()=>{forced++};p.scroll.scrollTop=45;p.scroll.dispatchEvent(new p.window.Event('scroll'));
  p.run(`testMsg.solveStatus='completed';testSession.messages.push({id:'official1',role:'assistant',type:'solution',status:'saved',solution:{type:'解答',stem:'题目',answer:{overall:'x=2',subs:[]},analysis:'正式解析',steps:[{title:'移项',detail:'2x=4'}],kp:{main:'方程',aux:[]}}});renderMessages();`);
  assert.equal(p.scroll.scrollTop,45);assert.equal(forced,0);assert.equal(p.document.querySelectorAll('.teacher-response').length,1);assert.equal(p.document.querySelectorAll('.live-solve').length,0);
});

test('SSE reader handles UTF-8 and CRLF frames split across network chunks',async()=>{
  const p=page();const payload='id: 1\r\nevent: text_delta\r\ndata: {"text":"数学解析"}\r\n\r\nid: 2\r\nevent: solution\r\ndata: {"committed":true,"question_id":"q1"}\r\n\r\nevent: done\r\ndata: {"status":"succeeded"}\r\n\r\n';const bytes=new TextEncoder().encode(payload);let offset=0;const events=[];
  p.context.fetch=async()=>({ok:true,headers:{get:()=> 'text/event-stream'},body:new ReadableStream({pull(controller){if(offset===bytes.length){controller.close();return;}controller.enqueue(bytes.slice(offset,++offset));}})});
  p.context.onTestEvent=(name,event)=>events.push([name,event]);const result=await p.run(`API.solve('session1',{text:'题目',id:'request1'},onTestEvent)`);
  assert.equal(result.official.question_id,'q1');assert.equal(events[0][1].text,'数学解析');assert.equal(events.filter(e=>e[0]==='solution').length,1);
});

test('new-chat creation and immediate submit share one server request',async()=>{
  const p=page();let calls=0,resolve;
  p.context.fetch=()=>{calls++;return new Promise(r=>{resolve=r;});};
  p.run(`testSession.draft=true;state.guideStep.session1=2;`);
  const both=p.run(`Promise.all([ensureServerConversation(testSession),ensureServerConversation(testSession)])`);
  assert.equal(calls,1);
  const id='c_'+'a'.repeat(32);
  resolve({ok:true,json:async()=>({conversation_id:id})});await both;
  assert.equal(p.run('state.activeId'),id);assert.equal(p.run('testSession.draft'),false);assert.equal(p.run(`state.guideStep['${id}']`),2);
});

/* ---------- 引导学习 ---------- */
function sseBody(text){
  const bytes=new TextEncoder().encode(text);let offset=0;
  return new ReadableStream({pull(controller){if(offset===bytes.length){controller.close();return;}controller.enqueue(bytes.slice(offset,++offset));}});
}

test('guide entry withholds the answer and starts tutoring',async()=>{
  const p=page();let solveCalls=0,submitBody=null;
  p.context.fetch=async(url,opts)=>{
    if(url==='/api/v1/messages'){submitBody=JSON.parse(opts.body);return{ok:true,json:async()=>({id:'message1',query:'求最值',client_request_id:'g1',image_asset_id:'',status:'RECEIVED'})};}
    if(url==='/api/v1/agentic/chat' && JSON.parse(opts.body).guide_question_id)return{ok:true,headers:{get:()=> 'text/event-stream'},body:sseBody('event: text\ndata: {"text":"先想想顶点在哪里？"}\n\n')};
    if(url==='/api/v1/agentic/chat'){solveCalls++;return{ok:true,headers:{get:()=> 'text/event-stream'},body:sseBody('event: guide_start\ndata: {"question_id":"q_'+'a'.repeat(32)+'","stem":"求最值"}\n\nevent: done\ndata: {"status":"guided"}\n\n')};}
    throw Error('unexpected '+url);
  };
  p.run(`var gs={id:'session1',title:'t',createdAt:1,messages:[{id:'old',role:'user',text:'x',saveStatus:'saved',submission:{id:'m0'}}]};state.sessions=[gs];state.activeId='session1';state.mode='guide';`);
  p.run(`var gm={id:'g1',role:'user',text:'求最值',saveStatus:'saving',mode:'guide'};gs.messages.push(gm);`);
  await p.run(`persistQuery(gs,gm)`);
  assert.equal(solveCalls,1);
  // 入库与解题必须携带同一个模式，否则请求编号会因内容不一致被判冲突。
  assert.equal(submitBody.mode,'guide');
  assert.equal(p.run('gs.guideQuestionId'),'q_'+'a'.repeat(32));
  assert.equal(p.run(`gm.solveStatus`),'guided');
  // 答案不得展示
  assert.equal(p.document.querySelectorAll('.solution-card').length,0);
  assert.ok(p.document.querySelector('.guide-status'),'应提示进入引导学习');
});

test('guide answer goes through tutoring and renders one guide bubble',async()=>{
  const p=page();let chatBody=null;
  p.context.fetch=async(url,opts)=>{
    if(url==='/api/v1/agentic/chat'){chatBody=JSON.parse(opts.body);return{ok:true,headers:{get:()=> 'text/event-stream'},body:sseBody('event: text\ndata: {"text":"先想想顶点在哪里？"}\n\n')};}
    throw Error('unexpected '+url);
  };
  p.run(`state.sessions[0].guideQuestionId='q_${'a'.repeat(32)}';`);
  await p.run(`persistGuide(state.sessions[0],{id:'a1',role:'user',text:'我猜顶点是 x=2',saveStatus:'saved',guide:true})`);
  assert.equal(chatBody.mode,'guide');
  assert.equal(chatBody.guide_question_id,'q_'+'a'.repeat(32));
  assert.equal(chatBody.conversation_id,'session1');
  const bubble=p.document.querySelector('.guide-hint');
  assert.ok(bubble,'应渲染引导气泡');
  assert.match(bubble.textContent,/顶点/);
  assert.equal(p.document.querySelectorAll('.solution-card').length,0);
  assert.equal(p.run(`state.sessions[0].messages.filter(m=>m.type==='guide').length`),1);
});

test('guided session remembers its question after reload',()=>{
  const p=page();
  p.run(`state.sessions=[{id:'c_${'b'.repeat(32)}',title:'t',messages:[]}];attachOfficialSolution({committed:true,question_id:'q_${'c'.repeat(32)}',solution_id:'s1',conversation_id:'c_${'b'.repeat(32)}',source_message_id:'message1',created_at:'2026-11-06T00:00:00Z',input_type:'text',question:{stem:'求最值',type:'constructed_response'},solution:{answer:[{content:'1'}],analysis:'a',steps:[],knowledge_points:[]}});`);
  p.run(`state.sessions[0].messages.push({id:'message1',role:'user',text:'求最值',serverMessageID:'message1',mode:'guide'});attachOfficialSolution({committed:true,question_id:'q_${'c'.repeat(32)}',solution_id:'s1',conversation_id:'c_${'b'.repeat(32)}',source_message_id:'message1',created_at:'2026-11-06T00:00:00Z',input_type:'text',question:{stem:'求最值',type:'constructed_response'},solution:{answer:[{content:'1'}],analysis:'a',steps:[],knowledge_points:[]}});`);
  assert.equal(p.run('state.sessions[0].guideQuestionId'),'q_'+'c'.repeat(32));
  assert.equal(p.run("state.sessions[0].messages.filter(m=>m.type==='solution').length"),0);
});


test('durable teacher replies restore in turn order across pages without duplicates or tool leakage',async()=>{
 const p=page(); let calls=0;
 p.run(`testSession.messages=[{id:'u1',role:'user',text:'第一问',mode:'guide',submission:{turn_id:'t1'}},{id:'local',role:'assistant',type:'guide',text:'旧本地回复'},{id:'u2',role:'user',text:'第二问',submission:{turn_id:'t2'}}];`);
 const reply=(id,turn,text)=>({message_id:id,turn_id:turn,role:'assistant',status:'SEALED',in_active_chain:true,content:{items:[{role:'tool',content:'SECRET ANSWER'},{role:'assistant',tool_calls:[{id:'call'}],content:'工具调用过程'},{role:'assistant',content:text}]}});
 p.context.fetch=async(url)=>{calls++;return {ok:true,json:async()=>url.includes('cursor=')?{messages:[reply('a2','t2','第二轮教师回复')]}:{execution_traces:[{turn_id:'t1',events:[{id:1,type:'execution_node',data:{id:'skill1',kind:'skill',name:'step-tutor',status:'completed'}}]}],messages:[reply('a1','t1','第一轮教师回复'),{...reply('other','t2','其他分支'),in_active_chain:false}],next_cursor:'next'}};};
 await p.run('restoreTeacherMessages(testSession)');
 assert.equal(calls,2);assert.equal(p.run(`testSession.messages[1].executionNodes[0].name`),'step-tutor');
 assert.equal(p.run('testSession.messages.map(m=>m.text).join("|")'),'第一问|第一轮教师回复|第二问|第二轮教师回复');
 await p.run('restoreTeacherMessages(testSession)');
 assert.equal(p.run('testSession.messages.length'),4);
 assert.ok(!p.run('JSON.stringify(testSession.messages)').includes('SECRET'));
});

test('teacher history read failure preserves local replies and official solution cards',async()=>{
 const p=page();p.run(`testSession.messages=[{id:'u',role:'user',text:'题目',submission:{turn_id:'t'}},{id:'s',role:'assistant',type:'solution',text:'正式解答'}];`);
 p.context.fetch=async()=>({ok:true,json:async()=>({messages:[{message_id:'a',turn_id:'t',role:'assistant',status:'SEALED',in_active_chain:true,content:{items:[{role:'assistant',content:'模型文字'}]}}]})});
 await p.run('restoreTeacherMessages(testSession)');
 assert.equal(p.run('testSession.messages.length'),2);
 p.context.fetch=async()=>{throw Error('offline')};
 await assert.rejects(p.run('restoreTeacherMessages(testSession)'));
 assert.equal(p.run('testSession.messages[1].text'),'正式解答');
});

test('review answers use the unified chat entry and preserve the bound question',async()=>{
 const p=page();let body;
 p.run(`testSession.reviewQuestionId='q_review';`);
 p.context.fetch=async(url,opts)=>{assert.equal(url,'/api/v1/agentic/chat');body=JSON.parse(opts.body);return{ok:true,headers:{get:()=> 'text/event-stream'},body:sseBody('event: text\ndata: {"text":"请解释为什么这样比较。"}\n\n')};};
 await p.run(`persistGuide(testSession,{id:'review1',text:'我的作答',review:true,imageAssetID:'im_review'})`);
 assert.equal(body.mode,'review');assert.equal(body.guide_question_id,'q_review');assert.equal(body.image_asset_id,'im_review');
 assert.equal(p.document.querySelectorAll('.solution-card').length,0);
});

test('delete persists remotely and preserves local conversation on failure',async()=>{
 const p=page();let request;
 p.context.fetch=async(url,opts)=>{request=[url,opts.method];return {ok:false,status:409,json:async()=>({error:'正在处理'})};};
 await p.run("deleteSession('session1')");assert.equal(p.run('state.sessions.length'),1);
 p.context.fetch=async(url,opts)=>{request=[url,opts.method];return {ok:true,json:async()=>({deleted:true})};};
 await p.run("deleteSession('session1')");assert.deepEqual(request,['/api/v1/conversations/session1','DELETE']);assert.equal(p.run('state.sessions.length'),0);
});

test('regeneration uses immutable original mode and question and retains old reply',async()=>{
 const p=page();p.context.crypto=require('node:crypto').webcrypto;
 p.run(`testMsg.mode='guide';testMsg.guideQuestionId='later';testSession.messages.push({id:'a1',role:'assistant',type:'guide',text:'原回复'});`);
 p.context.fetch=async(url)=>({ok:true,json:async()=>({id:'message1',query:'原题',mode:'guide',guide_question_id:''})});
 p.run(`var regenCaptured;persistQuery=async(s,m)=>{regenCaptured=m;};`);
 await p.run("regenerateReply('a1')");
 assert.equal(p.run('regenCaptured.regenerateOf'),'message1');assert.equal(p.run('regenCaptured.text'),'原题');assert.equal(p.run('regenCaptured.guideQuestionId'),'');assert.equal(p.run('!!regenCaptured.guide'),false);assert.equal(p.run('testSession.replyVersions[0][1].text'),'原回复');
});

test('real guide selection saves hidden answer, starts questioning and survives reload',{skip:!process.env.GUIDE_E2E_BASE,timeout:240000},async()=>{
 const p=page();p.context.crypto=require('node:crypto').webcrypto;p.context.URLSearchParams=URLSearchParams;p.window.location={search:''};p.context.location=p.window.location;
 p.context.fetch=(url,opts)=>fetch(new URL(url,process.env.GUIDE_E2E_BASE),opts);
 p.run(`testSession.messages=[];testSession.id=uid();state.activeId=testSession.id;testSession.draft=true;bindEvents();`);
 p.document.querySelector('[data-mode="guide"]').click();
 assert.equal(p.run('state.mode'),'guide');
 p.document.querySelector('#input').value='等差数列首项 a₁=3，公差 d=2，求 a₁₀ 和前10项和 S₁₀。';
 await p.run('send()');
 assert.equal(p.run('testSession.messages[0].mode'),'guide');
 assert.equal(p.run('testSession.messages[0].solveStatus'),'guided',p.run('testSession.messages[0].solveError'));
 assert.ok(p.run('!!testSession.guideQuestionId'));
 assert.equal(p.document.querySelectorAll('.solution-card').length,0);
 const hint=p.document.querySelector('.guide-hint');assert.ok(hint);assert.match(hint.textContent,/[？?]/);assert.doesNotMatch(hint.textContent,/120|=\s*21/);
 const c=p.run('testSession.id');console.log('Live guide conversation: '+c);
 p.run(`state.sessions=[];state.activeId=null;`);
 await p.run('restoreSubmissions()');
 p.run(`state.activeId=${JSON.stringify(c)};renderMessages();`);
 assert.equal(p.document.querySelectorAll('.solution-card').length,0);
 assert.ok(p.document.querySelector('.guide-hint'),'教师提问应在重新加载后恢复');
});

test('async guide accepts input immediately, keeps answer hidden and queues followup',async()=>{
 const p=page();p.context.crypto=require('node:crypto').webcrypto;let submitted=[];
 p.run(`asyncTeaching=true;testSession.messages=[];state.mode='guide';watchTeachingTask=()=>{};`);
 p.context.fetch=async(url,opts)=>{
  assert.equal(url,'/api/v1/teaching/tasks');const body=JSON.parse(opts.body);submitted.push(body);
  return {ok:true,json:async()=>({id:'tt_'+submitted.length,conversation_id:'session1',input:body,parent_task_id:body.parent_task_id,status:'QUEUED',created_at:'2026-11-09T00:00:00Z'})};
 };
 p.document.querySelector('#input').value='求a10和S10';await p.run('send()');
 assert.equal(p.run('sending'),false);assert.equal(p.run('testSession.guideTaskID'),'tt_1');assert.doesNotMatch(p.document.querySelector('#chatInner').textContent,/undefined/);
 p.run(`applyTeachingTask({id:'tt_1',conversation_id:'session1',input:{query:'求a10和S10',client_request_id:testSession.messages[0].id,mode:'guide'},status:'RUNNING',opening:'题目给出了哪些量？',created_at:'2026-11-09T00:00:00Z'},[]);`);
 assert.match(p.document.querySelector('.guide-hint').textContent,/哪些量/);assert.equal(p.document.querySelectorAll('.solution-card').length,0);
 p.document.querySelector('#input').value='我先求第10项';await p.run('send()');
 assert.equal(submitted[1].parent_task_id,'tt_1');assert.equal(p.run('sending'),false);assert.match(p.document.querySelector('#chatInner').textContent,/作答已保存/);
});

test('real async guide publishes early question and restores task after reload',{skip:!process.env.ASYNC_GUIDE_E2E_BASE,timeout:240000},async()=>{
 const p=page();p.context.crypto=require('node:crypto').webcrypto;p.context.URLSearchParams=URLSearchParams;p.window.location={search:''};p.context.location=p.window.location;
 p.context.fetch=(url,opts)=>fetch(new URL(url,process.env.ASYNC_GUIDE_E2E_BASE),opts);
 p.run(`asyncTeaching=true;testSession.id=uid();state.activeId=testSession.id;testSession.messages=[];testSession.draft=true;bindEvents();`);
 p.document.querySelector('[data-mode="guide"]').click();p.document.querySelector('#input').value='解方程2x+3=11。';
 const start=Date.now();await p.run('send()');assert.equal(p.run('sending'),false);assert.ok(p.run('testSession.messages[0].taskID'));
 for(let i=0;i<150 && !p.document.querySelector('.guide-hint');i++)await new Promise(r=>setTimeout(r,500));
 assert.ok(p.document.querySelector('.guide-hint'),'应在后台任务结束前看到首问');console.log('Async first question elapsed ms: '+(Date.now()-start));
 assert.equal(p.document.querySelectorAll('.solution-card').length,0);
 const c=p.run('testSession.id');p.run(`taskWatchers.clear();state.sessions=[];state.activeId=null;`);
 await p.run('restoreSubmissions()');p.run(`state.activeId=${JSON.stringify(c)};renderMessages();`);
 assert.ok(p.document.querySelector('.guide-hint'),'任务首问刷新可恢复');
 for(let i=0;i<180 && p.run(`state.sessions.find(s=>s.id===${JSON.stringify(c)}).messages.find(m=>m.taskID && !m.guide).taskStatus`)!=='READY';i++)await new Promise(r=>setTimeout(r,500));
 assert.equal(p.run(`state.sessions.find(s=>s.id===${JSON.stringify(c)}).messages.find(m=>m.taskID && !m.guide).taskStatus`),'READY');assert.equal(p.document.querySelectorAll('.solution-card').length,0);p.run('state.sessions=[]');
});

test('teacher narrative streams without exposing metadata panels',()=>{
 const p=page();p.run(`testMsg.streamDraft.teacher_response='先看**对称轴**，再比较端点。';renderMessages();`);
 assert.match(p.document.querySelector('.live-explanation').innerHTML, /<b>对称轴<\/b>/);
 assert.equal(p.document.querySelector('.live-draft').hidden,true);
});

test('durable solve renders one recoverable teacher status bubble then replaces it',()=>{
 const p=page();p.run(`testSession.title='测试会话';var durable={id:'task-solve',conversation_id:'session1',input:{query:'题目',client_request_id:'user1',mode:'solve'},status:'RUNNING',created_at:new Date().toISOString()};applyTeachingTask(durable,[{id:1,type:'progress',data:{stage:'compacting',text:'正在整理之前的对话，完成后自动继续'}}]);`);
 assert.equal(p.document.querySelectorAll('.live-solve').length,0);
 assert.match(p.document.querySelector('.msg.assistant').textContent,/正在整理之前的对话/);
 assert.equal(p.document.querySelector('.teacher-activity').open,true);
 p.run(`durable.status='READY';durable.result={text:'我们先看对称轴。',assistant_message_id:'teacher1'};applyTeachingTask(durable,[]);`);
 assert.equal(p.document.querySelectorAll('.msg.assistant').length,1);
 assert.match(p.document.querySelector('.msg.assistant').textContent,/我们先看对称轴/);
 assert.equal(p.document.querySelector('.teacher-activity').open,false);
 assert.match(p.document.querySelector('.teacher-activity').textContent,/正在整理/);
});

test('execution nodes show actual skill and tool names with icons and update status',()=>{
 const p=page();p.run(`testSession.title='执行节点';var task={id:'nodes-task',conversation_id:'session1',input:{query:'题目',client_request_id:'user1',mode:'solve'},status:'RUNNING',created_at:new Date().toISOString()};applyTeachingTask(task,[{id:1,type:'execution_node',data:{id:'call1',kind:'skill',name:'math-solve',status:'running'}}]);`);
 assert.equal(p.document.querySelectorAll('.execution-node').length,1);assert.match(p.document.querySelector('.execution-node').textContent,/math-solve/);assert.ok(p.document.querySelector('.node-icon'));
 p.run(`applyTeachingTask(task,[{id:2,type:'execution_node',data:{id:'call1',kind:'skill',name:'math-solve',status:'completed'}},{id:3,type:'execution_node',data:{id:'memory-extraction',kind:'memory',name:'长期记忆待后台提取',status:'pending'}}]);`);
 assert.equal(p.document.querySelectorAll('.execution-node').length,2);assert.match(p.document.querySelector('.node-state').textContent,/已完成/);assert.match(p.document.querySelector('.execution-node.pending').textContent,/等待处理/);
});

test('generic progress does not repeat or bury actual tool and skill nodes',()=>{
 const p=page();p.run(`testSession.title='节点去重';var repeatedTask={id:'repeat',conversation_id:'session1',input:{query:'题目',client_request_id:'user1',mode:'solve'},status:'RUNNING',created_at:new Date().toISOString()};applyTeachingTask(repeatedTask,[{id:1,type:'progress',data:{stage:'generating'}},{id:2,type:'progress',data:{stage:'preparing'}},{id:3,type:'progress',data:{stage:'generating'}},{id:4,type:'execution_node',data:{id:'call',kind:'skill',name:'math-solve',status:'completed'}}]);`);
 const panel=p.document.querySelector('.teacher-activity');assert.match(panel.textContent,/math-solve/);assert.equal(panel.querySelectorAll('.execution-note').length,0);assert.equal(panel.querySelectorAll('.execution-node').length,1);
});
test('storage receipts render teacher narrative and never leak guided answers',()=>{
 const p=page();const receipt=JSON.stringify({committed:true,question_id:'q1',solution_id:'s1',solution:{teacher_response:'先移项，再除以3，得到x=5。'}});p.context.testReceipt=receipt;
 assert.equal(p.run('teacherNarrative(testReceipt)'),'先移项，再除以3，得到x=5。');assert.equal(p.run('teacherNarrative(testReceipt,true)'),'');
 p.run(`testSession.messages=[{id:'reply',role:'assistant',type:'text',text:testReceipt}];renderMessages();`);assert.doesNotMatch(p.document.querySelector('#chatInner').textContent,/committed|solution_id/);assert.match(p.document.querySelector('#chatInner').textContent,/先移项/);
 p.run(`testSession.messages[0].type='guide';renderMessages();`);assert.doesNotMatch(p.document.querySelector('#chatInner').textContent,/x=5|committed/);
 assert.equal(p.run(`teacherNarrative('请先计算两个端点值。',true)`),'请先计算两个端点值。');
});
test('Feynman review hides solve modes and cannot be overridden',()=>{
 const p=page();p.run(`testSession.reviewQuestionId='q-review';testSession.taskOverride='solve';renderMessages();`);
 assert.equal(p.document.querySelector('#modeSwitch').hidden,true);assert.equal(p.document.querySelector('#feynmanMode').hidden,false);assert.match(p.document.querySelector('#feynmanMode').textContent,/费曼验证/);
 p.run(`state.mode='guide';setMode('solve');`);assert.equal(p.run('state.mode'),'guide');
 p.run(`delete testSession.reviewQuestionId;renderMessages();`);assert.equal(p.document.querySelector('#modeSwitch').hidden,false);assert.equal(p.document.querySelector('#feynmanMode').hidden,true);
});
test('review opening has no guided-learning label while ordinary guided replies do',()=>{
 const p=page();p.run(`testSession.messages=[{id:'opening',role:'assistant',type:'guide',delivery:true,text:'## 防遗忘复习\\n\\n解方程3x-5=10'}];renderMessages();`);
 assert.equal(p.document.querySelectorAll('.hint-level').length,0);assert.match(p.document.querySelector('.review-opening').textContent,/防遗忘复习/);
 p.run(`testSession.messages[0].delivery=false;renderMessages();`);
 assert.match(p.document.querySelector('.hint-level').textContent,/引导学习/);
});
test('reply icons copy actual text and regeneration pages preserve earlier reply',async()=>{
 const p=page();let copied='';p.context.navigator={clipboard:{writeText:async text=>{copied=text}}};
 p.run(`testSession.messages=[{...testMsg,submission:{id:'message1'}},{id:'reply',role:'assistant',type:'text',text:'原来的讲解'}];renderMessages();`);
 p.document.querySelector('[data-reply-action="copy"]').click();await new Promise(r=>setTimeout(r,0));assert.equal(copied,'原来的讲解');
 p.context.fetch=async()=>({ok:true,json:async()=>({id:'message1',query:'题目',mode:'solve'})});p.run(`persistQuery=async(s,m)=>{s.messages.push({id:'reply2',role:'assistant',type:'text',text:'新的讲解'});renderMessages();};`);
 await p.run(`regenerateReply('reply')`);assert.match(p.document.querySelector('#chatInner').textContent,/新的讲解/);assert.doesNotMatch(p.document.querySelector('#chatInner').textContent,/原来的讲解/);
 p.document.querySelector('[aria-label="上一个回复版本"]').click();assert.match(p.document.querySelector('#chatInner').textContent,/原来的讲解/);
 p.document.querySelector('[aria-label="下一个回复版本"]').click();assert.match(p.document.querySelector('#chatInner').textContent,/新的讲解/);
});
test('titles distinguish numerical questions and Feynman retries never show guide label',()=>{
 const p=page();assert.notEqual(p.window.TeacherChrome.topicTitle('等差数列首项为4，公差为3，求第8项a8'),p.window.TeacherChrome.topicTitle('等差数列首项为2，公差为5，求第9项'));assert.match(p.window.TeacherChrome.topicTitle('解方程3x-5=10'),/3x-5=10/);
 p.run(`testSession.reviewQuestionId='q-review';testSession.messages=[{id:'retry',role:'assistant',type:'guide',text:'暂未完成，请重试。'}];renderMessages();`);assert.equal(p.document.querySelectorAll('.hint-level').length,0);
});
test('real stream snapshots render before task completion and buffer incomplete formulas',()=>{
 const p=page();p.run(`testSession.messages=[];applyTeachingTask({id:'stream-task',conversation_id:'session1',input:{query:'题目',client_request_id:'stream-user',mode:'solve'},status:'RUNNING',created_at:'2026-11-12T00:00:00Z'},[{id:1,type:'stream_text',data:{text:'先移项，再计算 $x='}}]);`);
 assert.match(p.document.querySelector('#chatInner').textContent,/先移项/);assert.doesNotMatch(p.document.querySelector('#chatInner').textContent,/\$x=/);assert.equal(p.run(`testSession.messages.find(m=>m.role==='assistant').streaming`),true);
 p.run(`applyTeachingTask({id:'stream-task',conversation_id:'session1',input:{query:'题目',client_request_id:'stream-user',mode:'solve'},status:'RUNNING',created_at:'2026-11-12T00:00:00Z'},[{id:2,type:'stream_text',data:{text:'先移项，再计算 $x=5$。'}}]);`);assert.match(p.document.querySelector('#chatInner').textContent,/x=5/);
});
