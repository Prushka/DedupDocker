//go:build unix

package app

import (
	"io"
	"os"
	"path/filepath"
	"reflect"
	"syscall"
	"testing"
)

func TestSpecialFilesAreNeverOpenedOrRemoved(t *testing.T) {
	for _, mode := range []string{ModeDedup, ModeDeleteFilesIncluding, ModeFlatten} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			writeFixture(t, root, nil, "child/deep")
			pipe := filepath.Join(root, "child", "deep", "delete-me.pipe")
			if err := syscall.Mkfifo(pipe, 0600); err != nil {
				t.Fatal(err)
			}
			before := snapshot(t, root)
			config := testConfig(root, mode)
			config.EmptyDir = true
			config.DeleteFilesIncluding = []string{"delete-me"}
			if err := run(config, io.Discard); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, snapshot(t, root)) {
				t.Fatal("special file changed")
			}
		})
	}
}

func TestUnreadableDuplicateAbortsBeforeDeletion(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("requires a non-root user")
	}
	root := t.TempDir()
	writeFixture(t, root, map[string]string{"a": "same", "b": "same"})
	blocked := filepath.Join(root, "b")
	if err := os.Chmod(blocked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(blocked, 0640) })
	if err := run(testConfig(root, ModeDedup), io.Discard); err == nil {
		t.Fatal("read error was ignored")
	}
	if _, err := os.Stat(filepath.Join(root, "a")); err != nil {
		t.Fatal("deleted the readable copy after a hash error")
	}
}

func TestIgnoredUnreadableDirectoryIsSkipped(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("requires a non-root user")
	}
	root := t.TempDir()
	writeFixture(t, root, map[string]string{"__opaque__/keep": "keep", "delete-me": "delete"})
	blocked := filepath.Join(root, "__opaque__")
	if err := os.Chmod(blocked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(blocked, 0755) })
	config := testConfig(root, ModeDeleteFilesIncluding)
	config.IgnoreKeywords = []string{"__opaque__"}
	config.DeleteFilesIncluding = []string{"delete-me"}
	if err := run(config, io.Discard); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "delete-me")); !os.IsNotExist(err) {
		t.Fatalf("eligible file was not deleted: %v", err)
	}
}
