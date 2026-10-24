package app

import (
	"errors"
	"fmt"
	"os"
)

// capturedFile owns either a locked Windows handle or a file detached into a
// private Linux directory. Destructive operations act on that captured file,
// never on a pathname which another process can replace after a check.
type capturedFile interface {
	handle() *os.File
	record() *fileRecord
	remove() error
	move(string) (*fileRecord, error)
	close() error
}

func validateCaptured(expected *fileRecord, actual os.FileInfo, identityOnly bool) error {
	if !actual.Mode().IsRegular() || !os.SameFile(expected.info, actual) ||
		(!identityOnly && !sameVersion(expected.info, actual)) {
		return fmt.Errorf("file changed while capturing %q", expected.path)
	}
	return nil
}

// dedupContentSupported rejects file data or metadata which byte comparisons of
// the default stream cannot represent. Native moves can still preserve it.
func dedupContentSupported(file *fileRecord) (supported bool, resultErr error) {
	handle, err := file.open()
	if err != nil {
		return false, err
	}
	defer func() { resultErr = errors.Join(resultErr, handle.Close()) }()
	return contentSupportedHandle(handle)
}

func removeVerified(file *fileRecord) error {
	return removeCaptured(file, false, false)
}

func removeVerifiedContent(file *fileRecord) error {
	return removeCaptured(file, false, true)
}

// Hold the retained copy through comparison and source deletion. In particular,
// another process cannot remove the keeper between the byte check and unlink.
func removeDuplicateVerified(source, keeper *fileRecord) (resultErr error) {
	// A known mismatch should remain a no-op, including directory timestamps
	// on Linux where capturing the keeper temporarily stages its directory entry.
	equal, err := sameContents(source, keeper)
	if err != nil {
		return err
	}
	if !equal {
		return fmt.Errorf("duplicate content changed: %q and %q", source.path, keeper.path)
	}
	captured, err := captureFile(keeper, false, true)
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, captured.close()) }()
	supported, err := contentSupportedHandle(captured.handle())
	if err != nil {
		return err
	}
	if !supported {
		return fmt.Errorf("additional file content or metadata prevents duplicate deletion: %q", keeper.path)
	}
	equal, err = sameContents(source, captured.record())
	if err != nil {
		return err
	}
	if !equal {
		return fmt.Errorf("duplicate content changed: %q and %q", source.path, keeper.path)
	}
	return removeVerifiedContent(source)
}

func removeCreated(file *fileRecord) error {
	return removeCaptured(file, true, false)
}

func removeCaptured(file *fileRecord, identityOnly, protectContent bool) (resultErr error) {
	captured, err := captureFile(file, identityOnly, protectContent)
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, captured.close()) }()
	if protectContent {
		supported, err := contentSupportedHandle(captured.handle())
		if err != nil {
			return err
		}
		if !supported {
			return fmt.Errorf("additional file content or metadata prevents deletion: %q", file.path)
		}
	}
	return captured.remove()
}

func nativeMoveNoReplace(source *fileRecord, destination string) (result *fileRecord, resultErr error) {
	captured, err := captureFile(source, false, false)
	if err != nil {
		return nil, err
	}
	defer func() { resultErr = errors.Join(resultErr, captured.close()) }()
	return captured.move(destination)
}
