//go:build !linux && !windows

package app

import (
	"fmt"
	"os"
)

func syncArchiveDirectory(*os.File) error { return nil }
func syncArchiveParent(string) error      { return nil }
func setArchiveOwnership(*os.File, os.FileInfo, bool) error {
	return fmt.Errorf("safe extraction is unsupported on this platform")
}

func publishArchiveDirectory(*fileRecord, string, os.FileInfo) error {
	return fmt.Errorf("safe archive publication is unsupported on this platform")
}
