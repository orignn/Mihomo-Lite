'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { fixture } = require('./frontend-harness.cjs');
const goSource = fs.readFileSync(path.join(__dirname, '../cmd/mihomolite/unified_proxy.go'), 'utf8');
const unified = goSource.match(/const unifiedProxyName = "([^"]+)"/)[1];
function configured(data) { return fixture({ request: async route => route === '/api/rules' ? { ok: true, json: async () => data } : undefined }); }

test('offline new rule defaults to backend-published unified target', async () => {
 const {context,get} = configured({rules:[],policies:[unified,'DIRECT','REJECT'],unifiedPolicy:unified,running:false});
 await context.loadRules(); context.showRuleForm(); assert.equal(get('rulePolicy').value,unified);
});
test('editing an existing rule never migrates its policy implicitly', async () => {
 const {context,get} = configured({rules:[{id:'old',type:'DOMAIN',value:'example.test',policy:'OLD-SAVED-GROUP',enabled:true}],policies:[unified,'DIRECT','REJECT'],unifiedPolicy:unified,running:false});
 await context.loadRules(); context.showRuleForm(0); assert.equal(get('rulePolicy').value,'OLD-SAVED-GROUP');
});
test('missing stable policy leaves choice blank instead of defaulting DIRECT', async () => {
 const {context,get} = configured({rules:[],policies:['DIRECT','REJECT'],running:false});
 await context.loadRules(); context.showRuleForm(); assert.equal(get('rulePolicy').value,''); assert.equal(get('rulePolicy').selectedIndex,-1);
});
test('stable policy must actually appear among available targets', async () => {
 const {context,get} = configured({rules:[],policies:['DIRECT','REJECT'],unifiedPolicy:unified,running:true});
 await context.loadRules(); context.showRuleForm(); assert.equal(get('rulePolicy').value,'');
});
