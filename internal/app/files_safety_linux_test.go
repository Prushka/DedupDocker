//go:build linux

package app

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func writeSafetyAttribute(t *testing.T, path string) {
	t.Helper()
	if err := unix.Setxattr(path, "user.dedup-test", []byte("unique associated data"), 0); err != nil {
		t.Skipf("filesystem does not support user extended attributes: %v", err)
	}
}

func TestSafetyLinuxCopyRefusesExtendedAttributes(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, map[string]string{"source": "main content"})
	source, destination := filepath.Join(root, "source"), filepath.Join(root, "destination")
	writeSafetyAttribute(t, source)
	if _, err := copyMove(recordFile(t, source), destination); err == nil {
		t.Fatal("copy accepted extended attributes it cannot preserve")
	}
	assertLayout(t, root, map[string]string{"source": "main content"})
	buffer := make([]byte, 128)
	size, err := unix.Getxattr(source, "user.dedup-test", buffer)
	if err != nil || string(buffer[:size]) != "unique associated data" {
		t.Fatalf("source extended attribute changed: %q, %v", buffer, err)
	}
}

func TestSafetyLinuxNativeMovePreservesExtendedAttributes(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, map[string]string{"source": "main content"})
	source, destination := filepath.Join(root, "source"), filepath.Join(root, "destination")
	writeSafetyAttribute(t, source)
	if _, err := moveNoReplace(recordFile(t, source), destination); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 128)
	size, err := unix.Getxattr(destination, "user.dedup-test", buffer)
	if err != nil || string(buffer[:size]) != "unique associated data" {
		t.Fatalf("moved extended attribute changed: %q, %v", buffer, err)
	}
}

func TestSafetyLinuxDedupPreservesExtendedAttributes(t *testing.T) {
	for _, late := range []bool{false, true} {
		t.Run(map[bool]string{false: "existing", true: "late"}[late], func(t *testing.T) {
			root := t.TempDir()
			files := map[string]string{"a.txt": "same", "longer-name.txt": "same", "metadata/.DS_Store": "finder"}
			writeFixture(t, root, files)
			config := testConfig(root, ModeDedup)
			config.EmptyDir = true
			var planned *plan
			if late {
				var err error
				planned, err = buildPlan(config, testLogger())
				if err != nil {
					t.Fatal(err)
				}
			}
			for _, name := range []string{"a.txt", "metadata/.DS_Store"} {
				path := filepath.Join(root, filepath.FromSlash(name))
				info, err := os.Stat(path)
				if err != nil {
					t.Fatal(err)
				}
				writeSafetyAttribute(t, path)
				if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
					t.Fatal(err)
				}
			}
			if late {
				if err := planned.execute(false, testLogger()); err == nil {
					t.Fatal("deleted a file with late extended attributes")
				}
			} else if err := run(config, io.Discard); err != nil {
				t.Fatal(err)
			}
			assertLayout(t, root, files)
		})
	}
}

func TestSafetyLinuxCapturedDeletionPreservesReplacement(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, map[string]string{"source": "selected file"})
	source := filepath.Join(root, "source")
	captured, err := captureFile(recordFile(t, source), false, true)
	if err != nil {
		t.Fatal(err)
	}
	writeFixture(t, root, map[string]string{"source": "unrelated replacement"})
	if err := captured.remove(); err != nil {
		_ = captured.close()
		t.Fatal(err)
	}
	if err := captured.close(); err != nil {
		t.Fatal(err)
	}
	assertLayout(t, root, map[string]string{"source": "unrelated replacement"})
}

func TestSafetyLinuxRollbackNeverOverwritesReplacement(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, map[string]string{"source": "selected file"})
	source := filepath.Join(root, "source")
	captured, err := captureFile(recordFile(t, source), false, true)
	if err != nil {
		t.Fatal(err)
	}
	retained := captured.record().path
	writeFixture(t, root, map[string]string{"source": "unrelated replacement"})
	if err := captured.close(); err == nil || !strings.Contains(err.Error(), "original retained") {
		t.Fatalf("expected explicit retained-source error, got %v", err)
	}
	for path, expected := range map[string]string{source: "unrelated replacement", retained: "selected file"} {
		actual, err := os.ReadFile(path)
		if err != nil || string(actual) != expected {
			t.Fatalf("rollback changed %q: %q, %v", path, actual, err)
		}
	}
}

func TestSafetyLinuxTimestampsUseDescriptor(t *testing.T) {
	root := t.TempDir()
	path, retained := filepath.Join(root, "destination"), filepath.Join(root, "original")
	writeFixture(t, root, map[string]string{"destination": "created copy"})
	file, err := os.OpenFile(path, os.O_RDWR, 0)
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

func TestSafetyHeldLinuxKeeperRetainsRecoveryCopyAfterReplacement(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, map[string]string{"source": "original content", "keeper": "original content"})
	source, keeper := recordFile(t, filepath.Join(root, "source")), recordFile(t, filepath.Join(root, "keeper"))
	captured, err := captureFile(keeper, false, true)
	if err != nil {
		t.Fatal(err)
	}
	retained := captured.record().path
	equal, err := sameContents(source, captured.record())
	if err != nil || !equal {
		_ = captured.close()
		t.Fatalf("compare captured keeper: %t, %v", equal, err)
	}
	writeFixture(t, root, map[string]string{"keeper": "unrelated replacement"})
	if err := removeVerifiedContent(source); err != nil {
		_ = captured.close()
		t.Fatal(err)
	}
	if err := captured.close(); err == nil || !strings.Contains(err.Error(), retained) {
		t.Fatalf("expected recovery path after keeper replacement, got %v", err)
	}
	if _, err := os.Stat(source.path); !os.IsNotExist(err) {
		t.Fatalf("duplicate source remains: %v", err)
	}
	for path, expected := range map[string]string{keeper.path: "unrelated replacement", retained: "original content"} {
		actual, err := os.ReadFile(path)
		if err != nil || string(actual) != expected {
			t.Fatalf("last original-content copy was lost or replacement overwritten: %q, %v", actual, err)
		}
	}
}
