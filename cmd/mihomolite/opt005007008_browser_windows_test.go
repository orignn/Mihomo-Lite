//go:build windows

package main

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const opt005007008BrowserGate = "MIHOMOLITE_TEST_OPT005007008_BROWSER"

type optBrowserTransport struct{ calls atomic.Uint32 }

func (tr *optBrowserTransport) RoundTrip(*http.Request) (*http.Response, error) {
	tr.calls.Add(1)
	return nil, errors.New("component network forbidden in isolated browser fixture")
}

// Real Edge, embedded indexUI and local APIs. Validation is a shape-only test
// runner (-t only), not the official core. Component GET hashes synthetic local
// files; check/download responses and progress are SIMULATED, never a real
// update. No startup worker, connect, registry, proxy, install or rollback runs.
func TestOPT005007008Browser(t *testing.T) {
	if os.Getenv(opt005007008BrowserGate) != "1" {
		t.Skip("requires explicit isolated OPT-005/007/008 Edge gate")
	}
	t.Setenv(draftBrowserGate, "1")
	root := draftTestWorkspace(t)
	base := filepath.Join(root, ".cache", "tests", "opt005007008", "browser")
	if err := checkBackupPath(base); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(base, 0700); err != nil {
		t.Fatal(err)
	}
	if err := checkBackupPath(base); err != nil {
		t.Fatal(err)
	}
	runDir, err := os.MkdirTemp(base, time.Now().UTC().Format("20060102T150405Z")+"-")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("isolated OPT-005/007/008 Edge evidence: %s", runDir)
	uiHash := sha256.Sum256([]byte(indexHTML))
	t.Logf("embedded UI SHA-256: %x; shape-only validator; component network responses simulated; no installs", uiHash)
	tmp := filepath.Join(runDir, "tmp")
	if err := os.Mkdir(tmp, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TEMP", tmp)
	t.Setenv("TMP", tmp)
	var nonce [32]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(nil)
	defer server.Close()
	address := server.Listener.Addr().String()
	if !strings.HasPrefix(address, "127.0.0.1:") || strings.HasSuffix(address, ":17653") {
		t.Fatal("fixture requires a private ephemeral IPv4 listener")
	}
	fixture := draftBrowserFixture{
		URL: "http://" + address, Token: hex.EncodeToString(nonce[:]), Version: AppVersion, Edge: findDraftBrowserEdge(t),
		Profiles: []draftBrowserProfile{
			{ID: strings.Repeat("a", 64), Name: "Synthetic Alpha", YAML: draftBrowserYAML("Alpha") + "secret: synthetic-source-secret\n"},
			{ID: strings.Repeat("b", 64), Name: "Synthetic Beta", YAML: draftBrowserYAML("Beta")},
		},
		Rule: CustomRule{ID: "opt005007008-seed", Type: "DOMAIN", Value: "synthetic.browser-fixture.invalid", Policy: "DIRECT", Enabled: true},
	}
	app := seedDraftBrowserApplication(t, runDir, fixture)
	app.config.Listen = address
	app.config.Ports.Web = PortOption{Mode: "fixed", Port: server.Listener.Addr().(*net.TCPAddr).Port}
	app.runtime.Secret = fixture.Token
	installDraftBrowserValidator(t, app.baseDir)
	// Launcher and panel identity are derived from these exclusively synthetic
	// files, not the daily launcher, its CURRENT/release manifest, or its panel.
	launcherBytes := []byte("Synthetic OPT-008 launcher identity; not executable.\n")
	for path, data := range map[string][]byte{
		filepath.Join(app.baseDir, "MihomoLite.exe"):   launcherBytes,
		filepath.Join(app.dataDir, "ui", "index.html"): []byte("<!doctype html><title>Synthetic component fixture</title>"),
	} {
		if err := atomicWrite(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	// No CURRENT/release manifest: this is deliberately an unmanaged fixture,
	// so a simulated prepared download can expose confirmation without
	// relaxing the real controlled-publication installation policy.
	manager, err := newComponentUpdateManager(app.baseDir)
	if err != nil {
		t.Fatal(err)
	}
	transport := &optBrowserTransport{}
	manager.client = &http.Client{Transport: transport}
	// The fixture has no core process. Do not inspect machine-wide processes.
	manager.guard = func(string) error { return nil }
	app.componentUpdates = manager
	var mocks, installs, forbidden, formRequests, policySaves atomic.Uint32
	var settingsFailure atomic.Bool
	immutable := map[string][32]byte{}
	for _, path := range []string{
		app.profileContentPath(fixture.Profiles[0].ID), app.profileContentPath(fixture.Profiles[1].ID),
		filepath.Join(app.dataDir, "source.yaml"), filepath.Join(app.baseDir, "runtime", coreExecutable),
		filepath.Join(app.baseDir, "MihomoLite.exe"), filepath.Join(app.dataDir, "ui", "index.html"),
	} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		immutable[path] = sha256.Sum256(data)
	}
	routes := app.Routes()
	server.Config.ReadHeaderTimeout = 5 * time.Second
	server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != address || r.Header.Get(draftBrowserFixtureHeader) != fixture.Token {
			writeJSON(w, http.StatusForbidden, errorResponse{Error: "private fixture required"})
			return
		}
		switch r.Method + " " + r.URL.Path {
		case "GET /__opt_fixture_metrics":
			_, runtimeErr := os.Stat(filepath.Join(app.dataDir, "runtime.yaml"))
			sourceUnchanged := true
			for path, want := range immutable {
				data, err := os.ReadFile(path)
				if err != nil || sha256.Sum256(data) != want {
					sourceUnchanged = false
				}
			}
			writeJSON(w, 200, map[string]any{"mockComponentRequests": mocks.Load(), "installRequests": installs.Load(), "forbiddenRequests": forbidden.Load(), "networkRequests": transport.calls.Load(), "runtimeFileAbsent": errors.Is(runtimeErr, os.ErrNotExist), "syntheticOnly": true, "sourceUnchanged": sourceUnchanged, "formRequests": formRequests.Load(), "policySaves": policySaves.Load()})
			return
		case "POST /__opt_fixture_subscription_seed":
			// Preserve the older two-local-profile compact-home fixture. Seed only
			// after that acceptance; this edits synthetic metadata, never downloads.
			body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1024))
			var input map[string]json.RawMessage
			if err != nil || json.Unmarshal(body, &input) != nil || input == nil || len(input) != 0 || r.URL.RawQuery != "" {
				forbidden.Add(1)
				writeJSON(w, 403, errorResponse{Error: "metadata seed accepts only an empty object"})
				return
			}
			store, err := app.loadProfileStore()
			if err != nil || len(store.Profiles) != 2 || store.Profiles[0].ID != fixture.Profiles[0].ID || store.Profiles[0].Source != "local" {
				writeJSON(w, 409, errorResponse{Error: "synthetic subscription already seeded or invalid"})
				return
			}
			store.Profiles[0].Source = "subscription"
			store.Profiles[0].URL = "https://example.invalid/opt020-synthetic-subscription"
			// Existing non-default metadata, not a download; no scheduler runs.
			store.Profiles[0].UpdatePolicy = &subscriptionUpdatePolicy{
				Mode: "direct", UserAgent: "Synthetic-OPT020/1.0", Conditional: false, FailureBackoff: false,
			}
			if err := app.saveProfileStore(store); err != nil {
				writeJSON(w, 500, errorResponse{Error: "cannot seed synthetic subscription metadata"})
				return
			}
			writeJSON(w, 200, map[string]bool{"syntheticMetadataOnly": true, "downloaded": false})
			return
		case "POST /__opt_fixture_settings_failure_once":
			settingsFailure.Store(true)
			writeJSON(w, 200, map[string]bool{"simulatedFailure": true})
			return
		case "GET /api/settings":
			// Never call the real GET handler: it syncs Windows auto-start registry.
			writeJSON(w, 200, app.RuntimeSettings())
			return
		case "PUT /api/settings":
			body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4096))
			var obj map[string]json.RawMessage
			if err != nil || json.Unmarshal(body, &obj) != nil || len(obj) != 1 || obj["connectionPolicy"] == nil {
				forbidden.Add(1)
				writeJSON(w, 403, errorResponse{Error: "fixture accepts only connectionPolicy; registry changes forbidden"})
				return
			}
			if settingsFailure.Swap(false) {
				writeJSON(w, 503, errorResponse{Error: "Synthetic settings save failure; input must remain"})
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
		case "POST /api/overrides/form":
			// The real conversion route is pure: no profile writes or runtime/core.
			formRequests.Add(1)
		case "POST /api/profiles":
			// Only policy-only saves for the synthetic subscription reach the real
			// POST handler. All imports, URL edits, updates and switches stay blocked.
			body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4096))
			var input struct {
				Action string                    `json:"action"`
				ID     string                    `json:"id"`
				Name   string                    `json:"name"`
				Mode   string                    `json:"mode"`
				Policy *subscriptionUpdatePolicy `json:"policy"`
			}
			decoder := json.NewDecoder(bytes.NewReader(body))
			decoder.DisallowUnknownFields()
			if err != nil || decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF || r.URL.RawQuery != "" || input.Action != "update-policy" || input.ID != fixture.Profiles[0].ID || input.Name != "" || input.Mode != "" || input.Policy == nil {
				forbidden.Add(1)
				writeJSON(w, 403, errorResponse{Error: "fixture allows only synthetic subscription policy saves; downloads/imports forbidden"})
				return
			}
			policySaves.Add(1)
			r.Body = io.NopCloser(bytes.NewReader(body))
		case "POST /api/components":
			body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1024))
			var input struct {
				Component string `json:"component"`
				Action    string `json:"action"`
				Confirm   bool   `json:"confirm"`
			}
			decoder := json.NewDecoder(bytes.NewReader(body))
			decoder.DisallowUnknownFields()
			if err != nil || decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF || r.URL.RawQuery != "" {
				// These requests cannot pass the real API's pre-operation validation.
				r.Body = io.NopCloser(bytes.NewReader(body))
				routes.ServeHTTP(w, r)
				return
			}
			if input.Action == "install" || input.Action == "rollback" {
				installs.Add(1)
				writeJSON(w, 403, errorResponse{Error: "fixture never installs or rolls back components"})
				return
			}
			if input.Component == "launcher" {
				writeJSON(w, 409, errorResponse{Error: errComponentUnconfigured.Error()})
				return
			}
			if componentRepository(input.Component) == "" || (input.Action != "check" && input.Action != "download") {
				writeJSON(w, 400, errorResponse{Error: "fixture accepts only fixed component check/download"})
				return
			}
			mocks.Add(1)
			if input.Action == "check" {
				manager.setProgress(input.Component, "checking", 0, 0, nil)
				select {
				case <-r.Context().Done():
					return
				case <-time.After(1200 * time.Millisecond):
				}
				manager.mu.Lock()
				view := manager.views[input.Component]
				view.Available = &componentRelease{Component: input.Component, Repository: componentRepository(input.Component), Version: "v99.0.0-fixture", Asset: componentAsset{ID: 1, Name: "synthetic-never-downloaded.zip", Size: 100, Digest: "sha256:" + strings.Repeat("c", 64)}}
				view.CheckedAt = time.Now().UTC().Format(time.RFC3339Nano)
				manager.views[input.Component] = view
				manager.mu.Unlock()
				manager.setProgress(input.Component, "checked", 0, 0, nil)
			} else {
				manager.setProgress(input.Component, "downloading", 25, 100, nil)
				select {
				case <-r.Context().Done():
					return
				case <-time.After(1800 * time.Millisecond):
				}
				manager.mu.Lock()
				manager.prepared[input.Component] = componentDownload{ID: strings.Repeat("d", 32)}
				manager.mu.Unlock()
				manager.setProgress(input.Component, "ready", 100, 100, nil)
			}
			writeJSON(w, 200, map[string]any{"simulatedNetworkResponse": true, "installed": false})
			return
		case "GET /", "GET /favicon.ico", "GET /api/state", "GET /api/profiles", "GET /api/config", "GET /api/rules", "GET /api/events", "GET /api/logs", "GET /api/core-logs", "GET /api/overrides", "POST /api/overrides", "POST /api/config/preview", "GET /api/components":
			// Candidate save/preview use only local synthetic profiles and -t validator.
		default:
			forbidden.Add(1)
			writeJSON(w, 403, errorResponse{Error: "operation forbidden in isolated OPT-005/007/008 fixture"})
			return
		}
		routes.ServeHTTP(w, r)
	})
	server.Start()
	browserDir := filepath.Join(runDir, "browser")
	if err := os.Mkdir(browserDir, 0700); err != nil {
		t.Fatal(err)
	}
	node := os.Getenv("MIHOMOLITE_TEST_NODE")
	if node == "" {
		node, err = exec.LookPath("node.exe")
		if err != nil {
			t.Fatal("isolated Edge requires Node on PATH or MIHOMOLITE_TEST_NODE")
		}
	} else if !filepath.IsAbs(node) {
		t.Fatal("MIHOMOLITE_TEST_NODE must be an absolute executable path")
	}
	t.Cleanup(func() {
		if transport.calls.Load() != 0 || installs.Load() != 0 || forbidden.Load() != 0 {
			t.Errorf("unsafe fixture requests: network=%d installs=%d forbidden=%d", transport.calls.Load(), installs.Load(), forbidden.Load())
		}
		if _, err := os.Stat(filepath.Join(app.dataDir, "runtime.yaml")); !errors.Is(err, os.ErrNotExist) {
			t.Error("candidate UI wrote a runtime configuration")
		}
		for path, want := range immutable {
			data, err := os.ReadFile(path)
			if err != nil || sha256.Sum256(data) != want {
				t.Errorf("synthetic source/component unexpectedly changed: %s", filepath.Base(path))
			}
		}
		if state := app.State(); state.Running || state.ProxyEnabled || state.ConnectionRetry.Active {
			t.Error("fixture unexpectedly started a core/proxy/retry")
		}
	})
	runDraftBrowserNode(t, node, filepath.Join(root, "tests", "browser-opt005007008-smoke.cjs"), browserDir, fixture)
	if _, err := LoadSettings(filepath.Join(app.baseDir, "launcher.json")); err != nil {
		t.Fatal("isolated policy settings cannot be reloaded")
	}
}
