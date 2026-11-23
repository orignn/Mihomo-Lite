package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func unifiedSyntheticConfig(node, group string) string {
	config := "proxies:\n  - name: " + node + "\n    type: ss\n    server: 127.0.0.1\n    port: 9\n    cipher: aes-128-gcm\n    password: SYNTHETIC-NOT-A-REAL-SECRET\n"
	if group != "" {
		config += "proxy-groups:\n  - name: " + group + "\n    type: select\n    proxies: [" + node + "]\n"
	} else {
		group = node
	}
	return config + "rules:\n  - MATCH," + group + "\n"
}

func unifiedRenderedAlias(t *testing.T, data []byte) map[string]any {
	t.Helper()
	var value struct {
		Groups []map[string]any `yaml:"proxy-groups"`
	}
	if err := yaml.Unmarshal(data, &value); err != nil {
		t.Fatal("rendered fixture parse failed")
	}
	for _, group := range value.Groups {
		if group["name"] == unifiedProxyName {
			return group
		}
	}
	t.Fatal("unified group was not generated")
	return nil
}

func TestUnifiedProxyImportUpdateSwitchEdit(t *testing.T) {
	a := newProfileTestApp(t)
	custom := []CustomRule{{ID: "synthetic-one", Type: "DOMAIN", Value: "example.test", Policy: unifiedProxyName, Enabled: true}}
	storedRules, _ := json.Marshal(custom)
	if err := atomicWrite(a.customRulesPath(), storedRules, 0o600); err != nil {
		t.Fatal(err)
	}
	body := unifiedSyntheticConfig("alpha", "")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) }))
	defer server.Close()
	if err := a.addSubscription(context.Background(), "synthetic-first", server.URL, "direct"); err != nil {
		t.Fatal(err)
	}
	store, _ := a.loadProfileStore()
	firstID := store.ActiveID
	content, _ := os.ReadFile(a.profileContentPath(firstID))
	if string(content) != body || bytes.Contains(content, []byte(unifiedProxyMarker)) {
		t.Fatal("import changed subscription source")
	}
	_, rendered, err := a.prepareConfig(content)
	if err != nil {
		t.Fatal(err)
	}
	alias := unifiedRenderedAlias(t, rendered)
	if targets, ok := alias["proxies"].([]any); !ok || len(targets) != 1 || targets[0] != "alpha" {
		t.Fatal("single-node alias did not target node")
	}
	body = unifiedSyntheticConfig("beta", "🚀 节点选择")
	if err := a.updateSubscription(context.Background(), firstID, "direct"); err != nil {
		t.Fatal(err)
	}
	updated, _ := os.ReadFile(a.profileContentPath(firstID))
	if string(updated) != body {
		t.Fatal("update changed upstream source")
	}
	_, rendered, err = a.prepareConfig(updated)
	if err != nil {
		t.Fatal(err)
	}
	alias = unifiedRenderedAlias(t, rendered)
	if targets, ok := alias["proxies"].([]any); !ok || len(targets) != 1 || targets[0] != "🚀 节点选择" {
		t.Fatal("update did not retarget the unified group")
	}
	body = unifiedSyntheticConfig("gamma", "UnknownBrand")
	if err := a.addSubscription(context.Background(), "synthetic-second", server.URL, "direct"); err != nil {
		t.Fatal(err)
	}
	if err := a.switchProfile(firstID); err != nil {
		t.Fatal(err)
	}
	edited := unifiedSyntheticConfig("delta", "")
	if err := a.editActiveProfile(firstID, edited); err != nil {
		t.Fatal(err)
	}
	persisted, _ := os.ReadFile(a.profileContentPath(firstID))
	if string(persisted) != edited {
		t.Fatal("edit persisted generated fields")
	}
	stillRules, _ := os.ReadFile(a.customRulesPath())
	if !bytes.Equal(storedRules, stillRules) {
		t.Fatal("configuration changes rewrote global rules")
	}
	beforeStore, _ := os.ReadFile(a.profileStorePath())
	conflict := unifiedSyntheticConfig("collision", unifiedProxyName)
	if err := a.addLocalProfile("must-fail", conflict); err == nil {
		t.Fatal("unowned name collision was imported")
	}
	afterStore, _ := os.ReadFile(a.profileStorePath())
	if !bytes.Equal(beforeStore, afterStore) {
		t.Fatal("failed alias generation changed selection")
	}
}

func TestUnifiedProxyShareLinkTemplate(t *testing.T) {
	a := newProfileTestApp(t)
	link := []byte("vless://00000000-0000-4000-8000-000000000001@127.0.0.1:9?security=none&type=tcp#synthetic")
	template := unifiedSyntheticConfig("old", "节点选择") + "# template-only\n"
	template = strings.Replace(template, "  - MATCH,节点选择", "  - DOMAIN,a.test,节点选择\n  - DOMAIN,b.test,节点选择\n  - MATCH,节点选择", 1)
	for _, inputTemplate := range [][]byte{nil, []byte(template)} {
		before := append([]byte(nil), inputTemplate...)
		converted, err := a.normalizeProfileInput(link, inputTemplate)
		if err != nil {
			t.Fatal(err)
		}
		_, rendered, err := a.prepareConfigWithRules(converted, RuntimeConfig{}, []CustomRule{})
		if err != nil {
			t.Fatal(err)
		}
		unifiedRenderedAlias(t, rendered)
		if err := a.testConfig(rendered); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(before, inputTemplate) {
			t.Fatal("share conversion changed input template")
		}
		if bytes.Contains(converted, []byte(unifiedProxyMarker)) {
			t.Fatal("conversion stored a generated ownership marker")
		}
	}
}

func TestUnifiedProxyOfflineRulesExposeStableTarget(t *testing.T) {
	a := testApplication(t)
	a.config = &Settings{}
	recorder := httptest.NewRecorder()
	a.Rules(recorder, httptest.NewRequest(http.MethodGet, "http://127.0.0.1/api/rules", nil))
	if recorder.Code != http.StatusOK {
		t.Fatal("offline rules response failed")
	}
	var result RulesResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.UnifiedPolicy != unifiedProxyName {
		t.Fatal("offline API did not publish stable target")
	}
	found := false
	for _, name := range result.Policies {
		if name == unifiedProxyName {
			found = true
		}
	}
	if !found {
		t.Fatal("offline rule editor cannot select unified target")
	}
}
