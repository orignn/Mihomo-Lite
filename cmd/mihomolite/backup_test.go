//go:build windows

package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func opt002BackupHTTP(t *testing.T, app *Application, method, path string, data []byte) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, "http://127.0.0.1:17653"+path, bytes.NewReader(data))
	if method != http.MethodGet {
		request.Header.Set("X-Mihomo-Lite", "1")
		request.Header.Set("Origin", "http://127.0.0.1:17653")
	}
	response := httptest.NewRecorder()
	app.Routes().ServeHTTP(response, request)
	return response
}

func TestBackupExportContainsSavedMetadataAndNoLauncherSecrets(t *testing.T) {
	root := draftTestWorkspace(t)
	runDir := createDraftTestRunDir(t, root, "backup-export")
	t.Cleanup(func() { _ = os.RemoveAll(runDir) })
	fixture := syntheticDraftFixture()
	app := seedDraftBrowserApplication(t, runDir, fixture)
	app.config.APISecret = "SYNTHETIC-LAUNCHER-API-SECRET"
	app.config.Listen = "127.0.0.1:17653"
	if err := app.saveSettingsValue(*app.config); err != nil { t.Fatal("isolated launcher settings fixture failed") }
	previous := fixture.Profiles[0].YAML + "# synthetic previous version\n"
	if err := atomicWrite(app.profileContentPath(fixture.Profiles[0].ID)+".bak", []byte(previous), 0o600); err != nil { t.Fatal("isolated previous YAML fixture failed") }
	store, err := app.loadProfileStore()
	if err != nil { t.Fatal("isolated profile metadata unavailable") }
	for i := range store.Profiles {
		if store.Profiles[i].ID == fixture.Profiles[1].ID {
			store.Profiles[i].LastUpdate = &subscriptionUpdate{At: time.Now().UTC(), Status: "failed", Message: "SYNTHETIC-URL-MESSAGE-SENTINEL", Auto: true}
		}
	}
	if err := app.saveProfileStore(store); err != nil { t.Fatal("isolated update metadata fixture failed") }

	response := opt002BackupHTTP(t, app, http.MethodGet, "/api/backup", nil)
	if response.Code != http.StatusOK { t.Fatalf("backup GET returned status %d", response.Code) }
	if !strings.Contains(response.Header().Get("Content-Disposition"), "mihomolite-backup-") || !strings.Contains(response.Header().Get("Content-Disposition"), ".json") { t.Fatal("backup response lacks a clear JSON filename") }
	if !strings.Contains(response.Header().Get("Cache-Control"), "no-store") { t.Fatal("backup response is cacheable") }
	if strings.Contains(response.Body.String(), "SYNTHETIC-LAUNCHER-API-SECRET") || strings.Contains(response.Body.String(), "SYNTHETIC-URL-MESSAGE-SENTINEL") { t.Fatal("backup leaked launcher secret or raw update error message") }
	backup, err := decodeBackupDocument(response.Body.Bytes())
	if err != nil { t.Fatal("exported document did not pass strict schema and checksum verification") }
	if backup.ActiveID != fixture.Profiles[0].ID || len(backup.Profiles) != 2 || len(backup.Rules) != 1 { t.Fatal("backup omitted saved profile inventory or rules") }
	if backup.Profiles[1].URL != fixture.Profiles[1].Subscription || backup.Profiles[1].AutoUpdateHours != fixture.Profiles[1].AutoHours { t.Fatal("backup omitted subscription URL or update schedule") }
	if backup.Profiles[0].PreviousYAML != previous || backup.Profiles[0].PreviousSHA256 != sha256Hex([]byte(previous)) { t.Fatal("backup omitted validated previous YAML or its digest") }
	responseProfiles, err := app.profileResponse()
	if err != nil || !responseProfiles.Profiles[0].HasBackup { t.Fatal("hasBackup did not reflect the retained previous YAML") }
	settings, err := os.ReadFile(filepath.Join(app.baseDir, "launcher.json"))
	if err != nil || !bytes.Contains(settings, []byte("SYNTHETIC-LAUNCHER-API-SECRET")) { t.Fatal("export unexpectedly modified local settings") }
}

func TestBackupRejectsBadChecksumWithoutChangingFiles(t *testing.T) {
	root := draftTestWorkspace(t)
	runDir := createDraftTestRunDir(t, root, "backup-reject")
	t.Cleanup(func() { _ = os.RemoveAll(runDir) })
	fixture := syntheticDraftFixture()
	app := seedDraftBrowserApplication(t, runDir, fixture)
	valid, err := app.exportBackup()
	if err != nil { t.Fatal("synthetic backup export failed") }
	doc, err := decodeBackupDocument(valid)
	if err != nil { t.Fatal("synthetic backup failed strict validation") }
	doc.Checksum = strings.Repeat("0", 64)
	invalid, err := json.Marshal(doc)
	if err != nil { t.Fatal("synthetic tampered document serialization failed") }
	paths := []string{app.profileStorePath(), app.profileContentPath(fixture.Profiles[0].ID), app.profileContentPath(fixture.Profiles[1].ID), app.customRulesPath(), filepath.Join(app.dataDir, "source.yaml"), filepath.Join(app.baseDir, "launcher.json")}
	before := make([][]byte, len(paths))
	for i, path := range paths { before[i], err = os.ReadFile(path); if err != nil { t.Fatal("isolated baseline file unavailable") } }
	response := opt002BackupHTTP(t, app, http.MethodPost, "/api/backup", invalid)
	if response.Code != http.StatusBadRequest { t.Fatalf("tampered backup returned status %d", response.Code) }
	if strings.Contains(response.Body.String(), "SYNTHETIC") { t.Fatal("error response disclosed backup data") }
	for i, path := range paths { after, err := os.ReadFile(path); if err != nil || !bytes.Equal(after, before[i]) { t.Fatal("rejected import changed existing configuration data") } }
}

func TestBackupStrictJSONRejectsDuplicateKeys(t *testing.T) {
	if err := rejectDuplicateJSONKeys([]byte("{\"schemaVersion\":1,\"schemaVersion\":1}")); err == nil { t.Fatal("duplicate JSON member accepted") }
}
