'use strict';
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const script = fs.readFileSync(path.join(__dirname, '../cmd/mihomolite/index.go'), 'utf8').split('<script>')[1].split('</script>')[0];
const startup = script.indexOf('loadSettings();setInterval(loadSettings,');
new vm.Script(script);
class Element {
  constructor(tag = 'div') { this.tag = tag; this.children = []; this.style = {}; this.dataset = {}; this.value = ''; this.checked = false; this.disabled = false; this.hidden = false; this.open = false; this.listeners = new Map(); this.classes = new Set(); this.classList = { add: c => this.classes.add(c), remove: c => this.classes.delete(c), contains: c => this.classes.has(c), toggle: (c, on) => { if (on) this.classes.add(c); else this.classes.delete(c); } }; }
  set textContent(value) { this.text = String(value); this.children = []; }
  get textContent() { return this.text || ''; }
  append(...items) { this.children.push(...items); }
  appendChild(item) { this.append(item); return item; }
  get firstChild() { return this.children[0]; }
  get selectedIndex() { return this.children.findIndex(option => option.value === this.value); }
  setAttribute(name, value) { this[name] = value; }
  focus() {} click() { return this.onclick?.(); }
  showModal() { this.open = true; }
  close() { this.open = false; this.listeners.get('close')?.({}); }
  addEventListener(name, fn) { this.listeners.set(name, fn); }
  querySelectorAll() { return this.children.flatMap(child => ['button','select'].includes(child.tag) ? [child] : child.querySelectorAll()); }
}
function fixture(options = {}) {
  const elements = new Map(), listeners = new Map(), eventListeners = new Map(), storage = options.storage || new Map(), requests = [], downloads = [];
  const get = id => { if (!elements.has(id)) elements.set(id, new Element()); return elements.get(id); };
  const tabs = ['connectionView', 'rulesView', 'overridesView', 'settingsView', 'maintenanceView'].map(view => { const e = new Element('button'); e.dataset.view = view; return e; });
  const views = tabs.map(tab => { const view = get(tab.dataset.view); view.id = tab.dataset.view; return view; });
  get('ruleForm').style.display = 'none';
  const profiles = options.profiles || [{ id: 'a'.repeat(64), name: 'Synthetic A', source: 'local', active: true, size: 100, content: 'mode: rule\nproxies: []\nrules: [MATCH,DIRECT]\n' }, { id: 'b'.repeat(64), name: 'Synthetic B', source: 'local', active: false, size: 100, content: 'synthetic B' }];
  let activeId = profiles.find(p => p.active)?.id || '', serverRules = JSON.parse(JSON.stringify(options.rules || []));
  const server = { failYAML: false, failRules: false, failStorage: false, failBackup: false, state: { busy: false, running: false, coreReady: true, panelReady: true, profileCount: profiles.length }, rules: () => serverRules, setRules: rules => { serverRules = rules; } };
  const context = vm.createContext({
    document: { getElementById: get, querySelectorAll: selector => selector === '.tab' ? tabs : selector === '.view' ? views : [], querySelector: () => null, createElement: tag => { const element = new Element(tag); if (tag === 'a') element.click = () => downloads.push(element); return element; }, createDocumentFragment: () => new Element('fragment') },
    localStorage: { get length() { return storage.size; }, key: index => [...storage.keys()][index] ?? null, getItem: key => storage.get(key) ?? null, setItem: (key, value) => { if (server.failStorage) throw new Error('quota'); storage.set(key, value); }, removeItem: key => { if (server.failStorage) throw new Error('storage disabled'); storage.delete(key); } },
    addEventListener: (name, callback) => eventListeners.set(name, callback),
    EventSource: class { addEventListener(name, callback) { listeners.set(name, callback); } },
    setTimeout() {}, setInterval() {}, crypto: { randomUUID: () => 'synthetic-' + Math.random() }, confirm: () => true, prompt: () => null,
    URL: { createObjectURL: () => 'blob:synthetic', revokeObjectURL() {} }, Blob: class { constructor(parts) { this.parts = parts; } },
    fetch: async (route, request = {}) => {
      requests.push({ route, ...request });
      let value;
      if (options.request) value = await options.request(route, request);
      if (value !== undefined) return value;
      const method = request.method || 'GET', body = request.body ? JSON.parse(request.body) : {};
      const fail = error => ({ ok: false, json: async () => ({ error }) });
      if (route === '/api/config') value = { profileId: activeId, content: profiles.find(profile => profile.id === activeId)?.content || '' };
      else if (route === '/api/state') value = server.state;
      else if (route === '/api/profiles') {
        if (method === 'POST') {
          if (body.action === 'edit') { if (server.failYAML) return fail('synthetic invalid YAML'); if (body.id !== activeId) return fail('wrong profile'); profiles.find(p => p.id === activeId).content = body.content; }
          if (body.action === 'switch') { activeId = body.id; profiles.forEach(p => { p.active = p.id === activeId; }); }
        }
        value = { profiles, activeId };
      } else if (route === '/api/rules') {
        if (method === 'PUT') { if (server.failRules) return fail('synthetic rejected rules'); serverRules = body.rules; value = { applied: false }; }
        else value = { rules: JSON.parse(JSON.stringify(serverRules)), policies: ['统一代理','DIRECT','REJECT'], unifiedPolicy: '统一代理', running: false };
      } else if (route === '/api/import') { if (server.failYAML) return fail('synthetic invalid YAML'); value = {}; }
      else if (route === '/api/backup') { if (server.failBackup) return fail('synthetic invalid backup'); value = { profiles }; }
      else throw new Error('Unexpected request: ' + route);
      return { ok: true, json: async () => JSON.parse(JSON.stringify(value)) };
    },
  });
  vm.runInContext(script.slice(0, startup), context);
  context.initializeDrafts();
  vm.runInContext('state=' + JSON.stringify(server.state), context);
  return { context, get, tabs, profiles, server, requests, storage, eventListeners, downloads, run: code => vm.runInContext(code, context), emit: patch => { Object.assign(server.state, patch); listeners.get('state')({ data: JSON.stringify(server.state) }); } };
}
module.exports = { fixture, Element };
