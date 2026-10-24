package app

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func recordFile(t *testing.T, path string) *fileRecord {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	file, err := newFileRecord(path, info)
	if err != nil {
		t.Fatal(err)
	}
	return file
}

func TestMovesPreserveContentsAndMetadata(t *testing.T) {
	t.Parallel()
	for name, move := range map[string]func(*fileRecord, string) (*fileRecord, error){"link": moveNoReplace, "copy": copyMove} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			data := strings.Repeat("data\x00", 20000)
			writeFixture(t, root, map[string]string{"source": data})
			source := filepath.Join(root, "source")
			stamp := time.Unix(1600000000, 123400000)
			if err := os.Chtimes(source, stamp, stamp); err != nil {
				t.Fatal(err)
			}
			file := recordFile(t, source)
			target := filepath.Join(root, "destination")
			moved, err := move(file, target)
			if err != nil {
				t.Fatal(err)
			}
			if moved.path != target || moved.info.Mode() != file.info.Mode() || !moved.info.ModTime().Equal(file.info.ModTime()) {
				t.Fatal("move did not preserve file metadata")
			}
			assertLayout(t, root, map[string]string{"destination": data})
		})
	}
}

func TestMovesNeverOverwriteExistingDestinations(t *testing.T) {
	t.Parallel()
	for name, move := range map[string]func(*fileRecord, string) (*fileRecord, error){"link": moveNoReplace, "copy": copyMove} {
		t.Run(name, func(t *testing.T) {
			for _, kind := range []string{"file", "directory", "symlink"} {
				t.Run(kind, func(t *testing.T) {
					root := t.TempDir()
					writeFixture(t, root, map[string]string{"source": "source content"})
					target := filepath.Join(root, "destination")
					switch kind {
					case "file":
						writeFixture(t, root, map[string]string{"destination": "do not overwrite"})
					case "directory":
						writeFixture(t, root, nil, "destination")
					case "symlink":
						if err := os.Symlink(filepath.Join(root, "missing-target"), target); err != nil {
							t.Skipf("symbolic links unavailable: %v", err)
						}
					}
					before := snapshot(t, root)
					if _, err := move(recordFile(t, filepath.Join(root, "source")), target); err == nil {
						t.Fatal("move accepted an existing destination")
					}
					if !reflect.DeepEqual(before, snapshot(t, root)) {
						t.Fatal("failed move changed source or existing destination")
					}
				})
			}
		})
	}
}

func TestPlanAbortsIfSourceChanged(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeFixture(t, root, map[string]string{"a-delete-me": "first", "z-delete-me": "second"})
	config := testConfig(root, ModeDeleteFilesIncluding)
	config.DeleteFilesIncluding = []string{"delete-me"}
	planned, err := buildPlan(config, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	writeFixture(t, root, map[string]string{"z-delete-me": "changed content"})
	before := snapshot(t, root)
	if err := planned.execute(false, testLogger()); err == nil {
		t.Fatal("accepted source changed after planning")
	}
	if !reflect.DeepEqual(before, snapshot(t, root)) {
		t.Fatal("preflight error still allowed file edits")
	}
}

func TestDedupRechecksBytesEvenIfMetadataUnchanged(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeFixture(t, root, map[string]string{"a/file": "same", "b/file": "same"})
	planned, err := buildPlan(testConfig(root, ModeDedup), testLogger())
	if err != nil {
		t.Fatal(err)
	}
	changed := filepath.Join(root, "b", "file")
	original := recordFile(t, changed)
	writeFixture(t, root, map[string]string{"b/file": "DIFF"})
	if err := os.Chtimes(changed, original.info.ModTime(), original.info.ModTime()); err != nil {
		t.Fatal(err)
	}
	if err := original.verify(); err != nil {
		t.Fatalf("test did not restore metadata: %v", err)
	}
	before := snapshot(t, root)
	if err := planned.execute(false, testLogger()); err == nil {
		t.Fatal("deleted a file whose content no longer matched")
	}
	if !reflect.DeepEqual(before, snapshot(t, root)) {
		t.Fatal("content mismatch caused file edits")
	}
}

func TestNewDestinationAfterPlanningIsPreserved(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeFixture(t, root, map[string]string{"child/deep/file": "source"})
	planned, err := buildPlan(testConfig(root, ModeFlatten), testLogger())
	if err != nil {
		t.Fatal(err)
	}
	writeFixture(t, root, map[string]string{"child/file": "arrived after planning"})
	before := snapshot(t, root)
	if err := planned.execute(false, testLogger()); err == nil {
		t.Fatal("overwrote a file that arrived after planning")
	}
	if !reflect.DeepEqual(before, snapshot(t, root)) {
		t.Fatal("collision changed the source or new destination")
	}
}

func TestNewFilesPreventDirectoryRemoval(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeFixture(t, root, nil, "empty")
	config := testConfig(root, ModeDedup)
	config.EmptyDir = true
	planned, err := buildPlan(config, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	writeFixture(t, root, map[string]string{"empty/new-file": "preserve"})
	if err := planned.execute(false, testLogger()); err == nil {
		t.Fatal("expected nonempty-directory error")
	}
	assertLayout(t, root, map[string]string{"empty/new-file": "preserve"})
}

func TestReplacedParentDirectoryIsRejected(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	root := filepath.Join(parent, "root")
	writeFixture(t, root, map[string]string{"child/delete-me": "keep"})
	config := testConfig(root, ModeDeleteFilesIncluding)
	config.DeleteFilesIncluding = []string{"delete-me"}
	planned, err := buildPlan(config, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	original := filepath.Join(parent, "original")
	if err := os.Rename(filepath.Join(root, "child"), original); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(original, filepath.Join(root, "child")); err != nil {
		t.Skipf("symbolic links unavailable: %v", err)
	}
	if err := planned.execute(false, testLogger()); err == nil {
		t.Fatal("accepted substituted parent directory")
	}
	assertLayout(t, original, map[string]string{"delete-me": "keep"})
}

func TestMoveRejectsChangedAndNonregularSources(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeFixture(t, root, map[string]string{"source": "initial"}, "directory")
	file := recordFile(t, filepath.Join(root, "source"))
	writeFixture(t, root, map[string]string{"source": "changed data"})
	for _, record := range []*fileRecord{file, recordFile(t, filepath.Join(root, "directory"))} {
		if _, err := moveNoReplace(record, filepath.Join(root, "destination")); err == nil {
			t.Fatal("accepted unsafe source")
		}
	}
	assertLayout(t, root, map[string]string{"source": "changed data"}, "directory")
}

func TestHashAndCompareErrors(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeFixture(t, root, map[string]string{"a": "same", "b": "same"})
	a, b := recordFile(t, filepath.Join(root, "a")), recordFile(t, filepath.Join(root, "b"))
	if err := os.Remove(b.path); err != nil {
		t.Fatal(err)
	}
	if _, err := hashFile(b); err == nil {
		t.Fatal("hash did not report missing source")
	}
	if _, err := sameContents(a, b); err == nil {
		t.Fatal("comparison did not report missing source")
	}
}

func TestIgnoredRootDoesNothing(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeFixture(t, root, map[string]string{"delete-me": "keep"}, "empty")
	before := snapshot(t, root)
	for _, mode := range []string{ModeDedup, ModeDeleteFilesIncluding, ModeFlatten} {
		config := testConfig(root, mode)
		config.EmptyDir = true
		config.IgnoreKeywords = []string{filepath.Base(root)}
		config.DeleteFilesIncluding = []string{"delete-me"}
		if err := run(config, &bytes.Buffer{}); err != nil {
			t.Fatal(err)
		}
	}
	if !reflect.DeepEqual(before, snapshot(t, root)) {
		t.Fatal("ignored root changed")
	}
}

func TestFilteredDestinationIsNotCreated(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeFixture(t, root, map[string]string{"child/deep/file.txt": "same"})
	config := testConfig(root, ModeFlatten)
	config.IgnoreKeywords = []string{filepath.Join("child", "file.txt")}
	before := snapshot(t, root)
	if err := run(config, io.Discard); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, snapshot(t, root)) {
		t.Fatal("move created a path protected by ignore keywords")
	}
}

func TestMoveCleanupPreservesReplacedDestination(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeFixture(t, root, map[string]string{"destination": "created by move"})
	created := recordFile(t, filepath.Join(root, "destination"))
	if err := os.Rename(created.path, filepath.Join(root, "original")); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, root, map[string]string{"destination": "arrived later"})
	if err := removeCreated(created); err == nil {
		t.Fatal("cleanup accepted a replacement file")
	}
	assertLayout(t, root, map[string]string{"original": "created by move", "destination": "arrived later"})
}

func TestPlanRejectsReplacedFileWithMatchingMetadata(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeFixture(t, root, map[string]string{"delete-me": "same"})
	config := testConfig(root, ModeDeleteFilesIncluding)
	config.DeleteFilesIncluding = []string{"delete-me"}
	planned, err := buildPlan(config, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "delete-me")
	initial := planned.operations[0].file.info
	if err := os.Rename(source, filepath.Join(root, "original")); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, root, map[string]string{"delete-me": "same"})
	if err := os.Chtimes(source, initial.ModTime(), initial.ModTime()); err != nil {
		t.Fatal(err)
	}
	if err := planned.execute(false, testLogger()); err == nil {
		t.Fatal("accepted replacement with matching size and modification time")
	}
	assertLayout(t, root, map[string]string{"delete-me": "same", "original": "same"})
}
