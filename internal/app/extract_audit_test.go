package app

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	if value := os.Getenv("DEDUP_TEST_7ZIP_EXIT"); value != "" {
		code, _ := strconv.Atoi(value)
		os.Exit(code)
	}
	os.Exit(m.Run())
}

func TestExtractWarningsNeverAuthorizeDeletion(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("DEDUP_TEST_7ZIP_EXIT", "1")
	tool := archiveTool{executable: executable}
	if _, err := tool.password(context.Background(), "test.zip", []string{"test-password"}); err == nil || !strings.Contains(err.Error(), "exit code 1") {
		t.Fatalf("7-Zip warnings accepted as success: %v", err)
	}
}

func TestArchiveTimestampTransitions(t *testing.T) {
	zone, err := time.LoadLocation("America/Chicago")
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"2020-03-08 02:30:00", "2020-11-01 01:30:00", "1601-01-01 00:00:00"} {
		if _, err := parseArchiveModified(value, zone, true); err == nil {
			t.Fatalf("accepted an ambiguous or unrepresentable timestamp: %s", value)
		}
	}
	stamp, err := parseArchiveModified("2020-09-13 07:26:40", zone, true)
	if err != nil || !stamp.Equal(time.Unix(1600000000, 0)) {
		t.Fatalf("normal timestamp changed: %v, %v", stamp, err)
	}
}

func TestExtractFormats(t *testing.T) {
	tool := requireArchiveTool(t)
	for _, format := range []string{"zip", "7z", "tar", "wim", "gzip"} {
		t.Run(format, func(t *testing.T) {
			root, inputs := t.TempDir(), t.TempDir()
			writeFixture(t, inputs, map[string]string{"payload.txt": "format payload"})
			stamp := time.Unix(1600000000, 0)
			if err := os.Chtimes(filepath.Join(inputs, "payload.txt"), stamp, stamp); err != nil {
				t.Fatal(err)
			}
			extension := format
			if format == "gzip" {
				extension = "gz"
			}
			archive := filepath.Join(root, "bundle."+extension)
			command := exec.Command(tool.executable, "a", "-t"+format, archive, "payload.txt")
			command.Dir = inputs
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("create %s fixture: %v\n%s", format, err, output)
			}
			if err := run(testConfig(root, ModeExtract), io.Discard); err != nil {
				t.Fatal(err)
			}
			assertLayout(t, root, map[string]string{"bundle/payload.txt": "format payload"})
			info, err := os.Stat(filepath.Join(root, "bundle", "payload.txt"))
			if err != nil || !info.ModTime().Equal(stamp) {
				t.Fatalf("extraction reset the archived modification date: %v, %v", info, err)
			}
		})
	}
}

func writeStoredRAR(t *testing.T, path string) {
	t.Helper()
	var archive bytes.Buffer
	archive.WriteString("Rar!\x1a\x07\x00")
	header := func(kind byte, flags uint16, body []byte) {
		block := []byte{0, 0, kind}
		block = binary.LittleEndian.AppendUint16(block, flags)
		block = binary.LittleEndian.AppendUint16(block, uint16(7+len(body)))
		block = append(block, body...)
		binary.LittleEndian.PutUint16(block, uint16(crc32.ChecksumIEEE(block[2:])))
		archive.Write(block)
	}
	header(0x73, 0, make([]byte, 6))
	name, data := []byte("payload.txt"), []byte("RAR payload")
	body := binary.LittleEndian.AppendUint32(nil, uint32(len(data)))
	body = binary.LittleEndian.AppendUint32(body, uint32(len(data)))
	body = append(body, 3) // Unix host.
	body = binary.LittleEndian.AppendUint32(body, crc32.ChecksumIEEE(data))
	body = binary.LittleEndian.AppendUint32(body, 0)
	body = append(body, 20, 0x30) // RAR 2.0, stored data.
	body = binary.LittleEndian.AppendUint16(body, uint16(len(name)))
	body = binary.LittleEndian.AppendUint32(body, 0100644)
	body = append(body, name...)
	header(0x74, 0x8000, body)
	archive.Write(data)
	header(0x7b, 0, nil)
	if err := os.WriteFile(path, archive.Bytes(), 0640); err != nil {
		t.Fatal(err)
	}
}

func TestExtractRAR(t *testing.T) {
	requireArchiveTool(t)
	root := t.TempDir()
	writeStoredRAR(t, filepath.Join(root, "bundle.rar"))
	if err := run(testConfig(root, ModeExtract), io.Discard); err != nil {
		t.Fatal(err)
	}
	assertLayout(t, root, map[string]string{"bundle/payload.txt": "RAR payload"})
}

func TestExtractDoesNotGuessAnUnconfiguredPassword(t *testing.T) {
	tool := requireArchiveTool(t)
	root, input := t.TempDir(), t.TempDir()
	writeFixture(t, input, map[string]string{"payload.txt": "encrypted payload"})
	archive := filepath.Join(root, "protected.7z")
	command := exec.Command(tool.executable, "a", "-t7z", "-mhe=on", "-p-", archive, "payload.txt")
	command.Dir = input
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("create password fixture: %v\n%s", err, output)
	}
	if err := run(testConfig(root, ModeExtract), io.Discard); err == nil {
		t.Fatal("guessed a password that was not configured")
	}
	if _, err := os.Stat(archive); err != nil {
		t.Fatal("removed archive without a configured password")
	}
	config := testConfig(root, ModeExtract)
	config.Passwords = []string{"-"}
	if err := run(config, io.Discard); err != nil {
		t.Fatal(err)
	}
	assertLayout(t, root, map[string]string{"protected/payload.txt": "encrypted payload"})
}

func TestExtractListingRejectsConflictsAndLinks(t *testing.T) {
	for _, listing := range []string{
		"Path = a\nFolder = -\nSize = 1\n\nPath = a\nFolder = -\nSize = 1\n",
		"Path = a\nFolder = -\nSize = 1\n\nPath = a/b\nFolder = -\nSize = 1\n",
		"Path = link\nFolder = -\nSize = 3\nAttributes = A_ lrwxrwxrwx\n",
		"Path = link\nFolder = -\nSize = 3\nHard Link = other\n",
		"Path = file\nFolder = -\nSize = -1\n",
		"Path = file\nFolder = -\nSize = 1\nNT Security = 0\n",
	} {
		if _, err := parseArchiveListing(listing); err == nil {
			t.Fatalf("accepted unsafe inventory: %q", listing)
		}
	}
}

func TestExtractDestinationArrivingAfterPlanningSurvives(t *testing.T) {
	requireArchiveTool(t)
	root := t.TempDir()
	writeArchiveZIP(t, filepath.Join(root, "archive.zip"), map[string]string{"payload.txt": "data"})
	before, err := buildPlan(testConfig(root, ModeExtract), testLogger())
	if err != nil {
		t.Fatal(err)
	}
	writeFixture(t, root, map[string]string{"archive/existing": "keep"})
	if err := before.execute(false, testLogger()); err == nil {
		t.Fatal("accepted destination created after planning")
	}
	data, err := os.ReadFile(filepath.Join(root, "archive", "existing"))
	if err != nil || string(data) != "keep" {
		t.Fatalf("overwrote destination: %q, %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(root, "archive.zip")); err != nil {
		t.Fatal("removed archive after publication failure")
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 2 {
		t.Fatal(fmt.Sprintf("failed extraction left staging data: %v, %v", entries, err))
	}
}

func TestExtractRealTarLinksRetainOriginal(t *testing.T) {
	requireArchiveTool(t)
	for _, kind := range []byte{tar.TypeSymlink, tar.TypeLink} {
		t.Run(fmt.Sprint(kind), func(t *testing.T) {
			root := t.TempDir()
			var data bytes.Buffer
			writer := tar.NewWriter(&data)
			if err := writer.WriteHeader(&tar.Header{Name: "link", Linkname: "../outside", Typeflag: kind, Mode: 0777}); err != nil {
				t.Fatal(err)
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			archive := filepath.Join(root, "links.tar")
			if err := os.WriteFile(archive, data.Bytes(), 0640); err != nil {
				t.Fatal(err)
			}
			if err := run(testConfig(root, ModeExtract), io.Discard); err == nil {
				t.Fatal("accepted archive links")
			}
			assertLayout(t, root, map[string]string{"links.tar": data.String()})
		})
	}
}

func TestExtractDuplicateZIPEntriesRetainOriginal(t *testing.T) {
	requireArchiveTool(t)
	root := t.TempDir()
	var data bytes.Buffer
	writer := zip.NewWriter(&data)
	for _, content := range []string{"first", "second"} {
		entry, err := writer.Create("same.txt")
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.WriteString(entry, content)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "duplicates.zip"), data.Bytes(), 0640); err != nil {
		t.Fatal(err)
	}
	if err := run(testConfig(root, ModeExtract), io.Discard); err == nil {
		t.Fatal("accepted duplicate archive members")
	}
	assertLayout(t, root, map[string]string{"duplicates.zip": data.String()})
}
