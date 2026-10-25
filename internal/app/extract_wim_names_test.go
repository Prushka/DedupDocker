package app

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestExtractWIMSharedData(t *testing.T) {
	tool := requireArchiveTool(t)
	root, input := t.TempDir(), t.TempDir()
	writeFixture(t, input, map[string]string{"first.txt": "shared archived data", "second.txt": "shared archived data"})
	command := exec.Command(tool.executable, "a", "-twim", filepath.Join(root, "shared.wim"), "first.txt", "second.txt")
	command.Dir = input
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("create shared-data WIM: %v\n%s", err, output)
	}
	if err := run(testConfig(root, ModeExtract), io.Discard); err != nil {
		t.Fatal(err)
	}
	assertLayout(t, root, map[string]string{"shared/first.txt": "shared archived data", "shared/second.txt": "shared archived data"})
}

func TestExtractLongNamesPreserveExistingNamesAndHierarchy(t *testing.T) {
	requireArchiveTool(t)
	longStem, shortStem := strings.Repeat("界", 90), strings.Repeat("界", 83)
	longDirectory, shortDirectory := strings.Repeat("目", 90), strings.Repeat("目", 85)
	entries := []struct{ name, content string }{
		{longStem + "a.txt", "first long file"},
		{longStem + "b.txt", "second long file"},
		{longStem + ".TaR.Gz", "inner archive name"},
		{longDirectory + "/child.txt", "child of long directory"},
		{longDirectory + "/nested/deep.txt", "same shortened parent"},
		{longDirectory + "x/child.txt", "child of colliding long directory"},
		{shortStem + ".txt", "unchanged original name"},
		{strings.Repeat("界", 82) + " (1).txt", "unchanged numbered name"},
		{shortDirectory + "/child.txt", "child of unchanged directory"},
		{"ordinary.txt", "ordinary file"},
	}
	for _, format := range []string{"zip", "tar"} {
		t.Run(format, func(t *testing.T) {
			root := t.TempDir()
			var data bytes.Buffer
			if format == "zip" {
				writer := zip.NewWriter(&data)
				for _, fixture := range entries {
					entry, err := writer.Create(fixture.name)
					if err != nil {
						t.Fatal(err)
					}
					if _, err := io.WriteString(entry, fixture.content); err != nil {
						t.Fatal(err)
					}
				}
				if _, err := writer.Create(strings.Repeat("空", 90) + "/"); err != nil {
					t.Fatal(err)
				}
				if err := writer.Close(); err != nil {
					t.Fatal(err)
				}
			} else {
				writer := tar.NewWriter(&data)
				for _, fixture := range entries {
					if err := writer.WriteHeader(&tar.Header{Name: fixture.name, Mode: 0644, Size: int64(len(fixture.content))}); err != nil {
						t.Fatal(err)
					}
					if _, err := io.WriteString(writer, fixture.content); err != nil {
						t.Fatal(err)
					}
				}
				if err := writer.WriteHeader(&tar.Header{Name: strings.Repeat("空", 90) + "/", Typeflag: tar.TypeDir, Mode: 0755}); err != nil {
					t.Fatal(err)
				}
				if err := writer.Close(); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(root, "archive."+format), data.Bytes(), 0640); err != nil {
				t.Fatal(err)
			}
			if err := run(testConfig(root, ModeExtract), io.Discard); err != nil {
				t.Fatal(err)
			}
			assertLayout(t, root, map[string]string{
				"archive/" + strings.Repeat("界", 82) + " (2).txt":             "first long file",
				"archive/" + strings.Repeat("界", 82) + " (3).txt":             "second long file",
				"archive/" + strings.Repeat("目", 83) + " (1)/child.txt":       "child of long directory",
				"archive/" + strings.Repeat("目", 83) + " (1)/nested/deep.txt": "same shortened parent",
				"archive/" + strings.Repeat("目", 83) + " (2)/child.txt":       "child of colliding long directory",
				"archive/" + shortStem + ".txt":                               "unchanged original name",
				"archive/" + strings.Repeat("界", 82) + " (1).txt":             "unchanged numbered name",
				"archive/" + shortDirectory + "/child.txt":                    "child of unchanged directory",
				"archive/ordinary.txt":                                        "ordinary file",
				"archive/" + strings.Repeat("界", 82) + ".TaR.Gz":              "inner archive name",
			}, "archive/"+strings.Repeat("空", 85))
		})
	}
}

func TestExtractGzipLongEmbeddedName(t *testing.T) {
	requireArchiveTool(t)
	root := t.TempDir()
	var data bytes.Buffer
	writer := gzip.NewWriter(&data)
	writer.Name = strings.Repeat("g", 270) + ".txt"
	if _, err := io.WriteString(writer, "gzip payload"); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "archive.gz"), data.Bytes(), 0640); err != nil {
		t.Fatal(err)
	}
	if err := run(testConfig(root, ModeExtract), io.Discard); err != nil {
		t.Fatal(err)
	}
	assertLayout(t, root, map[string]string{"archive/" + strings.Repeat("g", 251) + ".txt": "gzip payload"})
}
