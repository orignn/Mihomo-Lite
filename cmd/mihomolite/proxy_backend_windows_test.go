//go:build windows

package main

import (
	"errors"
	"strings"
	"sync"
	"testing"
)

type memoryProxyBackend struct {
	state                         proxySnapshot
	reads, writes, applies        int
	readFailureAt, applyFailureAt int
	writeFailureAt                map[int]bool
}

func (b *memoryProxyBackend) Read() (proxySnapshot, error) {
	b.reads++
	if b.reads == b.readFailureAt {
		return proxySnapshot{}, errors.New("read failed")
	}
	return b.state, nil
}
func (b *memoryProxyBackend) Write(s proxySnapshot) error {
	b.writes++
	b.state = s // Simulate a partial OS write before a possible failure.
	if b.writeFailureAt[b.writes] {
		return errors.New("write failed")
	}
	return nil
}
func (b *memoryProxyBackend) Apply(string, string) error {
	b.applies++
	if b.applies == b.applyFailureAt {
		return errors.New("apply failed")
	}
	return nil
}
func proxyBackendOriginal() proxySnapshot {
	return proxySnapshot{enabled: 1, server: "127.0.0.1:18888", bypass: "preserve-local", autoConfigURL: "https://example.invalid/pac", autoDetect: 1}
}

func TestWindowsProxyRepeatedDisablePreservesOriginal(t *testing.T) {
	before := proxyBackendOriginal()
	b := &memoryProxyBackend{state: before}
	p := &WindowsProxy{backend: b}
	if err := p.Enable("127.0.0.1:17890", windowsProxyBypass); err != nil {
		t.Fatal(err)
	}
	if b.state.server == before.server {
		t.Fatal("Enable did not update backend")
	}
	if err := p.Disable(); err != nil {
		t.Fatal(err)
	}
	if b.state != before {
		t.Fatalf("original snapshot not restored: %+v", b.state)
	}
	reads, writes, applies := b.reads, b.writes, b.applies
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := p.Disable(); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if b.state != before || b.reads != reads || b.writes != writes || b.applies != applies {
		t.Fatal("second cleanup touched another application's proxy")
	}
}

func TestWindowsProxyUnownedDisableDoesNotTouchWindows(t *testing.T) {
	b := &memoryProxyBackend{state: proxyBackendOriginal()}
	p := &WindowsProxy{backend: b}
	if err := p.Disable(); err != nil {
		t.Fatal(err)
	}
	if b.reads != 0 || b.writes != 0 || b.applies != 0 {
		t.Fatal("unowned cleanup accessed the OS backend")
	}
}

func TestWindowsProxyEnableFailuresRollBackSnapshot(t *testing.T) {
	for _, mode := range []string{"write", "apply", "verify"} {
		t.Run(mode, func(t *testing.T) {
			before := proxyBackendOriginal()
			b := &memoryProxyBackend{state: before}
			switch mode {
			case "write":
				b.writeFailureAt = map[int]bool{1: true}
			case "apply":
				b.applyFailureAt = 1
			case "verify":
				b.readFailureAt = 2
			}
			p := &WindowsProxy{backend: b}
			if err := p.Enable("127.0.0.1:17890", windowsProxyBypass); err == nil {
				t.Fatal("expected enable failure")
			}
			if b.state != before || p.original != nil {
				t.Fatalf("failed enable did not roll back: %+v original=%+v", b.state, p.original)
			}
			writes := b.writes
			if err := p.Disable(); err != nil {
				t.Fatal(err)
			}
			if b.writes != writes {
				t.Fatal("successful rollback was overwritten by later cleanup")
			}
		})
	}
}

func TestWindowsProxyFailedRollbackKeepsRetrySnapshot(t *testing.T) {
	before := proxyBackendOriginal()
	b := &memoryProxyBackend{state: before, writeFailureAt: map[int]bool{1: true, 2: true}}
	p := &WindowsProxy{backend: b}
	if err := p.Enable("127.0.0.1:17890", windowsProxyBypass); err == nil || !strings.Contains(err.Error(), "恢复原系统代理失败") {
		t.Fatalf("rollback failure missing: %v", err)
	}
	if p.original == nil {
		t.Fatal("lost the only original snapshot after failed restore")
	}
	if err := p.Disable(); err != nil {
		t.Fatal(err)
	}
	if b.state != before || p.original != nil || p.LastError() != "" {
		t.Fatal("retry did not restore/clear proxy ownership")
	}
}
