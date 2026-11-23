'use strict';
const assert=require('node:assert/strict');
const fs=require('node:fs');
const path=require('node:path');
const {spawn}=require('node:child_process');
const {setTimeout:delay}=require('node:timers/promises');
const gate='MIHOMOLITE_TEST_OPT004006_BROWSER';
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
async function testRule(page,query,kind='destination'){await page.eval(kind=>{const el=document.querySelector('#ruleTestKind');if(el.value!==kind){el.value=kind;el.dispatchEvent(new Event('change',{bubbles:true}))}},kind);await page.fill('#ruleTestInput',query);await page.click('#ruleTestButton');await eventually(async()=>{const text=await page.text('#ruleTestResult');return text&&!text.startsWith('正在')},'rule simulation response');return page.text('#ruleTestResult')}
async function smoke(page,fixture){
 await page.click('button[data-view="connectionView"]');
 const quota='.profile-card:first-child [data-kind="traffic"]', nodeCard='.profile-card:nth-child(3)', nodeQuota=nodeCard+' [data-kind="traffic"]';
 const readSubscription=async(index=0)=>(await api(fixture,'/api/profiles')).profiles.find(p=>p.id===fixture.profiles[index].id);
 assert.match(await page.text(quota),/已用 3.00 KiB.*总额 10.0 KiB.*剩余 7.00 KiB/);
 assert.match(await page.text(quota),/已过期/);
 assert.doesNotMatch(await page.text('#profileList'),new RegExp(fixture.token));
 record('Quota shows used/remaining/expiry without exposing subscription token');
 assert.equal(await page.eval(()=>document.querySelectorAll('.profile-card:nth-child(2) button').length>0&&[...document.querySelectorAll('.profile-card:nth-child(2) button')].every(b=>b.textContent!=='修改订阅地址')),true,"Local YAML must retain ordinary controls without a subscription URL editor");
 record('Local YAML has no subscription URL edit control');
 assert.equal(await page.eval(()=>[...document.querySelectorAll('.profile-card:nth-child(2) button')].every(b=>b.textContent!=='订阅下载设置')),true,"Local YAML must not expose subscription download settings");
 assert.match(await page.text(nodeQuota),/已用 未知.*总额 未知.*剩余 12.5 GiB.*节点提示/);
 record('Existing subscription without a quota header displays a read-only node estimate; local YAML has no policy control');
 await page.click('button[data-view="rulesView"]');
 await eventually(async()=>(await page.text('#rulesList')).includes('example.com'),'saved rule chain');
 assert.match(await testRule(page,'www.example.com'),/无法模拟确定结果.*GEOSITE/);
 assert.match(await page.text('#ruleTestResult'),/未发现.*数据库/);
 record('GeoSite unknown and missing database are explicit in real backend/UI');
 await page.click('#rulesList .rule-row:first-child .rule-actions button','停用');
 assert.match(await testRule(page,'www.example.com'),/无法模拟确定结果.*GEOSITE/);
 assert.equal((await api(fixture,'/api/rules')).rules[0].enabled,true,"An unsaved rule draft must not change the persisted enabled state");
 record('Unsaved disabled-rule draft does not change saved-rule simulation');
 await page.click('#saveRules');await eventually(async()=>!(await api(fixture,'/api/rules')).rules[0].enabled,'saved disabled rule');
 await eventually(async()=>await page.text('#ruleTestResult')==='','old simulation invalidation after save');
 assert.match(await testRule(page,'www.example.com'),/模拟命中（非内核结果）.*DOMAIN-SUFFIX.*DIRECT/);
 record('Saved rule changes invalidate results and preserve first-match order');
 assert.match(await testRule(page,'2001:db8::1'),/模拟命中（非内核结果）.*IP-CIDR6/);
 record('IPv6 rule simulation returns the stored CIDR hit');
 assert.match(await testRule(page,'app.exe','process'),/无法模拟确定结果.*连接元信息/);
 assert.match(await page.text('#ruleTestResult'),/PROCESS-NAME/);
 record('Process query with missing destination metadata stays unknown');
 await page.screenshot('.rule-tester','rules-simulation.png');
 await page.click('#rulesList .rule-row:last-child .rule-actions button','停用');await page.click('#saveRules');await eventually(async()=>!(await api(fixture,'/api/rules')).rules.at(-1).enabled,'saved process disable');
 assert.match(await testRule(page,'notexample.com'),/模拟未命中.*不代表.*内核/);
 record('Domain suffix boundary produces a scoped miss, not a core claim');
 await page.click('button[data-view="connectionView"]');
 await page.click('.profile-buttons button','修改订阅地址');
 assert.equal(await page.value('#subscriptionURL'),fixture.profiles[0].subscription);
 assert.equal(await page.eval(()=>document.querySelector('#subscriptionName').readOnly),true,"Editing a subscription URL must keep its configuration name read-only");
 const bad=new URL('/fixture-sub/bad?token='+fixture.token,fixture.url).href;
 const replacement=new URL('/fixture-sub/new?token='+fixture.token,fixture.url).href;
 await page.fill('#subscriptionURL',bad);await page.click('#confirmSubscription');
 await eventually(async()=>!!await page.text('#subscriptionError'),'invalid subscription failure');
 assert.equal(await page.value('#subscriptionURL'),bad);
 assert.equal((await api(fixture,'/api/profiles')).profiles.find(p=>p.id===fixture.profiles[0].id).url,fixture.profiles[0].subscription);
 record('Invalid URL content retains old identity and editable failed input');
 await page.fill('#subscriptionURL',replacement);await page.click('input[name="downloadMode"][value="direct"]');await page.click('#confirmSubscription');
 await eventually(async()=>await page.eval(()=>document.querySelector('#subscriptionForm').style.display)==='none','successful URL edit');
 await eventually(async()=>(await page.text(quota)).includes('总额 20.0 KiB'),'new quota after URL commit');
 const profiles=await api(fixture,'/api/profiles'),stored=profiles.profiles.find(p=>p.id===fixture.profiles[0].id);
 assert.equal(stored.url,replacement);assert.equal(stored.id,fixture.profiles[0].id);assert.equal(stored.traffic.total,20480);assert.equal(stored.autoUpdateHours,24);
 assert.equal(await page.value('#subscriptionURL'),'');assert.doesNotMatch(await page.text('#profileList'),new RegExp(fixture.token));
 record('Validated URL edit atomically updates content/metadata without changing identity');
 await page.eval(()=>{const select=document.querySelector('.subscription-schedule select');select.value='6';select.dispatchEvent(new Event('change',{bubbles:true}))});
 await eventually(async()=>(await api(fixture,'/api/profiles')).profiles.find(p=>p.id===fixture.profiles[0].id).autoUpdateHours===6,'existing update schedule');
 record('Existing per-subscription scheduler remains editable after URL replacement');
 const requestsBefore=await api(fixture,'/fixture-stats');
 await page.click('.profile-card:first-child .profile-buttons button','订阅下载设置');
 assert.equal(await page.value('#subscriptionPolicyMode'),'direct');
 assert.equal(await page.value('#subscriptionURL'),'');
 assert.equal(await page.eval(()=>document.querySelector('#subscriptionPolicyConditional').checked),true,"Persisted conditional downloading must remain enabled when opening settings");
 await page.eval(()=>{document.querySelector('#subscriptionPolicyMode').value='auto'});
 assert.equal(await page.eval(()=>document.querySelector('#subscriptionPolicyAdvanced').open),false,"Download settings advanced options must start collapsed");
 assert.equal(await page.text('#subscriptionPolicyAdvancedSummary'),'高级选项');
 assert.equal(await page.eval(()=>{const details=document.querySelector('#subscriptionPolicyAdvanced'),ua=document.querySelector('#subscriptionPolicyUA');if(details.open)return false;if(typeof ua.checkVisibility==='function')return !ua.checkVisibility({contentVisibilityAuto:true,opacityProperty:true,visibilityProperty:true});const box=ua.getBoundingClientRect();return !ua.contains(document.elementFromPoint(box.left+box.width/2,box.top+box.height/2))}),true,"UA input inside closed advanced options must be invisible or excluded from hit testing");
 await page.click('#subscriptionPolicyAdvanced > summary');
 assert.equal(await page.eval(()=>document.querySelector('#subscriptionPolicyAdvanced').open),true,"A real summary click must expand advanced download settings");
 await page.fill('#subscriptionPolicyUA','MihomoLite-Policy-Fixture/1');
 await page.screenshot('#subscriptionPolicyForm','subscription-update-policy.png');
 await page.click('#subscriptionPolicyAdvanced > summary');
 assert.equal(await page.eval(()=>document.querySelector('#subscriptionPolicyAdvanced').open),false,"The second real summary click must collapse advanced download settings");
 assert.equal(await page.value('#subscriptionPolicyUA'),'MihomoLite-Policy-Fixture/1');
 assert.equal(await page.eval(()=>document.querySelector('#subscriptionPolicyConditional').checked&&document.querySelector('#subscriptionPolicyBackoff').checked),true,"Collapsing advanced download settings must preserve both enabled switches");
 assert.doesNotMatch(await page.text('#subscriptionPolicyAdvancedSummary'),/MihomoLite-Policy-Fixture/);
 record('Download settings advanced options open by a real summary click and preserve UA and switches after collapse');
 await page.click('#confirmSubscriptionPolicy');
 await eventually(async()=>await page.eval(()=>document.querySelector('#subscriptionPolicyForm').style.display)==='none','policy save');
 const savedPolicy=await readSubscription();
 assert.deepEqual(savedPolicy.updatePolicy,{mode:'auto',userAgent:'MihomoLite-Policy-Fixture/1',conditional:true,failureBackoff:true});
 assert.equal(savedPolicy.autoUpdateHours,6);assert.equal(await page.value('#subscriptionPolicyUA'),'');
 assert.deepEqual(await api(fixture,'/fixture-stats'),requestsBefore);
 assert.match(await page.text('.profile-card:first-child .subscription-policy-note'),/下载 自动（推荐）.*已设置高级选项/);
 assert.doesNotMatch(await page.text('.profile-card:first-child .subscription-policy-note'),/UA|条件下载|失败退避/);
 assert.doesNotMatch(await page.text('#profileList'),/MihomoLite-Policy-Fixture/);
 record('Policy-only save persists download method and UA without fetching, reconnecting or exposing UA in card text');
 const ready=()=>page.eval(()=>!document.querySelector('.profile-card:first-child .profile-buttons button').disabled);
 await page.click('.profile-card:first-child .profile-buttons button','更新');
 await eventually(async()=>{const p=await readSubscription();return p.lastUpdate?.at!==stored.lastUpdate?.at&&await ready()},'fresh body using changed UA');
 const first=await readSubscription(),stats=await api(fixture,'/fixture-stats');
 assert.equal(stats.requests,requestsBefore.requests+1);assert.equal(stats.bodies,requestsBefore.bodies+1);assert.equal(stats.customUA,requestsBefore.customUA+1);assert.equal(stats.notModified,requestsBefore.notModified);
 assert.equal(first.traffic.total,20480);assert.equal(first.downloadCache,undefined);
 record('Manual update uses the persisted UA and invalidates the old representation validators');
 await page.click('.profile-card:first-child .profile-buttons button','更新');
 await eventually(async()=>{const p=await readSubscription();return p.lastUpdate?.at!==first.lastUpdate?.at&&await ready()},'conditional update result');
 const unchanged=await readSubscription(),after304=await api(fixture,'/fixture-stats');
 assert.equal(after304.requests,stats.requests+1);assert.equal(after304.bodies,stats.bodies);assert.equal(after304.notModified,stats.notModified+1);assert.equal(after304.customUA,stats.customUA+1);
 assert.equal(unchanged.lastUpdate.status,'unchanged');assert.deepEqual(unchanged.traffic,first.traffic);assert.equal(unchanged.updatedAt,first.updatedAt);assert.equal(unchanged.downloadCache,undefined);
 record('HTTP 304 transfers no body and retains YAML update time and prior quota snapshot without exposing validators');
 await page.click(nodeCard+' .profile-buttons button','更新');
 await eventually(async()=>{const p=await readSubscription(2);return p.lastUpdate?.status==='unchanged'&&p.traffic?.source==='nodes'},'persisted node-only quota');
 const nodes=await readSubscription(2);assert.equal(nodes.traffic.remaining,13421772800);assert.equal(nodes.traffic.used,undefined);assert.equal(nodes.traffic.total,undefined);
 assert.match(await page.text(nodeQuota),/剩余 12.5 GiB.*节点提示/);
 record('A successful body with no userinfo header persists the estimated remaining node quota');
 await page.screenshot(nodeCard,'subscription-node-quota.png');
 await page.screenshot('.profile-card:first-child','subscription-information.png');
 await page.click('.profile-buttons button','修改订阅地址');await page.click('#cancelSubscription');assert.equal(await page.value('#subscriptionURL'),'');
 record('Cancelling the editor clears the sensitive URL field');
 const logs=JSON.stringify(await api(fixture,'/api/logs'));assert.ok(!logs.includes(fixture.token));
 record('Validation failure and successful edit logs do not disclose subscription token');
 await page.send('Page.reload',{ignoreCache:true});await eventually(async()=>(await page.text(quota)).includes('总额 20.0 KiB'),'persisted information after reload');
 record('Reload preserves committed subscription metadata');
 await page.click('button[data-view="connectionView"]');
 await eventually(async()=>(await page.text(nodeQuota)).includes('12.5 GiB'),'persisted node hint after reload');
 assert.match(await page.text('.profile-card:first-child .subscription-policy-note'),/自动（推荐）.*已设置高级选项/);
 assert.doesNotMatch(await page.text('.profile-card:first-child .subscription-policy-note'),/UA|条件下载|失败退避|MihomoLite-Policy-Fixture/);
 await page.send('Emulation.setDeviceMetricsOverride',{width:412,height:915,deviceScaleFactor:1,mobile:false});
 assert.equal(await page.eval(()=>document.documentElement.scrollWidth<=innerWidth),true,"The mobile page must not overflow horizontally at 412 pixels");
 await page.click('.profile-card:first-child .profile-buttons button','订阅下载设置');
 assert.equal(await page.eval(()=>{const form=document.querySelector('#subscriptionPolicyForm');const box=form.getBoundingClientRect();return box.left>=0&&box.right<=innerWidth&&form.scrollWidth<=form.clientWidth}),true,"Collapsed mobile download settings must fit the viewport without horizontal overflow");
 assert.equal(await page.eval(()=>document.querySelector('#subscriptionPolicyAdvanced').open),false,"Reopened saved mobile download settings must start collapsed");
 assert.equal(await page.text('#subscriptionPolicyAdvancedSummary'),'高级选项 · 已设置高级选项');
 assert.doesNotMatch(await page.text('#subscriptionPolicyAdvancedSummary'),/MihomoLite-Policy-Fixture/);
 assert.equal(await page.value('#subscriptionPolicyUA'),'MihomoLite-Policy-Fixture/1');
 assert.equal(await page.eval(()=>document.querySelector('#subscriptionPolicyConditional').checked&&document.querySelector('#subscriptionPolicyBackoff').checked),true,"Reopened saved mobile download settings must retain both enabled switches");
 await page.screenshot('#subscriptionPolicyForm','subscription-update-policy-mobile.png');
 await page.click('#subscriptionPolicyAdvanced > summary');
 assert.equal(await page.eval(()=>document.querySelector('#subscriptionPolicyAdvanced').open),true,"A real mobile summary click must expand advanced download settings");
 assert.equal(await page.value('#subscriptionPolicyUA'),'MihomoLite-Policy-Fixture/1');
 assert.equal(await page.eval(()=>{const form=document.querySelector('#subscriptionPolicyForm');const box=form.getBoundingClientRect();return box.left>=0&&box.right<=innerWidth&&form.scrollWidth<=form.clientWidth}),true,"Expanded mobile download settings must fit the viewport without horizontal overflow");
 await page.click('#cancelSubscriptionPolicy');
 assert.equal(await page.value('#subscriptionPolicyUA'),'');
 record('Persisted policy and node quota remain usable without horizontal overflow at 412 pixels, with advanced options closed or opened');
}
async function main(){const fixture=await readFixture();const profile=path.join(process.cwd(),'edge-profile');fs.mkdirSync(profile);const browser=spawn(fixture.edge,['--headless=new','--no-first-run','--no-default-browser-check','--disable-background-networking','--disable-component-update','--disable-sync','--disable-extensions','--disable-breakpad','--disable-features=msEdgeSidebarV2,msEdgeShoppingAssistant,msEdgeWallet','--no-proxy-server','--host-resolver-rules=MAP * ~NOTFOUND, EXCLUDE 127.0.0.1, EXCLUDE localhost','--remote-debugging-address=127.0.0.1','--remote-debugging-port=0','--user-data-dir='+profile,'about:blank'],{stdio:'ignore',windowsHide:true});let cdp;try{cdp=await openCDP(profile);const page=await attach(cdp,fixture);await smoke(page,fixture);assert.deepEqual(result.failures,[]);console.log('OPT-004/006 Edge: '+result.checks.length+' checks PASS')}catch(error){result.failures.push(error.message);throw error}finally{fs.writeFileSync(path.join(process.cwd(),'results.json'),JSON.stringify(result,null,2),{flag:'wx'});if(cdp){cdp.closing=true;try{await cdp.send('Browser.close')}catch{}cdp.socket.close()}if(browser.exitCode===null)browser.kill()}}
main().catch(error=>{console.error(error.stack||error.message);process.exitCode=1});
