package app

import (
	"io"
	"os/exec"
	"path/filepath"
	"testing"
)

func enableCaseSensitiveDirectory(t *testing.T, directory string) {
	t.Helper()
	command := exec.Command("fsutil", "file", "setCaseSensitiveInfo", directory, "enable")
	if output, err := command.CombinedOutput(); err != nil {
		t.Skipf("case-sensitive Windows directory unavailable: %v: %s", err, output)
	}
}

func TestWindowsCaseDistinctPathsAreNotDropped(t *testing.T) {
	for _, mode := range []string{ModeDedup, ModeFlatten} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			writeFixture(t, root, nil, "child/deep")
			enableCaseSensitiveDirectory(t, filepath.Join(root, "child"))
			enableCaseSensitiveDirectory(t, filepath.Join(root, "child", "deep"))
			lower := "same"
			if mode == ModeFlatten {
				lower = "different lower content"
			}
			writeFixture(t, root, map[string]string{"child/deep/A.txt": "same", "child/deep/a.txt": lower})
			if err := run(testConfig(root, mode), io.Discard); err != nil {
				t.Fatal(err)
			}
			if mode == ModeDedup {
				assertLayout(t, root, map[string]string{"child/deep/A.txt": "same"})
			} else {
				assertLayout(t, root, map[string]string{"child/A.txt": "same", "child/a (1).txt": lower})
			}
		})
	}
}

func TestWindowsCaseDistinctRootsDoNotOverlap(t *testing.T) {
	parent := t.TempDir()
	enableCaseSensitiveDirectory(t, parent)
	upper, lower := filepath.Join(parent, "A"), filepath.Join(parent, "a")
	writeFixture(t, upper, map[string]string{"delete-me": "upper"})
	writeFixture(t, lower, map[string]string{"delete-me": "lower"})
	config := testConfig(upper, ModeDeleteFilesIncluding)
	config.Roots = []string{upper, lower}
	config.DeleteFilesIncluding = []string{"delete-me"}
	if err := run(config, io.Discard); err != nil {
		t.Fatal(err)
	}
	assertLayout(t, upper, nil)
	assertLayout(t, lower, nil)
}

func TestWindowsCaseDistinctChildrenStaySeparate(t *testing.T) {
	root := t.TempDir()
	enableCaseSensitiveDirectory(t, root)
	writeFixture(t, root, map[string]string{"A/deep/file.txt": "upper child", "a/deep/file.txt": "lower child"})
	if err := run(testConfig(root, ModeFlatten), io.Discard); err != nil {
		t.Fatal(err)
	}
	assertLayout(t, root, map[string]string{"A/file.txt": "upper child", "a/file.txt": "lower child"})
}
