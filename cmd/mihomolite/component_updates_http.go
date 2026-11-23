package main

import (
	"context"
	"errors"
	"io"
	"net/http"
)

// Call after acquiring the application instance lock and before serving HTTP or
// starting a core. Failure leaves a non-nil manager with recoveryBlocked=true so
// the UI can explain the refusal instead of hiding the pending transaction.
func (a *Application) initComponentUpdates() error {
	m, err := newComponentUpdateManager(a.baseDir)
	if err != nil {
		m = &componentUpdateManager{root: a.baseDir, blocked: true, views: map[string]componentView{}, prepared: map[string]componentDownload{}, latest: map[string]componentTransaction{}}
		for _, c := range []string{"launcher", "core", "panel"} {
			trust := "configured"
			if c == "launcher" {
				trust = "unconfigured"
			}
			m.views[c] = componentView{Component: c, TrustStatus: trust, Repository: componentRepository(c), Progress: componentProgress{Phase: "failed", Error: errComponentRecovery.Error()}}
		}
		a.componentUpdates = m
		return errComponentRecovery
	}
	a.componentUpdates = m
	nativeGuard := m.guard
	m.guard = func(c string) error {
		a.mu.Lock()
		running := a.core != nil || a.corePID != 0 || a.proxyOn
		a.mu.Unlock()
		if running {
			return errComponentStopped
		}
		return nativeGuard(c)
	}
	return m.recover()
}

// Hook in startCore before accessing the executable or panel. The application
// serializes start/update with coreControl; this guard never acquires that lock.
func (a *Application) checkComponentUpdateRecovery() error {
	if a.componentUpdates != nil && a.componentUpdates.isBlocked() {
		return errComponentRecovery
	}
	return nil
}
func (a *Application) componentDisconnected() bool {
	a.mu.Lock()
	running := a.core != nil || a.corePID != 0 || a.proxyOn
	a.mu.Unlock()
	if running {
		return false
	}
	return a.componentUpdates != nil && a.componentUpdates.guard != nil && a.componentUpdates.guard("core") == nil
}
func componentAPIError(err error) string {
	for _, known := range []error{errComponentTrust, errComponentStopped, errComponentControlled, errComponentRecovery, errComponentBusy, errComponentUnconfigured} {
		if errors.Is(err, known) {
			return known.Error()
		}
	}
	// The component subsystem never logs downloaded response bodies, filesystem
	// contents, credentials or arbitrary OS errors through application state/SSE.
	return "组件操作失败，未提交或已恢复原组件；请检查官方资产与本地安全更新槽后重试"
}
func (a *Application) componentsHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if a.componentUpdates == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "组件更新未初始化"})
		return
	}
	m := a.componentUpdates
	if r.Method == http.MethodGet {
		writeJSON(w, http.StatusOK, m.status(a.componentDisconnected()))
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "不支持的方法"})
		return
	}
	if r.URL.RawQuery != "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "组件操作不接受查询参数或路径"})
		return
	}
	var input struct {
		Action    string `json:"action"`
		Component string `json:"component"`
		Confirm   bool   `json:"confirm,omitempty"`
	}
	data, readErr := io.ReadAll(http.MaxBytesReader(w, r.Body, 1024))
	if readErr != nil || componentJSONExact(data, &input) != nil || (input.Component != "launcher" && componentRepository(input.Component) == "") {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "组件请求无效：仅接受固定组件与动作，禁止 URL 或路径输入"})
		return
	}
	switch input.Action {
	case "check", "download", "install", "rollback":
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "组件动作无效"})
		return
	}
	mutating := input.Action == "install" || input.Action == "rollback"
	if mutating && !input.Confirm {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "安装或回滚需要明确 confirm=true，并先断开连接"})
		return
	}
	if err := a.begin(); err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": componentAPIError(err)})
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	if a.lifecycleContext != nil {
		stop := context.AfterFunc(a.lifecycleContext, cancel)
		defer stop()
	}
	var err error
	if mutating {
		a.coreControl.Lock()
		if !a.componentDisconnected() {
			err = errComponentStopped
		} else if input.Action == "install" {
			err = m.install(ctx, input.Component)
		} else {
			err = m.rollback(ctx, input.Component)
		}
		a.coreControl.Unlock()
	} else if input.Action == "check" {
		err = m.check(ctx, input.Component)
	} else {
		err = m.download(ctx, input.Component)
	}
	// Keep application logs/state free of raw transport/filesystem error strings.
	if err != nil {
		a.finish(errors.New(componentAPIError(err)))
		writeJSON(w, http.StatusConflict, map[string]string{"error": componentAPIError(err)})
		return
	}
	a.finish(nil)
	writeJSON(w, http.StatusOK, m.status(a.componentDisconnected()))
}
