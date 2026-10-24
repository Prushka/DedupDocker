//go:build !windows && !linux

package app

import (
	"fmt"
	"os"
	"time"
)

func inspectFile(path string) (os.FileInfo, error) { return os.Lstat(path) }

func openRegular(path string) (*os.File, error) {
	return nil, fmt.Errorf("safe file operations are unsupported on this platform: %q", path)
}

func contentSupportedHandle(file *os.File) (bool, error) { return false, nil }

func setFileTimes(file *os.File, stamp time.Time) error {
	return fmt.Errorf("descriptor timestamps are unsupported on this platform")
}

func crossDeviceError(err error) bool { return false }

func removeEmptyDirectory(file *fileRecord) error {
	return fmt.Errorf("safe directory removal is unsupported on this platform: %q", file.path)
}

func captureFile(file *fileRecord, identityOnly, read bool) (capturedFile, error) {
	return nil, fmt.Errorf("safe file operations are unsupported on this platform: %q", file.path)
}
