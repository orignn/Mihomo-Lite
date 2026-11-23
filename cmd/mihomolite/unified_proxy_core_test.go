package main

import (
	"context"
	"encoding/json"
	"fmt"
	"gopkg.in/yaml.v3"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func unifiedCoreCachePath(t *testing.T, path string) string {
	t.Helper()
	absolute, err := filepath.Abs(path)
	if err != nil {
		t.Fatal("invalid isolated path")
	}
	relative, err := filepath.Rel(filepath.Join(mustGetwd(t), ".cache", "tests", "unified-proxy"), absolute)
	if err != nil || relative == "." || strings.HasPrefix(relative, "..") {
		t.Fatal("native inputs must stay in dedicated test cache")
	}
	return absolute
}

// Explicit cache-only native evidence, not Application.Start or system proxy.
func TestUnifiedProxyNativeProviderFallback(t *testing.T) {
	core := os.Getenv("MIHOMOLITE_UNIFIED_TEST_CORE")
	if core == "" {
		t.Skip("explicit isolated native core required")
	}
	core = unifiedCoreCachePath(t, core)
	cases := []struct{ name, payload, group, want string }{
		{"loaded-provider", unifiedSyntheticConfig("PROVIDER-NODE", ""), "", "PROVIDER-NODE"},
		{"provider-control-adapters-filtered", "proxies:\n  - {name: LOCAL, type: direct}\n  - {name: PROVIDER-NODE, type: ss, server: 127.0.0.1, port: 9, cipher: aes-128-gcm, password: SYNTHETIC}\n", "", "PROVIDER-NODE"},
		{"initially-empty-provider", "proxies: []\n", "", "REJECT"},
		{"empty-original-selector", "proxies: []\n", "节点选择", "REJECT"},
		{"loaded-original-selector", unifiedSyntheticConfig("PROVIDER-NODE", ""), "节点选择", "PROVIDER-NODE"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			payload := tc.payload
			if index := strings.Index(payload, "rules:"); index >= 0 {
				payload = payload[:index]
			}
			if err := os.WriteFile(filepath.Join(dir, "provider.yaml"), []byte(payload), 0o600); err != nil {
				t.Fatal(err)
			}
			content := "proxy-providers:\n  SYN-P:\n    type: file\n    path: ./provider.yaml\n    health-check: {enable: false}\n"
			if tc.group != "" {
				content += "proxy-groups:\n  - name: " + tc.group + "\n    type: select\n    use: [SYN-P]\n"
			}
			content += "dns: {enable: false}\nmode: rule\nrules:\n  - MATCH,统一代理\n"
			controller, err := availablePort(0)
			if err != nil {
				t.Fatal(err)
			}
			mixed, err := availablePort(0)
			if err != nil {
				t.Fatal(err)
			}
			a := &Application{dataDir: dir, config: &Settings{APISecret: "SYNTHETIC-API-SECRET"}}
			_, rendered, err := a.prepareConfigWithRules([]byte(content), RuntimeConfig{BackendPort: controller, MixedPort: mixed, Secret: "SYNTHETIC-API-SECRET"}, []CustomRule{})
			if err != nil {
				t.Fatal("native fixture generation failed")
			}
			path := filepath.Join(dir, "config.yaml")
			if err := os.WriteFile(path, rendered, 0o600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			command := exec.CommandContext(ctx, core, "-d", dir, "-f", path)
			command.Dir = dir
			command.Stdout = io.Discard
			command.Stderr = io.Discard
			if err := command.Start(); err != nil {
				cancel()
				t.Fatal("isolated core could not start")
			}
			done := make(chan error, 1)
			go func() { done <- command.Wait() }()
			exited := false
			defer func() {
				cancel()
				if !exited {
					select {
					case <-done:
					case <-time.After(5 * time.Second):
						t.Error("isolated core did not stop")
					}
				}
			}()
			client := &http.Client{Timeout: 500 * time.Millisecond, Transport: &http.Transport{Proxy: nil}}
			defer client.CloseIdleConnections()
			deadline := time.Now().Add(10 * time.Second)
			now := ""
			for time.Now().Before(deadline) {
				request, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/proxies", controller), nil)
				request.Header.Set("Authorization", "Bearer SYNTHETIC-API-SECRET")
				response, err := client.Do(request)
				if err == nil {
					var data struct {
						Proxies map[string]struct {
							Now string `json:"now"`
						} `json:"proxies"`
					}
					err = json.NewDecoder(response.Body).Decode(&data)
					response.Body.Close()
					if err == nil {
						now = data.Proxies[unifiedProxyName].Now
						for depth := 0; depth < 64; depth++ {
							next := data.Proxies[now].Now
							if next == "" || next == now {
								break
							}
							now = next
						}
						if now == tc.want {
							break
						}
					}
				}
				select {
				case <-done:
					exited = true
					t.Fatal("isolated core exited before readiness")
				case <-time.After(30 * time.Millisecond):
				}
			}
			if now != tc.want {
				t.Fatalf("native outbound=%q want=%q", now, tc.want)
			}
			if now == "DIRECT" || now == "COMPATIBLE" {
				t.Fatal("empty pool fell back to direct")
			}
		})
	}
}

func TestUnifiedProxyPrivateSubscriptions(t *testing.T) {
	base := os.Getenv("MIHOMOLITE_UNIFIED_PRIVATE_ROOT")
	if base == "" {
		t.Skip("explicit isolated private snapshots required")
	}
	base = unifiedCoreCachePath(t, base)
	a := &Application{baseDir: base, dataDir: filepath.Join(base, "data"), config: &Settings{APISecret: "SYNTHETIC-API-SECRET"}}
	results := make([]map[string]any, 0, 4)
	for _, label := range []string{"fixture-01", "fixture-02", "fixture-03", "fixture-04"} {
		content, err := os.ReadFile(filepath.Join(base, "inputs", label+".private.yaml"))
		if err != nil {
			t.Fatal("private snapshot read failed")
		}
		_, rendered, err := a.prepareConfigWithRules(content, RuntimeConfig{}, []CustomRule{{ID: "synthetic", Type: "DOMAIN", Value: "example.test", Policy: unifiedProxyName, Enabled: true}})
		if err != nil {
			t.Fatalf("%s generation failed; private details omitted", label)
		}
		var data struct {
			Groups []struct {
				Name    string   `yaml:"name"`
				Proxies []string `yaml:"proxies"`
				Use     []string `yaml:"use"`
			} `yaml:"proxy-groups"`
		}
		if yaml.Unmarshal(rendered, &data) != nil {
			t.Fatal("private rendering parse failed")
		}
		count, targets := 0, 0
		for _, g := range data.Groups {
			if g.Name == unifiedProxyName {
				count++
				targets = len(g.Proxies) + len(g.Use)
			}
		}
		if count != 1 || targets == 0 {
			t.Fatalf("%s unified output invalid", label)
		}
		if err := a.testConfig(rendered); err != nil {
			t.Fatalf("%s core -t rejected; private details omitted", label)
		}
		results = append(results, map[string]any{"label": label, "unifiedGroups": count, "targets": targets, "coreTestPassed": true, "privateContentPrinted": false})
	}
	data, _ := json.MarshalIndent(results, "", "  ")
	if err := atomicWrite(filepath.Join(base, "private-results.json"), data, 0o600); err != nil {
		t.Fatal("redacted evidence write failed")
	}
	t.Log("four private snapshots passed generation and isolated -t")
}
