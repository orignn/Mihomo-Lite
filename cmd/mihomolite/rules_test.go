package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func testApplication(t *testing.T) *Application {
	t.Helper()
	dir := t.TempDir()
	return &Application{baseDir: dir, dataDir: filepath.Join(dir, "data")}
}

func TestCustomRulesMergeBeforeOriginal(t *testing.T) {
	a := testApplication(t)
	source := []byte("proxy-groups:\n  - name: 节点选择\n    type: select\n    proxies: [DIRECT]\nrules:\n  - DOMAIN-SUFFIX,example.com,DIRECT\n  - MATCH,DIRECT\n")
	custom := []CustomRule{
		{ID: "one", Type: "DOMAIN-SUFFIX", Value: "example.com", Policy: "节点选择", Enabled: true},
		{ID: "two", Type: "DOMAIN", Value: "disabled.example", Policy: "REJECT", Enabled: false},
		{ID: "three", Type: "IP-CIDR", Value: "10.0.0.0/8", Policy: "DIRECT", Enabled: true, NoResolve: true},
	}
	_, output, err := a.prepareConfigWithRules(source, RuntimeConfig{}, custom)
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Rules []string `yaml:"rules"`
	}
	if err := yaml.Unmarshal(output, &config); err != nil {
		t.Fatal(err)
	}
	expected := []string{"DOMAIN-SUFFIX,example.com,节点选择", "IP-CIDR,10.0.0.0/8,DIRECT,no-resolve", "DOMAIN-SUFFIX,example.com,DIRECT", "MATCH,DIRECT"}
	if len(config.Rules) != len(expected) {
		t.Fatalf("rules: %#v", config.Rules)
	}
	for i := range expected {
		if config.Rules[i] != expected[i] {
			t.Errorf("rule %d = %q, want %q", i, config.Rules[i], expected[i])
		}
	}
}

func TestRulesSurviveSourceImport(t *testing.T) {
	a := testApplication(t)
	custom := []CustomRule{{ID: "one", Type: "DOMAIN", Value: "测试.example", Policy: "DIRECT", Enabled: true}}
	if err := validateCustomRule(&custom[0]); err != nil {
		t.Fatal(err)
	}
	content, _ := json.Marshal(custom)
	if err := atomicWrite(a.customRulesPath(), content, 0o600); err != nil {
		t.Fatal(err)
	}
	_, output, err := a.prepareConfig([]byte("proxies: []\nrules:\n  - MATCH,DIRECT\n"))
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Rules []string `yaml:"rules"`
	}
	if err := yaml.Unmarshal(output, &config); err != nil {
		t.Fatal(err)
	}
	if len(config.Rules) != 2 || !strings.HasPrefix(config.Rules[0], "DOMAIN,xn--") || !strings.HasSuffix(config.Rules[0], ".example,DIRECT") {
		t.Fatalf("saved IDN rule not merged: %#v", config.Rules)
	}
}

func TestValidateCustomRule(t *testing.T) {
	valid := []CustomRule{
		{ID: "a", Type: "domain", Value: "Example.COM", Policy: "DIRECT"},
		{ID: "b", Type: "IP-CIDR", Value: "192.168.1.1/16", Policy: "DIRECT"},
		{ID: "c", Type: "IP-CIDR6", Value: "2001:db8::1/32", Policy: "REJECT"},
		{ID: "d", Type: "PROCESS-NAME", Value: "chrome.exe", Policy: "DIRECT"},
		{ID: "e", Type: "DOMAIN-KEYWORD", Value: "关键词", Policy: "节点选择"},
	}
	for i := range valid {
		if err := validateCustomRule(&valid[i]); err != nil {
			t.Errorf("valid rule %+v: %v", valid[i], err)
		}
	}
	if valid[0].Value != "example.com" || valid[1].Value != "192.168.0.0/16" || valid[2].Value != "2001:db8::/32" {
		t.Errorf("normalization failed: %+v", valid)
	}
	invalid := []CustomRule{
		{ID: "a", Type: "DOMAIN", Value: "https://example.com/a", Policy: "DIRECT"},
		{ID: "b", Type: "DOMAIN", Value: "example.com,evil", Policy: "DIRECT"},
		{ID: "c", Type: "IP-CIDR", Value: "2001:db8::/32", Policy: "DIRECT"},
		{ID: "d", Type: "PROCESS-PATH", Value: "chrome.exe", Policy: "DIRECT"},
		{ID: "e", Type: "MATCH", Value: "", Policy: "DIRECT"},
		{ID: "f", Type: "DOMAIN", Value: "example.com", Policy: "GLOBAL"},
		{Type: "DOMAIN", Value: "example.com", Policy: "DIRECT"},
	}
	for _, rule := range invalid {
		if err := validateCustomRule(&rule); err == nil {
			t.Errorf("invalid rule accepted: %+v", rule)
		}
	}
}

func TestCorruptRulesFileDoesNotPanic(t *testing.T) {
	a := testApplication(t)
	if err := os.MkdirAll(a.dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(a.customRulesPath(), []byte("not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := a.loadCustomRules(); err == nil {
		t.Fatal("corrupt file should return a clear error")
	}
}

func TestMatchCustomRules(t *testing.T) {
	rules := []CustomRule{
		{ID: "1", Type: "DOMAIN-SUFFIX", Value: "example.com", Policy: "DIRECT", Enabled: true},
		{ID: "2", Type: "DOMAIN-KEYWORD", Value: "google", Policy: "REJECT", Enabled: true},
		{ID: "3", Type: "IP-CIDR", Value: "192.168.0.0/16", Policy: "DIRECT", Enabled: true},
		{ID: "4", Type: "PROCESS-NAME", Value: "chrome.exe", Policy: "REJECT", Enabled: true},
		{ID: "5", Type: "DOMAIN-SUFFIX", Value: "example.com", Policy: "REJECT", Enabled: false},
	}
	result, _ := simulateCustomRules(rules, "www.example.com", "destination")
	if got := result.Matched; got == nil || got.ID != "1" {
		t.Fatalf("suffix match = %v", got)
	}
	result, _ = simulateCustomRules(rules, "www.google.com", "destination")
	if got := result.Matched; got == nil || got.ID != "2" {
		t.Fatalf("keyword match = %v", got)
	}
	result, _ = simulateCustomRules(rules, "192.168.1.10", "destination")
	if got := result.Matched; got == nil || got.ID != "3" {
		t.Fatalf("cidr match = %v", got)
	}
	result, _ = simulateCustomRules(rules[3:4], "chrome.exe", "process")
	if got := result.Matched; got == nil || got.ID != "4" {
		t.Fatalf("process match = %v", got)
	}
	result, _ = simulateCustomRules(rules, "example.org", "destination")
	if got := result.Matched; got != nil {
		t.Fatalf("unexpected match = %v", got)
	}
}

func TestSimulateCustomRulesReportsUncertainFirstMatch(t *testing.T) {
	rules := []CustomRule{
		{ID: "geo", Type: "GEOSITE", Value: "category-ads-all", Policy: "REJECT", Enabled: true},
		{ID: "suffix", Type: "DOMAIN-SUFFIX", Value: "example.com", Policy: "DIRECT", Enabled: true},
	}
	result, err := simulateCustomRules(rules, "www.example.com", "destination")
	if err != nil || result.Status != "unknown" || len(result.UncertainTypes) != 1 || result.UncertainTypes[0] != "GEOSITE" || result.PossibleMatch == nil || result.PossibleMatch.ID != "suffix" {
		t.Fatalf("uncertain GeoSite result = %+v, %v", result, err)
	}
	if result.Matched != nil {
		t.Fatalf("unknown first-match result must not claim a definite match: %+v", result)
	}

	rules[0].Enabled = false
	result, err = simulateCustomRules(rules, "www.example.com", "destination")
	if err != nil || result.Status != "matched" || result.Matched == nil || result.Matched.ID != "suffix" {
		t.Fatalf("disabled rule should not block later deterministic match: %+v, %v", result, err)
	}
}

func TestSimulateCustomRulesIPv6ProcessAndBoundaries(t *testing.T) {
	rules := []CustomRule{
		{ID: "v6", Type: "IP-CIDR6", Value: "2001:db8::/32", Policy: "DIRECT", Enabled: true, NoResolve: true},
		{ID: "process", Type: "PROCESS-NAME", Value: "chrome", Policy: "REJECT", Enabled: true},
		{ID: "suffix", Type: "DOMAIN-SUFFIX", Value: "example.com", Policy: "DIRECT", Enabled: true},
		{ID: "domain", Type: "DOMAIN", Value: "example.com", Policy: "REJECT", Enabled: true},
	}
	for _, tc := range []struct{ input, kind, wantStatus, wantID string }{
		{"2001:db8::1", "destination", "matched", "v6"},
		{"192.0.2.1", "destination", "no-match", ""},
		{"Chrome", "process", "matched", "process"},
		{"notexample.com", "destination", "no-match", ""},
		{"a.example.com", "destination", "matched", "suffix"},
		{"example.com", "destination", "matched", "suffix"},
	} {
		testRules := append([]CustomRule(nil), rules...)
		if tc.kind == "process" {
			testRules = testRules[1:2]
		} else {
			testRules[1].Enabled = false
		}
		result, err := simulateCustomRules(testRules, tc.input, tc.kind)
		if err != nil || result.Status != tc.wantStatus || (result.Matched != nil && result.Matched.ID != tc.wantID) || (result.Matched == nil && tc.wantID != "") {
			t.Errorf("%s (%s): result=%+v err=%v", tc.input, tc.kind, result, err)
		}
	}
}

func TestSimulateCustomRulesReportsMissingGeodata(t *testing.T) {
	a := testApplication(t)
	if err := os.MkdirAll(a.dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	rules := []CustomRule{{ID: "geo", Type: "GEOIP", Value: "CN", Policy: "DIRECT", Enabled: true}}
	content, err := json.Marshal(rules)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(a.customRulesPath(), content, 0o600); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/rules/test?q=192.0.2.1&kind=destination", nil)
	res := httptest.NewRecorder()
	a.RuleTest(res, req)
	var result RuleTestResult
	if err := json.Unmarshal(res.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if res.Code != http.StatusOK || result.Status != "unknown" || len(result.MissingDatabases) != 1 || result.MissingDatabases[0] != "GEOIP" {
		t.Fatalf("GeoIP probe response = %d %+v", res.Code, result)
	}
}
