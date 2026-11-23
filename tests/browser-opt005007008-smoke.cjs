'use strict';
const assert=require('node:assert/strict');
const fs=require('node:fs');
const path=require('node:path');
const {spawn}=require('node:child_process');
const {setTimeout:delay}=require('node:timers/promises');
const gate='MIHOMOLITE_TEST_OPT005007008_BROWSER';
const fixtureHeader='X-Mihomo-Draft-Browser-Fixture';
const result={checks:[],failures:[],images:[],dialogs:[],requests:[],scope:'Real isolated Edge OPT020 simple overrides/download settings with synthetic local APIs; shape-only validator; SIMULATED component network responses; policy-only saves, no subscription download, real update/install/proxy'};
const dialogChoices=[];
const record=name=>result.checks.push(name);
async function eventually(check,description,timeout=10000){const deadline=Date.now()+timeout;while(Date.now()<deadline){const value=await check();if(value)return value;await delay(40)}throw new Error('Timed out: '+description)}
class CDP{
 constructor(socket){this.socket=socket;this.next=0;this.pending=new Map();this.handlers=new Map();socket.addEventListener('message',event=>{const message=JSON.parse(String(event.data));if(message.id){const item=this.pending.get(message.id);if(!item)return;this.pending.delete(message.id);clearTimeout(item.timer);message.error?item.reject(Object.assign(new Error(item.method+' rejected: '+message.error.message),{cdpCode:message.error.code})):item.resolve(message.result);return}const handler=this.handlers.get(message.method);if(handler)Promise.resolve(handler(message)).catch(error=>{if(!this.closing)result.failures.push(error.message)})});socket.addEventListener('close',()=>{for(const item of this.pending.values()){clearTimeout(item.timer);item.reject(new Error('Owned browser disconnected'))}this.pending.clear()})}
 send(method,params={},sessionId){const id=++this.next;return new Promise((resolve,reject)=>{const timer=setTimeout(()=>{this.pending.delete(id);reject(new Error('CDP timed out: '+method))},8000);this.pending.set(id,{resolve,reject,timer,method});this.socket.send(JSON.stringify({id,method,params,sessionId}))})}
}
class Page{
 constructor(cdp,session){this.cdp=cdp;this.session=session}
 send(method,params){return this.cdp.send(method,params,this.session)}
 async eval(fn,...args){const response=await this.send('Runtime.evaluate',{expression:'('+fn.toString()+')(...'+JSON.stringify(args)+')',returnByValue:true,awaitPromise:true});if(response.exceptionDetails)throw new Error(response.result.description||response.exceptionDetails.text);return response.result.value}
 text(selector){return this.eval(selector=>document.querySelector(selector)?.textContent||'',selector)}
 value(selector){return this.eval(selector=>document.querySelector(selector)?.value??null,selector)}
 async click(selector,text=null){const point=await eventually(()=>this.eval(async(selector,text)=>{const element=[...document.querySelectorAll(selector)].find(el=>(text===null||el.textContent.trim()===text)&&!el.disabled&&el.getClientRects().length);if(!element)return null;element.scrollIntoView({block:'center',inline:'nearest',behavior:'instant'});await new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve)));const rect=element.getBoundingClientRect(),x=(Math.max(0,rect.left)+Math.min(innerWidth,rect.right))/2,y=(Math.max(0,rect.top)+Math.min(innerHeight,rect.bottom))/2;return element.contains(document.elementFromPoint(x,y))?{x,y}:null},selector,text),'clickable '+selector);await this.send('Input.dispatchMouseEvent',{type:'mousePressed',...point,button:'left',clickCount:1});await this.send('Input.dispatchMouseEvent',{type:'mouseReleased',...point,button:'left',clickCount:1})}
 async fill(selector,value){await this.click(selector);const key={key:'a',code:'KeyA',windowsVirtualKeyCode:65,modifiers:2};await this.send('Input.dispatchKeyEvent',{type:'rawKeyDown',...key});await this.send('Input.dispatchKeyEvent',{type:'keyUp',...key});await this.send('Input.insertText',{text:value});assert.equal(await this.value(selector),value)}
 async screenshot(selector,name,viewport=false){const clip=await this.eval(async(selector,viewport)=>{const el=document.querySelector(selector);el.scrollIntoView({block:viewport?'start':'center',behavior:'instant'});if(viewport)scrollBy(0,-((document.querySelector('.tabs')?.getBoundingClientRect().height||0)+12));await new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve)));const r=el.getBoundingClientRect();return{x:r.left+scrollX,y:r.top+scrollY,width:r.width,height:r.height,scale:1}},selector,viewport);const image=await this.send('Page.captureScreenshot',{format:'png',...(viewport?{captureBeyondViewport:false}:{clip,captureBeyondViewport:true})});const file=path.join(process.cwd(),name);fs.writeFileSync(file,Buffer.from(image.data,'base64'),{flag:'wx'});result.images.push(file)}
}
async function readFixture(){assert.equal(process.env[gate],'1','explicit isolated browser gate is required');let input='';for await(const chunk of process.stdin){input+=chunk;if(input.length>1<<20)throw new Error('Oversized fixture manifest')}const fixture=JSON.parse(input);const origin=new URL(fixture.url);assert.equal(origin.hostname,'127.0.0.1');assert.equal(origin.protocol,'http:');assert.ok(origin.port&&origin.port!=='17653');assert.match(fixture.token,/^[a-f0-9]{64}$/);assert.ok(path.isAbsolute(fixture.edge));assert.match(path.basename(fixture.edge),/^msedge[.]exe$/i);return fixture}
async function openCDP(profile){const address=await eventually(()=>{try{const [port,socket]=fs.readFileSync(path.join(profile,'DevToolsActivePort'),'utf8').trim().split(/\r?\n/);return /^\d{1,5}$/.test(port)&&Number(port)>0&&Number(port)<65536&&/^\/devtools\/browser\/[a-f0-9-]+$/i.test(socket)?'ws://127.0.0.1:'+port+socket:null}catch(error){if(error.code!=='ENOENT')throw error;return null}},'owned Edge debugging endpoint');const socket=new WebSocket(address);await new Promise((resolve,reject)=>{const timer=setTimeout(()=>reject(new Error('Owned browser socket timeout')),8000);socket.addEventListener('open',()=>{clearTimeout(timer);resolve()},{once:true});socket.addEventListener('error',()=>{clearTimeout(timer);reject(new Error('Owned browser socket failed'))},{once:true})});return new CDP(socket)}

// Every request is scoped to the nonce-authenticated ephemeral fixture. Never
// navigate to a real subscription, component service, daily launcher or panel.
async function attach(cdp,fixture){
 const {targetId}=await cdp.send('Target.createTarget',{url:'about:blank'});
 const {sessionId}=await cdp.send('Target.attachToTarget',{targetId,flatten:true});
 const page=new Page(cdp,sessionId);
 const abortedNetworkIds=new Set();result.cancelledInterceptions=[];
 cdp.handlers.set('Network.loadingFailed',({params})=>{if(params.canceled||params.errorText==='net::ERR_ABORTED')abortedNetworkIds.add(params.requestId)});
 cdp.handlers.set('Fetch.requestPaused',async({params})=>{
  const url=new URL(params.request.url);
  if(url.origin===fixture.url&&url.protocol==='http:'){
   const entry={method:params.request.method,path:url.pathname};if(url.pathname==='/api/profiles'&&params.request.method==='POST'){try{entry.action=JSON.parse(params.request.postData||'{}').action}catch{entry.action='invalid'}}result.requests.push(entry);
   try{await page.send('Fetch.continueRequest',{requestId:params.requestId})}catch(error){if(error.cdpCode===-32602&&/Invalid (?:InterceptionId|interceptionId|requestId)/i.test(error.message)&&(page.reloading||abortedNetworkIds.has(params.networkId))){result.cancelledInterceptions.push({path:url.pathname,reason:'owned navigation cancelled interception'})}else throw error}
  }else{await page.send('Fetch.failRequest',{requestId:params.requestId,errorReason:'BlockedByClient'});result.failures.push('Browser attempted a non-fixture origin')}
 });
 cdp.handlers.set('Runtime.exceptionThrown',()=>result.failures.push('Unexpected page script exception'));
 cdp.handlers.set('Page.javascriptDialogOpening',async({params})=>{
  const choice=dialogChoices.shift();
  if(params.type!=='beforeunload'&&choice===undefined)result.failures.push('Unexpected page dialog');
  const accept=choice??params.type==='beforeunload';
  result.dialogs.push({type:params.type,accepted:accept});
  await page.send('Page.handleJavaScriptDialog',{accept});
 });
 for(const method of ['Runtime.enable','Page.enable','Network.enable','Fetch.enable'])await page.send(method);
 await page.send('Network.setExtraHTTPHeaders',{headers:{[fixtureHeader]:fixture.token}});
 await page.send('Emulation.setDeviceMetricsOverride',{width:1200,height:1050,deviceScaleFactor:1,mobile:false});
 await page.send('Page.navigate',{url:fixture.url+'/'});
 await eventually(async()=>await page.text('#version')==='v'+fixture.version,'embedded version');
 await eventually(async()=>(await page.text('#profileList')).includes(fixture.profiles[0].name),'loaded synthetic profiles');
 return page;
}
async function request(fixture,route,body,method){
 const url=new URL(route,fixture.url);assert.equal(url.origin,fixture.url);
 const response=await fetch(url,{method:method||(body===undefined?'GET':'POST'),headers:{[fixtureHeader]:fixture.token,'X-Mihomo-Lite':'1','Content-Type':'application/json'},...(body===undefined?{}:{body:JSON.stringify(body)}),redirect:'error',signal:AbortSignal.timeout(6000)});
 return {status:response.status,body:await response.json()};
}
async function api(fixture,route,body,method){const response=await request(fixture,route,body,method);assert.equal(response.status,200,'fixture API rejected '+route+': '+(response.body.error||response.status));return response.body}
async function choose(page,selector,value){await eventually(()=>enabled(page,selector),'selection ready '+selector);assert.equal(await visible(page,selector),true,'select must be visible '+selector);await page.eval((selector,value)=>{const e=document.querySelector(selector);e.value=value;e.dispatchEvent(new Event('change',{bubbles:true}))},selector,value)}
const visible=(page,selector)=>page.eval(selector=>{const element=document.querySelector(selector);if(!element||!element.checkVisibility({checkVisibilityCSS:true}))return false;for(let parent=element.parentElement;parent;parent=parent.parentElement)if(parent.tagName==='DETAILS'&&!parent.open&&!parent.querySelector(':scope > summary')?.contains(element))return false;return !!element.getClientRects().length},selector);
const detailsOpen=(page,selector)=>page.eval(selector=>document.querySelector(selector).open,selector);
async function setDetails(page,selector,open){if(await detailsOpen(page,selector)!==open)await page.click(selector+' > summary');try{await eventually(async()=>await detailsOpen(page,selector)===open,'real summary toggle '+selector)}catch(error){const context=await page.eval(selector=>{const details=document.querySelector(selector),summary=details.querySelector(':scope > summary'),rect=summary.getBoundingClientRect();return{open:details.open,scrollY,summary:{top:rect.top,bottom:rect.bottom,left:rect.left,right:rect.right},active:document.activeElement?.id,hit:document.elementFromPoint((rect.left+rect.right)/2,(rect.top+rect.bottom)/2)?.id}},selector);throw new Error(error.message+' '+JSON.stringify(context))}}
async function assertNoOverflow(page){assert.equal(await page.eval(()=>document.documentElement.scrollWidth<=innerWidth),true,'horizontal overflow at 412px')}
const checked=(page,selector)=>page.eval(selector=>document.querySelector(selector).checked,selector);
const enabled=(page,selector)=>page.eval(selector=>!document.querySelector(selector).disabled,selector);
const componentButton=(component,action)=>'#componentList button[data-component="'+component+'"][data-action="'+action+'"]';
const componentCard=component=>'#componentList .profile-card:has(button[data-component="'+component+'"])';
async function reload(page){const marker=Date.now()+'-'+Math.random();await page.eval(marker=>globalThis.__ownedReloadMarker=marker,marker);page.reloading=true;try{await page.send('Page.reload',{ignoreCache:true});await eventually(()=>page.eval(marker=>globalThis.__ownedReloadMarker!==marker&&document.readyState==='complete'&&typeof confirmedSettings!=='undefined'&&!!confirmedSettings.connectionPolicy&&!!document.querySelector('#saveConnectionPolicy')&&!document.querySelector('#saveConnectionPolicy').disabled,marker),'new document and confirmed settings after reload')}finally{page.reloading=false}}
async function cleanToast(page){await eventually(async()=>await page.eval(()=>!document.querySelector('#toast').classList.contains('show')),'toast cleared')}
async function assertIdle(fixture){const state=await api(fixture,'/api/state');assert.equal(state.running,false);assert.equal(state.proxyEnabled,false);assert.equal(state.connectionRetry?.active,false)}
async function overridesSmoke(page,fixture){
 const [alpha,beta]=fixture.profiles;
 const savedGlobal='log-level: warning\ndns:\n  nameserver: [192.0.2.53]\n'.replaceAll('\\n','\n');
 const savedAlpha='log-level: error\ndns:\n  nameserver: [192.0.2.54]\n'.replaceAll('\\n','\n');
 const savedBeta='log-level: info\n'.replaceAll('\\n','\n');
 await page.click('.tab[data-view="overridesView"]');
 await eventually(()=>enabled(page,'#saveOverride'),'global override GET; error='+await page.text('#overrideError'));
 assert.equal(await page.value('#overrideYAML'),'');
 assert.equal(await page.value('#overrideScope'),'global');
 assert.equal(await detailsOpen(page,'#overrideAdvanced'),false);assert.equal(await detailsOpen(page,'#overridePreview'),false);
 record('OPT005 global layer is independently read as empty, with a selected preview target');
 await setDetails(page,'#overrideAdvanced',true);
 await page.fill('#overrideYAML',savedGlobal);await page.click('#saveOverride');
 await eventually(async()=>(await api(fixture,'/api/overrides?scope=global')).yaml===savedGlobal,'global layer persistence');
 assert.match(await page.text('#overrideStatus'),/重新连接|下次连接/);
 await assertIdle(fixture);record('OPT005 global save persists locally and requires reconnect, without starting a core');
 await choose(page,'#overrideScope','profile');await eventually(()=>enabled(page,'#saveOverride'),'profile layer read');
 assert.equal(await page.value('#overrideYAML'),'');
 await page.fill('#overrideYAML',savedAlpha);await page.click('#saveOverride');
 await eventually(async()=>(await api(fixture,'/api/overrides?scope=profile&profileId='+alpha.id)).yaml===savedAlpha,'Alpha layer saved');
 await choose(page,'#overrideProfile',beta.id);await eventually(()=>enabled(page,'#saveOverride'),'Beta layer read');
 assert.equal(await page.value('#overrideYAML'),'');
 await page.fill('#overrideYAML',savedBeta);await page.click('#saveOverride');
 await eventually(async()=>(await api(fixture,'/api/overrides?scope=profile&profileId='+beta.id)).yaml===savedBeta,'Beta layer saved');
 await choose(page,'#overrideProfile',alpha.id);await eventually(()=>enabled(page,'#saveOverride'),'Alpha read after switch');
 assert.equal(await page.value('#overrideYAML'),savedAlpha);
 assert.equal((await api(fixture,'/api/overrides?scope=global')).yaml,savedGlobal);
 record('OPT005 global, Alpha and Beta layers remain isolated by stable profile ID');
 const bad='dns: [unterminated';
 await page.fill('#overrideYAML',bad);await page.click('#saveOverride');
 await eventually(async()=>(await page.text('#overrideError')).length>0,'invalid override rejection');
 assert.equal(await page.value('#overrideYAML'),bad);
 assert.equal((await api(fixture,'/api/overrides?scope=profile&profileId='+alpha.id)).yaml,savedAlpha);
 assert.match(await page.text('#overrideStatus'),/失败.*保留/);
 record('OPT005 rejected save preserves failed YAML and the previously valid layer');
 const dialogs=result.dialogs.length;dialogChoices.push(false);
 await choose(page,'#overrideProfile',beta.id);
 await eventually(()=>result.dialogs.length===dialogs+1,'scope discard confirmation cancelled');
 assert.equal(await page.value('#overrideProfile'),alpha.id);assert.equal(await page.value('#overrideYAML'),bad);
 record('OPT005 cancelling a dirty profile switch retains the current ID and exact input');
 dialogChoices.push(true);await choose(page,'#overrideProfile',beta.id);await eventually(()=>enabled(page,'#saveOverride'),'scope discard accepted');
 assert.equal(await page.value('#overrideYAML'),savedBeta);
 await choose(page,'#overrideProfile',alpha.id);await eventually(()=>enabled(page,'#saveOverride'),'restored Alpha');
 await setDetails(page,'#overridePreview',true);await page.click('#previewOverride');await eventually(async()=>!!await page.value('#finalConfigPreview'),'Alpha preview');
 const preview=await page.value('#finalConfigPreview');
 assert.match(preview,/log-level: error/);assert.match(preview,/192[.]0[.]2[.]54/);
 assert.match(preview,/<redacted-api-secret>/);assert.ok(!preview.includes(fixture.token),'API secret escaped redaction');
 assert.ok(!preview.includes('source-secret'),'original API secret escaped redaction');
 assert.match(await page.text('#previewStatus'),/密码|秘密|敏感/);
 assert.match(await page.text('#previewStatus'),/校验/);
 assert.ok(await page.eval(()=>document.querySelector('#finalConfigPreview').readOnly));
 assert.equal((await api(fixture,'/__opt_fixture_metrics')).runtimeFileAbsent,true);
 record('OPT005 read-only preview applies precedence, redacts both API secrets and warns about sensitive YAML');
 await cleanToast(page);await page.screenshot('#overridesView','override-preview-desktop.png');
 const missingResource='proxy-providers:\n  MissingFixture:\n    type: http\n    url: https://provider.browser-fixture.invalid/nodes\n    path: providers/never-present.yaml\n    interval: 3600\n'.replaceAll('\\n','\n');
 await page.fill('#overrideYAML',missingResource);
 assert.equal(await page.value('#finalConfigPreview'),'');
 await page.click('#previewOverride');await eventually(async()=>!!await page.value('#finalConfigPreview'),'missing-resource preview');
 assert.match(await page.text('#previewStatus'),/尚未通过|未进行/);
 assert.match(await page.text('#previewStatus'),/缓存|资源/);
 const failed=await api(fixture,'/api/config/preview',{scope:'profile',profileId:alpha.id,yaml:missingResource});
 assert.equal(failed.validated,false);assert.match(failed.validation,/缓存|资源/);
 assert.equal((await api(fixture,'/api/overrides?scope=profile&profileId='+alpha.id)).yaml,savedAlpha);
 record('OPT005 missing local provider cache yields validated:false, not a fabricated offline success or download');
 // Remove the candidate without saving it, then verify all saved layers survive.
 await page.fill('#overrideYAML',savedAlpha);await reload(page);
 await page.click('.tab[data-view="overridesView"]');await eventually(()=>enabled(page,'#saveOverride'),'global layer after reload');
 assert.equal(await page.value('#overrideYAML'),savedGlobal);
 await choose(page,'#overrideScope','profile');await eventually(()=>enabled(page,'#saveOverride'),'profile after reload');
 assert.equal(await page.value('#overrideYAML'),savedAlpha);
 await choose(page,'#overrideProfile',beta.id);await eventually(()=>enabled(page,'#saveOverride'),'Beta after reload');
 assert.equal(await page.value('#overrideYAML'),savedBeta);
 record('OPT005 refresh reloads independently persisted global and per-profile layers');
}
// OPT020 exercises the visible novice controls; advanced YAML is only touched
// after a real summary click. Fixture APIs below never download subscriptions.
async function chooseFormOption(page,selector,value){
 const index=await page.eval((selector,value)=>[...document.querySelector(selector).options].findIndex(o=>o.value===value),selector,value);assert.ok(index>=0);
 await page.click(selector);
 for(const [key,code] of [['Escape',27],['Home',36],...Array.from({length:index},()=>['ArrowDown',40]),['Enter',13]]){const event={key,code:key,windowsVirtualKeyCode:code};await page.send('Input.dispatchKeyEvent',{type:'rawKeyDown',...event});await page.send('Input.dispatchKeyEvent',{type:'keyUp',...event})}
 assert.equal(await page.value(selector),value,'visible keyboard dropdown '+selector);
}
function assertAdvancedPreserved(yaml){
 assert.match(yaml,/enable: false/);assert.match(yaml,/fallback:\s*\[192[.]0[.]2[.]55\]|fallback:\s*\n\s*- 192[.]0[.]2[.]55/);
 assert.match(yaml,/fallback-filter:\s*\n\s+geoip: false/);assert.match(yaml,/proxy-groups:/);assert.match(yaml,/name: SyntheticRetainedPolicy/);assert.match(yaml,/type: select/);assert.match(yaml,/# Synthetic advanced fields must survive/);
}
async function simpleOverridesSmoke(page,fixture){
 const [alpha,beta]=fixture.profiles;
 await reload(page);await page.click('.tab[data-view="overridesView"]');
 const initial=await api(fixture,'/api/overrides?scope=global');await eventually(async()=>await page.value('#overrideYAML')===initial.yaml&&await enabled(page,'#overrideLogLevel'),'confirmed simple form GET');
 assert.equal(await detailsOpen(page,'#overrideAdvanced'),false);assert.equal(await detailsOpen(page,'#overridePreview'),false);
 assert.equal(await visible(page,'#overrideYAML'),false);assert.equal(await visible(page,'#previewOverride'),false);
 assert.equal(await visible(page,'#overrideLogLevel'),true);assert.equal(await visible(page,'#overrideDNSMode'),true);
 assert.equal(await page.value('#overrideLogLevel'),initial.simple.logLevel);assert.equal(await page.value('#overrideDNSMode'),initial.simple.dnsServers.length?'custom':'');assert.equal(await page.value('#overrideDNSServers'),initial.simple.dnsServers.join('\n'));
 assert.equal(initial.simple.logEditable,true);assert.equal(initial.simple.dnsEditable,true);
 record('OPT020 default simple form is visible, YAML/preview are folded, GET simple populates log and DNS');
 const seed='log-level: warning\ndns:\n  enable: false\n  nameserver: [192.0.2.53]\n  fallback: [192.0.2.55]\n  fallback-filter:\n    geoip: false\n# Synthetic advanced fields must survive\nproxy-groups:\n  - name: SyntheticRetainedPolicy\n    type: select\n    proxies: [DIRECT]\n';
 // A direct local seed represents a pre-existing advanced layer. Conversion is
 // independently checked to remain pure before any explicit UI save.
 await api(fixture,'/api/overrides',{scope:'global',yaml:seed});await page.click('#loadOverride');await eventually(async()=>await page.value('#overrideYAML')===seed&&await enabled(page,'#overrideLogLevel'),'existing advanced layer GET');
 const stored=await api(fixture,'/api/overrides?scope=global');assert.equal(stored.simple.hasAdvanced,true);assert.match(await page.text('#overrideAdvanced > summary'),/已有高级设置/);
 const snapshot=async()=>({global:await api(fixture,'/api/overrides?scope=global'),alpha:await api(fixture,'/api/overrides?scope=profile&profileId='+alpha.id),beta:await api(fixture,'/api/overrides?scope=profile&profileId='+beta.id),profiles:await api(fixture,'/api/profiles'),settings:await api(fixture,'/api/settings')});
 const before=await snapshot(),dnsServers=['192.0.2.80','https://dns.example.invalid/dns-query'];
 const converted=await api(fixture,'/api/overrides/form',{yaml:seed,logLevel:'error',dnsServers});
 assert.deepEqual(Object.keys(converted).sort(),['simple','yaml']);assert.deepEqual(converted.simple,{logLevel:'error',dnsServers,logEditable:true,dnsEditable:true,hasAdvanced:true});assertAdvancedPreserved(converted.yaml);
 const readOnly=await api(fixture,'/api/overrides/form',{yaml:converted.yaml});assert.equal(readOnly.yaml,converted.yaml);assert.deepEqual(readOnly.simple,converted.simple);
 assert.deepEqual(await snapshot(),before);const pureMetrics=await api(fixture,'/__opt_fixture_metrics');assert.equal(pureMetrics.runtimeFileAbsent,true);assert.equal(pureMetrics.sourceUnchanged,true);assert.equal(pureMetrics.networkRequests,0);
 record('OPT020 POST form returns yaml/simple with optional patches and is pure: persisted layers, settings and source data unchanged');
 await chooseFormOption(page,'#overrideLogLevel','error');await chooseFormOption(page,'#overrideDNSMode','custom');await page.fill('#overrideDNSServers',dnsServers.join('\n'));await page.click('#saveOverride');
 await eventually(async()=>(await api(fixture,'/api/overrides?scope=global')).yaml===converted.yaml&&await enabled(page,'#saveOverride'),'novice dropdown/DNS persistence');
 assertAdvancedPreserved((await api(fixture,'/api/overrides?scope=global')).yaml);assert.equal(await detailsOpen(page,'#overrideAdvanced'),false);assert.equal(await detailsOpen(page,'#overridePreview'),false);await assertIdle(fixture);
 await cleanToast(page);await page.screenshot('#overridesView','simple-overrides-desktop.png');
 record('OPT020 visible log dropdown, DNS mode and multiline input save without YAML, preserving enable=false, fallback, groups and comments');
 await chooseFormOption(page,'#overrideLogLevel','');await chooseFormOption(page,'#overrideDNSMode','');assert.equal(await visible(page,'#overrideDNSServers'),false);
 const inherited=await api(fixture,'/api/overrides/form',{yaml:converted.yaml,logLevel:'',dnsServers:[]});await page.click('#saveOverride');
 await eventually(async()=>(await api(fixture,'/api/overrides?scope=global')).yaml===inherited.yaml&&await enabled(page,'#saveOverride'),'inherit managed fields only');
 assert.doesNotMatch(inherited.yaml,/^log-level:|^\s+nameserver:/m);assertAdvancedPreserved(inherited.yaml);assert.deepEqual(inherited.simple,{logLevel:'',dnsServers:[],logEditable:true,dnsEditable:true,hasAdvanced:true});
 assert.equal((await api(fixture,'/__opt_fixture_metrics')).sourceUnchanged,true);
 record('OPT020 inherit removes only managed log-level/nameserver, retaining other DNS/policy fields and original subscription bytes');
 // Invalid simple conversion leaves both exact failed input and committed layer.
 await chooseFormOption(page,'#overrideDNSMode','custom');await page.fill('#overrideDNSServers','not a DNS address');await page.click('#saveOverride');
 await eventually(async()=>!!await page.text('#overrideError')&&await enabled(page,'#saveOverride'),'bad simple DNS rejected');assert.equal(await page.value('#overrideDNSServers'),'not a DNS address');assert.equal((await api(fixture,'/api/overrides?scope=global')).yaml,inherited.yaml);
 const dialogs=result.dialogs.length;dialogChoices.push(false);await choose(page,'#overrideScope','profile');await eventually(()=>result.dialogs.length===dialogs+1,'simple dirty scope cancelled');
 assert.equal(await page.value('#overrideScope'),'global');assert.equal(await page.value('#overrideDNSServers'),'not a DNS address');assert.equal(await page.value('#overrideDNSMode'),'custom');
 record('OPT020 failed simple DNS input is retained; cancelling an application-scope switch keeps values and saved baseline');
 await page.fill('#overrideDNSServers','192.0.2.81');await page.click('#saveOverride');const retry=await api(fixture,'/api/overrides/form',{yaml:inherited.yaml,dnsServers:['192.0.2.81']});
 await eventually(async()=>(await api(fixture,'/api/overrides?scope=global')).yaml===retry.yaml&&await enabled(page,'#saveOverride'),'simple corrected DNS retry');assertAdvancedPreserved(retry.yaml);
 await setDetails(page,'#overrideAdvanced',true);assert.equal(await visible(page,'#overrideYAML'),true);
 const advanced=retry.yaml+'log-level: debug\n';await page.fill('#overrideYAML',advanced);assert.equal(await enabled(page,'#overrideLogLevel'),false);assert.equal(await enabled(page,'#overrideDNSMode'),false);
 await page.click('#syncOverrideForm');await eventually(async()=>await enabled(page,'#overrideLogLevel')&&await page.value('#overrideLogLevel')==='debug','advanced sync to simple');
 assert.equal(await page.value('#overrideDNSServers'),'192.0.2.81');assert.equal((await api(fixture,'/api/overrides?scope=global')).yaml,retry.yaml);
 await setDetails(page,'#overrideAdvanced',false);assert.equal(await visible(page,'#overrideYAML'),false);await page.click('#saveOverride');await eventually(async()=>(await api(fixture,'/api/overrides?scope=global')).yaml===advanced&&await enabled(page,'#saveOverride'),'advanced candidate save');
 record('OPT020 real advanced summary reveals YAML; edited YAML disables stale simple fields; sync updates simple controls without saving');
 await setDetails(page,'#overrideAdvanced',true);const bad='dns: [unterminated';await page.fill('#overrideYAML',bad);await page.click('#syncOverrideForm');
 await eventually(async()=>(await page.text('#overrideStatus')).includes('同步失败')&&await enabled(page,'#syncOverrideForm'),'failed advanced synchronization');assert.equal(await page.value('#overrideYAML'),bad);assert.equal(await enabled(page,'#overrideLogLevel'),false);assert.equal((await api(fixture,'/api/overrides?scope=global')).yaml,advanced);
 await page.fill('#overrideYAML',advanced);await page.click('#syncOverrideForm');await eventually(()=>enabled(page,'#overrideLogLevel'),'corrected advanced sync');await setDetails(page,'#overrideAdvanced',false);
 record('OPT020 failed advanced synchronization preserves exact YAML and committed layer, with corrected input retry');
 await page.send('Emulation.setDeviceMetricsOverride',{width:412,height:915,deviceScaleFactor:1,mobile:false});await assertNoOverflow(page);
 assert.equal(await detailsOpen(page,'#overrideAdvanced'),false);assert.equal(await detailsOpen(page,'#overridePreview'),false);assert.equal(await visible(page,'#overrideDNSServers'),true);
 await setDetails(page,'#overrideAdvanced',true);await assertNoOverflow(page);await setDetails(page,'#overrideAdvanced',false);await setDetails(page,'#overridePreview',true);await page.click('#previewOverride');await eventually(async()=>!!await page.value('#finalConfigPreview'),'simple mobile read-only preview');await assertNoOverflow(page);await setDetails(page,'#overridePreview',false);
 record('OPT020 412px simple custom DNS, expanded advanced and read-only preview fit without horizontal overflow');
 await page.send('Emulation.setDeviceMetricsOverride',{width:1200,height:1050,deviceScaleFactor:1,mobile:false});await assertIdle(fixture);
}
async function downloadSettingsSmoke(page,fixture){
 const alpha=fixture.profiles[0],profile=async()=>(await api(fixture,'/api/profiles')).profiles.find(p=>p.id===alpha.id);
 await api(fixture,'/__opt_fixture_subscription_seed',{});await reload(page);const initial=await profile();assert.equal(initial.source,'subscription');assert.equal(new URL(initial.url).hostname,'example.invalid');assert.deepEqual(initial.updatePolicy,{mode:'direct',userAgent:'Synthetic-OPT020/1.0',conditional:false,failureBackoff:false});
 await page.click('#addSubscription');assert.equal(await detailsOpen(page,'#subscriptionAdvanced'),false);assert.equal(await visible(page,'#subscriptionUA'),false);await setDetails(page,'#subscriptionAdvanced',true);assert.equal(await visible(page,'#subscriptionUA'),true);await setDetails(page,'#subscriptionAdvanced',false);await page.click('#cancelSubscription');
 // Open URL editing only to inspect an existing subscription; never submit it.
 await page.click('#profileList button','修改订阅地址');assert.equal(await detailsOpen(page,'#subscriptionAdvanced'),false);assert.equal(await page.value('#subscriptionUA'),initial.updatePolicy.userAgent);assert.equal(await checked(page,'#subscriptionConditional'),false);assert.equal(await checked(page,'#subscriptionBackoff'),false);assert.match(await page.text('#subscriptionAdvanced > summary'),/已设置高级选项/);assert.ok(!(await page.text('#subscriptionAdvanced > summary')).includes(initial.updatePolicy.userAgent));
 await setDetails(page,'#subscriptionAdvanced',true);assert.equal(await visible(page,'#subscriptionUA'),true);await setDetails(page,'#subscriptionAdvanced',false);await page.click('#cancelSubscription');
 record('OPT020 subscription add/edit advanced defaults folded; existing non-default UA and false flags prefill, summary shows presence without UA value; no download');
 await page.click('#profileList button','订阅下载设置');assert.match(await page.text('#subscriptionPolicyTitle'),/订阅下载设置/);assert.equal((await page.text('#confirmSubscriptionPolicy')).trim(),'保存设置');
 assert.equal(await detailsOpen(page,'#subscriptionPolicyAdvanced'),false);assert.equal(await visible(page,'#subscriptionPolicyUA'),false);assert.equal(await page.value('#subscriptionPolicyUA'),initial.updatePolicy.userAgent);assert.equal(await checked(page,'#subscriptionPolicyConditional'),false);assert.equal(await checked(page,'#subscriptionPolicyBackoff'),false);assert.match(await page.text('#subscriptionPolicyAdvanced > summary'),/已设置高级选项/);
 await setDetails(page,'#subscriptionPolicyAdvanced',true);assert.equal(await visible(page,'#subscriptionPolicyUA'),true);await setDetails(page,'#subscriptionPolicyAdvanced',false);
 await chooseFormOption(page,'#subscriptionPolicyMode','proxy');const savesBefore=(await api(fixture,'/__opt_fixture_metrics')).policySaves;await page.click('#confirmSubscriptionPolicy');
 await eventually(async()=>(await profile()).updatePolicy.mode==='proxy'&&!await visible(page,'#subscriptionPolicyForm'),'collapsed download settings saved');const firstPolicy={...initial.updatePolicy,mode:'proxy'};assert.deepEqual((await profile()).updatePolicy,firstPolicy);assert.equal((await profile()).url,initial.url);assert.equal((await api(fixture,'/__opt_fixture_metrics')).policySaves,savesBefore+1);
 record('OPT020 collapsed download settings POST update-policy persists mode and retains prefilled UA/false flags without subscription network');
 await reload(page);await page.send('Emulation.setDeviceMetricsOverride',{width:412,height:915,deviceScaleFactor:1,mobile:false});await page.click('#profileList button','订阅下载设置');
 assert.equal(await detailsOpen(page,'#subscriptionPolicyAdvanced'),false);assert.equal(await page.value('#subscriptionPolicyMode'),'proxy');assert.equal(await page.value('#subscriptionPolicyUA'),firstPolicy.userAgent);assert.equal(await checked(page,'#subscriptionPolicyConditional'),false);assert.equal(await checked(page,'#subscriptionPolicyBackoff'),false);await assertNoOverflow(page);
 await setDetails(page,'#subscriptionPolicyAdvanced',true);await assertNoOverflow(page);await page.fill('#subscriptionPolicyUA','Synthetic-OPT020/2.0');await page.click('#subscriptionPolicyConditional');await page.click('#subscriptionPolicyBackoff');await setDetails(page,'#subscriptionPolicyAdvanced',false);await assertNoOverflow(page);await page.click('#confirmSubscriptionPolicy');
 const changed={mode:'proxy',userAgent:'Synthetic-OPT020/2.0',conditional:true,failureBackoff:true};await eventually(async()=>JSON.stringify((await profile()).updatePolicy)===JSON.stringify(changed)&&!await visible(page,'#subscriptionPolicyForm'),'expanded advanced changes survive folded save');
 await page.click('#profileList button','订阅下载设置');assert.equal(await detailsOpen(page,'#subscriptionPolicyAdvanced'),false);assert.equal(await page.value('#subscriptionPolicyUA'),changed.userAgent);assert.equal(await checked(page,'#subscriptionPolicyConditional'),true);assert.equal(await checked(page,'#subscriptionPolicyBackoff'),true);
 const metrics=await api(fixture,'/__opt_fixture_metrics');assert.equal(metrics.policySaves,savesBefore+2);assert.equal(metrics.sourceUnchanged,true);assert.equal(metrics.runtimeFileAbsent,true);assert.equal(metrics.networkRequests,0);assert.equal(metrics.forbiddenRequests,0);await assertIdle(fixture);
 record('OPT020 412px download settings fold/expand fit, reload retains advanced fields, edited advanced values survive folded policy-only save');
 await cleanToast(page);await page.screenshot('#subscriptionPolicyForm','simple-download-settings-mobile.png',true);
 await page.send('Emulation.setDeviceMetricsOverride',{width:1200,height:1050,deviceScaleFactor:1,mobile:false});
}
async function policySmoke(page,fixture){
 await reload(page);await page.click('.tab[data-view="settingsView"]');await page.click('#connectionPolicyDetails > summary');
 const initial=(await api(fixture,'/api/settings')).connectionPolicy;
 assert.equal(initial.startupConnect,false);assert.equal(initial.crashReconnect,false);
 assert.equal(await checked(page,'#startupConnect'),false);assert.equal(await checked(page,'#crashReconnect'),false);
 assert.equal(await page.value('#connectionMaxRetries'),'3');
 assert.equal(await page.eval(()=>document.querySelector('#cancelConnectionRetry').getClientRects().length),0,'inactive cancel action must be hidden');
 record('OPT007 both automation options default OFF; auto-start is separate from connection policy');
 await page.click('#startupConnect');await page.click('#crashReconnect');
 await page.fill('#connectionMaxRetries','4');await page.fill('#connectionInitialDelay','3');await page.fill('#connectionMaxDelay','40');
 await page.click('#saveConnectionPolicy');
 await eventually(async()=>(await api(fixture,'/api/settings')).connectionPolicy.maxRetries===4,'saved connection policy');
 const saved={startupConnect:true,crashReconnect:true,maxRetries:4,initialDelaySeconds:3,maxDelaySeconds:40};
 assert.deepEqual((await api(fixture,'/api/settings')).connectionPolicy,saved);
 await assertIdle(fixture);record('OPT007 opt-in save persists policy only, without immediately connecting or registering auto-start');
 await reload(page);await page.click('.tab[data-view="settingsView"]');await page.click('#connectionPolicyDetails > summary');await eventually(async()=>await checked(page,'#startupConnect'),'startup policy reload');
 assert.equal(await checked(page,'#crashReconnect'),true);assert.equal(await page.value('#connectionMaxRetries'),'4');
 await cleanToast(page);
 await assertIdle(fixture);record('OPT007 refresh preserves policy and remains disconnected');
 // Exercise the actual focus -> Ctrl+A -> late HTTP refresh -> first input
 // boundary, which formerly lost the selection when polling assigned .value.
 await page.click('#connectionMaxRetries');
 const selectAll={key:'a',code:'KeyA',windowsVirtualKeyCode:65,modifiers:2};
 await page.send('Input.dispatchKeyEvent',{type:'rawKeyDown',...selectAll});
 await page.send('Input.dispatchKeyEvent',{type:'keyUp',...selectAll});
 const reads=result.requests.filter(r=>r.path==='/api/settings'&&r.method==='GET').length;
 await page.eval(async()=>{await loadSettings()});
 assert.ok(result.requests.filter(r=>r.path==='/api/settings'&&r.method==='GET').length>reads,'focus barrier must perform a real fixture settings GET');
 await page.send('Input.insertText',{text:'6'});
 assert.equal(await page.value('#connectionMaxRetries'),'6','settings poll before first input must preserve the real Ctrl+A selection');
 record('OPT007 focus and Ctrl+A selection survive a real HTTP settings refresh before first input');
 await page.click('#saveConnectionPolicy');
 await eventually(async()=>(await page.text('#connectionPolicyError')).length>0,'retry range error');
 assert.equal(await page.value('#connectionMaxRetries'),'6');assert.deepEqual((await api(fixture,'/api/settings')).connectionPolicy,saved);
 record('OPT007 out-of-range retries are rejected with input preserved');
 await page.fill('#connectionMaxRetries','4');await page.fill('#connectionInitialDelay','41');await page.click('#saveConnectionPolicy');
 await eventually(async()=>(await page.text('#connectionPolicyError')).includes('最长等待'),'inverted delay bounds');
 assert.deepEqual((await api(fixture,'/api/settings')).connectionPolicy,saved);
 record('OPT007 inverted backoff bounds fail validation without changing persisted policy');
 await page.fill('#connectionInitialDelay','3');await page.fill('#connectionMaxRetries','2');
 await api(fixture,'/__opt_fixture_settings_failure_once',{});await page.click('#saveConnectionPolicy');
 await eventually(async()=>(await page.text('#connectionPolicyError')).includes('Synthetic settings save failure'),'simulated settings HTTP save error');
 assert.equal(await page.value('#connectionMaxRetries'),'2');assert.deepEqual((await api(fixture,'/api/settings')).connectionPolicy,saved);
 record('OPT007 SIMULATED server save failure retains draft settings for retry');
 await page.click('#saveConnectionPolicy');await eventually(async()=>(await api(fixture,'/api/settings')).connectionPolicy.maxRetries===2,'retry after failed settings save');
 await assertIdle(fixture);record('OPT007 retry succeeds after server failure and still does not auto-connect');
 await cleanToast(page);await page.screenshot('section:has(#saveConnectionPolicy)','connection-policy-desktop.png');
 // Restore opt-out before the fixture ends; this does not call disconnect/start.
 await page.click('#startupConnect');await page.click('#crashReconnect');await page.click('#saveConnectionPolicy');
 await eventually(async()=>(await api(fixture,'/api/settings')).connectionPolicy.startupConnect===false,'opt-out restored');
}
async function componentSmoke(page,fixture){
 await reload(page);await page.click('.tab[data-view="maintenanceView"]');await eventually(async()=>(await page.text('#componentList')).includes('启动器'),'component local state');
 const initial=await api(fixture,'/api/components');
 assert.equal(initial.components.length,3);
 const launcher=initial.components.find(c=>c.component==='launcher');
 assert.equal(launcher.trustStatus,'unconfigured');assert.equal(launcher.current.identityStatus,'local-hash');
 assert.equal(launcher.current.version,'');assert.equal(launcher.current.manifestStatus,'unmanaged-local');assert.match(launcher.current.sha256,/^[a-f0-9]{64}$/);
 for(const c of initial.components.filter(c=>c.component!=='launcher'))assert.equal(c.current.identityStatus,'local-hash');
 assert.match(await page.text('#componentList'),/尚未配置可信启动器发布源/);
 for(const action of ['check','download','install','rollback'])assert.equal(await enabled(page,componentButton('launcher',action)),false);
 assert.equal((await api(fixture,'/__opt_fixture_metrics')).mockComponentRequests,0);
 assert.ok(await page.eval(()=>document.querySelectorAll('#componentUpdates input[type=url]').length===0));
 record('OPT008 GET reads real hashes of unmanaged fixture components without inventing versions; launcher has no trusted source or update controls');
 await page.click('#refreshComponents');await eventually(()=>enabled(page,'#refreshComponents'),'local refresh');
 assert.equal((await api(fixture,'/__opt_fixture_metrics')).mockComponentRequests,0);
 assert.equal((await api(fixture,'/__opt_fixture_metrics')).networkRequests,0);
 record('OPT008 page load and local refresh do not automatically check or download network releases');
 const invalid=await request(fixture,'/api/components',{component:'core',action:'check',url:'https://untrusted.browser-fixture.invalid/update'});
 assert.equal(invalid.status,400);assert.match(invalid.body.error,/URL|固定组件|请求无效/);
 assert.equal((await api(fixture,'/__opt_fixture_metrics')).networkRequests,0);
 record('OPT008 real API rejects an arbitrary source URL before any component network activity');
 await page.click(componentButton('core','check'));
 await eventually(async()=>(await page.text(componentCard('core'))).includes('检查中'),'SIMULATED checking progress');
 await eventually(()=>enabled(page,componentButton('core','download')),'SIMULATED checked release');
 assert.match(await page.text(componentCard('core')),/v99[.]0[.]0-fixture/);
 record('OPT008 explicit check displays checking and available version from SIMULATED network response');
 await page.click(componentButton('core','download'));
 await eventually(async()=>(await page.text(componentCard('core'))).includes('25%'),'SIMULATED download percentage');
 await page.screenshot('#componentUpdates','component-progress-desktop.png');
 await eventually(()=>enabled(page,componentButton('core','install')),'SIMULATED verified download ready');
 assert.match(await page.text(componentCard('core')),/已校验|可安装/);
 assert.equal((await api(fixture,'/__opt_fixture_metrics')).installRequests,0);
 record('OPT008 SIMULATED download progress reaches verified-ready text, without installing anything');
 const dialogs=result.dialogs.length;dialogChoices.push(false);await page.click(componentButton('core','install'));
 await eventually(()=>result.dialogs.length===dialogs+1,'install confirmation cancelled');
 assert.equal(result.dialogs.at(-1).accepted,false);
 assert.equal((await api(fixture,'/__opt_fixture_metrics')).installRequests,0);
 await assertIdle(fixture);record('OPT008 cancelling explicit installation confirmation sends no install request and stops no process');
 assert.equal((await api(fixture,'/__opt_fixture_metrics')).mockComponentRequests,2);
 await cleanToast(page);await page.screenshot('#componentUpdates','components-local-desktop.png');
}
async function layoutSmoke(page,fixture){
 await page.send('Emulation.setDeviceMetricsOverride',{width:412,height:915,deviceScaleFactor:1,mobile:false});
 const overflow=async()=>assert.equal(await page.eval(()=>document.documentElement.scrollWidth<=innerWidth),true,'horizontal overflow at 412px');
 await page.click('.tab[data-view="overridesView"]');await eventually(()=>enabled(page,'#saveOverride'),'mobile override controls');await overflow();
 await setDetails(page,'#overridePreview',true);await page.click('#previewOverride');await eventually(async()=>!!await page.value('#finalConfigPreview'),'mobile preview');
 await page.screenshot('#overridesView','override-preview-mobile.png');
 await page.click('.tab[data-view="settingsView"]');await setDetails(page,'#connectionPolicyDetails',true);await overflow();
 const box=await page.eval(()=>{const e=document.querySelector('#saveConnectionPolicy').closest('section');e.scrollIntoView();const r=e.getBoundingClientRect();return{left:r.left,right:r.right,width:innerWidth}});
 assert.ok(box.left>=0&&box.right<=box.width);
 await cleanToast(page);await page.screenshot('section:has(#saveConnectionPolicy)','connection-policy-mobile.png');await overflow();
 await page.click('.tab[data-view="maintenanceView"]');await page.screenshot('#componentUpdates','components-local-mobile.png');await overflow();
 record('OPT005/007/008 forms and component cards fit 412px without horizontal document overflow');
 await assertIdle(fixture);
 const metrics=await api(fixture,'/__opt_fixture_metrics');result.safety=metrics;
 assert.equal(metrics.runtimeFileAbsent,true);assert.equal(metrics.sourceUnchanged,true);assert.equal(metrics.networkRequests,0);assert.equal(metrics.installRequests,0);assert.equal(metrics.forbiddenRequests,0);assert.ok(metrics.formRequests>0);assert.ok(metrics.policySaves>=2);
 const dangerous=result.requests.filter(r=>r.path==='/api/connect'||r.path==='/api/disconnect'||r.path==='/api/import'||r.path==='/api/profiles'&&r.method!=='GET'&&(r.method!=='POST'||r.action!=='update-policy'));
 assert.deepEqual(dangerous,[]);assert.deepEqual(dialogChoices,[]);
 record('Final safety: no runtime YAML, connect/start/autostart/proxy requests, real component network, install or rollback');
}
async function compactSmoke(page,fixture){
 await page.send('Emulation.setDeviceMetricsOverride',{width:1280,height:720,deviceScaleFactor:1,mobile:false});
 await page.eval(()=>scrollTo(0,0));
 const initial=await page.eval(()=>({height:document.scrollingElement.scrollHeight,viewport:innerHeight,settings:document.querySelector('#saveConnectionPolicy').getClientRects().length,maintenance:document.querySelector('#exportBackup').getClientRects().length,toolbar:document.querySelector('#addSubscription').getBoundingClientRect().bottom,profilesBottom:document.querySelector('#profileList').getBoundingClientRect().bottom}));
 await page.screenshot('main','compact-home-initial.png');assert.ok(initial.profilesBottom<=initial.viewport,'two-profile desktop home controls fit one viewport: '+JSON.stringify(initial));assert.equal(initial.settings,0);assert.equal(initial.maintenance,0);assert.ok(initial.toolbar<initial.viewport);
 result.homeGeometry=initial;record('OPT019 1280x720 two-profile home controls fit one screen; settings/maintenance no longer extend home');
 const ids=await page.eval(()=>[...document.querySelectorAll('[id]')].map(e=>e.id));assert.equal(new Set(ids).size,ids.length);record('OPT019 functional controls retain unique IDs after relocation');
 assert.deepEqual(await page.eval(()=>[...document.querySelectorAll('input,textarea,select')].filter(e=>e.getAttribute('autocomplete')!=='off').map(e=>e.id)),[]);record('OPT019 initial input/textarea/select elements suppress native autocomplete');
 await page.click('#addSubscription');assert.equal(await page.value('#subscriptionName'),'');await page.fill('#subscriptionName','Synthetic temporary name');await page.click('#confirmSubscription');
 assert.equal(await page.value('#subscriptionName'),'Synthetic temporary name');assert.match(await page.text('#subscriptionError'),/填写/);record('OPT019 validation failure keeps current input for retry, rather than clearing a draft');
 await page.click('#cancelSubscription');for(const id of ['subscriptionName','subscriptionURL','subscriptionUA'])assert.equal(await page.value('#'+id),'');
 await page.click('#addSubscription');assert.equal(await page.value('#subscriptionName'),'');await page.click('#cancelSubscription');record('OPT019 cancel clears name/URL/UA and reopening Add never pre-fills old input');
 await reload(page);await page.click('#addSubscription');assert.equal(await page.value('#subscriptionName'),'');await page.click('#cancelSubscription');record('OPT019 real browser reload and new Add remain empty');
 await page.click('.tab[data-view="settingsView"]');assert.equal(await page.eval(()=>document.querySelector('#connectionPolicyDetails').open),false);assert.equal(await page.eval(()=>document.querySelector('#portSettingsDetails').open),false);record('OPT019 advanced policy and ports are collapsed by default on settings tab');
 await page.click('.tab[data-view="connectionView"]');await cleanToast(page);await page.screenshot('main','compact-home-desktop.png');
 for(const [width,height] of [[800,500],[412,915],[320,640]]){
  await page.send('Emulation.setDeviceMetricsOverride',{width,height,deviceScaleFactor:1,mobile:false});await page.click('.tab[data-view="settingsView"]');
  await setDetails(page,'#connectionPolicyDetails',true);await setDetails(page,'#portSettingsDetails',true);await page.eval(()=>scrollTo(0,0));
  assert.equal(await page.eval(()=>document.documentElement.scrollWidth<=innerWidth),true,'settings horizontal fit '+width);
  const max=await page.eval(()=>document.scrollingElement.scrollHeight-innerHeight);assert.ok(max>120,'expanded settings exercise document scrolling');
  await page.send('Input.dispatchMouseEvent',{type:'mouseWheel',x:width-2,y:height/2,deltaX:0,deltaY:260});await eventually(()=>page.eval(()=>scrollY>80),'real wheel down '+width);
  const down=await page.eval(()=>scrollY);await page.eval(async()=>{await refreshState();await loadSettings()});assert.ok(Math.abs(await page.eval(()=>scrollY)-down)<=8,'HTTP refresh must not reset scroll (allow subpixel wheel/anchor settling)');
  await page.send('Input.dispatchMouseEvent',{type:'mouseWheel',x:width-2,y:height/2,deltaX:0,deltaY:-260});await eventually(()=>page.eval(down=>scrollY<down-40,down),'real wheel up '+width);
  await page.click('.tab[data-view="maintenanceView"]');assert.equal(await page.eval(()=>scrollY),0,'navigation resets only after successful switch');assert.equal(await page.eval(()=>document.documentElement.scrollWidth<=innerWidth),true,'maintenance horizontal fit '+width);
  await page.click('.tab[data-view="connectionView"]');assert.equal(await page.eval(()=>document.documentElement.scrollWidth<=innerWidth),true,'home horizontal fit '+width);
  record('OPT019 '+width+'px: real wheel moves both ways, polling preserves position, successful navigation returns to top, all tested tabs fit width');
 }
 await page.send('Emulation.setDeviceMetricsOverride',{width:412,height:915,deviceScaleFactor:1,mobile:false});await page.screenshot('main','compact-home-mobile.png');
 await page.send('Emulation.setDeviceMetricsOverride',{width:1200,height:1050,deviceScaleFactor:1,mobile:false});await reload(page);
}
async function main(){
 const fixture=await readFixture();
 assert.ok(process.cwd().replaceAll('\\','/').toLowerCase().includes('/.cache/tests/opt005007008/browser/'),'artifacts must remain in the isolated task run');
 const profile=path.join(process.cwd(),'edge-profile');fs.mkdirSync(profile);
 const browser=spawn(fixture.edge,['--headless=new','--no-first-run','--no-default-browser-check','--disable-background-networking','--disable-component-update','--disable-sync','--disable-extensions','--disable-breakpad','--disable-features=msEdgeSidebarV2,msEdgeShoppingAssistant,msEdgeWallet','--no-proxy-server','--host-resolver-rules=MAP * ~NOTFOUND, EXCLUDE 127.0.0.1, EXCLUDE localhost','--remote-debugging-address=127.0.0.1','--remote-debugging-port=0','--user-data-dir='+profile,'about:blank'],{stdio:'ignore',windowsHide:true});
 let cdp;
 try{
  cdp=await openCDP(profile);const page=await attach(cdp,fixture);
  for(const [name,run] of [['OPT019 layout/history',compactSmoke],['OPT005',overridesSmoke],['OPT020 simple overrides',simpleOverridesSmoke],['OPT020 download settings',downloadSettingsSmoke],['OPT007',policySmoke],['OPT008',componentSmoke],['layout/safety',layoutSmoke]]){
   try{await run(page,fixture)}catch(error){result.failures.push(name+': '+error.message);console.error(name+': '+(error.stack||error.message));dialogChoices.length=0}
  }
  assert.deepEqual(result.failures,[],'isolated Edge acceptance failures');
  console.log('OPT020 + OPT005/007/008 isolated Edge: '+result.checks.length+' checks PASS; component network responses SIMULATED; no install');
 }catch(error){if(!result.failures.length)result.failures.push(error.message);process.exitCode=1}
 finally{
  fs.writeFileSync(path.join(process.cwd(),'results.json'),JSON.stringify(result,null,2),{flag:'wx'});
  if(cdp){cdp.closing=true;try{await cdp.send('Browser.close')}catch{}cdp.socket.close()}
  if(browser.exitCode===null)browser.kill();
 }
}
main().catch(error=>{console.error(error.message);process.exitCode=1});
