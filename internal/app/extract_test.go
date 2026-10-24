package app

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writeArchiveZIP(t *testing.T, target string, files map[string]string) {
	t.Helper()
	var data bytes.Buffer
	writer := zip.NewWriter(&data)
	for name, content := range files {
		entry, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(entry, content); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, data.Bytes(), 0640); err != nil {
		t.Fatal(err)
	}
}

func requireArchiveTool(t *testing.T) archiveTool {
	t.Helper()
	tool, err := findArchiveTool()
	if err != nil {
		if os.Getenv("REQUIRE_ARCHIVE_TESTS") == "1" {
			t.Fatal(err)
		}
		t.Skip(err)
	}
	return tool
}

func TestExtractUppercaseDryRunAndCollision(t *testing.T) {
	requireArchiveTool(t)
	root := t.TempDir()
	archive := filepath.Join(root, "ALBUM.ZIP")
	name := "a very long filename that must remain completely unchanged.txt"
	writeArchiveZIP(t, archive, map[string]string{"nested/" + name: "original payload", "empty.txt": ""})
	writeFixture(t, root, map[string]string{"ALBUM/existing.txt": "do not overwrite"})
	before := snapshot(t, root)
	config := testConfig(root, "Extract")
	config.DryRun = true
	if err := run(config, io.Discard); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, snapshot(t, root)) {
		t.Fatal("extract dry run changed data or names")
	}
	config.DryRun = false
	if err := run(config, io.Discard); err != nil {
		t.Fatal(err)
	}
	assertLayout(t, root, map[string]string{"ALBUM/existing.txt": "do not overwrite", "ALBUM (1)/nested/" + name: "original payload", "ALBUM (1)/empty.txt": ""})
}

func TestExtractDryRunDoesNotRequireExternalTools(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, map[string]string{"preview.ZIP": "archive is not opened during a preview"})
	t.Setenv("PATH", "")
	t.Setenv("ProgramFiles", "")
	t.Setenv("ProgramFiles(x86)", "")
	before := snapshot(t, root)
	config := testConfig(root, ModeExtract)
	config.DryRun = true
	if err := run(config, io.Discard); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, snapshot(t, root)) {
		t.Fatal("preview changed files")
	}
}

func TestExtractUnsafeOrUnrepresentableArchiveRetainsOriginal(t *testing.T) {
	requireArchiveTool(t)
	for _, name := range []string{"../escape.txt", "/absolute.txt", "C:/drive.txt", "nested/../../escape.txt", "file:stream", "bad\nname.txt"} {
		t.Run(strings.ReplaceAll(name, "/", "_"), func(t *testing.T) {
			root := t.TempDir()
			writeArchiveZIP(t, filepath.Join(root, "bad.zip"), map[string]string{name: "must stay archived"})
			before := snapshot(t, root)
			if err := run(testConfig(root, ModeExtract), io.Discard); err == nil {
				t.Fatal("accepted unsafe or ambiguous archive path")
			}
			assertLayout(t, root, map[string]string{"bad.zip": before["bad.zip"].Data})
		})
	}
}

func TestExtractCorruptArchiveRetainsOriginal(t *testing.T) {
	requireArchiveTool(t)
	root := t.TempDir()
	writeFixture(t, root, map[string]string{"broken.zip": "not an archive"})
	if err := run(testConfig(root, ModeExtract), io.Discard); err == nil {
		t.Fatal("accepted corrupt archive")
	}
	assertLayout(t, root, map[string]string{"broken.zip": "not an archive"})
}

func TestExtractCompoundTarGzip(t *testing.T) {
	requireArchiveTool(t)
	root := t.TempDir()
	var data bytes.Buffer
	gz := gzip.NewWriter(&data)
	tarWriter := tar.NewWriter(gz)
	content := "complete inner tar payload"
	if err := tarWriter.WriteHeader(&tar.Header{Name: "nested/file.txt", Mode: 0644, Size: int64(len(content))}); err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(tarWriter, content)
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "BACKUP.TAR.GZ"), data.Bytes(), 0640); err != nil {
		t.Fatal(err)
	}
	if err := run(testConfig(root, ModeExtract), io.Discard); err != nil {
		t.Fatal(err)
	}
	assertLayout(t, root, map[string]string{"BACKUP/nested/file.txt": content})
}

func TestExtractPasswordsAndSolidArchive(t *testing.T) {
	tool := requireArchiveTool(t)
	root, inputs := t.TempDir(), t.TempDir()
	writeFixture(t, inputs, map[string]string{"a.txt": "first payload", "nested/b.txt": "second payload", "empty.txt": ""})
	archive := filepath.Join(root, "encrypted.7z")
	password := "secret-test-password"
	command := exec.Command(tool.executable, "a", "-t7z", "-ms=on", "-mhe=on", "-p"+password, archive, ".")
	command.Dir = inputs
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("create encrypted fixture: %v\n%s", err, output)
	}
	config := testConfig(root, ModeExtract)
	config.Passwords = []string{"wrong-password"}
	if err := run(config, io.Discard); err == nil {
		t.Fatal("wrong password succeeded")
	}
	if entries, err := os.ReadDir(root); err != nil || len(entries) != 1 || entries[0].Name() != "encrypted.7z" {
		t.Fatalf("wrong password left partial extraction or lost the archive: %v, %v", entries, err)
	}
	config.Passwords = []string{"wrong-password", password}
	var logs bytes.Buffer
	if err := run(config, &logs); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(logs.String(), password) || strings.Contains(logs.String(), "wrong-password") {
		t.Fatal("password leaked to logs")
	}
	assertLayout(t, root, map[string]string{"encrypted/a.txt": "first payload", "encrypted/nested/b.txt": "second payload", "encrypted/empty.txt": ""})
}

func TestExtractFiltersAndRootsEnvironment(t *testing.T) {
	root := configEnvironment(t)
	other := t.TempDir()
	t.Setenv("ROOTS", root+","+other)
	t.Setenv("ROOT", filepath.Join(root, "ignored-legacy-value"))
	t.Setenv("TARGET", filepath.Join(root, "ignored-target-value"))
	t.Setenv("MODE", "Extract")
	t.Setenv("PASSWORDS", " leading and trailing ,second")
	config, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if config.Mode != ModeExtract || !reflect.DeepEqual(config.Roots, []string{root, other}) || !reflect.DeepEqual(config.Passwords, []string{" leading and trailing ", "second"}) {
		t.Fatalf("incorrect extract configuration: %+v", config)
	}
	writeFixture(t, root, map[string]string{"keep.zip": "data", "small.gz": "x", "ignore/archive.tar": "data", "ordinary.txt": "data"})
	config.MinSize = 2
	config.ExcludeExt = []string{".ZIP"}
	config.IgnoreKeywords = []string{"ignore"}
	before := snapshot(t, root)
	if err := runContext(context.Background(), config, io.Discard); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, snapshot(t, root)) {
		t.Fatal("extract filters did not preserve excluded files")
	}
}
