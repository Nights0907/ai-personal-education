const test=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),vm=require('node:vm'),path=require('node:path');const {parseHTML}=require('linkedom');
for(const file of ['index.html','profile.html','memory.html','question.html','conversation.html','radar.html','models.html'])test('shared navigation contracts: '+file,()=>{
 const {document,window}=parseHTML(fs.readFileSync(path.join(__dirname,'../'+file),'utf8'));
 vm.runInNewContext(fs.readFileSync(path.join(__dirname,'../chrome.js'),'utf8'),{document,window,location:{pathname:file==='index.html'?'/':'/'+file},fetch:async()=>({json:async()=>({items:[]})}),Event:class{}});
 assert.equal(document.querySelectorAll('.shared-sidebar .side-item').length,3);assert.equal(document.querySelectorAll('.shared-actionbar a[href="memory.html"]').length,file==='memory.html'?0:1);
 assert.equal(document.querySelectorAll('.shared-actionbar a[href="conversation.html"]').length,file==='conversation.html'?0:1);
 if(file==='index.html'){assert.ok(document.querySelector('#problemCount'));assert.ok(document.querySelector('#btnNewChat'));assert.ok(document.querySelector('#btnConversationHistory'));}else assert.equal(document.querySelector('.shared-actionbar').firstElementChild.textContent,'← 返回对话');
 if(file==='profile.html'){assert.ok(document.querySelector('#sideName'));assert.ok(document.querySelector('#sideAvatar'));assert.ok(document.querySelector('#profileStatus'));}
 if(file==='conversation.html'){assert.ok(document.querySelector('#recentCount'));assert.ok(document.querySelector('#recentSessions'));}
});
test('notification opens a review only after explicit claim',async()=>{
 const {window,document}=parseHTML(fs.readFileSync(path.join(__dirname,'../memory.html'),'utf8'));const location={pathname:'/memory.html',href:'/memory.html'};const calls=[];
 const fetch=async(url,opts={})=>{calls.push([url,opts.method || 'GET']);if(url==='/api/v1/student/recall-plan')return {ok:true,json:async()=>({notifications:[{id:8,topic:'二次函数'}]})};if(url.endsWith('/8/claim'))return {ok:true,json:async()=>({conversation_id:'review-demo'})};return {ok:true,json:async()=>({items:[],basic:{}})};};
 vm.runInNewContext(fs.readFileSync(path.join(__dirname,'../chrome.js'),'utf8'),{document,window,location,fetch,Event:window.Event});
 assert.equal(calls.filter(c=>c[1]==='POST').length,0);
 document.querySelector('.notification-bell').click();await new Promise(r=>setTimeout(r,0));
 assert.equal(document.querySelector('.notification-panel').hidden,false);assert.match(document.querySelector('.notification-panel').textContent,/二次函数/);assert.equal(calls.filter(c=>c[1]==='POST').length,0);
 document.querySelector('.notification-item button').click();await new Promise(r=>setTimeout(r,0));
 assert.equal(calls.filter(c=>c[1]==='POST').length,1);assert.equal(location.href,'/?conversation_id=review-demo');
});

