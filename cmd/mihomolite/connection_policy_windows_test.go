//go:build windows

package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// No real process, Windows proxy backend, registry or protected directory is
// used. go test's TMP/TEMP are pointed at the task's isolated cache by the runner.
func connectionPolicyTestApp(t *testing.T) (*Application, *lifecycleProxy, *time.Time) {
	t.Helper()
	dir := t.TempDir()
	p := &lifecycleProxy{}
	a := &Application{baseDir: dir, dataDir: filepath.Join(dir, "data"), logsDir: filepath.Join(dir, "logs"), config: &Settings{Listen: "127.0.0.1:17653", ConnectionPolicy: ConnectionPolicy{CrashReconnect: true}}, proxy: p, events: NewEventHub(), logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	ctx, cancel := context.WithCancel(context.Background())
	a.connectionPolicy.ctx, a.connectionPolicy.cancel = ctx, cancel
	a.connectionPolicy.now = func() time.Time { return now }
	t.Cleanup(cancel)
	return a, p, &now
}

func connectionPolicySnapshot(a *Application) ConnectionRetryState {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.connectionRetryStateLocked()
}

func connectionPolicyQueue(a *Application) uint64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.connectionPolicy.wanted = true
	a.scheduleConnectionPolicyLocked("crash")
	return a.connectionPolicy.generation
}

// Deterministically fire a timer without sleeping through exponential delays.
func connectionPolicyRunDue(a *Application, ctx context.Context, generation uint64, reason string) {
	a.mu.Lock()
	if a.connectionPolicy.waiting {
		a.connectionPolicy.nextRetryAt = a.connectionNowLocked()
	}
	a.mu.Unlock()
	a.attemptConnectionPolicy(ctx, generation, reason)
}

func connectionPolicyFakeReady(a *Application, p *lifecycleProxy) *coreSession {
	// PID zero and Process nil are intentional: this fixture cannot kill a real
	// process. Production's enableCoreProxy has independent PID ownership tests.
	s := &coreSession{cmd: &exec.Cmd{}, done: make(chan struct{}), ready: true, proxyStarted: true}
	_ = p.Enable("127.0.0.1:1", "")
	a.mu.Lock()
	s.readyAt = a.connectionNowLocked()
	a.core, a.corePID, a.proxyOn = s, 0, true
	a.connectionCoreReadyLocked(s)
	a.mu.Unlock()
	return s
}

func connectionPolicyFakeExit(a *Application, s *coreSession) {
	a.handleCoreExit(s.pid, errors.New("synthetic core exit"))
	close(s.done)
}

func TestConnectionPolicyDefaultsBoundsAndBackoff(t *testing.T) {
	p, err := normalizeConnectionPolicy(ConnectionPolicy{})
	if err != nil || p.StartupConnect || p.CrashReconnect || p.MaxRetries != 3 || p.InitialDelaySeconds != 2 || p.MaxDelaySeconds != 30 {
		t.Fatalf("defaults: %+v %v", p, err)
	}
	for i, want := range []time.Duration{2, 4, 8, 16, 30, 30, 30} {
		if got := connectionRetryDelay(p, i+1); got != want*time.Second {
			t.Errorf("attempt %d delay %v", i+1, got)
		}
	}
	for _, bad := range []ConnectionPolicy{{MaxRetries: -1}, {MaxRetries: 6}, {InitialDelaySeconds: -1}, {InitialDelaySeconds: 61}, {MaxDelaySeconds: -1}, {MaxDelaySeconds: 301}, {InitialDelaySeconds: 31, MaxDelaySeconds: 30}} {
		if _, err := normalizeConnectionPolicy(bad); !errors.Is(err, errConnectionPolicy) {
			t.Errorf("accepted invalid policy: %+v", bad)
		}
	}
	for _, good := range []ConnectionPolicy{{MaxRetries: 1, InitialDelaySeconds: 1, MaxDelaySeconds: 1}, {MaxRetries: 5, InitialDelaySeconds: 60, MaxDelaySeconds: 300}} {
		if _, err := normalizeConnectionPolicy(good); err != nil {
			t.Error(err)
		}
	}
}

func TestConnectionPolicySettingsLegacyAndJSONState(t *testing.T) {
	a, _, _ := connectionPolicyTestApp(t)
	path := filepath.Join(a.baseDir, "launcher.json")
	if err := os.WriteFile(path, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := LoadSettings(path)
	if err != nil || s.ConnectionPolicy.MaxRetries != 3 || s.ConnectionPolicy.StartupConnect || s.ConnectionPolicy.CrashReconnect {
		t.Fatalf("legacy: %+v %v", s, err)
	}
	connectionPolicyQueue(a)
	body, err := json.Marshal(a.State())
	if err != nil {
		t.Fatal(err)
	}
	var state map[string]json.RawMessage
	_ = json.Unmarshal(body, &state)
	var retry ConnectionRetryState
	_ = json.Unmarshal(state["connectionRetry"], &retry)
	if !retry.Waiting || !retry.Active || retry.NextRetryAt == nil || retry.Attempt != 0 || retry.MaxRetries != 3 {
		t.Fatalf("state retry: %+v", retry)
	}
	a.cancelConnectionPolicy()
	if a.State().ConnectionRetry.NextRetryAt != nil {
		t.Fatal("cancelled deadline retained")
	}
}

func TestConnectionPolicyRapidCrashesExhaustBudget(t *testing.T) {
	a, p, now := connectionPolicyTestApp(t)
	var starts int
	a.connectionPolicy.start = func() error {
		starts++
		if a.coreControl.TryLock() {
			a.coreControl.Unlock()
			t.Error("automatic attempt omitted coreControl")
		}
		a.mu.Lock()
		busy := a.operation
		a.mu.Unlock()
		if !busy {
			t.Error("automatic attempt omitted begin")
		}
		s := connectionPolicyFakeReady(a, p)
		connectionPolicyFakeExit(a, s) // readiness succeeds, but it is not stable.
		return nil
	}
	generation := connectionPolicyQueue(a)
	for i := 1; i <= 3; i++ {
		before := connectionPolicySnapshot(a)
		if !before.Waiting || before.NextRetryAt == nil {
			t.Fatalf("attempt %d not scheduled: %+v", i, before)
		}
		if got := before.NextRetryAt.Sub(*now); got != connectionRetryDelay(a.configuredConnectionPolicyLocked(), i) {
			t.Errorf("attempt %d delay %v", i, got)
		}
		a.mu.Lock()
		*now = *before.NextRetryAt
		a.mu.Unlock()
		connectionPolicyRunDue(a, a.connectionPolicy.ctx, generation, "crash")
	}
	after := connectionPolicySnapshot(a)
	if starts != 3 || after.Attempt != 3 || !after.Exhausted || after.Active || after.Error == "" || a.core != nil {
		t.Fatalf("unbounded rapid crashes starts=%d state=%+v", starts, after)
	}
	connectionPolicyRunDue(a, a.connectionPolicy.ctx, generation, "crash")
	if starts != 3 {
		t.Fatal("exhausted callback started another core")
	}
	en, dis, owned := p.counts()
	if en != 3 || dis != 3 || owned {
		t.Fatalf("fake proxy cleanup %d/%d/%v", en, dis, owned)
	}
}

func TestConnectionPolicyStableResetNotInstantReady(t *testing.T) {
	a, p, now := connectionPolicyTestApp(t)
	a.connectionPolicy.attempt = 2
	a.connectionPolicy.wanted = true
	s := connectionPolicyFakeReady(a, p)
	a.mu.Lock()
	a.resetStableConnectionLocked()
	attempt := a.connectionPolicy.attempt
	a.mu.Unlock()
	if attempt != 2 {
		t.Fatal("instant ready reset retry budget")
	}
	a.mu.Lock()
	*now = now.Add(connectionStablePeriod - time.Nanosecond)
	a.resetStableConnectionLocked()
	attempt = a.connectionPolicy.attempt
	a.mu.Unlock()
	if attempt != 2 {
		t.Fatal("reset before stability deadline")
	}
	a.mu.Lock()
	*now = now.Add(time.Nanosecond)
	a.resetStableConnectionLocked()
	attempt = a.connectionPolicy.attempt
	a.mu.Unlock()
	if attempt != 0 {
		t.Fatal("stable connection did not reset budget")
	}
	connectionPolicyFakeExit(a, s)
	if state := connectionPolicySnapshot(a); !state.Waiting || state.Attempt != 0 {
		t.Fatalf("stable exit: %+v", state)
	}
}

func TestConnectionPolicyDuplicateSchedulingAndNoMultipleCore(t *testing.T) {
	a, p, _ := connectionPolicyTestApp(t)
	generation := connectionPolicyQueue(a)
	deadline := *connectionPolicySnapshot(a).NextRetryAt
	var schedules sync.WaitGroup
	for i := 0; i < 30; i++ {
		schedules.Add(1)
		go func() { defer schedules.Done(); a.mu.Lock(); a.scheduleConnectionPolicyLocked("crash"); a.mu.Unlock() }()
	}
	schedules.Wait()
	if got := connectionPolicySnapshot(a); got.Attempt != 0 || !got.NextRetryAt.Equal(deadline) {
		t.Fatalf("duplicates changed budget/deadline: %+v", got)
	}
	var starts atomic.Int32
	var session *coreSession
	a.connectionPolicy.start = func() error { starts.Add(1); session = connectionPolicyFakeReady(a, p); return nil }
	var attempts sync.WaitGroup
	for i := 0; i < 30; i++ {
		attempts.Add(1)
		go func() { defer attempts.Done(); connectionPolicyRunDue(a, a.connectionPolicy.ctx, generation, "crash") }()
	}
	attempts.Wait()
	if starts.Load() != 1 || a.core != session || a.operation {
		t.Fatalf("duplicate starts=%d busy=%v", starts.Load(), a.operation)
	}
	connectionPolicyFakeExit(a, session)
}

func TestConnectionPolicyBusyDefersWithoutBudgetOrStart(t *testing.T) {
	a, _, now := connectionPolicyTestApp(t)
	generation := connectionPolicyQueue(a)
	var starts int
	a.connectionPolicy.start = func() error { starts++; return errConnectionPolicyPermanent }
	if err := a.begin(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		connectionPolicyRunDue(a, a.connectionPolicy.ctx, generation, "crash")
	}
	state := connectionPolicySnapshot(a)
	if starts != 0 || state.Attempt != 0 || !state.Waiting || state.NextRetryAt == nil || !state.NextRetryAt.After(*now) {
		t.Fatalf("busy timer unsafe: %+v starts=%d", state, starts)
	}
	a.finish(nil)
	connectionPolicyRunDue(a, a.connectionPolicy.ctx, generation, "crash")
	if starts != 1 || !connectionPolicySnapshot(a).Exhausted {
		t.Fatal("did not safely resume after busy")
	}
}

func TestConnectionPolicyManualStopCancelsWaitingAndQueuedLock(t *testing.T) {
	a, _, _ := connectionPolicyTestApp(t)
	generation := connectionPolicyQueue(a)
	var starts atomic.Int32
	a.connectionPolicy.start = func() error { starts.Add(1); return nil }
	a.coreControl.Lock()
	attemptDone := make(chan struct{})
	go func() { connectionPolicyRunDue(a, a.connectionPolicy.ctx, generation, "crash"); close(attemptDone) }()
	a.cancelConnectionPolicy() // HTTP pre-begin cancellation; lock waiter stale.
	a.coreControl.Unlock()
	lifecycleWait(t, attemptDone, "stale timer")
	if err := a.Stop(); err != nil {
		t.Fatal(err)
	}
	state := connectionPolicySnapshot(a)
	if starts.Load() != 0 || state.Active || state.NextRetryAt != nil || state.Attempt != 0 {
		t.Fatalf("manual stop failed: %+v", state)
	}
}

func TestConnectionPolicyCancelAttemptReadyRaceStopsFakeCore(t *testing.T) {
	a, p, _ := connectionPolicyTestApp(t)
	generation := connectionPolicyQueue(a)
	entered, release := make(chan struct{}), make(chan struct{})
	a.connectionPolicy.start = func() error {
		s := connectionPolicyFakeReady(a, p)
		close(s.done) // Synthetic process already exited; Stop never touches OS.
		close(entered)
		<-release
		return nil
	}
	done := make(chan struct{})
	go func() { connectionPolicyRunDue(a, a.connectionPolicy.ctx, generation, "crash"); close(done) }()
	lifecycleWait(t, entered, "synthetic ready")
	req := httptest.NewRequest("POST", "/api/disconnect", nil)
	rec := httptest.NewRecorder()
	a.handleDisconnect(rec, req)
	if rec.Code != 202 {
		t.Fatalf("busy disconnect response=%d", rec.Code)
	}
	close(release)
	lifecycleWait(t, done, "cancelled automatic cleanup")
	state := connectionPolicySnapshot(a)
	if a.core != nil || state.Active || a.operation {
		t.Fatalf("cancel left a core/state: %+v", state)
	}
	_, dis, owned := p.counts()
	if dis != 1 || owned {
		t.Fatalf("proxy cleanup %d/%v", dis, owned)
	}
}

func TestConnectionPolicyStartupOnceAndTerminalErrors(t *testing.T) {
	for _, startup := range []bool{false, true} {
		t.Run(map[bool]string{false: "crash-validation", true: "startup-transient"}[startup], func(t *testing.T) {
			a, _, _ := connectionPolicyTestApp(t)
			reason := "crash"
			if startup {
				reason = "startup"
			}
			a.connectionPolicy.wanted = true
			a.connectionPolicy.waiting = true
			a.connectionPolicy.reason = reason
			var starts int
			a.connectionPolicy.start = func() error {
				starts++
				if startup {
					return errors.New("transient readiness")
				}
				return errors.Join(errConnectionPolicyPermanent, errors.New("synthetic configuration problem"))
			}
			connectionPolicyRunDue(a, a.connectionPolicy.ctx, 0, reason)
			connectionPolicyRunDue(a, a.connectionPolicy.ctx, 0, reason)
			state := connectionPolicySnapshot(a)
			if starts != 1 || state.Active || !state.Exhausted {
				t.Fatalf("terminal start not single: starts=%d %+v", starts, state)
			}
		})
	}
}

func TestConnectionPolicyTransientFailuresBounded(t *testing.T) {
	a, _, _ := connectionPolicyTestApp(t)
	generation := connectionPolicyQueue(a)
	var starts int
	a.connectionPolicy.start = func() error { starts++; return errors.New("synthetic readiness timeout") }
	for i := 0; i < 10; i++ {
		connectionPolicyRunDue(a, a.connectionPolicy.ctx, generation, "crash")
	}
	if state := connectionPolicySnapshot(a); starts != 3 || !state.Exhausted || state.Attempt != 3 {
		t.Fatalf("transient budget %d %+v", starts, state)
	}
}

func TestConnectionPolicyCleanupFailureAndUnreadyExitNeverRetry(t *testing.T) {
	for _, bad := range []string{"restore", "record", "unready", "expected"} {
		t.Run(bad, func(t *testing.T) {
			a, p, _ := connectionPolicyTestApp(t)
			a.connectionPolicy.wanted = true
			s := connectionPolicyFakeReady(a, p)
			switch bad {
			case "restore":
				p.mu.Lock()
				p.failure = errors.New("synthetic restore denied")
				p.mu.Unlock()
			case "record":
				if err := os.MkdirAll(filepath.Join(a.dataDir, "core.pid"), 0700); err != nil {
					t.Fatal(err)
				}
			case "unready":
				s.ready = false
				s.readyAt = time.Time{}
			case "expected":
				s.stopping = true
			}
			connectionPolicyFakeExit(a, s)
			state := connectionPolicySnapshot(a)
			if state.Waiting {
				t.Fatalf("unsafe retry scheduled: %+v", state)
			}
			if (bad == "restore" || bad == "record") && (!state.Exhausted || state.Error == "") {
				t.Fatalf("cleanup blocker not reported: %+v", state)
			}
			if !a.connectionPolicy.readyAt.IsZero() && bad != "restore" && bad != "record" {
				t.Fatal("stale stability timer remains")
			}
		})
	}
}

func TestConnectionPolicyDisableDoesNotReviveManualDisconnect(t *testing.T) {
	a, _, _ := connectionPolicyTestApp(t)
	connectionPolicyQueue(a)
	a.mu.Lock()
	a.config.ConnectionPolicy.CrashReconnect = false
	a.connectionPolicyChangedLocked()
	a.mu.Unlock()
	if connectionPolicySnapshot(a).Active {
		t.Fatal("disable retained retry")
	}
	a.mu.Lock()
	a.config.ConnectionPolicy.CrashReconnect = true
	a.config.ConnectionPolicy.StartupConnect = true
	a.connectionPolicyChangedLocked()
	a.scheduleConnectionPolicyLocked("crash")
	a.mu.Unlock()
	if connectionPolicySnapshot(a).Active {
		t.Fatal("setting change revived manual disconnected intent")
	}
}

func TestConnectionPolicyWorkerDefaultOffStartupIdempotentAndExit(t *testing.T) {
	for _, startup := range []bool{false, true} {
		t.Run(map[bool]string{false: "default-off", true: "startup-on"}[startup], func(t *testing.T) {
			a, _, _ := connectionPolicyTestApp(t)
			a.config.ConnectionPolicy.StartupConnect = startup
			var starts atomic.Int32
			called := make(chan struct{}, 2)
			a.connectionPolicy.start = func() error { starts.Add(1); called <- struct{}{}; return errConnectionPolicyPermanent }
			ctx, cancel := context.WithCancel(context.Background())
			a.startConnectionPolicy(ctx)
			a.startConnectionPolicy(ctx)
			if startup {
				select {
				case <-called:
				case <-time.After(3 * time.Second):
					t.Fatal("startup not called")
				}
			}
			cancel()
			a.stopConnectionPolicy()
			want := int32(0)
			if startup {
				want = 1
			}
			if starts.Load() != want || connectionPolicySnapshot(a).Active {
				t.Fatalf("worker starts %d want %d", starts.Load(), want)
			}
		})
	}
}

func TestConnectionPolicyExitCancelsInFlightAttempt(t *testing.T) {
	a, _, _ := connectionPolicyTestApp(t)
	a.config.ConnectionPolicy.StartupConnect = true
	entered := make(chan struct{})
	a.connectionPolicy.start = func() error { ctx := a.coreStartContext(); close(entered); <-ctx.Done(); return ctx.Err() }
	ctx, cancel := context.WithCancel(context.Background())
	a.startConnectionPolicy(ctx)
	lifecycleWait(t, entered, "automatic startup entered")
	cancel()
	a.stopConnectionPolicy()
	if a.operation || connectionPolicySnapshot(a).Active || a.core != nil {
		t.Fatal("exit did not converge")
	}
}

func TestConnectionPolicyEarlyAndDuplicateCallbacksRespectDeadline(t *testing.T) {
	a, _, now := connectionPolicyTestApp(t)
	generation := connectionPolicyQueue(a)
	var starts int
	a.connectionPolicy.start = func() error { starts++; return errors.New("temporary readiness failure") }
	// Callbacks arriving early must not move the deadline or spend the budget.
	deadline := *connectionPolicySnapshot(a).NextRetryAt
	for i := 0; i < 50; i++ {
		a.attemptConnectionPolicy(a.connectionPolicy.ctx, generation, "crash")
	}
	if starts != 0 || !connectionPolicySnapshot(a).NextRetryAt.Equal(deadline) {
		t.Fatal("early callback started/moved timer")
	}
	a.mu.Lock()
	*now = deadline
	a.mu.Unlock()
	a.attemptConnectionPolicy(a.connectionPolicy.ctx, generation, "crash")
	next := connectionPolicySnapshot(a)
	for i := 0; i < 50; i++ {
		a.attemptConnectionPolicy(a.connectionPolicy.ctx, generation, "crash")
	}
	if starts != 1 || connectionPolicySnapshot(a).Attempt != 1 || !connectionPolicySnapshot(a).NextRetryAt.Equal(*next.NextRetryAt) {
		t.Fatal("stale callback bypassed backoff")
	}
}

func TestConnectionPolicyInternalReloadDoesNotCancelOrResetBudget(t *testing.T) {
	a, p, _ := connectionPolicyTestApp(t)
	a.connectionPolicy.wanted = true
	a.connectionPolicy.attempt = 2
	generation := a.connectionPolicy.generation
	s := connectionPolicyFakeReady(a, p)
	close(s.done) // A fake already-exited process: no OS process is involved.
	if err := a.begin(); err != nil {
		t.Fatal(err)
	}
	err := a.restartCoreForPolicy(RuntimeConfig{MixedPort: 12345})
	a.finish(err)
	if !errors.Is(err, errConnectionPolicyPermanent) {
		t.Fatalf("missing isolated core preflight should fail: %v", err)
	}
	a.mu.Lock()
	wanted, attempt, gen, ready := a.connectionPolicy.wanted, a.connectionPolicy.attempt, a.connectionPolicy.generation, a.connectionPolicy.readyAt
	a.mu.Unlock()
	if !wanted || attempt != 2 || gen != generation || !ready.IsZero() {
		t.Fatalf("reload looked manual: wanted=%v attempt=%d gen=%d ready=%v", wanted, attempt, gen, ready)
	}
	if a.core != nil || connectionPolicySnapshot(a).Waiting {
		t.Fatal("failed reload started or scheduled another core")
	}
	_, dis, owned := p.counts()
	if dis != 1 || owned {
		t.Fatalf("reload proxy %d/%v", dis, owned)
	}
}

func TestConnectionPolicyBackupRecoveryBlocksWithoutBudget(t *testing.T) {
	a, _, _ := connectionPolicyTestApp(t)
	generation := connectionPolicyQueue(a)
	a.backupRecoveryBlocked = true
	starts := 0
	a.connectionPolicy.start = func() error { starts++; return nil }
	connectionPolicyRunDue(a, a.connectionPolicy.ctx, generation, "crash")
	if state := connectionPolicySnapshot(a); starts != 0 || state.Attempt != 0 || !state.Exhausted || state.Waiting {
		t.Fatalf("recovery blocker unsafe: %+v starts=%d", state, starts)
	}
}

func TestConnectionPolicyUnknownLegacyCoreStopsBeforeSpawn(t *testing.T) {
	a, _, _ := connectionPolicyTestApp(t)
	if err := os.MkdirAll(a.dataDir, 0700); err != nil {
		t.Fatal(err)
	}
	// Malformed local fixture, never a real PID or process identity.
	if err := os.WriteFile(filepath.Join(a.dataDir, "core.pid"), []byte("unknown-fixture-pid"), 0600); err != nil {
		t.Fatal(err)
	}
	generation := connectionPolicyQueue(a)
	connectionPolicyRunDue(a, a.connectionPolicy.ctx, generation, "crash")
	state := connectionPolicySnapshot(a)
	if a.core != nil || !state.Exhausted || state.Waiting || state.Attempt != 1 {
		t.Fatalf("unknown legacy core retried: %+v", state)
	}
}

func connectionPolicyAwait(t *testing.T, a *Application, check func() bool) {
	t.Helper()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		a.mu.Lock()
		ok := check()
		a.mu.Unlock()
		if ok {
			return
		}
		select {
		case <-tick.C:
		case <-deadline.C:
			t.Fatal("connection operation did not converge")
		}
	}
}

func TestConnectionPolicyConnectRequestDisconnectBeforeStartBarrier(t *testing.T) {
	a, _, _ := connectionPolicyTestApp(t)
	var starts atomic.Int32
	a.connectionPolicy.start = func() error { starts.Add(1); return nil }
	// Exact lifecycle-lock barrier: begin has completed but Start cannot run.
	a.coreControl.Lock()
	connect := httptest.NewRecorder()
	a.handleConnect(connect, httptest.NewRequest("POST", "/api/connect", nil))
	if connect.Code != 202 {
		a.coreControl.Unlock()
		t.Fatalf("connect=%d", connect.Code)
	}
	disconnect := httptest.NewRecorder()
	a.handleDisconnect(disconnect, httptest.NewRequest("POST", "/api/disconnect", nil))
	if disconnect.Code != 202 {
		a.coreControl.Unlock()
		t.Fatalf("disconnect=%d", disconnect.Code)
	}
	a.mu.Lock()
	generation := a.connectionPolicy.generation
	a.mu.Unlock()
	for i := 0; i < 5; i++ {
		if err := a.requestDisconnect(); err != nil {
			t.Error(err)
		}
	}
	a.mu.Lock()
	same := generation == a.connectionPolicy.generation
	a.mu.Unlock()
	a.coreControl.Unlock()
	connectionPolicyAwait(t, a, func() bool { return !a.operation && !a.connectionPolicy.disconnectPending })
	if starts.Load() != 0 || !same || a.core != nil {
		t.Fatalf("late Start revived cancellation: starts=%d coalesced=%v", starts.Load(), same)
	}
}

func TestConnectionPolicyReloadOperationTokenSurvivesLateHelperBarrier(t *testing.T) {
	a, p, _ := connectionPolicyTestApp(t)
	a.connectionPolicy.wanted = true
	s := connectionPolicyFakeReady(a, p)
	close(s.done)
	var starts atomic.Int32
	a.connectionPolicy.start = func() error { starts.Add(1); return nil }
	if err := a.begin(); err != nil {
		t.Fatal(err)
	} // profile began; helper not invoked.
	a.mu.Lock()
	original := a.connectionPolicy.operationGeneration
	a.mu.Unlock()
	if err := a.requestDisconnect(); err != nil {
		t.Fatal(err)
	}
	a.mu.Lock()
	current := a.connectionPolicy.generation
	a.mu.Unlock()
	if current == original {
		t.Fatal("original operation token not invalidated")
	}
	err := a.restartCoreForPolicy(RuntimeConfig{})
	if !errors.Is(err, errConnectionCancelled) {
		t.Fatalf("late helper adopted cancelled token: %v", err)
	}
	a.finish(nil)
	connectionPolicyAwait(t, a, func() bool { return !a.operation && !a.connectionPolicy.disconnectPending && a.core == nil })
	if starts.Load() != 0 {
		t.Fatal("late profile reload started a new core")
	}
	_, dis, owned := p.counts()
	if dis != 1 || owned {
		t.Fatalf("queued cleanup=%d/%v", dis, owned)
	}
}

func TestConnectionPolicyManualReconnectCancellationBarrier(t *testing.T) {
	a, p, _ := connectionPolicyTestApp(t)
	// Explicit manual reconnect may establish wanted for an old fake lifecycle
	// fixture; an internal configuration reload must never do so.
	s := connectionPolicyFakeReady(a, p)
	close(s.done)
	var starts atomic.Int32
	a.connectionPolicy.start = func() error { starts.Add(1); return nil }
	a.coreControl.Lock()
	if err := a.requestReconnect(); err != nil {
		a.coreControl.Unlock()
		t.Fatal(err)
	}
	if err := a.requestDisconnect(); err != nil {
		a.coreControl.Unlock()
		t.Fatal(err)
	}
	a.coreControl.Unlock()
	connectionPolicyAwait(t, a, func() bool { return !a.operation && !a.connectionPolicy.disconnectPending && a.core == nil })
	if starts.Load() != 0 {
		t.Fatal("queued manual reconnect overrode disconnect")
	}
	_, dis, owned := p.counts()
	if dis != 1 || owned {
		t.Fatalf("manual reconnect cleanup=%d/%v", dis, owned)
	}
}

func TestConnectionPolicyStaleDisconnectCannotStopNewManualIntent(t *testing.T) {
	a, p, _ := connectionPolicyTestApp(t)
	a.cancelConnectionPolicy()
	a.mu.Lock()
	generation := a.connectionPolicy.generation
	a.mu.Unlock()
	a.connectionPolicy.start = func() error { s := connectionPolicyFakeReady(a, p); close(s.done); return nil }
	if err := a.Start(); err != nil {
		t.Fatal(err)
	}
	if err := a.disconnectForIntent(generation); err != nil {
		t.Fatal(err)
	}
	_, dis, owned := p.counts()
	if dis != 0 || !owned || a.core == nil {
		t.Fatal("stale disconnect stopped newer manual core")
	}
	if err := a.Stop(); err != nil {
		t.Fatal(err)
	}
}

func TestConnectionPolicyShutdownCancelsQueuedManualStartBarrier(t *testing.T) {
	a, _, _ := connectionPolicyTestApp(t)
	ctx, cancel := context.WithCancel(context.Background())
	a.lifecycleContext = ctx
	var starts atomic.Int32
	a.connectionPolicy.start = func() error { starts.Add(1); return nil }
	a.coreControl.Lock()
	if err := a.requestConnect(); err != nil {
		a.coreControl.Unlock()
		t.Fatal(err)
	}
	cancel()
	a.mu.Lock()
	a.shuttingDown = true
	a.cancelConnectionPolicyLocked()
	a.mu.Unlock()
	a.coreControl.Unlock()
	a.waitForOperation()
	if starts.Load() != 0 || a.core != nil {
		t.Fatal("shutdown started queued manual kernel")
	}
}

func TestConnectionPolicyStrictJSONNullUnknownDuplicatesAndEOF(t *testing.T) {
	invalid := []string{"null", "[]", "{\"startupConnect\":null}", "{\"maxRetries\":null}", "{\"startupConnect\":true,\"startupConnect\":false}", "{\"maxRetires\":5}", "{\"MaxRetries\":5}", "{\"startupConnect\":1}", "{\"maxRetries\":1.5}", "{\"maxRetries\":6}", "{} {}"}
	for _, text := range invalid {
		t.Run(text, func(t *testing.T) {
			var p ConnectionPolicy
			if err := json.Unmarshal([]byte(text), &p); err == nil {
				t.Fatal("ambiguous/invalid policy accepted")
			}
		})
	}
	var p ConnectionPolicy
	if err := json.Unmarshal([]byte("{\"startupConnect\":true}"), &p); err != nil || !p.StartupConnect || p.MaxRetries != 3 {
		t.Fatalf("valid sparse object: %+v %v", p, err)
	}
	a, _, _ := connectionPolicyTestApp(t)
	for _, text := range []string{"{\"connectionPolicy\":null}", "{\"connectionPolicy\":{\"maxRetires\":5}}"} {
		path := filepath.Join(a.baseDir, "launcher.json")
		if err := os.WriteFile(path, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadSettings(path); !errors.Is(err, errConnectionPolicy) {
			t.Fatalf("LoadSettings ambiguity accepted: %v", err)
		}
	}
}

func TestConnectionPolicyStartupNeverOverridesEarlierManualStop(t *testing.T) {
	a, _, _ := connectionPolicyTestApp(t)
	a.config.ConnectionPolicy.StartupConnect = true
	var starts atomic.Int32
	a.connectionPolicy.start = func() error { starts.Add(1); return nil }
	a.cancelConnectionPolicy() // A tray cancellation can precede the startup hook.
	ctx, cancel := context.WithCancel(context.Background())
	a.startConnectionPolicy(ctx)
	cancel()
	a.stopConnectionPolicy()
	if starts.Load() != 0 {
		t.Fatal("startup hook overrode an earlier manual stop")
	}
}

func TestConnectionPolicySettingsHTTPStrictAndLegacyPatchCompatibility(t *testing.T) {
	a, _, _ := connectionPolicyTestApp(t)
	invalid := []string{"null", "{\"connectionPolicy\":null}", "{\"connectionPolicy\":{},\"connectionPolicy\":{}}", "{\"connectionPolicy\":{\"maxRetires\":5}}", "{\"unexpected\":true}", "{} {}"}
	for _, text := range invalid {
		req := httptest.NewRequest("PUT", "/api/settings", strings.NewReader(text))
		req.Header.Set("X-Mihomo-Lite", "1")
		rec := httptest.NewRecorder()
		a.handleSettings(rec, req)
		if rec.Code != 400 {
			t.Fatalf("invalid settings %q status=%d", text, rec.Code)
		}
	}
	req := httptest.NewRequest("PUT", "/api/settings", strings.NewReader("{\"autoStart\":false,\"closeToTray\":true,\"silentStart\":false,\"ports\":{}}"))
	req.Header.Set("X-Mihomo-Lite", "1")
	// Decode only: never apply an AutoStart patch to the real registry.
	patch, err := decodeRuntimeSettingsPatchStrict(req)
	if err != nil || patch.AutoStart == nil || patch.CloseToTray == nil || patch.SilentStart == nil || patch.Ports == nil {
		t.Fatalf("legacy patch not accepted: %+v %v", patch, err)
	}
}

func TestConnectionPolicyUnknownRecoveryStartBlocksSafely(t *testing.T) {
	// Missing local runtime assets stop before any process can be spawned.
	a, _, _ := connectionPolicyTestApp(t)
	err := a.Start()
	if !errors.Is(err, errConnectionPolicyPermanent) {
		t.Fatalf("preflight was retryable: %v", err)
	}
	if strings.Contains(connectionPolicySnapshot(a).Error, a.baseDir) {
		t.Fatal("status exposed private path")
	}
}
