//go:build windows

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	"golang.org/x/sys/windows"
)

// All records and PID files live in t.TempDir; the proxy is memory-only. The
// helper is ours, and stale creation stamps simulate another boot/PID instance.
func rebootRecoveryRecord(t *testing.T, core recoveryProcessIdentity) (encryptedRecoveryStore, string) {
	t.Helper()
	dir := t.TempDir()
	owner, err := captureRecoveryProcess(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	owner.Created ^= 1 // An old launcher instance, never the live test owner.
	store := encryptedRecoveryStore{path: filepath.Join(dir, proxyRecoveryFile)}
	record := &proxyRecoveryRecord{Version: 1, Session: "00112233445566778899aabbccddeeff", Owner: owner, Core: &core, Phase: "core"}
	if err := store.Save(record); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "core.pid"), []byte(strconv.Itoa(core.PID)), 0600); err != nil {
		t.Fatal(err)
	}
	return store, dir
}

func rebootDeniedCoreCheck(corePID int) func(recoveryProcessIdentity) (bool, error) {
	return func(id recoveryProcessIdentity) (bool, error) {
		if id.PID != corePID {
			return checkRecoveryProcess(id)
		}
		return checkRecoveryProcessWithSnapshot(id, func(recoveryProcessIdentity) (bool, error) {
			return false, windows.ERROR_ACCESS_DENIED
		}, readRecoveryProcessSnapshot)
	}
}

func TestRecoveryRebootReusedPIDRetiresJournalAndLegacy(t *testing.T) {
	h := startRecoveryProcessTestHelper(t, 0)
	stale := h.identity
	stale.Created ^= 1
	_, dir := rebootRecoveryRecord(t, stale)
	b := &memoryProxyBackend{state: proxyBackendOriginal()}
	stops := 0
	p := &WindowsProxy{backend: b, checkProcess: rebootDeniedCoreCheck(stale.PID), stopProcess: func(recoveryProcessIdentity) error { stops++; return nil }}
	if err := p.InitializeRecovery(dir); err != nil {
		t.Fatal("reused PID remained blocked", err)
	}
	if err := p.CheckRecoveryBeforeStart(); err != nil {
		t.Fatal(err)
	}
	if err := p.Recover(); err != nil {
		t.Fatal(err)
	}
	if p.RecoveryStatus().HasRecord || p.LastError() != "" {
		t.Fatal("obsolete recovery status remained")
	}
	a := &Application{baseDir: dir, dataDir: dir, proxy: p}
	if err := a.checkLegacyCore(); err != nil {
		t.Fatal("legacy PID fallback blocked the recovered session", err)
	}
	for _, name := range []string{proxyRecoveryFile, "core.pid"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("obsolete operational %s retained: %v", name, err)
		}
	}
	// A new proxy instance proves this was not an in-memory PID ignore list.
	q := &WindowsProxy{backend: b}
	if err := q.InitializeRecovery(dir); err != nil {
		t.Fatal(err)
	}
	a.proxy = q
	if err := a.checkLegacyCore(); err != nil {
		t.Fatal("second launch was blocked", err)
	}
	if b.reads != 0 || b.writes != 0 || b.applies != 0 || stops != 0 {
		t.Fatal("core-only recovery touched the proxy or stopped a process")
	}
	requireRecoveryProcessAlive(t, h.identity)
}

func TestRecoveryRebootUnknownSameInstanceRetainsEvidence(t *testing.T) {
	h := startRecoveryProcessTestHelper(t, 0)
	store, dir := rebootRecoveryRecord(t, h.identity)
	before, err := os.ReadFile(store.path)
	if err != nil {
		t.Fatal(err)
	}
	b := &memoryProxyBackend{state: proxyBackendOriginal()}
	stops := 0
	p := &WindowsProxy{backend: b, checkProcess: rebootDeniedCoreCheck(h.identity.PID), stopProcess: func(recoveryProcessIdentity) error { stops++; return windows.ERROR_ACCESS_DENIED }}
	if err := p.InitializeRecovery(dir); err == nil {
		t.Fatal("creation equality was accepted as full identity")
	}
	for _, action := range []func() error{p.CheckRecoveryBeforeStart, p.Recover, p.ForgetCore} {
		if err := action(); err == nil {
			t.Fatal("unknown identity bypassed recovery block")
		}
	}
	status := p.RecoveryStatus()
	if !status.HasRecord || !status.Blocked || status.CoreVerified {
		t.Fatal("unknown process was not reported fail-closed")
	}
	after, err := os.ReadFile(store.path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("unknown identity lost durable evidence")
	}
	if pid, err := readInt(filepath.Join(dir, "core.pid")); err != nil || pid != h.identity.PID {
		t.Fatal("unknown legacy PID was removed")
	}
	if b.reads != 0 || b.writes != 0 || b.applies != 0 || stops != 0 {
		t.Fatal("unknown core-only recovery had OS effects")
	}
	requireRecoveryProcessAlive(t, h.identity)
}

func TestRecoveryRebootLegacyRetirementIsConditionalAndRetryable(t *testing.T) {
	for _, mode := range []string{"missing", "different-pid", "malformed", "blocked-delete"} {
		t.Run(mode, func(t *testing.T) {
			h := startRecoveryProcessTestHelper(t, 0)
			stale := h.identity
			stale.Created ^= 1
			store, dir := rebootRecoveryRecord(t, stale)
			pidPath := filepath.Join(dir, "core.pid")
			var held windows.Handle
			switch mode {
			case "missing":
				if err := os.Remove(pidPath); err != nil {
					t.Fatal(err)
				}
			case "different-pid":
				if err := os.WriteFile(pidPath, []byte("2147483647"), 0600); err != nil {
					t.Fatal(err)
				}
			case "malformed":
				if err := os.WriteFile(pidPath, []byte("not-a-pid"), 0600); err != nil {
					t.Fatal(err)
				}
			case "blocked-delete":
				var err error
				held, err = windows.CreateFile(windows.StringToUTF16Ptr(pidPath), windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
				if err != nil {
					t.Fatal(err)
				}
				defer func() {
					if held != 0 {
						windows.CloseHandle(held)
					}
				}()
			}
			b := &memoryProxyBackend{state: proxyBackendOriginal()}
			p := &WindowsProxy{backend: b, checkProcess: rebootDeniedCoreCheck(stale.PID)}
			err := p.InitializeRecovery(dir)
			if mode == "malformed" || mode == "blocked-delete" {
				if err == nil || p.record == nil || p.record.Core == nil || p.CheckRecoveryBeforeStart() == nil {
					t.Fatal("cleanup failure discarded modern proof or allowed start")
				}
				if r, err := store.Load(); err != nil || r == nil || r.Core == nil {
					t.Fatal("cleanup failure lost durable identity")
				}
				if held != 0 {
					windows.CloseHandle(held)
					held = 0
				}
				if err := os.WriteFile(pidPath, []byte(strconv.Itoa(stale.PID)), 0600); err != nil {
					t.Fatal(err)
				}
				if err := p.Recover(); err != nil || p.record != nil || p.LastError() != "" {
					t.Fatal("cleanup retry failed", err)
				}
			} else if err != nil || p.record != nil {
				t.Fatal("safe conditional cleanup failed", err)
			}
			if mode == "different-pid" {
				if pid, err := readInt(pidPath); err != nil || pid != 2147483647 {
					t.Fatal("unrelated legacy PID was deleted")
				}
			}
			if b.reads != 0 || b.writes != 0 || b.applies != 0 {
				t.Fatal("core cleanup touched proxy")
			}
			requireRecoveryProcessAlive(t, h.identity)
		})
	}
}

func TestRecoveryRebootJournalFailureKeepsProofAfterLegacyRetirement(t *testing.T) {
	h := startRecoveryProcessTestHelper(t, 0)
	store, dir := rebootRecoveryRecord(t, h.identity)
	b := &memoryProxyBackend{state: proxyBackendOriginal()}
	p := &WindowsProxy{backend: b, checkProcess: rebootDeniedCoreCheck(h.identity.PID)}
	if err := p.InitializeRecovery(dir); err == nil {
		t.Fatal("expected unknown core")
	}
	p.checkProcess = func(id recoveryProcessIdentity) (bool, error) { return false, nil } // Synthetic confirmed exit.
	p.store = &failureRecoveryStore{delegate: store, failClear: true}
	if err := p.Recover(); err == nil || p.record == nil || p.record.Core == nil {
		t.Fatal("journal failure lost in-memory proof")
	}
	if r, err := store.Load(); err != nil || r == nil || r.Core == nil {
		t.Fatal("journal failure lost durable proof")
	}
	if _, err := os.Stat(filepath.Join(dir, "core.pid")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("legacy PID was not retired before journal save")
	}
	p.store = store
	if err := p.Recover(); err != nil || p.record != nil || p.LastError() != "" {
		t.Fatal("journal cleanup retry failed", err)
	}
	if b.reads != 0 || b.writes != 0 || b.applies != 0 {
		t.Fatal("journal cleanup touched proxy")
	}
	requireRecoveryProcessAlive(t, h.identity)
}

func TestRecoveryRebootOutOfRangeRecordPIDIsCorruptNotGone(t *testing.T) {
	if strconv.IntSize != 64 {
		t.Skip("out-of-range PID requires 64-bit int")
	}
	h := startRecoveryProcessTestHelper(t, 0)
	store, dir := rebootRecoveryRecord(t, h.identity)
	r, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	outOfRange := uint64(1)<<32 | uint64(r.Core.PID)
	r.Core.PID = int(outOfRange)
	if err := validateRecoveryRecord(r); err == nil {
		t.Fatal("out-of-range PID passed schema validation")
	}
	plain, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(plain)
	encrypted, err := store.crypt(plain, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.path, encrypted, 0600); err != nil {
		t.Fatal(err)
	}
	b := &memoryProxyBackend{state: proxyBackendOriginal()}
	p := &WindowsProxy{backend: b}
	if err := p.InitializeRecovery(dir); err == nil || p.CheckRecoveryBeforeStart() == nil {
		t.Fatal("invalid record was treated as a disappeared process")
	}
	after, err := os.ReadFile(store.path)
	if err != nil || !bytes.Equal(encrypted, after) {
		t.Fatal("invalid record was not retained")
	}
	if b.reads != 0 || b.writes != 0 || b.applies != 0 {
		t.Fatal("invalid record touched proxy")
	}
	requireRecoveryProcessAlive(t, h.identity)
}
func TestRecoveryRebootExitAndStopCannotBypassLegacySafety(t *testing.T) {
	for _, action := range []string{"exit", "stop"} {
		for _, mode := range []string{"matching", "different-pid", "malformed", "blocked-delete"} {
			t.Run(action+"/"+mode, func(t *testing.T) {
				h := startRecoveryProcessTestHelper(t, 0)
				holdRecoveryProcessTestHandle(t, h.identity.PID)
				store, dir := rebootRecoveryRecord(t, h.identity)
				b := &memoryProxyBackend{state: proxyBackendOriginal()}
				p := &WindowsProxy{backend: b}
				if err := p.InitializeRecovery(dir); err != nil {
					t.Fatal(err)
				}
				pidPath := filepath.Join(dir, "core.pid")
				var held windows.Handle
				switch mode {
				case "different-pid":
					if err := os.WriteFile(pidPath, []byte("2147483647"), 0600); err != nil {
						t.Fatal(err)
					}
				case "malformed":
					if err := os.WriteFile(pidPath, []byte("not-a-pid"), 0600); err != nil {
						t.Fatal(err)
					}
				case "blocked-delete":
					var err error
					held, err = windows.CreateFile(windows.StringToUTF16Ptr(pidPath), windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
					if err != nil {
						t.Fatal(err)
					}
					defer func() {
						if held != 0 {
							windows.CloseHandle(held)
						}
					}()
				}
				// Only our helper exits naturally on EOF, before running either cleanup path.
				if err := h.stdin.Close(); err != nil {
					t.Fatal(err)
				}
				waitRecoveryProcessTestHelper(t, h)
				if h.waitErr != nil {
					t.Fatal(h.waitErr)
				}
				s := &coreSession{cmd: &exec.Cmd{}, identity: h.identity, pid: h.identity.PID, done: h.done, stopping: true}
				a := &Application{baseDir: dir, dataDir: dir, logsDir: dir, config: &Settings{Listen: "127.0.0.1:17653"}, proxy: p, events: NewEventHub(), logger: slog.New(NewLogStore(20)), core: s, corePID: s.pid}
				var cleanupErr error
				if action == "exit" {
					a.handleCoreExit(s.pid, nil)
				} else {
					cleanupErr = a.Stop()
				}
				if mode == "malformed" || mode == "blocked-delete" {
					if action == "exit" && a.State().ConnectionError == "" || action == "stop" && cleanupErr == nil {
						t.Fatal("cleanup failure was hidden")
					}
					if r, err := store.Load(); err != nil || r == nil || r.Core == nil {
						t.Fatal("outer lifecycle bypassed retained modern proof")
					}
					if _, err := os.Stat(pidPath); err != nil {
						t.Fatal("outer lifecycle bypassed PID safety check", err)
					}
					if held != 0 {
						windows.CloseHandle(held)
						held = 0
					}
					if err := os.WriteFile(pidPath, []byte(strconv.Itoa(s.pid)), 0600); err != nil {
						t.Fatal(err)
					}
					if err := p.Recover(); err != nil {
						t.Fatal("lifecycle cleanup could not retry", err)
					}
				} else {
					if cleanupErr != nil || p.record != nil {
						t.Fatal("normal lifecycle did not retire identity", cleanupErr)
					}
					if mode == "different-pid" {
						if pid, err := readInt(pidPath); err != nil || pid != 2147483647 {
							t.Fatal("outer lifecycle deleted an unrelated PID")
						}
					} else if _, err := os.Stat(pidPath); !errors.Is(err, os.ErrNotExist) {
						t.Fatal("normal lifecycle retained own PID")
					}
				}
				if b.reads != 0 || b.writes != 0 || b.applies != 0 {
					t.Fatal("core-only lifecycle changed proxy")
				}
			})
		}
	}
}
func TestRecoveryRebootReparsePIDPathRetainsAllEvidence(t *testing.T) {
	h := startRecoveryProcessTestHelper(t, 0)
	stale := h.identity
	stale.Created ^= 1
	store, dir := rebootRecoveryRecord(t, stale)
	pidPath := filepath.Join(dir, "core.pid")
	target := filepath.Join(dir, "isolated-pid-target")
	if err := os.WriteFile(target, []byte(strconv.Itoa(stale.PID)), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(pidPath); err != nil {
		t.Fatal(err)
	}
	// Both link and target are owned isolated fixtures. No privilege escalation.
	if err := os.Symlink(target, pidPath); err != nil {
		t.Skipf("isolated file symlink unavailable: %v", err)
	}
	b := &memoryProxyBackend{state: proxyBackendOriginal()}
	p := &WindowsProxy{backend: b, checkProcess: rebootDeniedCoreCheck(stale.PID)}
	if err := p.InitializeRecovery(dir); err == nil {
		t.Fatal("reparse PID path bypassed recovery path check")
	}
	if r, err := store.Load(); err != nil || r == nil || r.Core == nil {
		t.Fatal("reparse PID path lost modern evidence")
	}
	if fi, err := os.Lstat(pidPath); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatal("reparse fixture was deleted")
	}
	if pid, err := readInt(target); err != nil || pid != stale.PID {
		t.Fatal("reparse target was changed")
	}
	if b.reads != 0 || b.writes != 0 || b.applies != 0 {
		t.Fatal("reparse rejection touched proxy")
	}
	requireRecoveryProcessAlive(t, h.identity)
}
func TestRecoveryRebootNoSessionDisconnectRetriesRetirement(t *testing.T) {
	for _, mode := range []string{"malformed", "blocked-delete"} {
		t.Run(mode, func(t *testing.T) {
			h := startRecoveryProcessTestHelper(t, 0)
			holdRecoveryProcessTestHandle(t, h.identity.PID)
			_, dir := rebootRecoveryRecord(t, h.identity)
			b := &memoryProxyBackend{state: proxyBackendOriginal()}
			p := &WindowsProxy{backend: b}
			if err := p.InitializeRecovery(dir); err != nil {
				t.Fatal(err)
			}
			pidPath := filepath.Join(dir, "core.pid")
			var held windows.Handle
			if mode == "malformed" {
				if err := os.WriteFile(pidPath, []byte("not-a-pid"), 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				var err error
				held, err = windows.CreateFile(windows.StringToUTF16Ptr(pidPath), windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
				if err != nil {
					t.Fatal(err)
				}
				defer func() {
					if held != 0 {
						windows.CloseHandle(held)
					}
				}()
			}
			if err := h.stdin.Close(); err != nil {
				t.Fatal(err)
			}
			waitRecoveryProcessTestHelper(t, h)
			s := &coreSession{identity: h.identity, pid: h.identity.PID, done: h.done, stopping: true}
			a := &Application{baseDir: dir, dataDir: dir, logsDir: dir, config: &Settings{Listen: "127.0.0.1:17653"}, proxy: p, events: NewEventHub(), logger: slog.New(NewLogStore(20)), core: s, corePID: s.pid}
			a.handleCoreExit(s.pid, nil)
			if a.core != nil || p.record == nil || p.LastError() == "" {
				t.Fatal("expected exited core with retained cleanup failure")
			}
			if err := a.disconnect(); err == nil || a.State().ConnectionError == "" || p.LastError() == "" {
				t.Fatal("no-session disconnect hid the pending retirement failure")
			}
			if held != 0 {
				windows.CloseHandle(held)
				held = 0
			}
			if err := os.WriteFile(pidPath, []byte(strconv.Itoa(s.pid)), 0600); err != nil {
				t.Fatal(err)
			}
			if err := a.disconnect(); err != nil || p.record != nil || p.LastError() != "" || a.State().ConnectionError != "" || a.State().LastError != "" {
				t.Fatal("no-session disconnect did not retry cleanup and retire stale errors", err)
			}
			if b.reads != 0 || b.writes != 0 || b.applies != 0 {
				t.Fatal("core-only disconnect retry changed proxy")
			}
		})
	}
}
