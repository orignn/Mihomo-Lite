'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const { fixture } = require('./frontend-harness.cjs');
const yamlId = 'a'.repeat(64);
const draftKey = 'mihomolite.drafts.v1';
const initialRules = [{ id: 'one', type: 'DOMAIN', value: 'one.example', policy: 'DIRECT', enabled: true }];
function editYAML(ui, value = 'synthetic draft', id = yamlId) { ui.context.openYAMLEditor(ui.profiles.find(p => p.id === id)?.content || '', id); ui.get('config').value = value; ui.get('config').oninput(); }

test('YAML draft survives keep, tab return and browser reload without crossing profiles', async () => {
 const ui = fixture(); await ui.context.loadProfiles(); editYAML(ui);
 const leave = ui.tabs[1].onclick(); assert.equal(ui.get('draftDecision').open, true);
 await ui.get('draftKeep').onclick(); await leave;
 await ui.tabs[0].onclick(); assert.equal(ui.get('config').value, 'synthetic draft');
 const reloaded = fixture({ storage: ui.storage }); await reloaded.context.loadProfiles();
 assert.equal(reloaded.get('draftList').children.length, 1);
 await reloaded.get('draftList').children[0].onclick(); assert.equal(reloaded.get('config').value, 'synthetic draft');
 await reloaded.get('draftKeep').onclick();
 const id = 'b'.repeat(64); reloaded.context.closeYAMLEditor(); reloaded.context.openYAMLEditor('synthetic B', id);
 assert.equal(reloaded.get('config').value, 'synthetic B');
 assert.equal(reloaded.context.readDraftStore().yaml[yamlId].content, 'synthetic draft');
});

test('cancel keeps edits, discard clears only the selected draft', async () => {
 const ui = fixture(); editYAML(ui);
 let leave = ui.get('cancel').onclick(); await ui.get('draftCancel').onclick(); await leave;
 assert.equal(ui.get('config').value, 'synthetic draft'); assert.equal(ui.run('editorOpen'), true);
 ui.run("drafts.yaml['"+'b'.repeat(64)+"']={content:'other draft',base:'other'}");
 leave = ui.get('cancel').onclick(); await ui.get('draftDiscard').onclick(); await leave;
 assert.equal(ui.run('editorOpen'), false);
 const data = ui.context.readDraftStore(); assert.equal(data.yaml[yamlId], undefined); assert.equal(data.yaml['b'.repeat(64)].content, 'other draft');
});

test('failed YAML save remains visible and persistent, then retry clears it after success', async () => {
 const ui = fixture(); editYAML(ui); ui.server.failYAML = true;
 await ui.get('save').onclick(); assert.equal(ui.get('config').value, 'synthetic draft'); assert.equal(ui.run('editorOpen'), true);
 assert.equal(ui.context.readDraftStore().yaml[yamlId].content, 'synthetic draft');
 ui.server.failYAML = false; await ui.get('save').onclick(); assert.equal(ui.run('editorOpen'), false); assert.equal(ui.context.readDraftStore().yaml[yamlId], undefined);
});

test('leave save failure keeps the decision open and allows retry or cancel', async () => {
 const ui = fixture(); editYAML(ui); ui.server.failYAML = true;
 const leave = ui.tabs[1].onclick(); await ui.get('draftSave').onclick();
 assert.equal(ui.get('draftDecision').open, true); assert.match(ui.get('draftDecisionError').textContent, /invalid YAML/);
 ui.server.failYAML = false; await ui.get('draftSave').onclick(); await leave;
 assert.equal(ui.get('draftDecision').open, false); assert.equal(ui.run('activeView'), 'rulesView');
});

test('storage failure blocks keep navigation and leaves content available', async () => {
 const ui = fixture(); editYAML(ui); ui.server.failStorage = true; ui.get('config').value = 'newest draft'; ui.get('config').oninput();
 const leave = ui.tabs[1].onclick(); await ui.get('draftKeep').onclick();
 assert.equal(ui.get('draftDecision').open, true); assert.equal(ui.run('activeView'), 'connectionView'); assert.match(ui.get('draftStorageWarning').textContent, /尚未写入|失败/);
 await ui.get('draftCancel').onclick(); await leave; assert.equal(ui.get('config').value, 'newest draft');
});

test('refresh and close request native confirmation while preserving YAML and rule form', async () => {
 const ui = fixture({ rules: initialRules }); editYAML(ui); await ui.context.loadRules(); ui.context.showRuleForm();
 ui.get('ruleValue').value = 'unfinished.example'; ui.get('ruleValue').oninput();
 const event = { prevented: false, preventDefault() { this.prevented = true; } }; ui.eventListeners.get('beforeunload')(event);
 assert.equal(event.prevented, true); assert.equal(event.returnValue, '');
 const reloaded = fixture({ rules: initialRules, storage: ui.storage }); await reloaded.context.loadRules();
 assert.equal(reloaded.get('ruleValue').value, 'unfinished.example'); assert.equal(reloaded.context.ruleFormDirty(), true);
});

test('rule drafts survive late reload responses and failed save retry', async () => {
 let reply; let hold = false;
 const ui = fixture({ rules: initialRules, request: async (route, request) => { if (hold && route === '/api/rules' && !request.method) return new Promise(resolve => { reply = resolve; }); } });
 await ui.context.loadRules(); hold = true; const loading = ui.context.loadRules();
 ui.run("rules[0].value='draft.example';markRulesDirty()");
 reply({ ok: true, json: async () => ({ rules: initialRules, policies: ['DIRECT'] }) }); await loading; hold = false;
 assert.equal(ui.run('rules[0].value'), 'draft.example');
 ui.server.failRules = true; assert.equal(await ui.get('saveRules').onclick(), false); assert.equal(ui.run('rulesDirty'), true);
 assert.equal(ui.context.readDraftStore().rules.rules[0].value, 'draft.example');
 await ui.context.loadRules(); assert.equal(ui.run('rules[0].value'), 'draft.example');
 ui.server.failRules = false; assert.equal(await ui.get('saveRules').onclick(), true); assert.equal(ui.run('rulesDirty'), false); assert.equal(ui.context.readDraftStore().rules, null);
});

test('unsent rule form can be saved from the leave decision', async () => {
 const ui = fixture({ rules: initialRules }); await ui.tabs[1].onclick(); ui.context.showRuleForm(); ui.get('ruleValue').value = 'new.example'; ui.get('ruleValue').oninput();
 const leave = ui.tabs[0].onclick(); await ui.get('draftSave').onclick(); await leave;
 assert.equal(ui.server.rules().length, 2); assert.equal(ui.server.rules()[1].value, 'new.example'); assert.equal(ui.run('activeView'), 'connectionView');
});

test('external YAML change or switched profile cannot silently overwrite from a draft', async () => {
 const ui = fixture(); editYAML(ui); ui.profiles[0].content = 'external change'; ui.context.confirm = () => false;
 await ui.get('save').onclick(); assert.equal(ui.profiles[0].content, 'external change'); assert.equal(ui.get('config').value, 'synthetic draft');
 ui.context.confirm = () => true; await ui.context.profileAction('switch', 'b'.repeat(64)); await ui.get('save').onclick();
 assert.equal(ui.profiles[1].content, 'synthetic B'); assert.equal(ui.get('config').value, 'synthetic draft');
});

test('profile switch offers draft protection and keep leaves a restorable draft', async () => {
 const ui = fixture(); await ui.context.loadProfiles(); editYAML(ui);
 const card = ui.get('profileList').children[1], button = card.children[1].children.find(button => button.textContent === '切换');
 const switching = button.onclick(); assert.equal(ui.get('draftDecision').open, true); await ui.get('draftKeep').onclick(); await switching;
 assert.equal(ui.run('editorOpen'), false); assert.equal(ui.context.readDraftStore().yaml[yamlId].content, 'synthetic draft');
});



test('kept rule form survives cancel, editing another form and refresh', async () => {
 const ui=fixture({rules:initialRules});await ui.context.loadRules();ui.context.showRuleForm();
 ui.get('ruleValue').value='parked.example';ui.get('ruleValue').oninput();const id=ui.get('ruleID').value;
 const closing=ui.get('cancelRule').onclick();await ui.get('draftKeep').onclick();await closing;
 ui.eventListeners.get('pagehide')();assert.equal(ui.context.readDraftStore().rules.forms[0].rule.value,'parked.example');
 await ui.context.requestRuleForm();ui.get('ruleValue').value='another.example';ui.get('ruleValue').oninput();
 const reloaded=fixture({rules:initialRules,storage:ui.storage});await reloaded.context.loadRules();
 assert.equal(reloaded.get('ruleValue').value,'another.example');
 const resume=reloaded.context.resumeRuleForm(id);await reloaded.get('draftKeep').onclick();await resume;
 assert.equal(reloaded.get('ruleValue').value,'parked.example');
 assert.equal(reloaded.context.readDraftStore().rules.forms[0].rule.value,'another.example');
});

test('form-only draft restores onto the latest saved rule list', async () => {
 const ui=fixture({rules:initialRules});await ui.context.loadRules();ui.context.showRuleForm();ui.get('ruleValue').value='new.example';ui.get('ruleValue').oninput();
 const newer=[...initialRules,{id:'external',type:'DOMAIN',value:'external.example',policy:'REJECT',enabled:true}];
 const reloaded=fixture({rules:newer,storage:ui.storage});await reloaded.context.loadRules();
 assert.equal(reloaded.run('rules.length'),2);assert.equal(reloaded.get('ruleValue').value,'new.example');
 await reloaded.get('saveRules').onclick();assert.equal(reloaded.server.rules().length,3);assert.equal(reloaded.server.rules()[1].id,'external');
});

test('restoring an empty YAML draft does not treat it as an unchanged editor', async () => {
 const ui=fixture();editYAML(ui,'');ui.context.closeYAMLEditor();
 const reloaded=fixture({storage:ui.storage});await reloaded.context.loadProfiles();await reloaded.get('draftList').children[0].onclick();
 assert.equal(reloaded.get('config').value,'');assert.equal(reloaded.context.yamlDirty(),true);
});



test('first rule load disables mutation and a failed load cannot replace saved rules', async () => {
 let reply;const ui=fixture({rules:initialRules,request: async (route,request)=>route==='/api/rules'&&!request.method?new Promise(resolve=>{reply=resolve}):undefined});
 const loading=ui.context.loadRules();assert.equal(ui.get('addRule').disabled,true);assert.equal(ui.get('importRules').disabled,true);
 await ui.get('addRule').onclick();assert.equal(ui.get('ruleForm').style.display,'none');
 assert.equal(await ui.get('saveRules').onclick(),false);
 reply({ok:false,json:async()=>({error:'synthetic load failure'})});await loading;
 assert.equal(ui.get('addRule').disabled,true);assert.equal(ui.get('saveRules').disabled,true);assert.equal(ui.server.rules().length,1);
});

test('different browser tabs keep drafts for different configurations through pagehide', () => {
 const storage=new Map(),a=fixture({storage}),b=fixture({storage});editYAML(a,'alpha',yamlId);editYAML(b,'beta','b'.repeat(64));
 a.eventListeners.get('pagehide')();b.eventListeners.get('pagehide')();
 const saved=a.context.readDraftStore();assert.equal(saved.yaml[yamlId].content,'alpha');assert.equal(saved.yaml['b'.repeat(64)].content,'beta');
});

test('same configuration changed in another tab is not silently overwritten', () => {
 const storage=new Map(),a=fixture({storage}),b=fixture({storage});editYAML(a,'alpha',yamlId);editYAML(b,'beta',yamlId);
 assert.equal(a.context.readDraftStore().yaml[yamlId].content,'alpha');assert.equal(b.get('config').value,'beta');assert.match(b.get('draftStorageWarning').textContent,/另一个标签页/);
});

test('failed persistent discard leaves the decision open with the current draft', async () => {
 const ui=fixture();editYAML(ui);ui.server.failStorage=true;
 const closing=ui.get('cancel').onclick();await ui.get('draftDiscard').onclick();
 assert.equal(ui.get('draftDecision').open,true);assert.equal(ui.get('config').value,'synthetic draft');assert.equal(ui.context.readDraftStore().yaml[yamlId].content,'synthetic draft');
 assert.match(ui.get('draftDecisionError').textContent,/无法清除/);await ui.get('draftCancel').onclick();await closing;
});

test('Escape cannot dismiss the decision while its save is still in flight', async () => {
 let reply;const ui=fixture({request:async(route,request)=>route==='/api/profiles'&&request.method==='POST'?new Promise(resolve=>{reply=resolve}):undefined});
 editYAML(ui);const leave=ui.tabs[1].onclick();const saving=ui.get('draftSave').onclick();
 await new Promise(setImmediate);assert.equal(ui.get('draftDecision').open,true);
 ui.get('draftDecision').listeners.get('cancel')({preventDefault(){}});assert.equal(ui.get('draftDecision').open,true);
 reply({ok:true,json:async()=>({})});await saving;await leave;assert.equal(ui.get('draftDecision').open,false);
});

test('discarding the current form to resume another keeps parked forms and list edits', async () => {
 const ui=fixture({rules:initialRules});await ui.context.loadRules();ui.context.showRuleForm();ui.get('ruleValue').value='parked.example';ui.get('ruleValue').oninput();const first=ui.get('ruleID').value;
 const close=ui.get('cancelRule').onclick();await ui.get('draftKeep').onclick();await close;
 await ui.context.requestRuleForm();ui.get('ruleValue').value='discard.example';ui.get('ruleValue').oninput();
 const resume=ui.context.resumeRuleForm(first);await ui.get('draftDiscard').onclick();await resume;
 assert.equal(ui.get('ruleValue').value,'parked.example');assert.equal(ui.context.readDraftStore().rules.form.rule.value,'parked.example');
});

test('form navigation uses stable IDs after a decision changes the current rows', async () => {
 const ui=fixture({rules:initialRules});await ui.context.loadRules();
 ui.run("rules.push({id:'draft-row',type:'DOMAIN',value:'draft.example',policy:'DIRECT',enabled:true});markRulesDirty()");
 ui.context.showRuleForm(0);ui.get('ruleValue').value='discarded.example';ui.get('ruleValue').oninput();
 const edit=ui.context.requestRuleForm('draft-row');await ui.get('draftDiscard').onclick();await edit;
 assert.equal(ui.get('ruleID').value,'draft-row');assert.equal(ui.get('ruleValue').value,'draft.example');assert.equal(ui.run('rulesDirty'),true);
});

test('clean open form refreshes its policy and note after saved rules change', async () => {
 const ui=fixture({rules:initialRules});await ui.context.loadRules();ui.context.showRuleForm(0);
 ui.server.setRules([{...initialRules[0],policy:'REJECT',note:'new note'}]);await ui.context.loadRules();
 assert.equal(ui.get('rulePolicy').value,'REJECT');assert.equal(ui.get('ruleNote').value,'new note');
 ui.get('ruleValue').value='edited.example';ui.get('ruleValue').oninput();await ui.get('saveRules').onclick();
 assert.equal(ui.server.rules()[0].policy,'REJECT');assert.equal(ui.server.rules()[0].note,'new note');
});

test('editing a rule with a parked form resumes the existing version', async () => {
 const ui=fixture({rules:initialRules});await ui.context.loadRules();await ui.context.requestRuleForm('one');
 ui.get('ruleValue').value='first-draft.example';ui.get('ruleValue').oninput();const close=ui.get('cancelRule').onclick();await ui.get('draftKeep').onclick();await close;
 await ui.context.requestRuleForm('one');assert.equal(ui.get('ruleValue').value,'first-draft.example');
 assert.equal(ui.context.readDraftStore().rules.form.rule.value,'first-draft.example');
});

test('saving a draft conflict keeps the other tab draft and allows a different profile draft', async () => {
 const storage=new Map(),first=fixture({storage});editYAML(first,'shared initial');
 const second=fixture({storage});editYAML(first,'first tab newest');editYAML(second,'second tab edit');
 await second.get('save').onclick();assert.equal(second.run('editorOpen'),false);assert.equal(second.context.readDraftStore().yaml[yamlId].content,'first tab newest');
 editYAML(second,'independent beta','b'.repeat(64));assert.equal(second.context.readDraftStore().yaml['b'.repeat(64)].content,'independent beta');
});

test('CRLF YAML loads clean and an unchanged save preserves previous content', async () => {
 const ui=fixture();ui.profiles[0].content='mode: rule\r\nproxies: []\r\n';ui.context.openYAMLEditor(ui.profiles[0].content,yamlId);
 assert.equal(ui.context.yamlDirty(),false);assert.equal(ui.get('config').value,'mode: rule\nproxies: []\n');
 await ui.get('save').onclick();assert.equal(ui.requests.some(request=>request.route==='/api/profiles'&&request.method==='POST'),false);
});

test('backup export warns about secrets and restore failure preserves drafts', async () => {
 const ui = fixture(); ui.context.confirm = () => false; await ui.get('exportBackup').onclick(); assert.equal(ui.requests.some(request => request.route === '/api/backup'), false);
 ui.context.confirm = () => true; editYAML(ui); ui.server.failBackup = true;
 const restore = ui.get('backupFile').onchange({ target: { files: [{ size: 2, text: async () => '{}' }] } });
 await ui.get('draftKeep').onclick(); await restore; assert.equal(ui.get('config').value, 'synthetic draft'); assert.equal(ui.run('editorOpen'), true);
});
