//go:build windows

package app

import (
	"errors"

	"golang.org/x/sys/windows"
)

func physicalRootPath(path string) (physical string, resultErr error) {
	// Newer Go releases classify junctions as irregular files, so
	// filepath.EvalSymlinks cannot resolve paths through them. Resolve the
	// captured directory handle instead, including all ancestor junctions.
	directory, err := openWindowsDirectory(path)
	if err != nil {
		return "", err
	}
	defer func() { resultErr = errors.Join(resultErr, directory.Close()) }()
	buffer := make([]uint16, 256)
	for {
		length, err := windows.GetFinalPathNameByHandle(windows.Handle(directory.Fd()), &buffer[0], uint32(len(buffer)), 0)
		if err != nil {
			return "", err
		}
		if length < uint32(len(buffer)) {
			return windows.UTF16ToString(buffer[:length]), nil
		}
		buffer = make([]uint16, int(length)+1)
	}
}
