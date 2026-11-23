//go:build windows

package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
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

const optimizationBrowserGate = "MIHOMOLITE_TEST_OPT004006_BROWSER"

func TestOptimizationsRuleAndSubscriptionBrowser(t *testing.T) {
	if os.Getenv(optimizationBrowserGate) != "1" {
		t.Skip("requires explicit isolated OPT-004/006 Edge gate")
	}
	t.Setenv(draftBrowserGate, "1")
	root := mustGetwd(t)
	base := filepath.Join(root, ".cache", "tests", "opt-004-006", "browser")
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
	origin := "http://" + server.Listener.Addr().String()
	fixture := draftBrowserFixture{
		URL: origin, Token: hex.EncodeToString(nonce), Version: AppVersion, Edge: findDraftBrowserEdge(t),
		Profiles: []draftBrowserProfile{
			{ID: strings.Repeat("a", 64), Name: "Synthetic Subscription", YAML: testProfileYAML, Subscription: origin + "/fixture-sub/old?token=" + hex.EncodeToString(nonce), AutoHours: 24},
			{ID: strings.Repeat("b", 64), Name: "Synthetic Local", YAML: testProfileYAML},
			{ID: strings.Repeat("c", 64), Name: "Synthetic Node Info", YAML: strings.ReplaceAll(testProfileYAML, "direct-test", "剩余流量：12.5 GB"), Subscription: origin + "/fixture-sub/nodes?token=" + hex.EncodeToString(nonce)},
		},
		Rule: CustomRule{ID: "opt-domain", Type: "DOMAIN-SUFFIX", Value: "example.com", Policy: "DIRECT", Enabled: true},
	}
	app := seedDraftBrowserApplication(t, runDir, fixture)
	app.config.Listen = server.Listener.Addr().String()
	installDraftBrowserValidator(t, app.baseDir)
	rules := []CustomRule{
		{ID: "opt-geo", Type: "GEOSITE", Value: "cn", Policy: "REJECT", Enabled: true},
		fixture.Rule,
		{ID: "opt-v6", Type: "IP-CIDR6", Value: "2001:db8::/32", Policy: "DIRECT", Enabled: true, NoResolve: true},
		{ID: "opt-process", Type: "PROCESS-NAME", Value: "app.exe", Policy: "REJECT", Enabled: true},
	}
	if err := app.saveCustomRules(context.Background(), rules); err != nil {
		t.Fatal(err)
	}
	store, err := app.loadProfileStore()
	if err != nil {
		t.Fatal(err)
	}
	store.Profiles[0].Traffic = parseSubscriptionUserinfo("upload=1024;download=2048;total=10240;expire=1")
	if err := app.saveProfileStore(store); err != nil {
		t.Fatal(err)
	}
	routes := app.Routes()
	var newRequests, newBodies, conditionalResponses, customUARequests atomic.Uint32
	server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != server.Listener.Addr().String() {
			writeJSON(w, http.StatusForbidden, errorResponse{Error: "fixture host required"})
			return
		}
		if strings.HasPrefix(r.URL.Path, "/fixture-sub/") {
			if r.Method != http.MethodGet || r.URL.Query().Get("token") != fixture.Token {
				writeJSON(w, http.StatusForbidden, errorResponse{Error: "fixture subscription required"})
				return
			}
			switch r.URL.Path {
			case "/fixture-sub/bad":
				_, _ = w.Write([]byte("not: [valid"))
			case "/fixture-sub/new":
				newRequests.Add(1)
				if r.UserAgent() == "MihomoLite-Policy-Fixture/1" {
					customUARequests.Add(1)
				}
				w.Header().Set("ETag", "\"synthetic-fixture-revision-1\"")
				w.Header().Set("Last-Modified", "Sat, 21 Nov 2026 00:00:00 GMT")
				if r.Header.Get("If-None-Match") == "\"synthetic-fixture-revision-1\"" {
					conditionalResponses.Add(1)
					w.WriteHeader(http.StatusNotModified)
					return
				}
				newBodies.Add(1)
				w.Header().Set("Subscription-Userinfo", "upload=2048;download=4096;total=20480;expire=1900000000")
				_, _ = w.Write([]byte(strings.ReplaceAll(testProfileYAML, "direct-test", "updated-fixture-test")))
			case "/fixture-sub/nodes":
				_, _ = w.Write([]byte(strings.ReplaceAll(testProfileYAML, "direct-test", "剩余流量：12.5 GB")))
			case "/fixture-sub/old":
				w.Header().Set("Subscription-Userinfo", "upload=1024;download=2048;total=10240;expire=1")
				_, _ = w.Write([]byte(testProfileYAML))
			default:
				writeJSON(w, http.StatusNotFound, errorResponse{Error: "fixture subscription not found"})
			}
			return
		}
		if r.Header.Get(draftBrowserFixtureHeader) != fixture.Token {
			writeJSON(w, http.StatusForbidden, errorResponse{Error: "private fixture header required"})
			return
		}
		if r.Method == http.MethodGet && r.URL.Path == "/fixture-stats" {
			writeJSON(w, http.StatusOK, map[string]uint32{"requests": newRequests.Load(), "bodies": newBodies.Load(), "notModified": conditionalResponses.Load(), "customUA": customUARequests.Load()})
			return
		}
		if r.Method == http.MethodGet && r.URL.Path == "/api/settings" {
			writeJSON(w, http.StatusOK, map[string]bool{"autoStart": false, "closeToTray": true, "silentStart": false})
			return
		}
		allowed := r.Method == http.MethodGet && (r.URL.Path == "/" || r.URL.Path == "/api/state" || r.URL.Path == "/api/profiles" || r.URL.Path == "/api/rules" || r.URL.Path == "/api/rules/test" || r.URL.Path == "/api/events" || r.URL.Path == "/api/logs" || r.URL.Path == "/api/core-logs")
		if r.Method == http.MethodPut && r.URL.Path == "/api/rules" {
			allowed = true
		}
		if r.Method == http.MethodPost && r.URL.Path == "/api/profiles" {
			allowed = true
		}
		if !allowed {
			writeJSON(w, http.StatusForbidden, errorResponse{Error: "operation disabled in optimization browser fixture"})
			return
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
		t.Fatal("isolated browser smoke requires Node on the test process PATH")
	}
	runDraftBrowserNode(t, node, filepath.Join(root, "tests", "browser-optimizations-smoke.cjs"), browserDir, fixture)
	store, err = app.loadProfileStore()
	if err != nil {
		t.Fatal(err)
	}
	if store.ActiveID != fixture.Profiles[0].ID || store.Profiles[0].URL != origin+"/fixture-sub/new?token="+fixture.Token || store.Profiles[0].Traffic == nil || *store.Profiles[0].Traffic.Total != 20480 || store.Profiles[0].AutoUpdateHours != 6 {
		t.Fatal("browser did not persist edited subscription identity/metadata/schedule")
	}
	if store.Profiles[0].UpdatePolicy == nil || store.Profiles[0].UpdatePolicy.Mode != "auto" || store.Profiles[0].UpdatePolicy.UserAgent != "MihomoLite-Policy-Fixture/1" || !store.Profiles[0].UpdatePolicy.Conditional || !store.Profiles[0].UpdatePolicy.FailureBackoff || store.Profiles[2].Traffic == nil || store.Profiles[2].Traffic.Remaining == nil || *store.Profiles[2].Traffic.Remaining != 13421772800 || store.Profiles[2].Traffic.Source != "nodes" || conditionalResponses.Load() == 0 || customUARequests.Load() == 0 {
		t.Fatal("browser did not persist policy/node metadata or exercise conditional download")
	}
	t.Logf("isolated OPT-004/006 Edge evidence: %s", runDir)
}
