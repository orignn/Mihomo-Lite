//go:build windows

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func updatePolicyFixtureStore(t *testing.T, a *Application, id string, policy *subscriptionUpdatePolicy, cache bool) Profile {
	t.Helper()
	store, err := a.loadProfileStore()
	if err != nil {
		t.Fatal("cannot read isolated fixture")
	}
	p := &store.Profiles[profileIndex(store, id)]
	p.UpdatePolicy = policy
	if cache {
		content, err := os.ReadFile(a.profileContentPath(id))
		if err != nil {
			t.Fatal("cannot read isolated fixture YAML")
		}
		p.DownloadCache = updatePolicyCacheFixture(p.URL, effectiveSubscriptionUpdatePolicy(policy).UserAgent, content)
	}
	if err := a.saveProfileStore(store); err != nil {
		t.Fatal("cannot save fixture policy")
	}
	return *p
}

func updatePolicyNonMetadataFiles(t *testing.T, a *Application, id string) map[string][]byte {
	t.Helper()
	files := subscriptionFixtureFiles(t, a, id)
	delete(files, a.profileStorePath())
	return files
}

func TestSubscriptionUpdatePolicyPolicyOnlyEditAndAtomicFailure(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); _, _ = w.Write([]byte(testProfileYAML)) }))
	defer server.Close()
	a, id := isolatedSubscriptionApp(t, server.URL)
	original := updatePolicyFixtureStore(t, a, id, updatePolicyFixture("direct", "old/v1"), true)
	store, _ := a.loadProfileStore()
	at := time.Now().UTC()
	store.Profiles[0].UpdateRetry = &subscriptionUpdateRetry{ConsecutiveFailures: 2, LastFailureAt: at, RetryAt: at.Add(15 * time.Minute)}
	store.Profiles[0].NextUpdateAt = store.Profiles[0].UpdateRetry.RetryAt.Format(time.RFC3339Nano)
	if a.saveProfileStore(store) != nil {
		t.Fatal("cannot seed retry")
	}
	before := updatePolicyNonMetadataFiles(t, a, id)
	t.Setenv(subscriptionValidatorMode, "fail") // A policy-only edit must not validate YAML.
	policy := updatePolicyFixture("proxy", "new/v2")
	body, _ := json.Marshal(map[string]any{"action": "update-policy", "id": id, "policy": policy})
	w := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/profiles", bytes.NewReader(body))
	request.Header.Set("X-Mihomo-Lite", "1")
	a.Profiles(w, request)
	if w.Code != http.StatusOK || requests.Load() != 0 {
		t.Fatal("policy-only HTTP action downloaded or validated content")
	}
	store, _ = a.loadProfileStore()
	p := store.Profiles[0]
	next, err := time.Parse(time.RFC3339Nano, p.NextUpdateAt)
	if err != nil || !reflect.DeepEqual(p.UpdatePolicy, policy) || p.UpdateRetry != nil || p.DownloadCache != nil || p.UpdatedAt != original.UpdatedAt || next.Before(at.Add(23*time.Hour)) {
		t.Fatal("policy-only edit did not reset retry/cache to the normal cadence")
	}
	assertSubscriptionFilesEqual(t, before)
	beforeAll := subscriptionFixtureFiles(t, a, id)
	path, err := windows.UTF16PtrFromString(a.profileStorePath())
	if err != nil {
		t.Fatal("invalid test path")
	}
	handle, err := windows.CreateFile(path, windows.GENERIC_READ, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal("cannot lock isolated metadata")
	}
	if err := a.setSubscriptionUpdatePolicy(id, updatePolicyFixture("auto", "candidate/v3")); err == nil {
		_ = windows.CloseHandle(handle)
		t.Fatal("locked store accepted policy edit")
	}
	_ = windows.CloseHandle(handle)
	assertSubscriptionFilesEqual(t, beforeAll)
	if err := a.setSubscriptionUpdatePolicy(id, nil); err == nil {
		t.Fatal("missing policy accepted")
	}
}

func TestSubscriptionUpdatePolicyAddCommitsPolicyWithAllFiles(t *testing.T) {
	var ua atomic.Value
	ua.Store("")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ua.Store(r.UserAgent())
		w.Header().Set("ETag", `"candidate-validator"`)
		_, _ = w.Write([]byte(testProfileYAML))
	}))
	defer server.Close()
	for _, target := range []string{"success", ".yaml", "source.yaml", "launcher.json", "profiles.json"} {
		t.Run(target, func(t *testing.T) {
			a, id := isolatedSubscriptionApp(t, server.URL+"/old")
			updatePolicyFixtureStore(t, a, id, updatePolicyFixture("direct", "old/v1"), true)
			before := subscriptionFixtureFiles(t, a, id)
			entriesBefore, _ := os.ReadDir(a.profileDir())
			original := backupReplaceFile
			failed := false
			t.Cleanup(func() { backupReplaceFile = original })
			if target != "success" {
				backupReplaceFile = func(source, destination string) error {
					if !failed && strings.HasSuffix(destination, target) {
						failed = true
						return errors.New("synthetic write fault")
					}
					return original(source, destination)
				}
			}
			policy := updatePolicyFixture("auto", "candidate/v3")
			err := a.addSubscriptionWithPolicy(context.Background(), "Candidate", server.URL+"/new", "auto", policy)
			if target != "success" {
				if err == nil || !failed {
					t.Fatal("add commit fault was not injected/rejected")
				}
				assertSubscriptionFilesEqual(t, before)
				entriesAfter, _ := os.ReadDir(a.profileDir())
				if len(entriesAfter) != len(entriesBefore) {
					t.Fatal("failed add left an orphan YAML")
				}
				return
			}
			store, loadErr := a.loadProfileStore()
			if err != nil || loadErr != nil || len(store.Profiles) != 2 || store.ActiveID == id || ua.Load().(string) != "candidate/v3" {
				t.Fatal("add transaction did not commit/select candidate")
			}
			p := store.Profiles[1]
			if !reflect.DeepEqual(p.UpdatePolicy, policy) || p.AutoUpdateHours != 0 || !trustedSubscriptionCache(p.DownloadCache, p.URL, "candidate/v3", []byte(testProfileYAML)) {
				t.Fatal("candidate policy/cache did not commit with normalized content")
			}
			if a.config.ConfigName != "Candidate" {
				t.Fatal("add did not commit config name")
			}
		})
	}
}

func TestSubscriptionUpdatePolicyEditFailuresKeepOldPolicyAndCache(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") != "" {
			t.Error("URL/UA changed but stale validators were sent")
		}
		w.Header().Set("ETag", `"candidate-validator"`)
		w.Header().Set("Subscription-Userinfo", "total=999")
		_, _ = w.Write([]byte(strings.ReplaceAll(testProfileYAML, "direct-test", "candidate-test")))
	}))
	defer server.Close()
	for _, target := range []string{"validation", ".yaml.bak", ".yaml", ".subscription-diff.json", "source.yaml", "profiles.json"} {
		t.Run(target, func(t *testing.T) {
			a, id := isolatedSubscriptionApp(t, server.URL+"/old")
			updatePolicyFixtureStore(t, a, id, updatePolicyFixture("direct", "old/v1"), true)
			before := subscriptionFixtureFiles(t, a, id)
			original := backupReplaceFile
			failed := false
			t.Cleanup(func() { backupReplaceFile = original })
			if target == "validation" {
				t.Setenv(subscriptionValidatorMode, "fail")
			} else {
				backupReplaceFile = func(source, destination string) error {
					if !failed && strings.HasSuffix(destination, target) {
						failed = true
						return errors.New("synthetic write fault")
					}
					return original(source, destination)
				}
			}
			err := a.updateSubscriptionURLWithPolicy(context.Background(), id, server.URL+"/new", "direct", updatePolicyFixture("auto", "candidate/v3"))
			if err == nil || (target != "validation" && !failed) {
				t.Fatal("edit policy fault was not rejected")
			}
			assertSubscriptionFilesEqual(t, before)
		})
	}
}

func TestSubscriptionUpdatePolicyExplicitOverrideDoesNotPersist(t *testing.T) {
	var directCalls, proxyCalls atomic.Int32
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		directCalls.Add(1)
		if r.UserAgent() != "persisted/v4" {
			t.Error("persisted UA ignored")
		}
		_, _ = w.Write([]byte(testProfileYAML))
	}))
	defer origin.Close()
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyCalls.Add(1)
		_, _ = w.Write([]byte(testProfileYAML))
	}))
	defer proxy.Close()
	a, id := isolatedSubscriptionApp(t, origin.URL)
	policy := updatePolicyFixture("proxy", "persisted/v4")
	updatePolicyFixtureStore(t, a, id, policy, false)
	updatePolicyProxyFixture(t, a, proxy)
	if err := a.updateSubscription(context.Background(), id, "direct"); err != nil {
		t.Fatal("one-shot direct override failed")
	}
	store, _ := a.loadProfileStore()
	if !reflect.DeepEqual(store.Profiles[0].UpdatePolicy, policy) || directCalls.Load() != 1 || proxyCalls.Load() != 0 {
		t.Fatal("one-shot override silently changed persisted policy")
	}
	if err := a.updateSubscription(context.Background(), id, ""); err != nil {
		t.Fatal("persistent proxy update failed")
	}
	if directCalls.Load() != 1 || proxyCalls.Load() != 1 {
		t.Fatal("empty mode did not use saved policy")
	}
}

func TestSubscriptionUpdatePolicy304KeepsContentPendingAndTrafficTime(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Header.Get("If-None-Match") == "" || r.Header.Get("If-Modified-Since") == "" {
			t.Error("trusted validators missing")
		}
		w.WriteHeader(304)
	}))
	defer server.Close()
	a, id := isolatedSubscriptionApp(t, server.URL)
	original := updatePolicyFixtureStore(t, a, id, nil, true)
	store, _ := a.loadProfileStore()
	store.Profiles[0].Traffic.CheckedAt = time.Now().UTC().Add(-3 * time.Hour)
	at := time.Now().UTC()
	store.Profiles[0].UpdateRetry = &subscriptionUpdateRetry{ConsecutiveFailures: 2, LastFailureAt: at, RetryAt: at.Add(15 * time.Minute)}
	store.Profiles[0].NextUpdateAt = store.Profiles[0].UpdateRetry.RetryAt.Format(time.RFC3339Nano)
	oldTraffic := *store.Profiles[0].Traffic
	if a.saveProfileStore(store) != nil || atomicWrite(a.pendingProfilePath(), []byte(id), 0o600) != nil {
		t.Fatal("cannot seed 304 fixture")
	}
	before := updatePolicyNonMetadataFiles(t, a, id)
	beforeTimes := map[string]time.Time{}
	for path := range before {
		if info, err := os.Stat(path); err == nil {
			beforeTimes[path] = info.ModTime()
		}
	}
	originalApply := applySavedSubscription
	var applies atomic.Int32
	applySavedSubscription = func(a *Application, id string) error { applies.Add(1); return errors.New("must not reconnect on 304") }
	t.Cleanup(func() { applySavedSubscription = originalApply })
	if err := a.updateSubscription(context.Background(), id, ""); err != nil {
		t.Fatal("trusted validated 304 failed")
	}
	store, _ = a.loadProfileStore()
	p := store.Profiles[0]
	if requests.Load() != 1 || applies.Load() != 0 || p.LastUpdate.Status != "unchanged" || p.UpdatedAt != original.UpdatedAt || p.Size != original.Size || p.UpdateRetry != nil || !reflect.DeepEqual(p.Traffic, &oldTraffic) {
		t.Fatal("304 changed content state, reconnected, or invented a metadata read time")
	}
	assertSubscriptionFilesEqual(t, before)
	for path, at := range beforeTimes {
		info, err := os.Stat(path)
		if err != nil || !info.ModTime().Equal(at) {
			t.Fatal("304 rewrote a non-metadata file")
		}
	}
	if !reflect.DeepEqual(p.DownloadCache, original.DownloadCache) {
		t.Fatal("304 without validators replaced old trusted cache")
	}
}

func TestSubscriptionUpdatePolicy304ValidMetadataAndValidationFailure(t *testing.T) {
	for _, kind := range []string{"valid metadata", "duplicate metadata", "validation failure", "invalid saved YAML"} {
		t.Run(kind, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("If-None-Match") == "" {
					t.Error("validator missing")
				}
				w.Header().Add("Subscription-Userinfo", "upload=10;download=20;total=500")
				if kind == "duplicate metadata" {
					w.Header().Add("Subscription-Userinfo", "TOTAL=999")
				}
				w.WriteHeader(304)
			}))
			defer server.Close()
			a, id := isolatedSubscriptionApp(t, server.URL)
			if kind == "invalid saved YAML" {
				if atomicWrite(a.profileContentPath(id), []byte("not: [valid"), 0o600) != nil {
					t.Fatal("cannot seed invalid saved YAML")
				}
			}
			old := updatePolicyFixtureStore(t, a, id, updatePolicyFixture("direct", "fixture/v2"), true)
			before := updatePolicyNonMetadataFiles(t, a, id)
			if kind == "validation failure" {
				t.Setenv(subscriptionValidatorMode, "fail")
			}
			err := a.updateSubscription(context.Background(), id, "")
			store, _ := a.loadProfileStore()
			p := store.Profiles[0]
			if kind == "validation failure" || kind == "invalid saved YAML" {
				if err == nil || p.LastUpdate.Status != "failed" || !reflect.DeepEqual(p.DownloadCache, old.DownloadCache) || !reflect.DeepEqual(p.Traffic, old.Traffic) {
					t.Fatal("304 bypassed current config validation or replaced old metadata on failure")
				}
			} else {
				if err != nil || p.Traffic == nil || p.Traffic.Upload == nil || *p.Traffic.Upload != 10 || !p.Traffic.CheckedAt.After(old.Traffic.CheckedAt) {
					t.Fatal("304 valid userinfo was not committed after validation")
				}
				if kind == "duplicate metadata" {
					if p.Traffic.Total != nil {
						t.Fatal("duplicate 304 quota did not remain unknown")
					}
				} else if p.Traffic.Total == nil || *p.Traffic.Total != 500 {
					t.Fatal("valid 304 quota omitted")
				}
			}
			assertSubscriptionFilesEqual(t, before)
		})
	}
}

func TestSubscriptionUpdatePolicyLocalEditUntrusted304AndCacheReset(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Header.Get("If-None-Match") != "" || r.Header.Get("If-Modified-Since") != "" {
			t.Error("locally edited YAML sent stale validators")
		}
		w.WriteHeader(304)
	}))
	defer server.Close()
	a, id := isolatedSubscriptionApp(t, server.URL)
	old := updatePolicyFixtureStore(t, a, id, updatePolicyFixture("direct", "fixture/v2"), true)
	local := []byte("# externally edited\n" + testProfileYAML)
	if atomicWrite(a.profileContentPath(id), local, 0o600) != nil {
		t.Fatal("cannot edit isolated YAML")
	}
	before := updatePolicyNonMetadataFiles(t, a, id)
	if err := a.updateSubscription(context.Background(), id, ""); err == nil || requests.Load() != 2 {
		t.Fatal("locally edited YAML falsely accepted an unsolicited 304")
	}
	store, _ := a.loadProfileStore()
	if !reflect.DeepEqual(store.Profiles[0].DownloadCache, old.DownloadCache) {
		t.Fatal("failed refetch replaced prior cache")
	}
	assertSubscriptionFilesEqual(t, before)
	if err := a.editActiveProfile(id, strings.ReplaceAll(testProfileYAML, "direct-test", "locally-saved")); err != nil {
		t.Fatal("isolated YAML editor save failed")
	}
	store, _ = a.loadProfileStore()
	if store.Profiles[0].DownloadCache != nil {
		t.Fatal("successful local edit did not invalidate validators")
	}
}

func TestSubscriptionUpdatePolicy200ClearsCacheOnlyAfterCommit(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "validation failure"}[fail], func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(testProfileYAML)) }))
			defer server.Close()
			a, id := isolatedSubscriptionApp(t, server.URL)
			old := updatePolicyFixtureStore(t, a, id, updatePolicyFixture("direct", "fixture/v2"), true)
			if fail {
				t.Setenv(subscriptionValidatorMode, "fail")
			}
			err := a.updateSubscription(context.Background(), id, "")
			store, _ := a.loadProfileStore()
			if fail {
				if err == nil || !reflect.DeepEqual(store.Profiles[0].DownloadCache, old.DownloadCache) {
					t.Fatal("rejected 200 cleared old cache")
				}
			} else if err != nil || store.Profiles[0].DownloadCache != nil {
				t.Fatal("200 without validators retained stale cache")
			}
		})
	}
}

func TestSubscriptionUpdatePolicyValidationErrorDoesNotFallback(t *testing.T) {
	var directCalls, proxyCalls atomic.Int32
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		directCalls.Add(1)
		_, _ = w.Write([]byte(testProfileYAML))
	}))
	defer origin.Close()
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { proxyCalls.Add(1); _, _ = w.Write([]byte("not: [valid")) }))
	defer proxy.Close()
	a, id := isolatedSubscriptionApp(t, origin.URL)
	updatePolicyProxyFixture(t, a, proxy)
	before := updatePolicyNonMetadataFiles(t, a, id)
	if err := a.updateSubscription(context.Background(), id, "auto"); err == nil || directCalls.Load() != 0 || proxyCalls.Load() != 1 {
		t.Fatal("content validation failure bypassed policy via direct fallback")
	}
	assertSubscriptionFilesEqual(t, before)
}

func TestSubscriptionUpdatePolicyBackoffRestartManualRetryAndReset(t *testing.T) {
	var status atomic.Int32
	status.Store(503)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(int(status.Load()))
		if status.Load() == 200 {
			_, _ = w.Write([]byte(testProfileYAML))
		}
	}))
	defer server.Close()
	a, id := isolatedSubscriptionApp(t, server.URL)
	for count, want := range []time.Duration{5 * time.Minute, 15 * time.Minute, 30 * time.Minute, time.Hour, time.Hour} {
		if err := a.refreshSubscription(context.Background(), id, "direct", true); err == nil {
			t.Fatal("automatic failure accepted")
		}
		// A new application reload proves durable state, without launching a core.
		restarted := &Application{baseDir: a.baseDir, dataDir: a.dataDir}
		store, err := restarted.loadProfileStore()
		if err != nil {
			t.Fatal("retry state did not survive restart")
		}
		p := store.Profiles[0]
		if p.UpdateRetry == nil || p.UpdateRetry.ConsecutiveFailures != count+1 || p.UpdateRetry.RetryAt.Sub(p.UpdateRetry.LastFailureAt) != want || p.NextUpdateAt != p.UpdateRetry.RetryAt.Format(time.RFC3339Nano) {
			t.Fatal("backoff progression/next time was not durable")
		}
		if len(restarted.dueSubscriptions(p.UpdateRetry.RetryAt.Add(-time.Second))) != 0 || len(restarted.dueSubscriptions(p.UpdateRetry.RetryAt)) != 1 {
			t.Fatal("retry due time was ignored")
		}
	}
	store, _ := a.loadProfileStore()
	oldRetry := *store.Profiles[0].UpdateRetry
	if err := a.updateSubscription(context.Background(), id, "direct"); err == nil {
		t.Fatal("manual failed retry accepted")
	}
	store, _ = a.loadProfileStore()
	if requests.Load() != 6 || !reflect.DeepEqual(store.Profiles[0].UpdateRetry, &oldRetry) {
		t.Fatal("manual update was blocked by backoff or advanced automatic failure count")
	}
	status.Store(200)
	if err := a.updateSubscription(context.Background(), id, ""); err != nil {
		t.Fatal("manual retry was blocked by future retry time")
	}
	store, _ = a.loadProfileStore()
	p := store.Profiles[0]
	next, _ := time.Parse(time.RFC3339Nano, p.NextUpdateAt)
	if p.UpdateRetry != nil || p.LastUpdate.Status != "unchanged" || next.Before(time.Now().Add(23*time.Hour)) {
		t.Fatal("successful manual retry did not restore normal cadence")
	}
	status.Store(503)
	if err := a.refreshSubscription(context.Background(), id, "", true); err == nil {
		t.Fatal("automatic failure accepted")
	}
	zero := 0
	if err := a.setSubscriptionAutoUpdate(id, &zero, time.Now()); err != nil {
		t.Fatal("cannot disable auto update")
	}
	store, _ = a.loadProfileStore()
	if store.Profiles[0].UpdateRetry != nil || store.Profiles[0].NextUpdateAt != "" || len(a.dueSubscriptions(time.Now().Add(10*24*time.Hour))) != 0 {
		t.Fatal("disabled auto update retained retry scheduling")
	}
	policy := updatePolicyFixture("direct", "")
	policy.FailureBackoff = false
	if a.setSubscriptionUpdatePolicy(id, policy) != nil {
		t.Fatal("cannot disable backoff")
	}
	hours := 6
	if a.setSubscriptionAutoUpdate(id, &hours, time.Now()) != nil {
		t.Fatal("cannot restore auto schedule")
	}
	if err := a.refreshSubscription(context.Background(), id, "", true); err == nil {
		t.Fatal("automatic failure accepted")
	}
	store, _ = a.loadProfileStore()
	next, _ = time.Parse(time.RFC3339Nano, store.Profiles[0].NextUpdateAt)
	if store.Profiles[0].UpdateRetry != nil || next.Before(time.Now().Add(5*time.Hour)) {
		t.Fatal("disabled backoff did not use normal schedule")
	}
}

func TestSubscriptionUpdatePolicyMultipleDueFailuresDoNotBlockLater(t *testing.T) {
	var directCalls, proxyCalls atomic.Int32
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		directCalls.Add(1)
		_, _ = w.Write([]byte(testProfileYAML))
	}))
	defer origin.Close()
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { proxyCalls.Add(1); w.WriteHeader(401) }))
	defer proxy.Close()
	a, id := isolatedSubscriptionApp(t, origin.URL)
	updatePolicyProxyFixture(t, a, proxy)
	store, _ := a.loadProfileStore()
	now := time.Now()
	p := &store.Profiles[0]
	p.UpdatePolicy = updatePolicyFixture("proxy", "")
	p.NextUpdateAt = now.Add(-time.Minute).Format(time.RFC3339Nano)
	second := *p
	second.ID = strings.Repeat("b", 64)
	second.Name = "Later"
	second.UpdatePolicy = updatePolicyFixture("direct", "")
	second.UpdateRetry = nil
	if atomicWrite(a.profileContentPath(second.ID), []byte(testProfileYAML), 0o600) != nil {
		t.Fatal("cannot seed second due subscription")
	}
	store.Profiles = append(store.Profiles, second)
	if a.saveProfileStore(store) != nil {
		t.Fatal("cannot seed due subscriptions")
	}
	before := updatePolicyNonMetadataFiles(t, a, id)
	a.runDueSubscriptionUpdates(context.Background(), now)
	store, err := a.loadProfileStore()
	if err != nil || a.operation || proxyCalls.Load() != 1 || directCalls.Load() != 1 || store.Profiles[0].LastUpdate.Status != "failed" || store.Profiles[0].UpdateRetry == nil || store.Profiles[1].LastUpdate.Status != "unchanged" {
		t.Fatal("first failure blocked later subscription or scheduler ignored stored modes")
	}
	assertSubscriptionFilesEqual(t, before)
	a.runDueSubscriptionUpdates(context.Background(), now)
	if proxyCalls.Load() != 1 || directCalls.Load() != 1 {
		t.Fatal("scheduler ignored backoff and ran an immediate busy retry")
	}
}

func TestSubscriptionUpdatePolicyMetadataCommitFailureKeeps304Cache(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"new-validator"`)
		w.Header().Set("Subscription-Userinfo", "total=999")
		w.WriteHeader(304)
	}))
	defer server.Close()
	a, id := isolatedSubscriptionApp(t, server.URL)
	updatePolicyFixtureStore(t, a, id, updatePolicyFixture("direct", "fixture/v2"), true)
	before := subscriptionFixtureFiles(t, a, id)
	path, _ := windows.UTF16PtrFromString(a.profileStorePath())
	handle, err := windows.CreateFile(path, windows.GENERIC_READ, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal("cannot lock isolated store")
	}
	if err := a.updateSubscription(context.Background(), id, ""); err == nil {
		_ = windows.CloseHandle(handle)
		t.Fatal("304 metadata committed to locked store")
	}
	_ = windows.CloseHandle(handle)
	assertSubscriptionFilesEqual(t, before)
}
func TestSubscriptionUpdatePolicyTrafficScannedMarker(t *testing.T) {
	var notModified atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if notModified.Load() {
			w.WriteHeader(304)
			return
		}
		w.Header().Set("ETag", `"synthetic-private-validator"`)
		w.Header().Add("Subscription-Userinfo", "total=500")
		w.Header().Add("Subscription-Userinfo", "TOTAL=999")
		_, _ = w.Write([]byte(testProfileYAML))
	}))
	defer server.Close()
	a, id := isolatedSubscriptionApp(t, server.URL)
	if err := a.updateSubscription(context.Background(), id, "direct"); err != nil {
		t.Fatal("valid 200 with unknown metadata failed")
	}
	store, _ := a.loadProfileStore()
	if !store.Profiles[0].TrafficScanned || store.Profiles[0].Traffic != nil {
		t.Fatal("200 unknown quota was not marked scanned")
	}
	for _, scanned := range []bool{false, true} {
		store.Profiles[0].TrafficScanned = scanned
		if a.saveProfileStore(store) != nil {
			t.Fatal("cannot seed marker")
		}
		notModified.Store(true)
		if err := a.updateSubscription(context.Background(), id, "direct"); err != nil {
			t.Fatal("trusted 304 failed")
		}
		store, _ = a.loadProfileStore()
		if store.Profiles[0].TrafficScanned != scanned {
			t.Fatal("304 changed content scan provenance")
		}
	}
	notModified.Store(false)
	store.Profiles[0].TrafficScanned = false
	if a.saveProfileStore(store) != nil {
		t.Fatal("cannot seed unscanned state")
	}
	t.Setenv(subscriptionValidatorMode, "fail")
	if err := a.updateSubscription(context.Background(), id, "direct"); err == nil {
		t.Fatal("invalid validator fixture succeeded")
	}
	store, _ = a.loadProfileStore()
	if store.Profiles[0].TrafficScanned {
		t.Fatal("failed 200 committed scan marker")
	}
}
