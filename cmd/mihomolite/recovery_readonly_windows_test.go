//go:build windows

package main

import (
	"crypto/sha256"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

// Explicitly opt-in, read-only support probe. It never constructs WindowsProxy,
// saves a recovery record, reads registry settings or terminates a process. Only
// process metadata and error codes are printed; snapshots/session IDs stay private.
func TestRecoveryReadOnlyRecordProbe(t *testing.T) {
	path := os.Getenv("MIHOMOLITE_RECOVERY_READONLY_PATH")
	if path == "" {
		t.Skip("requires an explicit read-only recovery record path")
	}
	if !filepath.IsAbs(path) || filepath.Base(path) != proxyRecoveryFile {
		t.Fatal("expected an absolute recovery record path")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal("cannot read recovery record")
	}
	before := sha256.Sum256(raw)
	clear(raw)
	t.Cleanup(func() {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Error("recovery record is no longer readable")
			return
		}
		defer clear(raw)
		if sha256.Sum256(raw) != before {
			t.Error("recovery record changed during read-only probe")
		}
	})
	record, err := (encryptedRecoveryStore{path: path}).Load()
	if err != nil {
		t.Fatal(err)
	}
	if record == nil {
		t.Fatal("no recovery record")
	}
	t.Logf("record phase=%s hasCore=%v hasSnapshot=%v", record.Phase, record.Core != nil, record.Original != nil)
	identities := []struct {
		name string
		id   recoveryProcessIdentity
	}{{"owner", record.Owner}}
	if record.Core != nil {
		identities = append(identities, struct {
			name string
			id   recoveryProcessIdentity
		}{"core", *record.Core})
	}
	for _, item := range identities {
		direct, directErr := checkRecoveryProcessByHandle(item.id)
		alive, err := checkRecoveryProcess(item.id)
		t.Logf("%s pid=%d recordedCreated=%d directMatches=%v directError=%v matches=%v checkError=%v", item.name, item.id.PID, item.id.Created, direct, directErr, alive, err)
		data, snapshotErr := readRecoveryProcessSnapshot()
		if snapshotErr == nil {
			disproved, parseErr := recoveryProcessSnapshotDisprovesInstance(data, item.id)
			t.Logf("%s snapshotDisprovesOldInstance=%v snapshotParseError=%v", item.name, disproved, parseErr)
		} else {
			t.Logf("%s snapshotError=%v", item.name, snapshotErr)
		}
		handle, openErr := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(item.id.PID))
		if openErr != nil {
			t.Logf("%s queryOnlyOpenError=%v", item.name, openErr)
			continue
		}
		var created, exited, kernel, user windows.Filetime
		timeErr := windows.GetProcessTimes(handle, &created, &exited, &kernel, &user)
		image, imageErr := recoveryProcessImage(handle)
		windows.CloseHandle(handle)
		actualCreated := uint64(created.HighDateTime)<<32 | uint64(created.LowDateTime)
		t.Logf("%s queryOnlyCreated=%d creationMatches=%v timeError=%v imageMatches=%v imageError=%v", item.name, actualCreated, actualCreated == item.id.Created, timeErr, image == item.id.Image, imageErr)
	}
}
