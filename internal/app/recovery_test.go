package app

import (
	"io"
	"reflect"
	"strings"
	"testing"
)

func TestRecoveryDirectoriesStopAllModesBeforeEdits(t *testing.T) {
	for _, mode := range []string{ModeDedup, ModeDeleteFilesIncluding, ModeFlatten} {
		for _, directory := range []string{".dedup-source-interrupted", ".dedup-copy-interrupted"} {
			t.Run(mode+directory, func(t *testing.T) {
				root := t.TempDir()
				writeFixture(t, root, map[string]string{"a-delete-me": "same", "child/b-delete-me": "same", "child/" + directory + "/delete-me": "recovery data"})
				before := snapshot(t, root)
				config := testConfig(root, mode)
				config.DeleteFilesIncluding = []string{"delete-me"}
				if err := run(config, io.Discard); err == nil || !strings.Contains(err.Error(), "unfinished file operation") {
					t.Fatalf("expected recovery error, got %v", err)
				}
				if !reflect.DeepEqual(before, snapshot(t, root)) {
					t.Fatal("recovery directory detection allowed edits")
				}
			})
		}
	}
}
