'use strict';

const assert = require('node:assert/strict');
const { fixture: subscriptionFixture } = require('./frontend-harness.cjs');
const fs = require('node:fs');
const path = require('node:path');
const test = require('node:test');
const vm = require('node:vm');

const page = fs.readFileSync(path.join(__dirname, '..', 'cmd', 'mihomolite', 'index.go'), 'utf8');
const script = page.split('<script>')[1].split('</script>')[0];
const startup = script.indexOf('loadSettings();setInterval(loadSettings,');
assert.ok(startup > 0);
new vm.Script(script);

class Element {
  constructor(tag) {
    this.tag = tag;
    this.children = [];
    this.style = {};
    this.dataset = {};
    this.classList = { toggle() {}, add() {}, remove() {} };
    this.disabled = false;
  }

  set textContent(text) {
    this.text = text;
    this.children = [];
  }

  focus() {}
  setAttribute(name, value) { this[name] = value; }
  append(...children) { this.children.push(...children); }
  appendChild(child) { this.append(child); }
  querySelectorAll() {
    return this.children.flatMap(child => child.tag === 'button' ? [child] : child.querySelectorAll());
  }
}

function harness(diagnostics, subscription = {}) {
  const elements = new Map();
  const listeners = new Map();
  const requests = [];
  const profiles = [
    { id: 'active', name: 'Active', source: 'subscription', active: true, size: 100, ...subscription },
    { id: 'other', name: 'Other', source: 'local', size: 100 },
  ];
  const get = id => {
    if (!elements.has(id)) elements.set(id, new Element('div'));
    return elements.get(id);
  };
  let serverState = { busy: false, running: true, coreReady: true, panelReady: true, profileCount: profiles.length };
  let readState = () => serverState;
  let finishUpdate;
  const update = new Promise(resolve => { finishUpdate = resolve; });
  const context = vm.createContext({
    document: { getElementById: get, querySelector: () => null, querySelectorAll: () => [], createElement: tag => new Element(tag), createDocumentFragment: () => new Element('fragment') },
    EventSource: class { addEventListener(name, listener) { listeners.set(name, listener); } },
    setTimeout() {},
    prompt: () => null,
    confirm: () => false,
    fetch: async (route, options = {}) => {
      if (route === '/api/state') {
        const payload = await readState();
        return { ok: true, json: async () => payload };
      }
      if (diagnostics && route === '/api/diagnostics') return { ok: true, json: async () => diagnostics.report };
      if (diagnostics && route.startsWith('/api/recovery/')) {
        requests.push({ route, ...JSON.parse(options.body) });
        return { ok: true, json: async () => ({}) };
      }
      if (diagnostics && route === '/api/repair-proxy') {
        diagnostics.repairs++;
        diagnostics.report = diagnostics.success;
        serverState = { ...serverState, proxyEnabled: true, connectionError: '' };
        return { ok: true, json: async () => ({}) };
      }
      assert.ok(route === '/api/profiles' || route === '/api/import');
      if (options.method === 'POST') {
        const request = JSON.parse(options.body);
        requests.push(request);
        if (await update === false) return { ok: false, json: async () => ({ error: 'invalid YAML' }) };
        if (route === '/api/import' || request.action === 'add-url') {
          profiles[0].active = false;
          profiles.push({ id: 'new', name: 'New config', source: 'local', active: true, size: 100 });
          serverState = { ...serverState, profileCount: profiles.length };
        }
      }
      return { ok: true, json: async () => ({ profiles }) };
    },
  });
  vm.runInContext(script.slice(0, startup), context);
  const emit = (busy, patch = {}) => {
    serverState = { ...serverState, busy, ...patch };
    listeners.get('state')({ data: JSON.stringify(serverState) });
  };
  emit(false);
  const buttons = () => get('profileList').querySelectorAll();
  const assertDisabled = disabled => {
    assert.ok(buttons().length > 0);
    for (const button of buttons()) assert.equal(button.disabled, disabled, button.text);
  };
  return { context, get, emit, buttons, assertDisabled, requests, finishUpdate, setStateReader: fn => { readState = fn; } };
}

test('subscription update restores all controls when reconnect finishes after the response', async () => {
  const ui = harness();
  await ui.context.loadProfiles();
  ui.assertDisabled(false);
  const update = ui.buttons().find(button => button.text === '更新').onclick();
  ui.assertDisabled(true);
  ui.emit(true);
  ui.finishUpdate();
  await update;
  ui.assertDisabled(true);
  ui.emit(false);
  ui.assertDisabled(false);
  assert.deepEqual(ui.requests.map(request => request.action), ['update']);
});

test('a failed subscription shows its result and can be updated again', async () => {
  const ui = harness(undefined, { lastUpdate: { at: '2026-11-19T10:00:00Z', status: 'failed', message: '下载失败' } });
  await ui.context.loadProfiles();
  const [card] = ui.get('profileList').children;
  assert.match(card.children[0].children[3].text, /手动更新.*失败.*下载失败/);
  const retry = ui.buttons().find(button => button.text === '重试更新');
  const result = retry.onclick();
  ui.finishUpdate();
  await result;
  assert.equal(ui.requests[0].action, 'update');
});

test('subscription interval starts off and uses the configured hour value', async () => {
  const ui = harness();
  await ui.context.loadProfiles();
  const [card] = ui.get('profileList').children;
  const select = card.children[0].children[4].children[1];
  assert.equal(select.value, '0');
  select.value = '24';
  const result = select.onchange();
  ui.finishUpdate();
  await result;
  assert.deepEqual(ui.requests.map(({ action, hours }) => ({ action, hours })), [{ action: 'auto-update', hours: 24 }]);
});

test('subscription comparison highlights changed lines and all other text stays literal', () => {
  const ui = harness();
  assert.equal(ui.context.renderSubscriptionDiff('first\nold\nlast', 'first\n<b>new</b>\nadded\nlast'), true);
  const oldLines = ui.get('subscriptionBefore').children[0].children;
  const newLines = ui.get('subscriptionAfter').children[0].children;
  assert.deepEqual(oldLines.map(line => line.className), ['diff-line', 'diff-line diff-removed', 'diff-line']);
  assert.deepEqual(newLines.map(line => line.className), ['diff-line', 'diff-line diff-added', 'diff-line diff-added', 'diff-line']);
  assert.equal(newLines[1].text, '<b>new</b>');
});

test('cancelled rename and delete keep current buttons usable', async () => {
  const ui = harness();
  await ui.context.loadProfiles();
  for (const label of ['重命名', '删除']) {
    await ui.buttons().find(button => button.text === label).onclick();
    ui.assertDisabled(false);
  }
  assert.deepEqual(ui.requests, []);
});

test('a late busy state fetch cannot override the reconnect completion event', async () => {
  const ui = harness();
  await ui.context.loadProfiles();
  ui.emit(true);
  let reply;
  ui.setStateReader(() => new Promise(resolve => { reply = resolve; }));
  const refresh = ui.context.refreshState();
  ui.emit(false);
  reply({ busy: true, running: true, coreReady: true, panelReady: true });
  await refresh;
  ui.assertDisabled(false);
});

test('connection failure opens diagnostics and proxy repair refreshes the checks', async () => {
  const healthy = { status: 'ok', detail: '正常' };
  const success = { core: healthy, port: healthy, proxy: healthy };
  const diagnostics = {
    repairs: 0,
    success,
    report: {
      ...success,
      proxy: { status: 'error', detail: '手动代理未开启' },
      reason: '系统代理启用失败',
      actions: [{ id: 'repairProxy', label: '修复系统代理' }],
    },
  };
  const ui = harness(diagnostics);
  ui.emit(false, { proxyEnabled: false, connectionError: '系统代理启用失败' });
  assert.equal(ui.get('diagnostics').hidden, false);
  assert.equal(ui.get('error').text, '系统代理启用失败');
  await new Promise(setImmediate);
  assert.match(ui.get('diagnosticProxy').text, /手动代理未开启/);
  const [repair] = ui.get('diagnosticActions').children;
  assert.equal(repair.text, '修复系统代理');
  await repair.onclick();
  await new Promise(setImmediate);
  assert.equal(diagnostics.repairs, 1);
  assert.equal(ui.get('statusText').text, '已连接');
  assert.equal(ui.get('diagnosticProxy').dataset.status, 'ok');
});


for (const [id, route] of [['keepCurrentProxy', '/api/recovery/keep-current-proxy'], ['stopLeftoverCore', '/api/recovery/stop-leftover-core']]) {
  test(id + ' requires confirmation and sends an explicit confirmed body', async () => {
    const healthy = { status: 'idle', detail: '隔离状态' };
    const diagnostics = { report: { core: healthy, port: healthy, proxy: healthy, actions: [{ id, label: id }] } };
    const ui = harness(diagnostics);
    ui.emit(false, { running: false });
    await ui.context.runDiagnosis();
    let action = ui.get('diagnosticActions').children[0];
    await action.onclick();
    assert.equal(ui.requests.length, 0, 'cancel must not send recovery request');
    ui.context.confirm = () => true;
    await action.onclick();
    assert.deepEqual(ui.requests, [{ route, confirm: true }]);
    ui.emit(true, { running: false });
    await ui.context.runDiagnosis();
    for (const button of ui.get('diagnosticActions').children) assert.equal(button.disabled, true);
  });
}

const yaml = 'mode: rule';
const addOperations = {
  subscription(ui) {
    ui.get('subscriptionName').value = 'New config';
    ui.get('subscriptionURL').value = 'https://example.com/subscription';
    return ui.get('confirmSubscription').onclick();
  },
  file(ui) {
    const file = { size: yaml.length, text: async () => yaml };
    return ui.get('file').onchange({ target: { files: [file] } });
  },
  paste(ui) {
    ui.get('paste').onclick();
    ui.get('config').value = yaml;
    return ui.get('save').onclick();
  },
};

for (const [name, add] of Object.entries(addOperations)) {
  test(name + ' refreshes the list and restores controls when adding succeeds', async () => {
    const ui = harness();
    await ui.context.loadProfiles();
    const addResult = add(ui);
    ui.assertDisabled(true);
    ui.emit(true);
    ui.finishUpdate(true);
    await addResult;
    ui.assertDisabled(true);
    ui.emit(false);
    ui.assertDisabled(false);
    const switchButtons = ui.buttons().filter(button => button.text === '切换');
    assert.equal(switchButtons.length, 2);
    await switchButtons[0].onclick();
    assert.equal(ui.requests[0].action || ui.requests[0].content, name === 'subscription' ? 'add-url' : yaml);
    assert.equal(ui.requests[1].action, 'switch');
  });

  test(name + ' restores controls when adding fails', async () => {
    const ui = harness();
    await ui.context.loadProfiles();
    const count = ui.buttons().length;
    const addResult = add(ui);
    ui.finishUpdate(false);
    await addResult;
    ui.assertDisabled(false);
    assert.equal(ui.buttons().length, count);
    if (name === 'paste') assert.equal(ui.get('config').value, yaml);
  });
}

function subscriptionUIProfile(traffic){return {id:'e'.repeat(64),name:'Synthetic subscription',source:'subscription',url:'https://synthetic.invalid/old?token=synthetic-only',active:true,size:1024,traffic}}
function usageRow(ui){return ui.get('profileList').children[0].children[0].children.find(child=>child.dataset.kind==='traffic')}

test('subscription quota shows used, remaining, upload, download, expiry and snapshot age without exposing URL',async()=>{
 const profile=subscriptionUIProfile({upload:1024,download:1024,total:8192,expire:1,checkedAt:'2026-11-22T00:00:00Z'});
 const ui=subscriptionFixture({profiles:[profile]});await ui.context.loadProfiles();
 const text=usageRow(ui).textContent;
 assert.match(text,/已用 2.00 KiB/);assert.match(text,/总额 8.00 KiB/);assert.match(text,/剩余 6.00 KiB/);assert.match(text,/上传 1.00 KiB/);assert.match(text,/下载 1.00 KiB/);assert.match(text,/已过期/);assert.match(text,/信息读取/);
 assert.doesNotMatch(ui.get('profileList').children[0].children[0].children.map(el=>el.textContent).join(' '),/synthetic-only|synthetic.invalid/);
});

for(const [label,traffic,pattern] of [
 ['missing header',undefined,/未知（服务器未提供/],
 ['partial counters',{upload:1,total:100},/已用 未知.*剩余 未知/],
 ['invalid counter',{upload:-1,download:2,total:100},/已用 未知/],
 ['unsafe integer',{upload:9007199254740992,download:1,total:9007199254740994},/已用 未知.*总额 未知.*剩余 未知/],
 ['unsafe sum',{upload:Number.MAX_SAFE_INTEGER,download:1,total:Number.MAX_SAFE_INTEGER},/已用 未知.*剩余 未知/],
 ['zero expiry',{upload:0,download:0,total:0,expire:0},/到期 未知/],
]) test('subscription metadata '+label+' stays honest',async()=>{
 const ui=subscriptionFixture({profiles:[subscriptionUIProfile(traffic)]});await ui.context.loadProfiles();assert.match(usageRow(ui).textContent,pattern);
});

test('subscription editor preserves failed input, retries the same ID, and sends the chosen mode',async()=>{
 const profile=subscriptionUIProfile(),submitted=[];let fail=true;
 const ui=subscriptionFixture({profiles:[profile],request:async(route,request)=>{
  if(route!=='/api/profiles'||request.method!=='POST')return undefined;
  const body=JSON.parse(request.body);submitted.push(body);
  if(body.action!=='edit-url')return undefined;
  if(fail)return {ok:false,json:async()=>({error:'synthetic validation rejected; old data retained'})};
  profile.url=body.url;return {ok:true,json:async()=>({profiles:[profile],activeId:profile.id})};
 }});
 ui.context.document.querySelector=()=>({value:'proxy'});
 await ui.context.loadProfiles();
 const edit=ui.get('profileList').children[0].children[1].children.find(button=>button.textContent==='修改订阅地址');
 await edit.onclick();
 assert.equal(ui.get('subscriptionURL').value,profile.url);assert.equal(ui.get('subscriptionName').readOnly,true);
 ui.get('subscriptionURL').value='https://synthetic.invalid/new?token=synthetic-next';
 await ui.get('confirmSubscription').onclick();
 assert.equal(profile.url,'https://synthetic.invalid/old?token=synthetic-only');assert.equal(ui.get('subscriptionForm').style.display,'block');assert.match(ui.get('subscriptionError').textContent,/synthetic validation rejected/);assert.match(ui.get('subscriptionURL').value,/synthetic-next/);assert.equal(ui.get('confirmSubscription').disabled,false);
 fail=false;await ui.get('confirmSubscription').onclick();
 assert.equal(submitted.length,2);assert.ok(submitted.every(body=>body.action==='edit-url'&&body.id===profile.id&&body.mode==='proxy'));
 assert.equal(ui.get('subscriptionForm').style.display,'none');assert.equal(ui.get('subscriptionURL').value,'');
});

test('cancelled subscription edit clears the token and local profiles have no editor button',async()=>{
 const profile=subscriptionUIProfile(),local={id:'f'.repeat(64),name:'Synthetic local',source:'local',size:10,active:false};
 const ui=subscriptionFixture({profiles:[profile,local]});await ui.context.loadProfiles();
 ui.context.showSubscriptionForm(profile);ui.get('cancelSubscription').onclick();
 assert.equal(ui.get('subscriptionURL').value,'');assert.equal(ui.get('subscriptionForm').style.display,'none');
 assert.equal(ui.requests.filter(r=>r.method==='POST').length,0);
 assert.ok(ui.get('profileList').children[1].children[1].children.every(button=>button.textContent!=='修改订阅地址'));
 ui.get('addSubscription').onclick();assert.equal(ui.get('subscriptionName').readOnly,false);assert.equal(ui.get('subscriptionURL').value,'');assert.equal(ui.get('confirmSubscription').textContent,'下载并校验');
});

function policyUI(profile=subscriptionUIProfile(),options={}){
 const ui=subscriptionFixture({profiles:[profile],...options});
 const radios=['auto','direct','proxy'].map(value=>({value,checked:false,disabled:false}));
 const previous=ui.context.document.querySelectorAll;
 ui.context.document.querySelectorAll=selector=>selector==='input[name="downloadMode"]'?radios:previous(selector);
 ui.context.document.querySelector=selector=>selector==='input[name="downloadMode"]:checked'?radios.find(r=>r.checked):null;
 return {...ui,radios};
}
function policyButton(ui,label){return ui.get('profileList').children[0].children[1].children.find(button=>button.textContent===label)}
function cardText(ui){const visit=el=>[el.textContent,...el.children.flatMap(visit)];return visit(ui.get('profileList')).join(' ')}

for(const prefix of ['subscription','subscriptionPolicy'])test(prefix+' download form uses plain labels and hides advanced controls in native details',()=>{
 const start=page.indexOf('<div class="subscription-form" id="'+prefix+'Form"');
 const end=page.indexOf(prefix==='subscription'?'<div class="subscription-form" id="subscriptionPolicyForm"':'<div class="profile-list" id="profileList"',start);
 const markup=page.slice(start,end),advanced=markup.match(/<details\b([^>]*)>([\s\S]*?)<\/details>/);
 assert.ok(advanced);assert.match(advanced[1],new RegExp('id="'+prefix+'Advanced"'));assert.doesNotMatch(advanced[1],/\bopen(?:\s|=|$)/);assert.match(advanced[1],/overflow-wrap:anywhere/);
 assert.match(advanced[2],new RegExp('<summary[^>]*id="'+prefix+'AdvancedSummary"[^>]*>高级选项<\/summary>'));
 for(const suffix of ['UA','Conditional','Backoff'])assert.match(advanced[2],new RegExp('id="'+prefix+suffix+'"'));
 const visible=markup.replace(advanced[0],'');assert.doesNotMatch(visible,/User-Agent|条件下载|失败退避|ETag|Last-Modified/);
 assert.match(visible,/不确定就选自动：连接时先用当前代理/);assert.match(visible,/选当前代理则不回退直连/);
 const labels=prefix==='subscription'?[...visible.matchAll(/<input\b[^>]*name="downloadMode"[^>]*value="([^"]+)"[^>]*>([^<]+)/g)]:[...visible.matchAll(/<option value="([^"]+)">([^<]+)<\/option>/g)];
 assert.deepEqual(labels.map(match=>[match[1],match[2]]),[['auto','自动（推荐）'],['direct','直连'],['proxy','当前代理']]);
 for(const control of markup.matchAll(/<(?:input|select)\b[^>]*>/g))assert.match(control[0],/autocomplete="off"/);
 if(prefix==='subscriptionPolicy'){assert.match(visible,/订阅下载设置/);assert.match(visible,/只保存下载设置，不会立即下载订阅或重新连接/);}
});

test('opening either editor always collapses advanced options and new subscriptions reset only to their own defaults',async()=>{
 const profile={...subscriptionUIProfile(),updatePolicy:{mode:'proxy',userAgent:'synthetic-saved/1',conditional:false,failureBackoff:false}},ui=policyUI(profile);
 await ui.context.loadProfiles();ui.context.showSubscriptionForm(profile);ui.get('subscriptionAdvanced').open=true;
 ui.context.showSubscriptionForm(profile);assert.equal(ui.get('subscriptionAdvanced').open,false);assert.equal(ui.get('subscriptionUA').value,profile.updatePolicy.userAgent);
 await policyButton(ui,'订阅下载设置').onclick();assert.equal(ui.get('subscriptionPolicyAdvanced').open,false);assert.match(ui.get('subscriptionPolicyTitle').textContent,/订阅下载设置/);
 ui.get('subscriptionPolicyAdvanced').open=true;ui.get('cancelSubscriptionPolicy').onclick();assert.equal(ui.get('subscriptionPolicyAdvanced').open,false);
 ui.get('addSubscription').onclick();assert.equal(ui.get('subscriptionAdvanced').open,false);assert.equal(ui.get('subscriptionAdvancedSummary').textContent,'高级选项');assert.equal(ui.get('subscriptionUA').value,'');assert.equal(ui.get('subscriptionConditional').checked,true);assert.equal(ui.get('subscriptionBackoff').checked,true);assert.equal(ui.radios.find(r=>r.checked).value,'auto');
 assert.equal(ui.requests.filter(r=>r.method==='POST').length,0);
});

for(const [label,overrides] of [['custom UA',{userAgent:'synthetic-private-UA/1'}],['conditional off',{conditional:false}],['backoff off',{failureBackoff:false}]])test('saved '+label+' has a safe advanced summary in both collapsed editors',async()=>{
 const profile={...subscriptionUIProfile(),updatePolicy:{mode:'auto',userAgent:'',conditional:true,failureBackoff:true,...overrides}},ui=policyUI(profile);await ui.context.loadProfiles();
 assert.match(cardText(ui),/已设置高级选项/);
 for(const prefix of ['subscription','subscriptionPolicy']){
  if(prefix==='subscription')ui.context.showSubscriptionForm(profile);else ui.context.showSubscriptionPolicyForm(profile);
  assert.equal(ui.get(prefix+'Advanced').open,false);assert.equal(ui.get(prefix+'AdvancedSummary').textContent,'高级选项 · 已设置高级选项');assert.doesNotMatch(ui.get(prefix+'AdvancedSummary').textContent,/private-UA|synthetic/);
  assert.equal(ui.get(prefix+'UA').value,profile.updatePolicy.userAgent);assert.equal(ui.get(prefix+'Conditional').checked,profile.updatePolicy.conditional);assert.equal(ui.get(prefix+'Backoff').checked,profile.updatePolicy.failureBackoff);
 }
});

test('an explicitly saved default UA is retained without claiming advanced customization',async()=>{
 const profile={...subscriptionUIProfile(),updatePolicy:{mode:'direct',userAgent:'clash-verge/v2.2.3',conditional:true,failureBackoff:true}},ui=policyUI(profile);await ui.context.loadProfiles();
 assert.doesNotMatch(cardText(ui),/已设置高级选项/);
 for(const prefix of ['subscription','subscriptionPolicy']){if(prefix==='subscription')ui.context.showSubscriptionForm(profile);else ui.context.showSubscriptionPolicyForm(profile);assert.equal(ui.get(prefix+'AdvancedSummary').textContent,'高级选项');assert.equal(ui.get(prefix+'UA').value,'clash-verge/v2.2.3');}
});

for(const [label,prefix,action] of [['settings','subscriptionPolicy','update-policy'],['URL editor','subscription','edit-url'],['new subscription','subscription','add-url']])test(label+' saves advanced values after collapse without resetting UA or switches',async()=>{
 const policy={mode:'proxy',userAgent:'synthetic-retained/1',conditional:false,failureBackoff:false},profile={...subscriptionUIProfile(),updatePolicy:policy},ui=policyUI(profile);await ui.context.loadProfiles();
 if(action==='update-policy')ui.context.showSubscriptionPolicyForm(profile);else if(action==='edit-url')ui.context.showSubscriptionForm(profile);else {ui.get('addSubscription').onclick();ui.get('subscriptionName').value='Synthetic new';ui.get('subscriptionURL').value='https://example.invalid/synthetic';}
 ui.get(prefix+'Advanced').open=true;ui.get(prefix+'UA').value=policy.userAgent;ui.get(prefix+'Conditional').checked=false;ui.get(prefix+'Backoff').checked=false;ui.get(prefix+'Advanced').open=false;
 if(prefix==='subscriptionPolicy')ui.get('subscriptionPolicyMode').value='direct';else ui.radios.forEach(r=>r.checked=r.value==='direct');
 await ui.get(prefix==='subscriptionPolicy'?'confirmSubscriptionPolicy':'confirmSubscription').onclick();
 const posts=ui.requests.filter(r=>r.method==='POST');assert.equal(posts.length,1);assert.equal(posts[0].route,'/api/profiles');const body=JSON.parse(posts[0].body);assert.equal(body.action,action);assert.deepEqual(body.policy,{...policy,mode:'direct'});
 if(action==='update-policy'){assert.equal(body.id,profile.id);assert.equal(body.mode,'');assert.equal(Object.hasOwn(body,'url'),false);assert.equal(profile.autoUpdateHours,undefined);}
});

test('collapsed policy controls retain strict mode validation',async()=>{
 const ui=policyUI();await ui.context.loadProfiles();ui.context.showSubscriptionPolicyForm(subscriptionUIProfile());ui.get('subscriptionPolicyAdvanced').open=false;ui.get('subscriptionPolicyMode').value='unexpected';await ui.get('confirmSubscriptionPolicy').onclick();assert.match(ui.get('subscriptionPolicyError').textContent,/有效的下载方式/);assert.equal(ui.requests.filter(r=>r.method==='POST').length,0);
});

test('node remaining alone shows an estimated source without inventing usage or a total',async()=>{
 const ui=policyUI(subscriptionUIProfile({remaining:1536,source:'nodes',checkedAt:'2026-11-22T00:00:00Z'}));await ui.context.loadProfiles();
 assert.match(usageRow(ui).textContent,/已用 未知.*总额 未知.*剩余 1.50 KiB/);
 assert.match(usageRow(ui).textContent,/节点提示（估算，GB按1024）/);assert.match(usageRow(ui).textContent,/上传 未知.*下载 未知/);
});

test('valid header counters take precedence over derived node used and remaining',async()=>{
 const ui=policyUI(subscriptionUIProfile({upload:1024,download:1024,total:4096,used:9999,remaining:9999,source:'mixed',checkedAt:'2026-11-22T00:00:00Z'}));await ui.context.loadProfiles();
 assert.match(usageRow(ui).textContent,/已用 2.00 KiB.*总额 4.00 KiB.*剩余 2.00 KiB/);assert.match(usageRow(ui).textContent,/响应头优先/);
});

test('node expiry is a date hint and never presented as a precise timestamp',async()=>{
 const ui=policyUI(subscriptionUIProfile({expireDate:'2028-02-29',source:'nodes',checkedAt:'2026-11-22T00:00:00Z'}));await ui.context.loadProfiles();
 assert.match(usageRow(ui).textContent,/到期 2028-02-29（日期提示，具体时刻未知）/);assert.doesNotMatch(usageRow(ui).textContent,/已过期/);
 assert.equal(ui.context.validTrafficDate('2027-02-29'),false);assert.equal(ui.context.validTrafficDate('2028-02-30'),false);assert.equal(ui.context.validTrafficDate('<b>2028-02-29</b>'),false);
});

test('invalid new node fields remain unknown instead of rounding unsafe byte values',async()=>{
 const ui=policyUI(subscriptionUIProfile({used:-1,remaining:Number.MAX_SAFE_INTEGER+1,expireDate:'2026-13-01',source:'nodes'}));await ui.context.loadProfiles();
 assert.match(usageRow(ui).textContent,/已用 未知.*总额 未知.*剩余 未知.*到期 未知/);
});

test('legacy subscriptions and the add form default to auto without enabling automatic updates',async()=>{
 const profile=subscriptionUIProfile();const ui=policyUI(profile);await ui.context.loadProfiles();
 assert.match(cardText(ui),/下载 自动（推荐）/);assert.doesNotMatch(cardText(ui),/UA|条件下载|失败退避|已设置高级选项/);
 await policyButton(ui,'订阅下载设置').onclick();assert.equal(ui.get('subscriptionPolicyMode').value,'auto');assert.equal(ui.get('subscriptionPolicyConditional').checked,true);assert.equal(ui.get('subscriptionPolicyBackoff').checked,true);
 assert.equal(ui.requests.filter(r=>r.method==='POST').length,0);assert.equal(profile.autoUpdateHours,undefined);
 ui.get('cancelSubscriptionPolicy').onclick();ui.get('addSubscription').onclick();assert.equal(ui.radios.find(r=>r.checked).value,'auto');assert.equal(ui.get('subscriptionConditional').checked,true);assert.equal(ui.get('subscriptionBackoff').checked,true);
});

test('saved policy preloads both editors and a custom UA is not exposed in card text',async()=>{
 const profile={...subscriptionUIProfile(),updatePolicy:{mode:'proxy',userAgent:'synthetic-private-UA/1',conditional:false,failureBackoff:false}};const ui=policyUI(profile);await ui.context.loadProfiles();
 assert.match(cardText(ui),/下载 当前代理.*已设置高级选项/);assert.doesNotMatch(cardText(ui),/UA 自定义|条件下载 关|失败退避 关/);assert.doesNotMatch(cardText(ui),/synthetic-private-UA|synthetic-only|synthetic.invalid/);
 await policyButton(ui,'订阅下载设置').onclick();assert.equal(ui.get('subscriptionPolicyUA').value,profile.updatePolicy.userAgent);assert.equal(ui.get('subscriptionPolicyConditional').checked,false);
 ui.get('cancelSubscriptionPolicy').onclick();assert.equal(ui.get('subscriptionPolicyUA').value,'');
 await policyButton(ui,'修改订阅地址').onclick();assert.equal(ui.radios.find(r=>r.checked).value,'proxy');assert.equal(ui.get('subscriptionUA').value,profile.updatePolicy.userAgent);assert.equal(ui.get('subscriptionBackoff').checked,false);
});

test('policy cancellation sends no request and reopening restores saved values',async()=>{
 const profile={...subscriptionUIProfile(),updatePolicy:{mode:'direct',conditional:true,failureBackoff:true}};const ui=policyUI(profile);await ui.context.loadProfiles();await policyButton(ui,'订阅下载设置').onclick();
 ui.get('subscriptionPolicyMode').value='proxy';ui.get('subscriptionPolicyUA').value='synthetic-unsaved/1';ui.get('cancelSubscriptionPolicy').onclick();
 assert.equal(ui.requests.filter(r=>r.method==='POST').length,0);assert.equal(profile.updatePolicy.mode,'direct');assert.equal(ui.get('subscriptionPolicyForm').style.display,'none');assert.equal(ui.get('subscriptionPolicyUA').value,'');
 await policyButton(ui,'订阅下载设置').onclick();assert.equal(ui.get('subscriptionPolicyMode').value,'direct');assert.equal(ui.get('subscriptionPolicyUA').value,'');
});

test('policy save failure preserves input and retry submits the same subscription without a URL',async()=>{
 const profile={...subscriptionUIProfile(),updatePolicy:{mode:'direct',conditional:true,failureBackoff:true}};const submitted=[];let fail=true;
 const ui=policyUI(profile,{request:async(route,request)=>{
  if(route!=='/api/profiles'||request.method!=='POST')return undefined;const body=JSON.parse(request.body);submitted.push(body);
  if(body.action!=='update-policy')return undefined;if(fail)return {ok:false,json:async()=>({error:'synthetic metadata write rejected'})};profile.updatePolicy=body.policy;return {ok:true,json:async()=>({profiles:[profile],activeId:profile.id})};
 }});await ui.context.loadProfiles();await policyButton(ui,'订阅下载设置').onclick();
 ui.get('subscriptionPolicyMode').value='proxy';ui.get('subscriptionPolicyUA').value='synthetic-UA/2';ui.get('subscriptionPolicyConditional').checked=false;
 await ui.get('confirmSubscriptionPolicy').onclick();assert.match(ui.get('subscriptionPolicyError').textContent,/metadata write rejected/);assert.equal(ui.get('subscriptionPolicyForm').style.display,'block');assert.equal(ui.get('subscriptionPolicyUA').value,'synthetic-UA/2');assert.equal(ui.get('confirmSubscriptionPolicy').disabled,false);assert.equal(profile.updatePolicy.mode,'direct');
 fail=false;await ui.get('confirmSubscriptionPolicy').onclick();assert.equal(submitted.length,2);assert.ok(submitted.every(body=>body.action==='update-policy'&&body.id===profile.id&&body.mode===''&&!Object.hasOwn(body,'url')));
 assert.deepEqual(submitted[1].policy,{mode:'proxy',userAgent:'synthetic-UA/2',conditional:false,failureBackoff:true});assert.equal(ui.get('subscriptionPolicyForm').style.display,'none');assert.equal(ui.get('subscriptionPolicyUA').value,'');
});

test('manual update leaves mode empty so the backend uses persisted policy despite connected UI state',async()=>{
 const profile={...subscriptionUIProfile(),updatePolicy:{mode:'direct',conditional:false,failureBackoff:true}};const ui=policyUI(profile);ui.emit({running:true});await ui.context.loadProfiles();await policyButton(ui,'更新').onclick();
 const request=ui.requests.find(r=>r.method==='POST');assert.equal(JSON.parse(request.body).action,'update');assert.equal(JSON.parse(request.body).mode,'');assert.equal(profile.updatePolicy.mode,'direct');
});

test('URL edits submit the selected policy together with the candidate address',async()=>{
 const profile={...subscriptionUIProfile(),updatePolicy:{mode:'proxy',userAgent:'synthetic-old/1',conditional:true,failureBackoff:true}};const ui=policyUI(profile);await ui.context.loadProfiles();await policyButton(ui,'修改订阅地址').onclick();
 ui.get('subscriptionURL').value='https://synthetic.invalid/new';ui.radios.forEach(r=>r.checked=r.value==='direct');ui.get('subscriptionUA').value='synthetic-new/1';ui.get('subscriptionBackoff').checked=false;
 await ui.get('confirmSubscription').onclick();const body=JSON.parse(ui.requests.find(r=>r.method==='POST').body);assert.equal(body.action,'edit-url');assert.equal(body.id,profile.id);assert.equal(body.mode,'direct');assert.equal(body.url,'https://synthetic.invalid/new');assert.deepEqual(body.policy,{mode:'direct',userAgent:'synthetic-new/1',conditional:true,failureBackoff:false});
});

for(const [name,value] of [['line break','valid\r\nX-Private: synthetic'],['control','valid\u0001'],['non-ASCII','测试UA'],['oversized','x'.repeat(513)]])test('unsafe UA '+name+' is rejected locally without sending a request',async()=>{
 const ui=policyUI();await ui.context.loadProfiles();await policyButton(ui,'订阅下载设置').onclick();ui.get('subscriptionPolicyUA').value=value;await ui.get('confirmSubscriptionPolicy').onclick();
 assert.match(ui.get('subscriptionPolicyError').textContent,/ASCII/);assert.equal(ui.requests.filter(r=>r.method==='POST').length,0);assert.equal(ui.get('subscriptionPolicyUA').value,value);assert.equal(ui.get('confirmSubscriptionPolicy').disabled,false);
});

test('busy policy save prevents duplicate requests and unlocks controls only after the finish event',async()=>{
 let reply;const ui=policyUI(subscriptionUIProfile(),{request:async(route,request)=>route==='/api/profiles'&&request.method==='POST'?new Promise(resolve=>{reply=resolve}):undefined});await ui.context.loadProfiles();await policyButton(ui,'订阅下载设置').onclick();
 const saving=ui.get('confirmSubscriptionPolicy').onclick();await new Promise(setImmediate);assert.equal(ui.get('subscriptionPolicyMode').disabled,true);assert.equal(ui.get('subscriptionPolicyUA').disabled,true);
 await ui.get('confirmSubscriptionPolicy').onclick();assert.equal(ui.requests.filter(r=>r.method==='POST').length,1);ui.emit({busy:true});reply({ok:true,json:async()=>({profiles:ui.profiles})});await saving;assert.equal(ui.get('confirmSubscriptionPolicy').disabled,true);ui.emit({busy:false});assert.equal(ui.get('confirmSubscriptionPolicy').disabled,false);assert.equal(ui.get('subscriptionPolicyUA').value,'');
});

test('local profiles have no update-policy action and retry metadata is visible without validator content',async()=>{
 const profile={...subscriptionUIProfile(),autoUpdateHours:6,nextUpdateAt:'2026-11-22T00:15:00Z',updateRetry:{consecutiveFailures:2,retryAt:'2026-11-22T00:15:00Z'}};const local={id:'f'.repeat(64),source:'local',name:'Synthetic local',size:10};const ui=policyUI(profile,{profiles:[profile,local]});await ui.context.loadProfiles();
 assert.match(cardText(ui),/退避重试.*连续自动更新失败 2 次/);assert.ok(ui.get('profileList').children[1].children[1].children.every(button=>button.textContent!=='订阅下载设置'));
});


