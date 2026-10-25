//go:build linux

package app

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

func inspectFile(path string) (os.FileInfo, error) { return os.Lstat(path) }

func openRegular(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return os.NewFile(uintptr(fd), path), nil
}

func setFileTimes(file *os.File, stamp time.Time) error {
	times := [2]unix.Timespec{unix.NsecToTimespec(stamp.UnixNano()), unix.NsecToTimespec(stamp.UnixNano())}
	connection, err := file.SyscallConn()
	if err != nil {
		return err
	}
	var timestampErr error
	err = connection.Control(func(fd uintptr) {
		// The NULL-path form is Linux's futimens implementation, available
		// since 2.6.22. An empty string with AT_EMPTY_PATH requires Linux 5.8
		// and fails with EINVAL on older NAS kernels. Keep using the held file
		// descriptor so a replaced pathname cannot redirect this update.
		_, _, errno := unix.Syscall6(unix.SYS_UTIMENSAT, fd, 0, uintptr(unsafe.Pointer(&times[0])), 0, 0, 0)
		if errno != 0 {
			timestampErr = errno
		}
	})
	if err := errors.Join(err, timestampErr); err != nil {
		return &os.PathError{Op: "set file timestamps", Path: file.Name(), Err: err}
	}
	return nil
}

func crossDeviceError(err error) bool { return errors.Is(err, unix.EXDEV) }

type linuxCapturedFile struct {
	fd           *os.File
	parent       *os.File
	directory    *os.File
	original     *fileRecord
	captured     *fileRecord
	name         string
	active       bool
	identityOnly bool
}

func captureFile(file *fileRecord, identityOnly, read bool) (capturedFile, error) {
	if !file.info.Mode().IsRegular() {
		return nil, fmt.Errorf("cannot capture nonregular file %q", file.path)
	}
	actual, err := inspectFile(file.path)
	if err != nil {
		return nil, err
	}
	if err := validateCaptured(file, actual, identityOnly); err != nil {
		return nil, err
	}
	parentPath := filepath.Dir(file.path)
	parentFD, err := unix.Open(parentPath, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	parent := os.NewFile(uintptr(parentFD), parentPath)
	var random [12]byte
	if _, err := rand.Read(random[:]); err != nil {
		_ = parent.Close()
		return nil, err
	}
	name := ".dedup-source-" + hex.EncodeToString(random[:])
	if err := unix.Mkdirat(parentFD, name, 0700); err != nil {
		_ = parent.Close()
		return nil, err
	}
	directoryPath := filepath.Join(parentPath, name)
	directoryFD, err := unix.Openat(parentFD, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		_ = unix.Unlinkat(parentFD, name, unix.AT_REMOVEDIR)
		_ = parent.Close()
		return nil, err
	}
	guard := &linuxCapturedFile{
		parent: parent, directory: os.NewFile(uintptr(directoryFD), directoryPath),
		original: file, name: name, identityOnly: identityOnly,
	}
	if err := unix.Renameat2(parentFD, filepath.Base(file.path), directoryFD, "file", unix.RENAME_NOREPLACE); err != nil {
		return nil, errors.Join(err, guard.close())
	}
	guard.active = true
	flags := unix.O_PATH | unix.O_NOFOLLOW | unix.O_CLOEXEC
	if read {
		flags = unix.O_RDONLY | unix.O_NONBLOCK | unix.O_NOFOLLOW | unix.O_CLOEXEC
	}
	fd, err := unix.Openat(directoryFD, "file", flags, 0)
	if err != nil {
		return nil, errors.Join(err, guard.close())
	}
	guard.fd = os.NewFile(uintptr(fd), filepath.Join(directoryPath, "file"))
	info, err := guard.fd.Stat()
	if err != nil {
		return nil, errors.Join(err, guard.close())
	}
	if err := validateCaptured(file, info, identityOnly); err != nil {
		return nil, errors.Join(err, guard.close())
	}
	guard.captured = &fileRecord{path: guard.fd.Name(), info: info, ctx: file.ctx}
	return guard, nil
}

func (file *linuxCapturedFile) handle() *os.File    { return file.fd }
func (file *linuxCapturedFile) record() *fileRecord { return file.captured }

func (file *linuxCapturedFile) sync() error {
	return errors.Join(file.fd.Sync(), file.directory.Sync(), file.parent.Sync())
}

func (file *linuxCapturedFile) remove() error {
	actual, err := file.fd.Stat()
	if err != nil {
		return err
	}
	if err := validateCaptured(file.original, actual, file.identityOnly); err != nil {
		return err
	}
	// The private directory is held by descriptor; replacing the original name
	// or any of its parents cannot redirect this unlink to another file.
	if err := unix.Unlinkat(int(file.directory.Fd()), "file", 0); err != nil {
		return err
	}
	file.active = false
	return nil
}

func (file *linuxCapturedFile) move(destination string) (*fileRecord, error) {
	parentFD, err := unix.Open(filepath.Dir(destination), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	defer unix.Close(parentFD)
	if err := unix.Renameat2(int(file.directory.Fd()), "file", parentFD, filepath.Base(destination), unix.RENAME_NOREPLACE); err != nil {
		return nil, &os.LinkError{Op: "move without replacement", Old: file.original.path, New: destination, Err: err}
	}
	file.active = false
	result := &fileRecord{path: destination, info: file.captured.info, ctx: file.original.ctx}
	if err := result.verify(); err != nil {
		// The original was verified while private, and the rename was atomic.
		// A changed public destination belongs to another actor. Moving that
		// pathname back would risk displacing its replacement, so leave it alone.
		return nil, fmt.Errorf("moved file could not be verified at %q; destination left untouched: %w", destination, err)
	}
	return result, nil
}

func (file *linuxCapturedFile) close() error {
	var result error
	if file.active {
		err := unix.Renameat2(int(file.directory.Fd()), "file", int(file.parent.Fd()), filepath.Base(file.original.path), unix.RENAME_NOREPLACE)
		if err != nil {
			result = fmt.Errorf("cannot restore %q without overwriting another file; original retained in %q: %w", file.original.path, filepath.Join(file.directory.Name(), "file"), err)
		} else {
			file.active = false
		}
	}
	if file.fd != nil {
		result = errors.Join(result, file.fd.Close())
	}
	if !file.active {
		result = errors.Join(result, unix.Unlinkat(int(file.parent.Fd()), file.name, unix.AT_REMOVEDIR))
	}
	return errors.Join(result, file.directory.Close(), file.parent.Close())
}
