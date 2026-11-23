//go:build windows && isolatedtest

package main

import (
	"debug/buildinfo"
	"debug/pe"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const p0p1BrowserMarker = "MIHOMOLITE_P0P1_BROWSER_RECORDER"
const p0p1BrowserOutput = "MIHOMOLITE_P0P1_BROWSER_OUTPUT"

var p0p1NamespacePattern = regexp.MustCompile("^P0P1[-A-Za-z0-9_.]+$")
var p0p1TargetNamespacePattern = regexp.MustCompile("(?:^|\\s)-X(?:=|\\s+)[\"']?main\\.instanceNamespace=([-A-Za-z0-9_.]+)[\"']?(?:\\s|$)")

type p0p1BrowserCall struct {
	Args       []string
	PID        int
	ParentPID  int
	Executable string
}

// Only the deliberately copied test runner, invoked as rundll32.exe with the
// exact namespace marker, acts as a recorder. A normal runner never exits here.
func init() {
	exe, err := os.Executable()
	// exec.Command("rundll32", ...) preserves Args[0] without .exe. Gate on
	// the actual loaded executable image, never the caller-supplied argv[0].
	if err != nil || !strings.EqualFold(filepath.Base(exe), "rundll32.exe") ||
		!p0p1NamespacePattern.MatchString(instanceNamespace) ||
		os.Getenv(p0p1BrowserMarker) != instanceNamespace {
		return
	}
	output := os.Getenv(p0p1BrowserOutput)
	if !filepath.IsAbs(output) || filepath.Base(output) != "browser-calls.jsonl" ||
		!strings.EqualFold(filepath.Clean(filepath.Dir(output)), filepath.Clean(filepath.Dir(exe))) {
		os.Exit(2)
	}
	content, err := json.Marshal(p0p1BrowserCall{Args: os.Args[1:], PID: os.Getpid(), ParentPID: os.Getppid(), Executable: exe})
	if err != nil {
		os.Exit(2)
	}
	file, err := os.OpenFile(output, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		os.Exit(2)
	}
	_, writeErr := file.Write(append(content, '\n'))
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		os.Exit(2)
	}
	os.Exit(0)
}

func p0p1BuildSetting(info *debug.BuildInfo, key string) string {
	for _, setting := range info.Settings {
		if setting.Key == key {
			return setting.Value
		}
	}
	return ""
}

func p0p1HasIsolatedTag(info *debug.BuildInfo) bool {
	for _, tag := range strings.FieldsFunc(p0p1BuildSetting(info, "-tags"), func(r rune) bool { return r == ',' || r == ' ' || r == '\t' }) {
		if tag == "isolatedtest" {
			return true
		}
	}
	return false
}

// Positive binary evidence, not the mere existence of a source helper, gates
// every fixture and every private HKCU change in this file.
func p0p1RequireIsolatedLauncher(t *testing.T) string {
	t.Helper()
	if os.Getenv("MIHOMOLITE_TEST_CONSOLE") != "1" {
		t.Skip("opt in with MIHOMOLITE_TEST_CONSOLE=1 and an isolated MIHOMOLITE_TEST_LAUNCHER")
	}
	if !p0p1NamespacePattern.MatchString(instanceNamespace) {
		t.Fatalf("runner needs a nonempty, path-safe P0P1 instance namespace, got %q", instanceNamespace)
	}
	base := "Software\\MihomoLite\\Tests\\" + instanceNamespace
	if autoRunKey != base+"\\Run" || isolatedProxyRegistryPath() != base+"\\Proxy" {
		t.Fatal("runner registry backends are not confined to its private namespace")
	}
	runner, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	runnerInfo, err := buildinfo.ReadFile(runner)
	if err != nil || !p0p1HasIsolatedTag(runnerInfo) {
		t.Fatalf("runner lacks positive isolatedtest build metadata: %v", err)
	}
	exe := os.Getenv("MIHOMOLITE_TEST_LAUNCHER")
	if !filepath.IsAbs(exe) {
		t.Fatal("MIHOMOLITE_TEST_LAUNCHER must be an absolute path")
	}
	info, err := buildinfo.ReadFile(exe)
	if err != nil || !p0p1HasIsolatedTag(info) {
		t.Fatalf("target lacks positive isolatedtest build metadata: %v", err)
	}
	namespaces := p0p1TargetNamespacePattern.FindAllStringSubmatch(p0p1BuildSetting(info, "-ldflags"), -1)
	if len(namespaces) != 1 || namespaces[0][1] != instanceNamespace {
		t.Fatalf("target namespace must unambiguously match runner %q; ldflags=%q", instanceNamespace, p0p1BuildSetting(info, "-ldflags"))
	}
	image, err := pe.Open(exe)
	if err != nil {
		t.Fatal(err)
	}
	defer image.Close()
	var subsystem uint16
	switch header := image.OptionalHeader.(type) {
	case *pe.OptionalHeader32:
		subsystem = header.Subsystem
	case *pe.OptionalHeader64:
		subsystem = header.Subsystem
	default:
		t.Fatal("target is not a PE executable with a supported optional header")
	}
	if subsystem != 2 {
		t.Fatalf("target must be Windows GUI PE subsystem 2, got %d", subsystem)
	}
	return exe
}

func p0p1StartConsoleTestLauncher(t *testing.T, silent bool) *consoleTestLauncher {
	t.Helper()
	p0p1RequireIsolatedLauncher(t)
	// This fixture intentionally has no core. Parent-owned core tests use their
	// own fixture; inherited core settings must not weaken disabled-menu checks.
	t.Setenv("MIHOMOLITE_TEST_CORE", "")
	t.Setenv("MIHOMOLITE_TEST_SYSTEM_PROXY", "")
	return startConsoleTestLauncher(t, silent)
}

func p0p1WindowPID(hwnd uintptr) uint32 {
	var pid uint32
	user32.NewProc("GetWindowThreadProcessId").Call(hwnd, uintptr(unsafe.Pointer(&pid)))
	return pid
}

func p0p1TrayWindow(t *testing.T, f *consoleTestLauncher) uintptr {
	t.Helper()
	select {
	case <-f.done:
		t.Fatal("owned fixture already exited")
	default:
	}
	hwnd := consoleTrayFor(f.cmd.Process.Pid)
	if hwnd == 0 || p0p1WindowPID(hwnd) != uint32(f.cmd.Process.Pid) {
		t.Fatal("cannot prove the SystrayClass window belongs to this fixture")
	}
	return hwnd
}

func p0p1PostOwned(t *testing.T, f *consoleTestLauncher, hwnd uintptr, message uint32, wparam, lparam uintptr) {
	t.Helper()
	if hwnd == 0 || p0p1WindowPID(hwnd) != uint32(f.cmd.Process.Pid) {
		t.Fatal("refusing to post a message to an unowned window")
	}
	if result, _, err := user32.NewProc("PostMessageW").Call(hwnd, uintptr(message), wparam, lparam); result == 0 {
		t.Fatalf("PostMessageW: %v", err)
	}
}

func p0p1Send(hwnd uintptr, message uint32) (uintptr, error) {
	var result uintptr
	ok, _, err := user32.NewProc("SendMessageTimeoutW").Call(hwnd, uintptr(message), 0, 0, 0x0003, 1000, uintptr(unsafe.Pointer(&result)))
	if ok == 0 {
		return 0, fmt.Errorf("SendMessageTimeoutW %#x: %v", message, err)
	}
	return result, nil
}

func p0p1PopupFor(pid int) uintptr {
	class := windows.StringToUTF16Ptr("#32768")
	var hwnd uintptr
	for {
		hwnd, _, _ = user32.NewProc("FindWindowExW").Call(0, hwnd, uintptr(unsafe.Pointer(class)), 0)
		if hwnd == 0 || p0p1WindowPID(hwnd) == uint32(pid) {
			return hwnd
		}
	}
}

type p0p1NativeMenu struct {
	popup, handle uintptr
}

type p0p1NativeMenuItem struct {
	Title string
	ID    uint32
	State uint32
}

func p0p1OpenMenu(t *testing.T, f *consoleTestLauncher) p0p1NativeMenu {
	t.Helper()
	tray := p0p1TrayWindow(t, f)
	p0p1PostOwned(t, f, tray, 0x0401, 100, 0x0205) // Native tray callback, WM_RBUTTONUP.
	var menu p0p1NativeMenu
	consoleEventually(t, 5*time.Second, "owned native tray popup and HMENU", func() bool {
		menu.popup = p0p1PopupFor(f.cmd.Process.Pid)
		if menu.popup == 0 {
			return false
		}
		var err error
		menu.handle, err = p0p1Send(menu.popup, 0x01e1) // MN_GETHMENU.
		return err == nil && menu.handle != 0
	})
	return menu
}

func p0p1CloseMenu(t *testing.T, f *consoleTestLauncher, menu p0p1NativeMenu) {
	t.Helper()
	if menu.popup == 0 || p0p1WindowPID(menu.popup) != uint32(f.cmd.Process.Pid) {
		return
	}
	// EndMenu is thread-local. WM_CANCELMODE is dispatched on this fixture's
	// menu-owning thread instead of cancelling a user's unrelated popup.
	_, _ = p0p1Send(menu.popup, 0x001f)
	if tray := consoleTrayFor(f.cmd.Process.Pid); tray != 0 && p0p1WindowPID(tray) == uint32(f.cmd.Process.Pid) {
		_, _ = p0p1Send(tray, 0x001f)
	}
	consoleEventually(t, 3*time.Second, "owned popup cancellation", func() bool { return p0p1PopupFor(f.cmd.Process.Pid) == 0 })
}

func p0p1MenuState(handle uintptr, position int) uint32 {
	state, _, _ := user32.NewProc("GetMenuState").Call(handle, uintptr(position), 0x0400)
	return uint32(state)
}

func p0p1MenuTitle(handle uintptr, position int) string {
	var text [256]uint16
	user32.NewProc("GetMenuStringW").Call(handle, uintptr(position), uintptr(unsafe.Pointer(&text[0])), uintptr(len(text)), 0x0400)
	return windows.UTF16ToString(text[:])
}

func p0p1AssertMenu(t *testing.T, f *consoleTestLauncher, autoStart, closeToTray bool) []p0p1NativeMenuItem {
	t.Helper()
	state := f.state()
	if state.Version != AppVersion {
		t.Fatal("native lifecycle menu assertions require a valid current HTTP state")
	}
	connectTitle := "连接"
	if state.Running {
		connectTitle = "断开连接"
	}
	connectDisabled := state.Busy || (!state.CanConnect && !state.CanDisconnect)
	restartDisabled := !state.Running || state.Busy
	menu := p0p1OpenMenu(t, f)
	defer p0p1CloseMenu(t, f, menu)
	consoleEventually(t, 5*time.Second, "menu checkbox and lifecycle synchronization", func() bool {
		auto, close, silent := p0p1MenuState(menu.handle, 6), p0p1MenuState(menu.handle, 7), p0p1MenuState(menu.handle, 8)
		connect, restart, panel := p0p1MenuState(menu.handle, 3), p0p1MenuState(menu.handle, 4), p0p1MenuState(menu.handle, 2)
		return auto != ^uint32(0) && close != ^uint32(0) && silent != ^uint32(0) && connect != ^uint32(0) && restart != ^uint32(0) && panel != ^uint32(0) &&
			(auto&8 != 0) == autoStart && (close&8 != 0) == closeToTray && (silent&8 != 0) == state.SilentStart &&
			(connect&3 != 0) == connectDisabled && (restart&3 != 0) == restartDisabled && (panel&3 != 0) == !state.HasPanelURL && p0p1MenuTitle(menu.handle, 3) == connectTitle
	})
	count, _, _ := user32.NewProc("GetMenuItemCount").Call(menu.handle)
	if int32(count) != 11 {
		t.Fatalf("native tray menu has %d items, want exactly 11 including separators", int32(count))
	}
	titles := []string{"显示控制台", "启动器页面", "Zashboard 面板", connectTitle, "重启连接", "", "开机自启", "关闭窗口最小化到托盘", "静默启动到托盘", "", "退出"}
	items := make([]p0p1NativeMenuItem, 11)
	for index := range items {
		var text [256]uint16
		user32.NewProc("GetMenuStringW").Call(menu.handle, uintptr(index), uintptr(unsafe.Pointer(&text[0])), uintptr(len(text)), 0x0400)
		id, _, _ := user32.NewProc("GetMenuItemID").Call(menu.handle, uintptr(index))
		items[index] = p0p1NativeMenuItem{Title: windows.UTF16ToString(text[:]), ID: uint32(id), State: p0p1MenuState(menu.handle, index)}
		item := items[index]
		if item.State == ^uint32(0) || item.Title != titles[index] {
			t.Fatalf("native item %d: %+v, want title %q", index, item, titles[index])
		}
		separator := index == 5 || index == 9
		if (item.State&0x0800 != 0) != separator {
			t.Fatalf("native item %d has wrong separator flag: %#x", index, item.State)
		}
		if separator {
			continue
		}
		if item.ID != uint32(index+1) {
			t.Fatalf("native item %d command ID=%d, want %d", index, item.ID, index+1)
		}
		wantDisabled := (index == 2 && !state.HasPanelURL) || (index == 3 && connectDisabled) || (index == 4 && restartDisabled)
		wantChecked := (index == 6 && autoStart) || (index == 7 && closeToTray) || (index == 8 && state.SilentStart)
		if (item.State&3 != 0) != wantDisabled || (item.State&8 != 0) != wantChecked {
			t.Fatalf("native item %d flags=%#x, want disabled=%v checked=%v", index, item.State, wantDisabled, wantChecked)
		}
	}
	t.Logf("native menu snapshot: %+v", items)
	return items
}

func p0p1Command(t *testing.T, f *consoleTestLauncher, id uint32) {
	t.Helper()
	p0p1PostOwned(t, f, p0p1TrayWindow(t, f), 0x0111, uintptr(id), 0)
}

type p0p1IconIdentifier struct {
	Size uint32
	Wnd  windows.Handle
	ID   uint32
	GUID windows.GUID
}

type p0p1Rect struct{ Left, Top, Right, Bottom int32 }

func p0p1OverflowVisibility() (known, visible bool) {
	for _, name := range []string{"NotifyIconOverflowWindow", "TopLevelWindowForOverflowXamlIsland"} {
		class := windows.StringToUTF16Ptr(name)
		var hwnd uintptr
		for {
			hwnd, _, _ = user32.NewProc("FindWindowExW").Call(0, hwnd, uintptr(unsafe.Pointer(class)), 0)
			if hwnd == 0 {
				break
			}
			known = true
			shown, _, _ := user32.NewProc("IsWindowVisible").Call(hwnd)
			visible = visible || shown != 0
		}
	}
	return
}

// HRESULT success includes S_FALSE (1), not only S_OK (0). With a closed
// overflow the shell can return a successful indicator/chevron rectangle; that
// is registration evidence, never a claim about the actual icon's pixels.
func p0p1ShellRectSucceeded(hresult uintptr, rect p0p1Rect) bool {
	return int32(uint32(hresult)) >= 0 && rect.Right > rect.Left && rect.Bottom > rect.Top
}

func TestP0P1ShellRectHRESULT(t *testing.T) {
	valid := p0p1Rect{Left: 2256, Top: 1400, Right: 2280, Bottom: 1440}
	for _, tc := range []struct {
		name   string
		result uintptr
		rect   p0p1Rect
		want   bool
	}{
		{"S_OK", 0, valid, true},
		{"S_FALSE_success", 1, valid, true},
		{"E_FAIL", 0x80004005, valid, false},
		{"empty_rectangle", 0, p0p1Rect{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := p0p1ShellRectSucceeded(tc.result, tc.rect); got != tc.want {
				t.Fatalf("HRESULT=%#x rect=%+v success=%v want=%v", tc.result, tc.rect, got, tc.want)
			}
		})
	}
}

func p0p1AssertIconRegistration(t *testing.T, f *consoleTestLauncher) {
	t.Helper()
	identifier := p0p1IconIdentifier{Wnd: windows.Handle(p0p1TrayWindow(t, f)), ID: 100}
	identifier.Size = uint32(unsafe.Sizeof(identifier))
	proc := windows.NewLazySystemDLL("shell32.dll").NewProc("Shell_NotifyIconGetRect")
	if err := proc.Find(); err != nil {
		t.Fatal(err)
	}
	var rect p0p1Rect
	var hresult uintptr
	var lastError error
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		hresult, _, lastError = proc.Call(uintptr(unsafe.Pointer(&identifier)), uintptr(unsafe.Pointer(&rect)))
		if p0p1ShellRectSucceeded(hresult, rect) {
			if uint32(hresult) == 1 {
				t.Log("S_FALSE is successful registration evidence; expand overflow for actual icon/tooltip screenshots, do not capture this indicator rectangle as the icon")
			}
			t.Logf("Shell_NotifyIconGetRect HRESULT(raw)=%#x DWORD=%#08x identifier.Size=%d actualSize=%d HWND=%#x ID=100 RECT=%+v lastError=%v (registration only; no pixel/tooltip claim)", hresult, uint32(hresult), identifier.Size, unsafe.Sizeof(identifier), identifier.Wnd, rect, lastError)
			return
		}
		select {
		case <-deadline.C:
			knownOverflow, visibleOverflow := p0p1OverflowVisibility()
			queryMayDependOnOverflow := uint32(hresult) == 0x80070490 || uint32(hresult) == 0x80004005 || uint32(hresult) == 0
			if !visibleOverflow && queryMayDependOnOverflow {
				t.Logf("WARNING: Shell_NotifyIconGetRect timeout with closed/unavailable overflow: HRESULT(raw)=%#x DWORD=%#08x identifier.Size=%d actualSize=%d HWND=%#x ID=%d RECT=%+v lastError=%v overflowClassKnown=%v. Registration is NOT verified by this helper; continue menu/settings/browser checks, and use the separate bounded visual probe for icon evidence.", hresult, uint32(hresult), identifier.Size, unsafe.Sizeof(identifier), identifier.Wnd, identifier.ID, rect, lastError, knownOverflow)
				return
			}
			t.Fatalf("Shell_NotifyIconGetRect timeout: HRESULT(raw)=%#x DWORD=%#08x identifier.Size=%d actualSize=%d HWND=%#x ID=%d RECT=%+v lastError=%v overflowClassKnown=%v overflowVisible=%v; no unsupported/absent inference", hresult, uint32(hresult), identifier.Size, unsafe.Sizeof(identifier), identifier.Wnd, identifier.ID, rect, lastError, knownOverflow, visibleOverflow)
		case <-ticker.C:
		}
	}
}

func p0p1AwaitSettings(t *testing.T, f *consoleTestLauncher, autoStart, closeToTray bool) {
	t.Helper()
	consoleEventually(t, 5*time.Second, "HTTP state and persisted runtime settings", func() bool {
		state := f.state()
		saved, err := LoadSettings(filepath.Join(f.dir, "launcher.json"))
		return err == nil && state.Version == AppVersion && state.AutoStart == autoStart && state.CloseToTray == closeToTray &&
			saved.AutoStart == autoStart && saved.CloseToTray == closeToTray
	})
}

func p0p1AssertRegistration(t *testing.T, exe string, enabled bool) {
	t.Helper()
	p0p1RequireIsolatedLauncher(t)
	entry, err := readAutoStartRegistration()
	if err != nil {
		t.Fatal(err)
	}
	if enabled {
		want := "\"" + exe + "\" --silent"
		if !entry.Exists || entry.Kind != registry.SZ || !strings.EqualFold(entry.Value, want) {
			t.Fatalf("private Run entry=%+v, want REG_SZ %q", entry, want)
		}
	} else if entry.Exists {
		t.Fatalf("disabled private Run entry still exists: %+v", entry)
	}
}

func TestP0P1IsolatedLauncherSafety(t *testing.T) {
	exe := p0p1RequireIsolatedLauncher(t)
	t.Logf("positive isolated binary evidence: target=%s namespace=%s private Run=%s", exe, instanceNamespace, autoRunKey)
}

func TestP0P1TrayRuntimeSettings(t *testing.T) {
	p0p1RequireIsolatedLauncher(t)
	before, err := readAutoStartRegistration()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := restoreAutoStartRegistration(before); err != nil {
			t.Errorf("restore original private Run registration: %v", err)
		}
	})
	if err := setAutoStart(false); err != nil {
		t.Fatal(err)
	}
	f := p0p1StartConsoleTestLauncher(t, true)
	p0p1AssertIconRegistration(t, f)
	items := p0p1AssertMenu(t, f, false, true)
	p0p1Command(t, f, items[0].ID)
	hwnd := f.window(t) // Unique fixture PID title, one owned helper, classic conhost only.
	helper := consoleChildren(f.cmd.Process.Pid, f.exe)[0]
	consolePost(t, hwnd, 0x0112, 0xf060)
	consoleEventually(t, 5*time.Second, "show then close while retaining launcher", func() bool { return !processExists(helper) })
	if f.state().Version != AppVersion || !processExists(f.cmd.Process.Pid) {
		t.Fatal("closeToTray=true closed the launcher instead of its disposable viewer")
	}

	f.request(t, http.MethodPut, "/api/settings", "{\"closeToTray\":false}")
	p0p1AwaitSettings(t, f, false, false)
	items = p0p1AssertMenu(t, f, false, false)
	p0p1Command(t, f, items[7].ID)
	p0p1AwaitSettings(t, f, false, true)
	items = p0p1AssertMenu(t, f, false, true)
	p0p1Command(t, f, items[7].ID)
	p0p1AwaitSettings(t, f, false, false)
	p0p1AssertMenu(t, f, false, false)

	f.request(t, http.MethodPut, "/api/settings", "{\"autoStart\":true}")
	p0p1AwaitSettings(t, f, true, false)
	p0p1AssertRegistration(t, f.exe, true)
	items = p0p1AssertMenu(t, f, true, false)
	p0p1Command(t, f, items[6].ID)
	p0p1AwaitSettings(t, f, false, false)
	p0p1AssertRegistration(t, f.exe, false)
	items = p0p1AssertMenu(t, f, false, false)
	p0p1Command(t, f, items[6].ID)
	p0p1AwaitSettings(t, f, true, false)
	p0p1AssertRegistration(t, f.exe, true)
	p0p1AssertMenu(t, f, true, false)
	f.request(t, http.MethodPut, "/api/settings", "{\"autoStart\":false}")
	p0p1AwaitSettings(t, f, false, false)
	p0p1AssertRegistration(t, f.exe, false)
	items = p0p1AssertMenu(t, f, false, false)
	f.request(t, http.MethodPut, "/api/settings", "{\"silentStart\":true}")
	p0p1AwaitSettings(t, f, false, false)
	consoleEventually(t, 5*time.Second, "web silentStart persisted and native tray checked", func() bool {
		saved, err := LoadSettings(filepath.Join(f.dir, "launcher.json"))
		menu := p0p1OpenMenu(t, f)
		defer p0p1CloseMenu(t, f, menu)
		return err == nil && saved.SilentStart && p0p1MenuState(menu.handle, 8)&8 != 0
	})
	p0p1Command(t, f, items[8].ID)
	consoleEventually(t, 5*time.Second, "tray silentStart persisted and unchecked", func() bool {
		saved, err := LoadSettings(filepath.Join(f.dir, "launcher.json"))
		menu := p0p1OpenMenu(t, f)
		defer p0p1CloseMenu(t, f, menu)
		return err == nil && !saved.SilentStart && !f.state().SilentStart && p0p1MenuState(menu.handle, 8)&8 == 0
	})

	p0p1Command(t, f, items[0].ID)
	hwnd = f.window(t)
	consolePost(t, hwnd, 0x0112, 0xf060)
	f.waitExit(t)
	t.Log("show, both HTTP/menu settings directions, persisted snapshots, private Run toggles and close=false exit verified")
}

func p0p1BrowserEnvironment(original []string, recorderDir, output string) []string {
	result := make([]string, 0, len(original)+4)
	for _, variable := range original {
		name, _, _ := strings.Cut(variable, "=")
		switch strings.ToUpper(name) {
		case "PATH", "MIHOMOLITE_NO_BROWSER", "MIHOMOLITE_NO_PAUSE", p0p1BrowserMarker, p0p1BrowserOutput:
			continue
		}
		result = append(result, variable)
	}
	return append(result, "PATH="+recorderDir+string(os.PathListSeparator)+os.Getenv("PATH"), "MIHOMOLITE_NO_PAUSE=1", p0p1BrowserMarker+"="+instanceNamespace, p0p1BrowserOutput+"="+output)
}

// Independent browser fixture: unlike startConsoleTestLauncher it deliberately
// omits MIHOMOLITE_NO_BROWSER and intercepts rundll32 through a private PATH.
func p0p1StartBrowserFixture(t *testing.T, silent bool) (*consoleTestLauncher, string) {
	t.Helper()
	exe := p0p1RequireIsolatedLauncher(t)
	mutex, err := windows.OpenMutex(windows.SYNCHRONIZE, false, instanceObjectName("SingleInstance"))
	if err == nil {
		windows.CloseHandle(mutex)
		t.Skip("this isolated namespace already has an instance; will not disturb it")
	}
	if !errors.Is(err, windows.ERROR_FILE_NOT_FOUND) {
		t.Fatalf("cannot positively rule out an existing isolated mutex: %v", err)
	}
	runner, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	recorderDir := t.TempDir()
	binary, err := os.ReadFile(runner)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(recorderDir, "rundll32.exe"), binary, 0700); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(recorderDir, "browser-calls.jsonl")
	dir := t.TempDir()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	listen := listener.Addr().String()
	listener.Close()
	settings, _ := json.Marshal(Settings{Listen: listen, CloseToTray: true})
	if err := os.WriteFile(filepath.Join(dir, "launcher.json"), settings, 0600); err != nil {
		t.Fatal(err)
	}
	args := []string{"--dir", dir}
	if silent {
		args = append(args, "--silent")
	}
	cmd := exec.Command(exe, args...)
	cmd.Dir = dir
	cmd.Env = p0p1BrowserEnvironment(os.Environ(), recorderDir, output)
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
		if hwnd := consoleTrayFor(cmd.Process.Pid); hwnd != 0 && p0p1WindowPID(hwnd) == uint32(cmd.Process.Pid) {
			user32.NewProc("PostMessageW").Call(hwnd, 0x0111, uintptr(trayCommandQuit), 0)
		}
		select {
		case <-f.done:
		case <-time.After(10 * time.Second):
			cmd.Process.Kill() // The exec.Cmd retains the handle to this owned fixture only.
			<-f.done
		}
	})
	consoleEventually(t, 10*time.Second, "isolated browser fixture HTTP and tray", func() bool {
		return f.state().Version == AppVersion && consoleTrayFor(cmd.Process.Pid) != 0
	})
	return f, output
}

func p0p1BrowserCalls(output string) ([]p0p1BrowserCall, error) {
	content, err := os.ReadFile(output)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var result []p0p1BrowserCall
	for _, line := range strings.Split(strings.TrimSpace(string(content)), "\n") {
		if line == "" {
			continue
		}
		var call p0p1BrowserCall
		if err := json.Unmarshal([]byte(line), &call); err != nil {
			return nil, err
		}
		result = append(result, call)
	}
	return result, nil
}

func p0p1AssertQuiet(t *testing.T, f *consoleTestLauncher, output string) {
	t.Helper()
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		if len(consoleChildren(f.cmd.Process.Pid, f.exe)) != 0 || consoleWindowFor(f.cmd.Process.Pid) != 0 {
			t.Fatal("silent launch or duplicate opened a console helper/window")
		}
		calls, err := p0p1BrowserCalls(output)
		if err != nil || len(calls) != 0 {
			t.Fatalf("silent launch or duplicate invoked the browser recorder: %+v %v", calls, err)
		}
		select {
		case <-f.done:
			t.Fatal("silent fixture unexpectedly exited")
		case <-timer.C:
			return
		case <-ticker.C:
		}
	}
}

func TestP0P1BrowserAndSilentStartup(t *testing.T) {
	p0p1RequireIsolatedLauncher(t)
	t.Run("normal-start-positive-browser-recorder", func(t *testing.T) {
		f, output := p0p1StartBrowserFixture(t, false)
		consoleEventually(t, 5*time.Second, "normal-start console helper", func() bool {
			return len(consoleChildren(f.cmd.Process.Pid, f.exe)) == 1 && consoleWindowFor(f.cmd.Process.Pid) != 0
		})
		var calls []p0p1BrowserCall
		consoleEventually(t, 5*time.Second, "positive browser recorder invocation", func() bool {
			var err error
			calls, err = p0p1BrowserCalls(output)
			return err == nil && len(calls) > 0
		})
		if len(calls) != 1 || calls[0].ParentPID != f.cmd.Process.Pid || len(calls[0].Args) != 2 || calls[0].Args[0] != "url.dll,FileProtocolHandler" || calls[0].Args[1] != f.url+"/" || !strings.EqualFold(filepath.Base(calls[0].Executable), "rundll32.exe") {
			t.Fatalf("positive startup browser record is not the owned expected call: %+v", calls)
		}
		p0p1AssertIconRegistration(t, f)
		p0p1Command(t, f, trayCommandQuit)
		f.waitExit(t)
		t.Logf("normal startup browser attempt positively intercepted: %+v", calls[0])
	})
	t.Run("silent-and-duplicate-stay-quiet", func(t *testing.T) {
		f, output := p0p1StartBrowserFixture(t, true)
		p0p1AssertIconRegistration(t, f)
		p0p1AssertQuiet(t, f, output)
		second := exec.Command(f.exe, "--dir", f.dir, "--silent")
		second.Dir = f.dir
		second.Env = append([]string(nil), f.cmd.Env...)
		second.SysProcAttr = hiddenProcessAttrs()
		if err := second.Run(); err != nil {
			t.Fatalf("owned duplicate silent launch: %v", err)
		}
		p0p1AssertQuiet(t, f, output)
		p0p1Command(t, f, trayCommandQuit)
		f.waitExit(t)
		t.Log("silent initial and duplicate launches had no console/helper or browser recorder calls over both observation windows")
	})
}
