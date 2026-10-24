package app

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func configEnvironment(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, name := range []string{"MODE", "DRY_RUN", "LOG", "EMPTY_DIR", "MIN_SIZE", "EXCLUDE_EXT", "DELETE_FILES_INCLUDING", "IGNORE_KEYWORDS", "DO_REMOVE", "DEDUP"} {
		t.Setenv(name, "")
	}
	t.Setenv("ROOT", root)
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
