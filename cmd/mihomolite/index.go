package main

const indexHTML = `<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<meta name="color-scheme" content="dark"><title>Mihomo Lite</title>
<style>
:root{font-family:Inter,"Segoe UI","Microsoft YaHei",sans-serif;color:#e7eaf0;background:#101217;font-synthesis:none}
[hidden]{display:none!important}
*{box-sizing:border-box}body{margin:0;min-height:100vh;background:radial-gradient(ellipse at 20% 0,#192b2c 0,transparent 48%),#101217}
main{width:min(1000px,calc(100% - 36px));margin:0 auto;padding:16px 0 12px}
header{display:flex;align-items:center;justify-content:space-between;margin-bottom:14px}
.brand{display:flex;align-items:center;gap:12px}.mark{width:36px;height:36px;border-radius:12px;background:#70dfc0;color:#102720;display:grid;place-items:center;font-weight:800;font-size:20px}
h1{font-size:22px;letter-spacing:-.5px;margin:0}small{color:#7f8998}.version{font:12px ui-monospace,monospace;color:#6e7b8c}
.card{background:#191d25;border:1px solid #2a303c;border-radius:18px;padding:18px;box-shadow:0 18px 50px #0003}
.connection{display:flex;justify-content:space-between;gap:20px;align-items:center}.eyebrow{font-size:12px;letter-spacing:1.2px;text-transform:uppercase;color:#8a95a5;margin-bottom:10px}
.status{font-size:30px;font-weight:650;letter-spacing:-1px;margin:0 0 8px}.description{color:#9aa5b5;line-height:1.6;font-size:14px;margin:0}
.dot{display:inline-block;width:9px;height:9px;border-radius:50%;background:#687487;margin-right:10px}.running .dot{background:#70dfc0;box-shadow:0 0 14px #70dfc066}.busy .dot{background:#e9bd6b;animation:pulse 1s infinite}@keyframes pulse{50%{opacity:.35}}
button,a.button{font:inherit;border:0;border-radius:10px;padding:12px 18px;cursor:pointer;text-decoration:none;display:inline-flex;align-items:center;justify-content:center;gap:8px;transition:.15s;white-space:nowrap}
button:disabled{opacity:.4;cursor:not-allowed}.primary{background:#70dfc0;color:#10271f;font-weight:650;min-width:118px}.primary:hover:not(:disabled){background:#8ae7ce}.secondary{background:#282e39;color:#dbe2ed}.secondary:hover:not(:disabled){background:#343c49}.danger{background:#39292d;color:#f1a1ad}
.row{display:flex;gap:10px;align-items:center;flex-wrap:wrap}.actions{margin-top:12px;padding-top:10px;border-top:1px solid #2a303c;display:flex;justify-content:space-between;gap:12px;flex-wrap:wrap}
.section{margin-top:18px;background:#191d25;border:1px solid #2a303c;border-radius:14px;padding:20px}.section h2{font-size:14px;margin:0 0 12px}.hint{font-size:13px;color:#8f9aaa;line-height:1.7;margin:0 0 14px}.filename{color:#b7c0ce;font-size:13px;word-break:break-all}
textarea{display:none;width:100%;height:300px;resize:vertical;margin:14px 0;background:#11141a;color:#cdd6e4;border:1px solid #303846;border-radius:10px;padding:14px;font:12px/1.6 ui-monospace,Consolas,monospace;outline:none}textarea:focus{border-color:#70dfc0}textarea.visible{display:block}
#file{display:none}.error{display:none;margin-top:16px;background:#352227;color:#f2a7b3;border-radius:10px;padding:12px 14px;font-size:13px;line-height:1.6;white-space:pre-wrap;word-break:break-word}.error.visible{display:block}
details{margin-top:16px;color:#7f8998;font-size:12px}summary{cursor:pointer}pre{max-height:180px;overflow:auto;line-height:1.7;white-space:pre-wrap;word-break:break-word}
footer{color:#616c7c;font-size:12px;line-height:1.8;margin-top:22px}a{color:#8bcab9}.secret{font:12px ui-monospace,monospace;background:#10141a;border:1px solid #303846;padding:8px;border-radius:6px;width:100%;margin:10px 0}
.toast{position:fixed;left:50%;bottom:30px;transform:translateX(-50%);background:#dfece7;color:#173429;padding:11px 18px;border-radius:10px;box-shadow:0 10px 40px #0008;font-size:13px;opacity:0;pointer-events:none;transition:.2s}.toast.show{opacity:1}
@media(max-width:560px){main{padding-top:28px}.card{padding:22px}.connection{align-items:flex-start;flex-direction:column}.primary{width:100%}.actions{flex-direction:column}.actions .row{width:100%}}

.tabs{display:grid;grid-template-columns:repeat(5,minmax(0,1fr));gap:4px;border-bottom:1px solid #2a303c;margin-bottom:14px;position:sticky;top:0;z-index:10;background:#101217f2;padding-top:8px;backdrop-filter:blur(12px)}.tab{background:transparent;color:#8995a6;padding:10px 15px;border-radius:8px 8px 0 0}.tab.active{color:#70dfc0;background:#202a2b}.view{display:none}.view.active{display:block}
.rules-toolbar{display:flex;justify-content:space-between;gap:12px;align-items:center;margin-bottom:16px}.rule-count{font-size:12px;color:#7f8998}.rules-list{display:flex;flex-direction:column;gap:8px}.rule-row{display:grid;grid-template-columns:auto minmax(100px,125px) minmax(140px,1fr) minmax(110px,155px) auto;gap:9px;align-items:center;background:#13171e;border:1px solid #2a303c;border-radius:10px;padding:12px}.rule-row.disabled{opacity:.5}.rule-type{font:11px ui-monospace,Consolas,monospace;color:#8ea0b6;word-break:break-word}.rule-value{font-size:13px;color:#e1e7ee;word-break:break-all}.rule-policy{font-size:12px;color:#9fcbbb;word-break:break-word}.rule-note{display:block;color:#738094;font-size:11px;margin-top:3px}.rule-actions{display:flex;gap:4px;align-items:center}.icon-button{padding:6px 8px;background:transparent;color:#91a0b3;border-radius:6px;font-size:12px}.icon-button:hover:not(:disabled){background:#2b323e;color:#fff}.icon-button.delete:hover{color:#f0a0ac}.empty{padding:28px 12px;text-align:center;color:#758195;border:1px dashed #343e4b;border-radius:10px;font-size:13px;line-height:1.8}.rule-form{background:#11161c;border:1px solid #324139;border-radius:12px;padding:16px;margin-bottom:15px}.rule-form h3{margin:0 0 14px;font-size:14px}.form-grid{display:grid;grid-template-columns:1fr 1fr;gap:12px}.form-field{display:flex;flex-direction:column;gap:7px}.form-field.full{grid-column:1/-1}.form-field label{font-size:12px;color:#96a3b4}.form-field input,.form-field select{width:100%;background:#1a202a;border:1px solid #34404f;border-radius:8px;padding:10px;color:#e3e9f0;font:inherit;font-size:13px;outline:none}.form-field input:focus,.form-field select:focus{border-color:#70dfc0}.form-field small{line-height:1.5}.form-bottom{display:flex;justify-content:space-between;align-items:center;gap:14px;margin-top:15px;flex-wrap:wrap}.check{display:flex;align-items:center;gap:7px;font-size:12px;color:#9cacc0}.check input{accent-color:#70dfc0}.rule-error{font-size:12px;color:#f0a4af;line-height:1.6;margin:10px 0;white-space:pre-wrap}.save-state{font-size:12px;color:#8c9bad}.reloading{color:#d9ba7c}
@media(max-width:650px){.rule-row{grid-template-columns:1fr auto}.rule-type{grid-column:1}.rule-value{grid-column:1/-1}.rule-policy{grid-column:1}.rule-actions{grid-column:2;grid-row:1/4;flex-wrap:wrap;justify-content:flex-end;max-width:110px}.form-grid{grid-template-columns:1fr}.form-field.full{grid-column:auto}}

.profile-top{display:flex;justify-content:space-between;align-items:center;gap:12px;margin-bottom:15px}.profile-top h2{margin:0}.profile-actions{display:flex;gap:8px;flex-wrap:wrap}.profile-list{display:flex;flex-direction:column;gap:9px}.profile-card{border:1px solid #2c3542;background:#141920;border-radius:11px;padding:14px;display:grid;grid-template-columns:1fr auto;gap:12px;align-items:center}.profile-card.active{border-color:#518e7d;background:#172321}.profile-name{font-size:14px;font-weight:600;display:flex;align-items:center;gap:8px;flex-wrap:wrap}.profile-meta{font-size:11px;color:#7f8d9f;line-height:1.7;margin-top:6px}.badge{font-size:10px;border-radius:5px;background:#2e3d4b;color:#aebdd0;padding:3px 6px}.badge.active{background:#275243;color:#9ee6cf}.badge.pending{background:#4b3f26;color:#e6c583}.profile-buttons{display:flex;gap:5px;flex-wrap:wrap;justify-content:flex-end}.profile-buttons button{padding:7px 10px;font-size:12px}.subscription-form{padding:16px;background:#131920;border:1px solid #334251;border-radius:12px;margin-bottom:16px}.subscription-form .form-field{margin-bottom:12px}.download-mode{display:flex;flex-wrap:wrap;gap:10px 18px;align-items:center;margin:10px 0 15px;font-size:12px;color:#a0adbf}.download-mode label{display:flex;align-items:center;gap:6px}.download-mode input{accent-color:#70dfc0}.profile-notice{font-size:12px;color:#dbc185;line-height:1.6;padding:11px 13px;border-radius:8px;background:#332d20;margin:14px 0;display:none}.profile-notice.visible{display:block}
.profile-card.subscription-card{grid-template-columns:minmax(0,1fr)}.subscription-card .profile-buttons{justify-content:flex-start}
.subscription-result{font-size:12px;line-height:1.6;margin-top:5px;color:#b8c7d7;overflow-wrap:anywhere}
.subscription-result[data-status=failed],.subscription-result[data-status=saved]{color:#efad9f}
.subscription-schedule{display:flex;align-items:center;gap:8px;flex-wrap:wrap;margin-top:9px;color:#a9b8c9;font-size:12px}
.subscription-schedule select{font:inherit;color:#dbe2ed;background:#1a202a;border:1px solid #34404f;border-radius:6px;padding:4px 6px}
.subscription-options{display:flex;gap:12px 20px;flex-wrap:wrap;margin:12px 0;color:#a0adbf;font-size:12px}.subscription-options label{display:flex;align-items:center;gap:6px}.subscription-options input{accent-color:#70dfc0}.subscription-policy-note{font-size:11px;color:#8ea1b8;overflow-wrap:anywhere}
#subscriptionDiff{width:min(1120px,calc(100% - 32px));max-height:90vh;color:#e7eaf0;background:#191d25;border:1px solid #374354;border-radius:14px;padding:20px}
#subscriptionDiff::backdrop{background:#0a0e17b3}.diff-grid{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:12px}
.diff-grid[hidden]{display:none}.diff-grid h3{font-size:13px;margin:0}.diff-grid pre{max-height:55vh;margin-bottom:0;background:#11141a;padding:10px;border-radius:8px;white-space:pre;word-break:normal;tab-size:2}
.diff-line{display:block;width:max-content;min-width:100%}.diff-removed{background:#562c36;color:#ffd6dc}.diff-added{background:#214839;color:#c9f7df}
@media(max-width:650px){.profile-card{grid-template-columns:minmax(0,1fr)}.profile-buttons{justify-content:flex-start}.diff-grid{grid-template-columns:minmax(0,1fr)}.diff-grid pre{max-height:28vh}}
.core-log-hit{background:#2b2416;color:#fbbf24}
.diagnostics{margin-top:14px;padding:14px;border:1px solid #323c49;border-radius:10px;background:#141920}
.diagnostics p{font-size:13px;line-height:1.6;overflow-wrap:anywhere;white-space:pre-line}
.diagnostic-checks{margin:12px 0;display:grid;gap:8px}
.diagnostic-checks div{display:grid;grid-template-columns:6em 1fr;gap:8px;font-size:13px}
.diagnostic-checks dd{margin:0;color:#aebdd0;overflow-wrap:anywhere}
.diagnostic-checks [data-status=ok]{color:#86dfc5}
.diagnostic-checks [data-status=error]{color:#ff9eaa}
.diagnostic-checks [data-status=warning],.diagnostic-checks [data-status=unknown]{color:#ecd095}
#diagnosticActions{flex-wrap:wrap}
#draftDecision{width:min(520px,calc(100% - 32px));color:#e7eaf0;background:#191d25;border:1px solid #374354;border-radius:14px;padding:24px}#draftDecision::backdrop{background:#0a0e17b3}.draft-box{margin:12px 0;padding:12px 14px;background:#222d2b;border:1px solid #38564e;border-radius:10px;font-size:12px;line-height:1.7}.draft-box[hidden]{display:none}.draft-box p{margin:0 0 8px}.draft-box button{padding:6px 10px;font-size:12px}.draft-warning{color:#f2c47f;white-space:pre-wrap}.draft-status{font-size:12px;color:#c1cfbd;margin-top:10px}.backup-actions{border-top:1px solid #2a303c;padding-top:14px;margin-top:16px}
.settings-block{margin:0;font-size:13px}.settings-block summary{font-size:14px;font-weight:600;color:#dbe2ed}.settings-block[open] summary{margin-bottom:14px}.help-details{margin:10px 0;font-size:12px}.help-details .hint{margin:10px 0 0}.profile-top{flex-wrap:wrap}.profile-top h2{flex-shrink:0}.profile-actions button{padding:9px 12px;font-size:13px}.profile-card{padding:10px}.section{margin-top:12px;padding:16px}.view>.section:first-child{margin-top:0}button:focus-visible,a:focus-visible,summary:focus-visible{outline:2px solid #70dfc0;outline-offset:3px}#connectionInfo{margin-top:14px;padding-top:12px;border-top:1px solid #2a303c}footer{margin-top:14px}#backupRestoreStatus:empty,#yamlDraftStatus:empty,#connectionRetryStatus:empty{display:none}@media(max-width:650px){.tabs{grid-template-columns:repeat(3,minmax(0,1fr))}.tab{font-size:12px;padding:10px 6px}.profile-actions{width:100%}.profile-actions button{flex:1}.rules-toolbar{flex-wrap:wrap}.section{padding:16px}main{padding-top:16px}.connection{gap:12px}.actions{gap:8px}}
</style></head><body><main>
<header><div class="brand"><div class="mark">M</div><div><h1>Mihomo Lite</h1><small>轻量内核启动器</small></div></div><span class="version" id="version">v0.1.0</span></header>

<nav class="tabs"><button class="tab active" data-view="connectionView">连接与配置</button><button class="tab" data-view="rulesView">自定义规则 <span id="ruleBadge"></span></button><button class="tab" data-view="overridesView">个性化设置</button><button class="tab" data-view="settingsView">运行设置</button><button class="tab" data-view="maintenanceView">维护</button></nav>
<div class="view active" id="connectionView"><section class="card"><div class="connection" id="connection"><div><div class="eyebrow">连接状态</div><div class="status" id="status"><span class="dot"></span><span id="statusText">未连接</span></div><p class="description" id="description">导入配置后，即可启动 mihomo。</p></div><div class="row" style="gap:10px"><button class="secondary" id="restart" style="display:none">重启</button><button class="primary" id="toggle" disabled>准备中</button></div></div>
<div class="error" id="error" role="status"></div>
<div class="actions"><button class="secondary" id="diagnose" aria-controls="diagnostics" aria-expanded="false">连接诊断</button><a class="button secondary" id="panel" href="#" target="_blank" rel="noreferrer" style="visibility:hidden">打开 zashboard ↗</a></div>
<div class="diagnostics" id="diagnostics" hidden>
  <p id="diagnosticReason" role="status"></p>
  <dl class="diagnostic-checks">
    <div><dt>内核状态</dt><dd id="diagnosticCore"></dd></div>
    <div><dt>代理端口</dt><dd id="diagnosticPort"></dd></div>
    <div><dt>系统代理</dt><dd id="diagnosticProxy"></dd></div>
  </dl>
  <p id="diagnosticAdvice"></p>
  <div class="row" id="diagnosticActions"></div>
</div><p class="draft-status" id="connectionRetryStatus" role="status"></p><button class="secondary" id="cancelConnectionRetry" hidden>取消自动连接</button><details id="connectionInfo" style="display:none"><summary>本机连接信息与复制</summary><p class="hint" id="ports"></p><div class="row"><button class="secondary" id="copySecret">复制面板密钥</button><button class="secondary" id="copyPorts">复制代理地址</button></div></details></section>
<section class="section"><div class="profile-top"><h2>配置管理</h2><div class="profile-actions"><button class="secondary" id="choose">选择 YAML</button><button class="secondary" id="paste">粘贴 YAML</button><button class="secondary" id="edit">编辑 YAML</button><button class="primary" id="addSubscription">＋ 添加订阅</button></div></div>
<p class="hint" style="margin-bottom:8px">配置独立保存；切换或更新后重连生效，自定义规则保留。</p><details class="help-details"><summary>订阅更新说明</summary><p class="hint">配置独立保存；自定义规则在切换或更新后仍保留。运行中切换或更新，重连后生效。订阅更新请在本页操作；zashboard 面板“设置-后端”里的“更新配置”只会重载运行时配置，不会下载订阅。</p></details>
<div class="profile-notice" id="profileNotice">所选配置已更新。当前连接仍使用旧配置，断开并重新连接后生效。</div>
<div class="draft-box" id="draftBox" hidden><p id="draftSummary"></p><div class="row" id="draftList"></div><p class="draft-warning" id="draftStorageWarning" role="status"></p></div>
<div class="subscription-form" id="subscriptionForm" style="display:none"><h3 id="subscriptionFormTitle" style="margin-top:0;font-size:14px">添加订阅 URL</h3><div class="form-field"><label for="subscriptionName">配置名称</label><input id="subscriptionName" maxlength="80" placeholder="例如：我的机场" autocomplete="off"></div><div class="form-field"><label for="subscriptionURL">订阅地址</label><input id="subscriptionURL" type="url" spellcheck="false" autocomplete="off" placeholder="https://example.com/subscription"><small>地址只保存在本机，可能含令牌，请勿分享；新增或修改地址会先下载并校验，失败保留旧数据。</small></div>
<label style="font-size:12px;color:#96a3b4">下载方式</label><div class="download-mode"><label><input type="radio" name="downloadMode" value="auto" checked autocomplete="off">自动（推荐）</label><label><input type="radio" name="downloadMode" value="direct" autocomplete="off">直连</label><label><input type="radio" name="downloadMode" value="proxy" autocomplete="off">当前代理</label></div><p class="hint">不确定就选自动：连接时先用当前代理，临时失败再试直连；未连接时用直连，选当前代理则不回退直连。</p>
<details class="help-details" id="subscriptionAdvanced" style="margin-bottom:14px;overflow-wrap:anywhere"><summary id="subscriptionAdvancedSummary" style="padding:10px 0;line-height:1.6">高级选项</summary><div class="form-field"><label for="subscriptionUA">自定义客户端标识（User-Agent / UA）</label><input id="subscriptionUA" maxlength="512" spellcheck="false" autocomplete="off" placeholder="留空使用 clash-verge/v2.2.3"><small>一般无需修改；自定义 UA 可能改变订阅格式，只支持最多 512 字节的可打印 ASCII。</small></div><div class="subscription-options"><label><input id="subscriptionConditional" type="checkbox" checked autocomplete="off">条件下载（内容没变时不重复下载）</label><label><input id="subscriptionBackoff" type="checkbox" checked autocomplete="off">失败后稍等再自动重试</label></div><p class="hint">条件下载需服务端支持（ETag / Last-Modified）；失败退避为 5 / 15 / 30 / 60 分钟，手动「重试更新」不受等待限制。下载方式和高级选项保存后用于手动 / 自动更新，折叠不会清除设置。</p><p class="hint">支持 Clash/mihomo YAML，或明文 / Base64 的 SS / VMess / VLESS / Trojan / Hysteria2 / TUIC 分享链接订阅；不支持项整批拒绝，不静默丢节点。新地址和下载设置通过下载校验后才一起提交；已保存但应用失败会单独提示。</p><p class="hint">自动模式仅对网络或服务端临时错误尝试另一方式；鉴权失败、限流与内容校验失败不会换路。</p></details><div class="row" style="justify-content:flex-end"><button class="secondary" id="cancelSubscription">取消</button><button class="primary" id="confirmSubscription">下载并校验</button></div><div class="rule-error" id="subscriptionError" role="status"></div></div>
<div class="subscription-form" id="subscriptionPolicyForm" style="display:none"><h3 id="subscriptionPolicyTitle" style="margin-top:0;font-size:14px">订阅下载设置</h3><div class="form-field"><label for="subscriptionPolicyMode">下载方式</label><select id="subscriptionPolicyMode" autocomplete="off"><option value="auto">自动（推荐）</option><option value="direct">直连</option><option value="proxy">当前代理</option></select><small>不确定就选自动：连接时先用当前代理，临时失败再试直连；未连接时用直连，选当前代理则不回退直连。</small></div><details class="help-details" id="subscriptionPolicyAdvanced" style="margin-bottom:14px;overflow-wrap:anywhere"><summary id="subscriptionPolicyAdvancedSummary" style="padding:10px 0;line-height:1.6">高级选项</summary><div class="form-field"><label for="subscriptionPolicyUA">自定义客户端标识（User-Agent / UA）</label><input id="subscriptionPolicyUA" maxlength="512" spellcheck="false" autocomplete="off" placeholder="留空使用 clash-verge/v2.2.3"><small>一般无需修改；自定义 UA 可能改变订阅格式，只支持最多 512 字节的可打印 ASCII。</small></div><div class="subscription-options"><label><input id="subscriptionPolicyConditional" type="checkbox" checked autocomplete="off">条件下载（内容没变时不重复下载）</label><label><input id="subscriptionPolicyBackoff" type="checkbox" checked autocomplete="off">失败后稍等再自动重试</label></div><p class="hint">条件下载需服务端支持（ETag / Last-Modified）；失败退避为 5 / 15 / 30 / 60 分钟，手动「重试更新」不受等待限制。折叠不会清除设置，自动更新频率仍在订阅卡片设置。</p><p class="hint">自动模式仅对网络或服务端临时错误尝试另一方式；鉴权失败、限流与内容校验失败不会换路。</p></details><p class="hint">只保存下载设置，不会立即下载订阅或重新连接。</p><div class="row" style="justify-content:flex-end"><button class="secondary" id="cancelSubscriptionPolicy">取消</button><button class="primary" id="confirmSubscriptionPolicy">保存设置</button></div><div class="rule-error" id="subscriptionPolicyError" role="status"></div></div>
<div class="profile-list" id="profileList"></div>
<div class="draft-status" id="yamlDraftStatus" role="status"></div><textarea id="config" aria-label="YAML 编辑器" spellcheck="false" placeholder="粘贴完整 YAML 或 SS / VMess / VLESS / Trojan / Hysteria2 / TUIC 分享链接（明文或 Base64）..." autocomplete="off"></textarea><div class="row" id="editorActions" style="display:none"><button class="primary" id="save">校验并保存到当前配置</button><button class="secondary" id="cancel">取消</button></div><input type="file" id="file" accept=".yaml,.yml,text/yaml" style="display:none" autocomplete="off"><div class="filename" id="filename" style="display:none"></div></section>
</div>
<div class="view" id="rulesView"><section class="section" style="margin-top:0">
<div class="rules-toolbar"><div><h2 style="margin-bottom:6px">自定义规则</h2><div class="rule-count" id="ruleSummary">优先于原配置中的规则</div></div><div class="row"><button class="secondary" id="importRules" disabled>导入</button><button class="secondary" id="exportRules">导出</button><button class="primary" id="addRule" disabled>＋ 添加规则</button></div></div>
<p class="hint">按从上到下顺序匹配，置于原始 YAML 规则之前。停用规则会保留但不参与匹配。跨订阅代理规则可选择「统一代理」：连接时自动关联节点选择组或节点/provider；原订阅和已有规则目标不会自动改写。</p>
<div class="rule-bulk" id="ruleBulk" style="display:none;align-items:center;gap:10px;flex-wrap:wrap;padding:10px 12px;background:#141922;border:1px solid #2a303c;border-radius:8px;margin-bottom:12px"><label class="check"><input type="checkbox" id="ruleSelectAll" autocomplete="off">全选</label><span id="ruleSelectedCount" style="font-size:12px;color:#8a95a5"></span><div class="row" style="margin-left:auto"><button class="secondary" id="bulkEnable">启用选中</button><button class="secondary" id="bulkDisable">停用选中</button><button class="secondary" id="bulkDelete">删除选中</button></div></div>
<div class="rule-tester" style="display:flex;flex-direction:column;gap:8px;padding:10px 12px;background:#141922;border:1px solid #2a303c;border-radius:8px;margin-bottom:12px"><label style="font-size:12px;color:#8a95a5">规则模拟测试</label><small style="color:#8a95a5">仅使用已保存的启用自定义规则，不包含未保存草稿或原 YAML 规则链；不执行 DNS 查询、Geo 数据库判定或完整内核流量匹配，也不验证进程归属与路由模式。未提供的另一维度信息也视为不确定。遇到无法模拟的前序规则会显示“无法确定”，而非“未命中”。</small><div class="row"><select id="ruleTestKind" aria-label="测试对象" autocomplete="off"><option value="destination">目标域名 / IP</option><option value="process">进程名 / 路径</option></select><input id="ruleTestInput" placeholder="输入域名或 IP，如 www.google.com" style="flex:1;min-width:220px" autocomplete="off"><button class="secondary" id="ruleTestButton">测试模拟结果</button></div><div class="rule-test-result" id="ruleTestResult"></div></div>
<input type="file" id="importRulesFile" accept=".json,application/json" style="display:none" autocomplete="off">
<div class="rule-form" id="ruleForm" style="display:none"><h3 id="formTitle">添加规则</h3><input type="hidden" id="ruleID" autocomplete="off"><div class="form-grid">
<div class="form-field"><label for="ruleType">匹配类型</label><select id="ruleType" autocomplete="off"><option value="DOMAIN-SUFFIX">域名后缀</option><option value="DOMAIN">完整域名</option><option value="DOMAIN-KEYWORD">域名关键词</option><option value="IP-CIDR">IPv4 网段</option><option value="IP-CIDR6">IPv6 网段</option><option value="PROCESS-NAME">进程名</option><option value="PROCESS-PATH">进程路径</option><option value="GEOIP">GEOIP 地区</option><option value="GEOSITE">GEOSITE 分类</option></select></div>
<div class="form-field"><label for="rulePolicy">目标策略</label><select id="rulePolicy" autocomplete="off"><option>DIRECT</option><option>REJECT</option></select></div>
<div class="form-field full"><label for="ruleValue">匹配内容</label><input id="ruleValue" autocomplete="off" spellcheck="false" placeholder="example.com"><small id="ruleHelp">匹配 example.com 及其所有子域名，不带 http:// 或路径。</small></div>
<div class="form-field full"><label for="ruleNote">备注（可选）</label><input id="ruleNote" maxlength="200" placeholder="这条规则的用途" autocomplete="off"></div>
</div><div class="form-bottom"><div class="row"><label class="check"><input type="checkbox" id="ruleEnabled" checked autocomplete="off">启用</label><label class="check" id="noResolveWrap" style="display:none"><input type="checkbox" id="ruleNoResolve" autocomplete="off">不触发 DNS 解析</label></div><div class="row"><button class="secondary" id="cancelRule">取消</button><button class="primary" id="confirmRule">添加到列表</button></div></div><div class="rule-error" id="ruleError"></div></div>
<div class="rules-list" id="rulesList"></div>
<div class="actions" style="margin-top:18px"><span class="save-state" id="ruleSaveState">更改尚未保存</span><button class="primary" id="saveRules" disabled>保存并应用</button></div>
<div class="rule-error" id="rulesError"></div>
</section></div>
<div class="view" id="overridesView"><section class="section" style="margin-top:0"><h2>个性化设置</h2><p class="hint">一般保持“沿用”即可。这里的设置不会被订阅更新覆盖；网站或软件的分流请使用“自定义规则”。</p><div class="form-grid"><div class="form-field"><label for="overrideScope">应用范围</label><select id="overrideScope" autocomplete="off"><option value="global">所有配置</option><option value="profile">仅所选配置</option></select></div><div class="form-field"><label for="overrideProfile">所选配置</label><select id="overrideProfile" autocomplete="off"></select><small id="overrideScopeHelp">所有配置共用这些设置；所选配置用于查看最终配置。</small></div><div class="form-field"><label for="overrideLogLevel">日志详细程度</label><select id="overrideLogLevel" autocomplete="off" disabled><option value="" id="overrideLogInherit">沿用订阅（推荐）</option><option value="info">正常：记录连接信息</option><option value="warning">精简：仅警告和错误</option><option value="error">仅错误</option><option value="silent">关闭日志</option><option value="debug">调试：排查问题时使用</option></select><small id="overrideLogHelp">不确定时保持沿用即可。</small></div><div class="form-field"><label for="overrideDNSMode">DNS 服务器</label><select id="overrideDNSMode" autocomplete="off" disabled><option value="" id="overrideDNSInherit">沿用订阅（推荐）</option><option value="custom">自定义服务器</option></select><small id="overrideDNSHelp">只调整 DNS 服务器，其他 DNS 设置和启用状态保持不变。</small></div><div class="form-field full" id="overrideDNSServersField" hidden><label for="overrideDNSServers">DNS 地址（每行一个）</label><textarea class="visible" id="overrideDNSServers" disabled style="height:88px;margin:0" spellcheck="false" autocomplete="off" placeholder="填写你要使用的 DNS IP 或 https:// 地址"></textarea><small>不确定填什么时，请选择沿用。不会自动启用原配置中关闭的 DNS。</small></div></div><p class="draft-status" id="overrideStatus" role="status">正在读取设置…</p><div class="row"><button class="primary" id="saveOverride" disabled>保存设置</button><button class="secondary" id="loadOverride">重新读取</button></div><p class="rule-error" id="overrideError" role="alert"></p><details class="settings-block" id="overrideAdvanced" style="margin-top:18px"><summary id="overrideAdvancedSummary">高级设置（YAML）</summary><p class="hint">不需要写 YAML 也能使用上面的设置。高级内容会保留；数组整体替换，不是追加。端口、本机监听和 TUN 等仍受 Lite 安全限制。</p><textarea class="visible" id="overrideYAML" disabled spellcheck="false" aria-label="高级覆写 YAML" autocomplete="off"></textarea><button class="secondary" id="syncOverrideForm">同步到简单设置</button><p class="hint">简单设置有修改时，先同步再编辑 YAML。修改 YAML 后可同步表单；仅同步，不保存、不连接。</p></details><details class="settings-block backup-actions" id="overridePreview"><summary>查看最终配置（排查问题时使用）</summary><p class="hint">只读预览，不保存、不连接或下载资源。自动端口可能与实际连接不同。节点密码、订阅地址仍可能可见，请勿分享。</p><button class="secondary" id="previewOverride" disabled>预览当前设置</button><p class="draft-status" id="previewStatus" role="status"></p><textarea class="visible" id="finalConfigPreview" readonly aria-label="最终配置只读预览" spellcheck="false" autocomplete="off"></textarea></details></section></div>
<div class="view" id="settingsView"><section class="section"><h2>窗口与启动</h2><div class="row" style="gap:16px;flex-wrap:wrap"><label class="check"><input type="checkbox" id="autoStart" autocomplete="off">开机自启（登录后自动启动并最小化到托盘）</label><label class="check"><input type="checkbox" id="closeToTray" autocomplete="off">关闭窗口时最小化到托盘</label><label class="check"><input type="checkbox" id="silentStart" autocomplete="off">静默启动到托盘（下次普通启动生效）</label></div>
</section><section class="section"><details class="settings-block" id="connectionPolicyDetails"><summary>自动连接策略</summary><p class="hint">均默认关闭。启动后连接仅尝试一次，保存开关不会立即连接。崩溃重连先恢复系统代理，再限次指数退避；手动断开 / 退出取消。持续配置错误或恢复失败不会无限重试，内核稳定运行 60 秒后才重置崩溃预算。</p><div class="row"><label class="check"><input id="startupConnect" type="checkbox" autocomplete="off">下次启动后自动连接</label><label class="check"><input id="crashReconnect" type="checkbox" autocomplete="off">内核意外退出后重连</label></div><div class="form-grid" style="margin-top:14px"><div class="form-field"><label for="connectionMaxRetries">最大重试次数（1–5）</label><input id="connectionMaxRetries" type="number" min="1" max="5" value="3" autocomplete="off"></div><div class="form-field"><label for="connectionInitialDelay">初始等待（秒，1–60）</label><input id="connectionInitialDelay" type="number" min="1" max="60" value="2" autocomplete="off"></div><div class="form-field"><label for="connectionMaxDelay">最长等待（秒，1–300）</label><input id="connectionMaxDelay" type="number" min="1" max="300" value="30" autocomplete="off"></div></div><div class="row" style="margin-top:14px"><button class="primary" id="saveConnectionPolicy">保存连接策略</button></div><p class="rule-error" id="connectionPolicyError" role="alert"></p>
</details></section><section class="section"><details class="settings-block" id="portSettingsDetails"><summary>本机端口</summary><p class="hint">自动模式优先使用默认端口，占用时另选空闲端口；固定模式仅使用指定端口，占用或不可用时明确失败，不自动回退。只监听 127.0.0.1，面板 / 内核 API 仍使用密钥认证。</p>
<div class="form-grid" id="portSettingsForm">
<div class="form-field"><label for="proxyPortMode">代理端口（Mixed HTTP + SOCKS5）</label><select id="proxyPortMode" autocomplete="off"><option value="auto">自动（优先 17890）</option><option value="fixed">固定</option></select><input id="proxyPort" type="number" min="1" max="65535" step="1" aria-label="固定代理端口" placeholder="17890" disabled autocomplete="off"></div>
<div class="form-field"><label for="backendPortMode">内核 backend API（external-controller）</label><select id="backendPortMode" autocomplete="off"><option value="auto">自动（空闲端口）</option><option value="fixed">固定</option></select><input id="backendPort" type="number" min="1" max="65535" step="1" aria-label="固定内核 backend API 端口" placeholder="19091" disabled autocomplete="off"></div>
<div class="form-field"><label for="panelPortMode">面板 API 转发端口（zashboard 入口）</label><select id="panelPortMode" autocomplete="off"><option value="auto">自动（优先 19090）</option><option value="fixed">固定</option></select><input id="panelPort" type="number" min="1" max="65535" step="1" aria-label="固定面板 API 转发端口" placeholder="19090" disabled autocomplete="off"></div>
<div class="form-field"><label for="webPortMode">启动器 Web 端口（本页面）</label><select id="webPortMode" autocomplete="off"><option value="auto">自动（优先 17653）</option><option value="fixed">固定</option></select><input id="webPort" type="number" min="1" max="65535" step="1" aria-label="固定启动器 Web 端口" placeholder="17653" disabled autocomplete="off"></div>
</div><p class="hint">固定端口范围 1–65535，四类端口不能重复。保存仅更新偏好；所有端口须退出并重新启动程序才生效，单独重连不会切换面板 / Web 地址，也不会改变本次程序启动的端口策略。</p><div class="row"><button class="primary" id="savePortSettings">保存端口设置</button></div><p class="draft-status" id="portSettingsStatus" role="status"></p><p class="rule-error" id="portSettingsError" role="alert"></p></details></section></div>
<div class="view" id="maintenanceView"><section class="section" id="backupSection"><h2>备份与还原</h2><div><div class="row"><button class="secondary" id="exportBackup">导出完整备份</button><button class="secondary" id="importBackup">从备份还原</button></div><details class="help-details"><summary>备份内容与安全说明</summary><p class="hint" style="margin:10px 0 0">备份包含已保存的配置、订阅信息、更新设置、自定义规则和上一版 YAML，可能含地址与密钥，请妥善保管。包含独立全局 / 每配置覆写及端口偏好；端口还原后下次程序启动生效。旧备份未含覆写时还原为空覆写；浏览器草稿、外部文件、内核与面板、启动器 API 密钥及其它本机启动设置不包含在内。旧备份不改变本机端口偏好。还原会替换配置集合和规则，需先断开连接。</p></details><p class="draft-status" id="backupRestoreStatus" role="status"></p><input type="file" id="backupFile" accept=".json,application/json" style="display:none" autocomplete="off"></div></section>
<section class="section" id="componentUpdates"><div class="profile-top"><h2>组件版本与更新</h2><button class="secondary" id="refreshComponents">读取本地状态</button></div><p class="hint">内核与面板仅使用固定官方 GitHub 发布源，下载必须通过 SHA-256 校验；下载不安装。安装 / 回滚须明确确认并已断开，不会强杀内核。受控发布安装须走整包发布，禁止单独替换破坏清单。启动器尚未配置可信发布源，只核对本地身份，不提供自替换。</p><div id="componentList" class="profile-list"></div><p class="rule-error" id="componentError" role="alert"></p></section>
<section class="section"><h2>日志</h2><details id="coreLogsDetails"><summary>mihomo 连接日志</summary><pre id="coreLogs" style="max-height:360px;overflow:auto">暂无日志</pre></details>
<details><summary>启动器日志</summary><pre id="logs">暂无日志</pre></details></section></div>
<footer>仅监听本机 · 不使用 TUN · 无需管理员权限<br>开启最小化到托盘时，关闭控制台窗口不会断开连接；托盘“退出”或 Ctrl+C 会停止内核并恢复系统代理。若异常断电，请检查 Windows 代理设置。</footer>
</main><dialog id="draftDecision" aria-labelledby="draftDecisionTitle"><h2 id="draftDecisionTitle" style="font-size:17px;margin-top:0">有未保存的修改</h2><p class="hint" id="draftDecisionMessage"></p><p class="draft-warning" id="draftDecisionError" role="status"></p><div class="row"><button class="primary" id="draftSave">保存后继续</button><button class="secondary" id="draftKeep">保留草稿并继续</button><button class="danger" id="draftDiscard">放弃修改并继续</button><button class="secondary" id="draftCancel">取消</button></div></dialog><dialog id="subscriptionDiff" aria-labelledby="subscriptionDiffTitle"><div class="profile-top"><h2 id="subscriptionDiffTitle">订阅更新对比</h2><form method="dialog" autocomplete="off"><button class="secondary">关闭</button></form></div>
<p class="hint" id="subscriptionDiffStatus" role="status"></p><div class="diff-grid" id="subscriptionDiffContent" hidden><section><h3>更新前</h3><pre id="subscriptionBefore" tabindex="0" aria-label="更新前 YAML"></pre></section><section><h3>更新后</h3><pre id="subscriptionAfter" tabindex="0" aria-label="更新后 YAML"></pre></section></div></dialog><div class="toast" id="toast"></div><script>
const $=id=>document.getElementById(id);let state={};let logs=[];
function toast(text){$('toast').textContent=text;$('toast').classList.add('show');setTimeout(()=>$('toast').classList.remove('show'),2400)}
function render(s){state=s;applySettings(s,true);const retry=s.connectionRetry||{};$('cancelConnectionRetry').hidden=!retry.active;$('cancelConnectionRetry').disabled=typeof connectionActionBusy!=='undefined'&&connectionActionBusy;$('connectionRetryStatus').textContent=retry.waiting?'等待自动重连：'+retry.attempt+'/'+retry.maxRetries+' · '+(retry.nextRetryAt?new Date(retry.nextRetryAt).toLocaleTimeString():''):(retry.active?'自动连接尝试中…':retry.exhausted?'自动连接已停止：'+(retry.error||'重试次数用尽'):'');$('version').textContent='v'+s.version;$('connection').className='connection '+(s.running&&s.proxyEnabled?'running ':'')+(s.busy?'busy':'');
$('statusText').textContent=s.busy?'处理中…':(s.running?(s.proxyEnabled?'已连接':'系统代理异常'):(s.connectionError?'连接异常':'未连接'));
$('description').textContent=s.running?(s.proxyEnabled?'zashboard 已就绪，系统代理已开启。':'内核已运行，但系统代理未成功开启。'):(s.hasConfig?'配置已就绪，可以启动连接。':'导入配置后，即可启动 mihomo。');
$('toggle').textContent=retry.active?'取消自动连接':s.busy?'请稍候…':(s.running?'断开连接':'连接');$('toggle').disabled=!s.canConnect&&!s.canDisconnect;$('toggle').className='primary '+(s.running?'danger':'');$('restart').style.display=s.running?'inline-block':'none';$('restart').disabled=s.busy;
$('panel').style.visibility=s.hasPanelURL?'visible':'hidden';if(s.hasPanelURL)$('panel').href=s.panelURL;
if(s.profilePending&&s.running)$('description').textContent='所选配置已更新，重连后生效。';const error=s.lastError||s.connectionError||s.proxyError||'';$('error').textContent=error;$('error').className='error '+(error?'visible':'');
$('connectionInfo').style.display=s.running?'block':'none';$('ports').textContent='Mixed HTTP + SOCKS5：127.0.0.1:'+s.mixedPort+'　·　面板 API 转发：127.0.0.1:'+s.apiPort;
if(!s.coreReady||!s.panelReady){$('description').textContent='缺少内核或面板，请先运行目录中的 setup.ps1。';if(!s.running)$('toggle').disabled=true}}

async function api(path,options={}){options.headers={'Content-Type':'application/json','X-Mihomo-Lite':'1',...(options.headers||{})};const r=await fetch(path,options);let data={};try{data=await r.json()}catch{}if(!r.ok)throw new Error(data.error||'请求失败');return data}
let diagnosisOpen=false;
let diagnosisRequest=0;
let connectionActionBusy=false;
const checkNames={ok:'正常',warning:'注意',error:'异常',unknown:'无法确认'};

async function connectionAction(path,body={}){
 if(connectionActionBusy||(state.busy&&!(path==='/api/disconnect'&&state.connectionRetry?.active)))return;
 connectionActionBusy=true;
 $('toggle').disabled=true;
 $('restart').disabled=true;
 $('diagnosticActions').querySelectorAll('button').forEach(button=>button.disabled=true);
 try{await api(path,{method:'POST',body:JSON.stringify(body)});}
 catch(error){toast(error.message);}
 finally{
  connectionActionBusy=false;
  const diagnosisBeforeRefresh=diagnosisRequest;
  await refreshState();
  if(diagnosisOpen&&!state.busy&&diagnosisRequest===diagnosisBeforeRefresh)await runDiagnosis();
 }
}

function showDiagnosis(){
 diagnosisOpen=true;
 $('diagnostics').hidden=false;
 $('diagnose').setAttribute('aria-expanded','true');
 $('diagnose').textContent='重新检查';
}

async function runDiagnosis(){
 showDiagnosis();
 const request=++diagnosisRequest;
 $('diagnose').disabled=true;
 $('diagnosticActions').textContent='';
 $('diagnosticAdvice').textContent='';
 $('diagnosticReason').textContent=state.busy?'操作完成后会重新检查。':'正在检查本机连接…';
 const rows=['Core','Port','Proxy'];
 for(const row of rows){
  const field=$('diagnostic'+row);
  field.textContent='等待检查';
  field.dataset.status='';
 }
 if(state.busy)return;
 try{
  const result=await api('/api/diagnostics');
  if(request!==diagnosisRequest)return;
  $('diagnosticReason').textContent=result.reason||(result.core.status==='ok'&&result.port.status==='ok'&&result.proxy.status==='ok'?'本机连接检查通过。':'');
  if(result.busy)return;
  if(state.running&&[result.core,result.port,result.proxy].some(check=>check.status==='error')){
   $('connection').classList.remove('running');
   $('statusText').textContent='连接异常';
   $('description').textContent='请查看连接诊断结果。';
  }
  for(const row of rows){
   const field=$('diagnostic'+row);
   const check=result[row.toLowerCase()];
   field.dataset.status=check.status;
   const label=checkNames[check.status];
   field.textContent=label?label+' · '+check.detail:check.detail;
  }
  $('diagnosticAdvice').textContent=result.advice||'';
  for(const action of result.actions||[]){
   const handler=diagnosticActions[action.id];
   if(!Object.hasOwn(diagnosticActions,action.id))continue;
   const button=document.createElement('button');
   button.className='secondary';
   button.textContent=action.label;
   button.disabled=connectionActionBusy&&(action.id==='retry'||action.id==='restart'||action.id==='repairProxy'||action.id==='restoreProxy'||action.id==='keepCurrentProxy'||action.id==='stopLeftoverCore');
   button.onclick=handler;
   $('diagnosticActions').appendChild(button);
  }
 }catch(error){
  if(request===diagnosisRequest)$('diagnosticReason').textContent='无法获取诊断结果：'+error.message;
 }finally{
  if(request===diagnosisRequest)$('diagnose').disabled=false;
 }
}

const diagnosticActions={
 retry:()=>connectionAction('/api/connect'),
 restart:()=>connectionAction('/api/reconnect'),
 repairProxy:()=>connectionAction('/api/repair-proxy'),
 restoreProxy:()=>connectionAction('/api/restore-proxy'),
 keepCurrentProxy:()=>{
  if(confirm('保留当前其他软件的代理设置，并放弃连接前的旧代理快照？此操作不修改系统代理。'))return connectionAction('/api/recovery/keep-current-proxy',{confirm:true});
 },
 stopLeftoverCore:()=>{
  if(confirm('结束已核验属于上次 Mihomo Lite 会话的遗留内核？未核验的进程不会被结束。'))return connectionAction('/api/recovery/stop-leftover-core',{confirm:true});
 },
 import:()=>$('choose').click(),
 logs:async()=>{
  if(!await navigateView('maintenanceView'))return;
  const details=$('coreLogsDetails');
  details.open=true;
  details.scrollIntoView({block:'start'});
  return loadCoreLogs();
 },
 windowsProxy:async()=>{
  try{await api('/api/open-proxy-settings',{method:'POST',body:'{}'});}
  catch(error){toast(error.message);}
 },
};
$('diagnose').onclick=runDiagnosis;
$('toggle').onclick=()=>connectionAction(state.running||state.connectionRetry?.active?'/api/disconnect':'/api/connect');
$('restart').onclick=()=>connectionAction('/api/reconnect');
$('choose').onclick=()=>$('file').click();
$('file').onchange=async e=>{
 const file=e.target.files[0];
 e.target.value='';
 if(!file)return;
 if(file.size>4*1024*1024){toast('文件超过 4 MiB');return}
 await runGuardedProfileOperation(async()=>{
  await importYAML(await file.text());
  closeYAMLEditor();
  toast('配置已导入并选中');
 });
};
let editorProfileID='',editorOpen=false,editorBase='',editorBaseline='';
const draftStorageKey='mihomolite.drafts.v1';
let drafts={yaml:{},rules:null},draftStorageError='',draftReadError=false,profileNames=new Map(),activeProfileID='';
let activeView='connectionView',draftDecisionResolve=null,draftDecisionKeys=[],draftDecisionBusy=false;
function fingerprint(text){let h=2166136261;for(let i=0;i<text.length;i++){h^=text.charCodeAt(i);h=Math.imul(h,16777619)}return text.length+':'+(h>>>0)}
let draftSnapshot={yaml:{},rules:null};
function readDraftStore(){
 const data={yaml:{},rules:null};
 for(let index=0;index<localStorage.length;index++){
  const key=localStorage.key(index);if(!key||!key.startsWith(draftStorageKey+':'))continue;
  const raw=localStorage.getItem(key);if(raw===null)continue;const value=JSON.parse(raw);
  if(key===draftStorageKey+':rules'){if(!value||!Array.isArray(value.rules))throw new Error();data.rules=value}
  else{const id=key.slice((draftStorageKey+':yaml:').length);if(!key.startsWith(draftStorageKey+':yaml:')||(id!=='new'&&!/^[a-f0-9]{64}$/i.test(id))||!value||typeof value.content!=='string'||typeof value.base!=='string')throw new Error();data.yaml[id]=value}
 }
 return data;
}
function initializeDrafts(){
 try{drafts=readDraftStore();draftSnapshot=JSON.parse(JSON.stringify(drafts))}
 catch{draftReadError=true;draftStorageError='无法读取浏览器草稿。现有存储不会被覆盖；请先保存或复制编辑内容。'}
 renderDrafts();
}
function persistDrafts(){
 let conflict=false;
 try{
  if(draftReadError)throw new Error();
  const encode=value=>value==null?null:JSON.stringify(value),changes=[];
  for(const id of new Set([...Object.keys(draftSnapshot.yaml),...Object.keys(drafts.yaml)]))if(encode(draftSnapshot.yaml[id])!==encode(drafts.yaml[id]))changes.push({key:draftStorageKey+':yaml:'+id,id,value:drafts.yaml[id],before:draftSnapshot.yaml[id]});
  if(encode(draftSnapshot.rules)!==encode(drafts.rules))changes.push({key:draftStorageKey+':rules',id:null,value:drafts.rules,before:draftSnapshot.rules});
  for(const change of changes){const raw=localStorage.getItem(change.key),actual=raw===null?null:encode(JSON.parse(raw));if(actual!==encode(change.before)&&actual!==encode(change.value)){conflict=true;throw new Error()}}
  for(const change of changes){if(change.value==null)localStorage.removeItem(change.key);else localStorage.setItem(change.key,encode(change.value));if(change.id===null)draftSnapshot.rules=change.value?JSON.parse(JSON.stringify(change.value)):null;else if(change.value==null)delete draftSnapshot.yaml[change.id];else draftSnapshot.yaml[change.id]=JSON.parse(JSON.stringify(change.value))}
  draftStorageError='';renderDrafts();return true;
 }catch{draftStorageError=conflict?'另一个标签页已修改同一份草稿，当前编辑仍保留，尚未覆盖其它标签页。请保存或复制内容后处理。':'草稿尚未写入浏览器（存储被禁用、空间不足或旧草稿损坏）。请保存或复制内容后再刷新/关闭；当前内容仍保留。';renderDrafts();return false}
}
function yamlDirty(){return editorOpen&&$('config').value!==editorBase}
function captureYAMLDraft(){
 if(!editorOpen)return true;
 const key=editorProfileID||'new';
 if(yamlDirty())drafts.yaml[key]={content:$('config').value,base:editorBaseline,at:new Date().toISOString()};else delete drafts.yaml[key];
 const saved=persistDrafts();
 $('yamlDraftStatus').textContent=yamlDirty()?(saved?'未保存 · 草稿已保存在此浏览器':'未保存 · 浏览器草稿保存失败'):'';
 return saved;
}
function renderDrafts(){
 const entries=Object.entries(drafts.yaml);$('draftBox').hidden=!entries.length&&!drafts.rules&&!draftStorageError;
 $('draftSummary').textContent='浏览器草稿：'+entries.length+' 份 YAML'+(drafts.rules?'、自定义规则':'')+'。仅保存在此浏览器，可能含密钥；不会随完整备份导出。';
 $('draftStorageWarning').textContent=draftStorageError;
 const list=$('draftList');list.textContent='';
 for(const [id] of entries){const button=document.createElement('button');button.className='secondary';button.textContent='恢复'+(id==='new'?'粘贴草稿':('“'+(profileNames.get(id)||'未选中的配置')+'”草稿'));button.onclick=async()=>{if(await guardDrafts(['yaml'])){const d=drafts.yaml[id];if(d){showView('connectionView');openYAMLEditor(d.content,id==='new'?'':id,d)}}};list.appendChild(button)}
 if(drafts.rules){const button=document.createElement('button');button.className='secondary';button.textContent='恢复规则草稿';button.onclick=async()=>{if(await guardDrafts(['yaml'])){showView('rulesView');await loadRules()}};list.appendChild(button);
  for(const [index,form] of (drafts.rules.forms||[]).entries()){const resume=document.createElement('button');resume.className='secondary';resume.textContent='恢复规则表单草稿 '+(index+1);resume.onclick=async()=>{if(await guardDrafts(['yaml'])){showView('rulesView');await loadRules();await resumeRuleForm(form.rule.id)}};list.appendChild(resume)}
 }
}
function normalizeEditorText(content){return String(content).replace(/\r\n?/g,'\n')}
function openYAMLEditor(content,profileID='',restored=null){
 editorProfileID=profileID;editorOpen=true;editorBase=normalizeEditorText(content);editorBaseline=fingerprint(content);
 const draft=restored||drafts.yaml[profileID||'new'];
 if(draft){$('config').value=normalizeEditorText(draft.content);editorBaseline=draft.base;editorBase=restored?null:normalizeEditorText(content);$('yamlDraftStatus').textContent='已恢复草稿 · 尚未保存'+(profileID&&profileID!==activeProfileID?' · 请先切换到对应配置再保存':'')}
 else{$('config').value=normalizeEditorText(content);$('yamlDraftStatus').textContent=''}
 $('config').classList.add('visible');$('editorActions').style.display='flex';
 $('save').textContent=profileID?'校验并保存到当前配置':'校验并导入';$('config').focus();
}
function closeYAMLEditor(){
 if(yamlDirty())captureYAMLDraft();
 $('config').classList.remove('visible');$('editorActions').style.display='none';$('config').value='';$('yamlDraftStatus').textContent='';
 editorProfileID='';editorOpen=false;editorBase='';editorBaseline='';
}
async function importYAML(content){await api('/api/import',{method:'POST',body:JSON.stringify({content})})}
async function saveYAML(){
 if(!editorOpen)return true;
 const content=$('config').value,id=editorProfileID;
 captureYAMLDraft();
 if(id){
  const current=await api('/api/config');
  if(current.profileId!==id)throw new Error('当前配置已切换，草稿仍保留。请切回对应配置后保存。');
  if(normalizeEditorText(current.content)===content){clearSavedYAMLDraft(id);editorBase=content;closeYAMLEditor();toast('内容未改变，上一版保持不变');return true}
  if(fingerprint(current.content)!==editorBaseline&&!confirm('已保存的 YAML 在草稿编辑期间发生变化。仍用这份草稿覆盖它？'))return false;
  await api('/api/profiles',{method:'POST',body:JSON.stringify({action:'edit',id,content})});
  toast(state.running?'当前配置已更新，重连后生效':'当前配置已更新');
 }else{await importYAML(content);toast('配置已导入并选中')}
 clearSavedYAMLDraft(id||'new');editorBase=content;closeYAMLEditor();return true;
}
function clearSavedYAMLDraft(id){
 const key=draftStorageKey+':yaml:'+id;
 try{
  const raw=localStorage.getItem(key),remote=raw===null?undefined:JSON.parse(raw);
  if(JSON.stringify(remote)!==JSON.stringify(draftSnapshot.yaml[id])){if(remote){drafts.yaml[id]=remote;draftSnapshot.yaml[id]=JSON.parse(JSON.stringify(remote))}else{delete drafts.yaml[id];delete draftSnapshot.yaml[id]}draftStorageError='配置已保存；其它标签页的草稿已保留。';renderDrafts();return}
  delete drafts.yaml[id];if(!persistDrafts()){if(draftSnapshot.yaml[id])drafts.yaml[id]=JSON.parse(JSON.stringify(draftSnapshot.yaml[id]));else delete drafts.yaml[id];renderDrafts()}
 }catch{if(draftSnapshot.yaml[id])drafts.yaml[id]=JSON.parse(JSON.stringify(draftSnapshot.yaml[id]));renderDrafts()}
}
$('config').oninput=captureYAMLDraft;
$('paste').onclick=()=>{if(yamlDirty())return guardDrafts(['yaml']).then(ok=>{if(ok)openYAMLEditor('')});openYAMLEditor('')};
$('edit').onclick=()=>runGuardedProfileOperation(async()=>{const config=await api('/api/config');if(!config.profileId)throw new Error('请先导入或添加配置');openYAMLEditor(config.content,config.profileId);return false},undefined,['yaml']);
$('cancel').onclick=async()=>{if(await guardDrafts(['yaml']))closeYAMLEditor()};
$('save').onclick=()=>runProfileOperation(saveYAML);
function pendingDraftKeys(keys){return keys.filter(key=>key==='yaml'?yamlDirty():key==='form'?ruleFormDirty():rulesDirty||ruleFormDirty())}
function guardDrafts(keys=['yaml','rules']){
 if(draftDecisionResolve)return Promise.resolve(false);
 const dirty=pendingDraftKeys(keys);if(!dirty.length)return Promise.resolve(true);
 draftDecisionKeys=dirty;$('draftDecisionMessage').textContent=''+dirty.map(key=>key==='yaml'?'YAML 编辑':key==='form'?'当前规则表单':'自定义规则（含编辑表单）').join('、')+'尚未保存。选择保留草稿后可以继续，稍后再恢复编辑。';$('draftDecisionError').textContent='';
 return new Promise(resolve=>{draftDecisionResolve=resolve;$('draftDecision').showModal()});
}
function finishDraftDecision(proceed){const resolve=draftDecisionResolve;draftDecisionResolve=null;$('draftDecision').close();if(resolve)resolve(proceed)}
async function decideDrafts(choice){
 if(!draftDecisionResolve||draftDecisionBusy)return;
 if(choice==='cancel'){finishDraftDecision(false);return}
 draftDecisionBusy=true;const buttons=['draftSave','draftKeep','draftDiscard','draftCancel'];buttons.forEach(id=>$(id).disabled=true);
 try{
  if(choice==='save'){
   if(profileBusy||state.busy||rulesLoading)throw new Error('当前正在处理操作，请稍后再保存。');
   for(const key of draftDecisionKeys){if(key==='yaml'){if(!await saveYAML())return}else if(!await saveRulesDraft())return}
   await loadProfiles();await refreshState();
  }else if(choice==='keep'){
   let saved=true;for(const key of draftDecisionKeys)saved=(key==='yaml'?captureYAMLDraft():captureRulesDraft())&&saved;
   if(!saved)throw new Error('草稿未能写入浏览器。请保存或复制内容，取消本次离开操作。');
  }else{
   const previous=JSON.parse(JSON.stringify(drafts));
   for(const key of draftDecisionKeys){if(key==='yaml')delete drafts.yaml[editorProfileID||'new'];else if(key==='form'){drafts.rules=(rulesDirty||parkedRuleForms.length)?{rules:JSON.parse(JSON.stringify(rules)),dirty:rulesDirty,base:rulesBaseline,forms:JSON.parse(JSON.stringify(parkedRuleForms)),form:null}:null}else drafts.rules=null}
   if(!persistDrafts()){drafts=previous;renderDrafts();throw new Error('无法清除已保存的浏览器草稿，修改仍保留。请恢复存储后重试，或取消。')}
   for(const key of draftDecisionKeys){if(key==='yaml'){editorBase=$('config').value;closeYAMLEditor()}else if(key==='form'){hideRuleForm();renderRules()}else{rules=JSON.parse(JSON.stringify(savedRules));rulesDirty=false;hideRuleForm();parkedRuleForms=[];rulesGeneration++;renderRules()}}
  }
  finishDraftDecision(true);
 }catch(error){$('draftDecisionError').textContent=error.message}
 finally{draftDecisionBusy=false;buttons.forEach(id=>$(id).disabled=false)}
}
$('draftSave').onclick=()=>decideDrafts('save');$('draftKeep').onclick=()=>decideDrafts('keep');$('draftDiscard').onclick=()=>decideDrafts('discard');$('draftCancel').onclick=()=>decideDrafts('cancel');
$('draftDecision').addEventListener?.('cancel',event=>{event.preventDefault();decideDrafts('cancel')});
globalThis.addEventListener?.('beforeunload',event=>{if(yamlDirty()||overrideDirty()||rulesDirty||ruleFormDirty()||profileBusy||rulesLoading){captureYAMLDraft();captureRulesDraft();event.preventDefault();event.returnValue=''}});
globalThis.addEventListener?.('pagehide',()=>{captureYAMLDraft();captureRulesDraft()});
function runGuardedProfileOperation(action,onError,keys=['yaml','rules']){if(!pendingDraftKeys(keys).length)return runProfileOperation(action,onError);return guardDrafts(keys).then(ok=>ok?runProfileOperation(action,onError):false)}
function showView(view){activeView=view;globalThis.scrollTo?.({top:0,behavior:'instant'});document.querySelectorAll('.tab').forEach(t=>t.classList.toggle('active',t.dataset.view===view));document.querySelectorAll('.view').forEach(v=>v.classList.toggle('active',v.id===view))}

async function copy(text){await navigator.clipboard.writeText(text);toast('已复制')}
$('copySecret').onclick=async()=>{try{const r=await api('/api/panel-secret');await copy(r.secret)}catch(e){toast(e.message)}};$('copyPorts').onclick=()=>copy('127.0.0.1:'+state.mixedPort);

let rules=[];let policies=['DIRECT','REJECT'];let unifiedPolicy='';let rulesDirty=false;let rulesLoading=false;let editingIndex=-1;let selectedRules=new Set();
let savedRules=[],rulesLoaded=false,rulesGeneration=0,ruleFormBase='',rulesBaseline='',parkedRuleForms=[];
function readRuleForm(){return {id:$('ruleID').value,type:$('ruleType').value,value:$('ruleValue').value,note:$('ruleNote').value,policy:$('rulePolicy').value,enabled:$('ruleEnabled').checked,noResolve:$('ruleNoResolve').checked}}
function ruleFormDirty(){return $('ruleForm').style.display==='block'&&JSON.stringify(readRuleForm())!==ruleFormBase}
function hideRuleForm(){$('ruleForm').style.display='none';$('ruleError').textContent='';ruleFormBase=''}
function currentRuleFormDraft(){return {rule:readRuleForm(),editingID:editingIndex>=0?$('ruleID').value:'',base:ruleFormBase}}
function parkRuleForm(){if(ruleFormDirty()){const form=currentRuleFormDraft();parkedRuleForms=parkedRuleForms.filter(item=>item.rule.id!==form.rule.id);parkedRuleForms.push(form)}}
function captureRulesDraft(){
 if(!rulesLoaded)return !rulesDirty&&!ruleFormDirty();
 if(rulesDirty||ruleFormDirty()||parkedRuleForms.length)drafts.rules={rules:JSON.parse(JSON.stringify(rules)),dirty:rulesDirty,base:rulesBaseline,forms:JSON.parse(JSON.stringify(parkedRuleForms)),form:ruleFormDirty()?currentRuleFormDraft():null};
 else drafts.rules=null;
 const saved=persistDrafts();if(!saved)$('rulesError').textContent=draftStorageError;return saved;
}
function ruleFormInput(){rulesGeneration++;const saved=captureRulesDraft();$('ruleSaveState').textContent=saved?'规则表单有未保存的修改':'未保存 · 浏览器草稿保存失败';renderRules()}
function restoreRuleForm(form){
 const index=form.editingID?rules.findIndex(rule=>rule.id===form.editingID):-1;
 showRuleForm(index);const rule=form.rule;
 for(const [field,id] of [['id','ruleID'],['type','ruleType'],['value','ruleValue'],['note','ruleNote']])$(id).value=String(rule[field]||'');
 const policy=String(rule.policy||'');if(![...$('rulePolicy').children].some(option=>option.value===policy)){const option=document.createElement('option');option.value=policy;option.textContent=policy;$('rulePolicy').appendChild(option)}
 $('rulePolicy').value=policy;$('ruleEnabled').checked=rule.enabled!==false;$('ruleNoResolve').checked=!!rule.noResolve;ruleFormBase=form.base||'';updateRuleHelp();
}
async function requestRuleForm(target=null){if(!rulesLoaded||rulesLoading)return;const id=typeof target==='number'?(target>=0?rules[target]?.id:null):target;if(id&&parkedRuleForms.some(item=>item.rule.id===id))return resumeRuleForm(id);if(ruleFormDirty()){if(!await guardDrafts(['form']))return;parkRuleForm()}const index=id?rules.findIndex(rule=>rule.id===id):-1;if(id&&index<0){toast('这条规则已不在当前列表中');return}showRuleForm(index);captureRulesDraft()}
async function resumeRuleForm(id){const form=parkedRuleForms.find(item=>item.rule.id===id);if(!form)return;if(ruleFormDirty()&&!await guardDrafts(['form']))return;parkedRuleForms=parkedRuleForms.filter(item=>item.rule.id!==id);parkRuleForm();restoreRuleForm(form);captureRulesDraft()}
function commitRuleForm(){
 if(!rulesLoaded||rulesLoading)return false;
 const raw=readRuleForm(),value=raw.value.trim(),policy=raw.policy;
 if(!value){$('ruleError').textContent='请填写匹配内容';return false}if(!policy){$('ruleError').textContent='请选择目标策略';return false}
 const rule={...raw,id:raw.id||newRuleID(),value,note:raw.note.trim()};
 const index=rules.findIndex(item=>item.id===raw.id);
 if(editingIndex>=0&&index>=0)rules[index]=rule;else rules.push(rule);
 parkedRuleForms=parkedRuleForms.filter(item=>item.rule.id!==raw.id);hideRuleForm();markRulesDirty();return true;
}
async function saveRulesDraft(){
 if(!rulesLoaded||rulesLoading||state.busy)return false;
 if(ruleFormDirty()&&!commitRuleForm())throw new Error($('ruleError').textContent);
 if(!rulesDirty)return true;
 captureRulesDraft();rulesLoading=true;renderRules();$('saveRules').textContent='校验中…';$('rulesError').textContent='';$('ruleSaveState').textContent='正在校验并保存…';
 const generation=rulesGeneration,payload=JSON.parse(JSON.stringify(rules));
 try{
  const current=await api('/api/rules');
  if(rulesBaseline&&fingerprint(JSON.stringify(current.rules||[]))!==rulesBaseline&&!confirm('已保存的规则在草稿编辑期间发生变化。仍用当前草稿覆盖它们？'))return false;
  const result=await api('/api/rules',{method:'PUT',body:JSON.stringify({rules:payload})});
  invalidateRuleTest();savedRules=payload;rulesBaseline=fingerprint(JSON.stringify(payload));
  if(generation===rulesGeneration)rulesDirty=false;captureRulesDraft();
  $('ruleSaveState').textContent=rulesDirty?'新修改仍保留为草稿':(result.applied?'已保存并热重载，规则现已生效':'已保存，下次连接时生效');
  toast(result.applied?'自定义规则已生效':'自定义规则已保存');return !rulesDirty;
 }catch(error){$('rulesError').textContent=error.message;$('ruleSaveState').textContent='保存失败 · 草稿保留，可重试';captureRulesDraft();throw error}
 finally{rulesLoading=false;$('saveRules').textContent='保存并应用';renderRules()}
}
const ruleLabels={'DOMAIN-SUFFIX':'域名后缀','DOMAIN':'完整域名','DOMAIN-KEYWORD':'域名关键词','IP-CIDR':'IPv4 网段','IP-CIDR6':'IPv6 网段','PROCESS-NAME':'进程名','PROCESS-PATH':'进程路径','GEOIP':'GEOIP 地区','GEOSITE':'GEOSITE 分类'};
const escapeHTML=value=>String(value).replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
async function navigateView(view){if(view===activeView)return true;if(!await guardDrafts([activeView==='rulesView'?'rules':'yaml']))return false;showView(view);if(activeView==='rulesView')await loadRules();if(activeView==='overridesView')await openOverrides();return true}
document.querySelectorAll('.tab').forEach(tab=>tab.onclick=()=>navigateView(tab.dataset.view));
function newRuleID(){return crypto.randomUUID?crypto.randomUUID():'rule-'+Date.now()+'-'+Math.random().toString(16).slice(2)}
function markRulesDirty(){rulesDirty=true;rulesGeneration++;const saved=captureRulesDraft();$('saveRules').disabled=false;$('ruleSaveState').textContent=saved?'有未保存的更改 · 草稿保留':'未保存 · 浏览器草稿保存失败';$('ruleSaveState').className='save-state';renderRules()}
function renderRules(){
 const list=$('rulesList');list.textContent='';
 if(!rules.length){const empty=document.createElement('div');empty.className='empty';empty.textContent='还没有自定义规则。点击“添加规则”，例如将某个域名设为直连或拦截。';list.appendChild(empty)}
 rules.forEach((rule,index)=>{
  const row=document.createElement('div');row.className='rule-row '+(rule.enabled?'':'disabled');
  const check=document.createElement('input');check.type='checkbox';check.checked=selectedRules.has(rule.id);check.title='选择这条规则';check.onchange=()=>{if(check.checked)selectedRules.add(rule.id);else selectedRules.delete(rule.id);renderBulk()};
  const type=document.createElement('div');type.className='rule-type';type.textContent=ruleLabels[rule.type]||rule.type;
  const value=document.createElement('div');value.className='rule-value';value.textContent=rule.value;if(rule.note){const note=document.createElement('span');note.className='rule-note';note.textContent=rule.note;value.appendChild(note)}
  const policy=document.createElement('div');policy.className='rule-policy';policy.textContent=rule.policy;
  const actions=document.createElement('div');actions.className='rule-actions';
  const button=(text,title,fn,disabled=false)=>{const b=document.createElement('button');b.className='icon-button';b.textContent=text;b.title=title;b.disabled=disabled||rulesLoading;b.onclick=fn;actions.appendChild(b)};
  button('↑','上移',()=>{if(index>0){[rules[index-1],rules[index]]=[rules[index],rules[index-1]];markRulesDirty()}},index===0);
  button('↓','下移',()=>{if(index<rules.length-1){[rules[index+1],rules[index]]=[rules[index],rules[index+1]];markRulesDirty()}},index===rules.length-1);
  button(rule.enabled?'停用':'启用',rule.enabled?'停用':'启用',()=>{rule.enabled=!rule.enabled;markRulesDirty()});
  button('编辑','编辑',()=>requestRuleForm(rule.id));
  const del=document.createElement('button');del.className='icon-button delete';del.textContent='删除';del.disabled=rulesLoading;del.onclick=()=>{if(confirm('删除这条自定义规则？')){rules.splice(index,1);selectedRules.delete(rule.id);markRulesDirty()}};actions.appendChild(del);
  row.append(check,type,value,policy,actions);list.appendChild(row);
 });
 const enabledCount=rules.filter(r=>r.enabled).length;
 $('ruleBadge').textContent=enabledCount?'('+enabledCount+')':'';
 $('ruleSummary').textContent=rules.length?rules.length+' 条 · 启用 '+enabledCount+' · 停用 '+(rules.length-enabledCount)+' · 优先于原配置':'优先于原配置中的规则';
 $('saveRules').disabled=!rulesLoaded||(!rulesDirty&&!ruleFormDirty())||rulesLoading||state.busy;
 for(const id of ['addRule','importRules','confirmRule','cancelRule','ruleType','ruleValue','ruleNote','rulePolicy','ruleEnabled','ruleNoResolve'])$(id).disabled=!rulesLoaded||rulesLoading||!!state.busy;
 renderBulk();
}
function renderBulk(){
 const sel=rules.filter(r=>selectedRules.has(r.id));
 $('ruleBulk').style.display=rules.length?'flex':'none';
 $('ruleSelectedCount').textContent=sel.length?'已选 '+sel.length+' 条':'';
 $('ruleSelectAll').checked=rules.length>0&&sel.length===rules.length;
 $('bulkEnable').disabled=!sel.length||rulesLoading||state.busy;
 $('bulkDisable').disabled=!sel.length||rulesLoading||state.busy;
 $('bulkDelete').disabled=!sel.length||rulesLoading||state.busy;
}
async function loadRules(){if(rulesLoading)return;rulesLoading=true;const generation=rulesGeneration;renderRules();$('rulesError').textContent='';try{const data=await api('/api/rules');policies=data.policies||['DIRECT','REJECT'];unifiedPolicy=data.unifiedPolicy||'';if(!rulesLoaded||rulesBaseline!==fingerprint(JSON.stringify(data.rules||[])))invalidateRuleTest();if(generation!==rulesGeneration||rulesDirty||ruleFormDirty())return;const cleanFormID=$('ruleForm').style.display==='block'&&!ruleFormDirty()?$('ruleID').value:null;savedRules=JSON.parse(JSON.stringify(data.rules||[]));rules=savedRules.map(rule=>({...rule}));rulesBaseline=fingerprint(JSON.stringify(savedRules));if(!rulesLoaded&&drafts.rules){const draft=drafts.rules;rules=JSON.parse(JSON.stringify(draft.dirty?draft.rules:savedRules));rulesDirty=!!draft.dirty;rulesBaseline=draft.base||rulesBaseline;rulesLoaded=true;parkedRuleForms=Array.isArray(draft.forms)?draft.forms:[];if(draft.form)restoreRuleForm(draft.form);$('ruleSaveState').textContent='已恢复规则草稿 · 尚未保存'}else{rulesDirty=false;rulesLoaded=true;$('ruleSaveState').textContent=data.running?'已加载 · 保存后热重载':'已加载 · 下次连接时生效'}if(cleanFormID&&!ruleFormDirty()){const index=rules.findIndex(rule=>rule.id===cleanFormID);if(index>=0)showRuleForm(index);else hideRuleForm()}selectedRules=new Set([...selectedRules].filter(id=>rules.some(rule=>rule.id===id)));renderRules()}catch(e){$('rulesError').textContent=e.message}finally{rulesLoading=false;renderRules()}}
function updateRuleHelp(){const type=$('ruleType').value;const help={DOMAIN:'只匹配这个完整域名，例如 www.example.com。','DOMAIN-SUFFIX':'匹配 example.com 及其所有子域名，不带 http:// 或路径。','DOMAIN-KEYWORD':'域名包含关键词，例如 example。模拟器先将域名转为 IDNA/ASCII host，Unicode 原文关键词不与转换前文本比较；不代表内核嗅探后的元数据。','IP-CIDR':'填写 IPv4 网段，例如 192.168.0.0/16。','IP-CIDR6':'填写 IPv6 网段，例如 2001:db8::/32。','PROCESS-NAME':'填写进程文件名，例如 chrome.exe；无需 TUN。','PROCESS-PATH':'填写完整路径，例如 C:\\Program Files\\App\\app.exe。','GEOIP':'按 IP 归属地匹配，填写国家/地区代码，例如 CN、US、JP，或 PRIVATE；需要 geoip 数据库（已随包具备）。','GEOSITE':'按域名分类匹配，填写分类名，例如 cn、geolocation-cn、category-ads-all；需要 geosite 数据库。'};$('ruleHelp').textContent=help[type];$('ruleValue').placeholder={DOMAIN:'www.example.com','DOMAIN-SUFFIX':'example.com','DOMAIN-KEYWORD':'example','IP-CIDR':'192.168.0.0/16','IP-CIDR6':'2001:db8::/32','PROCESS-NAME':'chrome.exe','PROCESS-PATH':'C:\\Program Files\\App\\app.exe'}[type];$('noResolveWrap').style.display=(type==='IP-CIDR'||type==='IP-CIDR6')?'flex':'none'}
function showRuleForm(index=-1){editingIndex=index;const r=index>=0?rules[index]:{type:'DOMAIN-SUFFIX',value:'',policy:policies.includes(unifiedPolicy)?unifiedPolicy:'',enabled:true,note:'',noResolve:false};$('formTitle').textContent=index>=0?'编辑规则':'添加规则';$('ruleID').value=r.id||newRuleID();$('ruleType').value=r.type;$('ruleValue').value=r.value;$('ruleNote').value=r.note||'';$('ruleEnabled').checked=r.enabled!==false;$('ruleNoResolve').checked=!!r.noResolve;const select=$('rulePolicy');select.textContent='';[...new Set([...policies,r.policy].filter(Boolean))].forEach(name=>{const option=document.createElement('option');option.value=name;option.textContent=name;select.appendChild(option)});select.value=r.policy;if(select.selectedIndex<0)select.value='';$('ruleError').textContent='';updateRuleHelp();$('ruleForm').style.display='block';ruleFormBase=JSON.stringify(readRuleForm());$('ruleValue').focus()}
$('addRule').onclick=()=>requestRuleForm();$('ruleType').onchange=()=>{updateRuleHelp();ruleFormInput()};
 for(const id of ['ruleValue','ruleNote','rulePolicy','ruleEnabled','ruleNoResolve']){$(id).oninput=ruleFormInput;$(id).onchange=ruleFormInput}
 $('cancelRule').onclick=async()=>{if(!ruleFormDirty()||await guardDrafts(['form'])){parkRuleForm();hideRuleForm();captureRulesDraft();renderRules()}};
$('confirmRule').onclick=commitRuleForm;
$('saveRules').onclick=async()=>{try{return await saveRulesDraft()}catch{return false}};

$('ruleSelectAll').onchange=event=>{
 if(event.target.checked)selectedRules=new Set(rules.map(rule=>rule.id));
 else selectedRules.clear();
 renderRules();
};
$('bulkEnable').onclick=()=>{rules.forEach(r=>{if(selectedRules.has(r.id))r.enabled=true});markRulesDirty();toast('已启用选中规则')};
$('bulkDisable').onclick=()=>{rules.forEach(r=>{if(selectedRules.has(r.id))r.enabled=false});markRulesDirty();toast('已停用选中规则')};
$('bulkDelete').onclick=()=>{if(selectedRules.size&&confirm('删除选中的 '+selectedRules.size+' 条规则？')){rules=rules.filter(r=>!selectedRules.has(r.id));selectedRules.clear();markRulesDirty();toast('已删除选中规则')}};
$('exportRules').onclick=()=>{if(!rules.length){toast('没有可导出的规则');return}const blob=new Blob([JSON.stringify(rules,null,2)],{type:'application/json'});const a=document.createElement('a');a.href=URL.createObjectURL(blob);a.download='mihomolite-custom-rules.json';a.click();URL.revokeObjectURL(a.href);toast('已导出 '+rules.length+' 条规则')};
$('importRules').onclick=()=>$('importRulesFile').click();
$('importRulesFile').onchange=async e=>{const file=e.target.files[0];if(!file||!rulesLoaded||rulesLoading)return;const overwrite=confirm('点“确定”覆盖现有规则，点“取消”合并到现有规则');try{const data=JSON.parse(await file.text());if(!Array.isArray(data))throw new Error('JSON 顶层必须是规则数组');if(!rulesLoaded||rulesLoading)throw new Error('规则尚未加载或正在保存，请稍后重新导入');const clean=data.map(x=>({id:String(x.id||newRuleID()),type:String(x.type||''),value:String(x.value||''),policy:String(x.policy||''),enabled:x.enabled!==false,note:String(x.note||''),noResolve:!!x.noResolve}));rules=overwrite?clean:rules.concat(clean);selectedRules.clear();markRulesDirty();toast('已导入 '+clean.length+' 条，记得保存并应用')}catch(err){toast('导入失败：'+(err.message||err))}finally{e.target.value=''}};
let ruleTestGeneration=0;
function invalidateRuleTest(){ruleTestGeneration++;$('ruleTestResult').textContent='';$('ruleTestButton').disabled=false}
$('ruleTestKind').onchange=()=>{invalidateRuleTest();$('ruleTestInput').placeholder=$('ruleTestKind').value==='process'?'输入进程名或完整路径，如 app.exe':'输入域名或 IP，如 www.example.com'};
$('ruleTestInput').oninput=invalidateRuleTest;
$('ruleTestButton').onclick=async()=>{
 const q=$('ruleTestInput').value.trim(),kind=$('ruleTestKind').value||'destination',generation=++ruleTestGeneration;
 if(!q){$('ruleTestResult').textContent='请输入内容';return}
 $('ruleTestResult').textContent='正在模拟已保存规则…';$('ruleTestResult').style.color='#8a95a5';$('ruleTestButton').disabled=true;
 try{
  const r=await api('/api/rules/test?q='+encodeURIComponent(q)+'&kind='+encodeURIComponent(kind));
  if(generation!==ruleTestGeneration)return;
  if(r.status==='matched'&&r.matched){
   $('ruleTestResult').textContent='按所提供信息模拟命中（非内核结果）：'+r.matched.type+' · '+r.matched.value+' → '+r.matched.policy;
   $('ruleTestResult').style.color='#4ade80';
  }else if(r.status==='unknown'){
   let detail='无法模拟确定结果：规则包含未模拟条件或缺少连接元信息（'+(r.uncertainTypes||[]).join('、')+'）。';
   if(r.possibleMatch)detail+=' 忽略这些不确定条件时，后续可能命中 '+r.possibleMatch.type+' · '+r.possibleMatch.value+' → '+r.possibleMatch.policy+'。';
   if(r.missingDatabases?.length)detail+=' 本机未发现 '+r.missingDatabases.join('、')+' 常见非空数据库文件；请补齐对应公开 Geo 数据库后用内核实际验证。';
   detail+=' 本测试不执行 DNS/Geo 查询，也不验证真实进程归属或路由模式。';
   $('ruleTestResult').textContent=detail;$('ruleTestResult').style.color='#f59e0b';
  }else if(r.status==='no-match'){
   $('ruleTestResult').textContent='在所提供信息的已保存自定义规则范围内模拟未命中；不代表原 YAML 规则链或内核最终结果。';
   $('ruleTestResult').style.color='#f59e0b';
  }else{throw new Error('规则模拟返回了无法识别的状态，请重试')}
 }catch(e){if(generation===ruleTestGeneration){$('ruleTestResult').textContent=e.message;$('ruleTestResult').style.color='#f87171'}}
 finally{if(generation===ruleTestGeneration)$('ruleTestButton').disabled=false}
};

let profileBusy=false;
let profileListRequest=0;
let stateRequest=0;
function syncProfileControls(){
 const disabled=profileBusy||!!state.busy;
 const list=$('profileList');
 list.style.opacity=profileBusy?'.55':'1';
 list.querySelectorAll('.profile-buttons button, .subscription-schedule select').forEach(control=>control.disabled=disabled);
 for(const id of ['choose','paste','addSubscription','confirmSubscription','save','cancel','cancelSubscription','exportBackup','importBackup','confirmSubscriptionPolicy','cancelSubscriptionPolicy','subscriptionName','subscriptionURL','subscriptionUA','subscriptionConditional','subscriptionBackoff','subscriptionPolicyMode','subscriptionPolicyUA','subscriptionPolicyConditional','subscriptionPolicyBackoff']) $(id).disabled=disabled;
 document.querySelectorAll('input[name="downloadMode"]').forEach(control=>control.disabled=disabled);
 $('importBackup').disabled=disabled||!!state.running;
 $('importBackup').title=state.running?'请先断开连接再还原':'';
 $('draftList').querySelectorAll('button').forEach(button=>button.disabled=disabled);
 $('edit').disabled=disabled||!state.profileCount;
 $('config').readOnly=profileBusy;
}
async function runProfileOperation(action,onError=error=>toast(error.message)){
 if(profileBusy||state.busy)return;
 profileBusy=true;
 syncProfileControls();
 try{
  if(await action()!==false)await loadProfiles();
 }catch(error){
  onError(error);
  await loadProfiles();
 }finally{
  profileBusy=false;
  syncProfileControls();
  await refreshState();
 }
}
function formatTime(value){if(!value)return '未知';const d=new Date(value);return isNaN(d.getTime())?'未知':d.toLocaleString('zh-CN',{hour12:false})}
function validTrafficNumber(value){return Number.isSafeInteger(value)&&value>=0}
function formatTrafficBytes(value){if(!validTrafficNumber(value))return '未知';const units=['B','KiB','MiB','GiB','TiB'];let amount=value,index=0;while(amount>=1024&&index<units.length-1){amount/=1024;index++}return (index===0?String(amount):amount.toFixed(amount>=10?1:2))+' '+units[index]}
function validTrafficDate(value){
 if(typeof value!=='string'||!/^\d{4}-\d{2}-\d{2}$/.test(value))return false;
 const [year,month,day]=value.split('-').map(Number);
 if(year<1||year>9999)return false;
 const date=new Date(value+'T00:00:00Z');
 return date.getUTCFullYear()===year&&date.getUTCMonth()+1===month&&date.getUTCDate()===day;
}
function renderSubscriptionTraffic(info,profile){
 const traffic=profile.traffic||null,usage=document.createElement('div');usage.className='subscription-result';usage.dataset.kind='traffic';
 if(!traffic){usage.textContent='套餐流量 / 到期信息：未知（服务器未提供可识别的响应头，订阅节点也未标注可识别的套餐信息）';info.appendChild(usage);return}
 const upload=validTrafficNumber(traffic.upload)?traffic.upload:null,download=validTrafficNumber(traffic.download)?traffic.download:null,total=validTrafficNumber(traffic.total)?traffic.total:null;
 const sum=upload!==null&&download!==null?upload+download:null,used=validTrafficNumber(sum)?sum:(validTrafficNumber(traffic.used)?traffic.used:null);
 const remaining=validTrafficNumber(sum)&&total!==null?Math.max(0,total-sum):(validTrafficNumber(traffic.remaining)?traffic.remaining:null);
 const hasExpiry=validTrafficNumber(traffic.expire)&&traffic.expire>0&&traffic.expire<=253402300799;
 const expiry=hasExpiry?formatTime(traffic.expire*1000)+(traffic.expire*1000<=Date.now()?'（已过期）':''):(validTrafficDate(traffic.expireDate)?traffic.expireDate+'（日期提示，具体时刻未知）':'未知');
 const source={nodes:'节点提示（估算，GB按1024）',mixed:'响应头优先 + 节点提示（估算，GB按1024）'}[traffic.source]||'订阅响应头';
 usage.textContent='套餐：已用 '+formatTrafficBytes(used)+' / 总额 '+formatTrafficBytes(total)+' · 剩余 '+formatTrafficBytes(remaining)+' · 上传 '+formatTrafficBytes(upload)+' · 下载 '+formatTrafficBytes(download)+' · 到期 '+expiry+' · 来源 '+source+' · 信息读取 '+formatTime(traffic.checkedAt);
 info.appendChild(usage);
}
function renderSubscriptionStatus(info,profile){
 renderSubscriptionTraffic(info,profile);
 const status=document.createElement('div');
 status.className='subscription-result';
 const update=profile.lastUpdate;
 if(update){
  status.dataset.status=update.status;
  const outcome={success:'成功',failed:'失败',unchanged:'无变化',saved:'配置已保存'}[update.status]||'已完成';
  status.textContent=(update.auto?'自动更新':'手动更新')+' · '+formatTime(update.at)+' · '+outcome+(update.message?'：'+update.message:'');
 }else{
  status.textContent='暂无订阅更新记录';
 }
 const schedule=document.createElement('label');
 schedule.className='subscription-schedule';
 const caption=document.createElement('span');
 caption.textContent='自动更新';
 schedule.appendChild(caption);
 const select=document.createElement('select');
 select.setAttribute('aria-label',profile.name+'的自动更新频率');
 for(const [hours,label] of [[0,'关闭'],[6,'每 6 小时'],[12,'每 12 小时'],[24,'每天'],[168,'每周']]){
  const option=document.createElement('option');
  option.value=String(hours);
  option.textContent=label;
  select.appendChild(option);
 }
 select.value=String(profile.autoUpdateHours||0);
 select.onchange=()=>runProfileOperation(async()=>{
  await profileAction('auto-update',profile.id,'','direct',{hours:Number(select.value)});
  toast('自动更新设置已保存');
 });
 schedule.appendChild(select);
 if(profile.nextUpdateAt){
  const next=document.createElement('span');
  next.textContent=(profile.updateRetry?.retryAt?'退避重试 ':'下次更新 ')+formatTime(profile.nextUpdateAt);
  schedule.appendChild(next);
 }
 const policy=effectiveSubscriptionPolicy(profile),note=document.createElement('span');note.className='subscription-policy-note';
 note.textContent='下载 '+({auto:'自动（推荐）',direct:'直连',proxy:'当前代理'}[policy.mode]||'自动（推荐）')+(subscriptionHasAdvancedOptions(policy)?' · 已设置高级选项':'');schedule.appendChild(note);
 if(profile.updateRetry?.consecutiveFailures){
  const retry=document.createElement('span');
  retry.textContent='连续自动更新失败 '+profile.updateRetry.consecutiveFailures+' 次';
  schedule.appendChild(retry);
 }
 info.append(status,schedule);
}

function renderSubscriptionDiff(before,after){
 const oldLines=before.split('\n');
 const newLines=after.split('\n');
 let start=0;
 while(start<oldLines.length&&start<newLines.length&&oldLines[start]===newLines[start])start++;
 let oldEnd=oldLines.length,newEnd=newLines.length;
 while(oldEnd>start&&newEnd>start&&oldLines[oldEnd-1]===newLines[newEnd-1]){oldEnd--;newEnd--}
 const oldCount=oldEnd-start,newCount=newEnd-start;
 if(oldLines.length+newLines.length>8000||(oldCount+1)*(newCount+1)>500000){
  $('subscriptionBefore').textContent=before;
  $('subscriptionAfter').textContent=after;
  return false;
 }
 const width=newCount+1;
 const matches=new Uint16Array((oldCount+1)*width);
 for(let i=oldCount-1;i>=0;i--){
  for(let j=newCount-1;j>=0;j--){
   matches[i*width+j]=oldLines[start+i]===newLines[start+j]
    ?matches[(i+1)*width+j+1]+1
    :Math.max(matches[(i+1)*width+j],matches[i*width+j+1]);
  }
 }
 const removed=new Set(),added=new Set();
 let i=0,j=0;
 while(i<oldCount||j<newCount){
  if(i<oldCount&&j<newCount&&oldLines[start+i]===newLines[start+j]){i++;j++}
  else if(i<oldCount&&(j===newCount||matches[(i+1)*width+j]>=matches[i*width+j+1]))removed.add(start+i++);
  else added.add(start+j++);
 }
 function renderSide(id,lines,changed,className){
  const target=$(id);
  target.textContent='';
  const content=document.createDocumentFragment();
  lines.forEach((line,index)=>{
   const row=document.createElement('span');
   row.className=changed.has(index)?'diff-line '+className:'diff-line';
   row.textContent=line||' ';
   content.appendChild(row);
  });
  target.appendChild(content);
 }
 renderSide('subscriptionBefore',oldLines,removed,'diff-removed');
 renderSide('subscriptionAfter',newLines,added,'diff-added');
 return true;
}

let diffRequest=0;
async function showSubscriptionDiff(profile){
 const request=++diffRequest;
 const dialog=$('subscriptionDiff');
 const status=$('subscriptionDiffStatus');
 const content=$('subscriptionDiffContent');
 $('subscriptionDiffTitle').textContent=profile.name+' · 订阅更新对比';
 status.textContent='正在读取更新前后配置…';
 content.hidden=true;
 if(!dialog.open)dialog.showModal();
 try{
  const diff=await api('/api/profiles/'+encodeURIComponent(profile.id)+'/update-diff');
  if(request!==diffRequest||!dialog.open)return;
  const highlighted=renderSubscriptionDiff(diff.before,diff.after);
  status.textContent=formatTime(diff.at)+(highlighted?' · 红色为删除，绿色为新增':' · 配置较长，显示完整的更新前后 YAML');
  content.hidden=false;
 }catch(error){
  if(request===diffRequest&&dialog.open)status.textContent=error.message;
 }
}
$('subscriptionDiff').addEventListener?.('close',()=>{
 diffRequest++;
 $('subscriptionBefore').textContent='';
 $('subscriptionAfter').textContent='';
});

function effectiveSubscriptionPolicy(profile){const policy=profile?.updatePolicy||{};return {mode:['auto','direct','proxy'].includes(policy.mode)?policy.mode:'auto',userAgent:typeof policy.userAgent==='string'?policy.userAgent:'',conditional:policy.conditional!==false,failureBackoff:policy.failureBackoff!==false}}
function subscriptionHasAdvancedOptions(policy){return !!(policy.userAgent&&policy.userAgent!=='clash-verge/v2.2.3')||!policy.conditional||!policy.failureBackoff}
function populateSubscriptionPolicy(prefix,policy){$(prefix+'UA').value=policy.userAgent;$(prefix+'Conditional').checked=policy.conditional;$(prefix+'Backoff').checked=policy.failureBackoff;$(prefix+'Advanced').open=false;$(prefix+'AdvancedSummary').textContent=subscriptionHasAdvancedOptions(policy)?'高级选项 · 已设置高级选项':'高级选项'}
function selectedDownloadMode(){return document.querySelector('input[name="downloadMode"]:checked')?.value||'auto'}
function readSubscriptionPolicy(prefix){
 const mode=prefix==='subscription'?selectedDownloadMode():$('subscriptionPolicyMode').value;
 const rawUA=$(prefix+'UA').value||'';
 if(!['auto','direct','proxy'].includes(mode))throw new Error('请选择有效的下载方式');
 if(/[^\x20-\x7e]/.test(rawUA)||rawUA.trim().length>512)throw new Error('UA 必须是最多 512 字节的可打印 ASCII');
 return {mode,userAgent:rawUA.trim(),conditional:$(prefix+'Conditional').checked!==false,failureBackoff:$(prefix+'Backoff').checked!==false};
}
let subscriptionPolicyEditingID='';
function showSubscriptionPolicyForm(profile){closeSubscriptionForm();subscriptionPolicyEditingID=profile.id;const policy=effectiveSubscriptionPolicy(profile);$('subscriptionPolicyTitle').textContent='订阅下载设置 · '+profile.name;$('subscriptionPolicyMode').value=policy.mode;populateSubscriptionPolicy('subscriptionPolicy',policy);$('subscriptionPolicyError').textContent='';$('subscriptionPolicyForm').style.display='block';$('subscriptionPolicyMode').focus()}
function closeSubscriptionPolicyForm(){subscriptionPolicyEditingID='';$('subscriptionPolicyForm').style.display='none';$('subscriptionPolicyUA').value='';$('subscriptionPolicyAdvanced').open=false;$('subscriptionPolicyAdvancedSummary').textContent='高级选项';$('confirmSubscriptionPolicy').textContent='保存设置'}
$('cancelSubscriptionPolicy').onclick=closeSubscriptionPolicyForm;
$('confirmSubscriptionPolicy').onclick=()=>runProfileOperation(async()=>{
 const id=subscriptionPolicyEditingID;$('subscriptionPolicyError').textContent='';if(!id)throw new Error('请先选择要编辑的订阅');const policy=readSubscriptionPolicy('subscriptionPolicy');$('confirmSubscriptionPolicy').textContent='保存中…';
 try{await profileAction('update-policy',id,'','',{policy});closeSubscriptionPolicyForm();toast('订阅下载设置已保存；下次更新生效')}finally{if($('subscriptionPolicyForm').style.display==='block')$('confirmSubscriptionPolicy').textContent='保存设置'}
},error=>{$('subscriptionPolicyError').textContent=error.message});
async function loadProfiles(){
 const request=++profileListRequest;
 try{const data=await api('/api/profiles');if(request!==profileListRequest)return;profileNames=new Map((data.profiles||[]).map(profile=>[profile.id,profile.name]));activeProfileID=data.activeId||(data.profiles||[]).find(profile=>profile.active)?.id||'';renderDrafts();const list=$('profileList');list.textContent='';$('profileNotice').classList.toggle('visible',!!data.pending);
 if(!data.profiles?.length){const empty=document.createElement('div');empty.className='empty';empty.textContent='还没有配置。添加订阅 URL，或导入本地 YAML。';list.appendChild(empty);return}
 for(const p of data.profiles){
  const card=document.createElement('div');card.className='profile-card'+(p.active?' active':'')+(p.source==='subscription'?' subscription-card':'');
  const info=document.createElement('div');const title=document.createElement('div');title.className='profile-name';title.textContent=p.name;
  const type=document.createElement('span');type.className='badge';type.textContent=p.source==='subscription'?'订阅':'本地 YAML';title.appendChild(type);
  if(p.active){const active=document.createElement('span');active.className='badge active';active.textContent='当前配置';title.appendChild(active)}
  if(p.pending){const pending=document.createElement('span');pending.className='badge pending';pending.textContent='重连后生效';title.appendChild(pending)}
  const meta=document.createElement('div');meta.className='profile-meta';meta.textContent=(p.nodes?p.nodes+' 个节点/提供器 · ':'')+'更新于 '+formatTime(p.updatedAt)+' · '+(p.size/1024).toFixed(1)+' KiB';info.append(title,meta);
  const buttons=document.createElement('div');buttons.className='profile-buttons';
  const addButton=(text,action,primary=false,protect=false)=>{
   const b=document.createElement('button');
   b.className=primary?'primary':'secondary';b.textContent=text;
   b.onclick=()=>protect?runGuardedProfileOperation(action):runProfileOperation(action);
   buttons.appendChild(b);
  };
  if(!p.active)addButton('切换',async()=>{await profileAction('switch',p.id);closeYAMLEditor()},true,true);
  if(p.source==='subscription'){
   renderSubscriptionStatus(info,p);
   addButton(p.lastUpdate?.status==='failed'?'重试更新':'更新',async()=>{
    const response=await profileAction('update',p.id);
    const result=response.profiles?.find(profile=>profile.id===p.id)?.lastUpdate;
    toast(result?.message||'订阅已更新');
   },false,true);
   addButton('修改订阅地址',async()=>{showSubscriptionForm(p);return false});
   addButton('订阅下载设置',async()=>{showSubscriptionPolicyForm(p);return false});
  }
  addButton('重命名',async()=>{const name=prompt('配置名称',p.name);if(name===null)return false;await profileAction('rename',p.id,name)});
  if(!p.active)addButton('删除',async()=>{if(!confirm('从配置列表删除“'+p.name+'”？\n原始文件会保留；本版本暂不提供已删除配置的回收站入口。'))return false;await profileAction('delete',p.id)});
  card.append(info,buttons);list.appendChild(card);
 }
 syncProfileControls();
 }catch(e){if(request!==profileListRequest)return;$('profileList').innerHTML='<div class="rule-error"></div>';$('profileList').firstChild.textContent=e.message}
}
async function profileAction(action,id,name='',mode='',extra={}){for(let attempt=0;attempt<4;attempt++){try{return await api('/api/profiles',{method:'POST',body:JSON.stringify({action,id,name,mode,...extra})})}catch(e){if(attempt<3&&/另一个操作正在进行/.test(e.message)){await new Promise(r=>setTimeout(r,600));continue}throw e}}}
function applyState(s){
 const profilesChanged=state.profileCount!==s.profileCount||!!state.profilePending!==!!s.profilePending||state.profileRevision!==s.profileRevision;
 const diagnosisChanged=['busy','pid','running','proxyEnabled','proxyError','connectionError','hasConfig','coreReady','panelReady'].some(key=>state[key]!==s[key]);
 render(s);
 syncProfileControls();
 if(profilesChanged){clearConfigPreview();loadProfiles();if(activeView==='overridesView'&&!overrideDirty())openOverrides()}
 if(diagnosisChanged&&(diagnosisOpen||(!s.busy&&(s.connectionError||s.proxyError))))runDiagnosis();
}
async function refreshState(){
 const request=++stateRequest;
 try{const s=await api('/api/state');if(request===stateRequest)applyState(s)}catch{}
}
let subscriptionEditingID='';
function showSubscriptionForm(profile=null){
 closeSubscriptionPolicyForm();
 subscriptionEditingID=profile?.id||'';
 $('subscriptionFormTitle').textContent=profile?'修改订阅地址 · '+profile.name:'添加订阅 URL';
 $('subscriptionName').value=profile?.name||'';$('subscriptionName').readOnly=!!profile;
 $('subscriptionURL').value=profile?.url||'';$('subscriptionError').textContent='';
 $('confirmSubscription').textContent=profile?'校验并保存地址':'下载并校验';
 const policy=effectiveSubscriptionPolicy(profile);populateSubscriptionPolicy('subscription',policy);
 for(const radio of document.querySelectorAll('input[name="downloadMode"]'))radio.checked=radio.value===policy.mode;
 $('subscriptionForm').style.display='block';(profile?$('subscriptionURL'):$('subscriptionName')).focus();
}
function closeSubscriptionForm(){subscriptionEditingID='';$('subscriptionForm').style.display='none';$('subscriptionURL').value='';$('subscriptionUA').value='';$('subscriptionName').value='';$('subscriptionName').readOnly=false;$('subscriptionError').textContent='';$('confirmSubscription').textContent='下载并校验'}
$('addSubscription').onclick=()=>showSubscriptionForm();
$('cancelSubscription').onclick=closeSubscriptionForm;
$('confirmSubscription').onclick=()=>runGuardedProfileOperation(async()=>{
 const id=subscriptionEditingID,name=$('subscriptionName').value.trim(),url=$('subscriptionURL').value.trim();
 $('subscriptionError').textContent='';
 if(!url||(!id&&!name))throw new Error(id?'请填写新的订阅 URL':'请填写配置名称和订阅 URL');
 $('confirmSubscription').textContent='下载并校验中…';
 try{
  const policy=readSubscriptionPolicy('subscription');
  if(id)await profileAction('edit-url',id,'',policy.mode,{url,policy});
  else await api('/api/profiles',{method:'POST',body:JSON.stringify({action:'add-url',name,url,mode:policy.mode,policy})});
  closeSubscriptionForm();closeYAMLEditor();toast(id?'订阅地址已保存并校验':'订阅已添加并选中');
 }finally{if($('subscriptionForm').style.display==='block')$('confirmSubscription').textContent=id?'校验并保存地址':'下载并校验'}
},error=>{$('subscriptionError').textContent=error.message});

$('exportBackup').onclick=()=>runProfileOperation(async()=>{
 if(!confirm('完整备份可能包含订阅地址、节点密码和其它密钥，文件不会加密。请保存在可信位置，不要作为公开发布包分享。备份只包含已保存的数据。继续导出？'))return false;
 const backup=await api('/api/backup');const blob=new Blob([JSON.stringify(backup,null,2)],{type:'application/json'});
 const link=document.createElement('a');link.href=URL.createObjectURL(blob);link.download='MihomoLite-backup-'+new Date().toISOString().slice(0,10)+'.json';link.click();URL.revokeObjectURL(link.href);toast('完整备份已导出');return false;
});
$('importBackup').onclick=()=>$('backupFile').click();
$('backupFile').onchange=async event=>{
 const file=event.target.files[0];event.target.value='';if(!file)return;
 if(file.size>64*1024*1024){toast('备份超过 64 MiB');return}
 if(state.running){toast('请先断开连接再从备份还原');return}
 if(!confirm('从备份还原会替换所有已保存的配置和自定义规则，并恢复配置名称、订阅信息及更新设置。还原前会自动保留当前数据快照。浏览器草稿和本机启动设置不随备份还原。继续？'))return;
 await runGuardedProfileOperation(async()=>{
  const restored=await api('/api/backup',{method:'POST',body:await file.text()});closeYAMLEditor();
  $('backupRestoreStatus').textContent=restored.recoverySnapshot?'还原前备份：'+restored.recoverySnapshot+'。需要撤销本次还原时，可通过“从备份还原”选择这份文件。':'';
  if(!rulesDirty&&!ruleFormDirty()){rulesLoaded=false;await loadRules()}
  toast('备份已还原；当前数据快照已保留');
 });
};
const events=new EventSource('/api/events');
events.addEventListener('state',e=>{stateRequest++;applyState(JSON.parse(e.data))});
events.onerror=()=>{setTimeout(refreshState,1000)};
async function loadLogs(){try{const r=await fetch('/api/logs');logs=await r.json();$('logs').textContent=logs.map(x=>x.time+' '+x.message).join('\n')||'暂无日志'}catch{}}
async function loadCoreLogs(){try{const r=await fetch('/api/core-logs');const data=await r.json();renderCoreLogs(data.text||'')}catch{}}
function renderCoreLogs(text){const pre=$('coreLogs');if(!text){pre.textContent='暂无连接日志';return}const lines=text.split('\n');const frag=document.createDocumentFragment();lines.forEach(line=>{const hit=rules.find(r=>r.enabled&&r.value&&line.includes(r.value));const div=document.createElement('div');div.textContent=line;if(hit){div.className='core-log-hit';div.title='命中自定义规则：'+hit.type+' '+hit.value+' → '+hit.policy}frag.appendChild(div)});pre.textContent='';pre.appendChild(frag);pre.scrollTop=pre.scrollHeight}
let overrideLoaded=false,overrideBusy=false,overrideBase='',overrideKey='',overrideGeneration=0,overridePreviewGeneration=0;
let overrideSimple=null,overrideFormChanges={},overrideFormStale=false,overrideProfileIDs=null,overrideListRequest=0;
function overrideSelectionBound(){if(!overrideLoaded||!overrideKey)return false;const saved=JSON.parse(overrideKey),current=overrideSelection();return saved.scope===current.scope&&(current.scope==='global'||saved.profileId===current.profileId)}
function overrideTargetAvailable(){return !!$('overrideProfile').value&&(overrideProfileIDs===null||overrideProfileIDs.includes($('overrideProfile').value))}
function overrideDirty(){return overrideLoaded&&($('overrideYAML').value!==overrideBase||Object.keys(overrideFormChanges).length>0)}
function clearConfigPreview(message='输入或配置已变更，请重新预览'){$('finalConfigPreview').value='';$('previewStatus').textContent=message;overridePreviewGeneration++}
function overrideSelection(){return {scope:$('overrideScope').value||'global',profileId:$('overrideProfile').value||''}}
function populateOverrideSimple(simple){overrideSimple=simple||null;overrideFormChanges={};overrideFormStale=false;$('overrideLogLevel').value=simple?.logLevel||'';$('overrideDNSMode').value=simple?.dnsServers?.length?'custom':'';$('overrideDNSServers').value=(simple?.dnsServers||[]).join('\n');renderOverrideControls()}
function renderOverrideControls(){
 for(const id of ['overrideScope','overrideProfile','loadOverride'])$(id).disabled=overrideBusy;$('overrideYAML').disabled=overrideBusy||!overrideSelectionBound()||Object.keys(overrideFormChanges).length>0;
 $('saveOverride').disabled=overrideBusy||!overrideSelectionBound()||(overrideSelection().scope==='profile'&&!overrideTargetAvailable());$('previewOverride').disabled=overrideBusy||!overrideSelectionBound()||!overrideTargetAvailable();$('syncOverrideForm').disabled=overrideBusy||!overrideSelectionBound();
 const inherited=overrideSelection().scope==='profile'?'沿用全局 / 订阅（推荐）':'沿用订阅（推荐）';$('overrideLogInherit').textContent=inherited;$('overrideDNSInherit').textContent=inherited;
 $('overrideScopeHelp').textContent=overrideSelection().scope==='profile'?'只调整所选配置；其他配置不受影响。':'所有配置共用这些设置；所选配置用于查看最终配置。';
 const blocked=overrideBusy||!overrideSelectionBound()||overrideFormStale;
 $('overrideLogLevel').disabled=blocked||!overrideSimple?.logEditable;$('overrideDNSMode').disabled=blocked||!overrideSimple?.dnsEditable;$('overrideDNSServers').disabled=blocked||!overrideSimple?.dnsEditable||$('overrideDNSMode').value!=='custom';$('overrideDNSServersField').hidden=$('overrideDNSMode').value!=='custom';
 const stale='高级 YAML 已修改；同步后可使用简单设置。';$('overrideLogHelp').textContent=overrideFormStale?stale:(overrideSimple?.logEditable?'不确定时保持沿用即可。':'此日志设置需在高级 YAML 中编辑，原内容会保留。');$('overrideDNSHelp').textContent=overrideFormStale?stale:(overrideSimple?.dnsEditable?'只调整 DNS 服务器，其他 DNS 设置和启用状态保持不变。':'此 DNS 设置需在高级 YAML 中编辑，原内容会保留。');$('overrideAdvancedSummary').textContent='高级设置（YAML）'+(overrideSimple?.hasAdvanced?' · 已有高级设置':'');
}
function markOverrideForm(field){if(!overrideLoaded||overrideFormStale)return;if(field==='logLevel')overrideFormChanges.logLevel=$('overrideLogLevel').value;else overrideFormChanges.dnsServers=true;overrideGeneration++;clearConfigPreview();$('overrideError').textContent='';$('overrideStatus').textContent='设置尚未保存 · 保存后重新连接生效';renderOverrideControls()}
function overrideFormPatch(){const patch={};if(Object.hasOwn(overrideFormChanges,'logLevel'))patch.logLevel=$('overrideLogLevel').value;if(Object.hasOwn(overrideFormChanges,'dnsServers')){if($('overrideDNSMode').value==='custom'){patch.dnsServers=$('overrideDNSServers').value.split(/\r?\n/).map(v=>v.trim()).filter(Boolean);if(!patch.dnsServers.length)throw new Error('请填写至少一个 DNS 地址，或选择沿用。')}else patch.dnsServers=[]}return patch}
async function materializeOverrideCandidate(sync=false){const patch=overrideFormPatch();if(!sync&&!Object.keys(patch).length)return $('overrideYAML').value;const generation=overrideGeneration,yaml=$('overrideYAML').value,input=[$('overrideLogLevel').value,$('overrideDNSMode').value,$('overrideDNSServers').value];const result=await api('/api/overrides/form',{method:'POST',body:JSON.stringify({yaml,...patch})});const current=[$('overrideLogLevel').value,$('overrideDNSMode').value,$('overrideDNSServers').value];if(generation===overrideGeneration&&JSON.stringify(current)!==JSON.stringify(input)){overrideGeneration++;if(current[0]!==input[0])overrideFormChanges.logLevel=current[0];if(current[1]!==input[1]||current[2]!==input[2])overrideFormChanges.dnsServers=true;clearConfigPreview()}if(generation!==overrideGeneration)throw new Error('输入已变化，请重新操作；新输入已保留。');$('overrideYAML').value=result.yaml;populateOverrideSimple(result.simple);return result.yaml}
async function syncOverrideForm(){if(overrideBusy||!overrideSelectionBound())return;overrideBusy=true;renderOverrideControls();try{await materializeOverrideCandidate(true);$('overrideError').textContent='';$('overrideStatus').textContent=overrideDirty()?'设置尚未保存 · 保存后重新连接生效':'与已保存设置一致。'}catch(e){$('overrideError').textContent=e.message;$('overrideStatus').textContent='同步失败，输入保留；请修正后重试。'}finally{overrideBusy=false;renderOverrideControls()}}
async function openOverrides(){if(overrideBusy)return;const request=++overrideListRequest,generation=overrideGeneration;try{const data=await api('/api/profiles');if(request!==overrideListRequest||overrideBusy||generation!==overrideGeneration)return;const previous=$('overrideProfile').value;const ids=data.profiles.map(p=>p.id);overrideProfileIDs=ids;$('overrideProfile').textContent='';for(const p of data.profiles){const option=document.createElement('option');option.value=p.id;option.textContent=p.name+(p.id===data.activeId?'（当前）':'');$('overrideProfile').appendChild(option)}const loaded=overrideLoaded&&overrideKey?JSON.parse(overrideKey):null;if(loaded?.scope==='profile'&&!ids.includes(loaded.profileId)){const missing=document.createElement('option');missing.value=loaded.profileId;missing.textContent='原配置已不在列表（设置保留）';missing.disabled=true;$('overrideProfile').appendChild(missing);$('overrideScope').value='profile';$('overrideProfile').value=loaded.profileId;$('overrideError').textContent='原配置已不在列表；未保存设置已保留，不会写入其他配置。选择新配置前请确认是否放弃。';clearConfigPreview('目标配置已不在列表');renderOverrideControls();return}$('overrideProfile').value=ids.includes(previous)?previous:(data.activeId||ids[0]||'');if(overrideLoaded&&overrideSelectionBound()){renderOverrideControls();return}await loadOverride()}catch(e){if(request===overrideListRequest)$('overrideError').textContent=e.message}}
async function loadOverride(){if(overrideBusy)return;if(overrideDirty()&&!confirm('重新读取会放弃当前未保存设置，继续吗？'))return;const selected=overrideSelection();if(selected.scope==='profile'&&!selected.profileId){$('overrideError').textContent='请先添加并选择配置';return}const generation=++overrideGeneration;overrideBusy=true;renderOverrideControls();clearConfigPreview('');try{const query='?scope='+selected.scope+(selected.scope==='profile'&&selected.profileId?'&profileId='+encodeURIComponent(selected.profileId):'');const data=await api('/api/overrides'+query);if(generation!==overrideGeneration)return;$('overrideYAML').value=data.yaml||'';overrideBase=$('overrideYAML').value;overrideKey=JSON.stringify(selected);overrideLoaded=true;populateOverrideSimple(data.simple);$('overrideError').textContent='';$('overrideStatus').textContent='已读取保存的设置。修改后请保存；重新连接生效。'}catch(e){overrideLoaded=false;$('overrideError').textContent=e.message}finally{overrideBusy=false;renderOverrideControls()}}
async function changeOverrideSelection(){if(overrideBusy)return;if(overrideLoaded&&overrideSelection().scope==='global'&&JSON.parse(overrideKey).scope==='global'){overrideKey=JSON.stringify(overrideSelection());clearConfigPreview();renderOverrideControls();return}if(overrideDirty()&&!confirm('切换应用范围会放弃当前未保存设置，继续吗？')){const previous=JSON.parse(overrideKey);$('overrideScope').value=previous.scope;$('overrideProfile').value=previous.profileId;return}overrideLoaded=false;overrideGeneration++;clearConfigPreview('');await loadOverride()}
async function saveOverride(){if(overrideBusy||!overrideSelectionBound()||(overrideSelection().scope==='profile'&&!overrideTargetAvailable()))return;overrideBusy=true;renderOverrideControls();const selected=overrideSelection();try{const yaml=await materializeOverrideCandidate();const generation=overrideGeneration,input=[$('overrideLogLevel').value,$('overrideDNSMode').value,$('overrideDNSServers').value];const result=await api('/api/overrides',{method:'POST',body:JSON.stringify({scope:selected.scope,...(selected.scope==='profile'?{profileId:selected.profileId}:{}),yaml})});if(generation===overrideGeneration){const current=[$('overrideLogLevel').value,$('overrideDNSMode').value,$('overrideDNSServers').value];if(JSON.stringify(current)!==JSON.stringify(input)){overrideGeneration++;if(current[0]!==input[0])overrideFormChanges.logLevel=current[0];if(current[1]!==input[1]||current[2]!==input[2])overrideFormChanges.dnsServers=true}}overrideBase=yaml;overrideKey=JSON.stringify(selected);$('overrideError').textContent='';$('overrideStatus').textContent=generation!==overrideGeneration?'已保存提交时的设置；新的输入尚未保存。':('设置已保存；'+(result.requiresReconnect?'重新连接后生效。':'下次连接生效。'));clearConfigPreview('已保存，请重新预览');await refreshState()}catch(e){$('overrideError').textContent=e.message;$('overrideStatus').textContent='校验 / 保存失败，输入保留，可修正后重试。'}finally{overrideBusy=false;renderOverrideControls()}}
async function previewOverride(){if(overrideBusy||!overrideSelectionBound()||!overrideTargetAvailable())return;const generation=++overridePreviewGeneration,selected=overrideSelection();overrideBusy=true;renderOverrideControls();$('finalConfigPreview').value='';$('previewStatus').textContent='正在只读合成并尝试离线校验…';try{const yaml=await materializeOverrideCandidate();if(generation!==overridePreviewGeneration)return;const result=await api('/api/config/preview',{method:'POST',body:JSON.stringify({...selected,yaml})});if(generation!==overridePreviewGeneration)return;$('finalConfigPreview').value=result.yaml||'';$('previewStatus').textContent=(result.validated?'离线内核校验通过。':'仅合成预览，尚未通过离线内核校验。')+(result.validation||'')+' '+(result.warning||'');$('overrideError').textContent=''}catch(e){if(generation===overridePreviewGeneration){$('previewStatus').textContent='预览失败';$('overrideError').textContent=e.message}}finally{overrideBusy=false;renderOverrideControls()}}
$('overrideScope').onchange=changeOverrideSelection;$('overrideProfile').onchange=changeOverrideSelection;$('loadOverride').onclick=loadOverride;$('saveOverride').onclick=saveOverride;$('previewOverride').onclick=previewOverride;$('syncOverrideForm').onclick=syncOverrideForm;
$('overrideLogLevel').onchange=()=>markOverrideForm('logLevel');$('overrideDNSMode').onchange=()=>markOverrideForm('dnsServers');$('overrideDNSServers').oninput=()=>markOverrideForm('dnsServers');
$('overrideAdvanced').ontoggle=async()=>{if(overrideBusy||!overrideLoaded)return;if($('overrideAdvanced').open&&Object.keys(overrideFormChanges).length){await syncOverrideForm();if(Object.keys(overrideFormChanges).length)$('overrideAdvanced').open=false}else if(!$('overrideAdvanced').open&&overrideFormStale)await syncOverrideForm()};
$('overrideYAML').oninput=()=>{overrideGeneration++;overrideFormChanges={};overrideFormStale=true;clearConfigPreview();$('overrideStatus').textContent=overrideDirty()?'高级设置尚未保存 · 草稿只在当前页面，请保存后再关闭':'与已保存设置一致';renderOverrideControls()};
let componentActionBusy=false,componentReadSeq=0,componentSnapshot=null;
const componentNames={launcher:'启动器',core:'mihomo 内核',panel:'zashboard 面板'};
const componentPhases={verifying:'校验中',preparing:'准备安装',switching:'切换组件',committed:'已安装','rolling-back':'回滚中','rolled-back':'已回滚',idle:'未检查',checking:'检查中',checked:'已检查',downloading:'下载中',downloaded:'下载完成',ready:'已校验，可安装',installing:'安装中',installed:'已安装',rollingBack:'回滚中',rolledBack:'已回滚',failed:'失败',error:'失败'};
function renderComponents(data){componentSnapshot=data;$('componentList').textContent='';for(const item of data.components||[]){const card=document.createElement('div');card.className='profile-card subscription-card';const title=document.createElement('div');title.className='profile-name';title.textContent=componentNames[item.component]||'未知组件';const meta=document.createElement('div');meta.className='profile-meta';const current=item.current||{},progress=item.progress||{};meta.textContent='当前版本：'+(current.version||'未知')+' · SHA-256：'+(current.sha256?current.sha256.slice(0,12):'未知')+'\n身份：'+(current.identityStatus||'未知')+' · 清单：'+(current.manifestStatus||'未知');const status=document.createElement('div');status.className='subscription-result';status.textContent=(componentPhases[progress.phase]||progress.phase||'尚未检查')+(progress.total?' · '+Math.min(100,Math.floor(100*progress.downloaded/progress.total))+'%':'')+(progress.error?' · '+progress.error:'');const notice=document.createElement('p');notice.className='hint';notice.textContent=item.component==='launcher'?'尚未配置可信启动器发布源；仅核对本地版本与发布清单，不提供下载或自替换。':((item.available?'官方可用版本：'+(item.available.version||item.available.tag||'未知')+'。 ':'')+(item.installBlockReason||'安装 / 回滚须已断开连接，并明确确认。'));const buttons=document.createElement('div');buttons.className='profile-buttons';for(const [action,label,allowed] of [['check','检查版本',item.component!=='launcher'],['download','下载并校验',item.component!=='launcher'&&!!item.available],['install','停机安装',item.canInstall],['rollback','回滚组件',item.canRollback]]){const button=document.createElement('button');button.className=action==='install'?'primary':'secondary';button.textContent=label;button.dataset.component=item.component;button.dataset.action=action;button.disabled=componentActionBusy||!allowed;button.onclick=()=>componentAction(item.component,action);buttons.appendChild(button)}card.append(title,meta,status,notice,buttons);$('componentList').appendChild(card)}$('refreshComponents').disabled=componentActionBusy}
async function loadComponents(){const seq=++componentReadSeq;try{const result=await api('/api/components');if(seq===componentReadSeq)renderComponents(result)}catch(e){if(seq===componentReadSeq)$('componentError').textContent=e.message}}
async function componentAction(component,action){if(componentActionBusy)return;let confirmed=false;if(action==='install'||action==='rollback'){confirmed=confirm((action==='install'?'安装':'回滚')+' '+componentNames[component]+'？需先断开内核；不会主动停机、强杀进程或修改用户配置。');if(!confirmed)return}componentActionBusy=true;if(componentSnapshot)renderComponents(componentSnapshot);$('componentError').textContent='';try{await api('/api/components',{method:'POST',body:JSON.stringify({component,action,...(confirmed?{confirm:true}:{})})});toast(action==='download'?'下载并校验完成，尚未安装':'组件操作完成')}catch(e){$('componentError').textContent=e.message}finally{componentActionBusy=false;await loadComponents();await refreshState()}}
$('refreshComponents').onclick=loadComponents;
$('cancelConnectionRetry').onclick=()=>connectionAction('/api/disconnect');

let settingsBusy=false;let settingsGeneration=0;let settingsRequestSeq=0;let confirmedSettings={};let portsDirty=false;let connectionPolicyDirty=false;
const portFields=['proxy','backend','panel','web'];
function renderPortSettings(){
 if(confirmedSettings.ports&&!portsDirty)for(const field of portFields){const option=confirmedSettings.ports[field];if(!option)continue;$(field+'PortMode').value=option.mode;$(field+'Port').value=option.mode==='fixed'?String(option.port):''}
 for(const field of portFields){$(field+'PortMode').disabled=settingsBusy;$(field+'Port').disabled=settingsBusy||$(field+'PortMode').value!=='fixed'}
 $('savePortSettings').disabled=settingsBusy;
 $('portSettingsStatus').textContent=portsDirty?'端口偏好尚未保存；当前连接和本页面地址保持不变。':(confirmedSettings.portsRestartRequired?'端口偏好已保存，待程序重启生效；单独重连不会生效。':'端口偏好与本次启动一致；修改并保存后下次程序启动生效。');
}
function applySettings(data,fromEvent=false){if(fromEvent)settingsRequestSeq++;if(settingsBusy)return;if(typeof data.autoStart==='boolean')confirmedSettings.autoStart=data.autoStart;if(typeof data.closeToTray==='boolean')confirmedSettings.closeToTray=data.closeToTray;if(typeof data.silentStart==='boolean')confirmedSettings.silentStart=data.silentStart;if(data.connectionPolicy)confirmedSettings.connectionPolicy=JSON.parse(JSON.stringify(data.connectionPolicy));if(data.ports)confirmedSettings.ports=JSON.parse(JSON.stringify(data.ports));if(data.activePorts)confirmedSettings.activePorts=JSON.parse(JSON.stringify(data.activePorts));if(typeof data.portsRestartRequired==='boolean')confirmedSettings.portsRestartRequired=data.portsRestartRequired;if(typeof confirmedSettings.autoStart==='boolean')$('autoStart').checked=confirmedSettings.autoStart;if(typeof confirmedSettings.closeToTray==='boolean')$('closeToTray').checked=confirmedSettings.closeToTray;if(typeof confirmedSettings.silentStart==='boolean')$('silentStart').checked=confirmedSettings.silentStart;renderPortSettings();renderConnectionPolicy()}
async function loadSettings(){if(settingsBusy)return;const generation=settingsGeneration;const requestSeq=++settingsRequestSeq;try{const data=await api('/api/settings');if(generation===settingsGeneration&&requestSeq===settingsRequestSeq&&!settingsBusy)applySettings(data)}catch{}}
async function saveRuntimeSettings(patch,message){if(settingsBusy)return;const savingPorts=!!patch.ports,savingConnection=!!patch.connectionPolicy;settingsBusy=true;settingsGeneration++;$('autoStart').disabled=true;$('closeToTray').disabled=true;$('silentStart').disabled=true;renderPortSettings();renderConnectionPolicy();try{const saved=await api('/api/settings',{method:'PUT',body:JSON.stringify(patch)});settingsBusy=false;if(savingPorts){portsDirty=false;$('portSettingsError').textContent=''}if(savingConnection){connectionPolicyDirty=false;$('connectionPolicyError').textContent=''}applySettings(saved);toast(message)}catch(e){settingsBusy=false;if(savingPorts)$('portSettingsError').textContent=e.message;if(savingConnection)$('connectionPolicyError').textContent=e.message;applySettings(confirmedSettings);toast(e.message)}finally{settingsBusy=false;settingsGeneration++;$('autoStart').disabled=false;$('closeToTray').disabled=false;$('silentStart').disabled=false;renderPortSettings();renderConnectionPolicy();await loadSettings()}}
function markPortsDirty(){if(settingsBusy)return;portsDirty=true;$('portSettingsError').textContent='';renderPortSettings()}
function readPortSettings(){const result={},seen=new Set();for(const field of portFields){const mode=$(field+'PortMode').value;if(mode!=='auto'&&mode!=='fixed')throw new Error('端口模式无效');let port=0;if(mode==='fixed'){const value=$(field+'Port').value;if(!/^[0-9]+$/.test(value))throw new Error('固定端口须为 1–65535 的整数');port=Number(value);if(!Number.isSafeInteger(port)||port<1||port>65535)throw new Error('固定端口须为 1–65535 的整数');if(seen.has(port))throw new Error('四类固定端口不能重复');seen.add(port)}result[field]={mode,port}}return result}
$('savePortSettings').onclick=async()=>{if(settingsBusy)return;try{const ports=readPortSettings();await saveRuntimeSettings({ports},'端口偏好已保存，下次程序启动生效')}catch(e){$('portSettingsError').textContent=e.message;toast(e.message)}};
for(const field of portFields){$(field+'PortMode').onchange=markPortsDirty;$(field+'Port').oninput=markPortsDirty}
$('autoStart').onchange=()=>saveRuntimeSettings({autoStart:$('autoStart').checked},'开机自启设置已更新');
$('closeToTray').onchange=()=>saveRuntimeSettings({closeToTray:$('closeToTray').checked},'关闭窗口行为已更新');
$('silentStart').onchange=()=>saveRuntimeSettings({silentStart:$('silentStart').checked},'静默启动设置已更新');
function renderConnectionPolicy(){const policy=confirmedSettings.connectionPolicy||{startupConnect:false,crashReconnect:false,maxRetries:3,initialDelaySeconds:2,maxDelaySeconds:30};if(!connectionPolicyDirty){$('startupConnect').checked=!!policy.startupConnect;$('crashReconnect').checked=!!policy.crashReconnect;for(const [id,key] of [['connectionMaxRetries','maxRetries'],['connectionInitialDelay','initialDelaySeconds'],['connectionMaxDelay','maxDelaySeconds']])$(id).value=String(policy[key])}for(const id of ['startupConnect','crashReconnect','connectionMaxRetries','connectionInitialDelay','connectionMaxDelay','saveConnectionPolicy'])$(id).disabled=settingsBusy}
function readConnectionPolicy(){const policy={startupConnect:$('startupConnect').checked,crashReconnect:$('crashReconnect').checked};for(const [id,key,max] of [['connectionMaxRetries','maxRetries',5],['connectionInitialDelay','initialDelaySeconds',60],['connectionMaxDelay','maxDelaySeconds',300]]){const raw=$(id).value,number=Number(raw);if(!/^[0-9]+$/.test(raw)||!Number.isSafeInteger(number)||number<1||number>max)throw new Error('重试次数 / 等待时间必须在标示范围内');policy[key]=number}if(policy.maxDelaySeconds<policy.initialDelaySeconds)throw new Error('最长等待不能小于初始等待');return policy}
for(const id of ['startupConnect','crashReconnect','connectionMaxRetries','connectionInitialDelay','connectionMaxDelay']){const editing=()=>{if(!settingsBusy)connectionPolicyDirty=true};$(id).onfocus=editing;$(id).oninput=editing}
$('saveConnectionPolicy').onclick=async()=>{if(settingsBusy)return;try{await saveRuntimeSettings({connectionPolicy:readConnectionPolicy()},'连接策略已保存；启动自动连接下次启动生效')}catch(e){$('connectionPolicyError').textContent=e.message}};
loadSettings();setInterval(loadSettings,1500);initializeDrafts();loadComponents();setInterval(()=>{if(componentActionBusy)loadComponents()},1000);
setInterval(loadLogs,2000);setInterval(loadCoreLogs,2000);loadLogs();loadCoreLogs();loadProfiles();refreshState();
</script></body></html>`
