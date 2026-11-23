//go:build windows

package main

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

const recoveryProcessHelperMarker = "MIHOMOLITE_OPT001_PROCESS_HELPER"
const recoveryProcessHelperExitCode = "MIHOMOLITE_OPT001_PROCESS_HELPER_EXIT_CODE"
const recoveryGuardHelperName = "MIHOMOLITE_OPT001_GUARD_HELPER_NAME"

// Only an explicitly selected test in this test executable can become a helper.
// It uses its private working directory/stdin and never enters the application.
func TestRecoveryProcessHelper(t *testing.T) {
	if os.Getenv(recoveryProcessHelperMarker) != "1" {
		t.Skip("isolated child process only")
	}
	code, err := strconv.Atoi(os.Getenv(recoveryProcessHelperExitCode))
	if err != nil {
		t.Fatal(err)
	}
	if name := os.Getenv(recoveryGuardHelperName); name != "" {
		prefix, err := proxyRecoveryGuardName()
		if err != nil {
			t.Fatal(err)
		}
		suffix := strings.TrimPrefix(name, prefix+".Test.")
		decoded, err := hex.DecodeString(suffix)
		if !strings.HasPrefix(name, prefix+".Test.") || err != nil || len(decoded) != 16 || hex.EncodeToString(decoded) != suffix {
			t.Fatal("refusing a non-isolated helper mutex name")
		}
		// Test-only unique mutex. os.Exit deliberately abandons ownership.
		if _, err := acquireNamedProxyRecoveryGuard(name); err != nil {
			t.Fatal(err)
		}
	}
	fmt.Fprintln(os.Stdout, "opt-001-helper-ready")
	if _, err := io.Copy(io.Discard, os.Stdin); err != nil {
		t.Fatal(err)
	}
	os.Exit(code)
}

type recoveryProcessTestHelper struct {
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	done     chan struct{}
	waitErr  error
	identity recoveryProcessIdentity
}

func startRecoveryProcessTestHelper(t *testing.T, exitCode int, extraEnv ...string) *recoveryProcessTestHelper {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-test.run=^TestRecoveryProcessHelper$", "-test.count=1")
	cmd.Dir = t.TempDir()
	cmd.Env = append(os.Environ(), recoveryProcessHelperMarker+"=1", recoveryProcessHelperExitCode+"="+strconv.Itoa(exitCode))
	cmd.Env = append(cmd.Env, extraEnv...)
	cmd.Stderr = os.Stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		stdin.Close()
		stdout.Close()
		t.Fatal(err)
	}
	h := &recoveryProcessTestHelper{cmd: cmd, stdin: stdin, done: make(chan struct{})}
	go func() {
		h.waitErr = cmd.Wait()
		close(h.done)
	}()
	t.Cleanup(func() {
		stdin.Close() // Prefer a graceful EOF exit for our own isolated child.
		select {
		case <-h.done:
		case <-time.After(5 * time.Second):
			// os.Process.Kill uses the retained child handle, never a reopened PID.
			_ = cmd.Process.Kill()
			select {
			case <-h.done:
			case <-time.After(5 * time.Second):
				t.Error("isolated helper did not exit during cleanup")
			}
		}
	})
	ready := make(chan error, 1)
	go func() {
		line, err := bufio.NewReader(stdout).ReadString('\n')
		if err == nil && strings.TrimSpace(line) != "opt-001-helper-ready" {
			err = errors.New("unexpected helper readiness response")
		}
		ready <- err
	}()
	select {
	case err := <-ready:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("isolated helper readiness timed out")
	}
	h.identity, err = captureRecoveryProcess(cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	if h.identity.PID == os.Getpid() {
		t.Fatal("refusing to use current test process as a stop target")
	}
	return h
}

func waitRecoveryProcessTestHelper(t *testing.T, h *recoveryProcessTestHelper) {
	t.Helper()
	select {
	case <-h.done:
	case <-time.After(5 * time.Second):
		t.Fatal("isolated helper exit timed out")
	}
}

func requireRecoveryProcessAlive(t *testing.T, identity recoveryProcessIdentity) {
	t.Helper()
	alive, err := checkRecoveryProcess(identity)
	if err != nil || !alive {
		t.Fatalf("expected matching live process: alive=%v err=%v", alive, err)
	}
}

func holdRecoveryProcessTestHandle(t *testing.T, pid int) windows.Handle {
	t.Helper()
	handle, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { windows.CloseHandle(handle) })
	return handle
}

func uniqueRecoveryGuardTestName(t *testing.T) string {
	t.Helper()
	name, err := proxyRecoveryGuardName()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(name, `Global\MihomoLite.ProxyRecovery.S-1-`) || strings.Count(name, `\`) != 1 {
		t.Fatal("guard name is not Global/account-SID scoped")
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		t.Fatal(err)
	}
	return name + ".Test." + hex.EncodeToString(random[:])
}

func TestRecoveryGuardExclusiveAndCrossGoroutineRelease(t *testing.T) {
	name := uniqueRecoveryGuardTestName(t)
	release, err := acquireNamedProxyRecoveryGuard(name)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(release)
	if second, err := acquireNamedProxyRecoveryGuard(name); err == nil || second != nil {
		if second != nil {
			second()
		}
		t.Fatal("guard allowed simultaneous recovery owners")
	}
	var group sync.WaitGroup
	for i := 0; i < 20; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			release()
		}()
	}
	group.Wait()
	again, err := acquireNamedProxyRecoveryGuard(name)
	if err != nil {
		t.Fatalf("guard was not released on its owning OS thread: %v", err)
	}
	again()
	if release, err := acquireNamedProxyRecoveryGuard(name + "\x00"); err == nil || release != nil {
		t.Fatal("invalid guard name did not fail closed")
	}
}

func TestRecoveryGuardChildOwnershipAndAbandonment(t *testing.T) {
	name := uniqueRecoveryGuardTestName(t)
	h := startRecoveryProcessTestHelper(t, 0, recoveryGuardHelperName+"="+name)
	// Keep the named mutex alive across child exit to exercise WAIT_ABANDONED,
	// rather than simply creating a fresh mutex after its last handle closes.
	wideName, err := windows.UTF16PtrFromString(name)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateMutexEx(nil, wideName, 0, windows.SYNCHRONIZE|windows.MUTEX_MODIFY_STATE)
	if err != nil && !errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		t.Fatal(err)
	}
	defer windows.CloseHandle(handle)
	if release, err := acquireNamedProxyRecoveryGuard(name); err == nil || release != nil {
		if release != nil {
			release()
		}
		t.Fatal("guard ignored ownership by the isolated child")
	}
	if err := h.stdin.Close(); err != nil {
		t.Fatal(err)
	}
	waitRecoveryProcessTestHelper(t, h)
	if h.waitErr != nil {
		t.Fatalf("guard helper did not exit normally: %v", h.waitErr)
	}
	release, err := acquireNamedProxyRecoveryGuard(name)
	if err != nil {
		t.Fatalf("abandoned guard could not be recovered: %v", err)
	}
	release()
}

func TestRecoveryProcessCaptureCurrent(t *testing.T) {
	identity, err := captureRecoveryProcess(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if identity.PID != os.Getpid() || identity.Created == 0 || !filepath.IsAbs(identity.Image) {
		t.Fatal("capture did not return a complete current-process identity")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(filepath.Clean(exe), filepath.Clean(identity.Image)) {
		t.Fatal("captured image is not the full current test executable path")
	}
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(windows.CurrentProcess(), &created, &exited, &kernel, &user); err != nil {
		t.Fatal(err)
	}
	if want := uint64(created.HighDateTime)<<32 | uint64(created.LowDateTime); identity.Created != want {
		t.Fatalf("creation FILETIME changed: got %d want %d", identity.Created, want)
	}
	requireRecoveryProcessAlive(t, identity) // Never stop the current test process.
	encoded, err := json.Marshal(identity)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil || len(fields) != 3 || fields["pid"] == nil || fields["created"] == nil || fields["image"] == nil {
		t.Fatal("identity JSON keys do not match the recovery format")
	}
	var decoded recoveryProcessIdentity
	if err := json.Unmarshal(encoded, &decoded); err != nil || decoded != identity {
		t.Fatal("identity JSON round trip did not preserve FILETIME/image")
	}
}

func TestRecoveryProcessHelperCaptureAndIdentityMismatch(t *testing.T) {
	h := startRecoveryProcessTestHelper(t, 0)
	requireRecoveryProcessAlive(t, h.identity)
	again, err := captureRecoveryProcess(h.identity.PID)
	if err != nil || again != h.identity {
		t.Fatalf("repeated capture changed identity: err=%v", err)
	}
	for _, mode := range []string{"pid-reuse-created", "different-image", "basename-only", "missing-created", "missing-image"} {
		t.Run(mode, func(t *testing.T) {
			wrong := h.identity
			switch mode {
			case "pid-reuse-created":
				wrong.Created ^= 1 // Same live PID, creation time of another instance.
			case "different-image":
				wrong.Image = filepath.Join(filepath.Dir(wrong.Image), "opt-001-not-the-helper.exe")
			case "basename-only":
				wrong.Image = filepath.Base(wrong.Image)
			case "missing-created":
				wrong.Created = 0
			case "missing-image":
				wrong.Image = ""
			}
			if alive, err := checkRecoveryProcess(wrong); err != nil || alive {
				t.Fatalf("wrong identity matched: alive=%v err=%v", alive, err)
			}
			if err := stopRecoveryProcess(wrong); err == nil {
				t.Fatal("stop did not report refusal for a mismatching/incomplete identity")
			}
			requireRecoveryProcessAlive(t, h.identity)
		})
	}
}

func TestRecoveryProcessMissingAndInvalidPID(t *testing.T) {
	// Windows allocates process IDs on four-byte boundaries; this high odd PID
	// is intentionally nonexistent, without enumerating unrelated processes.
	const missingPID = int(^uint32(0) >> 1)
	identity := recoveryProcessIdentity{PID: missingPID, Created: 1, Image: `C:\opt-001-missing.exe`}
	if alive, err := checkRecoveryProcess(identity); err != nil || alive {
		t.Fatalf("missing PID was not absent: alive=%v err=%v", alive, err)
	}
	if _, err := captureRecoveryProcess(missingPID); !errors.Is(err, windows.ERROR_INVALID_PARAMETER) || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing capture did not return the missing PID error: %v", err)
	}
	if err := stopRecoveryProcess(identity); err != nil {
		t.Fatalf("missing process stop was not idempotent: %v", err)
	}
	invalid := []int{0, -1}
	// Also reject values that would truncate to this actual process on 64-bit Go.
	if strconv.IntSize == 64 {
		tooLarge := uint64(1)<<32 | uint64(os.Getpid())
		invalid = append(invalid, int(tooLarge))
	}
	for _, pid := range invalid {
		identity.PID = pid
		if alive, err := checkRecoveryProcess(identity); err != nil || alive {
			t.Fatalf("invalid PID matched: alive=%v err=%v", alive, err)
		}
		if _, err := captureRecoveryProcess(pid); err == nil {
			t.Fatal("invalid PID capture succeeded")
		}
		if err := stopRecoveryProcess(identity); err == nil {
			t.Fatal("invalid PID stop succeeded")
		}
	}
}

func TestRecoveryProcessQueryErrorsFailClosed(t *testing.T) {
	// Restrict only our query handle, not the process ACL or registry/system state.
	handle, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(os.Getpid()))
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(handle)
	identity, alive, err := readRecoveryProcessIdentity(handle, os.Getpid())
	if !errors.Is(err, windows.ERROR_ACCESS_DENIED) || alive || identity != (recoveryProcessIdentity{}) {
		t.Fatalf("query denied did not fail closed: alive=%v err=%v", alive, err)
	}
	if _, err := recoveryProcessImage(handle); !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		t.Fatalf("image query failure was hidden: %v", err)
	}
	// InvalidHandle (-1) is also the valid current-process pseudo handle.
	if alive, err := recoveryProcessAlive(windows.Handle(0)); err == nil || alive {
		t.Fatalf("invalid handle wait did not fail closed: alive=%v err=%v", alive, err)
	}
}

func TestRecoveryProcessExplicitStopWaitsAndIsIdempotent(t *testing.T) {
	h := startRecoveryProcessTestHelper(t, 0)
	// Retain a handle so this child PID cannot be reused during the assertions.
	handle := holdRecoveryProcessTestHandle(t, h.identity.PID)
	requireRecoveryProcessAlive(t, h.identity)
	if err := stopRecoveryProcess(h.identity); err != nil {
		t.Fatal(err)
	}
	if result, err := windows.WaitForSingleObject(handle, 0); err != nil || result != windows.WAIT_OBJECT_0 {
		t.Fatalf("stop returned before process exit: result=%#x err=%v", result, err)
	}
	waitRecoveryProcessTestHelper(t, h)
	if alive, err := checkRecoveryProcess(h.identity); err != nil || alive {
		t.Fatalf("stopped helper still matched: alive=%v err=%v", alive, err)
	}
	if err := stopRecoveryProcess(h.identity); err != nil {
		t.Fatalf("repeat explicit stop failed: %v", err)
	}
}

func TestRecoveryProcessExitedWithStillActiveExitCode(t *testing.T) {
	h := startRecoveryProcessTestHelper(t, 259)
	handle := holdRecoveryProcessTestHandle(t, h.identity.PID)
	if err := h.stdin.Close(); err != nil {
		t.Fatal(err)
	}
	waitRecoveryProcessTestHelper(t, h)
	var code uint32
	if err := windows.GetExitCodeProcess(handle, &code); err != nil || code != 259 {
		t.Fatalf("helper did not exit with sentinel code: code=%d err=%v", code, err)
	}
	if alive, err := checkRecoveryProcess(h.identity); err != nil || alive {
		t.Fatalf("exited process with exit code 259 matched as alive: alive=%v err=%v", alive, err)
	}
	if _, err := captureRecoveryProcess(h.identity.PID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("capture did not classify an exited process as missing: %v", err)
	}
	if err := stopRecoveryProcess(h.identity); err != nil {
		t.Fatalf("already-exited stop failed: %v", err)
	}
}
