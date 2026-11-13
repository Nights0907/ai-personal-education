const test=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),vm=require('node:vm'),path=require('node:path');const {parseHTML}=require('linkedom');
test('memory overview renders all four stored layers and escapes raw dialogue',()=>{
 const {document}=parseHTML(fs.readFileSync(path.join(__dirname,'../memory.html'),'utf8'));
 const ctx=vm.createContext({document});const code=fs.readFileSync(path.join(__dirname,'../memory.js'),'utf8').replace(/;load\(\);\s*$/,';');vm.runInContext(code,ctx);
 ctx.data={student_id:'student-one',month:'2026-11',l0:{basic:{name:'学生甲'},revision:2},l1:[{month:'2026-11',content:{keywords:['等差数列'],turn_count:1}}],l2:{content:{summary:'仍需巩固',knowledge_areas:[{area:'SEQ',topics:[{topic:'等差数列',mastery:'partial',question_ids:['q_test']}]}]}},l3:[{seq:1,user:'<img src=x onerror=alert(1)>',assistant:'教师反馈',source:{turn_id:'turn-one'}}],total:1,offset:0,limit:20,has_more:false};
 ctx.data.l3[0].source.conversation_id='conversation-one';ctx.data.l3.push({...ctx.data.l3[0],seq:2});ctx.data.l3.push({...ctx.data.l3[0],seq:3,source:{conversation_id:'conversation-two'}});
 vm.runInContext('render(data)',ctx);assert.equal(document.querySelectorAll('.memory-session').length,2);assert.equal(document.querySelector('.memory-session').querySelectorAll('.turn-card').length,2);assert.match(document.querySelectorAll('.memory-session')[1].querySelector('.turn-card>summary').textContent,/第 1 轮/);const text=document.querySelector('#report').textContent;
 for(const label of ['L0','L1','L2','L3','学生甲','等差数列','教师反馈'])assert.ok(text.includes(label),label);
 assert.equal(document.querySelector('#report img'),null);assert.equal(document.querySelector('#next').disabled,true);assert.equal(document.querySelectorAll('.learning-note').length,9);
 assert.match(document.querySelector('#report a').getAttribute('href'),/question.html/);
});
