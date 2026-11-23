//go:build windows

package main

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func portFixture(p int) PortSettings {
	value, _ := normalizePortSettings(PortSettings{Proxy: PortOption{Mode: "fixed", Port: p}, Backend: PortOption{Mode: "fixed", Port: p + 1}, Panel: PortOption{Mode: "fixed", Port: p + 2}, Web: PortOption{Mode: "fixed", Port: p + 3}})
	return value
}

func TestPortSettingsValidation(t *testing.T) {
	for _, p := range []int{1, 65535} {
		if _, err := normalizePortSettings(PortSettings{Proxy: PortOption{Mode: "fixed", Port: p}}); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []int{-1, 0, 65536} {
		if _, err := normalizePortSettings(PortSettings{Proxy: PortOption{Mode: "fixed", Port: p}}); err == nil {
			t.Fatal("invalid fixed range accepted")
		}
	}
	for _, option := range []PortOption{{Mode: "other"}, {Mode: "auto", Port: 1}} {
		if _, err := normalizePortSettings(PortSettings{Proxy: option}); err == nil {
			t.Fatal("invalid option accepted")
		}
	}
	for _, pair := range [][2]int{{0, 1}, {0, 2}, {0, 3}, {1, 2}, {1, 3}, {2, 3}} {
		p := PortSettings{}
		options := []*PortOption{&p.Proxy, &p.Backend, &p.Panel, &p.Web}
		*options[pair[0]] = PortOption{Mode: "fixed", Port: 12345}
		*options[pair[1]] = PortOption{Mode: "fixed", Port: 12345}
		if _, err := normalizePortSettings(p); err == nil {
			t.Fatal("cross endpoint conflict accepted")
		}
	}
}

func TestPortSettingsLoadLegacyAndPersist(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "launcher.json")
	fresh, err := LoadSettings(path)
	if err != nil || fresh.Ports.Web.Mode != "auto" {
		t.Fatal("new install default ports invalid")
	}
	for _, listen := range []string{"localhost:23456", "127.0.0.1:23456"} {
		if err := os.WriteFile(path, []byte(fmt.Sprintf(`{"listen":%q,"apiSecret":"SYNTHETIC-PERSIST-SECRET","configName":"synthetic"}`, listen)), 0600); err != nil {
			t.Fatal(err)
		}
		s, err := LoadSettings(path)
		if err != nil || s.Ports.Web != (PortOption{Mode: "fixed", Port: 23456}) || s.Ports.Proxy.Mode != "auto" {
			t.Fatal("legacy listen migration invalid")
		}
		a := &Application{baseDir: dir, config: s}
		ports := portFixture(24000)
		if _, err := a.updateRuntimeSettings(RuntimeSettingsPatch{Ports: &ports}); err != nil {
			t.Fatal(err)
		}
		saved, err := LoadSettings(path)
		if err != nil || saved.Ports != ports || saved.Listen != "127.0.0.1:23456" || saved.APISecret != "SYNTHETIC-PERSIST-SECRET" || saved.ConfigName != "synthetic" {
			t.Fatal("new settings did not persist or changed protected fields")
		}
	}
	for _, body := range []string{
		`{"listen":"0.0.0.0:12345"}`, `{"listen":"[::1]:12345"}`, `{"ports":null}`,
		`{"ports":{"proxy":{"mode":"fixed","port":65536}}}`,
		`{"ports":{"proxy":{"mode":"fixed","port":1},"panel":{"mode":"fixed","port":1}}}`,
	} {
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadSettings(path); err == nil {
			t.Fatal("unsafe settings accepted")
		}
	}
}

func TestPortSettingsSaveStableAndReloadApplies(t *testing.T) {
	a := runtimeSettingsTestApp(t)
	a.config.Ports = portFixture(24100)
	before := a.RuntimeSettings()
	a.runtime = RuntimeConfig{MixedPort: 24100, BackendPort: 24101, APIPort: 24102, Secret: "SYNTHETIC-RUNTIME-SECRET"}
	runtimeBefore := a.runtime
	// A connected session remains stable. Save does not reconnect or touch registry.
	a.core = &coreSession{}
	ports := portFixture(24200)
	saved, err := a.updateRuntimeSettings(RuntimeSettingsPatch{Ports: &ports})
	if err != nil {
		t.Fatal(err)
	}
	if saved.Ports != ports || saved.ActivePorts != before.Ports || !saved.PortsRestartRequired || a.runtime != runtimeBefore || a.config.Listen != "127.0.0.1:17653" {
		t.Fatal("pending settings changed live runtime")
	}
	data, _ := json.Marshal(saved)
	if strings.Contains(string(data), "SYNTHETIC") {
		t.Fatal("settings leaked secret")
	}
	// YAML reapplication keeps the explicit live runtime endpoints.
	_, _, err = a.prepareConfigWithRules([]byte(testProfileYAML), runtimeBefore, []CustomRule{})
	if err != nil {
		t.Fatal(err)
	}
	// Disconnect/reconnect uses the same program snapshot, not pending preferences.
	a.core = nil
	a.runtime = RuntimeConfig{}
	prepared, _, err := a.prepareConfigWithRules([]byte(testProfileYAML), RuntimeConfig{}, []CustomRule{})
	if err != nil || prepared.MixedPort != 24100 || prepared.APIPort != 24102 || prepared.BackendPort != 24101 {
		t.Fatal("reconnect applied pending preferences before restart")
	}
	settings, err := LoadSettings(filepath.Join(a.baseDir, "launcher.json"))
	if err != nil {
		t.Fatal(err)
	}
	restarted := &Application{baseDir: a.baseDir, dataDir: a.dataDir, config: settings}
	prepared, _, err = restarted.prepareConfigWithRules([]byte(testProfileYAML), RuntimeConfig{}, []CustomRule{})
	if err != nil || prepared.MixedPort != 24200 || prepared.APIPort != 24202 || prepared.BackendPort != 24201 {
		t.Fatal("restart failed to apply saved preferences")
	}
	if restarted.RuntimeSettings().PortsRestartRequired {
		t.Fatal("restart still reports pending preferences")
	}
}

func TestPortSettingsWriteFailureAndInvalidPatchPreserveState(t *testing.T) {
	a := runtimeSettingsTestApp(t)
	before := *a.config
	ports := portFixture(24300)
	blocked := filepath.Join(a.baseDir, "blocked")
	if err := os.WriteFile(blocked, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	a.baseDir = blocked
	if _, err := a.updateRuntimeSettings(RuntimeSettingsPatch{Ports: &ports}); err == nil || *a.config != before {
		t.Fatal("write failure changed settings")
	}
	a.baseDir = t.TempDir()
	ports.Proxy.Port = 0
	if _, err := a.updateRuntimeSettings(RuntimeSettingsPatch{Ports: &ports}); err == nil || *a.config != before {
		t.Fatal("invalid preference changed settings")
	}
	if _, err := os.Stat(filepath.Join(a.baseDir, "launcher.json")); !os.IsNotExist(err) {
		t.Fatal("invalid patch wrote settings")
	}
}

func TestPortSettingsWebFixedOccupiedAndAutoFallback(t *testing.T) {
	occupied, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	port := occupied.Addr().(*net.TCPAddr).Port
	a := runtimeSettingsTestApp(t)
	a.config.Ports.Web = PortOption{Mode: "fixed", Port: port}
	ln, err := a.listenWeb()
	if err == nil {
		ln.Close()
		t.Fatal("fixed occupied Web port silently fell back")
	}
	if !strings.Contains(err.Error(), "不会自动回退") {
		t.Fatal("fixed failure not explicit")
	}
	// Verify automatic selection avoids reserved fixed ports even if not bound.
	a = &Application{config: &Settings{Ports: PortSettings{Proxy: PortOption{Mode: "fixed", Port: 17653}}}}
	ln, err = a.listenWeb()
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if ln.Addr().(*net.TCPAddr).Port == 17653 {
		t.Fatal("automatic Web conflicts with fixed proxy")
	}
	if !strings.HasPrefix(ln.Addr().String(), "127.0.0.1:") {
		t.Fatal("Web listener not loopback")
	}
}

func TestPortSettingsRuntimeAutoDistinctAndFixedValidationWhileOccupied(t *testing.T) {
	occupied, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	port := occupied.Addr().(*net.TCPAddr).Port
	a := runtimeSettingsTestApp(t)
	a.config.Ports.Proxy = PortOption{Mode: "fixed", Port: port}
	prepared, rendered, err := a.prepareConfigWithRules([]byte(testProfileYAML), RuntimeConfig{}, []CustomRule{})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.MixedPort != port || prepared.BackendPort == port || prepared.APIPort == port || prepared.BackendPort == prepared.APIPort {
		t.Fatal("runtime endpoints drifted or conflicted")
	}
	text := string(rendered)
	if !strings.Contains(text, "allow-lan: false") || !strings.Contains(text, "bind-address: 127.0.0.1") || !strings.Contains(text, "external-controller: 127.0.0.1:") {
		t.Fatal("runtime endpoint security changed")
	}
	for i := 0; i < 25; i++ {
		ports, _, err := (&Application{}).prepareConfigWithRules([]byte(testProfileYAML), RuntimeConfig{}, []CustomRule{})
		if err != nil || ports.MixedPort == ports.APIPort || ports.MixedPort == ports.BackendPort || ports.APIPort == ports.BackendPort {
			t.Fatal("automatic endpoints conflict")
		}
	}
}

func TestPortSettingsFixedCoreEndpointsOccupiedFailBeforeSpawn(t *testing.T) {
	for _, endpoint := range []string{"proxy", "backend", "panel"} {
		t.Run(endpoint, func(t *testing.T) {
			occupied, err := net.Listen("tcp4", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer occupied.Close()
			port := occupied.Addr().(*net.TCPAddr).Port
			a := newBackupTransactionApp(t)
			// The browser fixture intentionally reports unknown proxy state; OPT-007
			// must reject that before port preparation. This endpoint test instead
			// uses a known-state, memory-only proxy and never touches the registry.
			a.proxy = httpTestProxy{}
			switch endpoint {
			case "proxy":
				a.config.Ports.Proxy = PortOption{Mode: "fixed", Port: port}
			case "backend":
				a.config.Ports.Backend = PortOption{Mode: "fixed", Port: port}
			case "panel":
				a.config.Ports.Panel = PortOption{Mode: "fixed", Port: port}
			}
			// Deliberately non-executable sentinel: a correct occupied-port failure never
			// spawns a core, enables a proxy, or accesses a real registry backend.
			for _, path := range []string{filepath.Join(a.baseDir, "runtime", coreExecutable), filepath.Join(a.dataDir, "ui", "index.html")} {
				if err := atomicWrite(path, []byte("isolated sentinel"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := a.Start(); err == nil || !strings.Contains(err.Error(), "已被占用") {
				t.Fatal("occupied fixed endpoint did not fail before core spawn")
			}
			if a.core != nil || a.runtime != (RuntimeConfig{}) {
				t.Fatal("failed connection retained a live runtime")
			}
		})
	}
}

func TestPortSettingsBackupOptionalCompatibility(t *testing.T) {
	a := newBackupTransactionApp(t)
	a.config.Ports = portFixture(24400)
	if err := a.saveSettings(); err != nil {
		t.Fatal(err)
	}
	data, err := a.exportBackup()
	if err != nil {
		t.Fatal(err)
	}
	doc, err := decodeBackupDocument(data)
	if err != nil || doc.Ports == nil || *doc.Ports != a.config.Ports {
		t.Fatal("backup omitted port preferences")
	}
	if strings.Contains(string(data), a.config.APISecret) {
		t.Fatal("backup leaked launcher secret")
	}
	doc.Profiles = []backupProfile{}
	doc.Rules = []CustomRule{}
	doc.ActiveID = ""
	doc.ExportedAt = time.Now().UTC().Format(time.RFC3339)
	doc.Ports = nil
	doc.Scope = legacyBackupScope
	doc.Checksum = ""
	raw, _ := json.Marshal(doc)
	doc.Checksum = sha256Hex(raw)
	raw, _ = json.Marshal(doc)
	if _, err := decodeBackupDocument(raw); err != nil {
		t.Fatal("historical v1 backup rejected")
	}
	// Empty backup requires no core validation, allowing a completely isolated restore.
	before := a.RuntimeSettings()
	if err := a.importBackup(raw); err != nil {
		t.Fatal(err)
	}
	if a.config.Ports != before.Ports {
		t.Fatal("legacy backup changed port preferences")
	}
	ports := portFixture(24500)
	doc.Ports = &ports
	doc.Scope = backupScope
	doc.Checksum = ""
	raw, _ = json.Marshal(doc)
	doc.Checksum = sha256Hex(raw)
	raw, _ = json.Marshal(doc)
	if err := a.importBackup(raw); err != nil {
		t.Fatal(err)
	}
	after := a.RuntimeSettings()
	if after.Ports != ports || after.ActivePorts != before.ActivePorts || !after.PortsRestartRequired {
		t.Fatal("backup restored live rather than pending port settings")
	}
	ports.Proxy.Port = 0
	doc.Checksum = ""
	raw, _ = json.Marshal(doc)
	doc.Checksum = sha256Hex(raw)
	raw, _ = json.Marshal(doc)
	if _, err := decodeBackupDocument(raw); err == nil {
		t.Fatal("backup accepted invalid ports")
	}
}
