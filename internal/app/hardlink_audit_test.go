package app

import (
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestDedupPreservesAlreadySharedStorageAliases(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, map[string]string{"a": "same", "copy": "same"})
	if err := os.Link(filepath.Join(root, "a"), filepath.Join(root, "alias")); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}
	if err := run(testConfig(root, ModeDedup), io.Discard); err != nil {
		t.Fatal(err)
	}
	assertLayout(t, root, map[string]string{"a": "same", "alias": "same"})
}
