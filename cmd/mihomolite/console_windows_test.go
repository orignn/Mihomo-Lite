//go:build windows

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestCloseToTraySettingsDefaults(t *testing.T) {
	for _, tc := range []struct {
		name, content string
		want          bool
	}{
		{"missing", "", true},
		{"legacy", "{\"listen\":\"127.0.0.1:17653\"}", true},
		{"disabled", "{\"closeToTray\":false}", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "launcher.json")
			if tc.content != "" {
				if err := os.WriteFile(path, []byte(tc.content), 0600); err != nil {
					t.Fatal(err)
				}
			}
			settings, err := LoadSettings(path)
			if err != nil {
				t.Fatal(err)
			}
			if settings.CloseToTray != tc.want {
				t.Fatalf("CloseToTray=%v, want %v", settings.CloseToTray, tc.want)
			}
		})
	}
}

func TestCreateShowEventOpensExistingEvent(t *testing.T) {
	first, err := createShowEvent()
	if err != nil {
		t.Fatal(err)
	}
	defer closeShowEvent(first)
	second, err := createShowEvent()
	if err != nil {
		t.Fatalf("opening the shared Show event must accept ERROR_ALREADY_EXISTS: %v", err)
	}
	defer closeShowEvent(second)
}

type consoleTestLauncher struct {
	cmd           *exec.Cmd
	done          chan struct{}
	waitErr       error
	url, dir, exe string
	client        *http.Client
}

func consoleEventually(t *testing.T, timeout time.Duration, what string, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func consoleWindowFor(pid int) uintptr {
	title := windows.StringToUTF16Ptr(consoleTitle(pid))
	hwnd, _, _ := user32.NewProc("FindWindowW").Call(0, uintptr(unsafe.Pointer(title)))
	return hwnd
}

func consoleTrayFor(pid int) uintptr {
	class := windows.StringToUTF16Ptr("SystrayClass")
	var hwnd uintptr
	for {
		hwnd, _, _ = user32.NewProc("FindWindowExW").Call(0, hwnd, uintptr(unsafe.Pointer(class)), 0)
		if hwnd == 0 {
			return 0
		}
		var owner uint32
		user32.NewProc("GetWindowThreadProcessId").Call(hwnd, uintptr(unsafe.Pointer(&owner)))
		if owner == uint32(pid) {
			return hwnd
		}
	}
}

func consoleChildren(parentPID int, exe string) []int {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil
	}
	defer windows.CloseHandle(snapshot)
	entry := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	var pids []int
	for err := windows.Process32First(snapshot, &entry); err == nil; err = windows.Process32Next(snapshot, &entry) {
		if entry.ParentProcessID == uint32(parentPID) && strings.EqualFold(windows.UTF16ToString(entry.ExeFile[:]), filepath.Base(exe)) {
			pids = append(pids, int(entry.ProcessID))
		}
	}
	return pids
}

func consolePost(t *testing.T, hwnd uintptr, msg uint32, value uintptr) {
	t.Helper()
	if hwnd == 0 {
		t.Fatal("missing target window")
	}
	if result, _, err := user32.NewProc("PostMessageW").Call(hwnd, uintptr(msg), value, 0); result == 0 {
		t.Fatal(err)
	}
}

func startConsoleTestLauncher(t *testing.T, silent bool) *consoleTestLauncher {
	t.Helper()
	if os.Getenv("MIHOMOLITE_TEST_CONSOLE") != "1" {
		t.Skip("set MIHOMOLITE_TEST_CONSOLE=1 and MIHOMOLITE_TEST_LAUNCHER to test real Windows windows")
	}
	exe := os.Getenv("MIHOMOLITE_TEST_LAUNCHER")
	if exe == "" {
		t.Fatal("MIHOMOLITE_TEST_LAUNCHER is required")
	}
	if !filepath.IsAbs(exe) {
		t.Fatal("test launcher path must be absolute")
	}
	mutex, err := windows.OpenMutex(windows.SYNCHRONIZE, false, instanceObjectName("SingleInstance"))
	if err == nil {
		windows.CloseHandle(mutex)
		t.Skip("an existing Mihomo Lite instance owns the tray; will not disturb it")
	}
	dir := t.TempDir()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	listen := ln.Addr().String()
	ln.Close()
	settings, _ := json.Marshal(Settings{Listen: listen, CloseToTray: true})
	if err := os.WriteFile(filepath.Join(dir, "launcher.json"), settings, 0600); err != nil {
		t.Fatal(err)
	}
	core := os.Getenv("MIHOMOLITE_TEST_CORE")
	if core != "" && os.Getenv("MIHOMOLITE_TEST_SYSTEM_PROXY") == "1" {
		for _, path := range []string{"runtime", "data/ui"} {
			if err := os.MkdirAll(filepath.Join(dir, path), 0755); err != nil {
				t.Fatal(err)
			}
		}
		binary, err := os.ReadFile(core)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "runtime", coreExecutable), binary, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "data/ui/index.html"), []byte("test panel"), 0600); err != nil {
			t.Fatal(err)
		}
		source := "mode: rule\nlog-level: info\nproxies:\n  - name: local-direct\n    type: direct\nrules:\n  - MATCH,DIRECT\ndns:\n  enable: false\n"
		if err := os.WriteFile(filepath.Join(dir, "data/source.yaml"), []byte(source), 0600); err != nil {
			t.Fatal(err)
		}
	}
	args := []string{"--dir", dir}
	if silent {
		args = append(args, "--silent")
	}
	cmd := exec.Command(exe, args...)
	cmd.Env = append(os.Environ(), "MIHOMOLITE_NO_BROWSER=1", "MIHOMOLITE_NO_PAUSE=1")
	cmd.SysProcAttr = hiddenProcessAttrs()
	f := &consoleTestLauncher{cmd: cmd, done: make(chan struct{}), url: "http://" + listen, dir: dir, exe: exe, client: &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: time.Second}}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { f.waitErr = cmd.Wait(); close(f.done) }()
	t.Cleanup(func() {
		select {
		case <-f.done:
			return
		default:
		}
		if hwnd := consoleTrayFor(cmd.Process.Pid); hwnd != 0 {
			user32.NewProc("PostMessageW").Call(hwnd, 0x111, uintptr(trayCommandQuit), 0)
		}
		select {
		case <-f.done:
		case <-time.After(10 * time.Second):
			cmd.Process.Kill()
			<-f.done
		}
	})
	consoleEventually(t, 10*time.Second, "launcher HTTP and tray", func() bool {
		return f.state().Version == AppVersion && consoleTrayFor(cmd.Process.Pid) != 0
	})
	return f
}

func (f *consoleTestLauncher) state() State {
	resp, err := f.client.Get(f.url + "/api/state")
	if err != nil {
		return State{}
	}
	defer resp.Body.Close()
	var state State
	json.NewDecoder(resp.Body).Decode(&state)
	return state
}

func (f *consoleTestLauncher) request(t *testing.T, method, path, body string) {
	t.Helper()
	req, err := http.NewRequest(method, f.url+path, bytes.NewBufferString(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Mihomo-Lite", "1")
	resp, err := f.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		content, _ := io.ReadAll(resp.Body)
		t.Fatalf("%s %s: %d %s", method, path, resp.StatusCode, content)
	}
}

func (f *consoleTestLauncher) window(t *testing.T) uintptr {
	t.Helper()
	var hwnd uintptr
	consoleEventually(t, 10*time.Second, "visible helper console", func() bool {
		hwnd = consoleWindowFor(f.cmd.Process.Pid)
		return hwnd != 0 && len(consoleChildren(f.cmd.Process.Pid, f.exe)) == 1
	})
	var class [256]uint16
	user32.NewProc("GetClassNameW").Call(hwnd, uintptr(unsafe.Pointer(&class[0])), uintptr(len(class)))
	if windows.UTF16ToString(class[:]) != "ConsoleWindowClass" {
		t.Skip("SC_CLOSE automation requires a dedicated classic conhost window; do not close a shared Windows Terminal window")
	}
	return hwnd
}

func (f *consoleTestLauncher) waitExit(t *testing.T) {
	t.Helper()
	select {
	case <-f.done:
		if f.waitErr != nil {
			t.Fatal(f.waitErr)
		}
	case <-time.After(12 * time.Second):
		t.Fatal("launcher did not exit gracefully")
	}
	consoleEventually(t, 5*time.Second, "helper process cleanup", func() bool { return len(consoleChildren(f.cmd.Process.Pid, f.exe)) == 0 })
}

func consoleProbe(t *testing.T, helperPID int, sendCtrlC bool) []uint32 {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(t.TempDir(), "console-processes.json")
	cmd := exec.Command(exe, "-test.run=^TestConsoleControlProbe$")
	cmd.SysProcAttr = hiddenProcessAttrs()
	cmd.Env = append(os.Environ(), "MIHOMOLITE_CONSOLE_PROBE_PID="+strconv.Itoa(helperPID), "MIHOMOLITE_CONSOLE_PROBE_OUTPUT="+output, "MIHOMOLITE_CONSOLE_PROBE_CTRL_C="+strconv.FormatBool(sendCtrlC))
	if result, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("console probe failed: %v %s", err, result)
	}
	content, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	var pids []uint32
	if err := json.Unmarshal(content, &pids); err != nil {
		t.Fatal(err)
	}
	return pids
}

// Executed only in an isolated subprocess; never attach the test runner itself.
func TestConsoleControlProbe(t *testing.T) {
	value := os.Getenv("MIHOMOLITE_CONSOLE_PROBE_PID")
	if value == "" {
		t.Skip("console probe subprocess only")
	}
	pid, err := strconv.Atoi(value)
	if err != nil {
		t.Fatal(err)
	}
	detachConsole()
	if result, _, err := kernel32.NewProc("AttachConsole").Call(uintptr(pid)); result == 0 {
		t.Fatal(err)
	}
	defer detachConsole()
	kernel32.NewProc("SetConsoleCtrlHandler").Call(0, 1)
	var ids [64]uint32
	n, _, err := kernel32.NewProc("GetConsoleProcessList").Call(uintptr(unsafe.Pointer(&ids[0])), uintptr(len(ids)))
	if n == 0 || n > uintptr(len(ids)) {
		t.Fatal(err)
	}
	content, _ := json.Marshal(ids[:n])
	if err := os.WriteFile(os.Getenv("MIHOMOLITE_CONSOLE_PROBE_OUTPUT"), content, 0600); err != nil {
		t.Fatal(err)
	}
	if textPath := os.Getenv("MIHOMOLITE_CONSOLE_PROBE_TEXT_OUTPUT"); textPath != "" {
		out, err := os.OpenFile("CONOUT$", os.O_RDWR, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer out.Close()
		var chars [32768]uint16
		var count uint32
		if result, _, err := kernel32.NewProc("ReadConsoleOutputCharacterW").Call(out.Fd(), uintptr(unsafe.Pointer(&chars[0])), uintptr(len(chars)), 0, uintptr(unsafe.Pointer(&count))); result == 0 {
			t.Fatal(err)
		}
		if err := os.WriteFile(textPath, []byte(windows.UTF16ToString(chars[:count])), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if os.Getenv("MIHOMOLITE_CONSOLE_PROBE_CTRL_C") == "true" {
		if result, _, err := kernel32.NewProc("GenerateConsoleCtrlEvent").Call(0, 0); result == 0 {
			t.Fatal(err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func TestConsoleCloseKeepsTrayAndCore(t *testing.T) {
	f := startConsoleTestLauncher(t, false)
	hwnd := f.window(t)
	helper := consoleChildren(f.cmd.Process.Pid, f.exe)[0]
	var corePID int
	var proxyBefore *proxySnapshot
	var throughProxy func()
	if os.Getenv("MIHOMOLITE_TEST_CORE") != "" && os.Getenv("MIHOMOLITE_TEST_SYSTEM_PROXY") == "1" {
		before, err := readProxySnapshot()
		if err != nil {
			t.Fatal(err)
		}
		proxyBefore = &before
		t.Cleanup(func() {
			defer func() {
				if err := restoreProxy(before); err != nil {
					t.Error(err)
				}
			}()
			if processExists(f.cmd.Process.Pid) {
				f.request(t, http.MethodPost, "/api/disconnect", "{}")
				consoleEventually(t, 10*time.Second, "core disconnect", func() bool { return !f.state().Running && !f.state().Busy })
			}
		})
		f.request(t, http.MethodPost, "/api/connect", "{}")
		consoleEventually(t, 20*time.Second, "connected core and system proxy", func() bool { s := f.state(); return s.Running && s.ProxyEnabled && !s.Busy })
		s := f.state()
		corePID = s.PID
		coreHandle, err := windows.OpenProcess(windows.PROCESS_TERMINATE|windows.SYNCHRONIZE, false, uint32(corePID))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if result, err := windows.WaitForSingleObject(coreHandle, 0); err == nil && result == uint32(windows.WAIT_TIMEOUT) {
				windows.TerminateProcess(coreHandle, 0)
			}
			windows.CloseHandle(coreHandle)
		})
		endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "still-alive") }))
		defer endpoint.Close()
		proxyURL, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", s.MixedPort))
		client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}, Timeout: 3 * time.Second}
		throughProxy = func() {
			resp, err := client.Get(endpoint.URL)
			if err != nil {
				t.Fatal(err)
			}
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if string(body) != "still-alive" {
				t.Fatalf("proxy request failed: %s", body)
			}
		}
		throughProxy()
	}
	for _, pid := range consoleProbe(t, helper, false) {
		if int(pid) == f.cmd.Process.Pid || (corePID != 0 && int(pid) == corePID) {
			t.Fatalf("launcher/core is attached to helper console: %d", pid)
		}
	}
	consolePost(t, hwnd, 0x112, 0xf060) // The same SC_CLOSE command as the title-bar X.
	consoleEventually(t, 5*time.Second, "closed helper", func() bool { return !processExists(helper) })
	time.Sleep(16 * time.Second) // Exceed both the console close timeout and old shutdown window.
	if !processExists(f.cmd.Process.Pid) || consoleTrayFor(f.cmd.Process.Pid) == 0 || f.state().Version != AppVersion {
		t.Fatal("closing console terminated launcher or tray")
	}
	if corePID != 0 {
		s := f.state()
		if !s.Running || s.PID != corePID || !s.ProxyEnabled {
			t.Fatalf("core/proxy changed after closing console: %+v", s)
		}
		throughProxy()
	}
	consolePost(t, consoleTrayFor(f.cmd.Process.Pid), 0x111, uintptr(trayCommandShow))
	hwnd = f.window(t)
	f.request(t, http.MethodPut, "/api/settings", "{\"closeToTray\":false}")
	consolePost(t, hwnd, 0x112, 0xf060)
	f.waitExit(t)
	if corePID != 0 && processExists(corePID) {
		t.Fatal("core survived graceful launcher exit")
	}
	if proxyBefore != nil {
		after, err := readProxySnapshot()
		if err != nil || after != *proxyBefore {
			t.Fatalf("system proxy was not restored before test cleanup: before=%+v after=%+v err=%v", *proxyBefore, after, err)
		}
	}
	t.Logf("SC_CLOSE preserved launcher PID %d, core PID %d and tray for 16s; disabling closeToTray exited cleanly", f.cmd.Process.Pid, corePID)
}

func TestConsoleSilentShowReopenAndTrayQuit(t *testing.T) {
	f := startConsoleTestLauncher(t, true)
	if len(consoleChildren(f.cmd.Process.Pid, f.exe)) != 0 || consoleWindowFor(f.cmd.Process.Pid) != 0 {
		t.Fatal("silent launch opened a console")
	}
	second := exec.Command(f.exe, "--dir", f.dir)
	second.Env = append(os.Environ(), "MIHOMOLITE_NO_BROWSER=1", "MIHOMOLITE_NO_PAUSE=1")
	second.SysProcAttr = hiddenProcessAttrs()
	if err := second.Run(); err != nil {
		t.Fatal(err)
	}
	f.window(t)
	for i := 0; i < 20; i++ {
		helper := consoleChildren(f.cmd.Process.Pid, f.exe)[0]
		consolePost(t, consoleWindowFor(f.cmd.Process.Pid), 0x112, 0xf060)
		consoleEventually(t, 5*time.Second, "viewer close", func() bool { return !processExists(helper) })
		consolePost(t, consoleTrayFor(f.cmd.Process.Pid), 0x111, uintptr(trayCommandShow))
		f.window(t)
	}
	consolePost(t, consoleTrayFor(f.cmd.Process.Pid), 0x111, uintptr(trayCommandQuit))
	f.waitExit(t)
	t.Log("silent launch, duplicate show, repeated close/reopen and tray quit passed")
}

func TestRememberedSilentStart(t *testing.T) {
	if os.Getenv("MIHOMOLITE_TEST_CONSOLE") != "1" {
		t.Skip("isolated real-window test only")
	}
	exe := os.Getenv("MIHOMOLITE_TEST_LAUNCHER")
	if exe == "" {
		t.Fatal("isolated launcher required")
	}
	newDir := func(silent bool) string {
		dir := t.TempDir()
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		listen := ln.Addr().String()
		ln.Close()
		settings, _ := json.Marshal(Settings{Listen: listen, CloseToTray: true, SilentStart: silent})
		if err := os.WriteFile(filepath.Join(dir, "launcher.json"), settings, 0600); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	dir := newDir(true)
	cmd := exec.Command(exe, "--dir", dir)
	cmd.SysProcAttr = hiddenProcessAttrs()
	cmd.Env = append(os.Environ(), "MIHOMOLITE_NO_PAUSE=1")
	f := &consoleTestLauncher{cmd: cmd, done: make(chan struct{}), url: "http://" + mustListen(t, dir), dir: dir, exe: exe, client: &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: time.Second}}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { f.waitErr = cmd.Wait(); close(f.done) }()
	consoleEventually(t, 10*time.Second, "remembered silent launcher", func() bool { return f.state().Version == AppVersion && consoleTrayFor(cmd.Process.Pid) != 0 })
	if len(consoleChildren(cmd.Process.Pid, exe)) != 0 || consoleWindowFor(cmd.Process.Pid) != 0 {
		t.Fatal("remembered silent setting opened a console")
	}
	consolePost(t, consoleTrayFor(cmd.Process.Pid), 0x111, uintptr(trayCommandQuit))
	f.waitExit(t)
	t.Log("ordinary launch without --silent obeyed remembered silentStart=true")
}

func mustListen(t *testing.T, dir string) string {
	t.Helper()
	saved, err := LoadSettings(filepath.Join(dir, "launcher.json"))
	if err != nil {
		t.Fatal(err)
	}
	return saved.Listen
}

func TestConsoleCtrlCStopsLauncher(t *testing.T) {
	f := startConsoleTestLauncher(t, false)
	f.window(t)
	helper := consoleChildren(f.cmd.Process.Pid, f.exe)[0]
	consoleProbe(t, helper, true)
	f.waitExit(t)
	t.Log("Ctrl+C in helper requested graceful launcher shutdown")
}
