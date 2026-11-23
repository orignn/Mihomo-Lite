//go:build windows

package main

import (
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"
)

type lifecycleProxy struct {
	mu                             sync.Mutex
	enableEntered, enableRelease   chan struct{}
	disableEntered, disableRelease chan struct{}
	enableOnce, disableOnce        sync.Once
	enables, disables              int
	owned                          bool
	failure, lastError             error
}

func (p *lifecycleProxy) Enable(string, string) error {
	p.mu.Lock()
	p.enables++
	p.mu.Unlock()
	if p.enableEntered != nil {
		p.enableOnce.Do(func() { close(p.enableEntered) })
	}
	if p.enableRelease != nil {
		<-p.enableRelease
	}
	p.mu.Lock()
	p.owned = true
	p.mu.Unlock()
	return nil
}
func (p *lifecycleProxy) Disable() error {
	p.mu.Lock()
	p.disables++
	p.mu.Unlock()
	if p.disableEntered != nil {
		p.disableOnce.Do(func() { close(p.disableEntered) })
	}
	if p.disableRelease != nil {
		<-p.disableRelease
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.lastError = p.failure
	if p.failure == nil {
		p.owned = false
	}
	return p.failure
}
func (p *lifecycleProxy) LastError() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.lastError != nil {
		return p.lastError.Error()
	}
	return ""
}
func (p *lifecycleProxy) Inspect() (proxySnapshot, bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return proxySnapshot{}, p.owned && p.lastError != nil, nil
}
func (p *lifecycleProxy) counts() (int, int, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.enables, p.disables, p.owned
}
func lifecycleApp(t *testing.T, p *lifecycleProxy, s *coreSession) *Application {
	t.Helper()
	a := runtimeSettingsTestApp(t)
	a.proxy = p
	a.events = NewEventHub()
	a.logger = slog.New(NewLogStore(20))
	a.core = s
	a.corePID = s.pid
	a.proxyOn = s.proxyStarted
	return a
}
func lifecycleWait(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatal("timeout: " + what)
	}
}

func TestStopAndCoreExitRestoreProxyOnce(t *testing.T) {
	p := &lifecycleProxy{owned: true, disableEntered: make(chan struct{}), disableRelease: make(chan struct{})}
	s := &coreSession{cmd: &exec.Cmd{}, done: make(chan struct{}), proxyStarted: true} // PID 0: never kill a real process.
	a := lifecycleApp(t, p, s)
	stopped := make(chan struct{})
	var stopErr error
	go func() { stopErr = a.Stop(); close(stopped) }()
	lifecycleWait(t, p.disableEntered, "Stop restoring proxy")
	exited := make(chan struct{})
	go func() { a.handleCoreExit(0, nil); close(s.done); close(exited) }()
	close(p.disableRelease)
	lifecycleWait(t, stopped, "Stop finished")
	lifecycleWait(t, exited, "exit handler finished")
	if stopErr != nil {
		t.Fatal(stopErr)
	}
	_, n, owned := p.counts()
	if n != 1 || owned {
		t.Fatalf("cleanup calls=%d owned=%v", n, owned)
	}
}

func TestStopWaitsForNaturalExitProxyCleanup(t *testing.T) {
	p := &lifecycleProxy{owned: true, disableEntered: make(chan struct{}), disableRelease: make(chan struct{})}
	s := &coreSession{cmd: &exec.Cmd{}, done: make(chan struct{}), proxyStarted: true}
	a := lifecycleApp(t, p, s)
	exited := make(chan struct{})
	go func() { a.handleCoreExit(0, nil); close(s.done); close(exited) }()
	lifecycleWait(t, p.disableEntered, "natural exit restoring proxy")
	stopped := make(chan struct{})
	go func() { a.Stop(); close(stopped) }()
	select {
	case <-stopped:
		t.Error("Stop returned before natural-exit restoration finished")
	case <-time.After(30 * time.Millisecond):
	}
	close(p.disableRelease)
	lifecycleWait(t, exited, "natural exit finished")
	lifecycleWait(t, stopped, "Stop finished")
	_, n, owned := p.counts()
	if n != 1 || owned {
		t.Fatalf("cleanup calls=%d owned=%v", n, owned)
	}
}

func TestCoreExitDuringProxyEnableKeepsCleanupOwner(t *testing.T) {
	p := &lifecycleProxy{enableEntered: make(chan struct{}), enableRelease: make(chan struct{})}
	proc, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	defer proc.Release()
	s := &coreSession{cmd: &exec.Cmd{Process: proc}, pid: os.Getpid(), done: make(chan struct{})}
	a := lifecycleApp(t, p, s)
	enabled := make(chan struct{})
	var active bool
	var enableErr error
	go func() { active, enableErr = a.enableCoreProxy(s, 54321); close(enabled) }()
	lifecycleWait(t, p.enableEntered, "Enable entered")
	exited := make(chan struct{})
	go func() { a.handleCoreExit(s.pid, nil); close(exited) }()
	close(p.enableRelease)
	lifecycleWait(t, enabled, "Enable finished")
	lifecycleWait(t, exited, "exit cleanup finished")
	if !active || enableErr != nil {
		t.Fatalf("Enable active=%v err=%v", active, enableErr)
	}
	en, dis, owned := p.counts()
	if en != 1 || dis != 1 || owned {
		t.Fatalf("Enable=%d Disable=%d owned=%v", en, dis, owned)
	}
	if a.State().ProxyEnabled {
		t.Fatal("proxy still enabled after core exit")
	}
}

func TestExitedSessionCannotEnableProxy(t *testing.T) {
	p := &lifecycleProxy{}
	s := &coreSession{cmd: &exec.Cmd{}, done: make(chan struct{})}
	a := lifecycleApp(t, p, s)
	active, err := a.enableCoreProxy(s, 54321)
	if active || err != nil {
		t.Fatalf("unexpected active=%v err=%v", active, err)
	}
	en, _, _ := p.counts()
	if en != 0 {
		t.Fatal("dead session attempted Enable")
	}
}

func TestProxyRestoreFailureReportedAndRetriedFromDiagnosis(t *testing.T) {
	p := &lifecycleProxy{owned: true, failure: errors.New("restore denied")}
	s := &coreSession{cmd: &exec.Cmd{}, done: make(chan struct{}), proxyStarted: true}
	a := lifecycleApp(t, p, s)
	a.handleCoreExit(0, nil)
	if a.State().LastError == "" || p.LastError() == "" {
		t.Fatal("restore failure was silently ignored")
	}
	p.mu.Lock()
	p.failure = nil
	p.mu.Unlock()
	if err := a.recoverProxy(true); err != nil {
		t.Fatal(err)
	}
	_, n, owned := p.counts()
	if n != 2 || owned {
		t.Fatalf("retry calls=%d owned=%v", n, owned)
	}
}
