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
	sync() error
	close() error
}

func validateCaptured(expected *fileRecord, actual os.FileInfo, identityOnly bool) error {
	if !actual.Mode().IsRegular() || !os.SameFile(expected.info, actual) ||
		(!identityOnly && !sameVersion(expected.info, actual)) {
		return fmt.Errorf("file changed while capturing %q", expected.path)
	}
	return nil
}

func removeVerified(file *fileRecord) error {
	return removeCaptured(file, false)
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
	duplicate, err := captureFile(source, false, true)
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, duplicate.close()) }()
	equal, err = sameContents(duplicate.record(), captured.record())
	if err != nil {
		return err
	}
	if !equal {
		return fmt.Errorf("duplicate content changed: %q and %q", source.path, keeper.path)
	}
	// The retained file may be on another mount. Persist its staged name before
	// unlinking a source whose filesystem could commit independently.
	if err := captured.sync(); err != nil {
		return err
	}
	if err := captured.record().verify(); err != nil {
		return err
	}
	if err := contextError(source.ctx); err != nil {
		return err
	}
	return duplicate.remove()
}

func removeCreated(file *fileRecord) error {
	return removeCaptured(file, true)
}

func removeCaptured(file *fileRecord, identityOnly bool) (resultErr error) {
	captured, err := captureFile(file, identityOnly, false)
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, captured.close()) }()
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
