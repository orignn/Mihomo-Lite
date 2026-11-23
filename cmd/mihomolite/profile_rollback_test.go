package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUnchangedEditPreservesPreviousVersion(t *testing.T) {
	dir := t.TempDir()
	a := &Application{baseDir: dir, dataDir: filepath.Join(dir, "data"), config: &Settings{}}
	id := strings.Repeat("a", 64)
	current := []byte(testProfileYAML)
	previous := []byte("# previous version\n" + testProfileYAML)
	store := profileStore{ActiveID: id, Profiles: []Profile{{ID: id, Name: "Synthetic profile", Source: "local", Size: int64(len(current))}}}
	if err := atomicWrite(a.profileContentPath(id), current, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := atomicWrite(a.profileContentPath(id)+".bak", previous, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := a.saveProfileStore(store); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(a.profileStorePath())
	if err != nil {
		t.Fatal(err)
	}
	revision := a.profileRevision.Load()
	// No core is installed: an identical save must not need to validate again.
	if err := a.editActiveProfile(id, string(current)); err != nil {
		t.Fatal(err)
	}
	for file, expected := range map[string][]byte{a.profileContentPath(id): current, a.profileContentPath(id) + ".bak": previous, a.profileStorePath(): before} {
		actual, err := os.ReadFile(file)
		if err != nil || !bytes.Equal(actual, expected) {
			t.Fatalf("unchanged edit altered a saved file: %v", err)
		}
	}
	if a.profileRevision.Load() != revision {
		t.Fatal("unchanged edit advanced the saved configuration revision")
	}
	if fileExists(filepath.Join(dir, "launcher.json")) || fileExists(filepath.Join(a.dataDir, "source.yaml")) {
		t.Fatal("unchanged edit wrote unrelated configuration files")
	}
}
