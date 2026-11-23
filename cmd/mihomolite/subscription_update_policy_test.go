package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func updatePolicyFixture(mode, ua string) *subscriptionUpdatePolicy {
	return &subscriptionUpdatePolicy{Mode: mode, UserAgent: ua, Conditional: true, FailureBackoff: true}
}

func updatePolicyProxyFixture(t *testing.T, a *Application, proxy *httptest.Server) {
	t.Helper()
	_, rawPort, err := net.SplitHostPort(strings.TrimPrefix(proxy.URL, "http://"))
	if err != nil {
		t.Fatal("invalid isolated proxy address")
	}
	port, err := strconv.Atoi(rawPort)
	if err != nil {
		t.Fatal("invalid isolated proxy port")
	}
	a.runtime.MixedPort = port
	a.corePID = os.Getpid()
	a.core = &coreSession{cmd: &exec.Cmd{Process: &os.Process{Pid: a.corePID}}, pid: a.corePID, ready: true, done: make(chan struct{})}
	// This is an in-memory ownership fixture only; it must never be started,
	// waited or killed. Its network endpoint is exclusively httptest loopback.
	t.Cleanup(func() { a.core = nil; a.corePID = 0 })
}

func updatePolicySavedFixture(t *testing.T) (*Application, Profile) {
	t.Helper()
	a := testApplication(t)
	p := Profile{ID: strings.Repeat("a", 64), Name: "Synthetic", Source: "subscription", URL: "https://synthetic.invalid/subscription", CreatedAt: "2026-11-22T00:00:00Z", UpdatedAt: "2026-11-22T00:00:00Z", Size: int64(len(testProfileYAML))}
	if err := atomicWrite(a.profileContentPath(p.ID), []byte(testProfileYAML), 0o600); err != nil {
		t.Fatal("cannot create isolated YAML")
	}
	if err := a.saveProfileStore(profileStore{ActiveID: p.ID, Profiles: []Profile{p}}); err != nil {
		t.Fatal("cannot create isolated store")
	}
	return a, p
}

func updatePolicyCacheFixture(address, userAgent string, content []byte) *subscriptionDownloadCache {
	return &subscriptionDownloadCache{URLSHA256: subscriptionResourceHash(address), ResourceSHA256: subscriptionResourceHash(address), UserAgentSHA256: sha256Hex([]byte(userAgent)), ContentSHA256: sha256Hex(content), ETag: `"synthetic-private-validator"`, LastModified: "Sun, 22 Nov 2026 00:00:00 GMT"}
}

func TestSubscriptionUpdatePolicyDefaultsAndValidation(t *testing.T) {
	policy := effectiveSubscriptionUpdatePolicy(nil)
	if policy.Mode != "auto" || policy.UserAgent != defaultSubscriptionUserAgent || !policy.Conditional || !policy.FailureBackoff {
		t.Fatal("legacy nil policy lost safe defaults")
	}
	for _, ua := range []string{"agent/v1", "", strings.Repeat("x", 512)} {
		for _, mode := range []string{"auto", "direct", "proxy"} {
			if !validSubscriptionUpdatePolicy(updatePolicyFixture(mode, ua)) {
				t.Fatal("valid policy rejected")
			}
		}
	}
	for _, p := range []*subscriptionUpdatePolicy{updatePolicyFixture("", "ok"), updatePolicyFixture("system", "ok"), updatePolicyFixture("auto", "injected\r\nX-Test: private"), updatePolicyFixture("auto", "\r\n"), updatePolicyFixture("auto", "bad\x00"), updatePolicyFixture("auto", "bad\tvalue"), updatePolicyFixture("auto", "非ASCII"), updatePolicyFixture("auto", strings.Repeat("x", 513))} {
		if _, err := normalizeSubscriptionUpdatePolicy(p); err == nil {
			t.Fatal("unsafe policy was accepted")
		}
	}
	at := time.Now().UTC()
	for _, retry := range []*subscriptionUpdateRetry{{ConsecutiveFailures: 0, LastFailureAt: at, RetryAt: at.Add(time.Minute)}, {ConsecutiveFailures: 65, LastFailureAt: at, RetryAt: at.Add(time.Hour)}, {ConsecutiveFailures: 1, LastFailureAt: at, RetryAt: at}, {ConsecutiveFailures: 1, LastFailureAt: at, RetryAt: at.Add(2 * time.Hour)}, {ConsecutiveFailures: 1, RetryAt: at}} {
		if validSubscriptionUpdateRetry(retry) {
			t.Fatal("malformed retry accepted")
		}
	}
	if subscriptionDownloadBudget > 45*time.Second || subscriptionRouteBudget > 20*time.Second {
		t.Fatal("download budgets expanded")
	}
}

func TestSubscriptionUpdatePolicyStoreCompatibilityAndSanitize(t *testing.T) {
	a, p := updatePolicySavedFixture(t)
	store, err := a.loadProfileStore()
	if err != nil || store.Profiles[0].UpdatePolicy != nil || store.Profiles[0].UpdateRetry != nil || store.Profiles[0].AutoUpdateHours != 0 {
		t.Fatal("old store lost compatibility or auto-update was enabled")
	}
	at := time.Now().UTC()
	p.UpdatePolicy = updatePolicyFixture("proxy", "fixture/v2")
	p.AutoUpdateHours = 6
	p.UpdateRetry = &subscriptionUpdateRetry{ConsecutiveFailures: 2, LastFailureAt: at, RetryAt: at.Add(15 * time.Minute)}
	p.NextUpdateAt = p.UpdateRetry.RetryAt.Format(time.RFC3339Nano)
	p.DownloadCache = updatePolicyCacheFixture(p.URL, "fixture/v2", []byte(testProfileYAML))
	if err := a.saveProfileStore(profileStore{ActiveID: p.ID, Profiles: []Profile{p}}); err != nil {
		t.Fatal("valid new metadata was not saved")
	}
	store, err = a.loadProfileStore()
	if err != nil || store.Profiles[0].UpdatePolicy.UserAgent != "fixture/v2" || store.Profiles[0].UpdateRetry.ConsecutiveFailures != 2 || !validSubscriptionProfileMetadata(store.Profiles[0]) {
		t.Fatal("new metadata did not survive reload")
	}
	response, err := a.profileResponse()
	raw, _ := json.Marshal(response)
	if err != nil || strings.Contains(string(raw), "synthetic-private-validator") || strings.Contains(string(raw), "downloadCache") {
		t.Fatal("profile response leaked private validators")
	}
	for _, tc := range []struct {
		name     string
		key      string
		value    any
		disabled bool
	}{
		{"policy type", "updatePolicy", "synthetic-private", false},
		{"policy mode", "updatePolicy", map[string]any{"mode": "invalid", "conditional": true, "failureBackoff": true}, false},
		{"policy UA", "updatePolicy", map[string]any{"mode": "auto", "userAgent": "synthetic-private\r\n"}, false},
		{"retry time", "updateRetry", map[string]any{"consecutiveFailures": 1, "lastFailureAt": "synthetic-private", "retryAt": "synthetic-private"}, false},
		{"retry count", "updateRetry", map[string]any{"consecutiveFailures": -100, "lastFailureAt": at, "retryAt": at.Add(time.Minute)}, false},
		{"cache type", "downloadCache", "synthetic-private", false},
		{"cache validator", "downloadCache", map[string]any{"urlSHA256": strings.Repeat("a", 64), "userAgentSHA256": strings.Repeat("b", 64), "resourceSHA256": strings.Repeat("c", 64), "contentSHA256": strings.Repeat("d", 64), "etag": "synthetic-private\r\n"}, false},
		{"frequency", "autoUpdateHours", 1, true},
		{"due time", "nextUpdateAt", "synthetic-private", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, original := updatePolicySavedFixture(t)
			blob, _ := json.Marshal(original)
			var fields map[string]any
			if json.Unmarshal(blob, &fields) != nil {
				t.Fatal("invalid test fields")
			}
			fields["autoUpdateHours"], fields["nextUpdateAt"] = 6, at.Add(6*time.Hour).Format(time.RFC3339Nano)
			fields[tc.key] = tc.value
			encoded, _ := json.Marshal(map[string]any{"activeId": original.ID, "profiles": []any{fields}})
			if atomicWrite(a.profileStorePath(), encoded, 0o600) != nil {
				t.Fatal("cannot seed malformed metadata")
			}
			loaded, err := a.loadProfileStore()
			if err != nil {
				t.Fatal("optional malformed metadata rejected usable saved content")
			}
			got := loaded.Profiles[0]
			if got.UpdatePolicy != nil || got.UpdateRetry != nil || got.DownloadCache != nil || (tc.disabled && (got.AutoUpdateHours != 0 || len(a.dueSubscriptions(at.Add(10*time.Hour))) != 0)) {
				t.Fatal("malformed metadata was not safely downgraded")
			}
		})
	}
	p.UpdateRetry = nil
	p.DownloadCache.ETag = "synthetic-private\r\n"
	if _, err := encodeProfileStore(profileStore{Profiles: []Profile{p}}); err == nil || strings.Contains(err.Error(), "synthetic-private") {
		t.Fatal("unsafe metadata encode accepted or leaked validators")
	}
}

func TestSubscriptionUpdatePolicyRouting(t *testing.T) {
	for _, tc := range []struct {
		name, mode            string
		proxyStatus           int
		proxyPresent          bool
		wantProxy, wantDirect int32
		wantError             bool
	}{
		{"auto proxy first", "auto", 200, true, 1, 0, false},
		{"auto recoverable fallback", "auto", 503, true, 1, 1, false},
		{"explicit proxy never direct", "proxy", 503, true, 1, 0, true},
		{"explicit direct ignores proxy", "direct", 200, true, 0, 1, false},
		{"auto without proxy", "auto", 503, false, 0, 1, false},
		{"proxy unavailable", "proxy", 200, false, 0, 0, true},
		{"empty mode uses stored proxy", "", 200, true, 1, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var directCalls, proxyCalls atomic.Int32
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				directCalls.Add(1)
				if r.UserAgent() != "fixture/v2" {
					t.Error("custom UA lost on direct route")
				}
				_, _ = w.Write([]byte(testProfileYAML))
			}))
			defer origin.Close()
			proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				proxyCalls.Add(1)
				if r.UserAgent() != "fixture/v2" {
					t.Error("custom UA lost on proxy route")
				}
				w.WriteHeader(tc.proxyStatus)
				if tc.proxyStatus == 200 {
					_, _ = w.Write([]byte(testProfileYAML))
				}
			}))
			defer proxy.Close()
			a := testApplication(t)
			if tc.proxyPresent {
				updatePolicyProxyFixture(t, a, proxy)
			}
			_, err := a.downloadSubscriptionWithPolicy(context.Background(), origin.URL, tc.mode, updatePolicyFixture("proxy", "fixture/v2"), nil, nil)
			if (err != nil) != tc.wantError || proxyCalls.Load() != tc.wantProxy || directCalls.Load() != tc.wantDirect {
				t.Fatal("route selection/fallback violated the policy")
			}
		})
	}
}

func TestSubscriptionUpdatePolicyNoFallbackForUnsafeResponses(t *testing.T) {
	for _, status := range []int{401, 403, 404, 429, 501, 505} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			var directCalls, proxyCalls atomic.Int32
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				directCalls.Add(1)
				_, _ = w.Write([]byte(testProfileYAML))
			}))
			defer origin.Close()
			proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				proxyCalls.Add(1)
				w.Header().Set("Retry-After", "99999999999999")
				w.WriteHeader(status)
			}))
			defer proxy.Close()
			a := testApplication(t)
			updatePolicyProxyFixture(t, a, proxy)
			_, err := a.downloadSubscriptionWithPolicy(context.Background(), origin.URL+"/?token=synthetic-private", "auto", nil, nil, nil)
			if err == nil || directCalls.Load() != 0 || proxyCalls.Load() != 1 || strings.Contains(err.Error(), "synthetic-private") {
				t.Fatal("unsafe HTTP status retried directly or leaked address")
			}
		})
	}
	for _, location := range []string{"http://fixture:synthetic@127.0.0.1/unsafe", "ftp://127.0.0.1/unsafe", "%malformed-location"} {
		t.Run("redirect", func(t *testing.T) {
			var directCalls atomic.Int32
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { directCalls.Add(1) }))
			defer origin.Close()
			proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Location", location)
				w.WriteHeader(http.StatusFound)
			}))
			defer proxy.Close()
			a := testApplication(t)
			updatePolicyProxyFixture(t, a, proxy)
			_, err := a.downloadSubscriptionWithPolicy(context.Background(), origin.URL, "auto", nil, nil, nil)
			var fetch *subscriptionFetchError
			if err == nil || !errors.As(err, &fetch) || fetch.Kind != "redirect" || directCalls.Load() != 0 {
				t.Fatal("unsafe redirect was treated as a recoverable network failure")
			}
		})
	}
	for _, oversized := range []bool{false, true} {
		t.Run("invalid body", func(t *testing.T) {
			var directCalls atomic.Int32
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { directCalls.Add(1) }))
			defer origin.Close()
			proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if oversized {
					_, _ = w.Write([]byte(strings.Repeat("x", (4<<20)+1)))
				}
			}))
			defer proxy.Close()
			a := testApplication(t)
			updatePolicyProxyFixture(t, a, proxy)
			_, err := a.downloadSubscriptionWithPolicy(context.Background(), origin.URL, "auto", nil, nil, nil)
			if err == nil || directCalls.Load() != 0 {
				t.Fatal("invalid content triggered automatic rerouting")
			}
		})
	}
}

func TestSubscriptionUpdatePolicyNetworkFallbackBoundedAndCancellation(t *testing.T) {
	t.Run("at most one alternate", func(t *testing.T) {
		var directCalls, proxyCalls atomic.Int32
		origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { directCalls.Add(1); w.WriteHeader(503) }))
		defer origin.Close()
		proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { proxyCalls.Add(1); w.WriteHeader(503) }))
		defer proxy.Close()
		a := testApplication(t)
		updatePolicyProxyFixture(t, a, proxy)
		if _, err := a.downloadSubscriptionWithPolicy(context.Background(), origin.URL, "auto", nil, nil, nil); err == nil || directCalls.Load() != 1 || proxyCalls.Load() != 1 {
			t.Fatal("auto fallback was not bounded to one alternate")
		}
		a.core = nil
		if _, err := a.downloadSubscriptionWithPolicy(context.Background(), origin.URL, "auto", nil, nil, nil); err == nil || directCalls.Load() != 2 || proxyCalls.Load() != 1 {
			t.Fatal("no-proxy auto route looped")
		}
	})
	t.Run("network failure", func(t *testing.T) {
		var directCalls, proxyCalls atomic.Int32
		origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			directCalls.Add(1)
			_, _ = w.Write([]byte(testProfileYAML))
		}))
		defer origin.Close()
		proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			proxyCalls.Add(1)
			connection, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				_ = connection.Close()
			}
		}))
		defer proxy.Close()
		a := testApplication(t)
		updatePolicyProxyFixture(t, a, proxy)
		if _, err := a.downloadSubscriptionWithPolicy(context.Background(), origin.URL, "auto", nil, nil, nil); err != nil || directCalls.Load() != 1 || proxyCalls.Load() != 1 {
			t.Fatal("recoverable connection error did not fallback once")
		}
	})
	for _, deadline := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancel", true: "parent deadline"}[deadline], func(t *testing.T) {
			var directCalls atomic.Int32
			started := make(chan struct{})
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { directCalls.Add(1) }))
			defer origin.Close()
			proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(started); <-r.Context().Done() }))
			defer proxy.Close()
			a := testApplication(t)
			updatePolicyProxyFixture(t, a, proxy)
			var ctx context.Context
			var cancel context.CancelFunc
			if deadline {
				ctx, cancel = context.WithTimeout(context.Background(), 100*time.Millisecond)
			} else {
				ctx, cancel = context.WithCancel(context.Background())
			}
			defer cancel()
			done := make(chan error, 1)
			go func() {
				_, err := a.downloadSubscriptionWithPolicy(ctx, origin.URL, "auto", nil, nil, nil)
				done <- err
			}()
			select {
			case <-started:
			case <-time.After(3 * time.Second):
				t.Fatal("isolated request never started")
			}
			if !deadline {
				cancel()
			}
			select {
			case err := <-done:
				if !errors.Is(err, ctx.Err()) || directCalls.Load() != 0 {
					t.Fatal("context completion triggered fallback")
				}
			case <-time.After(3 * time.Second):
				t.Fatal("cancelled download did not converge")
			}
		})
	}
	t.Run("per request network timeout", func(t *testing.T) {
		var directCalls atomic.Int32
		origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			directCalls.Add(1)
			_, _ = w.Write([]byte(testProfileYAML))
		}))
		defer origin.Close()
		proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
		defer proxy.Close()
		base := http.DefaultTransport
		transport := base.(*http.Transport).Clone()
		transport.ResponseHeaderTimeout = 75 * time.Millisecond
		http.DefaultTransport = transport
		t.Cleanup(func() { http.DefaultTransport = base; transport.CloseIdleConnections() })
		a := testApplication(t)
		updatePolicyProxyFixture(t, a, proxy)
		if _, err := a.downloadSubscriptionWithPolicy(context.Background(), origin.URL, "auto", nil, nil, nil); err != nil || directCalls.Load() != 1 {
			t.Fatal("recoverable timeout did not use the available alternate")
		}
	})
}

func TestSubscriptionUpdatePolicyConditionalBindingAndUnrequested304(t *testing.T) {
	for _, tc := range []struct {
		name        string
		mutate      func(*subscriptionUpdatePolicy, *subscriptionDownloadCache, *string, *[]byte)
		conditional bool
	}{
		{"trusted", func(p *subscriptionUpdatePolicy, c *subscriptionDownloadCache, u *string, b *[]byte) {}, true},
		{"UA changed", func(p *subscriptionUpdatePolicy, c *subscriptionDownloadCache, u *string, b *[]byte) {
			p.UserAgent = "changed/v3"
		}, false},
		{"URL changed", func(p *subscriptionUpdatePolicy, c *subscriptionDownloadCache, u *string, b *[]byte) {
			*u += "/changed"
		}, false},
		{"content changed", func(p *subscriptionUpdatePolicy, c *subscriptionDownloadCache, u *string, b *[]byte) {
			*b = []byte("# locally edited\n" + testProfileYAML)
		}, false},
		{"disabled", func(p *subscriptionUpdatePolicy, c *subscriptionDownloadCache, u *string, b *[]byte) {
			p.Conditional = false
		}, false},
		{"bad cache", func(p *subscriptionUpdatePolicy, c *subscriptionDownloadCache, u *string, b *[]byte) {
			c.ETag = "bad\r\nprivate"
		}, false},
		{"resource changed", func(p *subscriptionUpdatePolicy, c *subscriptionDownloadCache, u *string, b *[]byte) {
			c.ResourceSHA256 = strings.Repeat("c", 64)
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				condition := r.Header.Get("If-None-Match") != "" || r.Header.Get("If-Modified-Since") != ""
				if condition != tc.conditional {
					t.Error("stale validators were sent or valid cache was ignored")
				}
				if condition {
					w.WriteHeader(304)
				} else {
					_, _ = w.Write([]byte(testProfileYAML))
				}
			}))
			defer server.Close()
			policy := updatePolicyFixture("direct", "fixture/v2")
			address, saved := server.URL, []byte(testProfileYAML)
			cache := updatePolicyCacheFixture(address, policy.UserAgent, saved)
			tc.mutate(policy, cache, &address, &saved)
			a := testApplication(t)
			download, err := a.downloadSubscriptionWithPolicy(context.Background(), address, "", policy, cache, saved)
			if err != nil || download.NotModified != tc.conditional || requests.Load() != 1 {
				t.Fatal("conditional cache binding was incorrect")
			}
			if !tc.conditional && download.Cache != nil {
				t.Fatal("200 without validators retained old cache")
			}
		})
	}
	for _, always304 := range []bool{false, true} {
		t.Run(map[bool]string{false: "one unconditional refetch", true: "repeated unsolicited 304 rejected"}[always304], func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := requests.Add(1)
				if r.Header.Get("If-None-Match") != "" || r.Header.Get("If-Modified-Since") != "" {
					t.Error("untrusted validators were sent")
				}
				if n == 1 || always304 {
					w.WriteHeader(304)
				} else {
					_, _ = w.Write([]byte(testProfileYAML))
				}
			}))
			defer server.Close()
			a := testApplication(t)
			download, err := a.downloadSubscriptionWithPolicy(context.Background(), server.URL, "direct", nil, nil, []byte(testProfileYAML))
			if (err != nil) != always304 || download.NotModified || requests.Load() != 2 {
				t.Fatal("unsolicited 304 was accepted or refetched more than once")
			}
		})
	}
}

func TestSubscriptionUpdatePolicyConditionalRedirectScope(t *testing.T) {
	var phase atomic.Int32
	var entryLeaks, otherLeaks atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Referer") != "" {
			t.Error("redirect leaked a subscription referer")
		}
		switch r.URL.Path {
		case "/entry":
			if r.Header.Get("If-None-Match") != "" || r.Header.Get("If-Modified-Since") != "" {
				entryLeaks.Add(1)
			}
			http.Redirect(w, r, "/first?token=synthetic-first", http.StatusFound)
		case "/first":
			if phase.Load() == 2 {
				http.Redirect(w, r, "/other?token=synthetic-other", http.StatusFound)
				return
			}
			if phase.Load() == 1 {
				if r.Header.Get("If-None-Match") == "" {
					t.Error("known final resource missed validators")
				}
				w.WriteHeader(304)
				return
			}
			w.Header().Set("ETag", `"synthetic-private-validator"`)
			_, _ = w.Write([]byte(testProfileYAML))
		case "/other":
			if r.Header.Get("If-None-Match") != "" || r.Header.Get("If-Modified-Since") != "" {
				otherLeaks.Add(1)
			}
			_, _ = w.Write([]byte(testProfileYAML))
		}
	}))
	defer server.Close()
	a := testApplication(t)
	address := server.URL + "/entry?token=synthetic-entry"
	first, err := a.downloadSubscriptionWithPolicy(context.Background(), address, "direct", nil, nil, nil)
	if err != nil {
		t.Fatal("isolated initial redirect failed")
	}
	cache := first.cacheForContent([]byte(testProfileYAML))
	if cache == nil {
		t.Fatal("initial validator cache missing")
	}
	phase.Store(1)
	second, err := a.downloadSubscriptionWithPolicy(context.Background(), address, "direct", nil, cache, []byte(testProfileYAML))
	if err != nil || !second.NotModified {
		t.Fatal("trusted redirected resource did not accept 304")
	}
	phase.Store(2)
	third, err := a.downloadSubscriptionWithPolicy(context.Background(), address, "direct", nil, cache, []byte(testProfileYAML))
	if err != nil || third.NotModified || third.Cache != nil || entryLeaks.Load() != 0 || otherLeaks.Load() != 0 {
		t.Fatal("validators crossed resources or 200 failed to clear cache")
	}
}

func TestSubscriptionUpdatePolicyValidatorParsingConservative(t *testing.T) {
	for _, name := range []string{"ETag", "Last-Modified"} {
		header := http.Header{}
		value := `"synthetic-validator"`
		if name == "Last-Modified" {
			value = "Sun, 22 Nov 2026 00:00:00 GMT"
		}
		header.Add(name, value)
		header.Add(name, value)
		if subscriptionResponseValidator(header, name) != "" {
			t.Fatal("duplicate validators were trusted")
		}
	}
	for _, value := range []string{"unquoted", `"embedded quote"bad"`, "bad\r\n", strings.Repeat("x", 1025)} {
		if validSubscriptionETag(value) {
			t.Fatal("unsafe ETag accepted")
		}
	}
}
func TestSubscriptionUpdatePolicyCacheDirectivesAndDefaultUA(t *testing.T) {
	for _, tc := range []struct {
		name, cacheControl, vary string
		allow                    bool
	}{
		{"normal", "", "", true},
		{"no store", "private, no-store", "", false},
		{"case insensitive", "NO-STORE", "", false},
		{"vary wildcard", "", "Accept, *", false},
		{"vary UA", "no-cache", "User-Agent", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, status := range []int{200, 304} {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.UserAgent() != defaultSubscriptionUserAgent {
						t.Error("legacy default UA changed")
					}
					w.Header().Set("Cache-Control", tc.cacheControl)
					w.Header().Set("Vary", tc.vary)
					w.Header().Set("ETag", `"synthetic-validator"`)
					w.WriteHeader(status)
					if status == 200 {
						_, _ = w.Write([]byte(testProfileYAML))
					}
				}))
				a := testApplication(t)
				cache := updatePolicyCacheFixture(server.URL, defaultSubscriptionUserAgent, []byte(testProfileYAML))
				download, err := a.downloadSubscriptionWithPolicy(context.Background(), server.URL, "direct", nil, cache, []byte(testProfileYAML))
				server.Close()
				if err != nil || download.NotModified != (status == 304) || (download.Cache != nil) != tc.allow {
					t.Fatal("cache directives or default UA were not honored")
				}
			}
		})
	}
}

func TestSubscriptionUpdatePolicyManagedProxyAvailability(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(testProfileYAML)) }))
	defer server.Close()
	for _, tc := range []struct {
		name   string
		change func(*Application)
	}{
		{"not ready", func(a *Application) { a.core.ready = false }},
		{"stopping", func(a *Application) { a.core.stopping = true }},
		{"finished", func(a *Application) { close(a.core.done) }},
		{"missing command", func(a *Application) { a.core.cmd = nil }},
		{"PID mismatch", func(a *Application) { a.corePID++ }},
		{"invalid port", func(a *Application) { a.runtime.MixedPort = 0 }},
		{"exiting", func(a *Application) { a.shuttingDown = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := testApplication(t)
			updatePolicyProxyFixture(t, a, server)
			tc.change(a)
			if a.currentSubscriptionProxy() != nil {
				t.Fatal("unready/unowned proxy session was considered available")
			}
			if _, err := a.downloadSubscriptionWithPolicy(context.Background(), server.URL, "proxy", nil, nil, nil); err == nil {
				t.Fatal("explicit proxy proceeded without a reliable session")
			}
		})
	}
}
