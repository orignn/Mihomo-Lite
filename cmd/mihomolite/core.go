package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const coreExecutable = "mihomo.exe"

var localHTTPClient = &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 5 * time.Second}

type coreSession struct {
	cmd      *exec.Cmd
	identity recoveryProcessIdentity
	pid      int
	done     chan struct{}
	exitErr  error // Written before done closes.
	// Guarded by Application.mu; claim proxy cleanup exactly once per session.
	proxyStarted bool
	ready        bool
	readyAt      time.Time // Successful proxy enable; used for stable retry reset.
	stopping     bool
}

func (a *Application) Start() error {
	// Manual intent supersedes a timer before waiting for the lifecycle lock.
	a.mu.Lock()
	a.cancelConnectionPolicyLocked()
	a.connectionPolicy.wanted = true
	generation := a.connectionPolicy.generation
	a.mu.Unlock()
	return a.startCoreForIntent(generation)
}

// Intent is captured synchronously by the request, never recreated by a queued goroutine.
func (a *Application) startCoreForIntent(generation uint64) error {
	a.coreControl.Lock()
	defer a.coreControl.Unlock()
	a.mu.Lock()
	if a.shuttingDown || a.connectionPolicy.generation != generation || !a.connectionPolicy.wanted {
		a.mu.Unlock()
		return errConnectionCancelled
	}
	parent := a.lifecycleContext
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	a.connectionPolicy.connectContext, a.connectionPolicy.connectCancel = ctx, cancel
	a.mu.Unlock()
	err := a.startManagedCore()
	cancel()
	a.mu.Lock()
	a.connectionPolicy.connectContext, a.connectionPolicy.connectCancel = nil, nil
	if err != nil && a.connectionPolicy.generation == generation {
		a.exhaustConnectionPolicyLocked("连接失败，请检查连接诊断后手动重试")
	}
	cancelled := a.connectionPolicy.generation != generation || !a.connectionPolicy.wanted || ctx.Err() != nil && parent.Err() != nil
	a.mu.Unlock()
	if cancelled {
		return a.stopCore()
	}
	return err
}

func (a *Application) startCore() (startErr error) {
	if err := a.coreStartContext().Err(); err != nil {
		return errConnectionCancelled
	}
	if err := a.checkComponentUpdateRecovery(); err != nil {
		return errors.Join(errConnectionPolicyPermanent, err)
	}
	retryable := false
	a.mu.Lock()
	if a.core != nil {
		a.mu.Unlock()
		return errors.New("内核已经运行")
	}
	a.connectionError = ""
	a.broadcastLocked()
	a.mu.Unlock()
	defer func() {
		if startErr != nil {
			if !retryable && !errors.Is(startErr, errConnectionCancelled) {
				startErr = errors.Join(errConnectionPolicyPermanent, startErr)
			}
			a.mu.Lock()
			if a.subscriptionApplying {
				startErr = safeSubscriptionValidationError(startErr)
			}
			a.connectionError = startErr.Error()
			if a.core == nil {
				a.runtime = RuntimeConfig{}
			}
			a.broadcastLocked()
			a.mu.Unlock()
		}
	}()
	if p, ok := a.proxy.(recoverableSystemProxy); ok {
		if err := p.CheckRecoveryBeforeStart(); err != nil {
			return err
		}
	}
	if err := a.checkLegacyCore(); err != nil {
		return err
	}
	a.logger.Info("收到连接请求：开始准备运行配置")
	if _, pending, inspectErr := a.proxy.Inspect(); pending || inspectErr != nil {
		return errors.Join(errConnectionPolicyPermanent, errors.New("上次系统代理恢复失败或状态未知，请先在连接诊断中恢复原系统代理"))
	}
	if !fileExists(filepath.Join(a.baseDir, "runtime", coreExecutable)) {
		return errors.New("缺少 mihomo 内核，请运行 setup.ps1 或重新解压完整安装包")
	}
	if !fileExists(filepath.Join(a.dataDir, "ui", "index.html")) {
		return errors.New("缺少 Zashboard 面板文件，请运行 setup.ps1 或重新解压完整安装包")
	}

	if err := a.ensureAPISecret(); err != nil {
		return fmt.Errorf("准备面板密钥失败: %w", err)
	}
	a.logger.Info("正在读取已导入配置")
	if err := a.syncCurrentSource(); err != nil {
		return fmt.Errorf("读取配置库失败: %w", err)
	}
	_ = os.Remove(a.pendingProfilePath())
	content, err := os.ReadFile(filepath.Join(a.dataDir, "source.yaml"))
	if os.IsNotExist(err) {
		return errors.New("请先导入一份 YAML 配置")
	}
	if err != nil {
		return fmt.Errorf("无法读取当前 YAML 配置: %w", err)
	}
	a.mu.Lock()
	previousRuntime := a.runtime
	a.mu.Unlock()
	runtime, rendered, err := a.prepareConfigWithRules(content, previousRuntime, nil)
	if err != nil {
		return fmt.Errorf("准备运行配置失败: %w", err)
	}
	a.mu.Lock()
	a.lastMixedPort = runtime.MixedPort
	a.mu.Unlock()
	listeners, err := loopbackListeners()
	if err != nil {
		return fmt.Errorf("无法检查内核启动端口: %w", err)
	}
	for _, endpoint := range []struct {
		name string
		port int
	}{
		{"代理端口", runtime.MixedPort}, {"内核 API 端口", runtime.BackendPort}, {"面板端口", runtime.APIPort},
	} {
		if pids := listeners[endpoint.port]; len(pids) > 0 {
			return fmt.Errorf("%s %s 已被占用（监听 PID %v），请检查占用程序后重试", endpoint.name, loopbackAddr(endpoint.port), pids)
		}
	}
	a.logger.Info("运行配置已生成")
	runtimePath := filepath.Join(a.dataDir, "runtime.yaml")
	if err := atomicWrite(runtimePath, rendered, 0o600); err != nil {
		return fmt.Errorf("无法写入运行配置: %w", err)
	}

	a.logger.Info("准备启动内核")
	logFile, err := os.OpenFile(filepath.Join(a.logsDir, "mihomo.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("无法打开内核日志: %w", err)
	}
	logStart, err := logFile.Seek(0, io.SeekEnd)
	if err != nil {
		_ = logFile.Close()
		return fmt.Errorf("无法定位内核日志: %w", err)
	}
	_, _ = fmt.Fprintf(logFile, "\n----- Mihomo Lite %s %s -----\n", AppVersion, time.Now().Format(time.RFC3339))
	cmd := exec.Command(filepath.Join(a.baseDir, "runtime", coreExecutable), "-d", a.dataDir, "-f", runtimePath)
	cmd.Dir = a.baseDir
	cmd.SysProcAttr = hiddenProcessAttrs()
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if err := a.coreStartContext().Err(); err != nil {
		_ = logFile.Close()
		return errConnectionCancelled
	}
	retryable = true
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		return fmt.Errorf("无法启动 mihomo: %w", err)
	}
	identity, identityErr := captureRecoveryProcess(cmd.Process.Pid)
	if identityErr != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		_ = logFile.Close()
		return errors.Join(errConnectionPolicyPermanent, errors.New("无法核验内核进程身份，已取消连接"))
	}
	session := &coreSession{cmd: cmd, identity: identity, pid: cmd.Process.Pid, done: make(chan struct{})}
	if p, ok := a.proxy.(recoverableSystemProxy); ok {
		if err := p.TrackCore(session.identity); err != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			_ = logFile.Close()
			return errors.Join(errConnectionPolicyPermanent, fmt.Errorf("无法保存内核恢复会话: %w", err))
		}
	}
	if err := atomicWrite(filepath.Join(a.dataDir, "core.pid"), []byte(fmt.Sprint(session.pid)), 0o600); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		_ = logFile.Close()
		cleanupErr := a.forgetExitedCore(session.pid)
		return errors.Join(errConnectionPolicyPermanent, fmt.Errorf("无法保存内核 PID: %w", err), cleanupErr)
	}
	panel, err := a.startPanelLifecycleProxy(runtime)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		_ = logFile.Close()
		cleanupErr := a.forgetExitedCore(session.pid)
		return errors.Join(fmt.Errorf("无法在 %s 启动面板代理: %w", loopbackAddr(runtime.APIPort), err), cleanupErr)
	}
	a.mu.Lock()
	a.core = session
	a.corePID = session.pid
	a.runtime = runtime
	a.panelProxy = panel
	a.broadcastLocked()
	a.mu.Unlock()
	go func() {
		waitErr := cmd.Wait()
		_ = logFile.Close()
		session.exitErr = startupExitError(waitErr, logFile.Name(), logStart, runtime.Secret)
		a.handleCoreExit(session.pid, waitErr)
		close(session.done)
	}()

	ctx, cancel := context.WithTimeout(a.coreStartContext(), 12*time.Second)
	defer cancel()
	poll := time.NewTicker(200 * time.Millisecond)
	defer poll.Stop()
	var readinessErr error
	for {
		if ctx.Err() != nil {
			return errors.Join(errConnectionCancelled, a.stopCore())
		}
		readinessErr = probeCoreAPI(ctx, runtime, session.pid)
		if readinessErr == nil {
			if err := checkCoreListener(runtime.MixedPort, session.pid); err != nil {
				readinessErr = fmt.Errorf("代理端口未就绪: %w", err)
			}
		} else {
			readinessErr = fmt.Errorf("内核 API 未就绪: %w", readinessErr)
		}
		if readinessErr == nil {
			active, err := a.enableCoreProxy(session, runtime.MixedPort)
			if !active {
				return errors.New("mihomo 在设置系统代理前已退出，请查看内核日志")
			}
			if err != nil {
				return errors.Join(errConnectionPolicyPermanent, fmt.Errorf("内核已启动，但无法启用系统代理: %w", err))
			}
			return nil
		}
		select {
		case <-session.done:
			return session.exitErr
		case <-ctx.Done():
			return errors.Join(fmt.Errorf("等待 mihomo 就绪超时（12 秒）: %w", readinessErr), a.stopCore())
		case <-poll.C:
		}
	}
}

// Use only this attempt's output, but never rebroadcast untrusted core text.
// Raw core logs remain local diagnostics and may contain sensitive configuration.
func startupExitError(waitErr error, logPath string, start int64, secret string) error {
	reason := "mihomo 在就绪前退出"
	if exit, ok := waitErr.(*exec.ExitError); ok {
		reason += fmt.Sprintf("（退出码 %d）", exit.ExitCode())
	}
	file, err := os.Open(logPath)
	if err == nil {
		defer file.Close()
		if end, err := file.Seek(0, io.SeekEnd); err == nil {
			if _, err = file.Seek(max(start, end-(64<<10)), io.SeekStart); err == nil {
				text, _ := io.ReadAll(io.LimitReader(file, 64<<10))
				lines := strings.Split(string(text), "\n")
				for i := len(lines) - 1; i >= 0; i-- {
					line := strings.TrimSpace(lines[i])
					if strings.Contains(line, "level=fatal") || strings.Contains(line, "level=error") {
						return errors.Join(errConnectionPolicyPermanent, errors.New(reason+"：本次内核报告配置或资源错误；请检查订阅及外部资源。原始内核日志仅供本机排查，可能包含敏感信息，请勿分享"))
					}
				}
			}
		}
	}
	return errors.New(reason + "，请在本机查看本次内核日志（可能包含敏感信息，请勿分享）")
}

// Serialize proxy ownership with session exit/Stop. A core that exits during
// Enable must not leave a newly enabled proxy without a cleanup owner.
func (a *Application) enableCoreProxy(session *coreSession, mixedPort int) (bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.core != session || session.stopping || !processExists(session.pid) || (a.connectionPolicy.connectContext != nil && a.connectionPolicy.connectContext.Err() != nil) {
		return false, nil
	}
	session.ready = true
	session.proxyStarted = true
	err := a.proxy.Enable("127.0.0.1:"+fmt.Sprint(mixedPort), windowsProxyBypass)
	a.proxyOn = err == nil
	if err != nil {
		a.logger.Error("内核已连接，但系统代理设置失败", "error", err)
	} else {
		session.readyAt = a.connectionNowLocked()
		a.connectionCoreReadyLocked(session)
		a.logger.Info("连接成功，系统代理已开启", "port", mixedPort)
	}
	a.broadcastLocked()
	return true, err
}

// Requires a.mu. Keep the session visible until restoration finishes; Stop
// cannot report success (or a new connection take ownership) before cleanup.
func (a *Application) restoreCoreProxyLocked(session *coreSession) error {
	a.proxyOn = false
	if session == nil || !session.proxyStarted {
		return nil
	}
	err := a.proxy.Disable()
	if err == nil {
		session.proxyStarted = false
	}
	return err
}

func (a *Application) handleCoreExit(pid int, waitErr error) {
	a.mu.Lock()
	if a.corePID != pid {
		a.mu.Unlock()
		return
	}
	session := a.core
	unexpected := session != nil && !session.stopping && (session.ready || !a.operation)
	proxyErr := a.restoreCoreProxyLocked(a.core)
	proxyErr = errors.Join(proxyErr, a.forgetExitedCore(pid))
	a.core = nil
	a.corePID = 0
	a.runtime = RuntimeConfig{}
	panel := a.panelProxy
	a.panelProxy = nil
	a.proxyOn = false
	if proxyErr != nil {
		a.lastError = "mihomo 已退出，但恢复系统代理失败: " + proxyErr.Error()
		a.logger.Error("恢复系统代理失败", "error", proxyErr)
	} else if unexpected {
		a.lastError = "mihomo 意外退出，系统代理已恢复，请查看内核日志"
		if waitErr != nil {
			a.lastError += "（" + waitErr.Error() + "）"
		}
	}
	if proxyErr != nil || unexpected {
		a.connectionError = a.lastError
	}
	a.connectionCoreExitedLocked(session, unexpected, proxyErr)
	a.broadcastLocked()
	if panel != nil {
		_ = panel.Close()
	}
	a.mu.Unlock()
	if waitErr != nil {
		a.logger.Error("mihomo 已退出", "error", waitErr)
	} else {
		a.logger.Info("mihomo 已退出")
	}
}

// Modern recovery owns PID retirement; no unconditional legacy-delete fallback
// may bypass its path/identity checks or discard evidence after a cleanup error.
func (a *Application) forgetExitedCore(pid int) error {
	if p, ok := a.proxy.(recoverableSystemProxy); ok {
		return p.ForgetCore()
	}
	return removeRecoveryCorePIDFile(filepath.Join(a.dataDir, "core.pid"), pid)
}

func (a *Application) Stop() error {
	a.cancelConnectionPolicy()
	a.coreControl.Lock()
	defer a.coreControl.Unlock()
	return a.stopCore()
}

func (a *Application) stopCore() error {
	a.mu.Lock()
	session := a.core
	if session == nil {
		// Retry the complete durable recovery, including PID/journal retirement.
		// Recover never stops a leftover process or overrides a third-party proxy.
		var err error
		if a.proxy.LastError() != "" {
			if p, ok := a.proxy.(recoverableSystemProxy); ok {
				err = p.Recover()
			} else {
				err = a.proxy.Disable()
			}
		}
		a.broadcastLocked()
		a.mu.Unlock()
		return err
	}
	session.stopping = true // The caller owns begin/finish; never invent a busy slot here.
	a.logger.Info("正在停止 mihomo 并恢复系统代理")
	proxyErr := a.restoreCoreProxyLocked(session)
	a.broadcastLocked()
	a.mu.Unlock()
	if p, ok := a.proxy.(recoverableSystemProxy); ok {
		if err := p.CheckCoreStopSafety(); err != nil {
			return errors.Join(proxyErr, err)
		}
	}
	var stopErr error
	if session.cmd != nil && session.cmd.Process != nil {
		stopErr = stopRecoveryProcess(session.identity)
	}
	select {
	case <-session.done:
	case <-time.After(5 * time.Second):
		if session.cmd != nil && session.cmd.Process != nil {
			stopErr = errors.Join(stopErr, stopRecoveryProcess(session.identity))
		}
		select {
		case <-session.done:
		case <-time.After(3 * time.Second):
		}
	}
	if stopErr != nil {
		return errors.Join(proxyErr, stopErr)
	}
	select {
	case <-session.done:
	default:
		return errors.Join(proxyErr, errors.New("内核尚未退出，保留恢复记录"))
	}
	a.mu.Lock()
	panel := a.panelProxy
	a.panelProxy = nil
	if a.core == session {
		a.core = nil
		a.corePID = 0
		a.runtime = RuntimeConfig{}
		a.proxyOn = false
		a.connectionCoreExitedLocked(session, false, nil)
	}
	a.broadcastLocked()
	if panel != nil {
		_ = panel.Close()
	}
	a.mu.Unlock()
	cleanupErr := a.forgetExitedCore(session.pid)
	a.logger.Info("已断开连接")
	return errors.Join(proxyErr, cleanupErr)
}

func openURL(url string) { _ = exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start() }
