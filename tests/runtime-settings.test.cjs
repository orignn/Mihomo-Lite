'use strict';

const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const test = require('node:test');
const vm = require('node:vm');

// Evaluate only the actual settings functions and checkbox handlers. Exclude
// startup/polling calls, and provide no fetch, browser, process, or real API.
const indexPath = path.join(__dirname, '..', 'cmd', 'mihomolite', 'index.go');
const lines = fs.readFileSync(indexPath, 'utf8').split(/\r?\n/);
const start = lines.findIndex(line => line.startsWith('let settingsBusy='));
const end = lines.findIndex((line, index) => index > start && line.startsWith('loadSettings();setInterval(loadSettings,'));
assert.ok(start >= 0 && end > start, 'Cannot locate runtime settings JavaScript in index.go');
const settingsSource = lines.slice(start, end).join('\n');

function deferred() {
  let resolve;
  const promise = new Promise(done => { resolve = done; });
  return { promise, resolve };
}

function harness(apiHandler, initial = { autoStart: false, closeToTray: true }) {
  const controls = {
    autoStart: { checked: false, disabled: false },
    closeToTray: { checked: false, disabled: false },
    silentStart: { checked: false, disabled: false },
  };
  for (const field of ['proxy', 'backend', 'panel', 'web']) {
    controls[field + 'PortMode'] = { value: 'auto', disabled: false };
    controls[field + 'Port'] = { value: '', disabled: true };
  }
  controls.savePortSettings = { disabled: false };
  controls.portSettingsStatus = { textContent: '' };
  controls.portSettingsError = { textContent: '' };
  for (const id of ['startupConnect','crashReconnect','connectionMaxRetries','connectionInitialDelay','connectionMaxDelay','saveConnectionPolicy','connectionPolicyError']) controls[id] = { value: '', checked: false, disabled: false, textContent: '' };
  const calls = [];
  const toasts = [];
  const context = vm.createContext({
    $: id => {
      assert.ok(Object.hasOwn(controls, id), 'Unexpected DOM access: ' + id);
      return controls[id];
    },
    toast: message => toasts.push(message),
    api: (route, options = {}) => {
      assert.equal(route, '/api/settings', 'Unexpected API path');
      const request = {
        method: options.method || 'GET',
        patch: options.body === undefined ? undefined : JSON.parse(options.body),
      };
      calls.push(request);
      return apiHandler(request);
    },
  });
  vm.runInContext(settingsSource, context, {
    filename: indexPath,
    lineOffset: start,
    timeout: 1000,
  });
  assert.equal(typeof context.applySettings, 'function');
  assert.equal(typeof context.loadSettings, 'function');
  assert.equal(typeof context.saveRuntimeSettings, 'function');
  assert.equal(typeof controls.autoStart.onchange, 'function');
  assert.equal(typeof controls.closeToTray.onchange, 'function');
  assert.equal(typeof controls.silentStart.onchange, 'function');
  context.applySettings({ ...initial });
  return { controls, calls, toasts, context };
}

const changes = [
  { field: 'autoStart', initial: { autoStart: false, closeToTray: true }, target: true },
  { field: 'closeToTray', initial: { autoStart: false, closeToTray: true, silentStart: false }, target: false },
  { field: 'silentStart', initial: { autoStart: false, closeToTray: true, silentStart: false }, target: true },
];

for (const change of changes) {
  test('failed PUT and failed GET roll back confirmed ' + change.field, { timeout: 2000 }, async () => {
    const h = harness(request => {
      throw new Error(request.method === 'PUT' ? 'injected save failure' : 'injected refresh failure');
    }, change.initial);
    h.controls[change.field].checked = change.target;
    await h.controls[change.field].onchange();

    assert.deepEqual(h.calls, [
      { method: 'PUT', patch: { [change.field]: change.target } },
      { method: 'GET', patch: undefined },
    ]);
    assert.equal(h.controls.autoStart.checked, change.initial.autoStart);
    assert.equal(h.controls.closeToTray.checked, change.initial.closeToTray);
    assert.equal(h.controls.autoStart.disabled, false);
    assert.equal(h.controls.closeToTray.disabled, false);
  assert.equal(h.controls.silentStart.disabled, false);
    assert.equal(h.controls.silentStart.checked, change.initial.silentStart ?? false);
    assert.equal(h.controls.silentStart.disabled, false);
    assert.deepEqual(h.toasts, ['injected save failure']);
  });

  test('pre-save GET cannot overwrite a successful ' + change.field + ' PUT', { timeout: 2000 }, async () => {
    const oldRead = deferred();
    let reads = 0;
    const saved = { ...change.initial, [change.field]: change.target };
    const h = harness(request => {
      if (request.method === 'PUT') return Promise.resolve({ ...saved });
      return reads++ === 0 ? oldRead.promise : Promise.resolve({ ...saved });
    }, change.initial);

    const pendingRead = h.context.loadSettings();
    h.controls[change.field].checked = change.target;
    await h.controls[change.field].onchange();
    assert.equal(h.controls[change.field].checked, change.target);

    oldRead.resolve({ ...change.initial });
    await pendingRead;
    assert.equal(h.controls.autoStart.checked, saved.autoStart);
    assert.equal(h.controls.closeToTray.checked, saved.closeToTray);
    assert.equal(h.controls.silentStart.checked, saved.silentStart ?? false);
    assert.deepEqual(h.calls.map(request => request.method), ['GET', 'PUT', 'GET']);
    assert.deepEqual(h.calls.find(request => request.method === 'PUT').patch, { [change.field]: change.target });
  });
}

test('busy settings suppress duplicate PUTs and concurrent polling', { timeout: 2000 }, async () => {
  const pendingSave = deferred();
  const saved = { autoStart: false, closeToTray: false };
  const h = harness(request => request.method === 'PUT' ? pendingSave.promise : Promise.resolve({ ...saved }));

  h.controls.closeToTray.checked = false;
  const firstSave = h.controls.closeToTray.onchange();
  assert.equal(h.controls.autoStart.disabled, true);
  assert.equal(h.controls.closeToTray.disabled, true);
  assert.equal(h.controls.silentStart.disabled, true);

  // Exercise the real callbacks even though disabled controls cannot be clicked
  // normally: a queued or programmatic change must not emit a second request.
  h.controls.autoStart.checked = true;
  await h.controls.autoStart.onchange();
  await h.controls.closeToTray.onchange();
  await h.context.loadSettings();
  assert.deepEqual(h.calls, [{ method: 'PUT', patch: { closeToTray: false } }]);
  assert.equal(h.controls.autoStart.disabled, true);
  assert.equal(h.controls.closeToTray.disabled, true);

  pendingSave.resolve({ ...saved });
  await firstSave;
  assert.deepEqual(h.calls.map(request => request.method), ['PUT', 'GET']);
  assert.equal(h.controls.autoStart.checked, false);
  assert.equal(h.controls.closeToTray.checked, false);
  assert.equal(h.controls.autoStart.disabled, false);
  assert.equal(h.controls.closeToTray.disabled, false);
  assert.equal(h.toasts.length, 1);
});

test('newer overlapping GET takes precedence over an older poll', { timeout: 2000 }, async () => {
  const oldRead = deferred();
  const newRead = deferred();
  const initial = { autoStart: true, closeToTray: true };
  const fresh = { autoStart: false, closeToTray: false };
  let reads = 0;
  const h = harness(request => {
    assert.equal(request.method, 'GET');
    return reads++ === 0 ? oldRead.promise : newRead.promise;
  }, initial);

  const firstPoll = h.context.loadSettings();
  const secondPoll = h.context.loadSettings();
  newRead.resolve({ ...fresh });
  await secondPoll;
  assert.equal(h.controls.autoStart.checked, false);
  assert.equal(h.controls.closeToTray.checked, false);

  oldRead.resolve({ ...initial });
  await firstPoll;
  assert.equal(h.controls.autoStart.checked, false);
  assert.equal(h.controls.closeToTray.checked, false);
  assert.deepEqual(h.calls.map(request => request.method), ['GET', 'GET']);
});

test('real state render invalidates an older GET after a tray/SSE update', { timeout: 2000 }, async () => {
  const oldRead = deferred();
  const initial = { autoStart: false, closeToTray: true };
  const fresh = { autoStart: true, closeToTray: false };
  const h = harness(request => {
    assert.equal(request.method, 'GET');
    return oldRead.promise;
  }, initial);

  // Exercise the actual render -> applySettings(..., true) connection rather
  // than assuming the state renderer passes the event-origin flag correctly.
  const renderStart = lines.findIndex(line => line.startsWith('function render(s)'));
  const renderEnd = lines.findIndex((line, index) => index > renderStart && line.startsWith('async function api('));
  assert.ok(renderStart >= 0 && renderEnd > renderStart, 'Cannot locate state render in index.go');
  const renderSource = lines.slice(renderStart, renderEnd).join('\n');
  for (const match of renderSource.matchAll(/\$\('([^']+)'\)/g)) {
    if (!Object.hasOwn(h.controls, match[1])) {
      h.controls[match[1]] = { style: {}, disabled: false, textContent: '', className: '' };
    }
  }
  h.context.state = {};
  h.context.busy = false;
  vm.runInContext(renderSource, h.context, { filename: indexPath, lineOffset: renderStart, timeout: 1000 });

  const pendingPoll = h.context.loadSettings();
  h.context.render({ ...fresh, busy: false, running: false, coreReady: true, panelReady: true });
  assert.equal(h.controls.autoStart.checked, true);
  assert.equal(h.controls.closeToTray.checked, false);

  oldRead.resolve({ ...initial });
  await pendingPoll;
  assert.equal(h.controls.autoStart.checked, true);
  assert.equal(h.controls.closeToTray.checked, false);
  assert.deepEqual(h.calls.map(request => request.method), ['GET']);
});

test('each checkbox sends only its target patch and preserves the other setting', { timeout: 2000 }, async () => {
  const backend = { autoStart: false, closeToTray: true, silentStart: false };
  const h = harness(request => {
    if (request.method === 'PUT') Object.assign(backend, request.patch);
    return Promise.resolve({ ...backend });
  }, backend);

  h.controls.autoStart.checked = true;
  await h.controls.autoStart.onchange();
  assert.deepEqual(backend, { autoStart: true, closeToTray: true, silentStart: false });
  assert.equal(h.controls.closeToTray.checked, true);

  h.controls.closeToTray.checked = false;
  await h.controls.closeToTray.onchange();
  assert.deepEqual(backend, { autoStart: true, closeToTray: false, silentStart: false });
  assert.equal(h.controls.autoStart.checked, true);
  assert.equal(h.controls.closeToTray.checked, false);
  h.controls.silentStart.checked = true;
  await h.controls.silentStart.onchange();
  assert.equal(backend.silentStart, true);
  assert.equal(h.controls.autoStart.checked, true);
  assert.equal(h.controls.closeToTray.checked, false);
  assert.deepEqual(h.calls.filter(request => request.method === 'PUT').map(request => request.patch), [
    { autoStart: true },
    { closeToTray: false },
    { silentStart: true },
  ]);
});

function autoPorts() { return Object.fromEntries(['proxy','backend','panel','web'].map(field => [field,{mode:'auto',port:0}])); }
function fixedPorts() { return Object.fromEntries(['proxy','backend','panel','web'].map((field,index) => [field,{mode:'fixed',port:24000+index}])); }
function editPort(h, field, mode, port = '') {
 h.controls[field+'PortMode'].value=mode;h.controls[field+'PortMode'].onchange();
 h.controls[field+'Port'].value=String(port);h.controls[field+'Port'].oninput();
}

test('port form sends only structured ports and explicitly reports restart semantics', async()=>{
 const initial={autoStart:false,closeToTray:true,ports:autoPorts(),activePorts:autoPorts(),portsRestartRequired:false};
 const saved={...initial,ports:fixedPorts(),portsRestartRequired:true};
 const h=harness(request=>Promise.resolve(request.method==='PUT'?saved:saved),initial);
 for(const [field,option] of Object.entries(saved.ports))editPort(h,field,option.mode,option.port);
 assert.equal(h.calls.length,0,'editing must not implicitly save');
 await h.controls.savePortSettings.onclick();
 assert.deepEqual(h.calls[0],{method:'PUT',patch:{ports:saved.ports}});
 assert.match(h.controls.portSettingsStatus.textContent,/程序重启/);
 assert.match(h.controls.portSettingsStatus.textContent,/单独重连不会/);
 assert.equal(h.controls.webPort.value,'24003');
});

for(const value of ['', '0','-1','65536','1.5','1e4',' 80','abc'])test('invalid fixed port '+JSON.stringify(value)+' never reaches API',async()=>{
 const h=harness(()=>assert.fail('invalid port called API'),{ports:autoPorts()});
 editPort(h,'proxy','fixed',value);await h.controls.savePortSettings.onclick();
 assert.equal(h.calls.length,0);assert.match(h.controls.portSettingsError.textContent,/1–65535/);
 assert.equal(h.controls.proxyPort.value,value);
});

test('every cross-endpoint fixed collision is rejected',async()=>{
 for(const [first,second] of [['proxy','backend'],['proxy','panel'],['proxy','web'],['backend','panel'],['backend','web'],['panel','web']]){
  const h=harness(()=>assert.fail('collision called API'),{ports:autoPorts()});
  editPort(h,first,'fixed',12345);editPort(h,second,'fixed',12345);await h.controls.savePortSettings.onclick();
  assert.match(h.controls.portSettingsError.textContent,/不能重复/);assert.equal(h.calls.length,0);
 }
});

test('unsaved port input survives polling and unrelated SSE settings',async()=>{
 const initial={ports:autoPorts()};const h=harness(()=>Promise.resolve(initial),initial);
 editPort(h,'proxy','fixed',65535);await h.context.loadSettings();h.context.applySettings({ports:fixedPorts()},true);
 assert.equal(h.controls.proxyPort.value,'65535');assert.equal(h.controls.proxyPortMode.value,'fixed');
 assert.match(h.controls.portSettingsStatus.textContent,/尚未保存/);
});

test('failed port persistence retains edits for retry even when recovery GET fails',async()=>{
 const h=harness(()=>{throw new Error('injected port save failure')},{ports:autoPorts()});
 editPort(h,'web','fixed',17655);await h.controls.savePortSettings.onclick();
 assert.equal(h.controls.webPort.value,'17655');assert.equal(h.controls.webPortMode.value,'fixed');
 assert.equal(h.controls.portSettingsError.textContent,'injected port save failure');assert.equal(h.controls.savePortSettings.disabled,false);
});

test('automatic mode disables ignored input and persists zero rather than stale fixed value',async()=>{
 const initial={ports:fixedPorts()};let patch;
 const h=harness(request=>{if(request.method==='PUT')patch=request.patch;return Promise.resolve({...initial,ports:patch?.ports||initial.ports})},initial);
 editPort(h,'proxy','auto','65536');assert.equal(h.controls.proxyPort.disabled,true);
 await h.controls.savePortSettings.onclick();assert.deepEqual(patch.ports.proxy,{mode:'auto',port:0});
});

test('pending port save locks fields and supersedes a stale GET',async()=>{
 const old=deferred(),put=deferred();let reads=0;const initial={ports:autoPorts()};const saved={ports:fixedPorts(),portsRestartRequired:true};
 const h=harness(request=>request.method==='PUT'?put.promise:(reads++===0?old.promise:Promise.resolve(saved)),initial);
 const oldLoad=h.context.loadSettings();for(const [field,option] of Object.entries(saved.ports))editPort(h,field,'fixed',option.port);
 const save=h.controls.savePortSettings.onclick();assert.equal(h.controls.savePortSettings.disabled,true);assert.equal(h.controls.webPortMode.disabled,true);
 await h.controls.savePortSettings.onclick();assert.equal(h.calls.filter(x=>x.method==='PUT').length,1);
 put.resolve(saved);await save;old.resolve(initial);await oldLoad;
 assert.equal(h.controls.webPort.value,'24003');assert.match(h.controls.portSettingsStatus.textContent,/程序重启/);
});

