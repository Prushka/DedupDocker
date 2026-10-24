package app

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSafetyDeleteRejectsReplacementWithMatchingMetadata(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, map[string]string{"source": "same"})
	source, retained := filepath.Join(root, "source"), filepath.Join(root, "original")
	file := recordFile(t, source)
	if err := os.Rename(source, retained); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, root, map[string]string{"source": "DIFF"})
	if err := os.Chtimes(source, file.info.ModTime(), file.info.ModTime()); err != nil {
		t.Fatal(err)
	}
	if err := removeVerified(file); err == nil {
		t.Fatal("deleted a replacement file")
	}
	assertLayout(t, root, map[string]string{"source": "DIFF", "original": "same"})
}

func TestSafetyCopyPreservesReadOnlyPermissions(t *testing.T) {
	root := t.TempDir()
	source, destination := filepath.Join(root, "source"), filepath.Join(root, "destination")
	if err := os.WriteFile(source, []byte("read-only data"), 0444); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(source, 0666); _ = os.Chmod(destination, 0666) })
	file := recordFile(t, source)
	moved, err := copyMove(file, destination)
	if err != nil {
		t.Fatal(err)
	}
	if err := moved.verify(); err != nil {
		t.Fatal(err)
	}
	if moved.info.Mode() != file.info.Mode() {
		t.Fatalf("read-only permissions changed: %v -> %v", file.info.Mode(), moved.info.Mode())
	}
	assertLayout(t, root, map[string]string{"destination": "read-only data"})
}

func TestSafetyDuplicateRemovalRetainsKeeper(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, map[string]string{"source": "same content", "keeper": "same content"})
	if err := removeDuplicateVerified(recordFile(t, filepath.Join(root, "source")), recordFile(t, filepath.Join(root, "keeper"))); err != nil {
		t.Fatal(err)
	}
	assertLayout(t, root, map[string]string{"keeper": "same content"})
}

func TestSafetyDuplicateRemovalRechecksContents(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, map[string]string{"source": "same", "keeper": "same"})
	source, keeper := recordFile(t, filepath.Join(root, "source")), recordFile(t, filepath.Join(root, "keeper"))
	writeFixture(t, root, map[string]string{"keeper": "DIFF"})
	if err := os.Chtimes(keeper.path, keeper.info.ModTime(), keeper.info.ModTime()); err != nil {
		t.Fatal(err)
	}
	if err := removeDuplicateVerified(source, keeper); err == nil {
		t.Fatal("deleted a file after keeper bytes changed")
	}
	assertLayout(t, root, map[string]string{"source": "same", "keeper": "DIFF"})
}

func TestSafetyCopiedSourceSurvivesChangedDestination(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, map[string]string{"source": "same", "destination": "same"})
	destination := recordFile(t, filepath.Join(root, "destination"))
	source, err := captureFile(recordFile(t, filepath.Join(root, "source")), false, true)
	if err != nil {
		t.Fatal(err)
	}
	writeFixture(t, root, map[string]string{"destination": "DIFF"})
	if err := os.Chtimes(destination.path, destination.info.ModTime(), destination.info.ModTime()); err != nil {
		_ = source.close()
		t.Fatal(err)
	}
	finishErr, closeErr := finishCopiedMove(source, destination), source.close()
	if finishErr == nil || closeErr != nil {
		t.Fatalf("changed copy accepted or source restoration failed: %v, %v", finishErr, closeErr)
	}
	assertLayout(t, root, map[string]string{"source": "same", "destination": "DIFF"})
}
