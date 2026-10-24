package app

import (
	"bytes"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	log "github.com/sirupsen/logrus"
)

func testConfig(root, mode string) Config {
	return Config{Roots: []string{root}, Mode: mode, Log: "debug"}
}

func writeFixture(t *testing.T, root string, files map[string]string, directories ...string) {
	t.Helper()
	for _, directory := range directories {
		if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(directory)), 0755); err != nil {
			t.Fatal(err)
		}
	}
	for name, data := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0640); err != nil {
			t.Fatal(err)
		}
	}
}

type snapshotEntry struct {
	Mode    os.FileMode
	ModTime int64
	Data    string
}

func snapshot(t *testing.T, root string) map[string]snapshotEntry {
	t.Helper()
	result := make(map[string]snapshotEntry)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		// Windows directory entries can retain stale timestamps after fixture creation.
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		value := snapshotEntry{Mode: info.Mode(), ModTime: info.ModTime().UnixNano()}
		if info.Mode().IsRegular() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			value.Data = string(data)
		} else if info.Mode()&os.ModeSymlink != 0 {
			value.Data, err = os.Readlink(path)
			if err != nil {
				return err
			}
		}
		relative, _ := filepath.Rel(root, path)
		result[filepath.ToSlash(relative)] = value
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func assertLayout(t *testing.T, root string, files map[string]string, extraDirs ...string) {
	t.Helper()
	expected := map[string]string{".": "directory"}
	addParents := func(path string) {
		for path != "." {
			expected[filepath.ToSlash(path)] = "directory"
			path = filepath.Dir(path)
		}
	}
	for path, data := range files {
		expected[path] = "file:" + data
		addParents(filepath.Dir(filepath.FromSlash(path)))
	}
	for _, directory := range extraDirs {
		addParents(filepath.FromSlash(directory))
	}
	actual := make(map[string]string)
	for path, entry := range snapshot(t, root) {
		if entry.Mode.IsDir() {
			actual[path] = "directory"
		} else {
			actual[path] = "file:" + entry.Data
		}
	}
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("layout differs\nactual: %#v\nexpected: %#v", actual, expected)
	}
}

func testLogger() *log.Logger {
	logger := log.New()
	logger.SetOutput(io.Discard)
	return logger
}

func TestModesAndDryRun(t *testing.T) {
	t.Parallel()
	cases := []struct {
		mode     string
		files    map[string]string
		dirs     []string
		expected map[string]string
		keptDirs []string
	}{
		{
			mode:     ModeDedup,
			files:    map[string]string{"z.txt": "same", "aaa/longer-name.txt": "same", "delete-me.txt": "keep", "metadata/.DS_Store": "finder"},
			dirs:     []string{"empty/nested"},
			expected: map[string]string{"longer-name.txt": "same", "delete-me.txt": "keep"},
		},
		{
			mode:     ModeDeleteFilesIncluding,
			files:    map[string]string{"a.txt": "same", "nested/b.txt": "same", "nested/delete-me.txt": "delete", "nested/delete-too.txt": "delete", "metadata/.DS_Store": "finder"},
			dirs:     []string{"empty/nested", "delete-me-folder"},
			expected: map[string]string{"a.txt": "same", "nested/b.txt": "same", "metadata/.DS_Store": "finder"},
			keptDirs: []string{"empty/nested", "delete-me-folder"},
		},
		{
			mode:     ModeFlatten,
			files:    map[string]string{"root-file.txt": "root", "one/direct.txt": "direct", "one/a/deep/file.txt": "one", "one/a/.DS_Store": "finder", "two/a/file.txt": "two", "two/delete-me.txt": "keep"},
			dirs:     []string{"empty-child/nested", "one/empty/nested"},
			expected: map[string]string{"root-file.txt": "root", "one/direct.txt": "direct", "one/file.txt": "one", "one/.DS_Store": "finder", "two/file.txt": "two", "two/delete-me.txt": "keep"},
			keptDirs: []string{"empty-child"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.mode, func(t *testing.T) {
			for _, dry := range []bool{true, false} {
				t.Run(fmt.Sprintf("dry=%t", dry), func(t *testing.T) {
					root := t.TempDir()
					writeFixture(t, root, tc.files, tc.dirs...)
					before := snapshot(t, root)
					config := testConfig(root, tc.mode)
					config.DryRun, config.EmptyDir = dry, true
					config.DeleteFilesIncluding = []string{"delete-me", "delete-too"}
					var output bytes.Buffer
					if err := run(config, &output); err != nil {
						t.Fatal(err)
					}
					if dry {
						if !reflect.DeepEqual(before, snapshot(t, root)) {
							t.Fatal("dry run changed names, contents, permissions, or modification times")
						}
						if !strings.Contains(output.String(), "Would ") {
							t.Fatal("dry run did not describe planned operations")
						}
					} else {
						assertLayout(t, root, tc.expected, tc.keptDirs...)
					}
				})
			}
		})
	}
}

func TestSharedFiltersInEveryMode(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{ModeDedup, ModeDeleteFilesIncluding, ModeFlatten} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			protected := map[string]string{
				"child/a/small-match.txt": "x", "child/b/tiny-match.txt": "x",
				"child/a/excluded-match.SKIP": "same", "child/b/another-match.skip": "same",
				"child/a/__protected__-match.txt": "same", "child/__protected__/match.txt": "same",
			}
			writeFixture(t, root, protected, "child/__protected__/empty")
			writeFixture(t, root, map[string]string{"child/a/ok-match.txt": "same", "child/b/longer-ok-match.txt": "same"})
			config := testConfig(root, mode)
			config.MinSize, config.EmptyDir = 4, true
			config.ExcludeExt = []string{"", " .skip "}
			config.IgnoreKeywords = []string{"", " __PROTECTED__ "}
			config.DeleteFilesIncluding = []string{"", " match "}
			if err := run(config, io.Discard); err != nil {
				t.Fatal(err)
			}
			expected := make(map[string]string)
			for path, data := range protected {
				expected[path] = data
			}
			switch mode {
			case ModeDedup:
				expected["child/a/longer-ok-match.txt"] = "same"
			case ModeFlatten:
				expected["child/ok-match.txt"], expected["child/longer-ok-match.txt"] = "same", "same"
			}
			assertLayout(t, root, expected, "child/__protected__/empty")
		})
	}
}

func TestFlattenCollisionsPreserveEveryFile(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeFixture(t, root, map[string]string{
		"child/file.txt": "existing", "child/file (1).txt": "existing suffix",
		"child/a/file.txt": "first", "child/b/file.txt": "second",
		"child/.env": "existing dotfile", "child/a/.env": "nested dotfile",
		"child/archive.tar.gz": "existing archive", "child/a/archive.tar.gz": "nested archive",
		"child/a/deep/a": "name conflicts with directory", "child/a (1)": "existing extensionless",
		"child/a/deep/space name 日本.txt": "unicode",
	})
	config := testConfig(root, ModeFlatten)
	var preview bytes.Buffer
	config.DryRun = true
	before := snapshot(t, root)
	if err := run(config, &preview); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, snapshot(t, root)) {
		t.Fatal("collision preview changed files")
	}
	for _, name := range []string{"file (2).txt", "file (3).txt", ".env (1)", "archive.tar (1).gz", "a (2)"} {
		if !strings.Contains(preview.String(), name) {
			t.Errorf("preview did not reserve collision name %q", name)
		}
	}
	config.DryRun = false
	if err := run(config, io.Discard); err != nil {
		t.Fatal(err)
	}
	assertLayout(t, root, map[string]string{
		"child/file.txt": "existing", "child/file (1).txt": "existing suffix",
		"child/file (2).txt": "first", "child/file (3).txt": "second",
		"child/.env": "existing dotfile", "child/.env (1)": "nested dotfile",
		"child/archive.tar.gz": "existing archive", "child/archive.tar (1).gz": "nested archive",
		"child/a (2)": "name conflicts with directory", "child/a (1)": "existing extensionless",
		"child/space name 日本.txt": "unicode",
	})
	before = snapshot(t, root)
	if err := run(config, io.Discard); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, snapshot(t, root)) {
		t.Fatal("flatten was not idempotent")
	}
}

func TestDedupExistingDestination(t *testing.T) {
	t.Parallel()
	for _, destinationIsDuplicate := range []bool{false, true} {
		t.Run(fmt.Sprintf("duplicate=%t", destinationIsDuplicate), func(t *testing.T) {
			root := t.TempDir()
			data := "unrelated content"
			if destinationIsDuplicate {
				data = "same"
			}
			writeFixture(t, root, map[string]string{"a.txt": "same", "nested/longer-name.txt": "same", "longer-name.txt": data})
			if err := run(testConfig(root, ModeDedup), io.Discard); err != nil {
				t.Fatal(err)
			}
			expected := map[string]string{"longer-name.txt": data}
			if !destinationIsDuplicate {
				expected["a.txt"] = "same"
			}
			assertLayout(t, root, expected, "nested")
		})
	}
}

func TestDedupEmptyFilesAndDifferentContent(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	large := strings.Repeat("a", 64*1024)
	writeFixture(t, root, map[string]string{
		"a-empty": "", "longer-empty": "", "same-a": "1234", "same-b": "5678",
		"large-a": large + "a", "longer-large-a": large + "a", "different-large": large + "b",
	})
	if err := run(testConfig(root, ModeDedup), io.Discard); err != nil {
		t.Fatal(err)
	}
	assertLayout(t, root, map[string]string{"longer-empty": "", "same-a": "1234", "same-b": "5678", "longer-large-a": large + "a", "different-large": large + "b"})
}

func TestEmptyDirFlagOnlyAffectsDedup(t *testing.T) {
	t.Parallel()
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprint(enabled), func(t *testing.T) {
			root := t.TempDir()
			writeFixture(t, root, nil, "empty/deep")
			config := testConfig(root, ModeDedup)
			config.EmptyDir = enabled
			if err := run(config, io.Discard); err != nil {
				t.Fatal(err)
			}
			if enabled {
				assertLayout(t, root, nil)
			} else {
				assertLayout(t, root, nil, "empty/deep")
			}
		})
	}
}

func TestMetadataCleanupRespectsFilters(t *testing.T) {
	t.Parallel()
	for _, filter := range []string{"size", "extension", "keyword"} {
		t.Run(filter, func(t *testing.T) {
			root := t.TempDir()
			files := map[string]string{"metadata/.DS_Store": "x"}
			writeFixture(t, root, files)
			config := testConfig(root, ModeDedup)
			config.EmptyDir = true
			switch filter {
			case "size":
				config.MinSize = 2
			case "extension":
				config.ExcludeExt = []string{".ds_store"}
			case "keyword":
				config.IgnoreKeywords = []string{".ds_store"}
			}
			if err := run(config, io.Discard); err != nil {
				t.Fatal(err)
			}
			assertLayout(t, root, files)
		})
	}
}

func TestMetadataCleanupPreservesOrdinaryDuplicate(t *testing.T) {
	t.Parallel()
	for _, priorRun := range []bool{false, true} {
		t.Run(fmt.Sprintf("prior-cleanup-disabled=%t", priorRun), func(t *testing.T) {
			root := t.TempDir()
			files := map[string]string{"a/z": "same", "longer-directory/.DS_Store": "same"}
			writeFixture(t, root, files)
			config := testConfig(root, ModeDedup)
			if priorRun {
				if err := run(config, io.Discard); err != nil {
					t.Fatal(err)
				}
				assertLayout(t, root, files)
			}
			config.EmptyDir, config.DryRun = true, true
			before := snapshot(t, root)
			if err := run(config, io.Discard); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, snapshot(t, root)) {
				t.Fatal("metadata cleanup preview changed files")
			}
			config.DryRun = false
			if err := run(config, io.Discard); err != nil {
				t.Fatal(err)
			}
			assertLayout(t, root, map[string]string{"a/z": "same"})
		})
	}
}

func TestMetadataAndOrdinaryDuplicatesUseSeparateKeepers(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeFixture(t, root, map[string]string{
		"a/x": "same", "longer/y": "same",
		"meta/.DS_Store": "same", "longer-meta/.DS_Store": "same",
	})
	config := testConfig(root, ModeDedup)
	if err := run(config, io.Discard); err != nil {
		t.Fatal(err)
	}
	assertLayout(t, root, map[string]string{"a/x": "same", "meta/.DS_Store": "same"}, "longer", "longer-meta")
	config.EmptyDir = true
	if err := run(config, io.Discard); err != nil {
		t.Fatal(err)
	}
	assertLayout(t, root, map[string]string{"a/x": "same"})
}

func TestDeleteWithoutSubstringsDoesNothing(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeFixture(t, root, map[string]string{"a": "same", "nested/b": "same"}, "empty")
	before := snapshot(t, root)
	config := testConfig(root, ModeDeleteFilesIncluding)
	config.EmptyDir = true
	config.DeleteFilesIncluding = []string{"", " "}
	if err := run(config, io.Discard); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, snapshot(t, root)) {
		t.Fatalf("empty substrings caused file operations\nbefore: %#v\nafter: %#v", before, snapshot(t, root))
	}
}

func TestAllRootsValidatedBeforeAnyChanges(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeFixture(t, root, map[string]string{"delete-me": "keep on failure"})
	before := snapshot(t, root)
	config := testConfig(root, ModeDeleteFilesIncluding)
	config.Roots = append(config.Roots, filepath.Join(root, "missing"))
	config.DeleteFilesIncluding = []string{"delete"}
	if err := run(config, io.Discard); err == nil {
		t.Fatal("expected invalid-root error")
	}
	if !reflect.DeepEqual(before, snapshot(t, root)) {
		t.Fatal("edited a root before validating all roots")
	}
}

func TestMultipleRootsKeepDedupAndCountersSeparate(t *testing.T) {
	t.Parallel()
	first, second := t.TempDir(), t.TempDir()
	for _, root := range []string{first, second} {
		writeFixture(t, root, map[string]string{"a": "same", "longer-name": "same"})
	}
	config := testConfig(first, ModeDedup)
	config.Roots = append(config.Roots, second)
	var output bytes.Buffer
	if err := run(config, &output); err != nil {
		t.Fatal(err)
	}
	for _, root := range []string{first, second} {
		assertLayout(t, root, map[string]string{"longer-name": "same"})
	}
	if !strings.Contains(output.String(), "2 files deleted (8 bytes)") {
		t.Fatalf("incorrect totals: %s", output.String())
	}
	output.Reset()
	if err := run(config, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "0 files deleted (0 bytes)") {
		t.Fatal("counters leaked across runs")
	}
}

func TestSymlinksAreNotFollowedInAnyMode(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{ModeDedup, ModeDeleteFilesIncluding, ModeFlatten} {
		t.Run(mode, func(t *testing.T) {
			root, outside := t.TempDir(), t.TempDir()
			writeFixture(t, outside, map[string]string{"delete-me.txt": "outside"})
			writeFixture(t, root, nil, "child/nested")
			link := filepath.Join(root, "child", "nested", "delete-me.txt")
			if err := os.Symlink(filepath.Join(outside, "delete-me.txt"), link); err != nil {
				t.Skipf("symbolic links unavailable: %v", err)
			}
			if err := os.Symlink(outside, filepath.Join(root, "child", "linked-directory")); err != nil {
				t.Fatal(err)
			}
			before, external := snapshot(t, root), snapshot(t, outside)
			config := testConfig(root, mode)
			config.EmptyDir = true
			config.DeleteFilesIncluding = []string{"delete"}
			if err := run(config, io.Discard); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, snapshot(t, root)) || !reflect.DeepEqual(external, snapshot(t, outside)) {
				t.Fatal("symlink or outside target changed")
			}
		})
	}
}

func TestScanErrorsAbortBeforeEdits(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("requires Unix permissions and a non-root user")
	}
	root := t.TempDir()
	writeFixture(t, root, map[string]string{"delete-me": "keep", "unreadable/keep": "hidden"})
	blocked := filepath.Join(root, "unreadable")
	if err := os.Chmod(blocked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(blocked, 0755) })
	config := testConfig(root, ModeDeleteFilesIncluding)
	config.DeleteFilesIncluding = []string{"delete"}
	if err := run(config, io.Discard); err == nil {
		t.Fatal("walk error was ignored")
	}
	if _, err := os.Stat(filepath.Join(root, "delete-me")); err != nil {
		t.Fatal("deleted a file before completing discovery")
	}
}

func TestFlattenPreservesContentWithinEachChild(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	random := rand.New(rand.NewSource(42))
	files := map[string]string{"root.txt": "keep root file"}
	before := make(map[string]map[string]int)
	for child := 0; child < 3; child++ {
		name := fmt.Sprintf("child-%d", child)
		before[name] = make(map[string]int)
		for index := 0; index < 50; index++ {
			data := fmt.Sprintf("content-%d", random.Intn(20))
			path := fmt.Sprintf("%s/branch-%d/deep-%d/file-%d.txt", name, index/10, index, random.Intn(5))
			files[path] = data
			before[name][data]++
		}
	}
	writeFixture(t, root, files)
	if err := run(testConfig(root, ModeFlatten), io.Discard); err != nil {
		t.Fatal(err)
	}
	after := make(map[string]map[string]int)
	for child := range before {
		after[child] = make(map[string]int)
		entries, err := os.ReadDir(filepath.Join(root, child))
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if entry.IsDir() {
				t.Fatalf("nested directory remains in %s", child)
			}
			data, err := os.ReadFile(filepath.Join(root, child, entry.Name()))
			if err != nil {
				t.Fatal(err)
			}
			after[child][string(data)]++
		}
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("flatten lost, duplicated, or moved contents between direct children")
	}
	if data, err := os.ReadFile(filepath.Join(root, "root.txt")); err != nil || string(data) != "keep root file" {
		t.Fatalf("root file changed: %v", err)
	}
}

func TestDedupPreservesEveryDistinctContent(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	random := rand.New(rand.NewSource(97))
	files := make(map[string]string)
	expected := make(map[string]int)
	for index := 0; index < 100; index++ {
		content := fmt.Sprintf("payload-%03d", random.Intn(15))
		files[fmt.Sprintf("directory-%d/file-%03d", random.Intn(5), index)] = content
		expected[content] = 1
	}
	writeFixture(t, root, files)
	config := testConfig(root, ModeDedup)
	config.EmptyDir = true
	if err := run(config, io.Discard); err != nil {
		t.Fatal(err)
	}
	actual := make(map[string]int)
	for _, entry := range snapshot(t, root) {
		if entry.Mode.IsRegular() {
			actual[entry.Data]++
		}
	}
	if !reflect.DeepEqual(actual, expected) {
		t.Fatal("dedup lost distinct contents or left duplicate copies")
	}
}
