//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"sync"

	"golang.org/x/sys/windows"
)

// recoveryProcessIdentity binds a PID to one process instance, not just a name.
// Created is the raw Windows creation FILETIME, without Unix-time conversion.
type recoveryProcessIdentity struct {
	PID     int    `json:"pid"`
	Created uint64 `json:"created"`
	Image   string `json:"image"`
}

const recoveryProcessStopWaitMilliseconds = 5000

// acquireProxyRecoveryGuard excludes concurrent recovery owners for this Windows
// account even across desktop sessions. Hold the returned release until exit.
// The name contains only a fixed product prefix and TokenUser SID, never a path.
func acquireProxyRecoveryGuard() (release func(), err error) {
	name, err := proxyRecoveryGuardName()
	if err != nil {
		return nil, err
	}
	return acquireNamedProxyRecoveryGuard(name)
}

func proxyRecoveryGuardName() (string, error) {
	var token windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY, &token); err != nil {
		return "", fmt.Errorf("open proxy recovery account token: %w", err)
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil {
		return "", fmt.Errorf("query proxy recovery account SID: %w", err)
	}
	if user.User.Sid == nil || user.User.Sid.String() == "" {
		return "", errors.New("proxy recovery account SID is missing")
	}
	return `Global\MihomoLite.ProxyRecovery.` + user.User.Sid.String(), nil
}

func acquireNamedProxyRecoveryGuard(name string) (func(), error) {
	mutexName, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return nil, fmt.Errorf("encode proxy recovery guard name: %w", err)
	}
	ready := make(chan error)
	releaseRequested := make(chan struct{})
	released := make(chan struct{})
	// Win32 mutex ownership is per OS thread. A dedicated pinned goroutine does
	// both Wait and Release; callers may release from any goroutine safely.
	go func() {
		defer close(released)
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		handle, err := windows.CreateMutexEx(nil, mutexName, 0, windows.SYNCHRONIZE|windows.MUTEX_MODIFY_STATE)
		if err != nil && !errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
			ready <- fmt.Errorf("create proxy recovery guard: %w", err)
			return
		}
		defer windows.CloseHandle(handle)
		result, err := windows.WaitForSingleObject(handle, 0)
		if err != nil {
			ready <- fmt.Errorf("acquire proxy recovery guard: %w", err)
			return
		}
		if result != windows.WAIT_OBJECT_0 && result != windows.WAIT_ABANDONED {
			if result == uint32(windows.WAIT_TIMEOUT) {
				ready <- errors.New("another process owns proxy recovery; refusing concurrent recovery")
			} else {
				ready <- fmt.Errorf("unexpected proxy recovery guard wait result %#x", result)
			}
			return
		}
		ready <- nil
		<-releaseRequested
		_ = windows.ReleaseMutex(handle) // Always on the acquiring OS thread.
	}()
	if err := <-ready; err != nil {
		<-released
		return nil, err
	}
	var once sync.Once
	return func() {
		once.Do(func() { close(releaseRequested) })
		<-released
	}, nil
}

// captureRecoveryProcess captures only a currently live process. It does not
// terminate anything or perform recovery automatically (OPT-001).
func captureRecoveryProcess(pid int) (recoveryProcessIdentity, error) {
	if !validRecoveryProcessPID(pid) {
		return recoveryProcessIdentity{}, fmt.Errorf("invalid recovery process PID %d", pid)
	}
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, uint32(pid))
	if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		return recoveryProcessIdentity{}, fmt.Errorf("recovery process %d does not exist: %w: %w", pid, os.ErrNotExist, err)
	}
	if err != nil {
		return recoveryProcessIdentity{}, fmt.Errorf("open recovery process %d: %w", pid, err)
	}
	defer windows.CloseHandle(handle)
	identity, alive, err := readRecoveryProcessIdentity(handle, pid)
	if err != nil {
		return recoveryProcessIdentity{}, err
	}
	if !alive {
		return recoveryProcessIdentity{}, fmt.Errorf("recovery process %d is not alive: %w", pid, os.ErrNotExist)
	}
	return identity, nil
}

// checkRecoveryProcess verifies live identity by handle. On query failure, only
// independent kernel PID/creation evidence may disprove an obsolete instance.
func checkRecoveryProcess(identity recoveryProcessIdentity) (bool, error) {
	return checkRecoveryProcessWithSnapshot(identity, checkRecoveryProcessByHandle, readRecoveryProcessSnapshot)
}

// Access denied and query failures alone remain unknown, never absent/matched.
func checkRecoveryProcessByHandle(identity recoveryProcessIdentity) (bool, error) {
	if !validRecoveryProcessIdentity(identity) {
		return false, nil
	}
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, uint32(identity.PID))
	if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		return false, nil // A valid nonzero PID no longer exists.
	}
	if err != nil {
		return false, fmt.Errorf("open recovery process %d: %w", identity.PID, err)
	}
	defer windows.CloseHandle(handle)
	actual, alive, err := readRecoveryProcessIdentity(handle, identity.PID)
	if err != nil {
		return false, err
	}
	return alive && actual == identity, nil
}

// stopRecoveryProcess requires a fully captured identity, never an unverified
// legacy PID. Cross-process recovery must call it only for an explicit user
// diagnostic action, not automatic recovery/startup. An existing current-session
// owner may also use its captured identity for its authorized normal Stop. One
// handle verifies, terminates, and waits, so PID reuse cannot switch the target.
// An already absent/exited process is a successful no-op; a mismatch is an error.
func stopRecoveryProcess(identity recoveryProcessIdentity) error {
	if !validRecoveryProcessIdentity(identity) {
		return fmt.Errorf("invalid recovery process identity for PID %d", identity.PID)
	}
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.PROCESS_TERMINATE|windows.SYNCHRONIZE, false, uint32(identity.PID))
	if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open recovery process %d for explicit stop: %w", identity.PID, err)
	}
	defer windows.CloseHandle(handle)
	actual, alive, err := readRecoveryProcessIdentity(handle, identity.PID)
	if err != nil {
		return err
	}
	if !alive {
		return nil
	}
	if actual != identity {
		return fmt.Errorf("recovery process %d identity changed; refusing to terminate", identity.PID)
	}
	if err := windows.TerminateProcess(handle, 1); err != nil {
		return fmt.Errorf("terminate verified recovery process %d: %w", identity.PID, err)
	}
	result, err := windows.WaitForSingleObject(handle, recoveryProcessStopWaitMilliseconds)
	if err != nil {
		return fmt.Errorf("wait for recovery process %d to stop: %w", identity.PID, err)
	}
	if result != windows.WAIT_OBJECT_0 {
		return fmt.Errorf("wait for recovery process %d to stop returned %#x", identity.PID, result)
	}
	return nil
}

func validRecoveryProcessPID(pid int) bool {
	return pid > 0 && uint64(pid) <= uint64(^uint32(0))
}

func validRecoveryProcessIdentity(identity recoveryProcessIdentity) bool {
	return validRecoveryProcessPID(identity.PID) && identity.Created != 0 && identity.Image != ""
}

func readRecoveryProcessIdentity(handle windows.Handle, pid int) (recoveryProcessIdentity, bool, error) {
	alive, err := recoveryProcessAlive(handle)
	if err != nil || !alive {
		return recoveryProcessIdentity{}, false, err
	}
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(handle, &created, &exited, &kernel, &user); err != nil {
		return recoveryProcessIdentity{}, false, fmt.Errorf("query creation time of recovery process %d: %w", pid, err)
	}
	image, err := recoveryProcessImage(handle)
	if err != nil {
		return recoveryProcessIdentity{}, false, fmt.Errorf("query image of recovery process %d: %w", pid, err)
	}
	// Recheck after the queries: a process may exit while its handle remains valid.
	alive, err = recoveryProcessAlive(handle)
	if err != nil || !alive {
		return recoveryProcessIdentity{}, false, err
	}
	identity := recoveryProcessIdentity{
		PID:     pid,
		Created: uint64(created.HighDateTime)<<32 | uint64(created.LowDateTime),
		Image:   image,
	}
	if !validRecoveryProcessIdentity(identity) {
		return recoveryProcessIdentity{}, false, fmt.Errorf("incomplete identity for recovery process %d", pid)
	}
	return identity, true, nil
}

func recoveryProcessAlive(handle windows.Handle) (bool, error) {
	result, err := windows.WaitForSingleObject(handle, 0)
	if err != nil {
		return false, fmt.Errorf("query recovery process liveness: %w", err)
	}
	switch result {
	case windows.WAIT_OBJECT_0:
		return false, nil
	case uint32(windows.WAIT_TIMEOUT):
		return true, nil
	default:
		return false, fmt.Errorf("unexpected recovery process wait result %#x", result)
	}
}

func recoveryProcessImage(handle windows.Handle) (string, error) {
	// QueryFullProcessImageName with flags 0 returns the full Win32 executable
	// path. Grow beyond MAX_PATH; never compare a truncated path or a basename.
	const maximumCharacters = 32768
	for capacity := 260; ; capacity *= 2 {
		if capacity > maximumCharacters {
			capacity = maximumCharacters
		}
		buffer := make([]uint16, capacity)
		size := uint32(len(buffer))
		err := windows.QueryFullProcessImageName(handle, 0, &buffer[0], &size)
		if err == nil {
			if size == 0 || size >= uint32(len(buffer)) {
				return "", errors.New("empty or truncated recovery process image")
			}
			return windows.UTF16ToString(buffer[:size]), nil
		}
		if !errors.Is(err, windows.ERROR_INSUFFICIENT_BUFFER) || capacity == maximumCharacters {
			return "", err
		}
	}
}
