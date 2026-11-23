'use strict';

// Invoked by TestDraftsBrowser after its Windows job owns this process.
// Uses the installed Edge and Node's built-in WebSocket only.
const assert = require('node:assert/strict');
const { spawn } = require('node:child_process');
const fs = require('node:fs');
const path = require('node:path');
const { setTimeout: delay } = require('node:timers/promises');

const gate = 'MIHOMOLITE_TEST_DRAFT_BROWSER';
const fixtureHeader = 'X-Mihomo-Draft-Browser-Fixture';
const rulesTab = 'button[data-view="rulesView"]';
const connectionTab = 'button[data-view="connectionView"]';
const maintenanceTab = 'button[data-view="maintenanceView"]';
const results = { checks: [], failures: [] };

async function eventually(check, description, timeout = 9000) {
  const deadline = Date.now() + timeout;
  while (Date.now() < deadline) {
    const value = await check();
    if (value) return value;
    await delay(50);
  }
  throw new Error('Timed out waiting for ' + description);
}

class CDP {
  constructor(socket) {
    this.socket = socket;
    this.sequence = 0;
    this.pending = new Map();
    this.events = [];
    this.methods = new Map();
    this.handlers = new Map();
    socket.addEventListener('message', event => {
      const data = JSON.parse(String(event.data));
      if (data.id) {
        const pending = this.pending.get(data.id);
        if (!pending) return;
        this.pending.delete(data.id);
        clearTimeout(pending.timer);
        if (data.error) pending.reject(new Error(pending.method + ': ' + data.error.message));
        else pending.resolve(data.result);
        return;
      }
      const handler = this.handlers.get(data.method);
      if (handler) {
        Promise.resolve(handler(data)).catch(error => {
          if (!this.closing) results.failures.push(error.message);
        });
      }
      if (data.method === 'Network.requestWillBeSent') this.methods.set(data.params.requestId, data.params.request.method);
      if (['Network.responseReceived', 'Browser.downloadWillBegin', 'Browser.downloadProgress', 'Page.fileChooserOpened'].includes(data.method)) {
        this.events.push(data);
      }
    });
    socket.addEventListener('close', () => {
      for (const pending of this.pending.values()) {
        clearTimeout(pending.timer);
        pending.reject(new Error('Edge disconnected during ' + pending.method));
      }
      this.pending.clear();
    });
  }

  send(method, params = {}, sessionId) {
    const id = ++this.sequence;
    return new Promise((resolve, reject) => {
      const timer = setTimeout(() => {
        this.pending.delete(id);
        reject(new Error('CDP command timed out: ' + method));
      }, 7000);
      this.pending.set(id, { method, timer, resolve, reject });
      try {
        this.socket.send(JSON.stringify({ id, method, params, sessionId }));
      } catch (error) {
        this.pending.delete(id);
        clearTimeout(timer);
        reject(error);
      }
    });
  }

  waitEvent(from, method, predicate = () => true) {
    return eventually(() => this.events.slice(from).find(event => event.method === method && predicate(event.params))?.params, method);
  }

  waitResponse(from, method, pathname, status) {
    return eventually(() => {
      const event = this.events.slice(from).find(({ method: name, params }) => name === 'Network.responseReceived' &&
        this.methods.get(params.requestId) === method && new URL(params.response.url).pathname === pathname);
      if (!event) return null;
      assert.equal(event.params.response.status, status, method + ' ' + pathname);
      return event.params;
    }, method + ' ' + pathname + ' to return ' + status);
  }
}

class Page {
  constructor(cdp, session) {
    this.cdp = cdp;
    this.session = session;
  }

  send(method, params) {
    return this.cdp.send(method, params, this.session);
  }

  async eval(fn, ...args) {
    const expression = '(' + fn.toString() + ')(...' + JSON.stringify(args) + ')';
    const { result, exceptionDetails } = await this.send('Runtime.evaluate', { expression, returnByValue: true });
    if (exceptionDetails) throw new Error(result.description || exceptionDetails.text);
    return result.value;
  }

  async click(selector, text, card) {
    const point = await eventually(() => this.eval((selector, text, card) => {
      const scope = card === null ? document : [...document.querySelectorAll('.profile-card')].find(el =>
        el.querySelector('.profile-name')?.firstChild?.textContent.trim() === card);
      const el = [...(scope?.querySelectorAll(selector) || [])].find(el =>
        (text === null || el.textContent.trim() === text) && !el.disabled && el.getClientRects().length);
      if (!el) return null;
      el.scrollIntoView({ block: 'center', inline: 'nearest', behavior: 'instant' });
      const rect = el.getBoundingClientRect();
      const x = (Math.max(0, rect.left) + Math.min(innerWidth, rect.right)) / 2;
      const y = (Math.max(0, rect.top) + Math.min(innerHeight, rect.bottom)) / 2;
      return el.contains(document.elementFromPoint(x, y)) ? { x, y } : null;
    }, selector, text ?? null, card ?? null), [selector, text, card].filter(Boolean).join(' '));
    await this.send('Input.dispatchMouseEvent', { type: 'mousePressed', ...point, button: 'left', clickCount: 1 });
    await this.send('Input.dispatchMouseEvent', { type: 'mouseReleased', ...point, button: 'left', clickCount: 1 });
  }

  async fill(selector, value) {
    await this.click(selector);
    const key = { key: 'a', code: 'KeyA', windowsVirtualKeyCode: 65, modifiers: 2 };
    await this.send('Input.dispatchKeyEvent', { type: 'rawKeyDown', ...key });
    await this.send('Input.dispatchKeyEvent', { type: 'keyUp', ...key });
    await this.send('Input.insertText', { text: value });
    assert.equal(await this.value(selector), value, 'trusted input in ' + selector);
  }

  visible(selector) {
    return this.eval(selector => !!document.querySelector(selector)?.getClientRects().length, selector);
  }

  value(selector) {
    return this.eval(selector => document.querySelector(selector)?.value ?? null, selector);
  }

  text(selector) {
    return this.eval(selector => document.querySelector(selector)?.textContent ?? '', selector);
  }

  waitVisible(selector) {
    return eventually(() => this.visible(selector), selector + ' to become visible');
  }

  waitHidden(selector) {
    return eventually(async () => !await this.visible(selector), selector + ' to become hidden');
  }

  async chooseDraft(choice) {
    await this.waitVisible('#draftDecision[open]');
    await this.click('#draft' + choice);
    if (choice !== 'Save') await this.waitHidden('#draftDecision[open]');
  }
}

function record(name) {
  results.checks.push(name);
  console.log('PASS ' + name);
}

async function readFixture() {
  assert.equal(process.env[gate], '1', 'launch only from the opt-in Go fixture');
  assert.equal(process.platform, 'win32');
  assert.equal(typeof WebSocket, 'function', 'Node 22+ with its built-in WebSocket is required');
  assert.match(path.resolve(process.cwd()), /[\\/]\.cache[\\/]tests[\\/]opt-002-003[\\/]20261120-development[\\/]browser[\\/]run-\d+$/i);
  let data = '';
  for await (const chunk of process.stdin) {
    data += chunk;
    assert.ok(data.length < 16384, 'fixture manifest exceeded 16 KiB');
  }
  const fixture = JSON.parse(data);
  const target = new URL(fixture.url);
  assert.match(fixture.url, /^http:\/\/127\.0\.0\.1:\d{1,5}$/);
  assert.ok(target.port && target.port !== '17653', 'refusing a daily instance or default port');
  assert.match(fixture.token, /^[a-f0-9]{64}$/);
  assert.ok(path.isAbsolute(fixture.edge) && /^msedge\.exe$/i.test(path.basename(fixture.edge)));
  return fixture;
}

function apiClient(fixture) {
  return async requestPath => {
    const url = new URL(requestPath, fixture.url);
    assert.equal(url.origin, fixture.url);
    const response = await fetch(url, {
      headers: { [fixtureHeader]: fixture.token }, redirect: 'error', signal: AbortSignal.timeout(5000),
    });
    if (!response.ok) throw new Error(requestPath + ' returned ' + response.status + ': ' + await response.text());
    return response.json();
  };
}

async function openCDP(profile) {
  const devtools = await eventually(() => {
    try {
      const [port, socket] = fs.readFileSync(path.join(profile, 'DevToolsActivePort'), 'utf8').trim().split(/\r?\n/);
      return /^\d{1,5}$/.test(port) && Number(port) > 0 && Number(port) <= 65535 && Number(port) !== 17653 &&
        /^\/devtools\/browser\/[a-f0-9-]+$/i.test(socket) && 'ws://127.0.0.1:' + port + socket;
    } catch (error) {
      if (error.code !== 'ENOENT') throw error;
      return null;
    }
  }, 'the owned Edge debugging socket');
  const socket = new WebSocket(devtools);
  await new Promise((resolve, reject) => {
    const timer = setTimeout(() => {
      socket.close();
      reject(new Error('Could not open the owned Edge debugging socket'));
    }, 5000);
    socket.addEventListener('open', () => { clearTimeout(timer); resolve(); }, { once: true });
    socket.addEventListener('error', () => { clearTimeout(timer); reject(new Error('Edge debugging socket error')); }, { once: true });
  });
  return new CDP(socket);
}

async function attachPage(cdp, fixture) {
  const { targetId } = await cdp.send('Target.createTarget', { url: 'about:blank' });
  const { sessionId } = await cdp.send('Target.attachToTarget', { targetId, flatten: true });
  const page = new Page(cdp, sessionId);
  const dialogs = [];
  cdp.handlers.set('Page.javascriptDialogOpening', async ({ params }) => {
    // A reload is deliberately allowed after the browser's beforeunload prompt.
    const expected = params.type === 'beforeunload' || (params.type === 'confirm' && /备份|还原|恢复/.test(params.message));
    if (!expected) results.failures.push('Unexpected browser dialog: ' + params.type);
    dialogs.push(params.type);
    await page.send('Page.handleJavaScriptDialog', { accept: expected });
  });
  cdp.handlers.set('Runtime.exceptionThrown', ({ params }) => {
    results.failures.push(params.exceptionDetails.exception?.description || params.exceptionDetails.text);
  });
  cdp.handlers.set('Fetch.requestPaused', async ({ params }) => {
    const url = new URL(params.request.url);
    // Match the whole origin, including the random port. Blob downloads use
    // the same origin and are generated by the embedded page itself.
    if (url.origin === fixture.url && ['http:', 'blob:'].includes(url.protocol)) {
      await page.send('Fetch.continueRequest', { requestId: params.requestId });
    } else {
      await page.send('Fetch.failRequest', { requestId: params.requestId, errorReason: 'BlockedByClient' });
      results.failures.push('Blocked browser request outside the fixture: ' + url.origin);
    }
  });
  await page.send('Runtime.enable');
  await page.send('Page.enable');
  await page.send('DOM.enable');
  await page.send('Network.enable');
  await page.send('Fetch.enable');
  await page.send('Network.setExtraHTTPHeaders', { headers: { [fixtureHeader]: fixture.token } });
  await page.send('Emulation.setDeviceMetricsOverride', { width: 1200, height: 950, deviceScaleFactor: 1, mobile: false });
  const navigation = await page.send('Page.navigate', { url: fixture.url + '/' });
  assert.ok(!navigation.errorText, navigation.errorText);
  return { page, dialogs };
}

async function ready(page, fixture) {
  await eventually(async () => await page.text('#version') === 'v' + fixture.version, 'embedded page version');
  await eventually(async () => (await page.text('#profileList')).includes(fixture.profiles[0].name), 'loaded synthetic profiles');
  await page.waitVisible('#connectionView');
}

async function reload(page, fixture) {
  const oldTimeOrigin = await page.eval(() => performance.timeOrigin);
  await page.send('Page.reload', { ignoreCache: true });
  await eventually(async () => {
    try {
      return await page.eval(() => performance.timeOrigin) !== oldTimeOrigin;
    } catch (error) {
      if (!/context|navigat/i.test(error.message)) throw error;
      return false;
    }
  }, 'a fresh page after reload');
  await ready(page, fixture);
}

async function smokeRules(page, cdp, api, fixture) {
  const queued = 'queued.browser-fixture.invalid';
  const pending = 'unsubmitted.browser-fixture.invalid';
  const note = 'Synthetic rule form note';
  const visibleRules = () => page.text('#rulesList');
  const assertDraft = async () => {
    await page.waitVisible('#ruleForm');
    assert.equal(await page.value('#ruleValue'), pending);
    assert.equal(await page.value('#ruleNote'), note);
    assert.ok((await visibleRules()).includes(queued), 'queued rule draft');
  };
  await page.click(rulesTab);
  await eventually(async () => (await visibleRules()).includes(fixture.rule.value), 'saved rule to render');
  await page.click('#addRule');
  await page.fill('#ruleValue', queued);
  await page.click('#confirmRule');
  await page.click('#addRule');
  await page.fill('#ruleValue', pending);
  await page.fill('#ruleNote', note);

  await page.click(connectionTab);
  await page.chooseDraft('Cancel');
  await page.waitVisible('#rulesView');
  await assertDraft();
  await page.click(connectionTab);
  await page.chooseDraft('Keep');
  await page.waitVisible('#connectionView');
  await page.click(rulesTab);
  await assertDraft();
  assert.deepEqual((await api('/api/rules')).rules, [fixture.rule]);
  record('Rules list and unsubmitted form survive Cancel, Keep, and tab round trip');

  await page.click(connectionTab);
  await page.chooseDraft('Keep');
  await reload(page, fixture);
  await page.click('#draftList button', '恢复规则草稿');
  await assertDraft();
  assert.deepEqual((await api('/api/rules')).rules, [fixture.rule]);
  record('Rules and pending form recover after a real reload without saving to HTTP');

  await page.fill('#ruleValue', queued);
  await page.click('#confirmRule');
  const duplicateResponse = cdp.events.length;
  await page.click('#saveRules');
  await cdp.waitResponse(duplicateResponse, 'PUT', '/api/rules', 400);
  await eventually(async () => (await page.text('#rulesError')).includes('重复'), 'real rule validation error');
  const duplicates = () => page.eval(queued => [...document.querySelectorAll('#rulesList .rule-value')].filter(el =>
    el.firstChild?.textContent === queued).length, queued);
  assert.equal(await duplicates(), 2);
  assert.deepEqual((await api('/api/rules')).rules, [fixture.rule]);
  await reload(page, fixture);
  await page.click('#draftList button', '恢复规则草稿');
  await eventually(async () => await duplicates() === 2, 'failed rule save to recover after reload');
  record('Rejected rules save does not replace saved rules and its draft recovers after reload');

  await page.click(connectionTab);
  await page.chooseDraft('Discard');
  await page.waitHidden('#draftBox');
  await page.click(rulesTab);
  await eventually(async () => (await visibleRules()).includes(fixture.rule.value), 'saved rule after discarding');
  assert.ok(!(await visibleRules()).includes(queued));
  await page.waitHidden('#ruleForm');
  await page.click(connectionTab);
  record('Explicit Discard removes the rules draft and leaves saved rules');

  await page.click(rulesTab);
  await page.click('#addRule');
  await page.fill('#ruleValue', pending);
  await page.fill('#ruleNote', note);
  await page.click('#cancelRule');
  await page.chooseDraft('Keep');
  await page.waitHidden('#ruleForm');
  await page.click('#addRule');
  const later = 'other-form.browser-fixture.invalid';
  await page.fill('#ruleValue', later);
  await page.click(connectionTab);
  await page.chooseDraft('Keep');
  await reload(page, fixture);

  await page.click('#draftList button', '恢复规则表单草稿 1');
  await page.chooseDraft('Keep');
  await eventually(async () => await page.value('#ruleValue') === pending, 'first parked rule form');
  assert.equal(await page.value('#ruleNote'), note);
  await page.click(connectionTab);
  await page.chooseDraft('Keep');
  await page.click('#draftList button', '恢复规则表单草稿 1');
  await page.chooseDraft('Keep');
  await eventually(async () => await page.value('#ruleValue') === later, 'second parked rule form');
  assert.deepEqual((await api('/api/rules')).rules, [fixture.rule]);
  record('Two separately parked rule forms survive reload and can both be resumed');

  await page.click(connectionTab);
  await page.chooseDraft('Discard');
  await page.waitHidden('#draftBox');
}

const yamlRestoreButton = profile => '恢复“' + profile.name + '”草稿';

async function waitConfig(api, profile, yaml = profile.yaml) {
  await eventually(async () => {
    const current = await api('/api/config');
    return current.profileId === profile.id && current.content === yaml;
  }, 'persisted YAML for ' + profile.name);
}

async function smokeYAML(page, cdp, api, fixture) {
  const [alpha, beta] = fixture.profiles;
  const alphaDraft = alpha.yaml + '# Alpha unsaved browser draft\n';
  const betaDraft = beta.yaml + '# Beta unsaved browser draft\n';
  const invalid = 'proxies: [unclosed\n';
  const corrected = alpha.yaml + '# Alpha saved edit\n';
  const checkEditor = async value => {
    await page.waitVisible('#config');
    assert.equal(await page.value('#config'), value);
  };
  const clickSwitch = profile => page.click('.profile-buttons button', '切换', profile.name);
  const switchTo = async (profile, keep = false, yaml = profile.yaml) => {
    const checkpoint = cdp.events.length;
    await clickSwitch(profile);
    if (keep) await page.chooseDraft('Keep');
    await cdp.waitResponse(checkpoint, 'POST', '/api/profiles', 200);
    await waitConfig(api, profile, yaml);
    await page.waitHidden('#config');
  };

  await page.click('#edit');
  await checkEditor(alpha.yaml);
  await page.fill('#config', alphaDraft);
  await page.click(rulesTab);
  await page.chooseDraft('Keep');
  await page.waitVisible('#rulesView');
  await page.click(connectionTab);
  if (!await page.visible('#config')) await page.click('#draftList button', yamlRestoreButton(alpha));
  await checkEditor(alphaDraft);
  await waitConfig(api, alpha);
  record('YAML draft survives a Keep decision and tab round trip');

  await switchTo(beta, true);
  await page.click('#edit');
  await checkEditor(beta.yaml);
  await page.fill('#config', betaDraft);
  await switchTo(alpha, true);
  await reload(page, fixture);
  await page.click('#draftList button', yamlRestoreButton(alpha));
  await checkEditor(alphaDraft);
  assert.ok((await page.text('#draftList')).includes(yamlRestoreButton(beta)));
  record('Separate configuration drafts survive profile switches and reload');

  await page.fill('#config', invalid);
  await clickSwitch(beta);
  const firstResponse = cdp.events.length;
  await page.chooseDraft('Save');
  await cdp.waitResponse(firstResponse, 'POST', '/api/profiles', 400);
  await eventually(() => page.text('#draftDecisionError'), 'real YAML validation error in the draft decision');
  await page.chooseDraft('Cancel');
  await checkEditor(invalid);
  await waitConfig(api, alpha);
  await reload(page, fixture);
  await page.click('#draftList button', yamlRestoreButton(alpha));
  await checkEditor(invalid);
  record('Rejected YAML save keeps the editor, persisted YAML, selection, and recoverable draft');

  await page.fill('#config', corrected);
  const retryResponse = cdp.events.length;
  await page.click('#save');
  await cdp.waitResponse(retryResponse, 'POST', '/api/profiles', 200);
  await waitConfig(api, alpha, corrected);
  await page.waitHidden('#config');
  const draftButtons = await page.text('#draftList');
  assert.ok(!draftButtons.includes(yamlRestoreButton(alpha)) && draftButtons.includes(yamlRestoreButton(beta)));
  await switchTo(beta);
  await page.click('#edit');
  await checkEditor(betaDraft);
  await page.click('#cancel');
  await page.chooseDraft('Keep');
  await page.waitHidden('#config');
  await switchTo(alpha, false, corrected);
  record('Successful YAML retry clears only its own draft and preserves the other profile draft');
  return { corrected, betaDraft };
}

async function smokeBackup(page, cdp, dialogs, api, fixture, saved) {
  const [alpha, beta] = fixture.profiles;
  await page.click(maintenanceTab);
  await page.waitVisible('#maintenanceView');
  const checkpoint = cdp.events.length;
  await page.click('#exportBackup');
  await cdp.waitResponse(checkpoint, 'GET', '/api/backup', 200);
  const download = await cdp.waitEvent(checkpoint, 'Browser.downloadWillBegin');
  assert.match(download.suggestedFilename, /\.json$/i);
  assert.match(download.guid, /^[a-f0-9-]+$/i);
  await cdp.waitEvent(checkpoint, 'Browser.downloadProgress', event => event.guid === download.guid && event.state === 'completed');
  // CDP uses the GUID as the on-disk name. Give the import chooser a JSON file.
  const backupFile = path.resolve('downloaded-backup.json');
  fs.copyFileSync(path.resolve('downloads', download.guid), backupFile, fs.constants.COPYFILE_EXCL);
  const backup = fs.readFileSync(backupFile, 'utf8');
  const document = JSON.parse(backup);
  assert.equal(document.activeId, alpha.id);
  assert.equal(document.profiles.find(p => p.id === alpha.id).yaml, saved.corrected);
  assert.equal(document.profiles.find(p => p.id === beta.id).url, beta.subscription);
  assert.ok(!backup.includes(saved.betaDraft.trimEnd().split('\n').at(-1)), 'local draft excluded from backup');
  record('Browser downloads the complete saved backup without local drafts');

  const importFile = async (filename, expectedStatus) => {
    const checkpoint = cdp.events.length;
    const before = dialogs.length;
    await page.click('#importBackup');
    const chooser = await cdp.waitEvent(checkpoint, 'Page.fileChooserOpened');
    await page.send('DOM.setFileInputFiles', { files: [filename], backendNodeId: chooser.backendNodeId });
    await cdp.waitResponse(checkpoint, 'POST', '/api/backup', expectedStatus);
    assert.equal(dialogs.slice(before).filter(kind => kind === 'confirm').length, 1, 'backup replacement confirmation');
  };

  await page.click(connectionTab);
  await page.waitVisible('#connectionView');
  const switchResponse = cdp.events.length;
  await page.click('.profile-buttons button', '切换', beta.name);
  await cdp.waitResponse(switchResponse, 'POST', '/api/profiles', 200);
  await waitConfig(api, beta);
  await page.click(rulesTab);
  await eventually(async () => (await page.text('#rulesList')).includes(fixture.rule.value), 'saved rules to render');
  await page.click('#addRule');
  const changedRule = 'after-backup.browser-fixture.invalid';
  await page.fill('#ruleValue', changedRule);
  await page.click('#confirmRule');
  const ruleResponse = cdp.events.length;
  await page.click('#saveRules');
  await cdp.waitResponse(ruleResponse, 'PUT', '/api/rules', 200);
  await eventually(async () => (await api('/api/rules')).rules.some(rule => rule.value === changedRule), 'post-backup saved rule');
  await page.click(connectionTab);
  await page.waitVisible('#connectionView');
  await page.click(maintenanceTab);
  await page.waitVisible('#maintenanceView');

  const invalidFile = path.resolve('invalid-backup.json');
  fs.writeFileSync(invalidFile, JSON.stringify({ schemaVersion: 999999 }));
  await importFile(invalidFile, 400);
  await waitConfig(api, beta);
  assert.ok((await api('/api/rules')).rules.some(rule => rule.value === changedRule));
  record('Invalid browser backup import preserves current configuration and rules');

  await importFile(backupFile, 200);
  await waitConfig(api, alpha, saved.corrected);
  assert.deepEqual((await api('/api/rules')).rules, [fixture.rule]);
  const restoredBeta = (await api('/api/profiles')).profiles.find(p => p.id === beta.id);
  assert.equal(restoredBeta.name, beta.name);
  assert.equal(restoredBeta.source, 'subscription');
  assert.equal(restoredBeta.url, beta.subscription);
  assert.equal(restoredBeta.autoUpdateHours, beta.autoHours);
  record('Browser backup restore replaces saved YAML, active selection, rules, and subscription metadata');

  assert.equal((await api('/api/profiles')).profiles.find(p => p.id === alpha.id).hasBackup, true);
  await page.click(connectionTab);
  await page.waitVisible('#connectionView');
  await eventually(() => page.eval(() => document.querySelectorAll('.profile-card').length > 0 && !profileBusy), 'restored profile controls');
  const actions = await page.eval(() => [...document.querySelectorAll('.profile-buttons button')].map(button => button.textContent.trim()));
  assert.ok(!actions.includes('恢复上一版') && !actions.includes('查看差异'), 'profile history actions stay hidden');
  for (const yaml of [alpha.yaml, saved.corrected]) {
    await page.eval(id => profileAction('rollback', id), alpha.id);
    await waitConfig(api, alpha, yaml);
  }
  record('Restored backup preserves previous YAML; history actions stay hidden and the rollback API still rotates it');
}

async function main() {
  const fixture = await readFixture();
  const api = apiClient(fixture);
  assert.deepEqual(await api('/__draft_browser_fixture'), { token: fixture.token, version: fixture.version });
  const profile = path.resolve('edge-profile');
  fs.mkdirSync(profile);
  fs.mkdirSync('downloads');
  const browser = spawn(fixture.edge, [
    '--headless=new', '--disable-gpu', '--disable-background-networking', '--disable-component-update',
    '--no-first-run', '--no-default-browser-check', '--disable-extensions', '--disable-sync',
    '--no-proxy-server', '--host-resolver-rules=MAP * ~NOTFOUND, EXCLUDE 127.0.0.1',
    '--remote-debugging-port=0', '--remote-debugging-address=127.0.0.1',
    '--user-data-dir=' + profile, 'about:blank',
  ], { stdio: 'ignore', windowsHide: true });
  let browserError;
  let exited = false;
  browser.once('error', error => { browserError = error; });
  browser.once('exit', () => { exited = true; });
  let cdp;
  let page;
  try {
    cdp = await openCDP(profile);
    const attached = await attachPage(cdp, fixture);
    page = attached.page;
    await cdp.send('Browser.setDownloadBehavior', { behavior: 'allowAndName', downloadPath: path.resolve('downloads'), eventsEnabled: true });
    await page.send('Page.setInterceptFileChooserDialog', { enabled: true });
    await ready(page, fixture);
    results.version = fixture.version;
    record('Installed Edge renders the actual embedded console served by Application.Routes');
    await smokeRules(page, cdp, api, fixture);
    const saved = await smokeYAML(page, cdp, api, fixture);
    await smokeBackup(page, cdp, attached.dialogs, api, fixture, saved);
    assert.deepEqual(results.failures, [], 'unexpected browser exceptions, dialogs, or traffic');
  } catch (error) {
    results.failures.push((browserError || error).message);
    if (page) {
      try {
        const screenshot = await page.send('Page.captureScreenshot', { format: 'png' });
        fs.writeFileSync('failure.png', Buffer.from(screenshot.data, 'base64'));
      } catch { /* A stopped browser cannot return a screenshot. */ }
    }
    throw error;
  } finally {
    if (cdp) {
      cdp.closing = true;
      try { await cdp.send('Browser.close'); } catch { /* Browser can disconnect during shutdown. */ }
      cdp.socket.close();
    }
    // The parent Windows job also terminates any descendants on every exit path.
    try {
      await eventually(() => exited || browserError, 'owned Edge shutdown', 4000);
    } catch (error) {
      browser.kill();
      results.failures.push(error.message);
      process.exitCode = 1;
    }
    fs.writeFileSync('results.json', JSON.stringify(results, null, 2) + '\n');
  }
}

main().catch(error => {
  console.error(error);
  process.exitCode = 1;
});
