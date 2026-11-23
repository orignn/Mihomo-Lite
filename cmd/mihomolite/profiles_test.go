package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const testProfileYAML = "proxies:\n  - name: direct-test\n    type: direct\n    udp: true\nproxy-groups:\n  - name: TEST\n    type: select\n    proxies: [direct-test]\nrules:\n  - MATCH,TEST\n"

func newProfileTestApp(t *testing.T) *Application {
	t.Helper()
	root := mustGetwd(t)
	corePath := filepath.Join(root, "runtime", "mihomo.exe")
	info, err := os.Stat(corePath)
	if os.IsNotExist(err) {
		t.Skip("optional official mihomo core fixture is absent; core-backed profile/override validation scenarios were not run")
	}
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() || info.Size() == 0 {
		t.Fatal("official mihomo core fixture must be a non-empty regular file")
	}
	dir := t.TempDir()
	if err := os.Symlink(filepath.Join(root, "runtime"), filepath.Join(dir, "runtime")); err != nil {
		// Windows may require developer mode for symlinks; copy only in tests.
		if err := os.MkdirAll(filepath.Join(dir, "runtime"), 0o755); err != nil {
			t.Fatal(err)
		}
		content, err := os.ReadFile(filepath.Join(mustGetwd(t), "runtime", "mihomo.exe"))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "runtime", "mihomo.exe"), content, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	a := &Application{baseDir: dir, dataDir: filepath.Join(dir, "data"), logsDir: filepath.Join(dir, "logs"), config: &Settings{}}
	a.logger = slog.New(NewLogStore(100))
	return a
}
func mustGetwd(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Dir(filepath.Dir(wd))
}

func TestFirstYAMLImportRegistersSelectableProfile(t *testing.T) {
	a := newProfileTestApp(t)
	if _, err := a.Import(strings.NewReader(testProfileYAML)); err != nil {
		t.Fatal(err)
	}
	first, err := a.profileResponse()
	if err != nil || len(first.Profiles) != 1 || !first.Profiles[0].Active {
		t.Fatalf("first import not listed: %+v, %v", first, err)
	}
	if _, err := a.Import(strings.NewReader("log-level: warning\n" + testProfileYAML)); err != nil {
		t.Fatal(err)
	}
	second, err := a.profileResponse()
	if err != nil || len(second.Profiles) != 2 || second.ActiveID == first.ActiveID {
		t.Fatalf("second import not selected: %+v, %v", second, err)
	}
	if err := a.editActiveProfile(first.ActiveID, testProfileYAML); err == nil {
		t.Fatal("editing a previously selected profile should fail")
	}
	if err := a.switchProfile(first.ActiveID); err != nil {
		t.Fatal(err)
	}
	if err := a.renameProfile(first.ActiveID, "Restored first profile"); err != nil {
		t.Fatal(err)
	}
	if err := a.deleteProfile(second.ActiveID); err != nil {
		t.Fatal(err)
	}
}

func TestLegacyMigration(t *testing.T) {
	a := newProfileTestApp(t)
	if err := os.MkdirAll(a.dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := []byte(testProfileYAML)
	if err := os.WriteFile(filepath.Join(a.dataDir, "source.yaml"), content, 0o600); err != nil {
		t.Fatal(err)
	}
	a.config.ConfigName = "旧配置"
	if err := a.migrateLegacySource(); err != nil {
		t.Fatal(err)
	}
	store, err := a.loadProfileStore()
	if err != nil {
		t.Fatal(err)
	}
	if len(store.Profiles) != 1 || store.Profiles[0].Name != "旧配置" || store.ActiveID == "" {
		t.Fatalf("migration failed: %+v", store)
	}
	got, err := os.ReadFile(a.sourceYAMLPath())
	if err != nil || string(got) != string(content) {
		t.Fatalf("source changed: %v", err)
	}
	if err := a.migrateLegacySource(); err != nil {
		t.Fatal(err)
	}
	store, _ = a.loadProfileStore()
	if len(store.Profiles) != 1 {
		t.Fatal("migration duplicated profile")
	}
}

func TestSubscriptionUpdateRollbackAndSwitch(t *testing.T) {
	a := newProfileTestApp(t)
	yaml := testProfileYAML
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/yaml")
		w.Header().Set("Subscription-Userinfo", "upload=100; download=900; total=10000; expire=1799999999")
		_, _ = w.Write([]byte(yaml))
	}))
	defer server.Close()
	if err := a.addSubscription(context.Background(), "测试订阅", server.URL, "direct"); err != nil {
		t.Fatal(err)
	}
	store, _ := a.loadProfileStore()
	if len(store.Profiles) != 1 || store.Profiles[0].URL != server.URL {
		t.Fatalf("add: %+v", store)
	}
	traffic := store.Profiles[0].Traffic
	if traffic == nil || traffic.Upload == nil || *traffic.Upload != 100 || traffic.Download == nil || *traffic.Download != 900 || traffic.Total == nil || *traffic.Total != 10000 || traffic.Expire == nil || *traffic.Expire != 1799999999 {
		t.Fatalf("subscription quota metadata not parsed: %+v", traffic)
	}
	id := store.ActiveID
	// Preserve a custom rule independently from the profile.
	rules := []CustomRule{{ID: "one", Type: "DOMAIN", Value: "example.com", Policy: "DIRECT", Enabled: true}}
	content, _ := json.Marshal(rules)
	if err := atomicWrite(a.customRulesPath(), content, 0o600); err != nil {
		t.Fatal(err)
	}
	yaml = "not: [valid"
	if err := a.updateSubscription(context.Background(), id, "direct"); err == nil {
		t.Fatal("invalid update accepted")
	}
	current, _ := os.ReadFile(a.profileContentPath(id))
	if string(current) != testProfileYAML {
		t.Fatal("bad update overwrote good profile")
	}
	yaml = strings.Replace(testProfileYAML, "direct-test", "updated-test", -1)
	if err := a.updateSubscription(context.Background(), id, "direct"); err != nil {
		t.Fatal(err)
	}
	current, _ = os.ReadFile(a.profileContentPath(id))
	if !strings.Contains(string(current), "updated-test") {
		t.Fatal("update not saved")
	}
	if !fileExists(a.profileContentPath(id) + ".bak") {
		t.Fatal("previous version was not backed up")
	}
	_, rendered, err := a.prepareConfig(current)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(rendered), "DOMAIN,example.com,DIRECT") {
		t.Fatal("custom rules lost after update")
	}
	if err := a.deleteProfile(id); err == nil {
		t.Fatal("active profile deletion should be rejected")
	}
	if err := a.addLocalProfile("第二个配置", testProfileYAML); err != nil {
		t.Fatal(err)
	}
	store, _ = a.loadProfileStore()
	if len(store.Profiles) != 2 || store.Profiles[1].Source != "local" {
		t.Fatalf("local add failed: %+v", store)
	}
	activeBeforeEdit := store.Profiles[1]
	edited := strings.ReplaceAll(testProfileYAML, "direct-test", "edited-test")
	if err := a.editActiveProfile(activeBeforeEdit.ID, edited); err != nil {
		t.Fatal(err)
	}
	store, _ = a.loadProfileStore()
	if store.ActiveID != activeBeforeEdit.ID || store.Profiles[1].Name != activeBeforeEdit.Name || store.Profiles[1].Source != "local" {
		t.Fatalf("edit changed active profile metadata: %+v", store.Profiles[1])
	}
	current, err = os.ReadFile(a.profileContentPath(store.ActiveID))
	if err != nil || !strings.Contains(string(current), "edited-test") {
		t.Fatalf("edited content not saved: %v", err)
	}
	if !fileExists(a.profileContentPath(store.ActiveID) + ".bak") {
		t.Fatal("editing did not back up the previous profile")
	}
	if err := a.switchProfile(id); err != nil {
		t.Fatal(err)
	}
	if err := a.deleteProfile(store.Profiles[1].ID); err != nil {
		t.Fatal(err)
	}
	store, _ = a.loadProfileStore()
	if len(store.Profiles) != 1 || store.ActiveID != id {
		t.Fatal("switch/delete failed")
	}
}

func TestSubscriptionUpdateDetailsAndAutoSchedule(t *testing.T) {
	a := newProfileTestApp(t)
	a.events = NewEventHub()
	a.proxy = httpTestProxy{}
	var response atomic.Value
	response.Store(testProfileYAML)
	var downloads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		downloads.Add(1)
		_, _ = w.Write([]byte(response.Load().(string)))
	}))
	defer server.Close()
	request := func(method, path, body string, want int) []byte {
		t.Helper()
		req := httptest.NewRequest(method, "http://127.0.0.1:17653"+path, strings.NewReader(body))
		req.Header.Set("X-Mihomo-Lite", "1")
		req.Header.Set("Origin", "http://127.0.0.1:17653")
		res := httptest.NewRecorder()
		a.Routes().ServeHTTP(res, req)
		if res.Code != want {
			t.Fatalf("%s %s: status %d, want %d, body %s", method, path, res.Code, want, res.Body)
		}
		return res.Body.Bytes()
	}
	profile := func() Profile {
		t.Helper()
		res, err := a.profileResponse()
		if err != nil || len(res.Profiles) != 1 {
			t.Fatalf("profileResponse: %+v, %v", res, err)
		}
		return res.Profiles[0]
	}
	ctx := context.Background()
	if err := a.addSubscription(ctx, "测试", server.URL, "direct"); err != nil {
		t.Fatal(err)
	}
	initial := profile()
	if initial.AutoUpdateHours != 0 || initial.LastUpdate != nil {
		t.Fatalf("unexpected default: %+v", initial)
	}
	diffPath := "/api/profiles/" + initial.ID + "/update-diff"
	request(http.MethodGet, diffPath, "", http.StatusNotFound)
	updateBody := `{"action":"update","id":"` + initial.ID + `","mode":"direct"}`
	response.Store("broken: [yaml")
	request(http.MethodPost, "/api/profiles", updateBody, http.StatusBadRequest)
	failed := profile()
	if failed.LastUpdate == nil || failed.LastUpdate.Status != "failed" || failed.LastUpdate.Auto || failed.LastUpdate.At.IsZero() || failed.UpdatedAt != initial.UpdatedAt || failed.HasUpdateDiff {
		t.Fatalf("unexpected failed result: %+v, update=%+v", failed, failed.LastUpdate)
	}
	updatedYAML := strings.ReplaceAll(testProfileYAML, "direct-test", "updated-test")
	response.Store(updatedYAML)
	request(http.MethodPost, "/api/profiles", updateBody, http.StatusOK)
	changed := profile()
	if changed.LastUpdate.Status != "success" || !changed.HasUpdateDiff {
		t.Fatalf("retry result: %+v, update=%+v", changed, changed.LastUpdate)
	}
	diffBytes := request(http.MethodGet, diffPath, "", http.StatusOK)
	var diff subscriptionDiff
	if err := json.Unmarshal(diffBytes, &diff); err != nil || diff.Before != testProfileYAML || diff.After != updatedYAML {
		t.Fatalf("incorrect update comparison: %+v, %v", diff, err)
	}
	updateSettings := `{"action":"auto-update","id":"` + initial.ID + `","hours":`
	request(http.MethodPost, "/api/profiles", updateSettings+"-1}", http.StatusBadRequest)
	request(http.MethodPost, "/api/profiles", updateSettings+"6}", http.StatusOK)
	next, err := time.Parse(time.RFC3339Nano, profile().NextUpdateAt)
	if err != nil || profile().AutoUpdateHours != 6 {
		t.Fatal("automatic interval was not saved")
	}
	calls := downloads.Load()
	a.runDueSubscriptionUpdates(ctx, next.Add(-time.Second))
	if downloads.Load() != calls {
		t.Fatal("download before schedule was due")
	}
	a.runDueSubscriptionUpdates(ctx, next)
	unchanged := profile()
	if downloads.Load() != calls+1 || unchanged.LastUpdate.Status != "unchanged" || !unchanged.LastUpdate.Auto || unchanged.NextUpdateAt == next.Format(time.RFC3339Nano) {
		t.Fatalf("automatic update result: %+v, update=%+v", unchanged, unchanged.LastUpdate)
	}
	if got := request(http.MethodGet, diffPath, "", http.StatusOK); string(got) != string(diffBytes) {
		t.Fatal("no-change update replaced the comparison")
	}
	if err := a.editActiveProfile(initial.ID, testProfileYAML); err != nil {
		t.Fatal(err)
	}
	if got := request(http.MethodGet, diffPath, "", http.StatusOK); string(got) != string(diffBytes) {
		t.Fatal("manual edit replaced the subscription comparison")
	}
	request(http.MethodPost, "/api/profiles", updateSettings+"0}", http.StatusOK)
	a.runDueSubscriptionUpdates(ctx, time.Now().Add(7*24*time.Hour))
	if current := profile(); current.AutoUpdateHours != 0 || current.NextUpdateAt != "" || downloads.Load() != calls+1 {
		t.Fatalf("disabled subscription was still scheduled: %+v", current)
	}
}

func TestSubscriptionTrafficUserinfoParsing(t *testing.T) {
	got := parseSubscriptionUserinfo("UPLOAD=1024; download=2048; total=8192; expire=1799999999; ignored=x; broken=-1")
	if got == nil || got.Upload == nil || *got.Upload != 1024 || got.Download == nil || *got.Download != 2048 || got.Total == nil || *got.Total != 8192 || got.Expire == nil || *got.Expire != 1799999999 {
		t.Fatalf("parsed quota = %+v", got)
	}
	if parseSubscriptionUserinfo("") != nil || parseSubscriptionUserinfo("upload=NaN;total=-3") != nil {
		t.Fatal("missing or invalid quota fields should remain unknown")
	}
}

func TestEditSubscriptionURLCommitsOnlyAfterValidatedDownload(t *testing.T) {
	a := newProfileTestApp(t)
	content := testProfileYAML
	userinfo := "upload=10; download=20; total=100; expire=1800000000"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/yaml")
		w.Header().Set("Subscription-Userinfo", userinfo)
		_, _ = w.Write([]byte(content))
	}))
	defer server.Close()
	oldAddress := server.URL + "/old?token=synthetic-old"
	newAddress := server.URL + "/new?token=synthetic-new"
	if err := a.addSubscription(context.Background(), "test subscription", oldAddress, "direct"); err != nil {
		t.Fatal("isolated initial subscription setup failed")
	}
	store, err := a.loadProfileStore()
	if err != nil || len(store.Profiles) != 1 {
		t.Fatalf("isolated profile setup failed: %v", err)
	}
	id := store.Profiles[0].ID
	before, err := os.ReadFile(a.profileContentPath(id))
	if err != nil {
		t.Fatal(err)
	}

	content = "not: [valid"
	if err := a.updateSubscriptionURL(context.Background(), id, newAddress, "direct"); err == nil {
		t.Fatal("invalid content from replacement URL should fail")
	}
	store, err = a.loadProfileStore()
	if err != nil || store.Profiles[0].URL != oldAddress {
		t.Fatalf("failed replacement changed saved address: err=%v", err)
	}
	current, err := os.ReadFile(a.profileContentPath(id))
	if err != nil || string(current) != string(before) {
		t.Fatalf("failed replacement changed prior content: %v", err)
	}

	content = strings.ReplaceAll(testProfileYAML, "direct-test", "replacement-test")
	userinfo = "upload=5; download=15; total=200; expire=1900000000"
	if err := a.updateSubscriptionURL(context.Background(), id, newAddress, "direct"); err != nil {
		t.Fatal("valid replacement subscription failed")
	}
	store, err = a.loadProfileStore()
	if err != nil || store.Profiles[0].URL != newAddress || store.Profiles[0].Traffic == nil || store.Profiles[0].Traffic.Total == nil || *store.Profiles[0].Traffic.Total != 200 {
		t.Fatalf("replacement metadata not committed: err=%v", err)
	}
	current, err = os.ReadFile(a.profileContentPath(id))
	if err != nil || !strings.Contains(string(current), "replacement-test") {
		t.Fatalf("replacement content not committed: %v", err)
	}
}

func TestSubscriptionValidation(t *testing.T) {
	for _, address := range []string{"file:///tmp/a", "ftp://example.com/a", "https://user:password@example.com/a", "not a url"} {
		if _, err := normalizeSubscriptionURL(address); err == nil {
			t.Errorf("accepted invalid address %q", address)
		}
	}
	if _, err := normalizeSubscriptionURL(" https://example.com/sub#fragment "); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("proxy: DIRECT")) }))
	defer server.Close()
	a := newProfileTestApp(t)
	if err := a.addSubscription(context.Background(), "错误订阅", server.URL, "direct"); err == nil {
		t.Fatal("incomplete YAML was accepted")
	}
	store, _ := a.loadProfileStore()
	if len(store.Profiles) != 0 {
		t.Fatal("invalid subscription was persisted")
	}
	a2 := newProfileTestApp(t)
	if _, err := a2.downloadSubscription(context.Background(), server.URL, "proxy"); err == nil {
		t.Fatal("proxy mode should require running core")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = ctx
}
