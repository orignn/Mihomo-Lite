//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"golang.org/x/sys/windows/registry"
)

type RuntimeSettings struct {
	ConnectionPolicy     ConnectionPolicy `json:"connectionPolicy"`
	AutoStart            bool             `json:"autoStart"`
	CloseToTray          bool             `json:"closeToTray"`
	SilentStart          bool             `json:"silentStart"`
	Ports                PortSettings     `json:"ports"`
	ActivePorts          PortSettings     `json:"activePorts"`
	PortsRestartRequired bool             `json:"portsRestartRequired"`
	PortsApplyAfter      string           `json:"portsApplyAfter"`
}

type RuntimeSettingsPatch struct {
	ConnectionPolicy *ConnectionPolicy `json:"connectionPolicy"`
	AutoStart        *bool             `json:"autoStart"`
	CloseToTray      *bool             `json:"closeToTray"`
	SilentStart      *bool             `json:"silentStart"`
	Ports            *PortSettings     `json:"ports"` // Replaces the full port preferences object.
}

type autoStartRegistration struct {
	Exists bool
	Value  string
	Kind   uint32
}

func expectedAutoStartCommand() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return "\"" + exe + "\" --silent", nil
}

func readAutoStartRegistration() (autoStartRegistration, error) {
	key, err := registry.OpenKey(registry.CURRENT_USER, autoRunKey, registry.QUERY_VALUE)
	if errors.Is(err, registry.ErrNotExist) {
		return autoStartRegistration{}, nil
	}
	if err != nil {
		return autoStartRegistration{}, err
	}
	defer key.Close()
	value, kind, err := key.GetStringValue(autoRunValue)
	if errors.Is(err, registry.ErrNotExist) {
		return autoStartRegistration{}, nil
	}
	if err != nil {
		return autoStartRegistration{}, err
	}
	return autoStartRegistration{Exists: true, Value: value, Kind: kind}, nil
}

func restoreAutoStartRegistration(saved autoStartRegistration) error {
	if !saved.Exists {
		return setAutoStart(false)
	}
	key, _, err := registry.CreateKey(registry.CURRENT_USER, autoRunKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer key.Close()
	if saved.Kind == registry.EXPAND_SZ {
		return key.SetExpandStringValue(autoRunValue, saved.Value)
	}
	return key.SetStringValue(autoRunValue, saved.Value)
}

func readAutoStartEnabled() (bool, error) {
	saved, err := readAutoStartRegistration()
	if err != nil {
		return false, err
	}
	expected, err := expectedAutoStartCommand()
	if err != nil {
		return false, err
	}
	return saved.Exists && strings.EqualFold(strings.TrimSpace(saved.Value), expected), nil
}

func (a *Application) runtimeSettingsLocked() RuntimeSettings {
	ports, active := a.configuredPortsLocked(), a.activePortsLocked()
	policy, _ := normalizeConnectionPolicy(a.config.ConnectionPolicy)
	return RuntimeSettings{ConnectionPolicy: policy, AutoStart: a.config.AutoStart, CloseToTray: a.config.CloseToTray, SilentStart: a.config.SilentStart,
		Ports: ports, ActivePorts: active, PortsRestartRequired: ports != active, PortsApplyAfter: "program-restart"}
}

func (a *Application) RuntimeSettings() RuntimeSettings {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.runtimeSettingsLocked()
}

// Registry is the source of truth, including changes outside this instance.
func (a *Application) syncAutoStartFromRegistry() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	enabled, err := readAutoStartEnabled()
	if err != nil {
		return err
	}
	if a.config.AutoStart != enabled {
		a.config.AutoStart = enabled
		if a.events != nil {
			a.broadcastLocked()
		}
	}
	return nil
}

// Serialize tray/HTTP updates and publish only a persisted, applied snapshot.
func (a *Application) updateRuntimeSettings(patch RuntimeSettingsPatch) (RuntimeSettings, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.backupRecoveryBlocked {
		return a.runtimeSettingsLocked(), errors.New("配置恢复事务未完成，请重新启动后修改运行设置")
	}
	if a.operation {
		return a.runtimeSettingsLocked(), errors.New("另一个操作正在进行，请稍后修改运行设置")
	}
	before := *a.config
	next := before
	if patch.AutoStart != nil {
		next.AutoStart = *patch.AutoStart
	}
	if patch.CloseToTray != nil {
		next.CloseToTray = *patch.CloseToTray
	}
	if patch.SilentStart != nil {
		next.SilentStart = *patch.SilentStart
	}
	if patch.Ports != nil {
		var err error
		next.Ports, err = normalizePortSettings(*patch.Ports)
		if err != nil {
			return a.runtimeSettingsLocked(), err
		}
	}
	if patch.ConnectionPolicy != nil {
		var err error
		next.ConnectionPolicy, err = normalizeConnectionPolicy(*patch.ConnectionPolicy)
		if err != nil {
			return a.runtimeSettingsLocked(), err
		}
	}
	// Freeze the old settings before persistence; a save cannot change any live
	// endpoint, current YAML validation or subsequent reconnect in this process.
	a.activePortsLocked()
	var saved autoStartRegistration
	var err error
	autoChanged := patch.AutoStart != nil
	if autoChanged {
		saved, err = readAutoStartRegistration()
		if err != nil {
			return a.runtimeSettingsLocked(), err
		}
		if err = setAutoStart(next.AutoStart); err != nil {
			return a.runtimeSettingsLocked(), fmt.Errorf("写入开机自启失败: %w", err)
		}
	}
	rollback := func(cause error) (RuntimeSettings, error) {
		if patch.CloseToTray != nil {
			if closeErr := setCloseToTray(before.CloseToTray); closeErr != nil {
				cause = errors.Join(cause, closeErr)
			}
		}
		if autoChanged {
			if regErr := restoreAutoStartRegistration(saved); regErr != nil {
				cause = errors.Join(cause, regErr)
			}
		}
		return a.runtimeSettingsLocked(), cause
	}
	if patch.CloseToTray != nil {
		if err = setCloseToTray(next.CloseToTray); err != nil {
			return rollback(err)
		}
	}
	if err = a.saveSettingsValue(next); err != nil {
		return rollback(fmt.Errorf("保存运行设置失败: %w", err))
	}
	*a.config = next
	if patch.ConnectionPolicy != nil {
		a.connectionPolicyChangedLocked()
	}
	if a.events != nil {
		a.broadcastLocked()
	}
	return a.runtimeSettingsLocked(), nil
}

func (a *Application) setConfigName(name string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	next := *a.config
	next.ConfigName = name
	if err := a.saveSettingsValue(next); err != nil {
		return err
	}
	*a.config = next
	return nil
}
