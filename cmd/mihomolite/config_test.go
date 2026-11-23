package main

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestPrepareConfigPreservesNodesAndOverridesLocalEndpoints(t *testing.T) {
	dir := t.TempDir()
	app := &Application{baseDir: dir, dataDir: filepath.Join(dir, "data")}
	input := []byte("log-level: info\nmixed-port: 7890\nexternal-controller: 0.0.0.0:9090\nsecret: original\nallow-lan: true\nproxies:\n  - name: test\n    type: direct\n    udp: true\nrules:\n  - MATCH,test\ntun:\n  enable: true\n")
	runtime, output, err := app.prepareConfig(input)
	if err != nil {
		t.Fatal(err)
	}
	text := string(output)
	for _, expected := range []string{"allow-lan: false", "bind-address: 127.0.0.1", "type: direct", "enable: false"} {
		if !strings.Contains(text, expected) {
			t.Errorf("missing %q in %s", expected, text)
		}
	}
	if strings.Contains(text, "original") || strings.Contains(text, "0.0.0.0:9090") {
		t.Error("untrusted local settings were preserved")
	}
	if strings.Contains(defaultConfig, "log-level: warning") == false {
		t.Error("default config should keep mihomo connection logs concise")
	}
	if strings.Contains(text, "log-level: info") == false {
		t.Error("user-selected mihomo log level should be preserved")
	}
	if runtime.Secret == "" || runtime.MixedPort == 0 || runtime.APIPort == 0 || !strings.Contains(runtime.PanelURL, "hostname=127.0.0.1") {
		t.Errorf("invalid runtime: %+v", runtime)
	}
}

func TestInvalidSubscriptionIsRejected(t *testing.T) {
	app := &Application{baseDir: t.TempDir(), dataDir: t.TempDir()}
	_, _, err := app.prepareConfig([]byte("c3M6Ly9leGFtcGxl"))
	if err == nil {
		t.Fatal("Base64 subscription should be rejected")
	}
}
