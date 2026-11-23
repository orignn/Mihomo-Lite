package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"time"
)

// The dashboard manages live proxies/rules/connections directly through the
// core. Only process-lifecycle operations are intercepted for this launcher.
type panelLifecycleProxy struct {
	server *http.Server
	app    *Application
	target *url.URL
	proxy  *httputil.ReverseProxy
}

func (a *Application) startPanelLifecycleProxy(runtime RuntimeConfig) (*panelLifecycleProxy, error) {
	target, err := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", runtime.BackendPort))
	if err != nil {
		return nil, err
	}
	p := &panelLifecycleProxy{app: a, target: target}
	p.proxy = httputil.NewSingleHostReverseProxy(target)
	p.proxy.Transport = &http.Transport{Proxy: nil}
	p.proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, err error) {
		http.Error(w, "核心暂时不可用，请稍后重试", http.StatusBadGateway)
	}
	// Reserve the desired port without racing another process for it.
	ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(runtime.APIPort))
	if err != nil {
		return nil, err
	}
	p.server = &http.Server{Handler: p}
	go func() {
		if err := p.server.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			a.logger.Error("面板代理服务失败", "error", err)
		}
	}()
	return p, nil
}

func (p *panelLifecycleProxy) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	return p.server.Shutdown(ctx)
}

func (p *panelLifecycleProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost && r.URL.Path == "/restart" {
		p.restart(w, r)
		return
	}
	if r.Method == http.MethodPost && (r.URL.Path == "/upgrade" || r.URL.Path == "/upgrade/ui") {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]string{"message": "Mihomo Lite 托管核心和面板；请使用发布包或 setup.ps1 更新，不能由面板替换运行文件。"})
		return
	}
	p.proxy.ServeHTTP(w, r)
}

func (p *panelLifecycleProxy) restart(w http.ResponseWriter, r *http.Request) {
	p.app.mu.Lock()
	runtime := p.app.runtime
	p.app.mu.Unlock()
	if !securePanelAuthorization(r, runtime.Secret) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if err := p.app.requestReconnect(); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func securePanelAuthorization(r *http.Request, secret string) bool {
	if secret == "" {
		return false
	}
	got := r.Header.Get("Authorization")
	if len(got) < 8 || got[:7] != "Bearer " {
		return false
	}
	return got[7:] == secret
}
