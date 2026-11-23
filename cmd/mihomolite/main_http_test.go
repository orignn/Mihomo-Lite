//go:build windows

package main

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// These tests never start a launcher/core, access the registry, or use a real proxy.
type httpTestProxy struct{}

func (httpTestProxy) Enable(string, string) error           { return nil }
func (httpTestProxy) Disable() error                        { return nil }
func (httpTestProxy) LastError() string                     { return "" }
func (httpTestProxy) Inspect() (proxySnapshot, bool, error) { return proxySnapshot{}, false, nil }

func httpTestApplication(t *testing.T) *Application {
	t.Helper()
	dir := t.TempDir()
	logs := NewLogStore(10)
	a := &Application{
		baseDir: dir, dataDir: filepath.Join(dir, "data"), logsDir: filepath.Join(dir, "logs"),
		config: &Settings{Listen: "127.0.0.1:17653", CloseToTray: true},
		logger: slog.New(logs), logStore: logs, events: NewEventHub(), proxy: httpTestProxy{},
	}
	a.events.current = a.State
	return a
}

func TestHTTPRejectsUntrustedMutations(t *testing.T) {
	endpoints := []struct{ method, path string }{
		{http.MethodPost, "/api/connect"},
		{http.MethodPost, "/api/disconnect"},
		{http.MethodPost, "/api/reconnect"},
		{http.MethodPost, "/api/repair-proxy"},
		{http.MethodPost, "/api/restore-proxy"},
		{http.MethodPost, "/api/recovery/keep-current-proxy"},
		{http.MethodPost, "/api/recovery/stop-leftover-core"},
		{http.MethodPost, "/api/open-proxy-settings"},
		{http.MethodPost, "/api/import"},
		{http.MethodPost, "/api/profiles"},
		{http.MethodPut, "/api/rules"},
		{http.MethodPut, "/api/settings"},
		{http.MethodPatch, "/api/settings"},
		{http.MethodDelete, "/api/settings"},
	}
	requests := []struct{ name, marker, origin string }{
		{name: "missing-marker"},
		{name: "wrong-marker", marker: "0"},
		{name: "cross-origin", marker: "1", origin: "https://example.com"},
		{name: "wrong-port", marker: "1", origin: "http://127.0.0.1:17654"},
		{name: "wrong-scheme", marker: "1", origin: "https://127.0.0.1:17653"},
		{name: "loopback-alias-is-not-same-origin", marker: "1", origin: "http://localhost:17653"},
		{name: "opaque-origin", marker: "1", origin: "null"},
		{name: "malformed-origin", marker: "1", origin: "://"},
		{name: "origin-with-user-info", marker: "1", origin: "http://user@127.0.0.1:17653"},
		{name: "origin-with-path", marker: "1", origin: "http://127.0.0.1:17653/path"},
		{name: "origin-with-query", marker: "1", origin: "http://127.0.0.1:17653?"},
		{name: "origin-with-fragment", marker: "1", origin: "http://127.0.0.1:17653#fragment"},
	}
	for _, endpoint := range endpoints {
		for _, request := range requests {
			t.Run(endpoint.method+endpoint.path+"/"+request.name, func(t *testing.T) {
				a := httpTestApplication(t)
				// A missing guard must fail before it could start any asynchronous operation.
				a.shuttingDown = true
				r := httptest.NewRequest(endpoint.method, "http://127.0.0.1:17653"+endpoint.path, strings.NewReader("{"))
				if request.marker != "" {
					r.Header.Set("X-Mihomo-Lite", request.marker)
				}
				if request.origin != "" {
					r.Header.Set("Origin", request.origin)
				}
				w := httptest.NewRecorder()
				a.Routes().ServeHTTP(w, r)
				if w.Code != http.StatusForbidden {
					t.Fatalf("untrusted request reached handler: got %d, want 403; body=%s", w.Code, w.Body.String())
				}
				if a.State().Busy {
					t.Fatal("rejected request acquired the operation lock")
				}
			})
		}
	}
}

func TestHTTPMutationGuardAllowsTrustedRequests(t *testing.T) {
	for _, tc := range []struct{ name, target, origin string }{
		{"non-browser-without-origin", "http://127.0.0.1:17653", ""},
		{"same-origin", "http://127.0.0.1:17653", "http://127.0.0.1:17653"},
		{"localhost", "http://localhost:17653", "http://localhost:17653"},
		{"hostname-case", "http://localhost:17653", "http://LOCALHOST:17653"},
		{"default-http-port", "http://127.0.0.1:80", "http://127.0.0.1"},
		{"default-port-in-origin", "http://127.0.0.1", "http://127.0.0.1:80"},
		{"same-https-origin", "https://127.0.0.1:17653", "https://127.0.0.1:17653"},
		{"same-ipv6-origin", "http://[::1]:17653", "http://[::1]:17653"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPut, tc.target+"/api/settings", strings.NewReader("{"))
			r.Header.Set("X-Mihomo-Lite", "1")
			if tc.origin != "" {
				r.Header.Set("Origin", tc.origin)
			}
			w := httptest.NewRecorder()
			// Invalid JSON proves the guard allowed dispatch without applying settings.
			(&Application{}).Routes().ServeHTTP(w, r)
			if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "unexpected EOF") {
				t.Fatalf("trusted request was not dispatched: %d %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestHTTPRejectsMultipleOrEmptyOrigins(t *testing.T) {
	for _, origins := range [][]string{
		{""},
		{"http://127.0.0.1:17653", "https://example.com"},
		{"http://127.0.0.1:17653 https://example.com"},
	} {
		r := httptest.NewRequest(http.MethodPut, "http://127.0.0.1:17653/api/settings", strings.NewReader("{"))
		r.Header.Set("X-Mihomo-Lite", "1")
		for _, origin := range origins {
			r.Header.Add("Origin", origin)
		}
		w := httptest.NewRecorder()
		(&Application{}).Routes().ServeHTTP(w, r)
		if w.Code != http.StatusForbidden {
			t.Fatalf("invalid Origin headers %q reached handler: %d", origins, w.Code)
		}
	}
}

func TestHTTPReadOnlyRequestsDoNotRequireMutationHeaders(t *testing.T) {
	for _, tc := range []struct {
		method, path string
		status       int
	}{
		{http.MethodGet, "/", http.StatusOK},
		{http.MethodHead, "/", http.StatusOK},
		{http.MethodOptions, "/api/settings", http.StatusMethodNotAllowed},
	} {
		r := httptest.NewRequest(tc.method, "http://127.0.0.1:17653"+tc.path, nil)
		r.Header.Set("Origin", "https://example.com")
		w := httptest.NewRecorder()
		(&Application{}).Routes().ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Fatalf("%s was changed by mutation guard: %d %s", tc.method, w.Code, w.Body.String())
		}
	}
}

func TestImportInvalidRequestsDoNotLeaveBusy(t *testing.T) {
	for _, tc := range []struct{ name, marker, body string }{
		{"missing-marker", "", "{}"},
		{"malformed-json", "1", "{"},
		{"invalid-field-type", "1", "{\"content\":false}"},
		{"oversized-json", "1", "{\"content\":\"" + strings.Repeat("x", 4<<20) + "\"}"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := httpTestApplication(t)
			r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:17653/api/import", strings.NewReader(tc.body))
			if tc.marker != "" {
				r.Header.Set("X-Mihomo-Lite", tc.marker)
			}
			w := httptest.NewRecorder()
			a.handleImport(w, r)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("invalid import status: %d %s", w.Code, w.Body.String())
			}
			if a.State().Busy {
				t.Fatal("invalid import left the launcher permanently busy")
			}
			if err := a.begin(); err != nil {
				t.Fatalf("next operation remains blocked: %v", err)
			}
			a.finish(nil)
		})
	}
}

func TestImportValidationFailureReleasesBusy(t *testing.T) {
	a := httpTestApplication(t)
	r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:17653/api/import", strings.NewReader("{\"content\":\"\"}"))
	r.Header.Set("X-Mihomo-Lite", "1")
	w := httptest.NewRecorder()
	a.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest || a.State().Busy {
		t.Fatalf("empty configuration did not release busy: %d %s", w.Code, w.Body.String())
	}
}

func TestImportConflictPreservesExistingBusy(t *testing.T) {
	a := httpTestApplication(t)
	a.operation = true
	r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:17653/api/import", strings.NewReader("{\"content\":\"not-used\"}"))
	r.Header.Set("X-Mihomo-Lite", "1")
	w := httptest.NewRecorder()
	a.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusConflict || !a.State().Busy {
		t.Fatalf("rejected import changed an existing operation: %d %s", w.Code, w.Body.String())
	}
}
