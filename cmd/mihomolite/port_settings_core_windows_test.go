//go:build windows

package main

import (
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// This backend is process-local memory only. It cannot touch WinINet, registry,
// recovery files, or another process. It is never wired into the release.
type portCoreMemoryProxy struct{ target string }

func (p *portCoreMemoryProxy) Enable(target, _ string) error { p.target = target; return nil }
func (p *portCoreMemoryProxy) Disable() error                { p.target = ""; return nil }
func (*portCoreMemoryProxy) LastError() string               { return "" }
func (*portCoreMemoryProxy) Inspect() (proxySnapshot, bool, error) {
	return proxySnapshot{}, false, nil
}

func TestPortSettingsOfficialCoreReload(t *testing.T) {
	if os.Getenv("MIHOMOLITE_TEST_OPT010_CORE") != "1" {
		t.Skip("requires explicit isolated official-core fixed-port gate")
	}
	root := draftTestWorkspace(t)
	source := os.Getenv("MIHOMOLITE_OPT010_TEST_CORE")
	absolute, err := filepath.Abs(source)
	relative, relErr := filepath.Rel(filepath.Join(root, ".cache", "tests"), absolute)
	if source == "" || err != nil || relErr != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) || checkBackupPath(absolute) != nil {
		t.Fatal("core must be an explicit non-reparse copy inside the test cache")
	}
	parent := filepath.Join(root, ".cache", "tests", "opt-009-010", "native-core")
	if err := checkBackupPath(parent); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(parent, 0700); err != nil {
		t.Fatal(err)
	}
	run, err := os.MkdirTemp(parent, "run-")
	if err != nil {
		t.Fatal(err)
	}
	logs := NewLogStore(50)
	proxy := &portCoreMemoryProxy{}
	app := &Application{baseDir: run, dataDir: filepath.Join(run, "data"), logsDir: filepath.Join(run, "logs"), config: &Settings{Listen: "127.0.0.1:17653", APISecret: "SYNTHETIC-OPT010-NATIVE-SECRET"}, logger: slog.New(logs), logStore: logs, events: NewEventHub(), proxy: proxy}
	if err := copyFile(absolute, filepath.Join(run, "runtime", coreExecutable)); err != nil {
		t.Fatal(err)
	}
	if err := atomicWrite(filepath.Join(app.dataDir, "ui", "index.html"), []byte("<!doctype html><title>isolated fixture</title>"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(app.logsDir, 0700); err != nil {
		t.Fatal(err)
	}
	input := []byte("dns: {enable: false}\nlog-level: warning\nproxies:\n - {name: Synthetic direct, type: direct}\nrules: ['MATCH,DIRECT']\n")
	if err := atomicWrite(filepath.Join(app.dataDir, "source.yaml"), input, 0600); err != nil {
		t.Fatal(err)
	}
	held := []net.Listener{}
	ports := []int{}
	for i := 0; i < 4; i++ {
		ln, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		held = append(held, ln)
		ports = append(ports, ln.Addr().(*net.TCPAddr).Port)
	}
	defer func() {
		for _, ln := range held {
			_ = ln.Close()
		}
	}()
	preferences := PortSettings{Proxy: PortOption{Mode: "fixed", Port: ports[0]}, Backend: PortOption{Mode: "fixed", Port: ports[1]}, Panel: PortOption{Mode: "fixed", Port: ports[2]}, Web: PortOption{Mode: "fixed", Port: ports[3]}}
	app.config.Listen = loopbackAddr(ports[3])
	if _, err := app.updateRuntimeSettings(RuntimeSettingsPatch{Ports: &preferences}); err != nil {
		t.Fatal(err)
	}
	// Re-create Application from saved settings, exactly the production restart path
	// after LoadSettings, without a native launcher window or registry activity.
	settings, err := LoadSettings(filepath.Join(run, "launcher.json"))
	if err != nil {
		t.Fatal(err)
	}
	app.config = settings
	app.activePorts = nil
	for _, ln := range held {
		_ = ln.Close()
	}
	held = nil
	web, err := app.listenWeb()
	if err != nil {
		t.Fatal(err)
	}
	defer web.Close()
	for attempt := 0; attempt < 2; attempt++ {
		if err := app.Start(); err != nil {
			_ = app.Stop()
			t.Fatalf("isolated fixed-port core failed: %v", err)
		}
		if app.runtime.MixedPort != ports[0] || app.runtime.BackendPort != ports[1] || app.runtime.APIPort != ports[2] || app.activeWebPort != ports[3] || proxy.target != loopbackAddr(ports[0]) {
			_ = app.Stop()
			t.Fatal("fixed endpoints were not reused")
		}
		client := &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 3 * time.Second}
		request, _ := http.NewRequest(http.MethodGet, "http://"+loopbackAddr(ports[2])+"/version", nil)
		request.Header.Set("Authorization", "Bearer "+settings.APISecret)
		response, err := client.Do(request)
		if err != nil {
			_ = app.Stop()
			t.Fatal("isolated panel API failed")
		}
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
		if response.StatusCode != http.StatusOK {
			_ = app.Stop()
			t.Fatal("isolated authorized panel API rejected")
		}
		request, _ = http.NewRequest(http.MethodGet, "http://"+loopbackAddr(ports[2])+"/version", nil)
		response, err = client.Do(request)
		if err != nil {
			_ = app.Stop()
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
		if response.StatusCode == http.StatusOK {
			_ = app.Stop()
			t.Fatal("panel API unexpectedly accepted missing secret")
		}
		client.CloseIdleConnections()
		if err := app.Stop(); err != nil {
			t.Fatal(err)
		}
		if proxy.target != "" {
			t.Fatal("memory proxy did not stop")
		}
		reloaded, err := LoadSettings(filepath.Join(run, "launcher.json"))
		if err != nil || reloaded.Ports != preferences || reloaded.APISecret != settings.APISecret {
			t.Fatal("fixed ports or secret changed across stop/reload")
		}
		app.config = reloaded
		app.activePorts = nil
	}
	t.Log("isolated official core fixed-port restart/reload evidence: " + run)
}
