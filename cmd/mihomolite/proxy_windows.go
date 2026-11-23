//go:build windows

package main

import (
	"errors"
	"fmt"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const windowsProxyBypass = "<local>;localhost;127.*;10.*;172.16.*;172.17.*;172.18.*;172.19.*;172.20.*;172.21.*;172.22.*;172.23.*;172.24.*;172.25.*;172.26.*;172.27.*;172.28.*;172.29.*;172.30.*;172.31.*;192.168.*"

type proxySnapshot struct {
	enabled       uint32
	server        string
	bypass        string
	autoConfigURL string
	autoDetect    uint32
}

type systemProxy interface {
	Enable(server, bypass string) error
	Disable() error
	LastError() string
	Inspect() (proxySnapshot, bool, error)
}

var newApplicationProxy = func() systemProxy { return NewWindowsProxy() }

// Keep OS effects behind a backend so regression tests can exercise the same
// snapshot/rollback logic against a private HKCU key without touching WinINet.
type proxyBackend interface {
	Read() (proxySnapshot, error)
	Write(proxySnapshot) error
	Apply(server, bypass string) error
}

type windowsProxyBackend struct{}

func (windowsProxyBackend) Read() (proxySnapshot, error) { return readProxySnapshot() }
func (windowsProxyBackend) Write(s proxySnapshot) error {
	return writeProxyRegistry(s.enabled, s.server, s.bypass, s.autoConfigURL, s.autoDetect)
}
func (windowsProxyBackend) Apply(server, bypass string) error { return applyProxy(server, bypass) }

type WindowsProxy struct {
	mu             sync.Mutex
	lastError      string
	original       *proxySnapshot
	backend        proxyBackend
	owned          *proxySnapshot
	store          recoveryStore
	record         *proxyRecoveryRecord
	recoveryError  error
	foreignSession bool
	checkProcess   func(recoveryProcessIdentity) (bool, error)
	captureProcess func(int) (recoveryProcessIdentity, error)
	stopProcess    func(recoveryProcessIdentity) error
	legacyCorePath string // Operational PID file, retired before losing modern identity.
}

func NewWindowsProxy() *WindowsProxy      { return &WindowsProxy{backend: windowsProxyBackend{}} }
func (p *WindowsProxy) LastError() string { p.mu.Lock(); defer p.mu.Unlock(); return p.lastError }

// Inspect uses the same backend as Enable and Disable, including in isolated tests.
func (p *WindowsProxy) Inspect() (proxySnapshot, bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	current, err := p.backend.Read()
	return current, p.recoveryError != nil || p.original != nil && (p.foreignSession || p.lastError != ""), err
}

func (p *WindowsProxy) Enable(server, bypass string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.ownerGoneLocked(); err != nil {
		p.lastError = err.Error()
		return err
	}
	current, err := p.backend.Read()
	if err != nil {
		p.lastError = "无法读取 Windows 代理设置"
		return errors.New(p.lastError)
	}
	if p.original != nil && p.owned != nil && current != *p.owned && current != *p.original {
		p.lastError = errProxyOwnership.Error()
		return errProxyOwnership
	}
	next := current
	next.enabled, next.server, next.bypass = 1, server, bypass
	original := p.original
	if original == nil {
		original = &current
	}
	if p.store != nil {
		record := p.record
		if record == nil {
			record, err = p.newRecordLocked()
			if err != nil {
				p.lastError = err.Error()
				return err
			}
		}
		prepared := *record
		prepared.Original = snapshotForRecovery(*original)
		prepared.Applied = snapshotForRecovery(next)
		prepared.Phase = "prepared"
		prepared.Endpoint = next.server
		if err := validateRecoveryRecord(&prepared); err != nil {
			return err
		}
		if err := p.saveRecordLocked(&prepared); err != nil {
			return err
		}
	}
	p.original = original
	p.owned = &next
	rollback := func(cause error) error {
		if restoreErr := p.disableLocked(); restoreErr != nil {
			cause = errors.Join(cause, fmt.Errorf("恢复原系统代理失败: %w", restoreErr))
		}
		p.lastError = cause.Error()
		return cause
	}
	// Read again after durable preparation: don't overwrite a takeover during disk IO.
	latest, err := p.backend.Read()
	if err != nil {
		return rollback(errors.New("系统代理写入前核验失败"))
	}
	if latest != current {
		p.lastError = errProxyOwnership.Error()
		return errProxyOwnership
	}
	if err := p.backend.Write(next); err != nil {
		return rollback(errors.New("写入 Windows 代理设置失败"))
	}
	if err := p.backend.Apply(server, bypass); err != nil {
		return rollback(errors.New("通知 Windows 代理变更失败"))
	}
	if err := verifyProxySnapshotWith(p.backend.Read, next); err != nil {
		return rollback(err)
	}
	if p.record != nil {
		applied := *p.record
		applied.Phase = "applied"
		if err := p.saveRecordLocked(&applied); err != nil {
			return rollback(err)
		}
	}
	p.lastError = ""
	return nil
}

func (p *WindowsProxy) Disable() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.disableLocked()
}
func (p *WindowsProxy) disableLocked() error {
	if err := p.ownerGoneLocked(); err != nil {
		p.lastError = err.Error()
		return err
	}
	if p.original == nil {
		p.lastError = ""
		return nil
	}
	fail := func(err error) error { p.lastError = err.Error(); return err }
	current, err := p.backend.Read()
	if err != nil {
		return fail(errors.New("无法核验当前系统代理，保留恢复快照"))
	}
	if p.owned == nil || current != *p.owned && current != *p.original {
		return fail(errProxyOwnership)
	}
	if p.record != nil {
		restoring := *p.record
		restoring.Phase = "restoring"
		if err := p.saveRecordLocked(&restoring); err != nil {
			return fail(err)
		}
	}
	latest, err := p.backend.Read()
	if err != nil {
		return fail(errors.New("系统代理恢复前核验失败"))
	}
	if latest != current {
		return fail(errProxyOwnership)
	}
	// current==original can mean a crash after registry restore, before WinINet notification.
	if current == *p.original {
		s := *p.original
		server, bypass := s.server, s.bypass
		if s.enabled == 0 || server == "" {
			server = ""
			bypass = windowsProxyBypass
		}
		if err := p.backend.Apply(server, bypass); err != nil {
			return fail(errors.New("通知原系统代理恢复失败，可重试"))
		}
	} else if err := p.restoreSnapshot(*p.original); err != nil {
		return fail(errors.New("恢复原系统代理失败，可重试"))
	}
	restored, err := p.backend.Read()
	if err != nil || restored != *p.original {
		return fail(errors.New("原系统代理恢复核验失败，保留快照；可能被其他软件接管"))
	}
	if p.record != nil {
		cleared := *p.record
		cleared.Original = nil
		cleared.Applied = nil
		cleared.Phase = "core"
		if err := p.saveRecordLocked(&cleared); err != nil {
			return fail(err)
		}
	}
	p.original = nil
	p.owned = nil
	p.lastError = ""
	return nil
}

const systemProxyRegistryKey = `Software\Microsoft\Windows\CurrentVersion\Internet Settings`

func readProxySnapshot() (proxySnapshot, error) { return readProxySnapshotAt(systemProxyRegistryKey) }

func readProxySnapshotAt(path string) (proxySnapshot, error) {
	key, err := registry.OpenKey(registry.CURRENT_USER, path, registry.QUERY_VALUE)
	if err != nil {
		return proxySnapshot{}, fmt.Errorf("无法读取 Windows 代理设置: %w", err)
	}
	defer key.Close()
	var snapshot proxySnapshot
	if snapshot.server, _, err = key.GetStringValue("ProxyServer"); err == windows.ERROR_FILE_NOT_FOUND {
		err = nil
	}
	if err != nil {
		return proxySnapshot{}, err
	}
	if snapshot.bypass, _, err = key.GetStringValue("ProxyOverride"); err == windows.ERROR_FILE_NOT_FOUND {
		err = nil
	}
	if err != nil {
		return proxySnapshot{}, err
	}
	if snapshot.autoConfigURL, _, err = key.GetStringValue("AutoConfigURL"); err == windows.ERROR_FILE_NOT_FOUND {
		err = nil
	}
	if err != nil {
		return proxySnapshot{}, err
	}
	var enabled uint64
	if enabled, _, err = key.GetIntegerValue("ProxyEnable"); err == windows.ERROR_FILE_NOT_FOUND {
		err = nil
	}
	if err != nil {
		return proxySnapshot{}, err
	}
	snapshot.enabled = uint32(enabled)
	var autoDetect uint64
	if autoDetect, _, err = key.GetIntegerValue("AutoDetect"); err == windows.ERROR_FILE_NOT_FOUND {
		err = nil
	}
	if err != nil {
		return proxySnapshot{}, err
	}
	snapshot.autoDetect = uint32(autoDetect)
	return snapshot, nil
}

func writeProxyRegistry(enabled uint32, server, bypass, autoConfigURL string, autoDetect uint32) error {
	return writeProxyRegistryAt(systemProxyRegistryKey, proxySnapshot{enabled: enabled, server: server, bypass: bypass, autoConfigURL: autoConfigURL, autoDetect: autoDetect})
}

func writeProxyRegistryAt(path string, s proxySnapshot) error {
	enabled, server, bypass, autoConfigURL, autoDetect := s.enabled, s.server, s.bypass, s.autoConfigURL, s.autoDetect
	key, err := registry.OpenKey(registry.CURRENT_USER, path, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("无法写入 Windows 代理设置: %w", err)
	}
	defer key.Close()
	if err := key.SetStringValue("ProxyServer", server); err != nil {
		return err
	}
	if err := key.SetStringValue("ProxyOverride", bypass); err != nil {
		return err
	}
	if err := key.SetDWordValue("ProxyEnable", enabled); err != nil {
		return err
	}
	if err := key.SetDWordValue("AutoDetect", autoDetect); err != nil {
		return err
	}
	if autoConfigURL != "" {
		if err := key.SetStringValue("AutoConfigURL", autoConfigURL); err != nil {
			return err
		}
	} else if err := key.DeleteValue("AutoConfigURL"); err != nil && err != windows.ERROR_FILE_NOT_FOUND {
		return err
	}
	return nil
}

func restoreProxy(snapshot proxySnapshot) error {
	if err := writeProxyRegistry(snapshot.enabled, snapshot.server, snapshot.bypass, snapshot.autoConfigURL, snapshot.autoDetect); err != nil {
		return err
	}
	if snapshot.enabled != 0 && snapshot.server != "" {
		return applyProxy(snapshot.server, snapshot.bypass)
	}
	return applyProxy("", windowsProxyBypass)
}

func (p *WindowsProxy) restoreSnapshot(s proxySnapshot) error {
	if err := p.backend.Write(s); err != nil {
		return err
	}
	if s.enabled != 0 && s.server != "" {
		return p.backend.Apply(s.server, s.bypass)
	}
	return p.backend.Apply("", windowsProxyBypass)
}

func verifyProxySnapshotWith(read func() (proxySnapshot, error), expected proxySnapshot) error {
	deadline := time.Now().Add(2 * time.Second)
	for {
		current, err := read()
		if err != nil {
			return errors.New("无法核验完整系统代理状态")
		}
		if current == expected {
			return nil
		}
		if time.Now().After(deadline) {
			return errors.New("完整系统代理核验失败；可能被其他软件接管（地址详情已隐藏）")
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func verifyProxy(enabled uint32, server, bypass string) error {
	return verifyProxyWith(readProxySnapshot, enabled, server, bypass)
}

func verifyProxyWith(read func() (proxySnapshot, error), enabled uint32, server, bypass string) error {
	deadline := time.Now().Add(2 * time.Second)
	for {
		current, err := read()
		if err != nil {
			return err
		}
		if current.enabled == enabled && current.server == server && current.bypass == bypass {
			return nil
		}
		if time.Now().After(deadline) {
			return errors.New("系统代理核验失败；可能被其他代理软件覆盖（地址详情已隐藏）")
		}
		time.Sleep(100 * time.Millisecond)
	}
}

type internetProxyInfo struct {
	accessType         uint32
	proxy, proxyBypass *uint16
}

func applyProxy(server, bypass string) error {
	proxy, err := windows.UTF16PtrFromString(server)
	if err != nil {
		return err
	}
	bypassPtr, err := windows.UTF16PtrFromString(bypass)
	if err != nil {
		return err
	}
	accessType := uint32(1)
	if server != "" {
		accessType = 3
	}
	info := internetProxyInfo{accessType: accessType, proxy: proxy, proxyBypass: bypassPtr}
	proc := windows.NewLazySystemDLL("wininet.dll").NewProc("InternetSetOptionW")
	result, _, callErr := proc.Call(0, 38, uintptr(unsafe.Pointer(&info)), unsafe.Sizeof(info))
	if result == 0 {
		return fmt.Errorf("InternetSetOptionW 设置代理失败: %v", callErr)
	}
	notifyProxyChange()
	return nil
}
func notifyProxyChange() {
	proc := windows.NewLazySystemDLL("wininet.dll").NewProc("InternetSetOptionW")
	_, _, _ = proc.Call(0, 39, 0, 0)
	_, _, _ = proc.Call(0, 37, 0, 0)
	// WM_SETTINGCHANGE lets browsers refresh without depending only on option 38.
	user32 := windows.NewLazySystemDLL("user32.dll").NewProc("SendMessageTimeoutW")
	target, _ := windows.UTF16PtrFromString(`Software\Microsoft\Windows\CurrentVersion\Internet Settings`)
	var result uintptr
	_, _, _ = user32.Call(0xffff, 0x1a, 0, uintptr(unsafe.Pointer(target)), 0x2, 1000, uintptr(unsafe.Pointer(&result)))
}
