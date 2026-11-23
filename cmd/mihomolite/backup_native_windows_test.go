//go:build windows

package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const backupNativeRootEnv = "MIHOMOLITE_TEST_BACKUP_NATIVE_ROOT"

// Opt in by setting MIHOMOLITE_TEST_BACKUP_NATIVE_ROOT to a cached test fixture
// that has runtime/mihomo.exe and data/geoip.metadb. The app itself remains
// stopped. Only invocations of the real validator via mihomo -t are exercised.
func TestBackupNativeValidation(t *testing.T) {
	source := os.Getenv(backupNativeRootEnv)
	if source == "" {
		t.Skip("opt in with " + backupNativeRootEnv + " pointing to a fixture under .cache/tests")
	}
	root := draftTestWorkspace(t)
	if !filepath.IsAbs(source) {
		source = filepath.Join(root, source)
	}
	testCache := filepath.Join(root, ".cache", "tests")
	relative, err := filepath.Rel(testCache, source)
	if err != nil || relative == "." || !filepath.IsLocal(relative) {
		t.Fatal("the native input root must be a fixture inside the workspace test cache")
	}
	source = canonicalNativeBackupPath(t, source)

	runDir := createDraftTestRunDir(t, root, "native-validator")
	t.Logf("isolated native validator artifacts: %s", runDir)
	fixture := syntheticDraftFixture()
	for i := range fixture.Profiles {
		// The cache-supplied Geo data lets tests also validate a real GEOIP rule.
		fixture.Profiles[i].YAML = strings.Replace(fixture.Profiles[i].YAML, "  - MATCH", "  - GEOIP,US,DIRECT\n  - MATCH", 1)
	}
	app := seedDraftBrowserApplication(t, runDir, fixture)
	copyNativeBackupAssets(t, source, app)
	t.Logf("copied official validator SHA-256 %x", hashNativeBackupFile(t, filepath.Join(app.baseDir, "runtime", coreExecutable)))

	const token = "synthetic-native-validation-fixture"
	app.config.Listen = "127.0.0.1:18081"
	routes := draftBrowserRoutes(app, token)
	request := func(method, path string, body []byte, wantStatus int) []byte {
		t.Helper()
		origin := "http://" + app.config.Listen
		req := httptest.NewRequest(method, origin+path, bytes.NewReader(body))
		req.Header.Set("X-Mihomo-Lite", "1")
		req.Header.Set("Origin", origin)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(draftBrowserFixtureHeader, token)
		w := httptest.NewRecorder()
		routes.ServeHTTP(w, req)
		if w.Code != wantStatus {
			t.Fatalf("%s %s returned %d, want %d: %s", method, path, w.Code, wantStatus, w.Body.String())
		}
		return w.Body.Bytes()
	}
	profileAction := func(action, id, content string, wantStatus int) {
		t.Helper()
		payload, err := json.Marshal(map[string]string{"action": action, "id": id, "content": content})
		if err != nil {
			t.Fatal(err)
		}
		request(http.MethodPost, "/api/profiles", payload, wantStatus)
	}

	alpha, beta := fixture.Profiles[0], fixture.Profiles[1]
	edited := alpha.YAML + "# Successfully saved native fixture edit\n"
	previous := app.profileContentPath(alpha.ID) + ".bak"
	paths := []string{
		app.profileStorePath(), app.profileContentPath(alpha.ID), app.profileContentPath(beta.ID), previous,
		app.customRulesPath(), filepath.Join(app.dataDir, "source.yaml"), filepath.Join(app.baseDir, "launcher.json"),
	}
	snapshot := func() [][sha256.Size]byte {
		t.Helper()
		hashes := make([][sha256.Size]byte, len(paths))
		for i, file := range paths {
			hashes[i] = hashNativeBackupFile(t, file)
		}
		return hashes
	}
	assertHashes := func(want [][sha256.Size]byte, operation string) {
		t.Helper()
		for i, hash := range snapshot() {
			if hash != want[i] {
				t.Fatalf("%s changed %s", operation, paths[i])
			}
		}
	}
	assertContent := func(file, want string) {
		t.Helper()
		content, err := os.ReadFile(file)
		if err != nil || string(content) != want {
			t.Fatalf("unexpected contents of %s: %v", file, err)
		}
	}

	profileAction("edit", alpha.ID, edited, http.StatusOK)
	assertContent(previous, alpha.YAML)
	t.Log("PASS official mihomo -t accepts synthetic YAML and retains previous content")
	backup := request(http.MethodGet, "/api/backup", nil, http.StatusOK)
	if err := os.WriteFile(filepath.Join(runDir, "synthetic-backup.json"), backup, 0o600); err != nil {
		t.Fatal(err)
	}
	invalidYAML := strings.Replace(edited, "port: 9", "port: invalid-fixture-port", 1)
	doc, err := decodeBackupDocument(backup)
	if err != nil || len(doc.Profiles) != 2 || doc.Profiles[0].ID != alpha.ID || doc.Profiles[0].YAML != edited {
		t.Fatalf("export did not contain the edited YAML: %v", err)
	}

	// Update both checksums so the edited candidate reaches native validation.
	doc.Profiles[0].YAML = invalidYAML
	doc.Profiles[0].Size = int64(len(invalidYAML))
	doc.Profiles[0].SHA256 = sha256Hex([]byte(invalidYAML))
	doc.Checksum = ""
	canonical, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	doc.Checksum = sha256Hex(canonical)
	invalidBackup, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	_, rendered, err := app.prepareConfig([]byte(invalidYAML))
	if err != nil {
		t.Fatalf("test candidate did not pass static preparation: %v", err)
	}
	if err := app.testConfig(rendered); err == nil || !strings.Contains(err.Error(), "mihomo 拒绝此配置") {
		t.Fatalf("official mihomo -t did not reject the malformed proxy port: %v", err)
	}

	profileAction("switch", beta.ID, "", http.StatusOK)
	changed, err := app.loadProfileStore()
	if err != nil {
		t.Fatal(err)
	}
	idx := profileIndex(changed, beta.ID)
	if idx < 0 {
		t.Fatal("fixture subscription not present")
	}
	changedSub := &changed.Profiles[idx]
	changedSub.Name, changedSub.URL = "Changed subscription name", "https://changed.fixture.invalid/subscription"
	changedSub.AutoUpdateHours, changedSub.NextUpdateAt = 0, ""
	if err := app.saveProfileStore(changed); err != nil {
		t.Fatal(err)
	}
	newRules := []byte(`{"rules":[{"id":"changed-rule","type":"DOMAIN","value":"changed.fixture.invalid","policy":"DIRECT","enabled":true}]}`)
	request(http.MethodPut, "/api/rules", newRules, http.StatusOK)
	beforeImport := snapshot()
	request(http.MethodPost, "/api/backup", invalidBackup, http.StatusBadRequest)
	assertHashes(beforeImport, "rejected backup import")
	t.Log("PASS backup containing YAML with an invalid proxy port leaves saved files byte-identical")

	request(http.MethodPost, "/api/backup", backup, http.StatusOK)
	store, err := app.loadProfileStore()
	if err != nil || store.ActiveID != alpha.ID || len(store.Profiles) != 2 {
		t.Fatalf("backup failed to restore configuration inventory: %v", err)
	}
	idx = profileIndex(store, beta.ID)
	if idx < 0 {
		t.Fatal("restored subscription not present")
	}
	p := store.Profiles[idx]
	if p.Name != beta.Name || p.Source != "subscription" || p.URL != beta.Subscription || p.AutoUpdateHours != beta.AutoHours {
		t.Fatal("restored subscription metadata differs from its saved backup")
	}
	rules, err := app.loadCustomRules()
	if err != nil || !reflect.DeepEqual(rules, []CustomRule{fixture.Rule}) {
		t.Fatalf("backup failed to restore saved rules: %v", err)
	}
	assertContent(app.profileContentPath(alpha.ID), edited)
	assertContent(app.profileContentPath(beta.ID), beta.YAML)
	assertContent(filepath.Join(app.dataDir, "source.yaml"), edited)
	assertContent(previous, alpha.YAML)
	t.Log("PASS backup import restores active YAML, previous version, subscription metadata, and saved rules")

	if err := atomicWrite(previous, []byte(invalidYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	beforeRollback := snapshot()
	profileAction("rollback", alpha.ID, "", http.StatusBadRequest)
	assertHashes(beforeRollback, "rejected rollback")
	t.Log("PASS native validation rejects a corrupt previous YAML without altering any saved files")
	if err := atomicWrite(previous, []byte(alpha.YAML), 0o600); err != nil {
		t.Fatal(err)
	}
	profileAction("rollback", alpha.ID, "", http.StatusOK)
	assertContent(app.profileContentPath(alpha.ID), alpha.YAML)
	assertContent(filepath.Join(app.dataDir, "source.yaml"), alpha.YAML)
	assertContent(previous, edited)
	profileAction("rollback", alpha.ID, "", http.StatusOK)
	assertContent(app.profileContentPath(alpha.ID), edited)
	assertContent(previous, alpha.YAML)
	t.Log("PASS rollback rotates the active and previous YAML under native validation")
}

func canonicalNativeBackupPath(t *testing.T, input string) string {
	t.Helper()
	path, err := filepath.EvalSymlinks(input)
	if err != nil || !strings.EqualFold(path, filepath.Clean(input)) {
		t.Fatalf("native validation input must not traverse a reparse point: %s (%v)", input, err)
	}
	return path
}

func copyNativeBackupAssets(t *testing.T, source string, app *Application) {
	t.Helper()
	copyAsset := func(folder, name, dest string) {
		t.Helper()
		input := canonicalNativeBackupPath(t, filepath.Join(source, folder, name))
		if err := copyFile(input, dest); err != nil {
			t.Fatal(err)
		}
		if hashNativeBackupFile(t, input) != hashNativeBackupFile(t, dest) {
			t.Fatalf("native asset copy changed %s", name)
		}
	}
	copyAsset("runtime", coreExecutable, filepath.Join(app.baseDir, "runtime", coreExecutable))
	for _, file := range geoCacheFiles {
		if file == "geoip.metadb" || fileExists(filepath.Join(source, "data", file)) {
			copyAsset("data", file, filepath.Join(app.dataDir, file))
		}
	}
}

func hashNativeBackupFile(t *testing.T, name string) [sha256.Size]byte {
	t.Helper()
	file, err := os.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		t.Fatal(err)
	}
	return [sha256.Size]byte(hash.Sum(nil))
}
