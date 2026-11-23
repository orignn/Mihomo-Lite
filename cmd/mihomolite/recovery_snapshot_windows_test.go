//go:build windows

package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// These tests call the real read-only kernel snapshot API. Access-denied fallback
// cases inject the primary handle error; they do not alter any process DACL,
// registry/proxy setting, user recovery record, or claim actual PID recycling.
type recoverySnapshotTestEntry struct {
	pid     uint64
	created uint64
}

func recoverySnapshotTestIdentity() recoveryProcessIdentity {
	return recoveryProcessIdentity{PID: 4000, Created: 100, Image: "C:\\isolated\\recovery-snapshot-helper.exe"}
}

func recoverySnapshotTestData(entries ...recoverySnapshotTestEntry) []byte {
	var layout windows.SYSTEM_PROCESS_INFORMATION
	header := int(unsafe.Sizeof(layout))
	pointer := int(unsafe.Sizeof(uintptr(0)))
	pidOffset := int(unsafe.Offsetof(layout.UniqueProcessID))
	createdOffset := int(unsafe.Offsetof(layout.CreateTime))
	data := make([]byte, header*len(entries))
	for index, item := range entries {
		entry := data[index*header : (index+1)*header]
		if index+1 < len(entries) {
			binary.LittleEndian.PutUint32(entry[:4], uint32(header))
		}
		if pointer == 8 {
			binary.LittleEndian.PutUint64(entry[pidOffset:pidOffset+pointer], item.pid)
		} else {
			binary.LittleEndian.PutUint32(entry[pidOffset:pidOffset+pointer], uint32(item.pid))
		}
		binary.LittleEndian.PutUint64(entry[createdOffset:createdOffset+8], item.created)
	}
	return data
}

// Extract only scalar metadata after the production parser validates the native
// list. No snapshot ImageName pointer is read or printed, even by test code.
func recoverySnapshotTestNativeCreated(t *testing.T, data []byte, identity recoveryProcessIdentity) uint64 {
	t.Helper()
	if _, err := recoveryProcessSnapshotDisprovesInstance(data, identity); err != nil {
		t.Fatalf("native snapshot is structurally invalid: %v", err)
	}
	var layout windows.SYSTEM_PROCESS_INFORMATION
	header := int(unsafe.Sizeof(layout))
	pointer := int(unsafe.Sizeof(uintptr(0)))
	pidOffset := int(unsafe.Offsetof(layout.UniqueProcessID))
	createdOffset := int(unsafe.Offsetof(layout.CreateTime))
	for offset := 0; len(data)-offset >= header; {
		entry := data[offset:]
		pid := uint64(binary.LittleEndian.Uint32(entry[pidOffset : pidOffset+pointer]))
		if pointer == 8 {
			pid = binary.LittleEndian.Uint64(entry[pidOffset : pidOffset+pointer])
		}
		if pid == uint64(identity.PID) {
			return binary.LittleEndian.Uint64(entry[createdOffset : createdOffset+8])
		}
		next := binary.LittleEndian.Uint32(entry[:4])
		if next == 0 {
			break
		}
		if uint64(next) < uint64(header) || uint64(next) > uint64(len(entry)) {
			t.Fatal("unexpected invalid native snapshot offset")
		}
		offset += int(next)
	}
	t.Fatalf("native snapshot omitted our owned process PID %d", identity.PID)
	return 0
}

func TestRecoverySnapshotNativeCurrentAndHelperFILETIME(t *testing.T) {
	current, err := captureRecoveryProcess(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	helper := startRecoveryProcessTestHelper(t, 0)
	data, err := readRecoveryProcessSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	for _, identity := range []recoveryProcessIdentity{current, helper.identity} {
		created := recoverySnapshotTestNativeCreated(t, data, identity)
		if created == 0 || int64(created) < 0 || created != identity.Created {
			t.Fatalf("native raw creation FILETIME differs from handle capture: PID=%d snapshot=%d handle=%d", identity.PID, created, identity.Created)
		}
		if disproved, err := recoveryProcessSnapshotDisprovesInstance(data, identity); err != nil || disproved {
			t.Fatalf("native snapshot disproved our matching live process: disproved=%v err=%v", disproved, err)
		}
		stale := identity
		stale.Created ^= 1 // Synthetic stale identity, not an actual recycled PID.
		if disproved, err := recoveryProcessSnapshotDisprovesInstance(data, stale); err != nil || !disproved {
			t.Fatalf("native snapshot failed to disprove a different raw creation: disproved=%v err=%v", disproved, err)
		}
	}
	// Graceful EOF exit of our private helper; never call the stop API here.
	if err := helper.stdin.Close(); err != nil {
		t.Fatal(err)
	}
	waitRecoveryProcessTestHelper(t, helper)
	if helper.waitErr != nil {
		t.Fatalf("private helper did not exit normally: %v", helper.waitErr)
	}
}

func TestRecoverySnapshotNativeFallbackWithInjectedDenial(t *testing.T) {
	identity, err := captureRecoveryProcess(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	data, err := readRecoveryProcessSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if got := recoverySnapshotTestNativeCreated(t, data, identity); got != identity.Created {
		t.Fatal("native snapshot did not preserve our creation FILETIME")
	}
	for _, stale := range []bool{false, true} {
		t.Run(fmt.Sprintf("stale-created=%v", stale), func(t *testing.T) {
			target := identity
			if stale {
				target.Created ^= 1
			}
			checks, snapshots := 0, 0
			alive, err := checkRecoveryProcessWithSnapshot(target,
				func(recoveryProcessIdentity) (bool, error) {
					checks++
					return false, windows.ERROR_ACCESS_DENIED // Explicit injection.
				}, func() ([]byte, error) {
					snapshots++
					return data, nil
				})
			if alive || checks != 1 || snapshots != 1 {
				t.Fatalf("snapshot granted verification or used wrong query path: alive=%v checks=%d snapshots=%d", alive, checks, snapshots)
			}
			if stale {
				if err != nil {
					t.Fatalf("independent native creation mismatch remained unknown: %v", err)
				}
			} else if !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
				t.Fatalf("equal creation incorrectly removed the primary denial: %v", err)
			}
		})
	}
}

func TestRecoverySnapshotParserDisproofAndMalformedRemainUnknown(t *testing.T) {
	identity := recoverySnapshotTestIdentity()
	header := int(unsafe.Sizeof(windows.SYSTEM_PROCESS_INFORMATION{}))
	different := func() []byte { return recoverySnapshotTestData(recoverySnapshotTestEntry{4000, 101}) }
	cases := []struct {
		name       string
		data       []byte
		disproved  bool
		parseError bool
	}{
		{"different-created", different(), true, false},
		{"same-created", recoverySnapshotTestData(recoverySnapshotTestEntry{4000, 100}), false, false},
		{"target-absent", recoverySnapshotTestData(recoverySnapshotTestEntry{4004, 101}), false, false},
		{"zero-created", recoverySnapshotTestData(recoverySnapshotTestEntry{4000, 0}), false, true},
		{"negative-created", recoverySnapshotTestData(recoverySnapshotTestEntry{4000, uint64(1) << 63}), false, true},
		{"negative-minus-one", recoverySnapshotTestData(recoverySnapshotTestEntry{4000, ^uint64(0)}), false, true},
		{"duplicate-target", recoverySnapshotTestData(recoverySnapshotTestEntry{4000, 101}, recoverySnapshotTestEntry{4000, 102}), false, true},
		{"duplicate-unrelated-after-target", recoverySnapshotTestData(recoverySnapshotTestEntry{4000, 101}, recoverySnapshotTestEntry{4004, 1}, recoverySnapshotTestEntry{4004, 2}), false, true},
		{"empty", nil, false, true},
		{"short-first-header", make([]byte, header-1), false, true},
		{"target-before-short-last-header", recoverySnapshotTestData(recoverySnapshotTestEntry{4000, 101}, recoverySnapshotTestEntry{4004, 1})[:2*header-1], false, true},
		{"idle-entry-before-target", recoverySnapshotTestData(recoverySnapshotTestEntry{0, 0}, recoverySnapshotTestEntry{4000, 101}), true, false},
	}
	for _, item := range []struct {
		name string
		next uint32
	}{
		{"offset-smaller-than-header", uint32(header - 1)},
		{"offset-beyond-buffer", uint32(2*header + 1)},
		{"offset-exactly-at-buffer-end", uint32(2 * header)},
		{"offset-misaligned", uint32(header + 1)},
		{"offset-uint32-overflow", ^uint32(0)},
	} {
		data := recoverySnapshotTestData(recoverySnapshotTestEntry{4000, 101}, recoverySnapshotTestEntry{4004, 1})
		binary.LittleEndian.PutUint32(data[:4], item.next)
		cases = append(cases, struct {
			name       string
			data       []byte
			disproved  bool
			parseError bool
		}{item.name, data, false, true})
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			disproved, err := recoveryProcessSnapshotDisprovesInstance(item.data, identity)
			if disproved != item.disproved || (err != nil) != item.parseError {
				t.Fatalf("unexpected parser result: disproved=%v err=%v", disproved, err)
			}
			alive, err := checkRecoveryProcessWithSnapshot(identity,
				func(recoveryProcessIdentity) (bool, error) { return false, windows.ERROR_ACCESS_DENIED },
				func() ([]byte, error) { return item.data, nil })
			if alive {
				t.Fatal("scalar snapshot granted live/image verification")
			}
			if item.disproved {
				if err != nil {
					t.Fatalf("positive replacement proof was discarded: %v", err)
				}
			} else if !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
				t.Fatalf("unknown snapshot lost the primary access denial: %v", err)
			}
		})
	}
}

func TestRecoverySnapshotWrapperOnlyErrorsFallback(t *testing.T) {
	identity := recoverySnapshotTestIdentity()
	apiFailure := errors.New("synthetic NT query failure")
	for _, item := range []struct {
		name         string
		primaryAlive bool
		primaryErr   error
		snapshotErr  error
		wantAlive    bool
		wantError    bool
		wantSnapshot int
	}{
		{"verified-handle-does-not-snapshot", true, nil, apiFailure, true, false, 0},
		{"absent-handle-does-not-snapshot", false, nil, apiFailure, false, false, 0},
		{"denial-and-api-failure-stay-unknown", false, windows.ERROR_ACCESS_DENIED, apiFailure, false, true, 1},
		{"error-with-true-never-grants-verification", true, windows.ERROR_ACCESS_DENIED, apiFailure, false, true, 1},
		{"error-and-replaced-is-only-negative-proof", true, windows.ERROR_ACCESS_DENIED, nil, false, false, 1},
	} {
		t.Run(item.name, func(t *testing.T) {
			checks, snapshots := 0, 0
			alive, err := checkRecoveryProcessWithSnapshot(identity,
				func(recoveryProcessIdentity) (bool, error) {
					checks++
					return item.primaryAlive, item.primaryErr
				}, func() ([]byte, error) {
					snapshots++
					return recoverySnapshotTestData(recoverySnapshotTestEntry{4000, 101}), item.snapshotErr
				})
			if alive != item.wantAlive || (err != nil) != item.wantError || checks != 1 || snapshots != item.wantSnapshot {
				t.Fatalf("wrong fallback contract: alive=%v err=%v checks=%d snapshots=%d", alive, err, checks, snapshots)
			}
			if item.wantError && (!errors.Is(err, item.primaryErr) || !errors.Is(err, apiFailure)) {
				t.Fatalf("joined fallback error lost its cause: %v", err)
			}
		})
	}
	for _, invalid := range []recoveryProcessIdentity{
		{}, {PID: -1, Created: 1, Image: identity.Image},
		{PID: identity.PID, Image: identity.Image},
		{PID: identity.PID, Created: identity.Created},
	} {
		if alive, err := checkRecoveryProcessWithSnapshot(invalid,
			func(recoveryProcessIdentity) (bool, error) {
				t.Fatal("invalid identity reached handle query")
				return true, nil
			},
			func() ([]byte, error) { t.Fatal("invalid identity reached snapshot query"); return nil, nil }); alive || err != nil {
			t.Fatalf("invalid identity changed the existing false/no-query contract: alive=%v err=%v", alive, err)
		}
	}
}

func TestRecoverySnapshotQueryBoundedGrowthAndLengths(t *testing.T) {
	t.Run("reported-size-grows-and-trims", func(t *testing.T) {
		calls := 0
		want := recoverySnapshotTestData(recoverySnapshotTestEntry{4000, 101})
		data, err := readRecoveryProcessSnapshotWithQuery(func(buffer []byte) (uint32, error) {
			calls++
			if calls == 1 {
				if len(buffer) != recoveryProcessSnapshotInitialBytes {
					t.Fatal("query did not start at its bounded initial capacity")
				}
				return uint32(3 * len(buffer)), windows.STATUS_INFO_LENGTH_MISMATCH
			}
			if calls != 2 || len(buffer) != 3*recoveryProcessSnapshotInitialBytes {
				t.Fatal("query ignored the required buffer size")
			}
			copy(buffer, want)
			return uint32(len(want)), nil
		})
		if err != nil || calls != 2 || !bytes.Equal(data, want) {
			t.Fatalf("bounded resize/return length failed: length=%d calls=%d err=%v", len(data), calls, err)
		}
	})
	t.Run("missing-required-size-still-doubles", func(t *testing.T) {
		calls := 0
		_, err := readRecoveryProcessSnapshotWithQuery(func(buffer []byte) (uint32, error) {
			calls++
			if calls == 1 {
				return 0, windows.STATUS_INFO_LENGTH_MISMATCH
			}
			if len(buffer) != 2*recoveryProcessSnapshotInitialBytes {
				t.Fatal("zero returned size did not trigger forward growth")
			}
			return uint32(unsafe.Sizeof(windows.SYSTEM_PROCESS_INFORMATION{})), nil
		})
		if err != nil || calls != 2 {
			t.Fatalf("resize without returned size failed: calls=%d err=%v", calls, err)
		}
	})
	for _, item := range []struct {
		name     string
		returned func([]byte) uint32
		err      error
	}{
		{"success-zero-length", func([]byte) uint32 { return 0 }, nil},
		{"success-length-beyond-capacity", func(b []byte) uint32 { return uint32(len(b) + 1) }, nil},
		{"required-size-exceeds-limit", func([]byte) uint32 { return uint32(recoveryProcessSnapshotMaximumBytes + 1) }, windows.STATUS_INFO_LENGTH_MISMATCH},
		{"required-size-uint32-max", func([]byte) uint32 { return ^uint32(0) }, windows.STATUS_INFO_LENGTH_MISMATCH},
		{"NT-api-access-denied", func([]byte) uint32 { return 0 }, windows.STATUS_ACCESS_DENIED},
		{"Win32-buffer-error-is-not-NT-resize", func([]byte) uint32 { return 0 }, windows.ERROR_INSUFFICIENT_BUFFER},
	} {
		t.Run(item.name, func(t *testing.T) {
			calls := 0
			data, err := readRecoveryProcessSnapshotWithQuery(func(buffer []byte) (uint32, error) {
				calls++
				return item.returned(buffer), item.err
			})
			if err == nil || data != nil || calls != 1 {
				t.Fatalf("bad length/API result retried or succeeded: len=%d calls=%d err=%v", len(data), calls, err)
			}
			if item.err != nil && !errors.Is(item.err, windows.STATUS_INFO_LENGTH_MISMATCH) && !errors.Is(err, item.err) {
				t.Fatalf("NT query lost the API error: %v", err)
			}
		})
	}
	t.Run("exact-capacity-allowed-but-further-growth-denied", func(t *testing.T) {
		calls := 0
		data, err := readRecoveryProcessSnapshotWithQuery(func(buffer []byte) (uint32, error) {
			calls++
			if len(buffer) > recoveryProcessSnapshotMaximumBytes || calls > recoveryProcessSnapshotMaximumTries {
				t.Fatal("snapshot exceeded its memory/retry limits")
			}
			if calls == 1 {
				return uint32(recoveryProcessSnapshotMaximumBytes), windows.STATUS_INFO_LENGTH_MISMATCH
			}
			if calls != 2 || len(buffer) != recoveryProcessSnapshotMaximumBytes {
				t.Fatal("snapshot did not reach its exact permitted cap")
			}
			return 0, windows.STATUS_INFO_LENGTH_MISMATCH
		})
		if err == nil || data != nil || calls != 2 {
			t.Fatalf("snapshot did not stop growing at cap: len=%d calls=%d err=%v", len(data), calls, err)
		}
	})
	t.Run("short-success-length-remains-unknown-at-parser", func(t *testing.T) {
		identity := recoverySnapshotTestIdentity()
		alive, err := checkRecoveryProcessWithSnapshot(identity,
			func(recoveryProcessIdentity) (bool, error) { return false, windows.ERROR_ACCESS_DENIED },
			func() ([]byte, error) {
				return readRecoveryProcessSnapshotWithQuery(func([]byte) (uint32, error) { return 1, nil })
			})
		if alive || !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
			t.Fatalf("nonzero but truncated snapshot cleared unknown state: alive=%v err=%v", alive, err)
		}
	})
}
