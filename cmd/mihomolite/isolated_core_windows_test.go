//go:build windows && isolatedtest

package main

import (
	"debug/buildinfo"
	"debug/pe"
	"encoding/json"
	"fmt"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"
)

func coreRequireIsolatedTarget(t *testing.T) {
	t.Helper()
	if os.Getenv("MIHOMOLITE_TEST_CONSOLE") != "1" {
		t.Skip("opt-in isolated Windows integration only")
	}
	if !strings.HasPrefix(instanceNamespace, "P0P1-") || strings.ContainsAny(instanceNamespace, "\\/ ") {
		t.Fatal("unsafe/empty test namespace")
	}
	if autoRunKey != isolatedRegistryBase()+"\\Run" {
		t.Fatal("test autostart key is not isolated")
	}
	exe := os.Getenv("MIHOMOLITE_TEST_LAUNCHER")
	info, err := buildinfo.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	isolated, namespace := false, false
	for _, s := range info.Settings {
		if s.Key == "-tags" {
			for _, tag := range strings.FieldsFunc(s.Value, func(r rune) bool { return r == ',' || r == ' ' }) {
				isolated = isolated || tag == "isolatedtest"
			}
		}
		if s.Key == "-ldflags" {
			for _, f := range strings.Fields(s.Value) {
				namespace = namespace || f == "main.instanceNamespace="+instanceNamespace
			}
		}
	}
	if !isolated || !namespace {
		t.Fatal("target is not the matching explicitly isolated build")
	}
	binary, err := pe.Open(exe)
	if err != nil {
		t.Fatal(err)
	}
	defer binary.Close()
	var subsystem uint16
	switch h := binary.OptionalHeader.(type) {
	case *pe.OptionalHeader64:
		subsystem = h.Subsystem
	case *pe.OptionalHeader32:
		subsystem = h.Subsystem
	}
	if subsystem != 2 {
		t.Fatal("target is not a GUI-subsystem build")
	}
}

func coreSeedPrivateProxy(t *testing.T) proxySnapshot {
	t.Helper()
	before := proxyBackendOriginal()
	if err := (isolatedProxyBackend{}).Write(before); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		path := isolatedProxyRegistryPath()
		if !strings.HasPrefix(path, "Software\\MihomoLite\\Tests\\P0P1-") || strings.HasSuffix(path, "\\Internet Settings") {
			t.Error("refusing non-test registry cleanup")
			return
		}
		if err := registry.DeleteKey(registry.CURRENT_USER, path); err != nil && err != registry.ErrNotExist {
			t.Error(err)
		}
	})
	actual, err := readProxySnapshot()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		after, err := readProxySnapshot()
		if err != nil || after != actual {
			t.Errorf("real Windows proxy changed during isolated test: before=%+v after=%+v err=%v", actual, after, err)
		}
	})
	return before
}

func coreInstallFixture(t *testing.T, f *consoleTestLauncher) {
	t.Helper()
	path := os.Getenv("MIHOMOLITE_TEST_CORE")
	if path == "" {
		t.Fatal("MIHOMOLITE_TEST_CORE must point to the real mihomo executable")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"runtime", "data/ui"} {
		if err := os.MkdirAll(filepath.Join(f.dir, dir), 0755); err != nil {
			t.Fatal(err)
		}
	}
	for path, data := range map[string][]byte{
		"runtime/" + coreExecutable: data,
		"data/ui/index.html":        []byte("isolated local panel"),
		"data/source.yaml":          []byte("mode: rule\nlog-level: info\nproxies:\n  - name: local-direct\n    type: direct\nrules:\n  - MATCH,DIRECT\ndns:\n  enable: false\n"),
	} {
		if err := os.WriteFile(filepath.Join(f.dir, path), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	consoleEventually(t, 3*time.Second, "fixture ready", func() bool { return f.state().CanConnect })
}

func coreConnected(t *testing.T, f *consoleTestLauncher, oldPID int) State {
	t.Helper()
	var s State
	consoleEventually(t, 20*time.Second, "real core connected", func() bool {
		s = f.state()
		return s.Running && s.ProxyEnabled && !s.Busy && (oldPID == 0 || s.PID != oldPID) || s.LastError != "" && !s.Busy
	})
	if !s.Running || !s.ProxyEnabled || s.PID == oldPID || s.LastError != "" {
		t.Fatalf("core connection failed: %+v", s)
	}
	coreAssertOwned(t, s.PID, f.cmd.Process.Pid)
	// Keep a stable handle for failure cleanup: never terminate a recycled PID.
	handle, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_TERMINATE, false, uint32(s.PID))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if result, err := windows.WaitForSingleObject(handle, 0); err == nil && result == uint32(windows.WAIT_TIMEOUT) {
			_ = windows.TerminateProcess(handle, 0)
		}
		_ = windows.CloseHandle(handle)
	})
	return s
}

func coreAssertOwned(t *testing.T, pid, parent int) {
	t.Helper()
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(snapshot)
	entry := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	for err := windows.Process32First(snapshot, &entry); err == nil; err = windows.Process32Next(snapshot, &entry) {
		if entry.ProcessID == uint32(pid) {
			if entry.ParentProcessID != uint32(parent) || !strings.EqualFold(windows.UTF16ToString(entry.ExeFile[:]), coreExecutable) {
				t.Fatal("test core process ownership mismatch")
			}
			return
		}
	}
	t.Fatal("test core process missing")
}

func coreAssertRestored(t *testing.T, before proxySnapshot) {
	t.Helper()
	after, err := (isolatedProxyBackend{}).Read()
	if err != nil || after != before {
		t.Fatalf("private HKCU original proxy not restored: before=%+v after=%+v err=%v", before, after, err)
	}
}

func coreProxyTraffic(t *testing.T, s State) func() {
	t.Helper()
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "proxy-still-alive") }))
	t.Cleanup(endpoint.Close)
	proxyURL, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", s.MixedPort))
	transport := &http.Transport{Proxy: http.ProxyURL(proxyURL)}
	t.Cleanup(transport.CloseIdleConnections)
	client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
	return func() {
		resp, err := client.Get(endpoint.URL)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil || string(body) != "proxy-still-alive" {
			t.Fatalf("real mixed proxy traffic failed: %s %v", body, err)
		}
	}
}

func TestIsolatedCoreTrayLifecycle(t *testing.T) {
	coreRequireIsolatedTarget(t)
	before := coreSeedPrivateProxy(t)
	f := startConsoleTestLauncher(t, false)
	coreInstallFixture(t, f)
	consolePost(t, consoleTrayFor(f.cmd.Process.Pid), 0x111, uintptr(trayCommandConnect))
	s := coreConnected(t, f, 0)
	p0p1AssertMenu(t, f, s.AutoStart, s.CloseToTray)
	traffic := coreProxyTraffic(t, s)
	traffic()
	hwnd := f.window(t)
	helper := consoleChildren(f.cmd.Process.Pid, f.exe)[0]
	for _, pid := range consoleProbe(t, helper, false) {
		if int(pid) == f.cmd.Process.Pid || int(pid) == s.PID {
			t.Fatal("launcher/core attached to disposable console")
		}
	}
	consolePost(t, hwnd, 0x112, 0xf060)
	consoleEventually(t, 5*time.Second, "console closed", func() bool { return !processExists(helper) })
	time.Sleep(16 * time.Second)
	if now := f.state(); !now.Running || now.PID != s.PID || !now.ProxyEnabled || consoleTrayFor(f.cmd.Process.Pid) == 0 {
		t.Fatalf("X changed core/tray: %+v", now)
	}
	traffic()
	consolePost(t, consoleTrayFor(f.cmd.Process.Pid), 0x111, uintptr(trayCommandShow))
	f.window(t)
	consolePost(t, consoleTrayFor(f.cmd.Process.Pid), 0x111, uintptr(trayCommandRestart))
	restarted := coreConnected(t, f, s.PID)
	if processExists(s.PID) {
		t.Fatal("old core survived restart")
	}
	consolePost(t, consoleTrayFor(f.cmd.Process.Pid), 0x111, uintptr(trayCommandConnect))
	consoleEventually(t, 12*time.Second, "tray disconnect", func() bool { st := f.state(); return !st.Running && !st.Busy })
	coreAssertRestored(t, before)
	st := f.state()
	p0p1AssertMenu(t, f, st.AutoStart, st.CloseToTray)
	if processExists(restarted.PID) {
		t.Fatal("core survived disconnect")
	}
	consolePost(t, consoleTrayFor(f.cmd.Process.Pid), 0x111, uintptr(trayCommandConnect))
	final := coreConnected(t, f, 0)
	consolePost(t, consoleTrayFor(f.cmd.Process.Pid), 0x111, uintptr(trayCommandQuit))
	f.waitExit(t)
	if processExists(final.PID) {
		t.Fatal("core survived tray Quit")
	}
	coreAssertRestored(t, before)
	t.Logf("real core/mixed traffic, X survival for 16s, tray Show/Connect/Restart/Disconnect/Quit and private HKCU restoration passed; elevated=%v", windows.GetCurrentProcessToken().IsElevated())
}

func TestP0P1ConnectOpensOnlyOnDemand(t *testing.T) {
	f, output := p0p1StartBrowserFixture(t, true)
	coreInstallFixture(t, f)
	f.request(t, http.MethodPost, "/api/connect", "{}")
	state := coreConnected(t, f, 0)
	p0p1AssertMenu(t, f, state.AutoStart, state.CloseToTray)
	time.Sleep(1200 * time.Millisecond)
	calls, err := p0p1BrowserCalls(output)
	if err != nil || len(calls) != 0 {
		t.Fatalf("connect unexpectedly opened a browser: calls=%+v err=%v", calls, err)
	}

	p0p1Command(t, f, trayCommandLauncher) // Launcher page.
	consoleEventually(t, 5*time.Second, "manual launcher browser invocation", func() bool {
		calls, err = p0p1BrowserCalls(output)
		return err == nil && len(calls) == 1
	})
	if got := calls[0].Args; len(got) != 2 || got[0] != "url.dll,FileProtocolHandler" || got[1] != f.url+"/" {
		t.Fatalf("launcher tray entry opened wrong URL: %+v", calls[0])
	}

	p0p1Command(t, f, trayCommandPanel) // Zashboard panel.
	consoleEventually(t, 5*time.Second, "manual panel browser invocation", func() bool {
		calls, err = p0p1BrowserCalls(output)
		return err == nil && len(calls) == 2
	})
	if got := calls[1].Args; len(got) != 2 || got[0] != "url.dll,FileProtocolHandler" || got[1] != state.PanelURL {
		t.Fatalf("panel tray entry opened wrong URL: got=%+v want=%q", calls[1], state.PanelURL)
	}
	p0p1Command(t, f, trayCommandQuit)
	f.waitExit(t)
	t.Log("connect remained quiet; launcher and Zashboard tray entries opened their exact URLs on demand")
}

func TestZashboardCoreRestartRecoversManagedSession(t *testing.T) {
	f, _ := p0p1StartBrowserFixture(t, true)
	coreInstallFixture(t, f)
	f.request(t, http.MethodPost, "/api/connect", "{}")
	before := coreConnected(t, f, 0)
	beforeProxy, err := readProxySnapshot()
	if err != nil {
		t.Fatal(err)
	}
	beforeURL, err := url.Parse(before.PanelURL)
	if err != nil {
		t.Fatal(err)
	}
	raw := beforeURL.Fragment
	if i := strings.Index(raw, "?"); i >= 0 {
		raw = raw[i+1:]
	}
	beforeQuery, err := url.ParseQuery(raw)
	if err != nil {
		t.Fatal(err)
	}
	beforeSecret := beforeQuery.Get("secret")
	req, err := http.NewRequest(http.MethodPost, fmt.Sprintf("http://127.0.0.1:%d/restart", before.APIPort), nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+beforeSecret)
	resp, err := f.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Zashboard restart API status=%d", resp.StatusCode)
	}
	after := coreConnected(t, f, before.PID)
	if after.APIPort != before.APIPort || after.MixedPort != before.MixedPort || !strings.Contains(after.PanelURL, "secret="+url.QueryEscape(beforeSecret)) {
		t.Fatalf("managed restart changed session identity: before=%+v after=%+v", before, after)
	}
	versionReq, err := http.NewRequest(http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/version", after.APIPort), nil)
	if err != nil {
		t.Fatal(err)
	}
	versionReq.Header.Set("Authorization", "Bearer "+beforeSecret)
	version, err := f.client.Do(versionReq)
	if err != nil {
		t.Fatal(err)
	}
	version.Body.Close()
	if version.StatusCode != http.StatusOK {
		t.Fatalf("recovered core API status=%d", version.StatusCode)
	}
	consolePost(t, consoleTrayFor(f.cmd.Process.Pid), 0x111, uintptr(trayCommandQuit))
	f.waitExit(t)
	current, err := readProxySnapshot()
	if err != nil || current != beforeProxy {
		t.Fatalf("private proxy not restored: before=%+v after=%+v err=%v", beforeProxy, current, err)
	}
	t.Logf("Zashboard /restart replaced core PID %d with %d, retained ports and restored proxy", before.PID, after.PID)
}

func TestIsolatedSubscriptionUpdateRestartsAndUnlocksProfiles(t *testing.T) {
	coreRequireIsolatedTarget(t)
	originalProxy := coreSeedPrivateProxy(t)
	f := startConsoleTestLauncher(t, true)
	f.client.Timeout = 35 * time.Second
	coreInstallFixture(t, f)
	const initial = "mode: rule\nlog-level: info\nproxies: [{name: local, type: direct}]\nrules: ['MATCH,DIRECT']\ndns: {enable: false}\n"
	updated := strings.Replace(initial, "log-level: info", "log-level: warning", 1)
	var serveUpdate atomic.Bool
	subscription := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		content := initial
		if serveUpdate.Load() {
			content = updated
		}
		_, _ = io.WriteString(w, content)
	}))
	defer subscription.Close()
	add, err := json.Marshal(map[string]string{"action": "add-url", "name": "Local subscription", "url": subscription.URL, "mode": "direct"})
	if err != nil {
		t.Fatal(err)
	}
	f.request(t, http.MethodPost, "/api/profiles", string(add))
	readProfiles := func() ProfileResponse {
		t.Helper()
		resp, err := f.client.Get(f.url + "/api/profiles")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("profile list status = %d", resp.StatusCode)
		}
		var profiles ProfileResponse
		if err := json.NewDecoder(resp.Body).Decode(&profiles); err != nil {
			t.Fatal(err)
		}
		return profiles
	}
	id := readProfiles().ActiveID
	if !validProfileID(id) {
		t.Fatal("missing active subscription")
	}
	f.request(t, http.MethodPost, "/api/connect", "{}")
	before := coreConnected(t, f, 0)
	serveUpdate.Store(true)
	f.request(t, http.MethodPost, "/api/profiles", fmt.Sprintf(`{"action":"update","id":%q,"mode":"direct"}`, id))
	// The response must arrive after the reconnect and the busy flag reset.
	after := f.state()
	if after.Busy || after.ProfilePending || !after.Running || after.PID == before.PID {
		t.Fatalf("subscription update did not finish reconnecting: %+v", after)
	}
	coreConnected(t, f, before.PID)
	runtimeYAML, err := os.ReadFile(filepath.Join(f.dir, "data", "runtime.yaml"))
	if err != nil || !strings.Contains(string(runtimeYAML), "log-level: warning") {
		t.Fatalf("new core did not receive the updated configuration: %v", err)
	}
	f.request(t, http.MethodPost, "/api/profiles", fmt.Sprintf(`{"action":"rename","id":%q,"name":"Renamed"}`, id))
	profiles := readProfiles()
	if len(profiles.Profiles) != 1 || profiles.Profiles[0].Name != "Renamed" {
		t.Fatal("rename after subscription update did not persist")
	}

	// Both the file picker and the paste editor use the same import endpoint.
	importBody, err := json.Marshal(map[string]string{"content": initial})
	if err != nil {
		t.Fatal(err)
	}
	for _, op := range []struct{ path, body string }{
		{"/api/profiles", string(add)},
		{"/api/import", string(importBody)},
	} {
		f.request(t, http.MethodPost, op.path, op.body)
		added := readProfiles()
		if f.state().Busy || len(added.Profiles) != 2 || added.ActiveID == id {
			t.Fatalf("new profile was not selectable after %s", op.path)
		}
		f.request(t, http.MethodPost, "/api/profiles", fmt.Sprintf(`{"action":"switch","id":%q}`, id))
		f.request(t, http.MethodPost, "/api/profiles", fmt.Sprintf(`{"action":"rename","id":%q,"name":"New configuration"}`, added.ActiveID))
		f.request(t, http.MethodPost, "/api/profiles", fmt.Sprintf(`{"action":"delete","id":%q}`, added.ActiveID))
		if remaining := readProfiles(); len(remaining.Profiles) != 1 || remaining.ActiveID != id {
			t.Fatalf("profile operations after %s did not persist", op.path)
		}
	}
	consolePost(t, consoleTrayFor(f.cmd.Process.Pid), 0x111, uintptr(trayCommandQuit))
	f.waitExit(t)
	coreAssertRestored(t, originalProxy)
}

func TestIsolatedCoreCtrlCAndCloseExit(t *testing.T) {
	for _, mode := range []string{"CtrlC", "close-exit"} {
		t.Run(mode, func(t *testing.T) {
			coreRequireIsolatedTarget(t)
			before := coreSeedPrivateProxy(t)
			f := startConsoleTestLauncher(t, false)
			coreInstallFixture(t, f)
			f.window(t)
			f.request(t, http.MethodPost, "/api/connect", "{}")
			s := coreConnected(t, f, 0)
			if mode == "CtrlC" {
				consoleProbe(t, consoleChildren(f.cmd.Process.Pid, f.exe)[0], true)
			} else {
				f.request(t, http.MethodPut, "/api/settings", "{\"closeToTray\":false}")
				consolePost(t, consoleWindowFor(f.cmd.Process.Pid), 0x112, 0xf060)
			}
			f.waitExit(t)
			if processExists(s.PID) {
				t.Fatal("core survived graceful console exit")
			}
			coreAssertRestored(t, before)
		})
	}
}

func TestIsolatedConnectionDiagnosticsAndRepair(t *testing.T) {
	coreRequireIsolatedTarget(t)
	original := coreSeedPrivateProxy(t)
	f := startConsoleTestLauncher(t, true)
	coreInstallFixture(t, f)
	report := func() connectionDiagnostics {
		t.Helper()
		res, err := f.client.Get(f.url + "/api/diagnostics")
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var d connectionDiagnostics
		if err = json.NewDecoder(res.Body).Decode(&d); err != nil || res.StatusCode != http.StatusOK {
			t.Fatalf("diagnostics status %d: %v", res.StatusCode, err)
		}
		return d
	}
	// The preferred port may already be occupied. Keep it occupied through Connect.
	if ln, err := net.Listen("tcp4", loopbackAddr(defaultMixedPort)); err == nil {
		defer ln.Close()
	}
	if d := report(); d.Core.Status != "idle" || d.Port.Status != "warning" {
		t.Fatalf("expected disconnected and occupied default port: %+v", d)
	}
	f.request(t, http.MethodPost, "/api/connect", "{}")
	connected := coreConnected(t, f, 0)
	if connected.MixedPort == defaultMixedPort {
		t.Fatal("did not fall back from occupied default port")
	}
	if d := report(); d.Core.Status != "ok" || d.Port.Status != "ok" || d.Proxy.Status != "ok" {
		t.Fatalf("unexpected connected diagnostics: %+v", d)
	}
	changed := original
	changed.enabled = 0
	if err := (isolatedProxyBackend{}).Write(changed); err != nil {
		t.Fatal(err)
	}
	d := report()
	if d.Proxy.Status != "error" || !strings.Contains(d.Reason, "未开启") {
		t.Fatalf("did not detect system proxy drift: %+v", d)
	}
	foundRepair := false
	for _, action := range d.Actions {
		foundRepair = foundRepair || action.ID == "repairProxy"
	}
	if !foundRepair {
		t.Fatalf("missing repair action: %+v", d)
	}
	f.request(t, http.MethodPost, "/api/repair-proxy", "{}")
	consoleEventually(t, 5*time.Second, "proxy repaired in place", func() bool { return report().Proxy.Status == "ok" })
	if s := f.state(); s.PID != connected.PID || s.ConnectionError != "" {
		t.Fatalf("unexpected recovery state: %+v", s)
	}
	f.request(t, http.MethodPost, "/api/disconnect", "{}")
	consoleEventually(t, 10*time.Second, "disconnected", func() bool { s := f.state(); return !s.Running && !s.Busy })
	coreAssertRestored(t, original)
	if err := os.WriteFile(filepath.Join(f.dir, "data", "source.yaml"), []byte("proxies:\n  - name: invalid\n    type: invalid-test-type\nrules:\n  - MATCH,DIRECT\n"), 0600); err != nil {
		t.Fatal(err)
	}
	f.request(t, http.MethodPost, "/api/connect", "{}")
	consoleEventually(t, 5*time.Second, "connection failure reason", func() bool { return !f.state().Busy && strings.Contains(report().Reason, "invalid-test-type") })
	consolePost(t, consoleTrayFor(f.cmd.Process.Pid), 0x111, uintptr(trayCommandQuit))
	f.waitExit(t)
}

func TestIsolatedCoreUnexpectedExitRestoresProxy(t *testing.T) {
	coreRequireIsolatedTarget(t)
	before := coreSeedPrivateProxy(t)
	f := startConsoleTestLauncher(t, true)
	coreInstallFixture(t, f)
	f.request(t, http.MethodPost, "/api/connect", "{}")
	s := coreConnected(t, f, 0)
	coreAssertOwned(t, s.PID, f.cmd.Process.Pid)
	h, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, uint32(s.PID))
	if err != nil {
		t.Fatal(err)
	}
	err = windows.TerminateProcess(h, 1)
	windows.CloseHandle(h)
	if err != nil {
		t.Fatal(err)
	}
	consoleEventually(t, 10*time.Second, "unexpected exit cleanup", func() bool {
		st := f.state()
		return !st.Running && !st.Busy && strings.Contains(st.LastError, "意外退出")
	})
	coreAssertRestored(t, before)
	consolePost(t, consoleTrayFor(f.cmd.Process.Pid), 0x111, uintptr(trayCommandQuit))
	f.waitExit(t)
}

func TestIsolatedVisualProbe(t *testing.T) {
	marker := os.Getenv("MIHOMOLITE_VISUAL_PROBE")
	if marker == "" {
		t.Skip("external UI probe fixture only")
	}
	coreRequireIsolatedTarget(t)
	coreSeedPrivateProxy(t)
	f := startConsoleTestLauncher(t, true)
	coreInstallFixture(t, f)
	info := map[string]any{"pid": f.cmd.Process.Pid, "url": f.url, "dir": f.dir, "trayHWND": consoleTrayFor(f.cmd.Process.Pid), "namespace": instanceNamespace, "proxyRegistryPath": isolatedProxyRegistryPath()}
	data, _ := json.Marshal(info)
	if err := os.WriteFile(marker, data, 0600); err != nil {
		t.Fatal(err)
	}
	consoleEventually(t, 15*time.Minute, "external UI probe completion", func() bool { return fileExists(marker + ".done") })
	consolePost(t, consoleTrayFor(f.cmd.Process.Pid), 0x111, uintptr(trayCommandQuit))
	f.waitExit(t)
}
