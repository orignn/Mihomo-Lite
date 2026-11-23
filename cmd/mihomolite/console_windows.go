//go:build windows

package main

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"syscall"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

var consoleSignal = make(chan struct{})
var consoleSignalOnce sync.Once

// CTRL_CLOSE_EVENT cannot be cancelled: Windows terminates every process
// attached to that console after its handler returns. Only the disposable
// helper owns a console; the launcher, tray and core never attach to it.
var consoleController struct {
	sync.Mutex
	cmd       *exec.Cmd
	url       string
	logger    *slog.Logger
	keepEvent windows.Handle
	nonce     string
}

func consoleEventName(kind string, pid int, nonce string) *uint16 {
	return windows.StringToUTF16Ptr(fmt.Sprintf("Local\\MihomoLite-Console%s-%d-%s", kind, pid, nonce))
}

func consoleTitle(pid int) string {
	return fmt.Sprintf("Mihomo Lite v%s - 控制台 [%d]", AppVersion, pid)
}

func detachConsole() {
	// Also detach when launched from cmd/PowerShell or through go run.
	kernel32.NewProc("FreeConsole").Call()
}

func initConsoleController(url string, logger *slog.Logger) error {
	nonce, err := randomSecret()
	if err != nil {
		return err
	}
	exitEvent, err := windows.CreateEvent(nil, 0, 0, consoleEventName("Exit", os.Getpid(), nonce))
	if err != nil {
		return err
	}
	keepEvent, err := windows.CreateEvent(nil, 1, 0, consoleEventName("Keep", os.Getpid(), nonce))
	if err != nil {
		windows.CloseHandle(exitEvent)
		return err
	}
	consoleController.Lock()
	consoleController.url = url
	consoleController.nonce = nonce
	consoleController.logger = logger
	consoleController.keepEvent = keepEvent
	consoleController.Unlock()
	setCloseToTray(closeToTray.Load())
	go func() {
		defer windows.CloseHandle(exitEvent)
		if result, err := windows.WaitForSingleObject(exitEvent, windows.INFINITE); err == nil && result == windows.WAIT_OBJECT_0 {
			consoleSignalOnce.Do(func() { close(consoleSignal) })
		}
	}()
	return nil
}

func setCloseToTray(enabled bool) error {
	consoleController.Lock()
	defer consoleController.Unlock()
	if consoleController.keepEvent != 0 {
		var err error
		if enabled {
			err = windows.SetEvent(consoleController.keepEvent)
		} else {
			err = windows.ResetEvent(consoleController.keepEvent)
		}
		if err != nil {
			return fmt.Errorf("更新控制台关闭行为失败: %w", err)
		}
	}
	closeToTray.Store(enabled)
	return nil
}

func consoleHWND() uintptr {
	// This HWND belongs to the helper's console host, not to the launcher.
	title := windows.StringToUTF16Ptr(consoleTitle(os.Getpid()))
	handle, _, _ := user32.NewProc("FindWindowW").Call(0, uintptr(unsafe.Pointer(title)))
	return handle
}

func showConsoleWindow() {
	consoleController.Lock()
	defer consoleController.Unlock()
	if consoleController.cmd != nil && processExists(consoleController.cmd.Process.Pid) {
		if handle := consoleHWND(); handle != 0 {
			procShowWindow.Call(handle, 9) // SW_RESTORE, including minimized windows
			procSetForegroundWindow.Call(handle)
		} else {
			// ConPTY/Windows Terminal may not expose a standalone console HWND.
			// Keep the helper and open the actual web console rather than losing Show.
			openURL(consoleController.url)
		}
		return
	}
	if consoleController.url == "" {
		return
	}
	exe, err := os.Executable()
	if err == nil {
		cmd := exec.Command(exe, "--console-helper", strconv.Itoa(os.Getpid()), consoleController.url, consoleController.nonce)
		cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_CONSOLE}
		err = cmd.Start()
		if err == nil {
			consoleController.cmd = cmd
			go func() {
				waitErr := cmd.Wait()
				consoleController.Lock()
				if waitErr != nil && cmd.ProcessState != nil && cmd.ProcessState.ExitCode() == 1 && consoleController.logger != nil {
					consoleController.logger.Error("控制台辅助进程初始化失败", "error", waitErr)
				}
				if consoleController.cmd == cmd {
					consoleController.cmd = nil
				}
				consoleController.Unlock()
			}()
		}
	}
	if err != nil && consoleController.logger != nil {
		consoleController.logger.Error("打开控制台窗口失败", "error", err)
	}
}

func runConsoleHelper(parentPID int, url, nonce string) error {
	// Open the process handle first so a recycled PID cannot leave a helper alive.
	parent, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(parentPID))
	if err != nil {
		return err
	}
	defer windows.CloseHandle(parent)
	exitEvent, err := windows.OpenEvent(windows.EVENT_MODIFY_STATE, false, consoleEventName("Exit", parentPID, nonce))
	if err != nil {
		return err
	}
	defer windows.CloseHandle(exitEvent)
	keepEvent, err := windows.OpenEvent(windows.SYNCHRONIZE, false, consoleEventName("Keep", parentPID, nonce))
	if err != nil {
		return err
	}
	defer windows.CloseHandle(keepEvent)

	if handle, _, _ := procGetConsoleWindow.Call(); handle == 0 {
		if result, _, err := kernel32.NewProc("AllocConsole").Call(); result == 0 {
			return err
		}
	}
	// Use UTF-8 for any byte-based console I/O, and Unicode writes below.
	if err := windows.SetConsoleCP(65001); err != nil {
		return err
	}
	if err := windows.SetConsoleOutputCP(65001); err != nil {
		return err
	}
	// A windowsgui executable starts with no usable standard handles.
	out, err := os.OpenFile("CONOUT$", os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer out.Close()
	in, err := os.OpenFile("CONIN$", os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer in.Close()
	os.Stdin, os.Stdout, os.Stderr = in, out, out
	for kind, file := range map[uint32]*os.File{windows.STD_INPUT_HANDLE: in, windows.STD_OUTPUT_HANDLE: out, windows.STD_ERROR_HANDLE: out} {
		if err := windows.SetStdHandle(kind, windows.Handle(file.Fd())); err != nil {
			return err
		}
	}
	callback := syscall.NewCallback(func(event uint32) uintptr {
		switch event {
		case 2: // Only the helper is terminated by CTRL_CLOSE_EVENT.
			if result, err := windows.WaitForSingleObject(keepEvent, 0); err == nil && result == uint32(windows.WAIT_TIMEOUT) {
				_ = windows.SetEvent(exitEvent)
			}
			return 1
		case 0, 1, 5, 6: // Ctrl+C/Break and session shutdown still request cleanup.
			_ = windows.SetEvent(exitEvent)
			return 1
		}
		return 0
	})
	if result, _, err := kernel32.NewProc("SetConsoleCtrlHandler").Call(callback, 1); result == 0 {
		return err
	}
	// Publish the final title only after close/Ctrl+C handling is installed.
	title := windows.StringToUTF16Ptr(consoleTitle(parentPID))
	kernel32.NewProc("SetConsoleTitleW").Call(uintptr(unsafe.Pointer(title)))
	message := fmt.Sprintf("\n  Mihomo Lite v%s 已启动\n\n  控制台: %s\n\n  已驻留系统托盘；开启“关闭窗口最小化到托盘”时，点 × 只关闭此窗口。\n  右键托盘“显示控制台”可重新打开；托盘“退出”会停止内核并恢复代理。\n  Ctrl+C 也可主动退出；关闭行为开关关闭时，点 × 将退出启动器。\n\n", AppVersion, url)
	if err := writeConsoleText(windows.Handle(out.Fd()), message); err != nil {
		return err
	}
	_, err = windows.WaitForSingleObject(parent, windows.INFINITE)
	return err
}

// Write UTF-16 directly so Chinese never depends on the OEM/ANSI code page.
func writeConsoleText(handle windows.Handle, text string) error {
	data := utf16.Encode([]rune(text))
	for len(data) > 0 {
		var written uint32
		if err := windows.WriteConsole(handle, &data[0], uint32(len(data)), &written, nil); err != nil {
			return err
		}
		if written == 0 {
			return fmt.Errorf("控制台 Unicode 输出未写入字符")
		}
		data = data[written:]
	}
	return nil
}

func showStartupError(err error) {
	text := windows.StringToUTF16Ptr("启动失败: " + err.Error())
	title := windows.StringToUTF16Ptr("Mihomo Lite")
	user32.NewProc("MessageBoxW").Call(0, uintptr(unsafe.Pointer(text)), uintptr(unsafe.Pointer(title)), 0x10)
}
