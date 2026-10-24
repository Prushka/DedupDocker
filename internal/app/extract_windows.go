//go:build windows

package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

func syncArchiveDirectory(*os.File) error { return nil }
func syncArchiveParent(string) error      { return nil }

func setArchiveOwnership(file *os.File, source os.FileInfo, directory bool) error {
	return file.Chmod(archiveOutputMode(source, directory))
}

func publishArchiveDirectory(source *fileRecord, destination string, expectedParent os.FileInfo) error {
	directory, err := openWindowsHandle(source.path, windows.DELETE|windows.FILE_READ_ATTRIBUTES|windows.SYNCHRONIZE, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE)
	if err != nil {
		return err
	}
	defer directory.Close()
	actual, err := directory.Stat()
	if err != nil || !os.SameFile(source.info, actual) || !actual.IsDir() {
		return errors.Join(err, fmt.Errorf("extraction output directory changed"))
	}
	parent, err := openWindowsDirectory(filepath.Dir(destination))
	if err != nil {
		return err
	}
	defer parent.Close()
	actual, err = parent.Stat()
	if err != nil || !os.SameFile(expectedParent, actual) {
		return errors.Join(err, fmt.Errorf("extraction parent directory changed"))
	}
	return renameWindowsHandle(directory, parent, filepath.Base(destination))
}
