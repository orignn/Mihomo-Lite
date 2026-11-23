//go:build windows

package main

import (
	"errors"
	"golang.org/x/sys/windows"
	"path/filepath"
)

// Reject every existing reparse ancestor before accessing managed backup data.
// Nonexistent leaves are allowed so a new installation can import a backup.
func checkBackupPath(path string) error {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return errInvalidBackup
	}
	for cursor := absolute; ; cursor = filepath.Dir(cursor) {
		name, err := windows.UTF16PtrFromString(cursor)
		if err != nil {
			return errInvalidBackup
		}
		attrs, err := windows.GetFileAttributes(name)
		if err == nil {
			if attrs&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
				return errInvalidBackup
			}
			if cursor != absolute && attrs&windows.FILE_ATTRIBUTE_DIRECTORY == 0 {
				return errInvalidBackup
			}
		} else if !errors.Is(err, windows.ERROR_FILE_NOT_FOUND) && !errors.Is(err, windows.ERROR_PATH_NOT_FOUND) {
			return err
		}
		if filepath.Dir(cursor) == cursor {
			break
		}
	}
	return nil
}
