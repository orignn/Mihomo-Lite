package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const defaultMixedPort = 17890

func loopbackAddr(port int) string { return fmt.Sprintf("127.0.0.1:%d", port) }

func checkCoreListener(port, pid int) error {
	listeners, err := loopbackListeners()
	if err != nil {
		return err
	}
	return verifyCoreListener(listeners, port, pid)
}

func verifyCoreListener(listeners map[int][]int, port, pid int) error {
	owners := listeners[port]
	if len(owners) == 0 {
		return fmt.Errorf("%s 尚无 TCP 监听", loopbackAddr(port))
	}
	if !listenerOwnedBy(owners, pid) {
		return fmt.Errorf("%s 的监听 PID %v 不属于当前内核（PID %d）", loopbackAddr(port), owners, pid)
	}
	return nil
}

func probeCoreAPI(ctx context.Context, cfg RuntimeConfig, pid int) error {
	if err := checkCoreListener(cfg.BackendPort, pid); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+loopbackAddr(cfg.BackendPort)+"/version", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.Secret)
	req.Close = true
	client := *localHTTPClient
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("内核 API %s 请求失败: %w", loopbackAddr(cfg.BackendPort), err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("内核 API %s 返回 HTTP %d", loopbackAddr(cfg.BackendPort), response.StatusCode)
	}
	return nil
}

type diagnosticCheck struct {
	Status string `json:"status"`
	Detail string `json:"detail"`
}

type diagnosticAction struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

type connectionDiagnostics struct {
	Busy    bool               `json:"busy"`
	Core    diagnosticCheck    `json:"core"`
	Port    diagnosticCheck    `json:"port"`
	Proxy   diagnosticCheck    `json:"proxy"`
	Reason  string             `json:"reason"`
	Advice  string             `json:"advice"`
	Actions []diagnosticAction `json:"actions"`
}

func (a *Application) handleDiagnostics(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.connectionDiagnostics(r.Context()))
}

func (a *Application) connectionDiagnostics(ctx context.Context) connectionDiagnostics {
	a.mu.Lock()
	if a.operation || a.shuttingDown {
		a.mu.Unlock()
		return connectionDiagnostics{Busy: true, Reason: "操作进行中，完成后可重新检查。"}
	}
	session, cfg, lastPort, failure := a.core, a.runtime, a.lastMixedPort, a.connectionError
	proxy, restorePending, proxyErr := a.proxy.Inspect()
	a.mu.Unlock()

	d := connectionDiagnostics{
		Core:   diagnosticCheck{"idle", "内核未运行"},
		Reason: failure,
	}
	missingCore := !fileExists(filepath.Join(a.baseDir, "runtime", coreExecutable))
	missingPanel := !fileExists(filepath.Join(a.dataDir, "ui", "index.html"))
	hasConfig := fileExists(filepath.Join(a.dataDir, "source.yaml"))
	running := session != nil && processExists(session.pid)
	if running {
		lastPort = cfg.MixedPort
		if err := probeCoreAPI(ctx, cfg, session.pid); err != nil {
			d.Core = diagnosticCheck{"error", fmt.Sprintf("PID %d 在运行，%v", session.pid, err)}
		} else {
			d.Core = diagnosticCheck{"ok", fmt.Sprintf("运行中（PID %d），内核 API 已响应", session.pid)}
		}
	} else if missingCore {
		d.Core = diagnosticCheck{"error", "找不到内核文件 runtime/mihomo.exe"}
	} else if session != nil {
		d.Core = diagnosticCheck{"error", fmt.Sprintf("内核进程 PID %d 已退出", session.pid)}
	}
	if lastPort == 0 {
		lastPort = defaultMixedPort
	}
	listeners, listenerErr := loopbackListeners()
	address := loopbackAddr(lastPort)
	switch {
	case listenerErr != nil:
		d.Port = diagnosticCheck{"unknown", listenerErr.Error()}
	case running:
		if err := verifyCoreListener(listeners, lastPort, session.pid); err != nil {
			d.Port = diagnosticCheck{"error", err.Error()}
		} else {
			d.Port = diagnosticCheck{"ok", address + " 正由当前内核监听"}
		}
	case len(listeners[lastPort]) > 0:
		d.Port = diagnosticCheck{"warning", fmt.Sprintf("%s 已被监听（PID %v），连接时会尝试其他端口", address, listeners[lastPort])}
	default:
		d.Port = diagnosticCheck{"idle", address + " 当前无 TCP 监听"}
	}
	d.Proxy = describeSystemProxy(proxy, proxyErr, running, d.Port.Status, address)
	if restorePending && !running {
		d.Proxy.Status = "error"
		d.Proxy.Detail += "。存在尚未恢复的代理快照"
	}
	switch {
	case restorePending && !running:
		d.Advice = "上次恢复失败，可重试还原连接前的 Windows 代理设置。"
		d.addAction("restoreProxy", "恢复原系统代理")
	case running && (d.Core.Status == "error" || d.Port.Status == "error"):
		d.Advice = "查看内核日志，确认没有其他程序占用端口，然后重启内核。"
		d.addAction("restart", "重启内核")
		d.addAction("logs", "查看内核日志")
	case running && d.Port.Status == "ok" && d.Core.Status == "ok" && (d.Proxy.Status == "error" || restorePending):
		d.Advice = "若其他代理软件正在接管 Windows 代理，请先退出该软件，再将系统代理指回当前内核。"
		d.addAction("repairProxy", "修复系统代理")
	case !running && (missingCore || missingPanel):
		if d.Reason == "" {
			if missingCore {
				d.Reason = d.Core.Detail
			} else {
				d.Reason = "找不到 Zashboard 面板文件"
			}
		}
		d.Advice = "在启动器目录运行 setup.ps1，或重新解压完整安装包。"
	case !running && !hasConfig:
		if d.Reason == "" {
			d.Reason = "尚未导入配置"
		}
		d.Advice = "添加订阅或导入一份有效的 YAML 配置后连接。"
		d.addAction("import", "选择 YAML")
	case !running && session == nil && d.Port.Status == "warning":
		d.Advice = "可在任务管理器的“详细信息”页按 PID 确认占用程序。重新连接时，启动器会尝试选择可用端口。"
		d.addAction("retry", "重新连接")
	case !running && session == nil && failure != "":
		d.Advice = "先查看内核日志或检查当前 YAML 配置，再重试连接。"
		d.addAction("logs", "查看内核日志")
		d.addAction("retry", "重试连接")
	}
	if d.Proxy.Status == "unknown" || d.Proxy.Status == "error" || !running && d.Proxy.Status == "warning" {
		if d.Advice == "" {
			d.Advice = "打开 Windows 代理设置并核对手动代理地址和自动配置。"
		}
		d.addAction("windowsProxy", "打开 Windows 代理设置")
	}
	if !running {
		if p, ok := a.proxy.(recoverableSystemProxy); ok {
			recovery := p.RecoveryStatus()
			if recovery.HasRecord {
				d.Reason = recovery.Detail
				d.Advice = "不会覆盖其他软件接管的代理，也不会仅凭 PID 结束进程。恢复失败会保留记录，可重试。"
				d.Actions = nil
				if recovery.Pending || !recovery.CoreVerified {
					d.addAction("restoreProxy", "核验并重试恢复")
				}
				if recovery.Conflict {
					d.addAction("keepCurrentProxy", "保留当前代理并释放旧快照")
				}
				if recovery.CoreVerified {
					d.Core = diagnosticCheck{"warning", fmt.Sprintf("已核验遗留内核（PID %d），不会自动结束", recovery.CorePID)}
					if !recovery.Pending {
						d.addAction("stopLeftoverCore", "结束已核验的遗留内核")
					}
				}
				d.addAction("windowsProxy", "打开 Windows 代理设置")
			}
		}
		if err := a.checkLegacyCore(); err != nil {
			d.Core = diagnosticCheck{"warning", err.Error()}
			d.Reason = err.Error()
			d.Advice = "仅有旧版 PID 记录，不能安全确认会话所有权；请在任务管理器手动核验并退出后重试。"
			d.Actions = nil
		}
	}
	if d.Reason == "" {
		for _, check := range []diagnosticCheck{d.Core, d.Port, d.Proxy} {
			if check.Status == "error" {
				d.Reason = check.Detail
				break
			}
		}
	}

	a.mu.Lock()
	stale := a.operation || a.shuttingDown || a.core != session || a.runtime != cfg || a.connectionError != failure
	a.mu.Unlock()
	if stale {
		return connectionDiagnostics{Busy: true, Reason: "连接状态已变化，请重新检查。"}
	}
	return d
}

func (d *connectionDiagnostics) addAction(id, label string) {
	d.Actions = append(d.Actions, diagnosticAction{id, label})
}

func describeSystemProxy(proxy proxySnapshot, err error, running bool, portStatus, expected string) diagnosticCheck {
	if err != nil {
		return diagnosticCheck{"unknown", "无法读取 Windows 代理设置: " + err.Error()}
	}
	automatic := proxy.autoConfigURL != "" || proxy.autoDetect != 0
	autoNote := ""
	if automatic {
		autoNote = "；同时存在自动配置，应用可能使用不同路由"
	}
	if proxy.enabled == 0 {
		if running {
			return diagnosticCheck{"error", "Windows 手动代理未开启" + autoNote}
		}
		if automatic {
			return diagnosticCheck{"idle", "手动代理已关闭，检测到自动代理配置"}
		}
		return diagnosticCheck{"idle", "Windows 手动代理已关闭"}
	}
	if strings.TrimSpace(proxy.server) != expected {
		if running {
			return diagnosticCheck{"error", "Windows 手动代理未指向当前代理地址 " + expected + autoNote}
		}
		return diagnosticCheck{"idle", "Windows 手动代理已开启，指向其他地址" + autoNote}
	}
	if !running {
		return diagnosticCheck{"warning", "Windows 手动代理仍指向 " + expected + "，当前没有运行中的托管内核" + autoNote}
	}
	if portStatus != "ok" {
		return diagnosticCheck{"warning", "Windows 手动代理指向 " + expected + "，该地址的监听尚未确认" + autoNote}
	}
	return diagnosticCheck{"ok", "Windows 手动代理已指向 " + expected + autoNote}
}

func (a *Application) handleRepairProxy(w http.ResponseWriter, _ *http.Request) {
	a.requestProxyRecovery(w, false)
}

func (a *Application) handleRestoreProxy(w http.ResponseWriter, _ *http.Request) {
	a.requestProxyRecovery(w, true)
}

func (a *Application) requestProxyRecovery(w http.ResponseWriter, restore bool) {
	if err := a.begin(); err != nil {
		writeJSON(w, http.StatusConflict, errorResponse{Error: err.Error()})
		return
	}
	go func() { a.finish(a.recoverProxy(restore)) }()
	writeJSON(w, http.StatusAccepted, a.State())
}

func (a *Application) recoverProxy(restore bool) (repairErr error) {
	a.coreControl.Lock()
	defer a.coreControl.Unlock()
	defer func() {
		a.mu.Lock()
		defer a.mu.Unlock()
		if repairErr != nil {
			a.connectionError = repairErr.Error()
		} else {
			a.connectionError = ""
		}
	}()
	a.mu.Lock()
	session, cfg := a.core, a.runtime
	if restore {
		defer a.mu.Unlock()
		if session != nil {
			return errors.New("内核正在运行，请重新检查连接状态")
		}
		if p, ok := a.proxy.(recoverableSystemProxy); ok {
			return p.Recover()
		}
		_, pending, _ := a.proxy.Inspect()
		if !pending {
			return errors.New("没有待恢复的代理快照，请在 Windows 代理设置中检查")
		}
		if err := a.proxy.Disable(); err != nil {
			return fmt.Errorf("恢复连接前的系统代理失败: %w", err)
		}
		return nil
	}
	a.mu.Unlock()
	if session == nil {
		return errors.New("内核已停止，请重新连接")
	}
	if err := probeCoreAPI(context.Background(), cfg, session.pid); err != nil {
		return fmt.Errorf("内核 API 未就绪，请先重启内核: %w", err)
	}
	if err := checkCoreListener(cfg.MixedPort, session.pid); err != nil {
		return fmt.Errorf("代理端口未就绪，请先重启内核: %w", err)
	}
	active, err := a.enableCoreProxy(session, cfg.MixedPort)
	if !active {
		return errors.New("内核已退出，请重新连接")
	}
	if err != nil {
		return fmt.Errorf("重新设置 Windows 系统代理失败: %w", err)
	}
	return nil
}

func (a *Application) disconnect() error {
	return a.recordDisconnectError(a.Stop())
}

// Consume a previously established disconnect token; never cancel a newer intent.
func (a *Application) disconnectForIntent(generation uint64) error {
	a.coreControl.Lock()
	defer a.coreControl.Unlock()
	a.mu.Lock()
	valid := a.connectionPolicy.generation == generation && !a.connectionPolicy.wanted
	a.mu.Unlock()
	if !valid {
		return nil
	}
	err := a.stopCore()
	a.mu.Lock()
	if a.connectionPolicy.generation == generation {
		a.connectionPolicy.disconnectPending = false
	}
	a.mu.Unlock()
	return a.recordDisconnectError(err)
}

func (a *Application) recordDisconnectError(err error) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.connectionError = ""
	if err != nil {
		a.connectionError = "恢复连接前的系统代理失败: " + err.Error()
	} else {
		a.lastError = "" // A successful retry retires the old cleanup error too.
	}
	return err
}

func (a *Application) handleKeepCurrentProxy(w http.ResponseWriter, r *http.Request) {
	a.requestRecoveryAction(w, r, false)
}
func (a *Application) handleStopLeftoverCore(w http.ResponseWriter, r *http.Request) {
	a.requestRecoveryAction(w, r, true)
}
func (a *Application) requestRecoveryAction(w http.ResponseWriter, r *http.Request, stopCore bool) {
	var request struct {
		Confirm bool `json:"confirm"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&request); err != nil || !request.Confirm {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "需要明确确认此恢复操作"})
		return
	}
	if err := a.begin(); err != nil {
		writeJSON(w, http.StatusConflict, errorResponse{Error: err.Error()})
		return
	}
	go func() {
		a.coreControl.Lock()
		a.mu.Lock()
		running := a.core != nil
		a.mu.Unlock()
		var err error
		if running {
			err = errors.New("内核正在运行，请先断开")
		} else if p, ok := a.proxy.(recoverableSystemProxy); ok {
			if stopCore {
				err = p.StopLeftoverCore()
			} else {
				err = p.KeepCurrentProxy()
			}
		} else {
			err = errors.New("当前代理后端不支持持久恢复")
		}
		a.coreControl.Unlock()
		a.mu.Lock()
		if err == nil {
			a.connectionError = ""
		} else {
			a.connectionError = err.Error()
		}
		a.mu.Unlock()
		a.finish(err)
	}()
	writeJSON(w, http.StatusAccepted, a.State())
}

func (a *Application) handleOpenProxySettings(w http.ResponseWriter, _ *http.Request) {
	cmd := exec.Command("explorer.exe", "ms-settings:network-proxy")
	if err := cmd.Start(); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "无法打开设置，请手动进入 Windows 设置 → 网络和 Internet → 代理"})
		return
	}
	_ = cmd.Process.Release()
	writeJSON(w, http.StatusOK, struct{}{})
}
