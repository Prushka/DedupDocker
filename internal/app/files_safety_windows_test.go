//go:build windows

package app

import (
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func writeSafetyStream(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path+":dedup-test", []byte(content), 0644); err != nil {
		t.Skipf("filesystem does not support named streams: %v", err)
	}
}

func TestSafetyDedupPreservesNamedStreams(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, map[string]string{"a.txt": "same", "longer-name.txt": "same", "metadata/.DS_Store": "finder"})
	writeSafetyStream(t, filepath.Join(root, "a.txt"), "unique user data")
	writeSafetyStream(t, filepath.Join(root, "metadata", ".DS_Store"), "metadata payload")
	config := testConfig(root, ModeDedup)
	config.EmptyDir = true
	if err := run(config, io.Discard); err != nil {
		t.Fatal(err)
	}
	assertLayout(t, root, map[string]string{"a.txt": "same", "longer-name.txt": "same", "metadata/.DS_Store": "finder"})
	for path, expected := range map[string]string{
		filepath.Join(root, "a.txt"): "unique user data", filepath.Join(root, "metadata", ".DS_Store"): "metadata payload",
	} {
		actual, err := os.ReadFile(path + ":dedup-test")
		if err != nil || string(actual) != expected {
			t.Fatalf("named stream changed at %q: %q, %v", path, actual, err)
		}
	}
}

func TestSafetyDedupRejectsLateNamedStreams(t *testing.T) {
	for _, metadata := range []bool{false, true} {
		t.Run(map[bool]string{false: "duplicate", true: "metadata"}[metadata], func(t *testing.T) {
			root := t.TempDir()
			files := map[string]string{"a.txt": "same", "longer-name.txt": "same"}
			if metadata {
				files = map[string]string{"metadata/.DS_Store": "finder"}
			}
			writeFixture(t, root, files)
			config := testConfig(root, ModeDedup)
			config.EmptyDir = metadata
			planned, err := buildPlan(config, testLogger())
			if err != nil {
				t.Fatal(err)
			}
			var target *fileRecord
			for _, op := range planned.operations {
				if op.kind == "delete" {
					target = op.file
					break
				}
			}
			if target == nil {
				t.Fatal("expected a planned deletion")
			}
			writeSafetyStream(t, target.path, "arrived after planning")
			if err := os.Chtimes(target.path, target.info.ModTime(), target.info.ModTime()); err != nil {
				t.Fatal(err)
			}
			if err := target.verify(); err != nil {
				t.Fatalf("fixture did not preserve original metadata: %v", err)
			}
			if err := planned.execute(false, testLogger()); err == nil {
				t.Fatal("deleted a file with a new named stream")
			}
			assertLayout(t, root, files)
			if actual, err := os.ReadFile(target.path + ":dedup-test"); err != nil || string(actual) != "arrived after planning" {
				t.Fatalf("late named stream changed: %q, %v", actual, err)
			}
		})
	}
}

func TestSafetyCopyRefusesNamedStreams(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, map[string]string{"source": "main content"})
	source, destination := filepath.Join(root, "source"), filepath.Join(root, "destination")
	writeSafetyStream(t, source, "unique associated data")
	if _, err := copyMove(recordFile(t, source), destination); err == nil {
		t.Fatal("copy accepted additional streams")
	}
	assertLayout(t, root, map[string]string{"source": "main content"})
	if actual, err := os.ReadFile(source + ":dedup-test"); err != nil || string(actual) != "unique associated data" {
		t.Fatalf("source stream changed: %q, %v", actual, err)
	}
}

func TestSafetyNativeMovePreservesStreamsAndReadOnlyHardlinks(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	source, destination := filepath.Join(root, "source"), filepath.Join(root, "destination")
	external := filepath.Join(outside, "keep")
	if err := os.WriteFile(external, []byte("main content"), 0644); err != nil {
		t.Fatal(err)
	}
	writeSafetyStream(t, external, "unique associated data")
	if err := os.Link(external, source); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(external, 0444); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(external, 0666) })
	file := recordFile(t, source)
	moved, err := moveNoReplace(file, destination)
	if err != nil {
		t.Fatal(err)
	}
	if err := moved.verify(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(source); !os.IsNotExist(err) {
		t.Fatalf("source remains: %v", err)
	}
	for _, path := range []string{destination, external} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0444 {
			t.Fatalf("read-only attribute changed at %q: %v, %v", path, info, err)
		}
		if actual, err := os.ReadFile(path + ":dedup-test"); err != nil || string(actual) != "unique associated data" {
			t.Fatalf("named stream changed at %q: %q, %v", path, actual, err)
		}
	}
}

func TestSafetyDeleteReadOnlyHardlinkPreservesOutsideAttributes(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	source, external := filepath.Join(root, "delete-me"), filepath.Join(outside, "keep")
	if err := os.WriteFile(external, []byte("outside content"), 0444); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(external, 0666) })
	if err := os.Link(external, source); err != nil {
		t.Fatal(err)
	}
	config := testConfig(root, ModeDeleteFilesIncluding)
	config.DeleteFilesIncluding = []string{"delete-me"}
	if err := run(config, io.Discard); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(external)
	if err != nil || info.Mode().Perm() != 0444 {
		t.Fatalf("outside hardlink lost its read-only attribute: %v, %v", info, err)
	}
	if actual, err := os.ReadFile(external); err != nil || string(actual) != "outside content" {
		t.Fatalf("outside hardlink changed: %q, %v", actual, err)
	}
}

func TestSafetyCapturedWindowsFilePreventsReplacement(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, map[string]string{"source": "selected content"})
	source := filepath.Join(root, "source")
	captured, err := captureFile(recordFile(t, source), false, true)
	if err != nil {
		t.Fatal(err)
	}
	defer captured.close()
	if err := os.Rename(source, filepath.Join(root, "renamed")); err == nil {
		t.Fatal("captured file allowed a concurrent rename")
	}
	if err := os.WriteFile(source, []byte("replacement"), 0644); err == nil {
		t.Fatal("captured file allowed a concurrent writer")
	}
	if err := captured.remove(); err != nil {
		t.Fatal(err)
	}
}

func TestSafetyWindowsTimestampsUseDescriptor(t *testing.T) {
	root := t.TempDir()
	path, retained := filepath.Join(root, "destination"), filepath.Join(root, "original")
	writeFixture(t, root, map[string]string{"destination": "created copy"})
	file, err := openWindowsHandle(path, windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := os.Rename(path, retained); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, root, map[string]string{"destination": "unrelated replacement"})
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	stamp := time.Unix(1600000000, 123400000)
	if err := setFileTimes(file, stamp); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	original, err := file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) || !original.ModTime().Equal(stamp) {
		t.Fatal("timestamp update followed the replaced pathname")
	}
}

func TestSafetyHeldWindowsKeeperPreventsReplacementDuringDeletion(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, map[string]string{"source": "original content", "keeper": "original content"})
	source, keeper := recordFile(t, filepath.Join(root, "source")), recordFile(t, filepath.Join(root, "keeper"))
	captured, err := captureFile(keeper, false, true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if captured != nil {
			_ = captured.close()
		}
	}()
	equal, err := sameContents(source, captured.record())
	if err != nil || !equal {
		t.Fatalf("compare captured keeper: %t, %v", equal, err)
	}
	if err := os.Rename(keeper.path, filepath.Join(root, "displaced")); err == nil {
		t.Fatal("retained keeper allowed a concurrent rename")
	}
	if err := os.WriteFile(keeper.path, []byte("replacement"), 0644); err == nil {
		t.Fatal("retained keeper allowed a concurrent writer")
	}
	if err := removeVerifiedContent(source); err != nil {
		t.Fatal(err)
	}
	if err := captured.close(); err != nil {
		t.Fatal(err)
	}
	captured = nil
	assertLayout(t, root, map[string]string{"keeper": "original content"})
}

func TestSafetyDuplicateRemovalRefusesAdditionalStreams(t *testing.T) {
	for _, extraAt := range []string{"source", "keeper"} {
		t.Run(extraAt, func(t *testing.T) {
			root := t.TempDir()
			writeFixture(t, root, map[string]string{"source": "same", "keeper": "same"})
			writeSafetyStream(t, filepath.Join(root, extraAt), "unique associated data")
			if err := removeDuplicateVerified(recordFile(t, filepath.Join(root, "source")), recordFile(t, filepath.Join(root, "keeper"))); err == nil {
				t.Fatal("duplicate removal accepted additional streams")
			}
			assertLayout(t, root, map[string]string{"source": "same", "keeper": "same"})
		})
	}
}
