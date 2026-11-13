const test=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),vm=require('node:vm'),path=require('node:path');const {parseHTML}=require('linkedom');
test('execution history pairs calls and results and identifies loaded skills',()=>{
 const {document}=parseHTML(fs.readFileSync(path.join(__dirname,'../conversation.html'),'utf8'));const ctx=vm.createContext({document,Intl,Date});
 const src=fs.readFileSync(path.join(__dirname,'../conversation.js'),'utf8').split('function renderSummary(')[0];vm.runInContext(src,ctx);
 vm.runInContext(`view.data={total_messages:2};view.messages=[{turn_id:'turn1',role:'user',system_prompts:['实际系统提示词原文'],dynamic_context:'动态技能目录与L1索引',status:'SEALED',created_at:'2026-11-10T00:00:00Z',content:{items:[{role:'user',content:'解题'}]}},{turn_id:'turn1',role:'assistant',status:'SEALED',created_at:'2026-11-10T00:00:01Z',content:{items:[{role:'assistant',tool_calls:[{id:'call1',function:{name:'read_file',arguments:JSON.stringify({path:'/skills/math-solve/SKILL.md'})}}]},{role:'tool',tool_call_id:'call1',tool_name:'read_file',content:'<script>unsafe</script>'}]}}];renderMessages();`,ctx);
 assert.equal(document.querySelectorAll('.execution-turn').length,1);assert.match(document.querySelector('.execution-label').textContent,/加载技能.*math-solve/);assert.equal(document.querySelectorAll('.execution-payload').length,4);assert.equal(document.querySelector('#messageList script'),null);assert.match(document.querySelector('#messageList').textContent,/System Prompt/);assert.match(document.querySelector('#messageList').textContent,/Dynamic Context/);
});

test('prompt tag highlighting preserves literal text and never interprets HTML',()=>{
 const {document}=parseHTML('<div id="target"></div>');const ctx=vm.createContext({document,Intl,Date});const src=fs.readFileSync(path.join(__dirname,'../conversation.js'),'utf8').split('function renderMessages()')[0];vm.runInContext(src,ctx);
 vm.runInContext(`highlightPromptTags(document.querySelector('#target'),'<system-prompt>正文<img src=x onerror=alert(1)></system-prompt>')`,ctx);
 assert.equal(document.querySelectorAll('.prompt-tag').length,3);assert.equal(document.querySelector('img'),null);assert.equal(document.querySelector('#target').textContent,'<system-prompt>\n正文\n<img src=x onerror=alert(1)>\n</system-prompt>');
});

test('prompt presentation removes boundary newlines without changing stored source',()=>{
 const {document}=parseHTML('<div id="target"></div>');const ctx=vm.createContext({document,Intl,Date});const src=fs.readFileSync(path.join(__dirname,'../conversation.js'),'utf8').split('function renderMessages()')[0];vm.runInContext(src,ctx);
 ctx.testPrompt='<history-summary>\n {"available":false}\n</history-summary>';vm.runInContext("highlightPromptTags(document.querySelector('#target'),testPrompt)",ctx);
 assert.equal(document.querySelector('#target').textContent,'<history-summary>\n{\n  "available": false\n}\n</history-summary>');
});
