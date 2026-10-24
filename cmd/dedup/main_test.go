package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestCommandEnvironmentAndExitStatus(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "dedup")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	build := exec.Command("go", "build", "-o", binary, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build CLI: %v\n%s", err, output)
	}
	for _, tc := range []struct {
		name    string
		env     []string
		failure bool
		changed bool
	}{
		{"defaults", nil, false, false},
		{"legacy-remove-cannot-enable-writes", []string{"DO_REMOVE=true"}, false, false},
		{"explicit-dry-dedup", []string{"MODE=dedup", "DRY_RUN=true", "EMPTY_DIR=true"}, false, false},
		{"explicit-dry-delete", []string{"MODE=delete-files-including", "DRY_RUN=true"}, false, false},
		{"explicit-dry-flatten", []string{"MODE=flatten", "DRY_RUN=true"}, false, false},
		{"explicit-dry-extract", []string{"MODE=Extract", "DRY_RUN=true", "TARGET=/unused"}, false, false},
		{"blank-dry-run", []string{"DRY_RUN="}, false, false},
		{"dedup", []string{"MODE=dedup", "DRY_RUN=false"}, false, true},
		{"delete", []string{"MODE=delete-files-including", "DRY_RUN=false", "EMPTY_DIR=true"}, false, true},
		{"flatten", []string{"MODE=flatten", "DRY_RUN=false"}, false, true},
		{"invalid-mode", []string{"MODE=unknown", "DRY_RUN=false"}, true, false},
		{"invalid-dry-run", []string{"DRY_RUN=maybe"}, true, false},
		{"invalid-minimum", []string{"DRY_RUN=false", "MIN_SIZE=-1"}, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.MkdirAll(filepath.Join(root, "child", "deep"), 0755); err != nil {
				t.Fatal(err)
			}
			for name, content := range map[string]string{"a.txt": "same", "child/deep/longer-name.txt": "same", "delete-me.txt": "delete"} {
				if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(name)), []byte(content), 0640); err != nil {
					t.Fatal(err)
				}
			}
			before := commandSnapshot(t, root)
			command := exec.Command(binary)
			command.Env = append(commandEnvironment(), "ROOT="+root, "DELETE_FILES_INCLUDING=delete-me")
			command.Env = append(command.Env, tc.env...)
			output, err := command.CombinedOutput()
			if tc.failure {
				var exit *exec.ExitError
				if !errors.As(err, &exit) || exit.ExitCode() != 1 {
					t.Fatalf("expected exit status 1, got %v\n%s", err, output)
				}
			} else if err != nil {
				t.Fatalf("command failed: %v\n%s", err, output)
			}
			after := commandSnapshot(t, root)
			if !tc.changed && !reflect.DeepEqual(before, after) {
				t.Fatalf("CLI changed files unexpectedly\n%s", output)
			}
			if tc.changed && reflect.DeepEqual(before, after) {
				t.Fatalf("CLI did not perform the requested mode\n%s", output)
			}
		})
	}
}

func commandEnvironment() []string {
	settings := map[string]bool{
		"ROOTS": true, "TARGET": true, "PASSWORDS": true,
		"ROOT": true, "MODE": true, "DRY_RUN": true, "LOG": true, "EMPTY_DIR": true,
		"MIN_SIZE": true, "EXCLUDE_EXT": true, "IGNORE_KEYWORDS": true,
		"DELETE_FILES_INCLUDING": true, "DO_REMOVE": true, "DEDUP": true,
	}
	var environment []string
	for _, setting := range os.Environ() {
		name, _, _ := strings.Cut(setting, "=")
		if !settings[strings.ToUpper(name)] {
			environment = append(environment, setting)
		}
	}
	return environment
}

func commandSnapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	result := make(map[string]string)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		var data []byte
		if !entry.IsDir() {
			data, err = os.ReadFile(path)
			if err != nil {
				return err
			}
		}
		result[path] = fmt.Sprintf("%d:%d:%s", info.Mode(), info.ModTime().UnixNano(), data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}
