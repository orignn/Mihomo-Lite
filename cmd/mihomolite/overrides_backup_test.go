package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func overrideBackupDocumentBytes(t *testing.T, doc backupDocument) []byte {
	t.Helper()
	doc.Checksum = ""
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	doc.Checksum = sha256Hex(raw)
	raw, err = json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func seedBackupOverrides(t *testing.T, a *Application, store overrideStore) {
	t.Helper()
	raw, err := json.Marshal(store)
	if err != nil {
		t.Fatal(err)
	}
	if err := atomicWrite(a.overridesPath(), raw, 0600); err != nil {
		t.Fatal(err)
	}
}

// Shape-only subprocess validator; official-core validation is covered by OPT-005 native tests.
func newOverridesBackupValidationApp(t *testing.T) *Application {
	t.Helper()
	a := newBackupTransactionApp(t)
	installDraftBrowserValidator(t, a.baseDir)
	tmp := filepath.Join(filepath.Dir(a.baseDir), "tmp")
	if err := os.MkdirAll(tmp, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv(draftBrowserGate, "1")
	t.Setenv("TEMP", tmp)
	t.Setenv("TMP", tmp)
	return a
}
func TestOverridesBackupRoundTripAndLegacyClearsLayers(t *testing.T) {
	a := newOverridesBackupValidationApp(t)
	id := strings.Repeat("a", 64)
	original := overrideStore{SchemaVersion: 1, Global: "log-level: warning\n", Profiles: map[string]string{id: "dns: {enable: true, nameserver: [1.1.1.1]}\n"}}
	seedBackupOverrides(t, a, original)
	raw, err := a.exportBackup()
	if err != nil {
		t.Fatal(err)
	}
	doc, err := decodeBackupDocument(raw)
	if err != nil || doc.Overrides == nil {
		t.Fatal("overrides omitted from backup")
	}
	if doc.Overrides.Global != original.Global || doc.Overrides.Profiles[id] != original.Profiles[id] {
		t.Fatal("override content changed")
	}
	a.config.ConnectionPolicy = ConnectionPolicy{StartupConnect: true, CrashReconnect: true, MaxRetries: 4}
	seedBackupOverrides(t, a, emptyOverrideStore())
	if err := a.importBackup(raw); err != nil {
		t.Fatal(err)
	}
	after, err := a.loadOverrides()
	if err != nil || after.Global != original.Global || after.Profiles[id] != original.Profiles[id] {
		t.Fatal("restored overrides differ")
	}
	if !a.config.ConnectionPolicy.StartupConnect {
		t.Fatal("backup changed local opt-in connection policy")
	}
	// A real legacy v1 has no optional overrides member; checksum remains compatible.
	doc.Overrides = nil
	doc.Scope = backupScope
	old := overrideBackupDocumentBytes(t, doc)
	if err := a.importBackup(old); err != nil {
		t.Fatal(err)
	}
	cleared, err := a.loadOverrides()
	if err != nil || cleared.Global != "" || len(cleared.Profiles) != 0 {
		t.Fatal("legacy restore retained unrelated layers")
	}
}
func TestOverridesBackupRejectsForeignIDWithoutMutation(t *testing.T) {
	a := newBackupTransactionApp(t)
	seedBackupOverrides(t, a, overrideStore{SchemaVersion: 1, Global: "log-level: warning", Profiles: map[string]string{}})
	raw, err := a.exportBackup()
	if err != nil {
		t.Fatal(err)
	}
	doc, err := decodeBackupDocument(raw)
	if err != nil {
		t.Fatal(err)
	}
	doc.Overrides = &overrideStore{SchemaVersion: 1, Profiles: map[string]string{strings.Repeat("b", 64): "log-level: info"}}
	before, err := os.ReadFile(a.overridesPath())
	if err != nil {
		t.Fatal(err)
	}
	if err := a.importBackup(overrideBackupDocumentBytes(t, doc)); err == nil {
		t.Fatal("foreign profile layer accepted")
	}
	after, err := os.ReadFile(a.overridesPath())
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("failed restore mutated layers")
	}
}
func TestOverridesBackupCommitFailureRestoresLayers(t *testing.T) {
	a := newOverridesBackupValidationApp(t)
	original := overrideStore{SchemaVersion: 1, Global: "log-level: warning", Profiles: map[string]string{}}
	seedBackupOverrides(t, a, original)
	raw, err := a.exportBackup()
	if err != nil {
		t.Fatal(err)
	}
	doc, err := decodeBackupDocument(raw)
	if err != nil {
		t.Fatal(err)
	}
	doc.Overrides.Global = "log-level: info"
	before, err := os.ReadFile(a.overridesPath())
	if err != nil {
		t.Fatal(err)
	}
	realReplace := backupReplaceFile
	t.Cleanup(func() { backupReplaceFile = realReplace })
	injected := false
	backupReplaceFile = func(source, target string) error {
		if filepath.Clean(target) == filepath.Join(a.baseDir, "launcher.json") && !injected {
			injected = true
			return errors.New("synthetic commit failure")
		}
		return realReplace(source, target)
	}
	if err := a.importBackup(overrideBackupDocumentBytes(t, doc)); err == nil {
		t.Fatal("commit unexpectedly succeeded")
	}
	if !injected {
		t.Fatal("failure injection was not reached")
	}
	after, err := os.ReadFile(a.overridesPath())
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("rollback did not restore original layers")
	}
}
func TestOverridesBackupExportOmitsDeletedLayersOnlyInCopy(t *testing.T) {
	a := newBackupTransactionApp(t)
	foreign := strings.Repeat("b", 64)
	seedBackupOverrides(t, a, overrideStore{SchemaVersion: 1, Profiles: map[string]string{foreign: "log-level: info"}})
	raw, err := a.exportBackup()
	if err != nil {
		t.Fatal(err)
	}
	doc, err := decodeBackupDocument(raw)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Overrides != nil && len(doc.Overrides.Profiles) > 0 {
		t.Fatal("deleted layer exported")
	}
	saved, err := a.loadOverrides()
	if err != nil || saved.Profiles[foreign] == "" {
		t.Fatal("export modified local orphan evidence")
	}
}
