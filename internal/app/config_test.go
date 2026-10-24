package app

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func configEnvironment(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, name := range []string{"ROOTS", "PASSWORDS", "TARGET", "MODE", "DRY_RUN", "LOG", "EMPTY_DIR", "MIN_SIZE", "EXCLUDE_EXT", "DELETE_FILES_INCLUDING", "IGNORE_KEYWORDS", "DO_REMOVE", "DEDUP"} {
		t.Setenv(name, "")
	}
	t.Setenv("ROOT", root)
	t.Setenv("ROOTS", root)
	return root
}

func TestEnvironmentDefaultsAndLegacyFlags(t *testing.T) {
	root := configEnvironment(t)
	t.Setenv("DO_REMOVE", "true")
	t.Setenv("DEDUP", "false")
	config, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if config.Mode != ModeDedup || !config.DryRun || config.EmptyDir || config.MinSize != 0 || !reflect.DeepEqual(config.Roots, []string{root}) {
		t.Fatalf("unsafe or incorrect defaults: %+v", config)
	}
	if len(config.ExcludeExt)+len(config.IgnoreKeywords)+len(config.DeleteFilesIncluding) != 0 {
		t.Fatal("empty environment lists were not cleaned")
	}
}

func TestEnvironmentParsesNewSettings(t *testing.T) {
	configEnvironment(t)
	t.Setenv("MODE", ModeFlatten)
	t.Setenv("DRY_RUN", "false")
	t.Setenv("EMPTY_DIR", "true")
	t.Setenv("MIN_SIZE", "42")
	t.Setenv("EXCLUDE_EXT", ".jpg, .PNG,,")
	t.Setenv("IGNORE_KEYWORDS", " first ,second,,")
	t.Setenv("DELETE_FILES_INCLUDING", " one ,two,,")
	config, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if config.Mode != ModeFlatten || config.DryRun || !config.EmptyDir || config.MinSize != 42 {
		t.Fatalf("incorrect parsed settings: %+v", config)
	}
	if !reflect.DeepEqual(config.ExcludeExt, []string{".jpg", ".PNG"}) || !reflect.DeepEqual(config.IgnoreKeywords, []string{"first", "second"}) || !reflect.DeepEqual(config.DeleteFilesIncluding, []string{"one", "two"}) {
		t.Fatalf("incorrect list parsing: %+v", config)
	}
}

func TestInvalidEnvironment(t *testing.T) {
	for _, tc := range []struct{ name, value, message string }{
		{"MODE", "unknown", "MODE"}, {"DRY_RUN", "maybe", "DRY_RUN"},
		{"EMPTY_DIR", "maybe", "EMPTY_DIR"}, {"MIN_SIZE", "-1", "MIN_SIZE"},
		{"MIN_SIZE", "big", "MIN_SIZE"}, {"LOG", "unknown", "LOG"},
	} {
		t.Run(tc.name+tc.value, func(t *testing.T) {
			configEnvironment(t)
			t.Setenv(tc.name, tc.value)
			if _, err := loadConfig(); err == nil || !strings.Contains(err.Error(), tc.message) {
				t.Fatalf("expected %s error, got %v", tc.message, err)
			}
		})
	}
}

func TestInvalidRoots(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, map[string]string{"file": "data"}, "nested")
	for _, roots := range [][]string{nil, {" "}, {filepath.Join(root, "missing")}, {filepath.Join(root, "file")}, {root, root}, {root, filepath.Join(root, "nested")}, {filepath.Join(root, "nested"), root}} {
		config := testConfig(root, ModeDedup)
		config.Roots = roots
		if _, err := config.normalized(); err == nil {
			t.Errorf("accepted invalid roots: %v", roots)
		}
	}
	link := filepath.Join(t.TempDir(), "root-link")
	if err := os.Symlink(root, link); err != nil {
		t.Skipf("symbolic links unavailable: %v", err)
	}
	if _, err := testConfig(link, ModeDedup).normalized(); err == nil {
		t.Fatal("accepted symbolic-link root")
	}
}

func directoryAlias(t *testing.T, target, alias string) {
	t.Helper()
	if err := os.Symlink(target, alias); err == nil {
		return
	} else if runtime.GOOS != "windows" {
		t.Skipf("directory aliases unavailable: %v", err)
	}
	// Junctions do not require the Windows symbolic-link privilege.
	if output, err := exec.Command("cmd", "/c", "mklink", "/J", alias, target).CombinedOutput(); err != nil {
		t.Skipf("directory junctions unavailable: %v: %s", err, output)
	}
}

func TestRootAncestorAliasesRejectedBeforeEdits(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	physical := filepath.Join(base, "physical")
	root := filepath.Join(physical, "root")
	writeFixture(t, root, map[string]string{"delete-me": "first", "nested/delete-me": "second"})
	alias := filepath.Join(base, "alias")
	directoryAlias(t, physical, alias)
	for _, roots := range [][]string{
		{root, filepath.Join(alias, "root")},
		{root, filepath.Join(alias, "root", "nested")},
		{filepath.Join(root, "nested"), filepath.Join(alias, "root")},
	} {
		config := testConfig(root, ModeDeleteFilesIncluding)
		config.Roots = roots
		config.DeleteFilesIncluding = []string{"delete-me"}
		before := snapshot(t, root)
		if err := run(config, io.Discard); err == nil || !strings.Contains(err.Error(), "overlap") {
			t.Fatalf("expected overlap error for %v, got %v", roots, err)
		}
		if !reflect.DeepEqual(before, snapshot(t, root)) {
			t.Fatalf("aliased root validation allowed edits: %v", roots)
		}
	}
}

func TestRootAncestorAliasPreservesKeywordPath(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	physical := filepath.Join(base, "physical")
	root := filepath.Join(physical, "root")
	writeFixture(t, root, map[string]string{"delete-me": "keep"})
	alias := filepath.Join(base, "__configured_alias__")
	directoryAlias(t, physical, alias)
	configured := filepath.Join(alias, "root")
	config := testConfig(configured, ModeDeleteFilesIncluding)
	config.IgnoreKeywords = []string{"__configured_alias__"}
	config.DeleteFilesIncluding = []string{"delete-me"}
	normalized, err := config.normalized()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(normalized.Roots, []string{configured}) {
		t.Fatalf("configured root path changed: %v", normalized.Roots)
	}
	before := snapshot(t, root)
	if err := run(config, io.Discard); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, snapshot(t, root)) {
		t.Fatal("resolving root ancestors changed keyword-filter behavior")
	}
}
