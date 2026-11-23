//go:build windows

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
	"gopkg.in/yaml.v3"
)

const subscriptionValidatorEnv = "MIHOMOLITE_TEST_SUB_VALIDATOR"
const subscriptionValidatorMode = "MIHOMOLITE_TEST_SUB_VALIDATOR_MODE"
const syntheticValidationSecret = "https://synthetic.invalid/private?token=synthetic-secret password=synthetic-secret"

// A copied Go test runner accepts only -t in this fixture's tmp directory.
// It never starts a real core, opens a proxy listener, or changes registry data.
func init() {
	expected := os.Getenv(subscriptionValidatorEnv)
	if expected == "" {
		return
	}
	exe, err := os.Executable()
	if err != nil || !strings.EqualFold(filepath.Clean(exe), filepath.Clean(expected)) {
		return
	}
	base := filepath.Dir(filepath.Dir(expected))
	if os.Getenv(subscriptionValidatorMode) == "startup-fail" && len(os.Args) == 5 && os.Args[1] == "-d" && os.Args[3] == "-f" && strings.EqualFold(filepath.Clean(os.Args[2]), filepath.Join(base, "data")) && strings.EqualFold(filepath.Clean(os.Args[4]), filepath.Join(base, "data", "runtime.yaml")) && checkBackupPath(base) == nil {
		time.Sleep(100 * time.Millisecond)
		fmt.Fprintln(os.Stderr, "level=fatal "+syntheticValidationSecret)
		os.Exit(1)
	}
	valid := len(os.Args) == 6 && os.Args[1] == "-t" && os.Args[2] == "-d" && os.Args[4] == "-f"
	if valid {
		dir := filepath.Clean(os.Args[3])
		valid = strings.EqualFold(filepath.Dir(dir), filepath.Join(base, "tmp")) && strings.HasPrefix(filepath.Base(dir), "mihomolite-validate-") && strings.EqualFold(filepath.Clean(os.Args[5]), filepath.Join(dir, "validate.yaml"))
	}
	if !valid || checkBackupPath(base) != nil {
		fmt.Fprintln(os.Stderr, "isolated validator rejected invocation")
		os.Exit(2)
	}
	raw, err := os.ReadFile(os.Args[5])
	var config struct {
		Bind  string   `yaml:"bind-address"`
		Rules []string `yaml:"rules"`
		TUN   struct{ Enable bool }
	}
	if err != nil || yaml.Unmarshal(raw, &config) != nil || config.Bind != "127.0.0.1" || len(config.Rules) == 0 || config.TUN.Enable {
		fmt.Fprintln(os.Stderr, "isolated validator rejected rendered shape")
		os.Exit(2)
	}
	if os.Getenv(subscriptionValidatorMode) == "fail" {
		fmt.Fprintln(os.Stderr, syntheticValidationSecret)
		os.Exit(1)
	}
	if os.Getenv(subscriptionValidatorMode) == "wait" {
		if os.WriteFile(filepath.Join(base, "validator-entered"), []byte("ready"), 0o600) != nil {
			os.Exit(2)
		}
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if fileExists(filepath.Join(base, "validator-release")) {
				os.Exit(0)
			}
			time.Sleep(10 * time.Millisecond)
		}
		os.Exit(2)
	}
	os.Exit(0)
}

func isolatedSubscriptionApp(t *testing.T, address string) (*Application, string) {
	t.Helper()
	a := newBackupTransactionApp(t)
	tmp := filepath.Join(a.baseDir, "tmp")
	if err := os.MkdirAll(tmp, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TEMP", tmp)
	t.Setenv("TMP", tmp)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	validator := filepath.Join(a.baseDir, "runtime", coreExecutable)
	if err := copyFile(self, validator); err != nil {
		t.Fatal(err)
	}
	t.Setenv(subscriptionValidatorEnv, validator)
	t.Setenv(subscriptionValidatorMode, "")
	store, err := a.loadProfileStore()
	if err != nil {
		t.Fatal(err)
	}
	p := &store.Profiles[0]
	p.Source = "subscription"
	p.URL = address
	p.Traffic = parseSubscriptionUserinfo("upload=1;download=2;total=100;expire=1900000000")
	p.AutoUpdateHours = 24
	p.NextUpdateAt = time.Now().Add(24 * time.Hour).Format(time.RFC3339Nano)
	id := p.ID
	if err := a.saveProfileStore(store); err != nil {
		t.Fatal(err)
	}
	if err := atomicWrite(a.profileContentPath(id)+".bak", []byte("# synthetic prior\n"+testProfileYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := atomicWrite(a.subscriptionDiffPath(id), []byte(`{"at":"2026-11-22T00:00:00Z","before":"synthetic before","after":"synthetic after"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := atomicWrite(a.pendingProfilePath(), []byte(strings.Repeat("b", 64)), 0o600); err != nil {
		t.Fatal(err)
	}
	fixtureProxy := a.proxy.(*draftBrowserProxy)
	t.Cleanup(func() {
		if fixtureProxy.calls.Load() != 0 {
			t.Error("isolated subscription test attempted a system proxy operation")
		}
	})
	return a, id
}

func subscriptionFixtureFiles(t *testing.T, a *Application, id string) map[string][]byte {
	t.Helper()
	result := map[string][]byte{}
	for _, path := range []string{a.profileStorePath(), a.profileContentPath(id), a.profileContentPath(id) + ".bak", a.subscriptionDiffPath(id), filepath.Join(a.dataDir, "source.yaml"), a.pendingProfilePath(), a.customRulesPath(), filepath.Join(a.baseDir, "launcher.json")} {
		raw, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			result[path] = nil
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		result[path] = raw
	}
	return result
}

func assertSubscriptionFilesEqual(t *testing.T, before map[string][]byte) {
	t.Helper()
	for path, raw := range before {
		current, err := os.ReadFile(path)
		if raw == nil {
			if !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("fixture absent target was recreated: %s", filepath.Base(path))
			}
			continue
		}
		if err != nil || !bytes.Equal(raw, current) {
			t.Fatalf("fixture target changed unexpectedly: %s", filepath.Base(path))
		}
	}
}

func TestSubscriptionEditCommitFailuresRetainAllOriginalFiles(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Subscription-Userinfo", "upload=10;download=20;total=200")
		_, _ = w.Write([]byte(strings.ReplaceAll(testProfileYAML, "direct-test", "candidate-test")))
	}))
	defer server.Close()
	for _, target := range []string{".yaml.bak", ".yaml", ".subscription-diff.json", "source.yaml", "profiles.json"} {
		t.Run(target, func(t *testing.T) {
			a, id := isolatedSubscriptionApp(t, server.URL+"/old?token=synthetic-old")
			before := subscriptionFixtureFiles(t, a, id)
			original := backupReplaceFile
			failed := false
			t.Cleanup(func() { backupReplaceFile = original })
			backupReplaceFile = func(source, destination string) error {
				if !failed && strings.HasSuffix(destination, target) {
					failed = true
					return errors.New("synthetic storage fault")
				}
				return original(source, destination)
			}
			if err := a.updateSubscriptionURL(context.Background(), id, server.URL+"/new?token=synthetic-new", "direct"); err == nil || !failed {
				t.Fatal("targeted commit fault was not rejected")
			}
			assertSubscriptionFilesEqual(t, before)
			snapshots := transactionSnapshots(t, a)
			if len(snapshots) != 1 || readTransactionJournal(t, snapshots[0]).State != "rolled-back" {
				t.Fatal("failed commit lacks a finished rollback journal")
			}
		})
	}
}

func TestSubscriptionQuotaOnlyEditStorageFailureKeepsOldIdentity(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Subscription-Userinfo", "upload=20;download=30;total=500")
		_, _ = w.Write([]byte(testProfileYAML))
	}))
	defer server.Close()
	a, id := isolatedSubscriptionApp(t, server.URL+"/old")
	before := subscriptionFixtureFiles(t, a, id)
	path, err := windows.UTF16PtrFromString(a.profileStorePath())
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(path, windows.GENERIC_READ, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(handle)
	if err := a.updateSubscriptionURL(context.Background(), id, server.URL+"/new", "direct"); err == nil {
		t.Fatal("locked metadata file accepted replacement")
	}
	assertSubscriptionFilesEqual(t, before)
}

func TestSubscriptionMetadataRefreshAndBackupRoundTrip(t *testing.T) {
	userinfo := "upload=10;download=20;total=200;expire=1900000000"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if userinfo != "" {
			w.Header().Set("Subscription-Userinfo", userinfo)
		}
		_, _ = w.Write([]byte(testProfileYAML))
	}))
	defer server.Close()
	a, id := isolatedSubscriptionApp(t, server.URL+"/old")
	initial, _ := a.loadProfileStore()
	if err := a.updateSubscriptionURL(context.Background(), id, server.URL+"/new", "direct"); err != nil {
		t.Fatal(err)
	}
	store, err := a.loadProfileStore()
	p := store.Profiles[0]
	if err != nil || p.ID != id || p.URL != server.URL+"/new" || p.Name != initial.Profiles[0].Name || p.CreatedAt != initial.Profiles[0].CreatedAt || store.ActiveID != initial.ActiveID || p.AutoUpdateHours != 24 || p.Traffic == nil || *p.Traffic.Total != 200 || p.LastUpdate.Status != "unchanged" {
		t.Fatal("quota-only edit did not preserve identity/schedule or commit new metadata")
	}
	exported, err := a.exportBackup()
	if err != nil {
		t.Fatal(err)
	}
	doc, err := decodeBackupDocument(exported)
	if err != nil || doc.Profiles[0].Traffic == nil {
		t.Fatal("backup omitted quota metadata")
	}
	userinfo = ""
	if err := a.updateSubscription(context.Background(), id, "direct"); err != nil {
		t.Fatal(err)
	}
	store, _ = a.loadProfileStore()
	if store.Profiles[0].Traffic != nil {
		t.Fatal("a successful response without quota kept stale values")
	}
	if err := a.importBackup(exported); err != nil {
		t.Fatal(err)
	}
	store, _ = a.loadProfileStore()
	if !reflect.DeepEqual(store.Profiles[0].Traffic, p.Traffic) || store.Profiles[0].URL != p.URL || store.Profiles[0].AutoUpdateHours != 24 {
		t.Fatal("backup restore lost subscription metadata")
	}
	legacy := doc
	legacy.Version = "0.6.12"
	legacy.Profiles = append([]backupProfile(nil), doc.Profiles...)
	legacy.Profiles[0].Traffic = nil
	if err := a.importBackup(resignSubscriptionBackup(t, legacy)); err != nil {
		t.Fatal("old v1 backup without optional traffic was rejected")
	}
	store, _ = a.loadProfileStore()
	if store.Profiles[0].Traffic != nil {
		t.Fatal("legacy absent metadata was invented")
	}
}

func TestSubscriptionBackupRejectsInvalidQuota(t *testing.T) {
	a, id := isolatedSubscriptionApp(t, "https://synthetic.invalid/subscription")
	raw, err := a.exportBackup()
	if err != nil {
		t.Fatal(err)
	}
	doc, err := decodeBackupDocument(raw)
	if err != nil {
		t.Fatal(err)
	}
	for _, traffic := range []*subscriptionTraffic{{Upload: trafficInt(-1), CheckedAt: time.Now().UTC()}, {Total: trafficInt(maxSubscriptionTrafficValue + 1), CheckedAt: time.Now().UTC()}, {Expire: trafficInt(maxSubscriptionExpiry + 1), CheckedAt: time.Now().UTC()}, {Upload: trafficInt(1)}} {
		before := subscriptionFixtureFiles(t, a, id)
		candidate := doc
		candidate.Profiles = append([]backupProfile(nil), doc.Profiles...)
		candidate.Profiles[0].Traffic = traffic
		if err := a.importBackup(resignSubscriptionBackup(t, candidate)); err == nil {
			t.Fatal("invalid backup quota was accepted")
		}
		assertSubscriptionFilesEqual(t, before)
	}
}

func TestSubscriptionChangedCommitRecoversAsOneIdentity(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Subscription-Userinfo", "upload=10;download=20;total=500")
		_, _ = w.Write([]byte(strings.ReplaceAll(testProfileYAML, "direct-test", "changed-test")))
	}))
	defer server.Close()
	a, id := isolatedSubscriptionApp(t, server.URL+"/old")
	before := subscriptionFixtureFiles(t, a, id)
	if err := a.updateSubscriptionURL(context.Background(), id, server.URL+"/new", "direct"); err != nil {
		t.Fatal(err)
	}
	snapshots := transactionSnapshots(t, a)
	journal := readTransactionJournal(t, snapshots[0])
	if journal.State != "committed" {
		t.Fatal("successful identity replacement has no committed journal")
	}
	journal.State = "prepared"
	if err := writeBackupJournal(snapshots[0], journal); err != nil {
		t.Fatal(err)
	}
	if err := a.recoverInterruptedBackupImports(); err != nil {
		t.Fatal(err)
	}
	assertSubscriptionFilesEqual(t, before)
	if readTransactionJournal(t, snapshots[0]).State != "rolled-back" {
		t.Fatal("recovery was not durably completed")
	}
	if err := a.recoverInterruptedBackupImports(); err != nil {
		t.Fatal(err)
	}
	assertSubscriptionFilesEqual(t, before)
}

func TestSubscriptionRoutineContentUpdateUsesScopedJournal(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Subscription-Userinfo", "total=500")
		_, _ = w.Write([]byte(strings.ReplaceAll(testProfileYAML, "direct-test", "routine-test")))
	}))
	defer server.Close()
	a, id := isolatedSubscriptionApp(t, server.URL)
	if err := a.updateSubscription(context.Background(), id, "direct"); err != nil {
		t.Fatal(err)
	}
	snapshots := transactionSnapshots(t, a)
	if len(snapshots) != 1 || readTransactionJournal(t, snapshots[0]).State != "committed" || fileExists(filepath.Join(snapshots[0], "backup.json")) || a.latestRecoverySnapshot() != "" {
		t.Fatal("routine update created a full subscription export or omitted its scoped journal")
	}
	store, _ := a.loadProfileStore()
	if store.Profiles[0].URL != server.URL || store.Profiles[0].Traffic == nil || *store.Profiles[0].Traffic.Total != 500 {
		t.Fatal("routine metadata was not committed with content")
	}
}

func TestSubscriptionValidationFailureDoesNotLeakCoreOutput(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(testProfileYAML)) }))
	defer server.Close()
	a, id := isolatedSubscriptionApp(t, server.URL+"/old")
	t.Setenv(subscriptionValidatorMode, "fail")
	before := subscriptionFixtureFiles(t, a, id)
	err := a.updateSubscriptionURL(context.Background(), id, server.URL+"/new", "direct")
	if err == nil || strings.Contains(err.Error(), "synthetic-secret") || strings.Contains(err.Error(), "synthetic.invalid/private") {
		t.Fatal("raw validator output leaked through the edit error")
	}
	if err := a.begin(); err != nil {
		t.Fatal(err)
	}
	a.finish(err)
	logs, _ := json.Marshal(a.logStore.Entries())
	if bytes.Contains(logs, []byte("synthetic-secret")) {
		t.Fatal("raw validator output leaked into launcher logs")
	}
	assertSubscriptionFilesEqual(t, before)
}

func TestSubscriptionCancelDuringValidationKeepsSameContentIdentity(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Subscription-Userinfo", "total=999")
		_, _ = w.Write([]byte(testProfileYAML))
	}))
	defer server.Close()
	a, id := isolatedSubscriptionApp(t, server.URL+"/old")
	before := subscriptionFixtureFiles(t, a, id)
	t.Setenv(subscriptionValidatorMode, "wait")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- a.updateSubscriptionURL(ctx, id, server.URL+"/new", "direct") }()
	deadline := time.After(5 * time.Second)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for !fileExists(filepath.Join(a.baseDir, "validator-entered")) {
		select {
		case <-deadline:
			t.Fatal("validator fixture never entered")
		case <-ticker.C:
		}
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal("validation cancellation was not preserved")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled validation did not stop")
	}
	assertSubscriptionFilesEqual(t, before)
}

func TestSubscriptionPostCommitApplyFailureReportsSavedAndNoSecrets(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Subscription-Userinfo", "total=999")
		_, _ = w.Write([]byte(strings.ReplaceAll(testProfileYAML, "direct-test", "apply-test")))
	}))
	defer server.Close()
	a, id := isolatedSubscriptionApp(t, server.URL+"/old")
	original := applySavedSubscription
	t.Cleanup(func() { applySavedSubscription = original })
	applySavedSubscription = func(a *Application, id string) error { return errors.New(syntheticValidationSecret) }
	err := a.updateSubscriptionURL(context.Background(), id, server.URL+"/new", "direct")
	store, _ := a.loadProfileStore()
	p := store.Profiles[0]
	if err == nil || strings.Contains(err.Error(), "synthetic-secret") || !strings.Contains(err.Error(), "已保存") || p.URL != server.URL+"/new" || p.Traffic == nil || *p.Traffic.Total != 999 || p.LastUpdate.Status != "saved" {
		t.Fatal("post-commit apply failure was mistaken for an uncommitted edit or leaked its cause")
	}
}

func TestSubscriptionApplicationContextAndOperationWait(t *testing.T) {
	a, _ := isolatedSubscriptionApp(t, "https://synthetic.invalid/subscription")
	request, cancelRequest := context.WithCancel(context.Background())
	cancelRequest()
	if a.subscriptionContext(request).Err() != nil {
		t.Fatal("browser disconnect cancelled an unbound transaction")
	}
	lifecycle, cancel := context.WithCancel(context.Background())
	defer cancel()
	a.lifecycleContext = lifecycle
	if a.subscriptionContext(request) != lifecycle {
		t.Fatal("manual subscription operation ignored application lifecycle")
	}
	if err := a.begin(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { a.waitForOperation(); close(done) }()
	select {
	case <-done:
		t.Fatal("operation wait completed before the operation")
	default:
	}
	a.finish(nil)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("operation completion did not unblock shutdown")
	}
	cancel()
	if a.subscriptionContext(context.Background()).Err() != context.Canceled {
		t.Fatal("application exit failed to cancel preparation")
	}
}

func TestSubscriptionCancellationAfterCommitStartsFinishesIdentityAndSkipsReconnect(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Subscription-Userinfo", "total=600")
		_, _ = w.Write([]byte(strings.ReplaceAll(testProfileYAML, "direct-test", "cancel-commit-test")))
	}))
	defer server.Close()
	a, id := isolatedSubscriptionApp(t, server.URL+"/old")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	original := backupReplaceFile
	originalApply := applySavedSubscription
	t.Cleanup(func() { backupReplaceFile = original; applySavedSubscription = originalApply })
	backupReplaceFile = func(source, target string) error { err := original(source, target); cancel(); return err }
	called := false
	applySavedSubscription = func(a *Application, id string) error { called = true; return nil }
	err := a.updateSubscriptionURL(ctx, id, server.URL+"/new", "direct")
	store, _ := a.loadProfileStore()
	p := store.Profiles[0]
	if err == nil || !strings.Contains(err.Error(), "已保存") || called || p.URL != server.URL+"/new" || p.Traffic == nil || *p.Traffic.Total != 600 || p.LastUpdate.Status != "saved" {
		t.Fatal("cancellation split a committed identity or restarted the core during exit")
	}
	if readTransactionJournal(t, transactionSnapshots(t, a)[0]).State != "committed" {
		t.Fatal("cancellation left an already-started commit incomplete")
	}
}

func TestSubscriptionManualHTTPUsesLifecycleAndQuiescesAfterExitCancel(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(started); <-r.Context().Done() }))
	defer server.Close()
	a, id := isolatedSubscriptionApp(t, server.URL+"/old")
	before := subscriptionFixtureFiles(t, a, id)
	lifecycle, cancel := context.WithCancel(context.Background())
	defer cancel()
	a.lifecycleContext = lifecycle
	request, cancelRequest := context.WithCancel(context.Background())
	cancelRequest()
	body, _ := json.Marshal(map[string]string{"action": "edit-url", "id": id, "url": server.URL + "/new", "mode": "direct"})
	r := httptest.NewRequest(http.MethodPost, "/api/profiles", bytes.NewReader(body)).WithContext(request)
	r.Header.Set("X-Mihomo-Lite", "1")
	w := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { a.Profiles(w, r); close(done) }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("manual update inherited the disconnected browser instead of lifecycle")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("application exit did not cancel manual subscription preparation")
	}
	a.waitForOperation()
	if w.Code != http.StatusBadRequest || a.operation {
		t.Fatal("cancelled manual operation did not finish")
	}
	assertSubscriptionFilesEqual(t, before)
}

func TestSubscriptionRoutineJournalFixedSlotAndStagingRecovery(t *testing.T) {
	body := testProfileYAML
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) }))
	defer server.Close()
	a, id := isolatedSubscriptionApp(t, server.URL)
	for _, name := range []string{"fixed-one", "fixed-two", "fixed-three"} {
		body = strings.ReplaceAll(testProfileYAML, "direct-test", name)
		if err := a.updateSubscription(context.Background(), id, "direct"); err != nil {
			t.Fatal(err)
		}
		snapshots := transactionSnapshots(t, a)
		if len(snapshots) != 1 || filepath.Base(snapshots[0]) != "snapshot-subscription-"+id || !readTransactionJournal(t, snapshots[0]).Scoped {
			t.Fatal("routine updates grew journals instead of reusing one completed slot")
		}
	}
	slot := transactionSnapshots(t, a)[0]
	before := subscriptionFixtureFiles(t, a, id)
	if err := writeBackupJournal(slot, backupJournal{Scoped: true, State: "staging", Entries: []backupJournalEntry{}}); err != nil {
		t.Fatal(err)
	}
	if err := atomicWrite(filepath.Join(slot, "files", "profiles", id+".yaml"), []byte("synthetic partial image"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := a.recoverInterruptedBackupImports(); err != nil {
		t.Fatal(err)
	}
	assertSubscriptionFilesEqual(t, before)
	body = strings.ReplaceAll(testProfileYAML, "direct-test", "after-staging")
	if err := a.updateSubscription(context.Background(), id, "direct"); err != nil {
		t.Fatal(err)
	}
	journal := readTransactionJournal(t, slot)
	journal.State = "prepared"
	if err := writeBackupJournal(slot, journal); err != nil {
		t.Fatal(err)
	}
	before = subscriptionFixtureFiles(t, a, id)
	body = strings.ReplaceAll(testProfileYAML, "direct-test", "must-not-overwrite")
	if err := a.updateSubscription(context.Background(), id, "direct"); err == nil || !a.backupRecoveryBlocked {
		t.Fatal("prepared transaction was overwritten")
	}
	assertSubscriptionFilesEqual(t, before)
	if readTransactionJournal(t, slot).State != "prepared" {
		t.Fatal("outstanding recovery was retired incorrectly")
	}
}

func TestSubscriptionAsyncReconnectWaitsForItsCompletion(t *testing.T) {
	a, _ := isolatedSubscriptionApp(t, "https://synthetic.invalid/subscription")
	a.coreControl.Lock()
	locked := true
	defer func() {
		if locked {
			a.coreControl.Unlock()
		}
	}()
	a.core = &coreSession{cmd: &exec.Cmd{}}
	if err := a.requestReconnect(); err != nil {
		t.Fatal(err)
	}
	a.mu.Lock()
	channel := a.operationDone
	a.mu.Unlock()
	if channel == nil {
		t.Fatal("async reconnect did not register operation completion")
	}
	done := make(chan struct{})
	go func() { a.waitForOperation(); close(done) }()
	select {
	case <-done:
		t.Fatal("shutdown ignored a queued async reconnect")
	default:
	}
	a.mu.Lock()
	a.shuttingDown = true
	a.core = nil
	a.mu.Unlock()
	a.coreControl.Unlock()
	locked = false
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("async reconnect did not finish after exit prevented startup")
	}
}

type subscriptionFailureProxy struct{ mutations int }

func (p *subscriptionFailureProxy) Enable(string, string) error {
	p.mutations++
	return errors.New("fixture proxy writes disabled")
}
func (p *subscriptionFailureProxy) Disable() error {
	p.mutations++
	return errors.New("fixture proxy writes disabled")
}
func (*subscriptionFailureProxy) LastError() string { return "" }
func (*subscriptionFailureProxy) Inspect() (proxySnapshot, bool, error) {
	return proxySnapshot{}, false, nil
}

func TestSubscriptionStartupFailureKeepsStateAndSSEFreeOfCoreSecrets(t *testing.T) {
	a, id := isolatedSubscriptionApp(t, "https://synthetic.invalid/subscription")
	p := &subscriptionFailureProxy{}
	a.proxy = p
	a.logsDir = filepath.Join(a.baseDir, "logs")
	if err := os.MkdirAll(a.logsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := atomicWrite(filepath.Join(a.dataDir, "ui", "index.html"), []byte("synthetic panel"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(subscriptionValidatorMode, "startup-fail")
	original := applySavedSubscription
	t.Cleanup(func() { applySavedSubscription = original })
	applySavedSubscription = func(a *Application, id string) error { return a.Start() }
	events := make(chan State, 100)
	a.events.mu.Lock()
	a.events.clients[-1] = events
	a.events.mu.Unlock()
	defer func() { a.events.mu.Lock(); delete(a.events.clients, -1); a.events.mu.Unlock() }()
	if err := a.applyCommittedSubscription(id); err == nil {
		t.Fatal("startup failure fixture was accepted")
	}
	if a.subscriptionApplying || p.mutations != 0 {
		t.Fatal("startup failure retained scope or attempted a system proxy mutation")
	}
	state, _ := json.Marshal(a.State())
	logs, _ := json.Marshal(a.logStore.Entries())
	if bytes.Contains(state, []byte("synthetic-secret")) || bytes.Contains(logs, []byte("synthetic-secret")) {
		t.Fatal("raw startup output leaked to state or launcher logs")
	}
	for len(events) > 0 {
		raw, _ := json.Marshal(<-events)
		if bytes.Contains(raw, []byte("synthetic-secret")) {
			t.Fatal("raw startup output was published before outer safe error")
		}
	}
	rawPath := filepath.Join(a.logsDir, "synthetic-core.log")
	if err := atomicWrite(rawPath, []byte("level=fatal "+syntheticValidationSecret), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := startupExitError(errors.New(syntheticValidationSecret), rawPath, 0, "synthetic-api-secret"); strings.Contains(err.Error(), "synthetic-secret") {
		t.Fatal("startup diagnosis rebroadcast raw fatal or wait error")
	}
}
