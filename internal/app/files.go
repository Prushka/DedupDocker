package app

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

func newFileRecord(path string, info os.FileInfo) (*fileRecord, error) {
	// Capture the identity before a later move or replacement changes what
	// the original path refers to.
	if !os.SameFile(info, info) {
		return nil, fmt.Errorf("cannot capture file identity: %q", path)
	}
	return &fileRecord{path: path, info: info}, nil
}

func sameVersion(expected, actual os.FileInfo) bool {
	if !os.SameFile(expected, actual) || expected.Mode() != actual.Mode() || !sameOwnership(expected, actual) {
		return false
	}
	// Directory timestamps and sizes change as this plan edits their children.
	return expected.IsDir() || (expected.Size() == actual.Size() && expected.ModTime().Equal(actual.ModTime()))
}

func (file *fileRecord) verify() error {
	actual, err := inspectFile(file.path)
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
	handle, err := openRegular(file.path)
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
	if _, err := io.Copy(hash, cancelReader{ctx: file.ctx, reader: handle}); err != nil {
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
	aReader, bReader := cancelReader{ctx: first.ctx, reader: a}, cancelReader{ctx: second.ctx, reader: b}
	for {
		n, aErr := io.ReadFull(aReader, left)
		m, bErr := io.ReadFull(bReader, right)
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

// Native no-replace renames preserve all file streams and metadata. Copying is
// only needed across filesystems and transfers the normal file contents.
func moveNoReplace(source *fileRecord, destination string) (*fileRecord, error) {
	if err := source.verify(); err != nil {
		return nil, err
	}
	if !source.info.Mode().IsRegular() {
		return nil, fmt.Errorf("cannot move nonregular file %q", source.path)
	}
	if _, err := os.Lstat(destination); err == nil {
		return nil, fmt.Errorf("destination already exists: %q", destination)
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	result, err := nativeMoveNoReplace(source, destination)
	if err == nil || !crossDeviceError(err) {
		return result, err
	}
	return copyMove(source, destination)
}

func copyMove(source *fileRecord, destination string) (result *fileRecord, resultErr error) {
	// Reject known collisions before detaching the source or creating temporary files.
	if _, err := os.Lstat(destination); err == nil {
		return nil, fmt.Errorf("destination already exists: %q", destination)
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	captured, err := captureFile(source, false, true)
	if err != nil {
		return nil, err
	}
	defer func() { resultErr = errors.Join(resultErr, captured.close()) }()
	input := captured.handle()
	// Publish only a completely written, verified file. A consumer cannot see
	// and modify an incomplete destination while the copy is in progress.
	temporaryDir, err := os.MkdirTemp(filepath.Dir(destination), ".dedup-copy-")
	if err != nil {
		return nil, err
	}
	defer func() { resultErr = errors.Join(resultErr, os.Remove(temporaryDir)) }()
	temporaryPath := filepath.Join(temporaryDir, "file")
	output, err := os.OpenFile(temporaryPath, os.O_RDWR|os.O_CREATE|os.O_EXCL, source.info.Mode().Perm())
	if err != nil {
		return nil, fmt.Errorf("create move copy: %w", err)
	}
	createdInfo, err := output.Stat()
	if err != nil {
		_ = output.Close()
		return nil, fmt.Errorf("inspect created copy: %w", err)
	}
	created := &fileRecord{path: temporaryPath, info: createdInfo}
	closed := false
	published := false
	defer func() {
		if !closed {
			resultErr = errors.Join(resultErr, output.Close())
		}
		if !published {
			resultErr = errors.Join(resultErr, removeCreated(created))
		}
	}()
	hash := sha256.New()
	written, err := io.Copy(io.MultiWriter(output, hash), cancelReader{ctx: source.ctx, reader: input})
	if err != nil {
		return nil, fmt.Errorf("copy %q: %w", source.path, err)
	}
	if written != source.info.Size() {
		return nil, fmt.Errorf("source size changed while copying %q", source.path)
	}
	if err := errors.Join(captured.record().verify(), preserveCopyMetadata(output, source.info), setFileTimes(output, source.info.ModTime()), output.Sync()); err != nil {
		return nil, fmt.Errorf("verify copied file %q: %w", destination, err)
	}
	info, err := output.Stat()
	if err != nil {
		return nil, err
	}
	temporary := &fileRecord{path: temporaryPath, info: info, ctx: source.ctx}
	closed = true
	if err := output.Close(); err != nil {
		return nil, fmt.Errorf("close copied file %q: %w", destination, err)
	}
	digest, err := hashFile(temporary)
	if err != nil {
		return nil, err
	}
	if digest != fmt.Sprintf("%x", hash.Sum(nil)) {
		return nil, fmt.Errorf("copied content differs: %q", destination)
	}
	equal, err := sameContents(captured.record(), temporary)
	if err != nil {
		return nil, err
	}
	if !equal {
		return nil, fmt.Errorf("source contents changed while copying %q", source.path)
	}
	result, err = nativeMoveNoReplace(temporary, destination)
	if err != nil {
		return nil, err
	}
	published = true
	if err := finishCopiedMove(captured, result); err != nil {
		return nil, fmt.Errorf("remove copied source %q: %w", source.path, err)
	}
	return result, nil
}

func finishCopiedMove(source capturedFile, destination *fileRecord) (resultErr error) {
	// Hold the destination through its last byte comparison and source removal.
	// If its public name is replaced, close retains a recovery copy instead of
	// overwriting the replacement or losing the last original-content copy.
	retained, err := captureFile(destination, false, true)
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, retained.close()) }()
	equal, err := sameContents(source.record(), retained.record())
	if err != nil {
		return err
	}
	if !equal {
		return fmt.Errorf("published copy changed: %q", destination.path)
	}
	// Persist file data and both staging-directory entries before removing the
	// source on the other filesystem. A later crash leaves a recoverable copy.
	if err := retained.sync(); err != nil {
		return fmt.Errorf("persist copied destination %q: %w", destination.path, err)
	}
	if err := contextError(destination.ctx); err != nil {
		return err
	}
	return source.remove()
}
