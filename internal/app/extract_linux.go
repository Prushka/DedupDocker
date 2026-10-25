//go:build linux

package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
)

func syncArchiveDirectory(file *os.File) error { return file.Sync() }

func setArchiveOwnership(file *os.File, source os.FileInfo, directory bool) error {
	stat, ok := source.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("cannot determine archive ownership")
	}
	actual, err := file.Stat()
	if err != nil {
		return err
	}
	if !sameOwnership(source, actual) {
		if err := file.Chown(int(stat.Uid), int(stat.Gid)); err != nil {
			return fmt.Errorf("cannot preserve archive ownership on output: %w", err)
		}
	}
	if err := clearInheritedACL(file, directory); err != nil {
		return err
	}
	return errors.Join(file.Chmod(archiveOutputMode(source, directory)), file.Sync())
}

func syncArchiveParent(path string) (resultErr error) {
	parent, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, parent.Close()) }()
	return parent.Sync()
}

func publishArchiveDirectory(source *fileRecord, destination string, expectedParent os.FileInfo) error {
	parent, err := os.Open(filepath.Dir(destination))
	if err != nil {
		return err
	}
	defer parent.Close()
	actual, err := parent.Stat()
	if err != nil || !os.SameFile(expectedParent, actual) {
		return errors.Join(err, fmt.Errorf("extraction parent directory changed"))
	}
	if err := source.verify(); err != nil {
		return err
	}
	return unix.Renameat2(unix.AT_FDCWD, source.path, int(parent.Fd()), filepath.Base(destination), unix.RENAME_NOREPLACE)
}
