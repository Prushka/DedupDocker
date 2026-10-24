//go:build windows

package app

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

func extendedWindowsPath(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if strings.HasPrefix(absolute, `\\?\`) {
		return absolute, nil
	}
	if strings.HasPrefix(absolute, `\\`) {
		return `\\?\UNC\` + absolute[2:], nil
	}
	return `\\?\` + absolute, nil
}

func openWindowsHandle(path string, access, share uint32) (*os.File, error) {
	extended, err := extendedWindowsPath(path)
	if err != nil {
		return nil, err
	}
	name, err := windows.UTF16PtrFromString(extended)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(name, access, share, nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return os.NewFile(uintptr(handle), path), nil
}

func inspectFile(path string) (info os.FileInfo, resultErr error) {
	file, err := openWindowsHandle(path, windows.FILE_READ_ATTRIBUTES, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE)
	if err != nil {
		return nil, err
	}
	defer func() { resultErr = errors.Join(resultErr, file.Close()) }()
	return file.Stat()
}

func openRegular(path string) (*os.File, error) {
	return openWindowsHandle(path, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE)
}

func contentSupportedHandle(file *os.File) (bool, error) {
	// FILE_STREAM_INFO contains an offset-linked list with a UTF-16 stream
	// name at byte 24. Inspect the captured handle, never a mutable pathname.
	for size := 1024; size <= 16*1024*1024; size *= 2 {
		buffer := make([]byte, size)
		err := windows.GetFileInformationByHandleEx(windows.Handle(file.Fd()), windows.FileStreamInfo, &buffer[0], uint32(len(buffer)))
		if errors.Is(err, windows.ERROR_MORE_DATA) || errors.Is(err, windows.ERROR_INSUFFICIENT_BUFFER) {
			continue
		}
		if errors.Is(err, windows.ERROR_HANDLE_EOF) {
			return true, nil
		}
		if errors.Is(err, windows.ERROR_INVALID_PARAMETER) || errors.Is(err, windows.ERROR_NOT_SUPPORTED) || errors.Is(err, windows.ERROR_INVALID_FUNCTION) {
			// Unknown stream semantics cannot safely be deduplicated or copied.
			return false, nil
		}
		if err != nil {
			return false, fmt.Errorf("inspect streams of %q: %w", file.Name(), err)
		}
		for offset := 0; ; {
			if offset+24 > len(buffer) {
				return false, fmt.Errorf("invalid stream information for %q", file.Name())
			}
			next := int(binary.LittleEndian.Uint32(buffer[offset:]))
			length := int(binary.LittleEndian.Uint32(buffer[offset+4:]))
			if length%2 != 0 || length > len(buffer)-offset-24 {
				return false, fmt.Errorf("invalid stream name for %q", file.Name())
			}
			name := make([]uint16, length/2)
			for index := range name {
				name[index] = binary.LittleEndian.Uint16(buffer[offset+24+index*2:])
			}
			if string(utf16.Decode(name)) != "::$DATA" {
				return false, nil
			}
			if next == 0 {
				return true, nil
			}
			if next < 24+length || next > len(buffer)-offset-24 {
				return false, fmt.Errorf("invalid stream list for %q", file.Name())
			}
			offset += next
		}
	}
	return false, fmt.Errorf("stream information is too large for %q", file.Name())
}

func setFileTimes(file *os.File, stamp time.Time) error {
	timestamp := windows.NsecToFiletime(stamp.UnixNano())
	return windows.SetFileTime(windows.Handle(file.Fd()), nil, &timestamp, &timestamp)
}

func crossDeviceError(err error) bool { return errors.Is(err, windows.ERROR_NOT_SAME_DEVICE) }

type windowsCapturedFile struct {
	fd           *os.File
	parent       *os.File
	original     *fileRecord
	captured     *fileRecord
	identityOnly bool
}

func openWindowsDirectory(path string) (*os.File, error) {
	file, err := openWindowsHandle(path, windows.FILE_READ_ATTRIBUTES|windows.SYNCHRONIZE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		_ = file.Close()
		return nil, errors.Join(err, fmt.Errorf("not a regular parent directory: %q", path))
	}
	return file, nil
}

func captureFile(file *fileRecord, identityOnly, read bool) (capturedFile, error) {
	if !file.info.Mode().IsRegular() {
		return nil, fmt.Errorf("cannot capture nonregular file %q", file.path)
	}
	parent, err := openWindowsDirectory(filepath.Dir(file.path))
	if err != nil {
		return nil, err
	}
	name, err := windows.NewNTUnicodeString(filepath.Base(file.path))
	if err != nil {
		_ = parent.Close()
		return nil, err
	}
	attributes := windows.OBJECT_ATTRIBUTES{RootDirectory: windows.Handle(parent.Fd()), ObjectName: name}
	attributes.Length = uint32(unsafe.Sizeof(attributes))
	access := uint32(windows.FILE_READ_ATTRIBUTES | windows.DELETE | windows.SYNCHRONIZE)
	if read {
		access |= windows.GENERIC_READ
	}
	var handle windows.Handle
	var status windows.IO_STATUS_BLOCK
	// Excluding share-write and share-delete keeps the captured file's data and
	// name stable until its verified rename/deletion completes.
	err = windows.NtCreateFile(&handle, access, &attributes, &status, nil, 0, windows.FILE_SHARE_READ,
		windows.FILE_OPEN, windows.FILE_OPEN_REPARSE_POINT|windows.FILE_OPEN_FOR_BACKUP_INTENT|windows.FILE_SYNCHRONOUS_IO_NONALERT, 0, 0)
	if err != nil {
		_ = parent.Close()
		return nil, &os.PathError{Op: "capture", Path: file.path, Err: windowsError(err)}
	}
	guard := &windowsCapturedFile{fd: os.NewFile(uintptr(handle), file.path), parent: parent, original: file, identityOnly: identityOnly}
	info, err := guard.fd.Stat()
	if err != nil {
		return nil, errors.Join(err, guard.close())
	}
	if err := validateCaptured(file, info, identityOnly); err != nil {
		return nil, errors.Join(err, guard.close())
	}
	guard.captured = &fileRecord{path: file.path, info: info, ctx: file.ctx}
	return guard, nil
}

func windowsError(err error) error {
	if status, ok := err.(windows.NTStatus); ok {
		return status.Errno()
	}
	return err
}

func (file *windowsCapturedFile) handle() *os.File    { return file.fd }
func (file *windowsCapturedFile) record() *fileRecord { return file.captured }

// The copy was flushed before publication. Windows capture locks its published
// name in place, so it needs no additional staging-directory synchronization.
func (file *windowsCapturedFile) sync() error { return nil }

func (file *windowsCapturedFile) remove() error {
	actual, err := file.fd.Stat()
	if err != nil {
		return err
	}
	if err := validateCaptured(file.original, actual, file.identityOnly); err != nil {
		return err
	}
	flags := uint32(windows.FILE_DISPOSITION_DELETE | windows.FILE_DISPOSITION_POSIX_SEMANTICS | windows.FILE_DISPOSITION_IGNORE_READONLY_ATTRIBUTE)
	err = windows.SetFileInformationByHandle(windows.Handle(file.fd.Fd()), windows.FileDispositionInfoEx,
		(*byte)(unsafe.Pointer(&flags)), uint32(unsafe.Sizeof(flags)))
	if err == nil {
		return nil
	}
	if !errors.Is(err, windows.ERROR_INVALID_PARAMETER) && !errors.Is(err, windows.ERROR_NOT_SUPPORTED) && !errors.Is(err, windows.ERROR_INVALID_FUNCTION) {
		return err
	}
	if actual.Mode().Perm()&0222 == 0 {
		return fmt.Errorf("safe deletion of read-only files is unsupported: %w", err)
	}
	// The legacy handle operation is safe for writable files. Unlike os.Remove,
	// it never clears attributes shared with surviving hardlinks.
	deleteFile := byte(1)
	return windows.SetFileInformationByHandle(windows.Handle(file.fd.Fd()), windows.FileDispositionInfo, &deleteFile, 1)
}

type windowsRenameInformation struct {
	ReplaceIfExists uint32
	RootDirectory   windows.Handle
	FileNameLength  uint32
	FileName        [1]uint16
}

func renameWindowsHandle(file, directory *os.File, name string) error {
	encoded, err := windows.UTF16FromString(name)
	if err != nil {
		return err
	}
	nameLength := (len(encoded) - 1) * 2
	var header windowsRenameInformation
	buffer := make([]byte, int(unsafe.Offsetof(header.FileName))+nameLength)
	information := (*windowsRenameInformation)(unsafe.Pointer(&buffer[0]))
	information.RootDirectory = windows.Handle(directory.Fd())
	information.FileNameLength = uint32(nameLength)
	copy(unsafe.Slice(&information.FileName[0], nameLength/2), encoded[:len(encoded)-1])
	var status windows.IO_STATUS_BLOCK
	err = windows.NtSetInformationFile(windows.Handle(file.Fd()), &status, &buffer[0], uint32(len(buffer)), windows.FileRenameInformation)
	return windowsError(err)
}

func (file *windowsCapturedFile) move(destination string) (*fileRecord, error) {
	parent, err := openWindowsDirectory(filepath.Dir(destination))
	if err != nil {
		return nil, err
	}
	defer parent.Close()
	if err := renameWindowsHandle(file.fd, parent, filepath.Base(destination)); err != nil {
		return nil, &os.LinkError{Op: "move without replacement", Old: file.original.path, New: destination, Err: err}
	}
	result := &fileRecord{path: destination, info: file.captured.info, ctx: file.original.ctx}
	if err := result.verify(); err != nil {
		rollbackErr := renameWindowsHandle(file.fd, file.parent, filepath.Base(file.original.path))
		if rollbackErr != nil {
			rollbackErr = fmt.Errorf("cannot restore %q without overwriting another file; moved file retained at %q: %w", file.original.path, destination, rollbackErr)
		}
		return nil, errors.Join(err, rollbackErr)
	}
	return result, nil
}

func (file *windowsCapturedFile) close() error {
	return errors.Join(file.fd.Close(), file.parent.Close())
}
