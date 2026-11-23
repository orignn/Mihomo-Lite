//go:build windows

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func recoveryTestProxy(t *testing.T, b proxyBackend, dir string) *WindowsProxy {
	t.Helper()
	p := &WindowsProxy{backend: b, checkProcess: func(recoveryProcessIdentity) (bool, error) { return false, nil }}
	if err := p.InitializeRecovery(dir); err != nil {
		t.Fatal(err)
	}
	return p
}
func TestRecoveryEncryptedRestartAndIdempotence(t *testing.T) {
	dir := t.TempDir()
	before := proxyBackendOriginal()
	b := &memoryProxyBackend{state: before}
	p := recoveryTestProxy(t, b, dir)
	if err := p.Enable("127.0.0.1:17890", windowsProxyBypass); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, proxyRecoveryFile))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(before.server)) || bytes.Contains(raw, []byte(before.autoConfigURL)) || bytes.Contains(raw, []byte("Original")) {
		t.Fatal("recovery file contains plaintext")
	}
	q := recoveryTestProxy(t, b, dir)
	if b.state != before || q.original != nil {
		t.Fatal("fresh process did not restore original")
	}
	writes, applies := b.writes, b.applies
	if err := q.Recover(); err != nil {
		t.Fatal(err)
	}
	if b.writes != writes || b.applies != applies {
		t.Fatal("repeat recovery changed proxy")
	}
	if _, err := os.Stat(filepath.Join(dir, proxyRecoveryFile)); !os.IsNotExist(err) {
		t.Fatal("clean journal not removed")
	}
}
func TestRecoveryDoesNotOverwriteTakeover(t *testing.T) {
	for _, field := range []string{"server", "enabled", "bypass", "pac", "detect", "partial"} {
		t.Run(field, func(t *testing.T) {
			dir := t.TempDir()
			before := proxyBackendOriginal()
			b := &memoryProxyBackend{state: before}
			p := recoveryTestProxy(t, b, dir)
			if err := p.Enable("127.0.0.1:17890", windowsProxyBypass); err != nil {
				t.Fatal(err)
			}
			switch field {
			case "server":
				b.state.server = "other"
			case "enabled":
				b.state.enabled = 0
			case "bypass":
				b.state.bypass = "other"
			case "pac":
				b.state.autoConfigURL = "other"
			case "detect":
				b.state.autoDetect = 0
			case "partial":
				b.state.bypass = before.bypass
			}
			external := b.state
			writes, applies := b.writes, b.applies
			if err := p.Disable(); !errors.Is(err, errProxyOwnership) {
				t.Fatal("live restore failed to reject takeover")
			}
			q := &WindowsProxy{backend: b, checkProcess: func(recoveryProcessIdentity) (bool, error) { return false, nil }}
			if err := q.InitializeRecovery(dir); !errors.Is(err, errProxyOwnership) {
				t.Fatal("restart failed to reject takeover")
			}
			if b.state != external || b.writes != writes || b.applies != applies {
				t.Fatal("takeover overwritten")
			}
			if !q.RecoveryStatus().Conflict || q.CheckRecoveryBeforeStart() == nil {
				t.Fatal("missing takeover block")
			}
			if proxyUsesRecoveryEndpoint(external, "127.0.0.1:17890") {
				if err := q.KeepCurrentProxy(); err == nil || q.original == nil {
					t.Fatal("released snapshot while proxy still depends on core")
				}
				if b.state != external || b.writes != writes || b.applies != applies {
					t.Fatal("dependency rejection touched proxy")
				}
				b.state.enabled = 0 // Explicit synthetic user change in Windows settings.
				external = b.state
			}
			if err := q.KeepCurrentProxy(); err != nil {
				t.Fatal(err)
			}
			if b.state != external || b.writes != writes || b.applies != applies || q.original != nil {
				t.Fatal("keep-current touched proxy")
			}
		})
	}
}
func TestRecoveryFailuresKeepDurableRetry(t *testing.T) {
	for _, mode := range []string{"write", "apply", "verify", "cleanup"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			before := proxyBackendOriginal()
			b := &memoryProxyBackend{state: before}
			p := recoveryTestProxy(t, b, dir)
			if err := p.Enable("127.0.0.1:17890", windowsProxyBypass); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "write":
				b.writeFailureAt = map[int]bool{b.writes + 1: true}
			case "apply":
				b.applyFailureAt = b.applies + 1
			case "verify":
				b.readFailureAt = b.reads + 3
			case "cleanup":
				p.store = &failureRecoveryStore{delegate: p.store, failClear: true}
			}
			if err := p.Disable(); err == nil {
				t.Fatal("expected recovery failure")
			}
			if p.original == nil {
				t.Fatal("failure lost in-memory snapshot")
			}
			store := encryptedRecoveryStore{path: filepath.Join(dir, proxyRecoveryFile)}
			record, err := store.Load()
			if err != nil || record == nil || record.Original == nil {
				t.Fatal("failure lost durable snapshot")
			}
			b.readFailureAt = 0
			b.writeFailureAt = nil
			b.applyFailureAt = 0
			q := recoveryTestProxy(t, b, dir)
			if b.state != before || q.original != nil {
				t.Fatal("restart retry not restored")
			}
		})
	}
}

type failureRecoveryStore struct {
	delegate  recoveryStore
	failClear bool
}

func (s *failureRecoveryStore) Load() (*proxyRecoveryRecord, error) { return s.delegate.Load() }
func (s *failureRecoveryStore) Save(r *proxyRecoveryRecord) error {
	if !s.failClear || r == nil || r.Original == nil {
		return errors.New("simulated storage failure")
	}
	return s.delegate.Save(r)
}
func TestRecoveryPrepareFailureNeverTouchesProxy(t *testing.T) {
	b := &memoryProxyBackend{state: proxyBackendOriginal()}
	p := recoveryTestProxy(t, b, t.TempDir())
	p.store = &failureRecoveryStore{delegate: p.store}
	if err := p.Enable("127.0.0.1:17890", windowsProxyBypass); err == nil {
		t.Fatal("expected durable preparation failure")
	}
	if b.writes != 0 || b.applies != 0 || p.original != nil {
		t.Fatal("changed proxy before durable snapshot")
	}
}
func TestRecoveryLiveOwnerOrUnknownBlocksAllEffects(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		t.Run(map[bool]string{false: "alive", true: "unknown"}[unknown], func(t *testing.T) {
			dir := t.TempDir()
			b := &memoryProxyBackend{state: proxyBackendOriginal()}
			p := recoveryTestProxy(t, b, dir)
			if err := p.Enable("127.0.0.1:17890", windowsProxyBypass); err != nil {
				t.Fatal(err)
			}
			q := &WindowsProxy{backend: b, checkProcess: func(recoveryProcessIdentity) (bool, error) {
				if unknown {
					return false, errors.New("denied")
				}
				return true, nil
			}}
			writes, applies := b.writes, b.applies
			if err := q.InitializeRecovery(dir); err == nil {
				t.Fatal("accepted live or unknown owner")
			}
			if err := q.Disable(); err == nil {
				t.Fatal("disable bypassed owner check")
			}
			if q.CheckRecoveryBeforeStart() == nil || !q.RecoveryStatus().Blocked {
				t.Fatal("missing fail-closed block")
			}
			if b.writes != writes || b.applies != applies {
				t.Fatal("live owner overwritten")
			}
		})
	}
}
func TestRecoveryCorruptOrCopiedRecordIsRetained(t *testing.T) {
	for _, mode := range []string{"corrupt", "copied", "oversized", "schema"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			b := &memoryProxyBackend{state: proxyBackendOriginal()}
			p := recoveryTestProxy(t, b, dir)
			if err := p.Enable("127.0.0.1:17890", windowsProxyBypass); err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(dir, proxyRecoveryFile)
			raw, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "corrupt":
				raw[0] ^= 255
			case "copied":
				dir = t.TempDir()
				file = filepath.Join(dir, proxyRecoveryFile)
			case "oversized":
				raw = make([]byte, (1<<20)+1)
			case "schema":
				r := *p.record
				r.Version = 99
				data, _ := json.Marshal(&r)
				raw, err = (encryptedRecoveryStore{path: file}).crypt(data, true)
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(file, raw, 0600); err != nil {
				t.Fatal(err)
			}
			writes, applies := b.writes, b.applies
			q := &WindowsProxy{backend: b}
			if err := q.InitializeRecovery(dir); err == nil {
				t.Fatal("accepted unusable journal")
			}
			if q.CheckRecoveryBeforeStart() == nil {
				t.Fatal("unusable journal did not block connect")
			}
			if b.writes != writes || b.applies != applies {
				t.Fatal("unusable journal touched proxy")
			}
			retained, err := os.ReadFile(file)
			if err != nil || !bytes.Equal(raw, retained) {
				t.Fatal("unusable journal changed")
			}
		})
	}
}
func TestRecoveryLeftoverCoreRequiresOwnerAndProxySafety(t *testing.T) {
	dir := t.TempDir()
	b := &memoryProxyBackend{state: proxyBackendOriginal()}
	p := recoveryTestProxy(t, b, dir)
	identity, err := captureRecoveryProcess(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if err := p.TrackCore(identity); err != nil {
		t.Fatal(err)
	}
	if err := p.Enable("127.0.0.1:17890", windowsProxyBypass); err != nil {
		t.Fatal(err)
	}
	p.foreignSession = true
	p.checkProcess = func(i recoveryProcessIdentity) (bool, error) {
		return i.PID == p.record.Core.PID && i.Created == p.record.Core.Created && i.PID != p.record.Owner.PID, nil
	}
	// Distinct synthetic launcher identity, fake core lifecycle. Never terminate any real process.
	p.record.Owner.PID = 123
	p.record.Owner.Created = 1
	p.checkProcess = func(i recoveryProcessIdentity) (bool, error) { return i.PID == os.Getpid(), nil }
	stopped := 0
	p.stopProcess = func(recoveryProcessIdentity) error {
		if b.state != proxyBackendOriginal() {
			t.Fatal("stopped core before restore")
		}
		stopped++
		return nil
	}
	if !p.RecoveryStatus().CoreVerified || p.CheckRecoveryBeforeStart() == nil {
		t.Fatal("leftover not blocked")
	}
	if err := p.StopLeftoverCore(); err != nil {
		t.Fatal(err)
	}
	if stopped != 1 || p.record != nil {
		t.Fatal("leftover not retired")
	}
	if err := p.StopLeftoverCore(); err != nil || stopped != 1 {
		t.Fatal("repeat stop not idempotent")
	}
}

// Automatic config changes are ownership changes too, not successful enables.
type recoveryPACChangingBackend struct{ memoryProxyBackend }

func (b *recoveryPACChangingBackend) Apply(server, bypass string) error {
	if err := b.memoryProxyBackend.Apply(server, bypass); err != nil {
		return err
	}
	b.state.autoConfigURL = "synthetic-third-party-pac"
	return nil
}
func TestRecoveryEnableVerifiesAutomaticProxyFields(t *testing.T) {
	b := &recoveryPACChangingBackend{memoryProxyBackend: memoryProxyBackend{state: proxyBackendOriginal()}}
	p := recoveryTestProxy(t, b, t.TempDir())
	if err := p.Enable("127.0.0.1:17890", windowsProxyBypass); err == nil {
		t.Fatal("PAC takeover reported as successful enable")
	}
	if p.original == nil || b.writes != 1 {
		t.Fatal("PAC takeover was overwritten or snapshot discarded")
	}
}

func TestRecoveryKeepCurrentCannotStrandDependentCore(t *testing.T) {
	dir := t.TempDir()
	b := &memoryProxyBackend{state: proxyBackendOriginal()}
	p := recoveryTestProxy(t, b, dir)
	identity, err := captureRecoveryProcess(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if err := p.TrackCore(identity); err != nil {
		t.Fatal(err)
	}
	if err := p.Enable("127.0.0.1:17890", windowsProxyBypass); err != nil {
		t.Fatal(err)
	}
	p.foreignSession = true
	b.state.bypass = "synthetic-third-party-bypass"
	if err := p.KeepCurrentProxy(); err == nil || p.original == nil {
		t.Fatal("released still-dependent snapshot")
	}
	b.state.enabled = 0
	if err := p.KeepCurrentProxy(); err != nil {
		t.Fatal(err)
	}
	if p.record == nil || p.record.Endpoint != "127.0.0.1:17890" {
		t.Fatal("lost core endpoint after snapshot release")
	}
	// A different program can re-enable the endpoint after the confirmation dialog.
	b.state.enabled = 1
	b.state.server = "http=localhost:17890;https=127.0.0.1:17890"
	stopped := 0
	p.stopProcess = func(recoveryProcessIdentity) error { stopped++; return nil }
	writes, applies := b.writes, b.applies
	if err := p.StopLeftoverCore(); err == nil || stopped != 0 {
		t.Fatal("stopped a core still used by system proxy")
	}
	if b.writes != writes || b.applies != applies {
		t.Fatal("dependency check changed third-party proxy")
	}
	b.state.server = "other-proxy:18888"
	if err := p.StopLeftoverCore(); err != nil || stopped != 1 {
		t.Fatal("independent core did not stop", err)
	}
}
func TestRecoveryEndpointReferences(t *testing.T) {
	for _, server := range []string{"127.0.0.1:17890", "http=127.0.0.1:17890;https=other:80", "socks=localhost:17890", "[::1]:17890"} {
		if !proxyUsesRecoveryEndpoint(proxySnapshot{enabled: 1, server: server}, "127.0.0.1:17890") {
			t.Fatal("missed loopback dependency")
		}
	}
	if proxyUsesRecoveryEndpoint(proxySnapshot{enabled: 0, server: "127.0.0.1:17890"}, "127.0.0.1:17890") {
		t.Fatal("disabled manual proxy counted as active")
	}
}

// File-backed synthetic proxy: helper EXE crash/restart, with zero registry/WinINet effects.
type recoveryFileBackend struct{ path string }

func (b recoveryFileBackend) Read() (proxySnapshot, error) {
	data, err := os.ReadFile(b.path)
	if err != nil {
		return proxySnapshot{}, err
	}
	var s recoverySnapshot
	err = json.Unmarshal(data, &s)
	return s.snapshot(), err
}
func (b recoveryFileBackend) Write(s proxySnapshot) error {
	data, err := json.Marshal(snapshotForRecovery(s))
	if err != nil {
		return err
	}
	return atomicWrite(b.path, data, 0600)
}
func (b recoveryFileBackend) Apply(string, string) error { return nil }
func TestRecoveryCrashHelper(t *testing.T) {
	dir := os.Getenv("MIHOMOLITE_RECOVERY_TEST_DIR")
	if dir == "" {
		return
	}
	b := recoveryFileBackend{path: filepath.Join(dir, "synthetic-proxy.json")}
	p := &WindowsProxy{backend: b}
	if err := p.InitializeRecovery(dir); err != nil {
		os.Exit(81)
	}
	if err := p.Enable("127.0.0.1:17890", windowsProxyBypass); err != nil {
		os.Exit(82)
	}
	switch os.Getenv("MIHOMOLITE_RECOVERY_TEST_PHASE") {
	case "restored-registry":
		if err := b.Write(proxyBackendOriginal()); err != nil {
			os.Exit(83)
		}
	case "partial":
		s, _ := b.Read()
		s.bypass = proxyBackendOriginal().bypass
		if err := b.Write(s); err != nil {
			os.Exit(84)
		}
	}
	os.Exit(87) // Simulated abrupt launcher exit: deliberately skips Disable and test defers.
}
func TestRecoveryAcrossActualHelperProcessExit(t *testing.T) {
	for _, phase := range []string{"applied", "restored-registry", "partial"} {
		t.Run(phase, func(t *testing.T) {
			dir := t.TempDir()
			b := recoveryFileBackend{path: filepath.Join(dir, "synthetic-proxy.json")}
			before := proxyBackendOriginal()
			if err := b.Write(before); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(os.Args[0], "-test.run=^TestRecoveryCrashHelper$")
			cmd.Env = append(os.Environ(), "MIHOMOLITE_RECOVERY_TEST_DIR="+dir, "MIHOMOLITE_RECOVERY_TEST_PHASE="+phase)
			err := cmd.Run()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 87 {
				t.Fatal("helper did not reach crash checkpoint", err)
			}
			p := &WindowsProxy{backend: b}
			err = p.InitializeRecovery(dir)
			current, readErr := b.Read()
			if readErr != nil {
				t.Fatal(readErr)
			}
			if phase == "partial" {
				if !errors.Is(err, errProxyOwnership) || current == before || p.original == nil {
					t.Fatal("partial state not held for explicit diagnosis")
				}
			} else if err != nil || current != before || p.original != nil {
				t.Fatal("new process did not recover", err)
			}
		})
	}
}
