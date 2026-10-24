package app

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestFlattenLongCollisionNamesPreserveEveryFile(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		label, name string
		keepExt     bool
	}{
		{"ascii", strings.Repeat("a", 250) + ".txt", true},
		{"unicode", strings.Repeat("日", 83) + ".txt", true},
		{"long-extension", "a." + strings.Repeat("x", 252), false},
		{"unicode-extension", "日." + strings.Repeat("本", 83), false},
	} {
		t.Run(tc.label, func(t *testing.T) {
			root := t.TempDir()
			files := map[string]string{"child/" + tc.name: "existing destination"}
			expectedContent := map[string]int{"existing destination": 1}
			for index := 0; index < 12; index++ {
				content := fmt.Sprintf("nested content %d", index)
				files[fmt.Sprintf("child/nested-%02d/%s", index, tc.name)] = content
				expectedContent[content]++
			}
			writeFixture(t, root, files)
			before := snapshot(t, root)
			config := testConfig(root, ModeFlatten)
			planned, err := buildPlan(config, testLogger())
			if err != nil {
				t.Fatal(err)
			}
			moves := 0
			for _, op := range planned.operations {
				if op.kind != "move" {
					continue
				}
				moves++
				name := filepath.Base(op.to)
				if len(name) > 255 || !utf8.ValidString(name) {
					t.Fatalf("invalid preview destination (%d bytes): %q", len(name), name)
				}
				if tc.keepExt && filepath.Ext(name) != ".txt" {
					t.Fatalf("collision lost its extension: %q", name)
				}
			}
			if moves != 12 {
				t.Fatalf("expected 12 collision moves, got %d", moves)
			}
			if err := planned.execute(true, testLogger()); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, snapshot(t, root)) {
				t.Fatal("long-name preview changed the fixture")
			}
			if err := run(config, io.Discard); err != nil {
				t.Fatal(err)
			}
			entries, err := os.ReadDir(filepath.Join(root, "child"))
			if err != nil {
				t.Fatal(err)
			}
			actualContent := make(map[string]int)
			for _, entry := range entries {
				if entry.IsDir() || len(entry.Name()) > 255 || !utf8.ValidString(entry.Name()) {
					t.Fatalf("invalid final flattened entry: %q", entry.Name())
				}
				data, err := os.ReadFile(filepath.Join(root, "child", entry.Name()))
				if err != nil {
					t.Fatal(err)
				}
				actualContent[string(data)]++
			}
			if !reflect.DeepEqual(expectedContent, actualContent) {
				t.Fatalf("long-name collision lost or duplicated files: %#v", actualContent)
			}
			data, err := os.ReadFile(filepath.Join(root, "child", tc.name))
			if err != nil || string(data) != "existing destination" {
				t.Fatalf("original destination changed: %q, %v", data, err)
			}
		})
	}
}
