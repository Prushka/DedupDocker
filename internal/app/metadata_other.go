//go:build !linux

package app

import "os"

func directoryIdentity(os.FileInfo) (directoryID, bool) { return directoryID{}, false }
func directoryIsMountPoint(string) (bool, error)        { return false, nil }
func sameOwnership(os.FileInfo, os.FileInfo) bool       { return true }

func preserveCopyMetadata(file *os.File, source os.FileInfo) error {
	return file.Chmod(source.Mode())
}
