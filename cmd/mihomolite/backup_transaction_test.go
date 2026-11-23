package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newBackupTransactionApp(t *testing.T) *Application {
	t.Helper()
	dir := t.TempDir()
	logs := NewLogStore(10)
	a := &Application{baseDir: dir, dataDir: filepath.Join(dir, "data"), config: &Settings{ConfigName: "Synthetic", APISecret: "synthetic-local-key"}, logger: slog.New(logs), logStore: logs, events: NewEventHub(), proxy: &draftBrowserProxy{}}
	id := strings.Repeat("a", 64)
	now := time.Now().UTC().Format(time.RFC3339)
	if err := atomicWrite(a.profileContentPath(id), []byte(testProfileYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := a.saveProfileStore(profileStore{ActiveID: id, Profiles: []Profile{{ID: id, Name: "Synthetic", Source: "local", CreatedAt: now, UpdatedAt: now, Size: int64(len(testProfileYAML))}}}); err != nil {
		t.Fatal(err)
	}
	if err := atomicWrite(a.customRulesPath(), []byte("[]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := atomicWrite(filepath.Join(a.dataDir, "source.yaml"), []byte(testProfileYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := a.saveSettings(); err != nil {
		t.Fatal(err)
	}
	return a
}
func transactionSnapshots(t *testing.T, a *Application) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(a.dataDir, "backup-import-recovery"))
	if err != nil {
		t.Fatal(err)
	}
	var found []string
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "snapshot-") {
			found = append(found, filepath.Join(a.dataDir, "backup-import-recovery", entry.Name()))
		}
	}
	return found
}
func readTransactionJournal(t *testing.T, dir string) backupJournal {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "journal.json"))
	if err != nil {
		t.Fatal(err)
	}
	var journal backupJournal
	if json.Unmarshal(raw, &journal) != nil {
		t.Fatal("invalid fixture journal")
	}
	return journal
}

func TestBackupFailedCommitIsDurablyRolledBack(t *testing.T) {
	a := newBackupTransactionApp(t)
	originalReplace := backupReplaceFile
	t.Cleanup(func() { backupReplaceFile = originalReplace })
	calls := 0
	backupReplaceFile = func(source, target string) error {
		calls++
		if calls == 2 {
			return errors.New("synthetic replacement failure")
		}
		return originalReplace(source, target)
	}
	source := filepath.Join(a.dataDir, "source.yaml")
	original, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	changes := []backupFileChange{{Path: "source.yaml", Content: []byte("changed synthetic")}, {Path: "custom-rules.json", Content: []byte("[]")}}
	if err := a.commitBackupFiles(changes); err == nil {
		t.Fatal("commit unexpectedly succeeded")
	}
	got, err := os.ReadFile(source)
	if err != nil || !bytes.Equal(original, got) {
		t.Fatal("failed commit did not restore originals")
	}
	snapshots := transactionSnapshots(t, a)
	if len(snapshots) != 1 {
		t.Fatal("missing recovery snapshot")
	}
	journal := readTransactionJournal(t, snapshots[0])
	if journal.State != "rolled-back" {
		t.Fatal("completed rollback remains pending")
	}
	userBackup, err := os.ReadFile(filepath.Join(snapshots[0], "backup.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeBackupDocument(userBackup); err != nil {
		t.Fatal("recovery snapshot is not a complete backup")
	}
	latest := []byte("new content saved after rollback")
	if err := atomicWrite(source, latest, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := a.recoverInterruptedBackupImports(); err != nil {
		t.Fatal(err)
	}
	got, err = os.ReadFile(source)
	if err != nil || !bytes.Equal(got, latest) {
		t.Fatal("startup replayed a completed rollback over a later save")
	}
}

func TestBackupPreparedJournalReplaysOnceAndVerifiesAllSources(t *testing.T) {
	a := newBackupTransactionApp(t)
	source := filepath.Join(a.dataDir, "source.yaml")
	original, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.commitBackupFiles([]backupFileChange{{Path: "source.yaml", Content: []byte("synthetic committed replacement")}}); err != nil {
		t.Fatal(err)
	}
	dir := transactionSnapshots(t, a)[0]
	journal := readTransactionJournal(t, dir)
	journal.State = "prepared"
	if err := writeBackupJournal(dir, journal); err != nil {
		t.Fatal(err)
	}
	if err := a.recoverInterruptedBackupImports(); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(source)
	if err != nil || !bytes.Equal(got, original) {
		t.Fatal("prepared journal failed to restore original")
	}
	if readTransactionJournal(t, dir).State != "rolled-back" {
		t.Fatal("recovery did not persist its completed state")
	}
	journal.State = "prepared"
	if err := writeBackupJournal(dir, journal); err != nil {
		t.Fatal(err)
	}
	if err := atomicWrite(filepath.Join(dir, "files", "source.yaml"), []byte("corrupt snapshot"), 0o600); err != nil {
		t.Fatal(err)
	}
	latest := []byte("keep current on corrupt snapshot")
	if err := atomicWrite(source, latest, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := a.recoverInterruptedBackupImports(); err == nil {
		t.Fatal("corrupt snapshot unexpectedly restored")
	}
	got, err = os.ReadFile(source)
	if err != nil || !bytes.Equal(got, latest) {
		t.Fatal("corrupt snapshot changed current content")
	}
}

func TestBackupRollbackFailureBlocksFurtherSettingsAndOperations(t *testing.T) {
	a := newBackupTransactionApp(t)
	originalReplace := backupReplaceFile
	t.Cleanup(func() { backupReplaceFile = originalReplace })
	source := filepath.Join(a.dataDir, "source.yaml")
	backupReplaceFile = func(_, target string) error {
		// Simulate the replaced source becoming unreadable while rollback attempts it.
		if target == source {
			if err := os.Remove(source); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(source, 0o700); err != nil {
				t.Fatal(err)
			}
		}
		return errors.New("synthetic unrecoverable replacement")
	}
	if err := a.commitBackupFiles([]backupFileChange{{Path: "source.yaml", Content: []byte("new synthetic")}}); err == nil {
		t.Fatal("failed commit succeeded")
	}
	if !a.backupRecoveryBlocked {
		t.Fatal("failed rollback did not block future writes")
	}
	if err := a.begin(); err == nil {
		t.Fatal("new operation allowed with pending recovery")
	}
	before, err := os.ReadFile(filepath.Join(a.baseDir, "launcher.json"))
	if err != nil {
		t.Fatal(err)
	}
	silent := true
	if _, err := a.updateRuntimeSettings(RuntimeSettingsPatch{SilentStart: &silent}); err == nil {
		t.Fatal("settings update allowed with pending recovery")
	}
	after, err := os.ReadFile(filepath.Join(a.baseDir, "launcher.json"))
	if err != nil || !bytes.Equal(before, after) || a.config.SilentStart {
		t.Fatal("blocked settings changed state")
	}
}

func TestBackupOperationBlocksRuntimeSettingsBeforeSideEffects(t *testing.T) {
	a := newBackupTransactionApp(t)
	a.operation = true
	silent := true
	if _, err := a.updateRuntimeSettings(RuntimeSettingsPatch{SilentStart: &silent}); err == nil {
		t.Fatal("settings updated during configuration operation")
	}
	if a.config.SilentStart {
		t.Fatal("blocked setting changed memory")
	}
}

func TestBackupJournalBoundsAndPathTraversalFailBeforeWrites(t *testing.T) {
	a := newBackupTransactionApp(t)
	before, err := os.ReadFile(filepath.Join(a.dataDir, "source.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	excessive := make([]backupFileChange, maxBackupJournalEntries+1)
	if err := a.commitBackupFiles(excessive); err == nil {
		t.Fatal("oversized transaction accepted")
	}
	for _, path := range []string{"../outside.yaml", "profiles/../outside.yaml", "profiles/" + strings.Repeat("a", 64) + ".yaml/extra", "C:/outside.yaml"} {
		if err := a.commitBackupFiles([]backupFileChange{{Path: path, Content: []byte("bad")}}); err == nil {
			t.Fatalf("unsafe path accepted: %s", path)
		}
	}
	after, err := os.ReadFile(filepath.Join(a.dataDir, "source.yaml"))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("rejected transaction changed current files")
	}
}
