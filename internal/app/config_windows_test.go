//go:build windows

package app

import (
	"io"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestRootAncestorJunction(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	physical := filepath.Join(base, "physical")
	root := filepath.Join(physical, "root")
	writeFixture(t, root, map[string]string{"delete-me": "keep"})
	alias := filepath.Join(base, "__junction_alias__")
	if output, err := exec.Command("cmd", "/c", "mklink", "/J", alias, physical).CombinedOutput(); err != nil {
		t.Skipf("directory junctions unavailable: %v: %s", err, output)
	}
	configured := filepath.Join(alias, "root")
	config := testConfig(configured, ModeDeleteFilesIncluding)
	config.DeleteFilesIncluding = []string{"delete-me"}
	config.IgnoreKeywords = []string{"__junction_alias__"}
	before := snapshot(t, root)
	if err := run(config, io.Discard); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, snapshot(t, root)) {
		t.Fatal("resolving a junction changed keyword-filter behavior")
	}
	config.Roots = []string{root, configured}
	if err := run(config, io.Discard); err == nil || !strings.Contains(err.Error(), "overlap") {
		t.Fatalf("expected overlap error through junction, got %v", err)
	}
	if !reflect.DeepEqual(before, snapshot(t, root)) {
		t.Fatal("junction overlap validation allowed edits")
	}
}
