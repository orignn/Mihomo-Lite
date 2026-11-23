package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"encoding/json"
)

type Application struct {
	baseDir  string
	dataDir  string
	logsDir  string
	config   *Settings
	logger   *slog.Logger
	logStore *LogStore
	events   *EventHub
	proxy    systemProxy

	mu                    sync.Mutex
	coreControl           sync.Mutex
	connectionPolicy      connectionPolicyRuntime
	componentUpdates      *componentUpdateManager
	shuttingDown          bool
	operation             bool
	operationDone         chan struct{}
	subscriptionApplying  bool
	lifecycleContext      context.Context
	core                  *coreSession
	corePID               int
	runtime               RuntimeConfig
	activePorts           *PortSettings // Immutable preferences for this program lifetime.
	activeWebPort         int
	panelProxy            *panelLifecycleProxy
	proxyOn               bool
	lastError             string
	connectionError       string
	lastMixedPort         int
	profileRevision       atomic.Uint64
	backupFiles           sync.RWMutex
	backupRecoveryBlocked bool
	lastBackupSnapshot    string
	savedFileState        struct {
		profiles, rules    int
		hasConfig, pending bool
	}
}

func main() {
	if len(os.Args) == 5 && os.Args[1] == "--console-helper" {
		parentPID, err := strconv.Atoi(os.Args[2])
		if err != nil || parentPID <= 0 {
			os.Exit(1)
		}
		if err := runConsoleHelper(parentPID, os.Args[3], os.Args[4]); err != nil {
			os.Exit(1)
		}
		return
	}
	detachConsole()
	exe, err := os.Executable()
	must(err)
	baseDir := filepath.Dir(exe)
	if filepath.Base(baseDir) == "mihomolite" {
		baseDir = filepath.Dir(baseDir)
	}
	silent := false
	for _, arg := range os.Args[1:] {
		if arg == "--silent" {
			silent = true
		}
	}
	if len(os.Args) > 2 && os.Args[1] == "--dir" {
		baseDir, err = filepath.Abs(os.Args[2])
		must(err)
	} else if len(os.Args) > 1 && os.Args[1] == "--dev" {
		baseDir, err = os.Getwd()
		must(err)
	}
	// Claim the instance before reading/migrating any shared configuration.
	showEvent, err := createShowEvent()
	must(err)
	defer closeShowEvent(showEvent)
	release, first, err := acquireSingleInstance()
	must(err)
	if !first {
		if !silent {
			_ = notifyExistingInstance()
		}
		return
	}
	defer release()
	must(os.MkdirAll(filepath.Join(baseDir, "data"), 0o755))

	releaseRecoveryGuard, err := acquireProxyRecoveryGuard()
	must(err)
	defer releaseRecoveryGuard()
	startupProxy := newApplicationProxy()
	var recoveryErr error
	if recoverable, ok := startupProxy.(recoverableSystemProxy); ok {
		recoveryErr = recoverable.InitializeRecovery(filepath.Join(baseDir, "data"))
	}
	must(os.MkdirAll(filepath.Join(baseDir, "logs"), 0o755))
	// Recovery precedes settings parsing, migration and listener setup.
	backupRecovery := &Application{baseDir: baseDir, dataDir: filepath.Join(baseDir, "data")}
	must(backupRecovery.recoverInterruptedBackupImports())
	settings, err := LoadSettings(filepath.Join(baseDir, "launcher.json"))
	must(err)
	// The command line can force silence for one launch. Otherwise remember
	// the user's normal-start preference; autostart always passes --silent.
	silent = silent || settings.SilentStart
	settings.AutoStart, err = readAutoStartEnabled()
	must(err)
	logStore := NewLogStore(200)
	logger := slog.New(logStore)
	app := &Application{
		baseDir: baseDir,
		dataDir: filepath.Join(baseDir, "data"),
		logsDir: filepath.Join(baseDir, "logs"),
		config:  settings,
		logger:  logger, logStore: logStore, events: NewEventHub(),
		proxy: startupProxy,
	}
	appLog, logErr := os.OpenFile(filepath.Join(app.logsDir, "launcher.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if logErr == nil {
		_, _ = fmt.Fprintf(appLog, "\n[%s] launcher %s start\n", time.Now().Format(time.RFC3339), AppVersion)
		defer appLog.Close()
		app.logger = slog.New(multiHandler{handlers: []slog.Handler{logStore, slog.NewTextHandler(appLog, nil)}})
	}
	if err := app.initComponentUpdates(); err != nil {
		app.connectionError = "组件更新恢复未完成，请检查组件更新记录"
		app.logger.Error("组件更新恢复未完成")
	}
	if err := app.migrateLegacySource(); err != nil {
		app.logger.Error("旧配置迁移失败", "error", err)
	}
	app.events.current = func() State { return app.State() }

	if recoveryErr != nil {
		app.connectionError = recoveryErr.Error()
		app.logger.Error("旧会话恢复未完成，请打开连接诊断", "error", recoveryErr)
	}
	if err := app.checkLegacyCore(); err != nil {
		app.connectionError = err.Error()
		app.logger.Warn("存在未核验的旧 PID 记录，请打开连接诊断")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	app.lifecycleContext = ctx
	shutdownDone := make(chan struct{})
	server := &http.Server{Addr: settings.Listen, Handler: app.Routes(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		defer close(shutdownDone)
		<-ctx.Done()
		app.mu.Lock()
		app.shuttingDown = true
		app.mu.Unlock()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			_ = server.Close()
		}
	}()
	go func() {
		select {
		case <-ctx.Done():
		case <-consoleSignal:
			stop()
		}
		// The main goroutine performs cleanup once after Serve returns.
		// Concurrent Stop calls could restore the proxy and then disable it again.
	}()

	ln, err := app.listenWeb()
	must(err)
	url := "http://" + settings.Listen + "/"
	must(setCloseToTray(settings.CloseToTray))
	must(initConsoleController(url, app.logger))
	go watchShowEvent(showEvent)
	go runTray(app, settings, stop)
	if !silent {
		showConsoleWindow()
	}
	if os.Getenv("MIHOMOLITE_NO_BROWSER") != "1" && !silent {
		_ = exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	}
	updatesDone := make(chan struct{})
	go func() {
		defer close(updatesDone)
		app.runSubscriptionUpdates(ctx)
	}()
	app.startConnectionPolicy(ctx)
	err = server.Serve(ln)
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		must(err)
	}
	stop()
	app.stopConnectionPolicy()
	<-updatesDone
	<-shutdownDone
	app.waitForOperation()
	if err := app.Stop(); err != nil {
		app.logger.Error("退出清理未完成，已保留恢复记录；下次启动请打开连接诊断", "error", err)
	}
}

func must(err error) {
	if err == nil {
		return
	}
	fmt.Fprintln(os.Stderr, "启动失败:", err)
	if os.Getenv("MIHOMOLITE_NO_PAUSE") == "" {
		showStartupError(err)
	}
	os.Exit(1)
}

func (a *Application) State() State {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.stateLocked()
}

func (a *Application) stateLocked() State {
	running := a.core != nil && a.core.cmd.Process != nil && processExists(a.corePID)
	if a.backupFiles.TryRLock() {
		a.savedFileState.profiles = a.profileCount()
		a.savedFileState.rules = a.enabledRuleCount()
		a.savedFileState.hasConfig = fileExists(filepath.Join(a.dataDir, "source.yaml"))
		a.savedFileState.pending = fileExists(a.pendingProfilePath())
		a.backupFiles.RUnlock()
	}
	return State{
		RuntimeSettings: a.runtimeSettingsLocked(),
		ConnectionRetry: a.connectionRetryStateLocked(),
		Running:         running, PID: a.corePID, Busy: a.operation,
		ProxyEnabled: running && a.proxyOn, ProxyError: a.proxy.LastError(),
		ConfigName: a.config.ConfigName, HasConfig: a.savedFileState.hasConfig,
		CoreReady:  fileExists(filepath.Join(a.baseDir, "runtime", coreExecutable)),
		PanelReady: fileExists(filepath.Join(a.dataDir, "ui", "index.html")),
		PanelURL:   a.runtime.PanelURL, HasPanelURL: running && a.runtime.PanelURL != "",
		APIPort: a.runtime.APIPort, MixedPort: a.runtime.MixedPort,
		LastError: a.lastError, ConnectionError: a.connectionError, Version: AppVersion,
		CanImport: !running && !a.operation, CanConnect: !running && !a.operation && fileExists(filepath.Join(a.dataDir, "source.yaml")) && fileExists(filepath.Join(a.baseDir, "runtime", coreExecutable)) && fileExists(filepath.Join(a.dataDir, "ui", "index.html")),
		CanDisconnect:   (running || a.connectionPolicy.waiting || a.connectionPolicy.attempting) && !a.shuttingDown && (!a.operation || a.connectionPolicy.waiting || a.connectionPolicy.attempting),
		CustomRules:     a.savedFileState.rules,
		ProfileCount:    a.savedFileState.profiles,
		ProfilePending:  a.savedFileState.pending,
		ProfileRevision: a.profileRevision.Load(),
	}
}

func (a *Application) begin() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.beginLocked()
}

// beginLocked atomically reserves an operation and its original connection intent.
func (a *Application) beginLocked() error {
	if a.shuttingDown {
		return errors.New("启动器正在退出")
	}
	if a.backupRecoveryBlocked {
		return errors.New("配置恢复事务未完成，请退出并重新启动以恢复原数据")
	}
	if a.operation {
		return errors.New("另一个操作正在进行，请稍候")
	}
	a.operation = true
	a.operationDone = make(chan struct{})
	a.connectionPolicy.operationGeneration = a.connectionPolicy.generation
	a.lastError = ""
	a.broadcastLocked()
	return nil
}

func (a *Application) finish(err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err != nil {
		a.lastError = err.Error()
		a.logger.Error("操作失败", "error", err)
	}
	a.operation = false
	if a.operationDone != nil {
		close(a.operationDone)
		a.operationDone = nil
	}
	a.broadcastLocked()
}

func (a *Application) waitForOperation() {
	a.mu.Lock()
	done := a.operationDone
	a.mu.Unlock()
	if done != nil {
		<-done
	}
}

func (a *Application) subscriptionContext(request context.Context) context.Context {
	if a.lifecycleContext != nil {
		return a.lifecycleContext
	}
	return context.WithoutCancel(request)
}

func (a *Application) broadcastLocked() { a.events.Publish(a.stateLocked()) }

func (a *Application) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", a.handleIndex)
	mux.HandleFunc("GET /api/state", a.handleState)
	mux.HandleFunc("GET /api/diagnostics", a.handleDiagnostics)
	mux.HandleFunc("POST /api/repair-proxy", a.handleRepairProxy)
	mux.HandleFunc("POST /api/restore-proxy", a.handleRestoreProxy)
	mux.HandleFunc("POST /api/recovery/keep-current-proxy", a.handleKeepCurrentProxy)
	mux.HandleFunc("POST /api/recovery/stop-leftover-core", a.handleStopLeftoverCore)
	mux.HandleFunc("POST /api/open-proxy-settings", a.handleOpenProxySettings)
	mux.HandleFunc("GET /api/logs", a.handleLogs)
	mux.HandleFunc("GET /api/events", a.handleEvents)
	mux.HandleFunc("GET /api/config", a.handleConfig)
	mux.HandleFunc("GET /api/overrides", a.Overrides)
	mux.HandleFunc("POST /api/overrides", a.Overrides)
	mux.HandleFunc("POST /api/overrides/form", a.OverrideForm)
	mux.HandleFunc("POST /api/config/preview", a.ConfigPreview)
	mux.HandleFunc("GET /api/components", a.componentsHTTP)
	mux.HandleFunc("POST /api/components", a.componentsHTTP)
	mux.HandleFunc("GET /api/profiles", a.Profiles)
	mux.HandleFunc("POST /api/profiles", a.Profiles)
	mux.HandleFunc("GET /api/backup", a.Backup)
	mux.HandleFunc("POST /api/backup", a.Backup)
	mux.HandleFunc("GET /api/profiles/{id}/update-diff", a.handleSubscriptionDiff)
	mux.HandleFunc("GET /api/rules", a.Rules)
	mux.HandleFunc("GET /api/rules/test", a.RuleTest)
	mux.HandleFunc("GET /api/core-logs", a.handleCoreLogs)
	mux.HandleFunc("PUT /api/rules", a.Rules)
	mux.HandleFunc("POST /api/import", a.handleImport)
	mux.HandleFunc("POST /api/connect", a.handleConnect)
	mux.HandleFunc("POST /api/disconnect", a.handleDisconnect)
	mux.HandleFunc("POST /api/reconnect", a.handleReconnect)
	mux.HandleFunc("GET /api/panel-secret", a.handlePanelSecret)
	mux.HandleFunc("GET /api/settings", a.handleSettings)
	mux.HandleFunc("PUT /api/settings", a.handleSettings)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		if host != "127.0.0.1" && host != "localhost" && host != "[::1]" && host != "::1" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
			if r.Header.Get("X-Mihomo-Lite") != "1" || !sameRequestOrigin(r) {
				writeJSON(w, http.StatusForbidden, errorResponse{Error: "请求校验失败，请刷新页面后重试"})
				return
			}
		}
		if r.Method == http.MethodGet && (r.URL.Path == "/api/profiles" || r.URL.Path == "/api/config" || r.URL.Path == "/api/rules" || strings.HasSuffix(r.URL.Path, "/update-diff")) {
			a.backupFiles.RLock()
			defer a.backupFiles.RUnlock()
		}
		mux.ServeHTTP(w, r)
	})
}

// Origin is optional for non-browser clients; a supplied value must be a single
// serialized origin matching the request scheme, host and effective port.
func sameRequestOrigin(r *http.Request) bool {
	origins := r.Header.Values("Origin")
	if len(origins) == 0 {
		return true
	}
	if len(origins) != 1 {
		return false
	}
	origin, err := url.Parse(origins[0])
	if err != nil || origin.Host == "" || origin.User != nil || origin.Opaque != "" ||
		origin.Path != "" || origin.RawQuery != "" || origin.ForceQuery || strings.Contains(origins[0], "#") {
		return false
	}
	scheme, defaultPort := "http", "80"
	if r.TLS != nil {
		scheme, defaultPort = "https", "443"
	}
	if origin.Scheme != scheme {
		return false
	}
	target := &url.URL{Scheme: scheme, Host: r.Host}
	originPort, targetPort := origin.Port(), target.Port()
	if originPort == "" {
		originPort = defaultPort
	}
	if targetPort == "" {
		targetPort = defaultPort
	}
	return strings.EqualFold(origin.Hostname(), target.Hostname()) && originPort == targetPort
}

func (a *Application) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(indexHTML))
}

func (a *Application) handleState(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.State())
}
func (a *Application) handleLogs(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.logStore.Entries())
}

func (a *Application) handleEvents(w http.ResponseWriter, r *http.Request) { a.events.ServeHTTP(w, r) }

func (a *Application) handleConnect(w http.ResponseWriter, r *http.Request) {
	if err := a.requestConnect(); err != nil {
		writeJSON(w, http.StatusConflict, errorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusAccepted, a.State())
}

func (a *Application) handleDisconnect(w http.ResponseWriter, r *http.Request) {
	if err := a.requestDisconnect(); err != nil {
		writeJSON(w, http.StatusConflict, errorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusAccepted, a.State())
}

func (a *Application) handleReconnect(w http.ResponseWriter, r *http.Request) {
	if err := a.requestReconnect(); err != nil {
		writeJSON(w, http.StatusConflict, errorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusAccepted, a.State())
}

func (a *Application) handleConfig(w http.ResponseWriter, r *http.Request) {
	store, err := a.loadProfileStore()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: err.Error()})
		return
	}
	path := filepath.Join(a.dataDir, "source.yaml")
	if store.ActiveID != "" {
		path = a.profileContentPath(store.ActiveID)
	}
	content, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		content = []byte(defaultConfig)
		err = nil
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"content": string(content), "profileId": store.ActiveID})
}

func (a *Application) handleImport(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Content string `json:"content"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4<<20)
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}
	if err := a.begin(); err != nil {
		writeJSON(w, http.StatusConflict, errorResponse{Error: "请先断开连接或等待当前操作完成"})
		return
	}
	name, err := a.Import(strings.NewReader(req.Content))
	a.finish(err)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"name": name})
}

func (a *Application) handleCoreLogs(w http.ResponseWriter, r *http.Request) {
	text, err := readFileTail(filepath.Join(a.logsDir, "mihomo.log"), 256<<10)
	if err != nil && !os.IsNotExist(err) {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"text": text})
}

func readFileTail(path string, maxBytes int64) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	stat, err := file.Stat()
	if err != nil {
		return "", err
	}
	offset := int64(0)
	if stat.Size() > maxBytes {
		offset = stat.Size() - maxBytes
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return "", err
	}
	content, err := io.ReadAll(io.LimitReader(file, maxBytes))
	if err != nil {
		return "", err
	}
	text := string(content)
	if offset > 0 {
		if idx := strings.IndexByte(text, '\n'); idx >= 0 {
			text = text[idx+1:]
		}
	}
	return text, nil
}

func (a *Application) handleSettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		if err := a.syncAutoStartFromRegistry(); err != nil {
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, a.RuntimeSettings())
	case http.MethodPut:
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		req, decodeErr := decodeRuntimeSettingsPatchStrict(r)
		if err := decodeErr; err != nil {
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
			return
		}
		settings, err := a.updateRuntimeSettings(req)
		if err != nil {
			status := http.StatusInternalServerError
			if errors.Is(err, errPortSettings) || errors.Is(err, errConnectionPolicy) {
				status = http.StatusBadRequest
			}
			writeJSON(w, status, errorResponse{Error: err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, settings)
	default:
		w.Header().Set("Allow", "GET, PUT")
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Error: "不支持的请求方法"})
	}
}

func (a *Application) handlePanelSecret(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.core == nil {
		writeJSON(w, http.StatusConflict, errorResponse{Error: "请先连接"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"secret": a.runtime.Secret})
}

func writeJSON(w http.ResponseWriter, code int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(value)
}

func decodeJSON(r *http.Request, v any) error {
	if r.Method != http.MethodGet && r.Header.Get("X-Mihomo-Lite") != "1" {
		return errors.New("请求校验失败，请刷新页面后重试")
	}
	return json.NewDecoder(r.Body).Decode(v)
}
