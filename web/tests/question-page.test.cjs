const test=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),vm=require('node:vm'),path=require('node:path');
const {parseHTML}=require('linkedom');
test('question page renders choices, subquestions and complete solution independently',()=>{
 const {document}=parseHTML(fs.readFileSync(path.join(__dirname,'../question.html'),'utf8'));
 const context=vm.createContext({document,marked:{parse:s=>s},DOMPurify:{sanitize:s=>s},renderMathInElement:()=>{}});
 const src=fs.readFileSync(path.join(__dirname,'../question.js'),'utf8').replace("$('#retry').addEventListener('click',loadQuestion);loadQuestion();",'');vm.runInContext(src,context);
 context.fixture={question:{stem:'真实题干',type:'single_choice',difficulty:'easy',options:[{key:'A',text:'选项内容'}],subquestions:[{id:'1',text:'第一小问'}]},solution:{teacher_response:'先观察条件',answer:[{subquestion_id:'1',content:'结果'}],analysis:'方法说明',steps:[{title:'第一步',content:'推导过程'}],knowledge_points:[{code:'sequences',is_primary:true}]},original_text:'提交原文',committed:true,question_id:'q_test',solution_id:'s_test',review_status:'structure_validated'};
 vm.runInContext('renderQuestion(fixture)',context);
 const text=document.querySelector('#question').textContent;
 for(const value of ['真实题干','A.','第一小问','先观察条件','结果','方法说明','推导过程','数列','提交原文'])assert.ok(text.includes(value),value);
 assert.equal(document.querySelector('#question').hidden,false);
 assert.equal(document.querySelector('a').getAttribute('href'),'profile.html');
});
