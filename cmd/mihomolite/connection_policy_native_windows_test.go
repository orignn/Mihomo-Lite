//go:build windows

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// This proxy is exclusively process-local memory. No registry, WinINet, real
// SystemProxy, recovery ownership guard or Windows auto-start API is constructed.
type opt007NativeMemoryProxy struct {
	mu                          sync.Mutex
	target                      string
	enabled, disabled, overlaps int
}

func (p *opt007NativeMemoryProxy) Enable(target, _ string) error {
	host, port, err := net.SplitHostPort(target)
	if err != nil || host != "127.0.0.1" || port == "" {
		return errors.New("native fixture rejected non-loopback proxy target")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.target != "" {
		p.overlaps++
	}
	p.target = target
	p.enabled++
	return nil
}
func (p *opt007NativeMemoryProxy) Disable() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.target = ""
	p.disabled++
	return nil
}
func (*opt007NativeMemoryProxy) LastError() string { return "" }
func (*opt007NativeMemoryProxy) Inspect() (proxySnapshot, bool, error) {
	return proxySnapshot{}, false, nil
}
func (p *opt007NativeMemoryProxy) snapshot() (int, int, int, string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.enabled, p.disabled, p.overlaps, p.target
}

type opt007NativeFixture struct {
	app           *Application
	proxy         *opt007NativeMemoryProxy
	run, coreHash string
	cancel        context.CancelFunc
	sessions      []*coreSession
	maxLive       int
}

// Fixed reserved documentation addresses are never dialed. There are no URL-test
// groups, providers, Geo/DNS resources, subscriptions or external health checks.
const opt007NativeYAML = "dns: {enable: false}\nlog-level: warning\nproxies: [{name: SYNTHETIC-IDLE-DIRECT, type: direct}]\nproxy-groups: [{name: TEST, type: select, proxies: [SYNTHETIC-IDLE-DIRECT]}]\nrules: ['MATCH,DIRECT']\n"

func newOpt007NativeFixture(t *testing.T, policy ConnectionPolicy) *opt007NativeFixture {
	t.Helper()
	root := draftTestWorkspace(t)
	source := filepath.Join(root, "runtime", coreExecutable)
	if checkBackupPath(source) != nil || !regularFileExists(source) {
		t.Fatal("existing official kernel source must be a regular non-reparse file")
	}
	parent := filepath.Join(root, ".cache", "tests", "opt007", "native")
	if checkBackupPath(parent) != nil {
		t.Fatal("unsafe native fixture parent")
	}
	if err := os.MkdirAll(parent, 0700); err != nil {
		t.Fatal(err)
	}
	run, err := os.MkdirTemp(parent, "run-")
	if err != nil {
		t.Fatal(err)
	}
	rel, err := filepath.Rel(parent, run)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || checkBackupPath(run) != nil {
		t.Fatal("native run escaped its isolated cache")
	}
	core := filepath.Join(run, "runtime", coreExecutable)
	if err := copyFile(source, core); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	copied, err := os.ReadFile(core)
	if err != nil {
		t.Fatal(err)
	}
	originalHash, copiedHash := sha256.Sum256(original), sha256.Sum256(copied)
	if originalHash != copiedHash {
		t.Fatal("isolated official kernel copy hash mismatch")
	}
	normalized, err := normalizeConnectionPolicy(policy)
	if err != nil {
		t.Fatal(err)
	}
	logs := NewLogStore(100)
	proxy := &opt007NativeMemoryProxy{}
	ctx, cancel := context.WithCancel(context.Background())
	a := &Application{baseDir: run, dataDir: filepath.Join(run, "data"), logsDir: filepath.Join(run, "logs"), config: &Settings{Listen: "127.0.0.1:17653", APISecret: "SYNTHETIC-OPT007-API-SECRET", ConnectionPolicy: normalized}, logger: slog.New(logs), logStore: logs, events: NewEventHub(), proxy: proxy, lifecycleContext: ctx}
	a.events.current = a.State
	if err := os.MkdirAll(a.logsDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := atomicWrite(filepath.Join(a.dataDir, "ui", "index.html"), []byte("<!doctype html><title>OPT-007 generated isolated native panel</title>"), 0600); err != nil {
		t.Fatal(err)
	}
	id := strings.Repeat("7", 64)
	if err := atomicWrite(a.profileContentPath(id), []byte(opt007NativeYAML), 0600); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if err := a.saveProfileStore(profileStore{ActiveID: id, Profiles: []Profile{{ID: id, Name: "OPT007 synthetic idle native fixture", Source: "local", CreatedAt: now, UpdatedAt: now, Size: int64(len(opt007NativeYAML))}}}); err != nil {
		t.Fatal(err)
	}
	if err := atomicWrite(filepath.Join(a.dataDir, "source.yaml"), []byte(opt007NativeYAML), 0600); err != nil {
		t.Fatal(err)
	}
	f := &opt007NativeFixture{app: a, proxy: proxy, run: run, coreHash: hex.EncodeToString(copiedHash[:]), cancel: cancel}
	t.Cleanup(func() { f.close(t) })
	// No default endpoint can be chosen because it is already in use: all runtime
	// endpoints are selected through the normal launcher auto-port logic.
	_, rendered, err := a.prepareProfileConfig([]byte(opt007NativeYAML), id, a.previewRuntime(), []CustomRule{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.testConfigOfflineContext(ctx, rendered); err != nil {
		t.Fatal("official kernel isolated -t fixture failed", err)
	}
	return f
}

func (f *opt007NativeFixture) validateSession(s *coreSession) error {
	if s == nil || s.cmd == nil || s.cmd.Process == nil || s.pid != s.cmd.Process.Pid || s.pid != s.identity.PID || s.identity.Created == 0 {
		return errors.New("native session lacks held process and captured identity")
	}
	if !strings.EqualFold(filepath.Clean(s.identity.Image), filepath.Join(f.run, "runtime", coreExecutable)) {
		return errors.New("native fixture refused a daily/unowned process")
	}
	if checkBackupPath(s.identity.Image) != nil {
		return errors.New("native core image path is unsafe")
	}
	return nil
}

// Bounded state observation checks all captured *process instances*, never kills
// or probes an unrelated PID. Old PIDs reused by Windows cannot pass the identity.
func (f *opt007NativeFixture) observe() error {
	f.app.mu.Lock()
	current := f.app.core
	f.app.mu.Unlock()
	if current != nil {
		if err := f.validateSession(current); err != nil {
			return err
		}
		known := false
		for _, s := range f.sessions {
			if s == current {
				known = true
			}
		}
		if !known {
			f.sessions = append(f.sessions, current)
		}
	}
	live := 0
	for _, s := range f.sessions {
		exists, err := checkRecoveryProcessByHandle(s.identity)
		if err != nil {
			return err
		}
		if exists {
			live++
		}
	}
	if live > f.maxLive {
		f.maxLive = live
	}
	if live > 1 {
		return errors.New("two fixture-owned official kernel instances were simultaneously live")
	}
	_, _, overlaps, _ := f.proxy.snapshot()
	if overlaps != 0 {
		return errors.New("memory proxy was enabled twice without cleanup")
	}
	return nil
}
func (f *opt007NativeFixture) wait(t *testing.T, label string, budget time.Duration, predicate func() bool) {
	t.Helper()
	timer := time.NewTimer(budget)
	defer timer.Stop()
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		if err := f.observe(); err != nil {
			t.Fatal(label, err)
		}
		if predicate() {
			return
		}
		select {
		case <-timer.C:
			t.Fatalf("native fixture timeout waiting for %s", label)
		case <-tick.C:
		}
	}
}
func (f *opt007NativeFixture) quiet(t *testing.T, duration time.Duration, predicate func() bool) {
	t.Helper()
	timer := time.NewTimer(duration)
	defer timer.Stop()
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		if err := f.observe(); err != nil {
			t.Fatal(err)
		}
		if !predicate() {
			t.Fatal("native fixture changed during no-reconnect observation window")
		}
		select {
		case <-timer.C:
			return
		case <-tick.C:
		}
	}
}
func (f *opt007NativeFixture) ready(t *testing.T, previous *coreSession) *coreSession {
	t.Helper()
	f.wait(t, "new ready official kernel", 15*time.Second, func() bool {
		f.app.mu.Lock()
		defer f.app.mu.Unlock()
		return f.app.core != nil && f.app.core != previous && f.app.core.ready && !f.app.operation && !f.app.connectionPolicy.attempting
	})
	f.app.mu.Lock()
	s, runtime := f.app.core, f.app.runtime
	f.app.mu.Unlock()
	if err := f.validateSession(s); err != nil {
		t.Fatal(err)
	}
	actual, err := captureRecoveryProcess(s.pid)
	if err != nil || actual != s.identity {
		t.Fatal("ready official kernel identity mismatch")
	}
	if previous != nil && previous.pid == s.pid {
		t.Fatal("crash reconnect unexpectedly retained old PID")
	}
	if err := checkCoreListener(runtime.MixedPort, s.pid); err != nil {
		t.Fatal("proxy endpoint is not exclusively owned by fixture kernel", err)
	}
	if err := checkCoreListener(runtime.BackendPort, s.pid); err != nil {
		t.Fatal("controller endpoint is not exclusively owned by fixture kernel", err)
	}
	if err := probeCoreAPI(context.Background(), runtime, s.pid); err != nil {
		t.Fatal("fixture loopback official-core API not ready", err)
	}
	// Read effective YAML from this fixture only, without disclosing its API secret.
	rendered, err := os.ReadFile(filepath.Join(f.app.dataDir, "runtime.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	root := overrideRenderRoot(t, rendered)
	if mappingValue(root, "allow-lan").Value != "false" || mappingValue(root, "bind-address").Value != "127.0.0.1" || !strings.HasPrefix(mappingValue(root, "external-controller").Value, "127.0.0.1:") || mappingValue(mappingValue(root, "tun"), "enable").Value != "false" {
		t.Fatal("fixture kernel effective config lost loopback/TUN safety")
	}
	t.Logf("owned official core ready: pid=%d created=%d mixed=%d backend=%d", s.pid, s.identity.Created, runtime.MixedPort, runtime.BackendPort)
	return s
}
func (f *opt007NativeFixture) crash(t *testing.T, s *coreSession) {
	t.Helper()
	if err := f.validateSession(s); err != nil {
		t.Fatal(err)
	}
	actual, err := captureRecoveryProcess(s.pid)
	if err != nil || actual != s.identity {
		t.Fatal("refused crash injection without live exact fixture identity")
	}
	// The held os.Process came from exec.Cmd.Start; never os.FindProcess(PID).
	if err := s.cmd.Process.Kill(); err != nil {
		t.Fatal("failed to terminate own native fixture process", err)
	}
	select {
	case <-s.done:
	case <-time.After(10 * time.Second):
		t.Fatal("owned crashed session did not finish cleanup")
	}
	if alive, err := checkRecoveryProcessByHandle(s.identity); err != nil || alive {
		t.Fatal("old native process instance remained live")
	}
	t.Logf("intentional fixture-only crash completed: pid=%d", s.pid)
}
func (f *opt007NativeFixture) stopped() bool {
	f.app.mu.Lock()
	defer f.app.mu.Unlock()
	return f.app.core == nil && !f.app.operation && !f.app.connectionPolicy.waiting && !f.app.connectionPolicy.attempting
}
func (f *opt007NativeFixture) close(t *testing.T) {
	t.Helper()
	f.cancel()
	f.app.stopConnectionPolicy()
	f.app.mu.Lock()
	current := f.app.core
	f.app.mu.Unlock()
	if current != nil {
		if err := f.validateSession(current); err != nil {
			t.Error("refused unsafe native fixture cleanup", err)
			return
		}
	}
	if err := f.app.Stop(); err != nil {
		t.Error("native fixture normal Stop failed", err)
	}
	for _, s := range f.sessions {
		alive, err := checkRecoveryProcessByHandle(s.identity)
		if err != nil {
			t.Error(err)
			continue
		}
		if alive {
			t.Error("native fixture leaked an owned process instance")
		}
	}
	enabled, disabled, overlaps, target := f.proxy.snapshot()
	if target != "" || overlaps != 0 {
		t.Error("native fixture memory-proxy ownership did not clean up")
	}
	identities := []recoveryProcessIdentity{}
	for _, s := range f.sessions {
		identities = append(identities, s.identity)
	}
	evidence := map[string]any{"scenario": t.Name(), "applicationVersion": AppVersion, "kernelSHA256": f.coreHash, "kernel": "existing official kernel copied to this isolated run", "proxyBackend": "process-local fake memory; NOT real system proxy/registry/WinINet acceptance", "scope": "native core lifecycle and loopback binding only; no real nodes/provider/Geo/network traffic", "policy": f.app.config.ConnectionPolicy, "sessions": identities, "maxObservedLiveOwnedCores": f.maxLive, "fakeProxyEnableCalls": enabled, "fakeProxyDisableCalls": disabled, "fakeProxyOverlaps": overlaps, "passed": !t.Failed()}
	data, err := json.MarshalIndent(evidence, "", "  ")
	if err != nil {
		t.Error(err)
	} else if err := atomicWrite(filepath.Join(f.run, "native-evidence.json"), append(data, '\n'), 0600); err != nil {
		t.Error(err)
	}
	t.Log("OPT007 isolated official-core evidence: " + f.run)
}

func TestConnectionPolicyOfficialCoreNative(t *testing.T) {
	if os.Getenv("MIHOMOLITE_TEST_OPT007_NATIVE") != "1" {
		t.Skip("explicit OPT-007 isolated official-core gate required; fake memory proxy is not real system proxy acceptance")
	}
	t.Run("default-off-and-no-retroactive-startup", func(t *testing.T) {
		f := newOpt007NativeFixture(t, ConnectionPolicy{})
		f.app.startConnectionPolicy(f.app.lifecycleContext)
		f.quiet(t, 2*time.Second, func() bool { enabled, _, _, _ := f.proxy.snapshot(); return f.stopped() && enabled == 0 })
		f.app.mu.Lock()
		f.app.config.ConnectionPolicy.StartupConnect = true
		f.app.connectionPolicyChangedLocked()
		f.app.mu.Unlock()
		f.app.startConnectionPolicy(f.app.lifecycleContext)
		f.quiet(t, 2*time.Second, func() bool { enabled, _, _, _ := f.proxy.snapshot(); return f.stopped() && enabled == 0 })
		if len(f.sessions) != 0 {
			t.Fatal("default-off policy created a native kernel")
		}
	})
	t.Run("startup-once-crash-reconnect-off", func(t *testing.T) {
		f := newOpt007NativeFixture(t, ConnectionPolicy{StartupConnect: true})
		f.app.startConnectionPolicy(f.app.lifecycleContext)
		s := f.ready(t, nil)
		f.app.startConnectionPolicy(f.app.lifecycleContext)
		f.quiet(t, 2*time.Second, func() bool {
			enabled, _, _, _ := f.proxy.snapshot()
			f.app.mu.Lock()
			same := f.app.core == s
			f.app.mu.Unlock()
			return same && enabled == 1
		})
		f.crash(t, s)
		f.app.startConnectionPolicy(f.app.lifecycleContext)
		f.quiet(t, 2*time.Second, func() bool { enabled, _, _, _ := f.proxy.snapshot(); return f.stopped() && enabled == 1 })
		if len(f.sessions) != 1 {
			t.Fatal("single-shot startup restarted after crash")
		}
	})
	t.Run("real-crashes-two-retries-and-no-double-core", func(t *testing.T) {
		f := newOpt007NativeFixture(t, ConnectionPolicy{StartupConnect: true, CrashReconnect: true, MaxRetries: 2, InitialDelaySeconds: 1, MaxDelaySeconds: 1})
		f.app.startConnectionPolicy(f.app.lifecycleContext)
		s := f.ready(t, nil)
		for attempt := 1; attempt <= 2; attempt++ {
			previous := s
			f.crash(t, previous)
			s = f.ready(t, previous)
			f.app.mu.Lock()
			count := f.app.connectionPolicy.attempt
			f.app.mu.Unlock()
			if count != attempt {
				t.Fatalf("actual native retry count=%d want=%d", count, attempt)
			}
		}
		f.crash(t, s)
		f.wait(t, "native retry budget exhausted", 3*time.Second, func() bool {
			f.app.mu.Lock()
			defer f.app.mu.Unlock()
			return f.app.connectionPolicy.exhausted && f.app.connectionPolicy.attempt == 2 && f.app.core == nil
		})
		f.quiet(t, 2*time.Second, func() bool { enabled, _, _, _ := f.proxy.snapshot(); return f.stopped() && enabled == 3 })
		if len(f.sessions) != 3 || f.maxLive != 1 {
			t.Fatal("native retry PID history or single-core observation missing")
		}
	})
	t.Run("manual-cancel-prevents-native-revival", func(t *testing.T) {
		f := newOpt007NativeFixture(t, ConnectionPolicy{StartupConnect: true, CrashReconnect: true, MaxRetries: 2, InitialDelaySeconds: 2, MaxDelaySeconds: 2})
		f.app.startConnectionPolicy(f.app.lifecycleContext)
		s := f.ready(t, nil)
		f.crash(t, s)
		f.wait(t, "crash waiting before manual cancel", time.Second, func() bool { f.app.mu.Lock(); defer f.app.mu.Unlock(); return f.app.connectionPolicy.waiting })
		if err := f.app.requestDisconnect(); err != nil {
			t.Fatal(err)
		}
		f.wait(t, "manual disconnect completed", 5*time.Second, f.stopped)
		f.quiet(t, 2300*time.Millisecond, func() bool { enabled, _, _, _ := f.proxy.snapshot(); return f.stopped() && enabled == 1 })
		f.app.mu.Lock()
		wanted := f.app.connectionPolicy.wanted
		f.app.mu.Unlock()
		if wanted || len(f.sessions) != 1 {
			t.Fatal("manual cancellation revived native core intent")
		}
	})
	t.Run("permanent-resource-error-stops-native-retry", func(t *testing.T) {
		f := newOpt007NativeFixture(t, ConnectionPolicy{StartupConnect: true, CrashReconnect: true, MaxRetries: 3, InitialDelaySeconds: 1, MaxDelaySeconds: 1})
		f.app.startConnectionPolicy(f.app.lifecycleContext)
		s := f.ready(t, nil)
		panel := filepath.Join(f.app.dataDir, "ui", "index.html")
		relative, err := filepath.Rel(f.run, panel)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || checkBackupPath(panel) != nil {
			t.Fatal("refused non-fixture resource failure injection")
		}
		if err := os.Remove(panel); err != nil {
			t.Fatal(err)
		}
		f.crash(t, s)
		f.wait(t, "permanent missing generated panel stops retry", 5*time.Second, func() bool {
			f.app.mu.Lock()
			defer f.app.mu.Unlock()
			return f.app.connectionPolicy.exhausted && f.app.connectionPolicy.attempt == 1 && f.app.core == nil && !f.app.operation
		})
		f.quiet(t, 2*time.Second, func() bool { enabled, _, _, _ := f.proxy.snapshot(); return f.stopped() && enabled == 1 })
		if len(f.sessions) != 1 {
			t.Fatal("permanent local resource failure spawned another kernel")
		}
	})
	t.Log("OPT007 real official-core lifecycle verified only in isolated cache with memory-only fake SystemProxy; no real system proxy, registry, WinINet, real-node traffic or daily process acceptance")
}
