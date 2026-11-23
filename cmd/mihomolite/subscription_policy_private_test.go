package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// Opt-in private evidence only. No network fetches, launcher or system proxy.
// Default controlled builds clear MIHOMOLITE_* and skip this test.
func TestSubscriptionPolicyPrivateFixture(t *testing.T) {
	inputPath := os.Getenv("MIHOMOLITE_POLICY_PRIVATE_INPUT")
	baseDir := os.Getenv("MIHOMOLITE_POLICY_ISOLATION_ROOT")
	if inputPath == "" || baseDir == "" {
		t.Skip("private isolated -t evidence requires explicit fixture paths")
	}
	allowed := filepath.Join(mustGetwd(t), ".cache", "tests", "subscription-policy-compat")
	for _, path := range []string{baseDir, inputPath} {
		absolute, err := filepath.Abs(path)
		if err != nil {
			t.Fatal("invalid private fixture path")
		}
		relative, err := filepath.Rel(allowed, absolute)
		if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
			t.Fatal("private fixture paths must remain under the dedicated test cache")
		}
	}
	content, err := os.ReadFile(inputPath)
	if err != nil {
		t.Fatal("could not read private fixture")
	}
	a := &Application{baseDir: baseDir, dataDir: filepath.Join(baseDir, "private-fixture-data"), config: &Settings{APISecret: "synthetic-validation-secret"}}
	// The original -t run can fetch GeoSite for DNS even without GEOSITE rules.
	// Seed all available validation resources from that isolated run, never data/.
	for _, name := range geoCacheFiles {
		source := filepath.Join(baseDir, "original-test", name)
		if fileExists(source) {
			if err := copyFile(source, filepath.Join(a.dataDir, name)); err != nil {
				t.Fatal("could not copy isolated Geo database")
			}
		}
	}
	_, rendered, err := a.prepareConfigWithRules(content, RuntimeConfig{}, []CustomRule{})
	if err != nil {
		t.Fatal("original prepare failed; private details omitted")
	}
	if err := a.testConfig(rendered); err != nil {
		t.Fatal("original core -t failed; private details omitted")
	}

	custom := []CustomRule{
		{ID: "one", Type: "DOMAIN-SUFFIX", Value: "release-assets.githubusercontent.com", Policy: "国外域名", Enabled: true},
		{ID: "two", Type: "DOMAIN-SUFFIX", Value: "synthetic-download.example", Policy: "国外域名", Enabled: true},
	}
	// Recreate the old unguarded overlay as a source fixture. Passing an explicit
	// empty custom list then leaves native -t to verify the original failure.
	var doc yaml.Node
	if err := yaml.Unmarshal(content, &doc); err != nil || len(doc.Content) != 1 {
		t.Fatal("private YAML parse failed")
	}
	root := doc.Content[0]
	original := mappingValue(root, "rules")
	if original == nil || original.Kind != yaml.SequenceNode {
		t.Fatal("private fixture does not have an explicit rule sequence")
	}
	sequence := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	for _, rule := range custom {
		sequence.Content = append(sequence.Content, yamlScalar(rule.Type+","+rule.Value+","+rule.Policy))
	}
	sequence.Content = append(sequence.Content, original.Content...)
	setMapping(root, "rules", sequence)
	legacy, err := yaml.Marshal(&doc)
	if err != nil {
		t.Fatal("private legacy fixture rendering failed")
	}
	_, legacyRendered, err := a.prepareConfigWithRules(legacy, RuntimeConfig{}, []CustomRule{})
	if err != nil {
		t.Fatal("private legacy prepare failed")
	}
	err = a.testConfig(legacyRendered)
	if err == nil || !strings.Contains(err.Error(), "proxy [国外域名] not found") {
		t.Fatal("legacy overlay did not reproduce the missing policy; private details omitted")
	}
	_, _, err = a.prepareConfigWithRules(content, RuntimeConfig{}, custom)
	if err == nil || !strings.Contains(err.Error(), "2 条") || !strings.Contains(err.Error(), "自定义规则") {
		t.Fatal("new preflight did not identify the two stale custom rules")
	}

	var summary struct {
		Proxies []struct {
			Name string `yaml:"name"`
		} `yaml:"proxies"`
		Groups []struct {
			Name string `yaml:"name"`
		} `yaml:"proxy-groups"`
		Rules []string `yaml:"rules"`
	}
	if err := yaml.Unmarshal(content, &summary); err != nil {
		t.Fatal("private summary parse failed")
	}
	digest := sha256.Sum256(content)
	result := map[string]any{
		"originalCoreTestPassed": true, "legacyOverlayMissingPolicyReproduced": true,
		"newLocalPreflightRejectedBeforeCoreTest": true, "affectedCustomRules": len(custom),
		"sourceSHA256": hex.EncodeToString(digest[:]), "nodes": len(summary.Proxies), "groups": len(summary.Groups), "sourceRules": len(summary.Rules),
		"mihomoTestOnly": true, "privateContentPrinted": false, "realProxyTouched": false,
	}
	encoded, _ := json.MarshalIndent(result, "", "  ")
	outputPath := filepath.Join(baseDir, "private-acceptance.json")
	if fileExists(outputPath) {
		t.Fatal("private evidence exists; use a new isolation root")
	}
	if err := atomicWrite(outputPath, encoded, 0o600); err != nil {
		t.Fatal("could not save redacted evidence")
	}
	t.Logf("original -t passed; legacy overlay rejected; new preflight identified 2 rules; nodes=%d groups=%d sourceRules=%d", len(summary.Proxies), len(summary.Groups), len(summary.Rules))
}
