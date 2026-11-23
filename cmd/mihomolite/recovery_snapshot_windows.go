//go:build windows

package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// OPT-001: a reboot can reuse an old core PID for an inaccessible system process.
// A successful, complete kernel process snapshot can DISPROVE that old instance
// using its raw creation FILETIME. It never proves image/ownership, grants stop
// authority, or turns access denied alone into an absent process.
func checkRecoveryProcessWithSnapshot(identity recoveryProcessIdentity, check func(recoveryProcessIdentity) (bool, error), snapshot func() ([]byte, error)) (bool, error) {
	if !validRecoveryProcessIdentity(identity) {
		return false, nil
	}
	alive, checkErr := check(identity)
	if checkErr == nil {
		return alive, nil
	}
	data, err := snapshot()
	if err != nil {
		return false, errors.Join(checkErr, fmt.Errorf("query independent recovery process snapshot: %w", err))
	}
	disproved, err := recoveryProcessSnapshotDisprovesInstance(data, identity)
	if err != nil {
		return false, errors.Join(checkErr, err)
	}
	if !disproved {
		return false, checkErr // Equal/missing PID never verifies a live process/image.
	}
	return false, nil // Positively a different process instance; never a stop target.
}

const (
	recoveryProcessSnapshotInitialBytes = 64 << 10
	recoveryProcessSnapshotMaximumBytes = 32 << 20
	recoveryProcessSnapshotMaximumTries = 12
)

func readRecoveryProcessSnapshot() ([]byte, error) {
	return readRecoveryProcessSnapshotWithQuery(func(buffer []byte) (uint32, error) {
		var returned uint32
		err := windows.NtQuerySystemInformation(windows.SystemProcessInformation, unsafe.Pointer(&buffer[0]), uint32(len(buffer)), &returned)
		return returned, err
	})
}

func readRecoveryProcessSnapshotWithQuery(query func([]byte) (uint32, error)) ([]byte, error) {
	size := recoveryProcessSnapshotInitialBytes
	for attempt := 0; attempt < recoveryProcessSnapshotMaximumTries; attempt++ {
		buffer := make([]byte, size)
		returned, err := query(buffer)
		if err == nil {
			if returned == 0 || uint64(returned) > uint64(len(buffer)) {
				return nil, errors.New("incomplete recovery process snapshot length")
			}
			return buffer[:int(returned)], nil
		}
		if !errors.Is(err, windows.STATUS_INFO_LENGTH_MISMATCH) {
			return nil, err
		}
		nextSize := uint64(size) * 2
		if uint64(returned) > nextSize {
			nextSize = uint64(returned)
		}
		if nextSize > recoveryProcessSnapshotMaximumBytes {
			return nil, errors.New("recovery process snapshot exceeds size limit")
		}
		size = int(nextSize)
	}
	return nil, errors.New("recovery process snapshot did not stabilize")
}

// Read scalar fields only: never dereference the snapshot's ImageName pointers.
// Validate the entire linked list before returning even a PID/creation mismatch.
func recoveryProcessSnapshotDisprovesInstance(data []byte, identity recoveryProcessIdentity) (bool, error) {
	var layout windows.SYSTEM_PROCESS_INFORMATION
	headerBytes := int(unsafe.Sizeof(layout))
	pointerBytes := int(unsafe.Sizeof(uintptr(0)))
	pidOffset := int(unsafe.Offsetof(layout.UniqueProcessID))
	createdOffset := int(unsafe.Offsetof(layout.CreateTime))
	seen := make(map[uint64]bool)
	disproved := false
	for offset := 0; ; {
		if len(data)-offset < headerBytes {
			return false, errors.New("truncated recovery process snapshot entry")
		}
		entry := data[offset:]
		next := binary.LittleEndian.Uint32(entry[:4])
		pid := uint64(binary.LittleEndian.Uint32(entry[pidOffset : pidOffset+pointerBytes]))
		if pointerBytes == 8 {
			pid = binary.LittleEndian.Uint64(entry[pidOffset : pidOffset+pointerBytes])
		}
		if seen[pid] {
			return false, errors.New("duplicate PID in recovery process snapshot")
		}
		seen[pid] = true
		if pid == uint64(identity.PID) {
			created := binary.LittleEndian.Uint64(entry[createdOffset : createdOffset+8])
			if created == 0 || int64(created) < 0 {
				return false, errors.New("missing creation identity in recovery process snapshot")
			}
			disproved = created != identity.Created
		}
		if next == 0 {
			return disproved, nil
		}
		if uint64(next) < uint64(headerBytes) || uint64(next) > uint64(len(entry)) || int(next)%pointerBytes != 0 {
			return false, errors.New("invalid recovery process snapshot entry offset")
		}
		offset += int(next)
	}
}
