package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const AppVersion = "0.6.18"

const defaultConfig = "# 请粘贴完整的 mihomo / Clash YAML 配置，或点击“选择 YAML 文件”。\n# 只需要配置节点和规则；端口、面板地址和密钥由启动器接管。\n# 支持 proxies 或 proxy-providers；也可粘贴 SS、VMess、VLESS、Trojan、Hysteria2、TUIC 的明文或 Base64 分享链接，启动器会先转换并校验。\n\nmode: rule\nlog-level: warning\nproxies: []\nrules:\n  - MATCH,DIRECT\n"

type Settings struct {
	Listen           string           `json:"listen"`
	ConfigName       string           `json:"configName"`
	APISecret        string           `json:"apiSecret"`
	AutoStart        bool             `json:"autoStart"`
	CloseToTray      bool             `json:"closeToTray"`
	SilentStart      bool             `json:"silentStart"`
	Ports            PortSettings     `json:"ports"`
	ConnectionPolicy ConnectionPolicy `json:"connectionPolicy"`
}

func LoadSettings(path string) (*Settings, error) {
	s := &Settings{Listen: "127.0.0.1:17653", CloseToTray: true}
	content, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if len(content) > 0 {
		if err := json.Unmarshal(content, s); err != nil {
			return nil, fmt.Errorf("launcher.json 无效: %w", err)
		}
	}
	host, port, err := net.SplitHostPort(s.Listen)
	if err != nil || (host != "127.0.0.1" && host != "localhost") {
		return nil, fmt.Errorf("控制台只能监听 127.0.0.1")
	}
	p, err := strconv.Atoi(port)
	if err != nil || p < 1 || p > 65535 {
		return nil, fmt.Errorf("控制台端口无效")
	}
	s.Listen = "127.0.0.1:" + strconv.Itoa(p)
	if len(content) > 0 {
		var fields map[string]json.RawMessage
		_ = json.Unmarshal(content, &fields)
		if _, present := fields["ports"]; !present {
			// Legacy listen was always a fixed loopback launcher Web endpoint.
			s.Ports.Web = PortOption{Mode: "fixed", Port: p}
		} else if string(fields["ports"]) == "null" {
			return nil, fmt.Errorf("%w：ports 不能为 null", errPortSettings)
		}
	}
	s.Ports, err = normalizePortSettings(s.Ports)
	if err != nil {
		return nil, err
	}
	s.ConnectionPolicy, err = normalizeConnectionPolicy(s.ConnectionPolicy)
	if err != nil {
		return nil, err
	}
	return s, nil
}

// saveSettings requires a.mu when called after startup.
func (a *Application) saveSettings() error {
	return a.saveSettingsValue(*a.config)
}

func (a *Application) saveSettingsValue(settings Settings) error {
	content, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(filepath.Join(a.baseDir, "launcher.json"), content, 0o600)
}

type State struct {
	ConnectionRetry ConnectionRetryState `json:"connectionRetry"`
	RuntimeSettings
	Running         bool   `json:"running"`
	PID             int    `json:"pid"`
	Busy            bool   `json:"busy"`
	ProxyEnabled    bool   `json:"proxyEnabled"`
	ProxyError      string `json:"proxyError"`
	ConfigName      string `json:"configName"`
	HasConfig       bool   `json:"hasConfig"`
	CoreReady       bool   `json:"coreReady"`
	PanelReady      bool   `json:"panelReady"`
	PanelURL        string `json:"panelURL"`
	HasPanelURL     bool   `json:"hasPanelURL"`
	APIPort         int    `json:"apiPort"`
	MixedPort       int    `json:"mixedPort"`
	LastError       string `json:"lastError"`
	ConnectionError string `json:"connectionError"`
	Version         string `json:"version"`
	CanImport       bool   `json:"canImport"`
	CanConnect      bool   `json:"canConnect"`
	CanDisconnect   bool   `json:"canDisconnect"`
	CustomRules     int    `json:"customRules"`
	ProfileCount    int    `json:"profileCount"`
	ProfilePending  bool   `json:"profilePending"`
	ProfileRevision uint64 `json:"profileRevision"`
}

type LogEntry struct {
	Time    string `json:"time"`
	Level   string `json:"level"`
	Message string `json:"message"`
}

type errorResponse struct {
	Error string `json:"error"`
}

type LogStore struct {
	mu      sync.Mutex
	entries []LogEntry
	limit   int
}

func NewLogStore(limit int) *LogStore                        { return &LogStore{limit: limit} }
func (l *LogStore) Enabled(context.Context, slog.Level) bool { return true }
func (l *LogStore) Handle(_ context.Context, record slog.Record) error {
	message := record.Message
	record.Attrs(func(attr slog.Attr) bool {
		if attr.Key == "error" || attr.Key == "pid" || attr.Key == "port" {
			message += " | " + attr.Key + "=" + attr.Value.String()
		}
		return true
	})
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = append(l.entries, LogEntry{Time: record.Time.Format("15:04:05"), Level: record.Level.String(), Message: message})
	if len(l.entries) > l.limit {
		l.entries = append([]LogEntry(nil), l.entries[len(l.entries)-l.limit:]...)
	}
	return nil
}
func (l *LogStore) WithAttrs([]slog.Attr) slog.Handler { return l }
func (l *LogStore) WithGroup(string) slog.Handler      { return l }
func (l *LogStore) Entries() []LogEntry {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]LogEntry(nil), l.entries...)
}

type EventHub struct {
	mu         sync.Mutex
	nextID     int
	clients    map[int]chan State
	current    func() State
	generation uint64
	latest     State
}

func NewEventHub() *EventHub { return &EventHub{clients: make(map[int]chan State)} }
func (h *EventHub) Publish(state State) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.generation++
	h.latest = state
	for _, ch := range h.clients {
		// State snapshots are replaceable: a slow client needs the newest value,
		// not a stale queue that can leave its settings permanently out of sync.
		select {
		case ch <- state:
		default:
			select {
			case <-ch:
			default:
			}
			select {
			case ch <- state:
			default:
			}
		}
	}
}
func (h *EventHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Connection", "keep-alive")
	ch := make(chan State, 1)
	h.mu.Lock()
	h.nextID++
	id := h.nextID
	h.clients[id] = ch
	generation := h.generation
	h.mu.Unlock()
	defer func() { h.mu.Lock(); delete(h.clients, id); h.mu.Unlock() }()
	// Subscribe before taking the initial snapshot. Updates published while
	// current() runs are queued instead of being lost in a registration gap.
	initial := h.current()
	h.mu.Lock()
	if h.generation != generation {
		initial = h.latest
	}
	// Do not replay an older pre-snapshot update after the current snapshot.
	select {
	case <-ch:
	default:
	}
	h.mu.Unlock()
	send := func(s State) bool {
		content, _ := json.Marshal(s)
		if _, err := fmt.Fprintf(w, "event: state\ndata: %s\n\n", content); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}
	if !send(initial) {
		return
	}
	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case state := <-ch:
			if !send(state) {
				return
			}
		case <-ticker.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func fileExists(path string) bool { info, err := os.Stat(path); return err == nil && !info.IsDir() }
func readInt(path string) (int, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(b)))
}
func atomicWrite(path string, content []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err = tmp.Chmod(mode); err == nil {
		_, err = tmp.Write(content)
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return replaceFile(name, path)
}

// multiHandler keeps both the browser log and a durable launcher diagnostic log.
type multiHandler struct{ handlers []slog.Handler }

func (h multiHandler) Enabled(ctx context.Context, level slog.Level) bool {
	for _, handler := range h.handlers {
		if handler.Enabled(ctx, level) {
			return true
		}
	}
	return false
}
func (h multiHandler) Handle(ctx context.Context, record slog.Record) error {
	for _, handler := range h.handlers {
		if handler.Enabled(ctx, record.Level) {
			_ = handler.Handle(ctx, record.Clone())
		}
	}
	return nil
}
func (h multiHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	next := multiHandler{handlers: make([]slog.Handler, 0, len(h.handlers))}
	for _, handler := range h.handlers {
		next.handlers = append(next.handlers, handler.WithAttrs(attrs))
	}
	return next
}
func (h multiHandler) WithGroup(name string) slog.Handler {
	next := multiHandler{handlers: make([]slog.Handler, 0, len(h.handlers))}
	for _, handler := range h.handlers {
		next.handlers = append(next.handlers, handler.WithGroup(name))
	}
	return next
}
