'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { fixture } = require('./frontend-harness.cjs');

test('select all and clear all update row checkboxes immediately', async () => {
 const savedRules = [
  {id:'one',type:'DOMAIN',value:'one.example',policy:'DIRECT',enabled:true},
  {id:'two',type:'DOMAIN',value:'two.example',policy:'DIRECT',enabled:false},
 ];
 const ui=fixture({rules:savedRules}); await ui.context.loadRules();
 const master=ui.get('ruleSelectAll');
 const rowChecks=()=>ui.get('rulesList').children.map(row=>row.children[0]);
 const checkedRows=()=>rowChecks().map(check=>check.checked);
 const toggle=element=>{element.checked=!element.checked;element.onchange({target:element})};
 assert.deepEqual(checkedRows(),[false,false]);
 toggle(master);assert.deepEqual(checkedRows(),[true,true]);assert.equal(ui.get('ruleSelectedCount').textContent,'已选 2 条');assert.equal(ui.get('bulkDisable').disabled,false);
 toggle(rowChecks()[0]);assert.equal(master.checked,false);assert.equal(ui.get('ruleSelectedCount').textContent,'已选 1 条');
 toggle(master);assert.deepEqual(checkedRows(),[true,true]);toggle(master);assert.deepEqual(checkedRows(),[false,false]);
 assert.equal(ui.get('ruleSelectedCount').textContent,'');assert.equal(ui.get('bulkDisable').disabled,true);assert.equal(ui.get('saveRules').disabled,true);
 assert.deepEqual(savedRules.map(rule=>rule.enabled),[true,false]);assert.equal(ui.get('rulesError').textContent,'');
});

test('rule tester displays unknown instead of a false miss and names the saved-rule scope', async () => {
 const page = fs.readFileSync(path.join(__dirname, '..', 'cmd', 'mihomolite', 'index.go'), 'utf8');
 assert.match(page, /仅使用已保存的启用自定义规则/);
 assert.match(page, /不包含未保存草稿或原 YAML 规则链/);
 const result = { status: 'unknown', uncertainTypes: ['GEOSITE'], missingDatabases: ['GEOSITE'] };
 const ui = fixture({ request: async route => route.startsWith('/api/rules/test?') ? { ok: true, json: async () => result } : undefined });
 await ui.context.loadRules();
 ui.get('ruleTestKind').value = 'destination';
 ui.get('ruleTestInput').value = 'www.example.com';
 await ui.get('ruleTestButton').onclick();
 assert.match(ui.get('ruleTestResult').textContent, /无法模拟确定结果/);
 assert.match(ui.get('ruleTestResult').textContent, /GEOSITE/);
 assert.doesNotMatch(ui.get('ruleTestResult').textContent, /未命中任何/);
 assert.match(ui.requests.find(request => request.route.startsWith('/api/rules/test?')).route, /kind=destination/);
});

for (const response of [
 {status:'matched', matched:{type:'DOMAIN',value:'example.com',policy:'DIRECT'}},
 {status:'no-match'},
 {status:'unknown',uncertainTypes:['PROCESS-NAME'],possibleMatch:{type:'DOMAIN',value:'example.com',policy:'DIRECT'}},
]) test('rule tester renders '+response.status+' with its simulation boundary', async()=>{
 const ui=fixture({request:async route=>route.startsWith('/api/rules/test?')?{ok:true,json:async()=>response}:undefined});
 ui.get('ruleTestInput').value='example.com';ui.get('ruleTestKind').value='destination';
 await ui.get('ruleTestButton').onclick();
 const text=ui.get('ruleTestResult').textContent;
 if(response.status==='matched'){assert.match(text,/模拟命中（非内核结果）/);assert.match(text,/DIRECT/)}
 if(response.status==='no-match'){assert.match(text,/模拟未命中/);assert.match(text,/不代表.*内核/)}
 if(response.status==='unknown'){assert.match(text,/缺少连接元信息/);assert.match(text,/后续可能命中 DOMAIN/);assert.doesNotMatch(text,/确定命中/)}
 assert.equal(ui.get('ruleTestButton').disabled,false);
});

test('rule test input invalidates late responses and input-kind changes clear prior results',async()=>{
 let release;
 const slow=new Promise(resolve=>{release=resolve});
 const ui=fixture({request:async route=>{
  if(!route.startsWith('/api/rules/test?'))return undefined;
  const data=route.includes('first.example')?await slow:{status:'matched',matched:{type:'DOMAIN',value:'second.example',policy:'REJECT'}};
  return {ok:true,json:async()=>data};
 }});
 ui.get('ruleTestKind').value='destination';ui.get('ruleTestInput').value='first.example';
 const first=ui.get('ruleTestButton').onclick();
 assert.equal(ui.get('ruleTestButton').disabled,true);
 ui.get('ruleTestInput').value='second.example';ui.get('ruleTestInput').oninput();
 await ui.get('ruleTestButton').onclick();
 assert.match(ui.get('ruleTestResult').textContent,/second.example/);
 release({status:'no-match'});await first;
 assert.match(ui.get('ruleTestResult').textContent,/second.example/);
 ui.get('ruleTestKind').value='process';ui.get('ruleTestKind').onchange();
 assert.equal(ui.get('ruleTestResult').textContent,'');
 assert.match(ui.get('ruleTestInput').placeholder,/进程/);
});


test('saving rules invalidates displayed results and pending old-snapshot responses',async()=>{
 let release;const slow=new Promise(resolve=>{release=resolve});
 const oldRule={id:'one',type:'DOMAIN',value:'old.example',policy:'DIRECT',enabled:true};
 const ui=fixture({rules:[oldRule],request:async route=>route.startsWith('/api/rules/test?')?{ok:true,json:async()=>await slow}:undefined});
 await ui.context.loadRules();ui.get('ruleTestKind').value='destination';ui.get('ruleTestInput').value='old.example';
 const pending=ui.get('ruleTestButton').onclick();
 ui.run("rules=[{id:'new',type:'DOMAIN',value:'new.example',policy:'REJECT',enabled:true}];rulesDirty=true;rulesGeneration++");
 assert.equal(await ui.context.saveRulesDraft(),true);
 assert.equal(ui.get('ruleTestResult').textContent,'');
 release({status:'matched',matched:oldRule});await pending;
 assert.equal(ui.get('ruleTestResult').textContent,'');
 ui.get('ruleTestResult').textContent='old displayed result';
 ui.server.setRules([oldRule]);await ui.context.loadRules();
 assert.equal(ui.get('ruleTestResult').textContent,'');
});


test('first saved-rule load invalidates a test response from an older snapshot',async()=>{
 let releaseLoad,releaseTest;
 const loading=new Promise(resolve=>{releaseLoad=resolve}),testing=new Promise(resolve=>{releaseTest=resolve});
 const ui=fixture({request:async(route,request)=>{
  if(route==='/api/rules'&&request.method==='GET')return {ok:true,json:async()=>await loading};
  if(route.startsWith('/api/rules/test?'))return {ok:true,json:async()=>await testing};
  return undefined;
 }});
 const firstLoad=ui.context.loadRules();
 ui.get('ruleTestKind').value='destination';ui.get('ruleTestInput').value='example.com';
 const oldTest=ui.get('ruleTestButton').onclick();
 releaseLoad({rules:[{id:'new',type:'DOMAIN',value:'example.com',policy:'REJECT',enabled:true}],policies:['DIRECT','REJECT'],running:false});
 await firstLoad;
 releaseTest({status:'matched',matched:{type:'DOMAIN',value:'example.com',policy:'DIRECT'}});await oldTest;
 assert.equal(ui.get('ruleTestResult').textContent,'');
});

