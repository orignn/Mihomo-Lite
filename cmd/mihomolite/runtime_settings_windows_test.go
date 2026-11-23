//go:build windows

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"golang.org/x/sys/windows"
)

func runtimeSettingsTestApp(t *testing.T) *Application {
	t.Helper()
	dir := t.TempDir()
	return &Application{baseDir: dir, dataDir: filepath.Join(dir, "data"), config: &Settings{Listen: "127.0.0.1:17653", ConfigName: "existing-profile", APISecret: "preserve-secret", CloseToTray: true}, proxy: NewWindowsProxy()}
}

func TestRuntimeSettingsPersistAndPreserveOtherFields(t *testing.T) {
	old := closeToTray.Load()
	defer setCloseToTray(old)
	a := runtimeSettingsTestApp(t)
	disabled := false
	enabled := true
	settings, err := a.updateRuntimeSettings(RuntimeSettingsPatch{CloseToTray: &disabled, SilentStart: &enabled})
	if err != nil {
		t.Fatal(err)
	}
	if settings.CloseToTray || !settings.SilentStart || closeToTray.Load() {
		t.Fatal("runtime close setting did not update")
	}
	saved, err := LoadSettings(filepath.Join(a.baseDir, "launcher.json"))
	if err != nil {
		t.Fatal(err)
	}
	if saved.CloseToTray || !saved.SilentStart || saved.APISecret != "preserve-secret" || saved.ConfigName != "existing-profile" {
		t.Fatalf("wrong persisted snapshot: %+v", saved)
	}
	stateJSON, _ := json.Marshal(a.State())
	var body map[string]any
	json.Unmarshal(stateJSON, &body)
	if body["closeToTray"] != false || body["autoStart"] != false || body["silentStart"] != true {
		t.Fatalf("State/SSE snapshot lacks settings: %s", stateJSON)
	}
}

func TestRuntimeSettingsWriteFailureRollsBack(t *testing.T) {
	old := closeToTray.Load()
	defer setCloseToTray(old)
	a := runtimeSettingsTestApp(t)
	blocked := filepath.Join(a.baseDir, "not-a-directory")
	if err := os.WriteFile(blocked, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	a.baseDir = blocked
	setCloseToTray(true)
	disabled := false
	if _, err := a.updateRuntimeSettings(RuntimeSettingsPatch{CloseToTray: &disabled}); err == nil {
		t.Fatal("expected persistence failure")
	}
	if !a.RuntimeSettings().CloseToTray || !closeToTray.Load() {
		t.Fatal("failed save committed an in-memory/IPC change")
	}
}

func TestCloseEventFailureDoesNotCommit(t *testing.T) {
	consoleController.Lock()
	saved := consoleController.keepEvent
	consoleController.keepEvent = windows.Handle(^uintptr(0))
	consoleController.Unlock()
	old := closeToTray.Load()
	defer func() {
		consoleController.Lock()
		consoleController.keepEvent = saved
		consoleController.Unlock()
		setCloseToTray(old)
	}()
	if err := setCloseToTray(!old); err == nil {
		t.Fatal("expected invalid event error")
	}
	if closeToTray.Load() != old {
		t.Fatal("invalid event committed the atomic close setting")
	}
}

func TestConcurrentRuntimeSettingsAndProfileNames(t *testing.T) {
	old := closeToTray.Load()
	defer setCloseToTray(old)
	a := runtimeSettingsTestApp(t)
	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			value := i%2 == 0
			if _, err := a.updateRuntimeSettings(RuntimeSettingsPatch{CloseToTray: &value}); err != nil {
				t.Error(err)
			}
			if err := a.setConfigName("profile-" + strconv.Itoa(i)); err != nil {
				t.Error(err)
			}
			_ = a.RuntimeSettings()
		}(i)
	}
	wg.Wait()
	saved, err := LoadSettings(filepath.Join(a.baseDir, "launcher.json"))
	if err != nil {
		t.Fatal(err)
	}
	if saved.APISecret != "preserve-secret" || saved.ConfigName != a.config.ConfigName || saved.CloseToTray != a.config.CloseToTray {
		t.Fatalf("racing save lost fields: %+v", saved)
	}
}

func TestAtomicReplaceFailureKeepsOldFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(path, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(p, windows.GENERIC_READ, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(handle)
	if err := atomicWrite(path, []byte("replacement"), 0600); err == nil {
		t.Fatal("expected locked destination replacement to fail")
	}
	content, err := os.ReadFile(path)
	if err != nil || string(content) != "original" {
		t.Fatalf("failed atomic replace damaged original: %q %v", content, err)
	}
}

func TestConsoleChineseUnicodeBuffer(t *testing.T) {
	f := startConsoleTestLauncher(t, false)
	f.window(t)
	helper := consoleChildren(f.cmd.Process.Pid, f.exe)[0]
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	textPath := filepath.Join(dir, "screen.txt")
	cmd := exec.Command(exe, "-test.run=^TestConsoleControlProbe$")
	cmd.SysProcAttr = hiddenProcessAttrs()
	cmd.Env = append(os.Environ(), "MIHOMOLITE_CONSOLE_PROBE_PID="+strconv.Itoa(helper), "MIHOMOLITE_CONSOLE_PROBE_OUTPUT="+filepath.Join(dir, "pids.json"), "MIHOMOLITE_CONSOLE_PROBE_TEXT_OUTPUT="+textPath)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("screen probe: %v %s", err, output)
	}
	content, err := os.ReadFile(textPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(content)
	for _, expected := range []string{"已启动", "控制台:", "已驻留系统托盘", "关闭窗口最小化到托盘", "恢复代理"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("missing readable Chinese %q in native screen buffer: %s", expected, text)
		}
	}
	if strings.Contains(text, "宸插") || strings.Contains(text, "鎺у") {
		t.Fatal("native screen buffer contains mojibake")
	}
	consolePost(t, consoleTrayFor(f.cmd.Process.Pid), 0x111, uintptr(trayCommandQuit))
	f.waitExit(t)
	t.Log("ReadConsoleOutputCharacterW verified readable Chinese in the real console buffer")
}

func TestSilentDuplicateDoesNotShowConsole(t *testing.T) {
	f := startConsoleTestLauncher(t, true)
	second := exec.Command(f.exe, "--dir", f.dir, "--silent")
	second.SysProcAttr = hiddenProcessAttrs()
	second.Env = append(os.Environ(), "MIHOMOLITE_NO_BROWSER=1", "MIHOMOLITE_NO_PAUSE=1")
	if err := second.Run(); err != nil {
		t.Fatal(err)
	}
	if len(consoleChildren(f.cmd.Process.Pid, f.exe)) != 0 {
		t.Fatal("repeated --silent launch created a viewer")
	}
	consolePost(t, consoleTrayFor(f.cmd.Process.Pid), 0x111, uintptr(trayCommandQuit))
	f.waitExit(t)
}

func TestRuntimeSettingsSilentStartFailureRollsBack(t *testing.T) {
	a := runtimeSettingsTestApp(t)
	blocked := filepath.Join(a.baseDir, "not-a-directory")
	if err := os.WriteFile(blocked, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	a.baseDir = blocked
	enabled := true
	if _, err := a.updateRuntimeSettings(RuntimeSettingsPatch{SilentStart: &enabled}); err == nil {
		t.Fatal("expected persistence failure")
	}
	if a.RuntimeSettings().SilentStart {
		t.Fatal("failed silent-start save changed in-memory state")
	}
}
