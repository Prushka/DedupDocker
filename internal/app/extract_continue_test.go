package app

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestExtractContinuesAfterArchiveFailuresAcrossRoots(t *testing.T) {
	tool := requireArchiveTool(t)
	first, second, input := t.TempDir(), t.TempDir(), t.TempDir()
	writeFixture(t, first, map[string]string{"a-corrupt.zip": "corrupt original"})
	writeArchiveZIP(t, filepath.Join(first, "b-valid.zip"), map[string]string{"中文/日本語.txt": "first root payload"})
	writeArchiveZIP(t, filepath.Join(second, "a-unsafe.zip"), map[string]string{"../escape.txt": "unsafe original payload"})
	unsafeData, err := os.ReadFile(filepath.Join(second, "a-unsafe.zip"))
	if err != nil {
		t.Fatal(err)
	}
	writeFixture(t, input, map[string]string{"private.txt": "encrypted payload"})
	command := exec.Command(tool.executable, "a", "-t7z", "-mhe=on", "-pprivate-password", filepath.Join(second, "b-protected.7z"), "private.txt")
	command.Dir = input
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("create encrypted fixture: %v\n%s", err, output)
	}
	protectedData, err := os.ReadFile(filepath.Join(second, "b-protected.7z"))
	if err != nil {
		t.Fatal(err)
	}
	writeArchiveZIP(t, filepath.Join(second, "c-valid.zip"), map[string]string{"payload.txt": "second root payload"})
	config := testConfig(first, ModeExtract)
	config.Roots = append(config.Roots, second)
	config.Passwords = []string{"wrong-password"}
	var output bytes.Buffer
	config.DryRun = true
	beforeFirst, beforeSecond := snapshot(t, first), snapshot(t, second)
	if err := run(config, &output); err != nil {
		t.Fatalf("preview opened or validated archive contents: %v", err)
	}
	if !reflect.DeepEqual(beforeFirst, snapshot(t, first)) || !reflect.DeepEqual(beforeSecond, snapshot(t, second)) {
		t.Fatal("preview changed archives")
	}
	config.DryRun = false
	output.Reset()
	err = run(config, &output)
	if err == nil || !strings.Contains(err.Error(), "3 archives failed") {
		t.Fatalf("expected a final failure count after processing all archives: %v\n%s", err, output.String())
	}
	assertLayout(t, first, map[string]string{"a-corrupt.zip": "corrupt original", "b-valid/中文/日本語.txt": "first root payload"})
	assertLayout(t, second, map[string]string{"a-unsafe.zip": string(unsafeData), "b-protected.7z": string(protectedData), "c-valid/payload.txt": "second root payload"})
	logs := output.String()
	if strings.Count(logs, "Skipping archive") != 3 || !strings.Contains(logs, "2 archives extracted") || !strings.Contains(logs, "3 archives failed") {
		t.Fatalf("incorrect extraction summary: %s", logs)
	}
	if strings.Contains(logs, "private-password") || strings.Contains(logs, "wrong-password") {
		t.Fatal("archive password appeared in failure logs")
	}
}

func TestExtractContinuesAfterSourceOrDestinationChanges(t *testing.T) {
	requireArchiveTool(t)
	for _, change := range []string{"missing-source", "changed-source", "occupied-destination", "changed-parent"} {
		t.Run(change, func(t *testing.T) {
			root := t.TempDir()
			writeFixture(t, root, nil, "a-first")
			archive := filepath.Join(root, "a-first", "archive.zip")
			writeArchiveZIP(t, archive, map[string]string{"payload.txt": "first payload"})
			writeArchiveZIP(t, filepath.Join(root, "z-next.zip"), map[string]string{"payload.txt": "next payload"})
			planned, err := buildPlan(testConfig(root, ModeExtract), testLogger())
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "missing-source":
				err = os.Remove(archive)
			case "changed-source":
				err = os.WriteFile(archive, []byte("changed original"), 0640)
			case "occupied-destination":
				writeFixture(t, root, map[string]string{"a-first/archive/existing.txt": "keep destination"})
			case "changed-parent":
				err = os.Rename(filepath.Join(root, "a-first"), filepath.Join(root, "moved-parent"))
				if err == nil {
					err = os.Mkdir(filepath.Join(root, "a-first"), 0755)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			before := extractSnapshotWithoutDirectoryTimes(t, root)
			if err := planned.execute(false, testLogger()); err == nil || !strings.Contains(err.Error(), "1 archives failed") {
				t.Fatalf("expected one skipped archive: %v", err)
			}
			actual := extractSnapshotWithoutDirectoryTimes(t, root)
			if entry, ok := actual["z-next/payload.txt"]; !ok || entry.Data != "next payload" {
				t.Fatal("did not extract the next archive")
			}
			// The failed archive, any replacement parent and occupied destination
			// must be untouched. Only the later successful extraction may change.
			delete(before, "z-next.zip")
			delete(actual, "z-next")
			delete(actual, "z-next/payload.txt")
			if !reflect.DeepEqual(before, actual) {
				t.Fatalf("changed files belonging to the failed extraction\nbefore: %#v\nafter: %#v", before, actual)
			}
		})
	}
}

func TestExtractCancellationStillStopsBatch(t *testing.T) {
	requireArchiveTool(t)
	root := t.TempDir()
	writeArchiveZIP(t, filepath.Join(root, "a-first.zip"), map[string]string{"payload.txt": "first payload"})
	writeArchiveZIP(t, filepath.Join(root, "z-next.zip"), map[string]string{"payload.txt": "next payload"})
	before := extractSnapshotWithoutDirectoryTimes(t, root)
	base, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := &extractReviewContext{Context: base, hook: func() {
		entries, _ := os.ReadDir(root)
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), ".dedup-extract-") {
				cancel()
			}
		}
	}}
	if err := runContext(ctx, testConfig(root, ModeExtract), io.Discard); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation to stop the batch: %v", err)
	}
	if !reflect.DeepEqual(before, extractSnapshotWithoutDirectoryTimes(t, root)) {
		t.Fatal("cancellation changed the original archives or processed the next archive")
	}
}

func extractSnapshotWithoutDirectoryTimes(t *testing.T, root string) map[string]snapshotEntry {
	t.Helper()
	result := snapshot(t, root)
	for name, entry := range result {
		if entry.Mode.IsDir() {
			entry.ModTime = 0 // Creating and cleaning private staging changes parent timestamps.
			result[name] = entry
		}
	}
	return result
}
