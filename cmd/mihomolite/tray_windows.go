//go:build windows

package main

import (
	_ "embed"
	"runtime"
	"sync/atomic"
	"time"

	"github.com/getlantern/systray"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

//go:embed assets/tray-gray.ico
var trayIconGray []byte

//go:embed assets/tray-green.ico
var trayIconGreen []byte

// Empty in releases; isolated integration builds set this with -ldflags -X.
var instanceNamespace string

func instanceObjectName(kind string) *uint16 {
	name := "Local\\MihomoLite"
	if instanceNamespace != "" {
		name += "-" + instanceNamespace
	}
	return windows.StringToUTF16Ptr(name + "-" + kind)
}

const (
	trayCommandShow = iota + 1
	trayCommandLauncher
	trayCommandPanel
	trayCommandConnect
	trayCommandRestart
	trayCommandSeparator1
	trayCommandAutoStart
	trayCommandCloseToTray
	trayCommandSilentStart
	trayCommandSeparator2
	trayCommandQuit
)

var trayReady atomic.Bool
var closeToTray atomic.Bool

var (
	kernel32                = windows.NewLazySystemDLL("kernel32.dll")
	procGetConsoleWindow    = kernel32.NewProc("GetConsoleWindow")
	user32                  = windows.NewLazySystemDLL("user32.dll")
	procShowWindow          = user32.NewProc("ShowWindow")
	procSetForegroundWindow = user32.NewProc("SetForegroundWindow")
)

func acquireSingleInstance() (release func(), ok bool, err error) {
	handle, err := windows.CreateMutex(nil, false, instanceObjectName("SingleInstance"))
	if err == windows.ERROR_ALREADY_EXISTS {
		if handle != 0 {
			windows.CloseHandle(handle)
		}
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return func() { windows.CloseHandle(handle) }, true, nil
}

func notifyExistingInstance() error {
	handle, err := windows.OpenEvent(windows.EVENT_MODIFY_STATE, false, instanceObjectName("ShowEvent"))
	if err != nil {
		return err
	}
	defer windows.CloseHandle(handle)
	return windows.SetEvent(handle)
}

func createShowEvent() (windows.Handle, error) {
	handle, err := windows.CreateEvent(nil, 0, 0, instanceObjectName("ShowEvent"))
	if err == windows.ERROR_ALREADY_EXISTS && handle != 0 {
		return handle, nil
	}
	return handle, err
}

func closeShowEvent(handle windows.Handle) {
	windows.CloseHandle(handle)
}

func watchShowEvent(handle windows.Handle) {
	for {
		result, _ := windows.WaitForSingleObject(handle, 1000)
		if result == windows.WAIT_OBJECT_0 {
			showConsoleWindow()
		}
	}
}

// runTray starts the systray message loop in a goroutine. exit is called when
// the user chooses "退出"; it should cancel the main signal context.
func runTray(app *Application, settings *Settings, exit func()) {
	// Window creation and GetMessage must run on the same Windows thread.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	app.logger.Info("正在启动系统托盘")
	systray.Run(func() {
		app.logger.Info("系统托盘已就绪")
		trayReady.Store(true)
		systray.SetIcon(trayIconGray)
		systray.SetTitle("Mihomo Lite")
		systray.SetTooltip("Mihomo Lite - 未连接")

		mShow := systray.AddMenuItem("显示控制台", "打开控制台窗口")
		mLauncher := systray.AddMenuItem("启动器页面", "在浏览器中打开 Mihomo Lite 启动器")
		mPanel := systray.AddMenuItem("Zashboard 面板", "在浏览器中打开 Zashboard 面板")
		mConnect := systray.AddMenuItem("连接", "启动 mihomo")
		mRestart := systray.AddMenuItem("重启连接", "断开并重新连接")
		systray.AddSeparator()
		initial := app.RuntimeSettings()
		mAuto := systray.AddMenuItemCheckbox("开机自启", "登录时自动启动（仅当前用户）", initial.AutoStart)
		mTray := systray.AddMenuItemCheckbox("关闭窗口最小化到托盘", "关闭控制台窗口不会退出", initial.CloseToTray)
		mSilent := systray.AddMenuItemCheckbox("静默启动到托盘", "下次普通启动时不显示控制台或打开浏览器", initial.SilentStart)
		systray.AddSeparator()
		mQuit := systray.AddMenuItem("退出", "停止内核并退出")
		// systray publishes clicks with a non-blocking send. Its default
		// unbuffered channels drop commands while this loop is refreshing state
		// or applying a previous setting. Keep ordered clicks pending instead.
		bufferTrayClicks(mShow, mLauncher, mPanel, mConnect, mRestart, mAuto, mTray, mSilent, mQuit)

		lastConnected := false
		refresh := func() {
			if err := app.syncAutoStartFromRegistry(); err != nil {
				app.logger.Error("读取开机自启状态失败", "error", err)
			}
			st := app.State()
			if st.AutoStart {
				mAuto.Check()
			} else {
				mAuto.Uncheck()
			}
			if st.CloseToTray {
				mTray.Check()
			} else {
				mTray.Uncheck()
			}
			if st.SilentStart {
				mSilent.Check()
			} else {
				mSilent.Uncheck()
			}
			if !st.CanDisconnect && (st.Busy || !st.CanConnect) {
				mConnect.Disable()
			} else {
				mConnect.Enable()
			}
			if st.Running && !st.Busy {
				mRestart.Enable()
			} else {
				mRestart.Disable()
			}
			if st.HasPanelURL {
				mPanel.Enable()
			} else {
				mPanel.Disable()
			}
			connected := st.Running && st.ProxyEnabled
			if connected != lastConnected {
				lastConnected = connected
				if connected {
					systray.SetIcon(trayIconGreen)
				} else {
					systray.SetIcon(trayIconGray)
				}
			}
			if st.ConnectionRetry.Active {
				mConnect.SetTitle("取消自动连接")
				mConnect.SetTooltip("取消等待或尝试并恢复系统代理")
			} else if st.Running {
				mConnect.SetTitle("断开连接")
				mConnect.SetTooltip("停止 mihomo 并恢复系统代理")
			} else {
				mConnect.SetTitle("连接")
				mConnect.SetTooltip("启动 mihomo")
			}
			systray.SetTooltip(trayStatusTooltip(st))
		}
		refresh()
		ticker := time.NewTicker(800 * time.Millisecond)

		go func() {
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					refresh()
				case <-mShow.ClickedCh:
					showConsoleWindow()
				case <-mLauncher.ClickedCh:
					openURL(consoleController.url)
				case <-mPanel.ClickedCh:
					if url := app.State().PanelURL; url != "" {
						openURL(url)
					}
				case <-mConnect.ClickedCh:
					st := app.State()
					if st.Running || st.ConnectionRetry.Active {
						if err := app.requestDisconnect(); err != nil {
							app.logger.Error("断开连接请求失败", "error", err)
						}
					} else {
						if err := app.requestConnect(); err != nil {
							app.logger.Error("连接请求失败", "error", err)
						}
					}
				case <-mRestart.ClickedCh:
					// Reserve intent in click order; the request itself starts async work.
					if err := app.requestReconnect(); err != nil {
						app.logger.Error("重连请求失败", "error", err)
					}
				case <-mAuto.ClickedCh:
					target := !app.RuntimeSettings().AutoStart
					if _, err := app.updateRuntimeSettings(RuntimeSettingsPatch{AutoStart: &target}); err != nil {
						app.logger.Error("更新开机自启失败", "error", err)
					}
					refresh()
				case <-mTray.ClickedCh:
					target := !app.RuntimeSettings().CloseToTray
					if _, err := app.updateRuntimeSettings(RuntimeSettingsPatch{CloseToTray: &target}); err != nil {
						app.logger.Error("更新关闭行为失败", "error", err)
					}
					refresh()
				case <-mSilent.ClickedCh:
					target := !app.RuntimeSettings().SilentStart
					if _, err := app.updateRuntimeSettings(RuntimeSettingsPatch{SilentStart: &target}); err != nil {
						app.logger.Error("更新静默启动失败", "error", err)
					}
					refresh()
				case <-mQuit.ClickedCh:
					exit()
					systray.Quit()
					return
				}
			}
		}()
	}, func() {
		trayReady.Store(false)
		exit()
	})
}

func trayStatusTooltip(st State) string {
	if st.Running && st.ProxyEnabled {
		return "Mihomo Lite - 已连接"
	}
	if st.Busy {
		return "Mihomo Lite - 未连接（处理中）"
	}
	if st.Running {
		return "Mihomo Lite - 内核运行中，系统代理未开启"
	}
	return "Mihomo Lite - 未连接"
}

var autoRunKey = `Software\Microsoft\Windows\CurrentVersion\Run`

const autoRunValue = "MihomoLite"

func setAutoStart(enabled bool) error {
	if !enabled {
		key, err := registry.OpenKey(registry.CURRENT_USER, autoRunKey, registry.SET_VALUE)
		if err == registry.ErrNotExist {
			return nil
		}
		if err != nil {
			return err
		}
		defer key.Close()
		err = key.DeleteValue(autoRunValue)
		if err == registry.ErrNotExist {
			return nil
		}
		return err
	}
	key, _, err := registry.CreateKey(registry.CURRENT_USER, autoRunKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer key.Close()
	command, err := expectedAutoStartCommand()
	if err != nil {
		return err
	}
	return key.SetStringValue(autoRunValue, command)
}

func autoStartEnabled() bool { enabled, _ := readAutoStartEnabled(); return enabled }

func bufferTrayClicks(items ...*systray.MenuItem) {
	for _, item := range items {
		item.ClickedCh = make(chan struct{}, 8)
	}
}
