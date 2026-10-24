package app

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
)

func newFileRecord(path string, info os.FileInfo) (*fileRecord, error) {
	// Go 1.22 on Windows resolves file IDs lazily. Capture the ID now, before
	// any move or replacement can change what the original path refers to.
	if !os.SameFile(info, info) {
		return nil, fmt.Errorf("cannot capture file identity: %q", path)
	}
	return &fileRecord{path: path, info: info}, nil
}

func sameVersion(expected, actual os.FileInfo) bool {
	if !os.SameFile(expected, actual) || expected.Mode() != actual.Mode() {
		return false
	}
	// Directory timestamps and sizes change as this plan edits their children.
	return expected.IsDir() || (expected.Size() == actual.Size() && expected.ModTime().Equal(actual.ModTime()))
}

func (file *fileRecord) verify() error {
	actual, err := os.Lstat(file.path)
	if err != nil {
		return fmt.Errorf("inspect %q: %w", file.path, err)
	}
	if actual.Mode()&os.ModeSymlink != 0 || !sameVersion(file.info, actual) {
		return fmt.Errorf("file changed since scan: %q", file.path)
	}
	return nil
}

func (file *fileRecord) open() (*os.File, error) {
	if !file.info.Mode().IsRegular() {
		return nil, fmt.Errorf("not a regular file: %q", file.path)
	}
	if err := file.verify(); err != nil {
		return nil, err
	}
	handle, err := os.Open(file.path)
	if err != nil {
		return nil, fmt.Errorf("open %q: %w", file.path, err)
	}
	actual, err := handle.Stat()
	if err != nil || !sameVersion(file.info, actual) {
		_ = handle.Close()
		return nil, fmt.Errorf("file changed while opening %q: %w", file.path, errors.Join(err, errors.New("metadata mismatch")))
	}
	return handle, nil
}

func hashFile(file *fileRecord) (digest string, resultErr error) {
	handle, err := file.open()
	if err != nil {
		return "", err
	}
	defer func() { resultErr = errors.Join(resultErr, handle.Close()) }()
	hash := sha256.New()
	if _, err := io.Copy(hash, handle); err != nil {
		return "", fmt.Errorf("hash %q: %w", file.path, err)
	}
	if err := file.verify(); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", hash.Sum(nil)), nil
}

func sameContents(first, second *fileRecord) (equal bool, resultErr error) {
	a, err := first.open()
	if err != nil {
		return false, err
	}
	defer func() { resultErr = errors.Join(resultErr, a.Close()) }()
	b, err := second.open()
	if err != nil {
		return false, err
	}
	defer func() { resultErr = errors.Join(resultErr, b.Close()) }()
	left, right := make([]byte, 64*1024), make([]byte, 64*1024)
	for {
		n, aErr := io.ReadFull(a, left)
		m, bErr := io.ReadFull(b, right)
		if aErr != nil && aErr != io.EOF && aErr != io.ErrUnexpectedEOF {
			return false, fmt.Errorf("read %q: %w", first.path, aErr)
		}
		if bErr != nil && bErr != io.EOF && bErr != io.ErrUnexpectedEOF {
			return false, fmt.Errorf("read %q: %w", second.path, bErr)
		}
		if n != m || !bytes.Equal(left[:n], right[:m]) {
			return false, nil
		}
		if aErr != nil || bErr != nil {
			break
		}
	}
	if err := errors.Join(first.verify(), second.verify()); err != nil {
		return false, err
	}
	return true, nil
}

// moveNoReplace creates the destination exclusively, so even a destination
// created after planning cannot be overwritten. Linking avoids copying large
// files; exclusive, verified copying supports filesystems without hard links.
func moveNoReplace(source *fileRecord, destination string) (*fileRecord, error) {
	if err := source.verify(); err != nil {
		return nil, err
	}
	if !source.info.Mode().IsRegular() {
		return nil, fmt.Errorf("cannot move nonregular file %q", source.path)
	}
	if err := os.Link(source.path, destination); err == nil {
		linked := &fileRecord{path: destination, info: source.info}
		if err := errors.Join(source.verify(), linked.verify()); err != nil {
			return nil, errors.Join(err, removeCreated(linked))
		}
		if err := os.Remove(source.path); err != nil {
			return nil, errors.Join(fmt.Errorf("unlink moved source %q: %w", source.path, err), removeCreated(linked))
		}
		return linked, nil
	} else if os.IsExist(err) {
		return nil, fmt.Errorf("destination already exists %q: %w", destination, err)
	}
	return copyMove(source, destination)
}

func removeCreated(file *fileRecord) error {
	actual, err := os.Lstat(file.path)
	if err != nil {
		return err
	}
	if !os.SameFile(file.info, actual) {
		return fmt.Errorf("cannot clean up replaced destination %q", file.path)
	}
	return os.Remove(file.path)
}

func copyMove(source *fileRecord, destination string) (result *fileRecord, resultErr error) {
	input, err := source.open()
	if err != nil {
		return nil, err
	}
	defer func() {
		if input != nil {
			resultErr = errors.Join(resultErr, input.Close())
		}
	}()
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, source.info.Mode().Perm())
	if err != nil {
		return nil, fmt.Errorf("create move destination %q: %w", destination, err)
	}
	createdInfo, err := output.Stat()
	if err != nil {
		_ = output.Close()
		return nil, fmt.Errorf("inspect created destination %q: %w", destination, err)
	}
	created := &fileRecord{path: destination, info: createdInfo}
	closed := false
	defer func() {
		if !closed {
			resultErr = errors.Join(resultErr, output.Close())
		}
		if resultErr != nil {
			resultErr = errors.Join(resultErr, removeCreated(created))
		}
	}()
	hash := sha256.New()
	written, err := io.Copy(io.MultiWriter(output, hash), input)
	if err != nil {
		return nil, fmt.Errorf("copy %q: %w", source.path, err)
	}
	if written != source.info.Size() {
		return nil, fmt.Errorf("source size changed while copying %q", source.path)
	}
	if err := errors.Join(source.verify(), output.Chmod(source.info.Mode().Perm()), output.Sync()); err != nil {
		return nil, fmt.Errorf("verify copied file %q: %w", destination, err)
	}
	closed = true
	if err := output.Close(); err != nil {
		return nil, fmt.Errorf("close copied file %q: %w", destination, err)
	}
	if err := os.Chtimes(destination, source.info.ModTime(), source.info.ModTime()); err != nil {
		return nil, fmt.Errorf("preserve modification time %q: %w", destination, err)
	}
	info, err := os.Lstat(destination)
	if err != nil {
		return nil, err
	}
	if !os.SameFile(createdInfo, info) {
		return nil, fmt.Errorf("destination changed while copying %q", destination)
	}
	result = &fileRecord{path: destination, info: info}
	digest, err := hashFile(result)
	if err != nil {
		return nil, err
	}
	if digest != fmt.Sprintf("%x", hash.Sum(nil)) {
		return nil, fmt.Errorf("copied content differs: %q", destination)
	}
	equal, err := sameContents(source, result)
	if err != nil {
		return nil, err
	}
	if !equal {
		return nil, fmt.Errorf("source contents changed while copying %q", source.path)
	}
	if err := input.Close(); err != nil {
		return nil, err
	}
	input = nil
	if err := source.verify(); err != nil {
		return nil, err
	}
	if err := os.Remove(source.path); err != nil {
		return nil, fmt.Errorf("remove copied source %q: %w", source.path, err)
	}
	return result, nil
}
