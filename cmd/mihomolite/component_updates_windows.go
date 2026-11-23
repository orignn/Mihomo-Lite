//go:build windows

package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

func componentCheckPath(path string) error {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return errComponentTrust
	}
	for cursor := absolute; ; cursor = filepath.Dir(cursor) {
		ptr, e := windows.UTF16PtrFromString(cursor)
		if e != nil {
			return errComponentTrust
		}
		attrs, e := windows.GetFileAttributes(ptr)
		if e == nil {
			if attrs&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
				return errComponentTrust
			}
			if cursor != absolute && attrs&windows.FILE_ATTRIBUTE_DIRECTORY == 0 {
				return errComponentTrust
			}
		} else if !errors.Is(e, windows.ERROR_FILE_NOT_FOUND) && !errors.Is(e, windows.ERROR_PATH_NOT_FOUND) {
			return e
		}
		if filepath.Dir(cursor) == cursor {
			break
		}
	}
	return nil
}
func componentAcquireFileLock(path string) (func(), error) {
	if err := componentCheckPath(path); err != nil {
		return nil, err
	}
	ptr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(ptr, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil, windows.OPEN_ALWAYS, windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, errComponentBusy
	}
	return func() { windows.CloseHandle(h) }, nil
}

// Inspection only: never request termination rights, never terminate or wait on
// an arbitrary PID. Inaccessible candidate processes remain unknown/fail-closed.
func componentCoreStopped(root string) error {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return errComponentStopped
	}
	defer windows.CloseHandle(snapshot)
	entry := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	err = windows.Process32First(snapshot, &entry)
	target := filepath.Join(root, "runtime", "mihomo.exe")
	for err == nil {
		name := strings.ToLower(windows.UTF16ToString(entry.ExeFile[:]))
		if strings.HasPrefix(name, "mihomo") && !strings.HasPrefix(name, "mihomolite") {
			h, e := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, entry.ProcessID)
			if errors.Is(e, windows.ERROR_INVALID_PARAMETER) {
				err = windows.Process32Next(snapshot, &entry)
				continue
			}
			if e != nil {
				return errComponentStopped
			}
			image, e := recoveryProcessImage(h)
			windows.CloseHandle(h)
			if e != nil {
				return errComponentStopped
			}
			if strings.EqualFold(filepath.Clean(image), target) {
				return errComponentStopped
			}
		}
		err = windows.Process32Next(snapshot, &entry)
	}
	if !errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		return errComponentStopped
	}
	// A persisted legacy PID might refer to an untracked executable; don't turn
	// unreadable/stale ownership evidence into permission to replace its files.
	pidPath := filepath.Join(root, "data", "core.pid")
	if err = componentCheckPath(pidPath); err != nil {
		return errComponentStopped
	}
	if st, e := os.Lstat(pidPath); e == nil {
		// Any retained ownership record (even empty or malformed) needs the
		// existing recovery workflow, not component updater interpretation.
		_ = st
		return errComponentStopped
	} else if !os.IsNotExist(e) {
		return errComponentStopped
	}
	return nil
}

func componentRenameNoReplace(source, target string) error {
	if err := componentCheckPath(source); err != nil {
		return err
	}
	if err := componentCheckPath(target); err != nil {
		return err
	}
	src, err := windows.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	dst, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	// Without MOVEFILE_REPLACE_EXISTING, even a concurrent target creation is
	// refused by the kernel. WRITE_THROUGH makes the directory move durable.
	return windows.MoveFileEx(src, dst, windows.MOVEFILE_WRITE_THROUGH)
}
