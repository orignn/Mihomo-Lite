'use strict';
const assert=require('node:assert/strict');
const fs=require('node:fs');
const path=require('node:path');
const {spawn}=require('node:child_process');
const {setTimeout:delay}=require('node:timers/promises');
const gate='MIHOMOLITE_TEST_OPT009010_BROWSER';
const fixtureHeader='X-Mihomo-Draft-Browser-Fixture';
const result={checks:[],failures:[],images:[]};
const record=name=>result.checks.push(name);
async function eventually(check,description,timeout=10000){const deadline=Date.now()+timeout;while(Date.now()<deadline){const value=await check();if(value)return value;await delay(40)}throw new Error('Timed out: '+description)}
class CDP{
 constructor(socket){this.socket=socket;this.next=0;this.pending=new Map();this.handlers=new Map();socket.addEventListener('message',event=>{const message=JSON.parse(String(event.data));if(message.id){const item=this.pending.get(message.id);if(!item)return;this.pending.delete(message.id);clearTimeout(item.timer);message.error?item.reject(new Error(item.method+' rejected')):item.resolve(message.result);return}const handler=this.handlers.get(message.method);if(handler)Promise.resolve(handler(message)).catch(error=>{if(!this.closing)result.failures.push(error.message)})});socket.addEventListener('close',()=>{for(const item of this.pending.values()){clearTimeout(item.timer);item.reject(new Error('Owned browser disconnected'))}this.pending.clear()})}
 send(method,params={},sessionId){const id=++this.next;return new Promise((resolve,reject)=>{const timer=setTimeout(()=>{this.pending.delete(id);reject(new Error('CDP timed out: '+method))},8000);this.pending.set(id,{resolve,reject,timer,method});this.socket.send(JSON.stringify({id,method,params,sessionId}))})}
}
class Page{
 constructor(cdp,session){this.cdp=cdp;this.session=session}
 send(method,params){return this.cdp.send(method,params,this.session)}
 async eval(fn,...args){const response=await this.send('Runtime.evaluate',{expression:'('+fn.toString()+')(...'+JSON.stringify(args)+')',returnByValue:true,awaitPromise:true});if(response.exceptionDetails)throw new Error(response.result.description||response.exceptionDetails.text);return response.result.value}
 text(selector){return this.eval(selector=>document.querySelector(selector)?.textContent||'',selector)}
 value(selector){return this.eval(selector=>document.querySelector(selector)?.value??null,selector)}
 async click(selector,text=null){const point=await eventually(()=>this.eval((selector,text)=>{const element=[...document.querySelectorAll(selector)].find(el=>(text===null||el.textContent.trim()===text)&&!el.disabled&&el.getClientRects().length);if(!element)return null;element.scrollIntoView({block:'center',inline:'nearest',behavior:'instant'});const rect=element.getBoundingClientRect(),x=(Math.max(0,rect.left)+Math.min(innerWidth,rect.right))/2,y=(Math.max(0,rect.top)+Math.min(innerHeight,rect.bottom))/2;return element.contains(document.elementFromPoint(x,y))?{x,y}:null},selector,text),'clickable '+selector);await this.send('Input.dispatchMouseEvent',{type:'mousePressed',...point,button:'left',clickCount:1});await this.send('Input.dispatchMouseEvent',{type:'mouseReleased',...point,button:'left',clickCount:1})}
 async fill(selector,value){await this.click(selector);const key={key:'a',code:'KeyA',windowsVirtualKeyCode:65,modifiers:2};await this.send('Input.dispatchKeyEvent',{type:'rawKeyDown',...key});await this.send('Input.dispatchKeyEvent',{type:'keyUp',...key});await this.send('Input.insertText',{text:value});assert.equal(await this.value(selector),value)}
 async screenshot(selector,name){const clip=await this.eval(selector=>{const el=document.querySelector(selector);el.scrollIntoView({block:'center',behavior:'instant'});const r=el.getBoundingClientRect();return{x:r.left+scrollX,y:r.top+scrollY,width:r.width,height:r.height,scale:1}},selector);const image=await this.send('Page.captureScreenshot',{format:'png',clip,captureBeyondViewport:true});const file=path.join(process.cwd(),name);fs.writeFileSync(file,Buffer.from(image.data,'base64'),{flag:'wx'});result.images.push(file)}
}
async function readFixture(){assert.equal(process.env[gate],'1','explicit isolated browser gate is required');let input='';for await(const chunk of process.stdin){input+=chunk;if(input.length>1<<20)throw new Error('Oversized fixture manifest')}const fixture=JSON.parse(input);const origin=new URL(fixture.url);assert.equal(origin.hostname,'127.0.0.1');assert.equal(origin.protocol,'http:');assert.ok(origin.port&&origin.port!=='17653');assert.match(fixture.token,/^[a-f0-9]{64}$/);assert.ok(path.isAbsolute(fixture.edge));assert.match(path.basename(fixture.edge),/^msedge[.]exe$/i);return fixture}
async function openCDP(profile){const address=await eventually(()=>{try{const [port,socket]=fs.readFileSync(path.join(profile,'DevToolsActivePort'),'utf8').trim().split(/\r?\n/);return /^\d{1,5}$/.test(port)&&Number(port)>0&&Number(port)<65536&&/^\/devtools\/browser\/[a-f0-9-]+$/i.test(socket)?'ws://127.0.0.1:'+port+socket:null}catch(error){if(error.code!=='ENOENT')throw error;return null}},'owned Edge debugging endpoint');const socket=new WebSocket(address);await new Promise((resolve,reject)=>{const timer=setTimeout(()=>reject(new Error('Owned browser socket timeout')),8000);socket.addEventListener('open',()=>{clearTimeout(timer);resolve()},{once:true});socket.addEventListener('error',()=>{clearTimeout(timer);reject(new Error('Owned browser socket failed'))},{once:true})});return new CDP(socket)}
async function attach(cdp,fixture){const {targetId}=await cdp.send('Target.createTarget',{url:'about:blank'});const {sessionId}=await cdp.send('Target.attachToTarget',{targetId,flatten:true});const page=new Page(cdp,sessionId);cdp.handlers.set('Fetch.requestPaused',async({params})=>{const url=new URL(params.request.url);if(url.origin===fixture.url&&['http:','blob:'].includes(url.protocol))await page.send('Fetch.continueRequest',{requestId:params.requestId});else{await page.send('Fetch.failRequest',{requestId:params.requestId,errorReason:'BlockedByClient'});result.failures.push('Browser attempted a non-fixture origin')}});cdp.handlers.set('Runtime.exceptionThrown',()=>result.failures.push('Unexpected page script exception'));cdp.handlers.set('Page.javascriptDialogOpening',async({params})=>{const accept=params.type==='beforeunload';if(!accept)result.failures.push('Unexpected page dialog');await page.send('Page.handleJavaScriptDialog',{accept})});for(const method of ['Runtime.enable','Page.enable','Network.enable','Fetch.enable'])await page.send(method);await page.send('Network.setExtraHTTPHeaders',{headers:{[fixtureHeader]:fixture.token}});await page.send('Emulation.setDeviceMetricsOverride',{width:1200,height:1050,deviceScaleFactor:1,mobile:false});await page.send('Page.navigate',{url:fixture.url+'/'});await eventually(async()=>await page.text('#version')==='v'+fixture.version,'embedded version');await eventually(async()=>(await page.text('#profileList')).includes(fixture.profiles[0].name),'loaded synthetic profiles');return page}
async function api(fixture,route){const response=await fetch(new URL(route,fixture.url),{headers:{[fixtureHeader]:fixture.token},redirect:'error',signal:AbortSignal.timeout(5000)});assert.ok(response.ok,'fixture API rejected '+route);return response.json()}
async function choose(page,selector,value){await page.eval((selector,value)=>{const e=document.querySelector(selector);e.value=value;e.dispatchEvent(new Event('change',{bubbles:true}))},selector,value)}
async function showPortSettings(page){
 await page.click('button[data-view="settingsView"]');
 if(!await page.eval(()=>document.querySelector('#portSettingsDetails').open))await page.click('#portSettingsDetails > summary');
 await eventually(()=>page.eval(()=>document.querySelector('#settingsView').classList.contains('active')&&document.querySelector('#portSettingsDetails').open),'visible port settings');
}
async function smoke(page,fixture){
 await showPortSettings(page);
 const initial=await api(fixture,'/api/settings');
 await eventually(async()=>await page.value('#proxyPortMode')==='auto','loaded automatic proxy setting');
 assert.equal(await page.value('#webPortMode'),'fixed');
 assert.ok(await page.eval(()=>document.querySelector('#proxyPort').disabled));
 record('Legacy Web listen and automatic proxy defaults are displayed separately');
 const fixed={proxy:23181,backend:23182,panel:23183,web:23184};
 for(const [kind,port] of Object.entries(fixed)){await choose(page,'#'+kind+'PortMode','fixed');await page.fill('#'+kind+'Port',String(port))}
 await page.click('#savePortSettings');
 await eventually(async()=>(await api(fixture,'/api/settings')).ports?.proxy?.port===fixed.proxy,'saved fixed ports');
 const saved=await api(fixture,'/api/settings');
 for(const [kind,port] of Object.entries(fixed))assert.deepEqual(saved.ports[kind],{mode:'fixed',port});
 assert.equal(saved.portsRestartRequired,true);assert.equal(saved.portsApplyAfter,'program-restart');
 assert.deepEqual(saved.activePorts,initial.activePorts);
 assert.match(await page.text('#portSettingsStatus'),/重启/);
 assert.ok(!JSON.stringify(saved).includes(fixture.token));
 record('Four fixed ports save atomically, without changing the current program endpoints');
 await page.screenshot('section:has(#savePortSettings)','port-save-desktop.png');
 await page.fill('#panelPort',String(fixed.proxy));await page.click('#savePortSettings');
 await eventually(async()=>(await page.text('#portSettingsError')).length>0,'duplicate port error');
 assert.deepEqual((await api(fixture,'/api/settings')).ports,saved.ports);
 assert.equal(await page.value('#panelPort'),String(fixed.proxy));
 record('Cross-endpoint port conflict is explicit and preserves saved settings and failed input');
 await page.send('Page.reload',{ignoreCache:true});
 await eventually(async()=>await page.value('#panelPort')===String(fixed.panel),'saved settings after reload');
 await showPortSettings(page);
 assert.equal((await api(fixture,'/api/settings')).portsRestartRequired,true);
 record('Reload uses persisted fixed ports and retains program-restart warning');
 for(const kind of Object.keys(fixed))await choose(page,'#'+kind+'PortMode','auto');
 await page.click('#savePortSettings');
 await eventually(async()=>(await api(fixture,'/api/settings')).ports?.proxy?.mode==='auto','automatic ports saved');
 const automatic=await api(fixture,'/api/settings');
 for(const kind of Object.keys(fixed))assert.deepEqual(automatic.ports[kind],{mode:'auto',port:0});
 record('All endpoints can explicitly return to automatic mode');
 for(const [kind,port] of Object.entries(fixed)){await choose(page,'#'+kind+'PortMode','fixed');await page.fill('#'+kind+'Port',String(port))}
 await page.click('#savePortSettings');await eventually(async()=>(await api(fixture,'/api/settings')).ports?.proxy?.port===fixed.proxy,'fixed settings restored');
 const before=await api(fixture,'/api/profiles');
 const id='11111111-1111-4111-8111-111111111111';
 const vmess='vmess://'+Buffer.from(JSON.stringify({v:'2',ps:'Fixture VMess',add:'192.0.2.2',port:'443',id,aid:'0',scy:'auto',net:'ws',type:'none',host:'fixture.invalid',path:'/ws',tls:'tls',sni:'fixture.invalid'})).toString('base64');
 const links=[
 'ss://'+Buffer.from('aes-128-gcm:fixture-pass').toString('base64url')+'@192.0.2.1:443#Fixture%20SS',
 vmess,
 'vless://'+id+'@192.0.2.3:443?security=tls&type=tcp&sni=fixture.invalid#Fixture%20VLESS',
 'trojan://fixture-pass@192.0.2.4:443?sni=fixture.invalid#Fixture%20Trojan',
 'hy2://fixture-pass@192.0.2.5:443?sni=fixture.invalid#Fixture%20Hy2',
 'tuic://'+id+':fixture-pass@192.0.2.6:443?sni=fixture.invalid&congestion_control=bbr#Fixture%20TUIC'
 ].join('\n');
 await page.click('button[data-view="connectionView"]');
 await page.click('#paste');await page.fill('#config',links);await page.click('#save');
 await eventually(async()=>(await api(fixture,'/api/profiles')).profiles.length===before.profiles.length+1,'mixed protocol import');
 const imported=await api(fixture,'/api/config');
 for(const type of ['ss','vmess','vless','trojan','hysteria2','tuic'])assert.ok(imported.content.includes('type: '+type));
 assert.match(imported.content,/fixture[.]invalid/);assert.match(imported.content,/MATCH/);
 record('Real paste/save imports six protocols through conversion and template merge');
 const stableProfiles=await api(fixture,'/api/profiles');
 await page.click('#edit');
 const broken=links+'\ntrojan://'+fixture.token+'@192.0.2.7:443?type=unsupported#'+fixture.token;
 await page.fill('#config',broken);await page.click('#save');
 await eventually(async()=>(await page.text('#error')).length>0,'mixed-batch parse rejection');
 assert.ok(!(await page.text('#error')).includes(fixture.token));
 assert.equal(await page.value('#config'),broken);
 assert.equal((await api(fixture,'/api/config')).content,imported.content);
 assert.equal((await api(fixture,'/api/profiles')).profiles.length,stableProfiles.profiles.length);
 record('Bad mixed node rejects the whole edit, retaining old YAML and allowing input retry');
 const logs=JSON.stringify(await api(fixture,'/api/logs'));
 assert.ok(!logs.includes(fixture.token));record('Conversion errors and launcher logs do not disclose link credentials or names');
 await page.fill('#config',imported.content);await page.click('#save');
 await eventually(async()=>await page.eval(()=>!document.querySelector('#config').classList.contains('visible')),'editor closed after retry');
 record('A valid retry succeeds after the rejected batch');
 await page.send('Emulation.setDeviceMetricsOverride',{width:412,height:915,deviceScaleFactor:1,mobile:false});
 await showPortSettings(page);
 assert.equal(await page.eval(()=>document.documentElement.scrollWidth<=innerWidth),true);
 const box=await page.eval(()=>{const e=document.querySelector('#savePortSettings').closest('section');e.scrollIntoView();const r=e.getBoundingClientRect();return {left:r.left,right:r.right,width:innerWidth}});
 assert.ok(box.left>=0&&box.right<=box.width);record('Port form remains usable at 412 pixels without horizontal overflow');
 await eventually(async()=>await page.eval(()=>!document.querySelector('#toast').classList.contains('show')),'toast cleared before final layout capture');
 await page.screenshot('section:has(#savePortSettings)','port-save-mobile.png');
}
async function main(){const fixture=await readFixture();const profile=path.join(process.cwd(),'edge-profile');fs.mkdirSync(profile);const browser=spawn(fixture.edge,['--headless=new','--no-first-run','--no-default-browser-check','--disable-background-networking','--disable-component-update','--disable-sync','--disable-extensions','--disable-breakpad','--disable-features=msEdgeSidebarV2,msEdgeShoppingAssistant,msEdgeWallet','--no-proxy-server','--host-resolver-rules=MAP * ~NOTFOUND, EXCLUDE 127.0.0.1, EXCLUDE localhost','--remote-debugging-address=127.0.0.1','--remote-debugging-port=0','--user-data-dir='+profile,'about:blank'],{stdio:'ignore',windowsHide:true});let cdp;try{cdp=await openCDP(profile);const page=await attach(cdp,fixture);await smoke(page,fixture);assert.deepEqual(result.failures,[]);console.log('OPT-009/010 Edge: '+result.checks.length+' checks PASS')}catch(error){result.failures.push(error.message);throw error}finally{fs.writeFileSync(path.join(process.cwd(),'results.json'),JSON.stringify(result,null,2),{flag:'wx'});if(cdp){cdp.closing=true;try{await cdp.send('Browser.close')}catch{}cdp.socket.close()}if(browser.exitCode===null)browser.kill()}}
main().catch(error=>{console.error(error.message);process.exitCode=1});