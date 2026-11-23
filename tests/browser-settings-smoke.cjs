'use strict';
// Uses an existing installed Edge and the Node standard library only.
// Target must be the explicitly isolated TestIsolatedVisualProbe fixture.
const fs=require('node:fs');
const path=require('node:path');
const os=require('node:os');
const {spawn,spawnSync}=require('node:child_process');
const {setTimeout:delay}=require('node:timers/promises');
const assert=require('node:assert/strict');

async function eventually(fn,what,timeout=10000){const end=Date.now()+timeout;while(Date.now()<end){const result=await fn();if(result)return result;await delay(80)}throw new Error('timeout: '+what)}
class CDP {
  constructor(socket){this.socket=socket;this.next=0;this.pending=new Map();this.errors=[];socket.addEventListener('message',event=>{const data=JSON.parse(String(event.data));if(data.id){const pending=this.pending.get(data.id);if(!pending)return;this.pending.delete(data.id);clearTimeout(pending.timer);if(data.error)pending.reject(new Error(JSON.stringify(data.error)));else pending.resolve(data.result||{})}else if(data.method==='Runtime.exceptionThrown'){this.errors.push(data.params)}})}
  send(method,params={},sessionId){const id=++this.next;return new Promise((resolve,reject)=>{const timer=setTimeout(()=>{this.pending.delete(id);reject(new Error('CDP timeout '+method))},10000);this.pending.set(id,{resolve,reject,timer});this.socket.send(JSON.stringify({id,method,params,...(sessionId?{sessionId}:{})}))})}
  async evaluate(expression,sessionId){const result=await this.send('Runtime.evaluate',{expression,returnByValue:true,awaitPromise:true},sessionId);if(result.exceptionDetails)throw new Error(JSON.stringify(result.exceptionDetails));return result.result.value}
}
function trayCommand(fixture,id){
  assert.ok([7,8,9].includes(id),'only runtime-settings commands are allowed');
  const source='using System; using System.Runtime.InteropServices; public static class OwnedTrayCommand { [DllImport("user32.dll")] public static extern uint GetWindowThreadProcessId(IntPtr hwnd,out uint pid); [DllImport("user32.dll",SetLastError=true)] public static extern bool PostMessageW(IntPtr hwnd,uint msg,IntPtr w,IntPtr l); }';
  const command='Add-Type -TypeDefinition '+"'"+source+"'"+'; $owner=0; [OwnedTrayCommand]::GetWindowThreadProcessId([IntPtr]'+fixture.trayHWND+',[ref]$owner) | Out-Null; if ($owner -ne '+fixture.pid+') { throw "unowned tray HWND" }; if (-not [OwnedTrayCommand]::PostMessageW([IntPtr]'+fixture.trayHWND+',0x111,[IntPtr]'+id+',[IntPtr]0)) { throw "PostMessage failed" }';
  const child=spawnSync('pwsh.exe',['-NoProfile','-Command',command],{stdio:'inherit',windowsHide:true});if(child.error)throw child.error;if(child.status!==0)throw new Error('owned tray command failed');
}
async function main(){
  const [fixtureFile,imageFile,resultFile]=process.argv.slice(2);if(!fixtureFile||!imageFile||!resultFile)throw new Error('usage: node tests/browser-settings-smoke.cjs fixture.json image.png results.json');
  const fixture=JSON.parse(fs.readFileSync(fixtureFile,'utf8'));
  assert.match(fixture.namespace,/^P0P1[-A-Za-z0-9_.]+$/);assert.equal(fixture.proxyRegistryPath,'Software\\MihomoLite\\Tests\\'+fixture.namespace+'\\Proxy');
  const target=new URL(fixture.url);assert.equal(target.hostname,'127.0.0.1');assert.equal(target.protocol,'http:');assert.notEqual(target.port,'17653','never use the existing user instance');
  const edge=process.env.MIHOMOLITE_TEST_BROWSER||'C:\\Program Files (x86)\\Microsoft\\Edge\\Application\\msedge.exe';assert.ok(fs.existsSync(edge),'installed Edge is required');
  const profile=fs.mkdtempSync(path.join(os.tmpdir(),'mihomolite-cdp-'));
  const browser=spawn(edge,['--headless=new','--disable-gpu','--no-first-run','--no-default-browser-check','--disable-extensions','--remote-debugging-port=0','--remote-debugging-address=127.0.0.1','--user-data-dir='+profile,'about:blank'],{stdio:'ignore',windowsHide:true});
  let exited=false;browser.once('exit',()=>{exited=true});let cdp;let socket;const results={namespace:fixture.namespace,url:fixture.url,checks:[],errors:[]};
  try{
    const devtools=await eventually(()=>{const file=path.join(profile,'DevToolsActivePort');return fs.existsSync(file)?fs.readFileSync(file,'utf8').trim().split(/\r?\n/):null},'Edge debugging endpoint');
    const socketURL='ws://127.0.0.1:'+devtools[0]+devtools[1];socket=new WebSocket(socketURL);
    await new Promise((resolve,reject)=>{socket.addEventListener('open',resolve,{once:true});socket.addEventListener('error',reject,{once:true})});cdp=new CDP(socket);
    const created=await cdp.send('Target.createTarget',{url:'about:blank'});const attached=await cdp.send('Target.attachToTarget',{targetId:created.targetId,flatten:true});const session=attached.sessionId;
    await cdp.send('Runtime.enable',{},session);await cdp.send('Page.enable',{},session);await cdp.send('Emulation.setDeviceMetricsOverride',{width:1040,height:1100,deviceScaleFactor:1,mobile:false},session);await cdp.send('Page.navigate',{url:fixture.url+'/'},session);
    const readDOM=()=>cdp.evaluate('({version:document.getElementById("version").textContent,autoStart:document.getElementById("autoStart").checked,closeToTray:document.getElementById("closeToTray").checked,silentStart:document.getElementById("silentStart").checked,disabled:document.getElementById("autoStart").disabled,status:document.getElementById("statusText").textContent})',session);
    const readState=()=>fetch(fixture.url+'/api/state').then(r=>r.json());
    await eventually(async()=>{try{return (await readDOM()).version==='v0.6.3'}catch{return false}},'real console page render');
    await cdp.evaluate('document.querySelector("button[data-view=settingsView]").click()',session);
    await eventually(()=>cdp.evaluate('document.getElementById("settingsView").classList.contains("active") && document.getElementById("autoStart").getClientRects().length > 0',session),'runtime settings view');
    for(const [field,want] of [['autoStart',true],['closeToTray',false],['silentStart',true]]){
      await cdp.evaluate('document.getElementById('+JSON.stringify(field)+').click()',session);
      await eventually(async()=>{const s=await readState(),d=await readDOM();return s[field]===want&&d[field]===want&&!d.disabled},'UI save '+field);
      results.checks.push('real checkbox '+field+' -> persisted API state '+want);
    }
    trayCommand(fixture,7);await eventually(async()=>{const s=await readState(),d=await readDOM();return s.autoStart===false&&d.autoStart===false},'tray autostart -> browser');results.checks.push('native tray autoStart -> real browser checkbox via SSE/poll');
    trayCommand(fixture,8);await eventually(async()=>{const s=await readState(),d=await readDOM();return s.closeToTray===true&&d.closeToTray===true},'tray close behavior -> browser');results.checks.push('native tray closeToTray -> real browser checkbox via SSE/poll');
    trayCommand(fixture,9);await eventually(async()=>{const s=await readState(),d=await readDOM();return s.silentStart===false&&d.silentStart===false},'tray silent startup -> browser');results.checks.push('native tray silent startup -> real browser checkbox via SSE/poll');
    results.finalDOM=await readDOM();results.errors=cdp.errors;assert.equal(results.errors.length,0,'browser JavaScript errors');
    const screenshot=await cdp.send('Page.captureScreenshot',{format:'png',captureBeyondViewport:false},session);fs.writeFileSync(imageFile,Buffer.from(screenshot.data,'base64'));
    fs.writeFileSync(resultFile,JSON.stringify(results,null,2));console.log(JSON.stringify(results,null,2));
  }finally{
    try{if(cdp)await cdp.send('Browser.close')}catch{}
    if(socket)socket.close();await eventually(()=>exited,'owned Edge shutdown',5000).catch(()=>{if(!exited)browser.kill()});
    // Leave the unique profile for diagnosis rather than recursively deleting an unverified path.
  }
}
main().catch(error=>{console.error(error);process.exitCode=1});
