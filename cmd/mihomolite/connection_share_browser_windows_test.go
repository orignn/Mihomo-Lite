//go:build windows

package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const connectionShareBrowserGate = "MIHOMOLITE_TEST_OPT009010_BROWSER"

// Exercises the real embedded UI and HTTP routes with synthetic profiles only.
// The validator is shape-only; native protocol validation is covered separately.
// No connect/autostart/registry/proxy route is available to this fixture.
func TestConnectionAndShareBrowser(t *testing.T) {
	if os.Getenv(connectionShareBrowserGate) != "1" {
		t.Skip("requires explicit isolated OPT-009/010 Edge gate")
	}
	t.Setenv(draftBrowserGate, "1")
	root := draftTestWorkspace(t)
	base := filepath.Join(root, ".cache", "tests", "opt-009-010", "browser")
	if err := checkBackupPath(base); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(base, 0o700); err != nil {
		t.Fatal(err)
	}
	runDir, err := os.MkdirTemp(base, time.Now().UTC().Format("20060102T150405Z")+"-")
	if err != nil {
		t.Fatal(err)
	}
	tmp := filepath.Join(runDir, "tmp")
	if err := os.MkdirAll(tmp, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TEMP", tmp)
	t.Setenv("TMP", tmp)
	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(nil)
	fixture := draftBrowserFixture{
		URL: "http://" + server.Listener.Addr().String(), Token: hex.EncodeToString(nonce), Version: AppVersion, Edge: findDraftBrowserEdge(t),
		Profiles: []draftBrowserProfile{{ID: strings.Repeat("a", 64), Name: "Synthetic Original", YAML: testProfileYAML}},
		Rule:     CustomRule{ID: "opt009010-seed", Type: "DOMAIN", Value: "fixture.invalid", Policy: "DIRECT", Enabled: true},
	}
	app := seedDraftBrowserApplication(t, runDir, fixture)
	app.config.Listen = server.Listener.Addr().String()
	app.config.Ports.Web = PortOption{Mode: "fixed", Port: server.Listener.Addr().(*net.TCPAddr).Port}
	app.activePorts = nil
	installDraftBrowserValidator(t, app.baseDir)
	routes := app.Routes()
	server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != server.Listener.Addr().String() || r.Header.Get(draftBrowserFixtureHeader) != fixture.Token {
			writeJSON(w, http.StatusForbidden, errorResponse{Error: "private fixture required"})
			return
		}
		if r.Method == http.MethodGet && r.URL.Path == "/api/settings" {
			writeJSON(w, http.StatusOK, app.RuntimeSettings())
			return
		}
		allowed := r.Method == http.MethodGet && (r.URL.Path == "/" || r.URL.Path == "/api/state" || r.URL.Path == "/api/config" || r.URL.Path == "/api/profiles" || r.URL.Path == "/api/rules" || r.URL.Path == "/api/events" || r.URL.Path == "/api/logs" || r.URL.Path == "/api/core-logs" || r.URL.Path == "/api/settings")
		allowed = allowed || (r.Method == http.MethodPost && (r.URL.Path == "/api/profiles" || r.URL.Path == "/api/import")) || (r.Method == http.MethodPut && r.URL.Path == "/api/settings")
		if !allowed {
			writeJSON(w, http.StatusForbidden, errorResponse{Error: "operation disabled in connection/share browser fixture"})
			return
		}
		if r.Method == http.MethodPut {
			body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4096))
			var obj map[string]json.RawMessage
			if err != nil || json.Unmarshal(body, &obj) != nil || len(obj) != 1 || obj["ports"] == nil {
				writeJSON(w, http.StatusForbidden, errorResponse{Error: "fixture accepts only port settings"})
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
		}
		if r.Method == http.MethodPost && r.URL.Path == "/api/profiles" {
			body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4<<20))
			var req struct{ Action string }
			if err != nil || json.Unmarshal(body, &req) != nil || req.Action != "edit" {
				writeJSON(w, http.StatusForbidden, errorResponse{Error: "fixture accepts only local profile edit"})
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
		}
		routes.ServeHTTP(w, r)
	})
	server.Start()
	defer server.Close()
	browserDir := filepath.Join(runDir, "browser")
	if err := os.MkdirAll(browserDir, 0o700); err != nil {
		t.Fatal(err)
	}
	node, err := exec.LookPath("node.exe")
	if err != nil {
		t.Fatal("isolated Edge test requires Node on PATH")
	}
	runDraftBrowserNode(t, node, filepath.Join(root, "tests", "browser-connection-share-smoke.cjs"), browserDir, fixture)
	if _, err := LoadSettings(filepath.Join(app.baseDir, "launcher.json")); err != nil {
		t.Fatal("persisted connection settings could not be reloaded")
	}
	t.Logf("isolated OPT-009/010 Edge evidence: %s", runDir)
}
