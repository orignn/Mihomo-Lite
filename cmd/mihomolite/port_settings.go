package main

import (
	"errors"
	"fmt"
	"net"
	"strconv"
)

// Ports are saved preferences, not live listener addresses. All changes apply at
// the next program start so reloads, reconnects and validation share one snapshot.
type PortOption struct {
	Mode string `json:"mode"`
	Port int    `json:"port"`
}
type PortSettings struct {
	Proxy   PortOption `json:"proxy"`
	Backend PortOption `json:"backend"`
	Panel   PortOption `json:"panel"`
	Web     PortOption `json:"web"`
}

var errPortSettings = errors.New("端口设置无效")

func normalizePortSettings(p PortSettings) (PortSettings, error) {
	endpoints := []struct {
		name   string
		option *PortOption
	}{
		{"代理", &p.Proxy}, {"内核 backend API", &p.Backend}, {"面板 API 转发", &p.Panel}, {"启动器 Web", &p.Web},
	}
	fixed := map[int]string{}
	for _, endpoint := range endpoints {
		option := endpoint.option
		if option.Mode == "" {
			option.Mode = "auto"
		}
		switch option.Mode {
		case "auto":
			if option.Port != 0 {
				return p, fmt.Errorf("%w：%s自动模式端口必须为 0", errPortSettings, endpoint.name)
			}
		case "fixed":
			if option.Port < 1 || option.Port > 65535 {
				return p, fmt.Errorf("%w：%s固定端口须为 1–65535", errPortSettings, endpoint.name)
			}
			if name, exists := fixed[option.Port]; exists {
				return p, fmt.Errorf("%w：%s与%s固定端口不能相同", errPortSettings, name, endpoint.name)
			}
			fixed[option.Port] = endpoint.name
		default:
			return p, fmt.Errorf("%w：%s模式仅支持 auto / fixed", errPortSettings, endpoint.name)
		}
	}
	return p, nil
}

func (a *Application) configuredPortsLocked() PortSettings {
	p := PortSettings{}
	if a.config != nil {
		p = a.config.Ports
	}
	p, _ = normalizePortSettings(p)
	return p
}

// Capture lazily as well for embedded/isolated applications that do not use main.
// Caller holds a.mu. Returned structs are values; pending saves cannot mutate them.
func (a *Application) activePortsLocked() PortSettings {
	if a.activePorts == nil {
		p := a.configuredPortsLocked()
		a.activePorts = &p
	}
	return *a.activePorts
}

func fixedPortSet(p PortSettings) map[int]bool {
	result := map[int]bool{}
	for _, option := range []PortOption{p.Proxy, p.Backend, p.Panel, p.Web} {
		if option.Mode == "fixed" {
			result[option.Port] = true
		}
	}
	return result
}

// Fixed choices are not probed during YAML validation: validation must also work
// while the current core owns them. startCore checks ownership and real binding
// failures; neither path substitutes a random port for a fixed choice.
func selectRuntimePort(option PortOption, preferred int, excluded map[int]bool) (int, error) {
	if option.Mode == "fixed" {
		return option.Port, nil
	}
	for attempt := 0; attempt < 32; attempt++ {
		candidate := preferred
		if excluded[candidate] {
			candidate = 0
		}
		port, err := availablePort(candidate)
		if err != nil {
			return 0, err
		}
		if !excluded[port] {
			return port, nil
		}
		preferred = 0
	}
	return 0, errors.New("无法选择互不冲突的本地自动端口")
}

func (a *Application) runtimePortSnapshot() (PortSettings, int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	p := a.activePortsLocked()
	web := a.activeWebPort
	if web == 0 && a.config != nil {
		_, port, err := net.SplitHostPort(a.config.Listen)
		if err == nil {
			web, _ = strconv.Atoi(port)
		}
	}
	return p, web
}

func (a *Application) listenWeb() (net.Listener, error) {
	p, _ := a.runtimePortSnapshot()
	preferred := 17653
	if p.Web.Mode == "fixed" {
		preferred = p.Web.Port
	}
	excluded := fixedPortSet(p)
	if p.Web.Mode == "auto" && excluded[preferred] {
		preferred = 0
	}
	ln, err := net.Listen("tcp4", loopbackAddr(preferred))
	if err != nil && p.Web.Mode == "auto" {
		ln, err = net.Listen("tcp4", loopbackAddr(0))
	}
	if err != nil {
		return nil, fmt.Errorf("启动器 Web 端口 %s 无法监听（已被占用或不可用）；固定模式不会自动回退：%w", loopbackAddr(preferred), err)
	}
	// An ephemeral assignment can coincide with a not-yet-bound fixed core port.
	for attempts := 0; p.Web.Mode == "auto" && excluded[ln.Addr().(*net.TCPAddr).Port]; attempts++ {
		_ = ln.Close()
		if attempts >= 32 {
			return nil, errors.New("无法选择无冲突的启动器 Web 端口")
		}
		ln, err = net.Listen("tcp4", loopbackAddr(0))
		if err != nil {
			return nil, err
		}
	}
	a.mu.Lock()
	a.activeWebPort = ln.Addr().(*net.TCPAddr).Port
	a.config.Listen = loopbackAddr(a.activeWebPort)
	a.mu.Unlock()
	return ln, nil
}
