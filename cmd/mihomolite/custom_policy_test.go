package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func customPolicyDocument(t *testing.T, content string) *yaml.Node {
	t.Helper()
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(content), &doc); err != nil || len(doc.Content) != 1 {
		t.Fatal("invalid synthetic policy fixture")
	}
	return doc.Content[0]
}

func TestMissingCustomPolicyPreflight(t *testing.T) {
	root := customPolicyDocument(t, testProfileYAML)
	before, _ := yaml.Marshal(root)
	custom := []CustomRule{
		{ID: "one", Type: "DOMAIN-SUFFIX", Value: "private-fixture.example", Note: "token=NOT-A-REAL-SECRET", Policy: "国外域名", Enabled: true},
		{ID: "two", Type: "DOMAIN", Value: "second-fixture.example", Policy: "国外域名", Enabled: true},
		{ID: "disabled", Type: "DOMAIN", Value: "disabled-fixture.example", Policy: "另一缺失策略", Enabled: false},
	}
	err := mergeCustomRules(root, custom)
	if err == nil {
		t.Fatal("missing custom policy was merged instead of rejected before mihomo validation")
	}
	for _, want := range []string{"2 条", "1 个", "1、2", "自定义规则", "停用", "重新选择", "未自动"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing actionable policy diagnostic %q", want)
		}
	}
	for _, secret := range []string{"private-fixture.example", "token=", "NOT-A-REAL-SECRET", "second-fixture.example", "另一缺失策略"} {
		if strings.Contains(err.Error(), secret) {
			t.Error("diagnostic leaked a rule payload or included an inactive policy")
		}
	}
	after, _ := yaml.Marshal(root)
	if !bytes.Equal(before, after) {
		t.Fatal("rejected policy merge mutated the candidate YAML")
	}
}

func TestCustomPolicyEffectiveYAMLNames(t *testing.T) {
	cases := []struct {
		name, source, policy string
		missing              bool
	}{
		{"flow-group", "proxies: []\nproxy-groups: [{name: 国外域名, type: select, proxies: [DIRECT]}]\nrules:\n  - MATCH,DIRECT\n", "国外域名", false},
		{"direct-proxy-node", testProfileYAML, "direct-test", false},
		{"group-list-alias", "groups: &groups [{name: 国外域名, type: select, proxies: [DIRECT]}]\nproxies: []\nproxy-groups: *groups\nrules: []\n", "国外域名", false},
		{"node-list-alias", "nodes: &nodes [{name: aliased-node, type: direct}]\nproxies: *nodes\nrules: []\n", "aliased-node", false},
		{"group-item-alias", "group: &group {name: 国外域名, type: select, proxies: [DIRECT]}\nproxies: []\nproxy-groups: [*group]\nrules: []\n", "国外域名", false},
		{"scalar-name-alias", "name: &policy 国外域名\nproxies: []\nproxy-groups: [{name: *policy, type: select, proxies: [DIRECT]}]\nrules: []\n", "国外域名", false},
		{"group-merge", "group: &group {name: 国外域名, type: select, proxies: [DIRECT]}\nproxies: []\nproxy-groups: [{<<: *group}]\nrules: []\n", "国外域名", false},
		{"root-merge", "defaults: &defaults {proxies: [], proxy-groups: [{name: 国外域名, type: select, proxies: [DIRECT]}]}\n<<: *defaults\nrules: []\n", "国外域名", false},
		{"explicit-group-override", "defaults: &defaults {proxy-groups: [{name: 国外域名, type: select, proxies: [DIRECT]}]}\n<<: *defaults\nproxies: []\nproxy-groups: [{name: NEW, type: select, proxies: [DIRECT]}]\nrules: []\n", "国外域名", true},
		{"explicit-name-override", "group: &group {name: 国外域名, type: select, proxies: [DIRECT]}\nproxies: []\nproxy-groups: [{<<: *group, name: NEW}]\nrules: []\n", "国外域名", true},
		{"provider-backed-group", "proxy-providers: {SUB: {type: file, path: ./synthetic.yaml}}\nproxy-groups: [{name: 国外域名, type: select, use: [SUB]}]\nrules: []\n", "国外域名", false},
		{"provider-is-not-policy", "proxy-providers: {国外域名: {type: file, path: ./synthetic.yaml}}\nproxy-groups: [{name: NEW, type: select, use: [国外域名]}]\nrules: []\n", "国外域名", true},
		{"names-are-case-sensitive", testProfileYAML, "test", true},
		{"absent-group", testProfileYAML, "国外域名", true},
		{"name-only-in-comment", "# 国外域名\n" + testProfileYAML, "国外域名", true},
		{"exact-name-whitespace", "proxies: []\nproxy-groups: [{name: \" TEST \", type: select, proxies: [DIRECT]}]\nrules: []\n", "TEST", true},
		{"merge-sequence-first-wins", "one: &one {proxy-groups: [{name: 国外域名}]}\ntwo: &two {proxy-groups: [{name: NEW}]}\n<<: [*one, *two]\nproxies: []\nrules: []\n", "国外域名", false},
		{"merge-sequence-reversed", "one: &one {proxy-groups: [{name: 国外域名}]}\ntwo: &two {proxy-groups: [{name: NEW}]}\n<<: [*two, *one]\nproxies: []\nrules: []\n", "国外域名", true},
	}
	for _, special := range []string{"DIRECT", "REJECT", "REJECT-DROP", "PASS", "COMPATIBLE"} {
		cases = append(cases, struct {
			name, source, policy string
			missing              bool
		}{"builtin-" + special, testProfileYAML, special, false})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := customPolicyDocument(t, tc.source)
			custom := []CustomRule{{ID: "synthetic", Type: "DOMAIN", Value: "example.test", Policy: tc.policy, Enabled: true}}
			err := mergeCustomRules(root, custom)
			if (err != nil) != tc.missing {
				t.Fatalf("policy preflight rejected=%t, want %t", err != nil, tc.missing)
			}
		})
	}
}

func TestDisabledMissingCustomPolicyDoesNotBlock(t *testing.T) {
	root := customPolicyDocument(t, testProfileYAML)
	custom := []CustomRule{{ID: "disabled", Type: "DOMAIN", Value: "example.test", Policy: "国外域名", Enabled: false}}
	if err := mergeCustomRules(root, custom); err != nil {
		t.Fatal("inactive custom policy blocked a valid candidate")
	}
	var got struct {
		Rules []string `yaml:"rules"`
	}
	if err := root.Decode(&got); err != nil || len(got.Rules) != 1 || got.Rules[0] != "MATCH,TEST" {
		t.Fatal("inactive rule changed source rule ordering")
	}
}

func TestCustomPolicyDiagnosticHidesUntrustedPolicy(t *testing.T) {
	root := customPolicyDocument(t, testProfileYAML)
	custom := []CustomRule{
		{ID: "one", Type: "DOMAIN", Value: "private-fixture.example", Policy: "https://example.invalid/sub?token=NOT-A-REAL-POLICY-SECRET", Enabled: true},
		{ID: "two", Type: "DOMAIN", Value: "private-fixture.example", Policy: "\x1bCONTROL-POLICY-SECRET", Enabled: true},
	}
	err := mergeCustomRules(root, custom)
	if err == nil || !strings.Contains(err.Error(), "2 条") || !strings.Contains(err.Error(), "2 个") {
		t.Fatal("untrusted policy names were not counted")
	}
	for _, secret := range []string{"https://", "token=", "NOT-A-REAL-POLICY-SECRET", "CONTROL-POLICY-SECRET", "\x1b", "private-fixture.example"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatal("diagnostic exposed a user-controlled policy name or payload")
		}
	}
}

func TestCustomPolicyDecodeErrorsAreRedacted(t *testing.T) {
	for _, source := range []string{
		"proxies: PRIVATE_SENTINEL\nrules: []\n",
		"proxies: [{name: [PRIVATE_SENTINEL]}]\nrules: []\n",
		"proxies: []\nproxy-groups: [{name: A, name: PRIVATE_SENTINEL}]\nrules: []\n",
		"group: &group {name: *group}\nproxies: []\nproxy-groups: [*group]\nrules: []\n",
	} {
		root := customPolicyDocument(t, source)
		err := mergeCustomRules(root, []CustomRule{{ID: "one", Type: "DOMAIN", Value: "example.test", Policy: "DIRECT", Enabled: true}})
		if err == nil || !strings.Contains(err.Error(), "YAML 结构") || strings.Contains(err.Error(), "PRIVATE_SENTINEL") {
			t.Fatal("invalid policy structure did not return a redacted structural diagnostic")
		}
	}
}

func TestCustomPolicyExplicitRuleOverridesIgnoreOldStore(t *testing.T) {
	a := testApplication(t)
	oldRules, _ := json.Marshal([]CustomRule{{ID: "one", Type: "DOMAIN", Value: "example.test", Policy: "国外域名", Enabled: true}})
	if err := atomicWrite(a.customRulesPath(), oldRules, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, override := range [][]CustomRule{{}, {{ID: "two", Type: "DOMAIN", Value: "other.test", Policy: "DIRECT", Enabled: true}}} {
		if _, _, err := a.prepareConfigWithRules([]byte(testProfileYAML), RuntimeConfig{}, override); err != nil {
			t.Fatal("explicit rules override was polluted by saved stale policies")
		}
	}
	if _, _, err := a.prepareConfigWithRules([]byte(testProfileYAML), RuntimeConfig{}, nil); err == nil || !strings.Contains(err.Error(), "自定义规则") {
		t.Fatal("nil override did not retain saved rule semantics")
	}
	stillSaved, _ := os.ReadFile(a.customRulesPath())
	if !bytes.Equal(oldRules, stillSaved) {
		t.Fatal("preflight mutated the global rule store")
	}
	corrupt := []byte("synthetic-corrupt-store")
	if err := atomicWrite(a.customRulesPath(), corrupt, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.prepareConfigWithRules([]byte(testProfileYAML), RuntimeConfig{}, []CustomRule{}); err != nil {
		t.Fatal("explicit empty override still read a corrupt old rule file")
	}
	if _, _, err := a.prepareConfigWithRules([]byte(testProfileYAML), RuntimeConfig{}, nil); err == nil {
		t.Fatal("nil override ignored a corrupt rule store")
	}
	stillSaved, _ = os.ReadFile(a.customRulesPath())
	if !bytes.Equal(corrupt, stillSaved) {
		t.Fatal("diagnostic changed the corrupt synthetic store")
	}
}

func TestCustomPolicyPreflightPreservesComplexSourceRules(t *testing.T) {
	root := customPolicyDocument(t, testProfileYAML)
	original := []string{"RULE-SET,国外域名,DIRECT,no-resolve", "AND,((NETWORK,TCP),(DOMAIN,example.test)),TEST", "OR,((DOMAIN,a.test),(DOMAIN,b.test)),TEST", "SUB-RULE,(NETWORK,TCP),synthetic_sub_rules", "MATCH,TEST"}
	sequence := mappingValue(root, "rules")
	sequence.Content = nil
	for _, value := range original {
		sequence.Content = append(sequence.Content, yamlScalar(value))
	}
	custom := []CustomRule{{ID: "one", Type: "DOMAIN", Value: "custom.test", Policy: "TEST", Enabled: true}}
	if err := mergeCustomRules(root, custom); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Rules []string `yaml:"rules"`
	}
	if err := root.Decode(&got); err != nil || len(got.Rules) != len(original)+1 {
		t.Fatal("source rule sequence changed shape")
	}
	for i, value := range original {
		if got.Rules[i+1] != value {
			t.Fatal("preflight rewrote a native complex source rule")
		}
	}
}

func TestCustomPolicyDiagnosticIsBounded(t *testing.T) {
	root := customPolicyDocument(t, testProfileYAML)
	custom := make([]CustomRule, 1000)
	for i := range custom {
		custom[i] = CustomRule{ID: fmt.Sprint(i), Type: "DOMAIN", Value: "private.test", Policy: fmt.Sprintf("missing-%04d", i), Enabled: true}
	}
	err := mergeCustomRules(root, custom)
	if err == nil || !strings.Contains(err.Error(), "1000 条") || len(err.Error()) > 1600 {
		t.Fatal("missing-policy diagnostic was absent, uncounted or unbounded")
	}
}

func TestCustomPolicyMismatchSwitchPreservesSelection(t *testing.T) {
	a := testApplication(t)
	a.config = &Settings{ConfigName: "old"}
	oldID, newID := strings.Repeat("a", 64), strings.Repeat("b", 64)
	store := profileStore{ActiveID: oldID, Profiles: []Profile{{ID: oldID, Name: "old", Source: "local"}, {ID: newID, Name: "new", Source: "local"}}}
	if err := a.saveProfileStore(store); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{oldID, newID} {
		if err := atomicWrite(a.profileContentPath(id), []byte(testProfileYAML), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	custom, _ := json.Marshal([]CustomRule{{ID: "one", Type: "DOMAIN", Value: "example.test", Policy: "国外域名", Enabled: true}})
	for path, content := range map[string][]byte{
		a.customRulesPath():                       custom,
		filepath.Join(a.dataDir, "source.yaml"):   []byte(testProfileYAML),
		a.pendingProfilePath():                    []byte(oldID),
		filepath.Join(a.baseDir, "launcher.json"): []byte("{\"configName\":\"old\"}"),
	} {
		if err := atomicWrite(path, content, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	paths := []string{a.profileStorePath(), filepath.Join(a.dataDir, "source.yaml"), a.customRulesPath(), a.pendingProfilePath(), filepath.Join(a.baseDir, "launcher.json")}
	before := make(map[string][]byte)
	for _, path := range paths {
		before[path], _ = os.ReadFile(path)
	}
	if err := a.switchProfile(newID); err == nil || !strings.Contains(err.Error(), "自定义规则") {
		t.Fatal("switch with missing custom policy was not rejected before selection/write/reconnect")
	}
	for _, path := range paths {
		after, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(before[path], after) {
			t.Fatal("rejected switch changed stored state")
		}
	}
}
