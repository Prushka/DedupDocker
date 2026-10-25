package app

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

func TestExtractReviewDirectoryPayloadRetainsArchive(t *testing.T) {
	requireArchiveTool(t)
	root := t.TempDir()
	var data bytes.Buffer
	writer := zip.NewWriter(&data)
	header := &zip.FileHeader{Name: "directory", Method: zip.Deflate}
	header.SetMode(os.ModeDir | 0755)
	entry, err := writer.CreateHeader(header)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(entry, "directory payload"); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(root, "payload.zip")
	if err := os.WriteFile(archive, data.Bytes(), 0640); err != nil {
		t.Fatal(err)
	}
	if err := run(testConfig(root, ModeExtract), io.Discard); err == nil {
		t.Fatal("discarded directory payload and reported extraction success")
	}
	assertLayout(t, root, map[string]string{"payload.zip": data.String()})
}

func TestExtractReviewVerifiesEmptyDirectories(t *testing.T) {
	for _, change := range []string{"missing", "extra", "directory-to-file"} {
		t.Run(change, func(t *testing.T) {
			var data bytes.Buffer
			writer := tar.NewWriter(&data)
			if err := writer.WriteHeader(&tar.Header{Name: "empty/", Typeflag: tar.TypeDir, Mode: 0755}); err != nil {
				t.Fatal(err)
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			archive := filepath.Join(t.TempDir(), "empty.tar")
			if err := os.WriteFile(archive, data.Bytes(), 0640); err != nil {
				t.Fatal(err)
			}
			root, err := os.OpenRoot(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			manifest, err := unpackTar(context.Background(), archive, root)
			if err != nil {
				t.Fatal(err)
			}
			if err := verifyExtractedTree(context.Background(), root, manifest); err != nil {
				t.Fatal(err)
			}
			if change == "missing" || change == "directory-to-file" {
				err = root.Remove("empty")
			} else {
				err = root.Mkdir("unexpected", 0755)
			}
			if err != nil {
				t.Fatal(err)
			}
			if change == "directory-to-file" {
				if err := root.WriteFile("empty", nil, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := verifyExtractedTree(context.Background(), root, manifest); err == nil {
				t.Fatal("accepted a changed empty-directory tree")
			}
		})
	}
}

func TestExtractReviewPreservesEmptyDirectories(t *testing.T) {
	requireArchiveTool(t)
	for _, format := range []string{"zip", "tar"} {
		t.Run(format, func(t *testing.T) {
			root := t.TempDir()
			var data bytes.Buffer
			if format == "zip" {
				writer := zip.NewWriter(&data)
				if _, err := writer.Create("nested/empty/"); err != nil {
					t.Fatal(err)
				}
				if err := writer.Close(); err != nil {
					t.Fatal(err)
				}
			} else {
				writer := tar.NewWriter(&data)
				if err := writer.WriteHeader(&tar.Header{Name: "nested/empty/", Typeflag: tar.TypeDir, Mode: 0755}); err != nil {
					t.Fatal(err)
				}
				if err := writer.Close(); err != nil {
					t.Fatal(err)
				}
			}
			archive := filepath.Join(root, "archive."+format)
			if err := os.WriteFile(archive, data.Bytes(), 0640); err != nil {
				t.Fatal(err)
			}
			if err := run(testConfig(root, ModeExtract), io.Discard); err != nil {
				t.Fatal(err)
			}
			if info, err := os.Stat(filepath.Join(root, "archive", "nested", "empty")); err != nil || !info.IsDir() {
				t.Fatalf("empty directory not preserved: %v", err)
			}
			if _, err := os.Stat(archive); !os.IsNotExist(err) {
				t.Fatalf("verified archive was not removed: %v", err)
			}
		})
	}
}

type extractReviewContext struct {
	context.Context
	mu   sync.Mutex
	hook func()
}

func (ctx *extractReviewContext) Err() error {
	ctx.mu.Lock()
	defer ctx.mu.Unlock()
	ctx.hook()
	return ctx.Context.Err()
}

func TestExtractReviewMissingPublishedDirectoryRetainsArchive(t *testing.T) {
	requireArchiveTool(t)
	root := t.TempDir()
	var data bytes.Buffer
	writer := tar.NewWriter(&data)
	if err := writer.WriteHeader(&tar.Header{Name: "empty-child/", Typeflag: tar.TypeDir, Mode: 0755}); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(root, "empty.tar")
	if err := os.WriteFile(archive, data.Bytes(), 0640); err != nil {
		t.Fatal(err)
	}
	removed := false
	ctx := &extractReviewContext{Context: context.Background(), hook: func() {
		if !removed && os.Remove(filepath.Join(root, "empty", "empty-child")) == nil {
			removed = true
		}
	}}
	if err := runContext(ctx, testConfig(root, ModeExtract), io.Discard); err == nil {
		t.Fatal("removed the original after its published empty directory disappeared")
	}
	if !removed {
		t.Fatal("publication hook did not run")
	}
	retained, err := os.ReadFile(archive)
	if err != nil || !bytes.Equal(retained, data.Bytes()) {
		t.Fatalf("original archive was not retained intact: %v", err)
	}
}

func TestExtractReviewNativeBackslashNames(t *testing.T) {
	requireArchiveTool(t)
	for _, format := range []string{"tar", "gz"} {
		t.Run(format, func(t *testing.T) {
			root := t.TempDir()
			name := "literal\\name.txt"
			content := "literal filename payload"
			var data bytes.Buffer
			if format == "tar" {
				writer := tar.NewWriter(&data)
				if err := writer.WriteHeader(&tar.Header{Name: name, Mode: 0644, Size: int64(len(content))}); err != nil {
					t.Fatal(err)
				}
				_, _ = io.WriteString(writer, content)
				if err := writer.Close(); err != nil {
					t.Fatal(err)
				}
			} else {
				writer := gzip.NewWriter(&data)
				writer.Name = name
				_, _ = io.WriteString(writer, content)
				if err := writer.Close(); err != nil {
					t.Fatal(err)
				}
			}
			archive := filepath.Join(root, "archive."+format)
			if err := os.WriteFile(archive, data.Bytes(), 0640); err != nil {
				t.Fatal(err)
			}
			err := run(testConfig(root, ModeExtract), io.Discard)
			if runtime.GOOS == "windows" {
				if err == nil {
					t.Fatal("silently rewrote an unrepresentable native archive filename")
				}
				assertLayout(t, root, map[string]string{"archive." + format: data.String()})
			} else {
				if err != nil {
					t.Fatal(err)
				}
				assertLayout(t, root, map[string]string{"archive/" + name: content})
			}
		})
	}
}

func TestExtractReviewPAXGlobalComment(t *testing.T) {
	requireArchiveTool(t)
	root := t.TempDir()
	var data bytes.Buffer
	writer := tar.NewWriter(&data)
	if err := writer.WriteHeader(&tar.Header{Typeflag: tar.TypeXGlobalHeader, PAXRecords: map[string]string{"comment": "ordinary global comment"}}); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteHeader(&tar.Header{Name: "payload.txt", Mode: 0644, Size: 7}); err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(writer, "payload")
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "archive.tar"), data.Bytes(), 0640); err != nil {
		t.Fatal(err)
	}
	if err := run(testConfig(root, ModeExtract), io.Discard); err != nil {
		t.Fatal(err)
	}
	assertLayout(t, root, map[string]string{"archive/payload.txt": "payload"})
}

func TestExtractReviewUnsupportedPAXGlobals(t *testing.T) {
	for _, key := range []string{"path", "size", "mtime", "SCHILY.xattr.user.test", "SCHILY.acl.access"} {
		t.Run(key, func(t *testing.T) {
			var data bytes.Buffer
			writer := tar.NewWriter(&data)
			if err := writer.WriteHeader(&tar.Header{Typeflag: tar.TypeXGlobalHeader, PAXRecords: map[string]string{key: "1"}}); err != nil {
				t.Fatal(err)
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			archive := filepath.Join(t.TempDir(), "global.tar")
			if err := os.WriteFile(archive, data.Bytes(), 0640); err != nil {
				t.Fatal(err)
			}
			root, err := os.OpenRoot(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			if _, err := unpackTar(context.Background(), archive, root); err == nil {
				t.Fatal("ignored global metadata that changes extraction semantics")
			}
		})
	}
}

// Fixed fixtures contain "stream payload", either directly or in payload.txt
// inside a tar. Generated with Python's bz2, lzma and compression.zstd modules.
var reviewStreamFixtures = map[string][2]string{
	"bz2":  {"QlpoOTFBWSZTWZl1XxQAAAWRgEAAJgbcICAAIgGgaEAAwULGF9w8R4u5IpwoSEy6r4oA", "QlpoOTFBWSZTWaWJOUgAAHX7gMqABABAAW0AAgBmBt5gCAggAFQlKbUZAHpBoNqCSSeoaDQ0AGlO7iCEE3QhEumtM742IEMDHEZOJ2ETggymsJ55LFKHU+yhvUh4NRZfO7lXjIZEQPxdyRThQkKWJOUg"},
	"xz":   {"/Td6WFoAAAFpIt42AgAhAQAAAAA3J5fWAQANc3RyZWFtIHBheWxvYWQAAADaAjVhAAEiDgzejGCQQpkNAQAAAAABWVo=", "/Td6WFoAAATm1rRGAgAhARYAAAB0L+Wj4Cf/AGpdADgYS5l1D0YIb9M6IJf02WKG7Muw1HXRo4kjDp9TyFU19NR4F9sfaBvttAYhGyxI8Kfj3rLtFASm/dWePIimc1fnNItsqFUzarnYRC+M13kBXtDuGOnUtMOkQFG4VD7MyP86G26SCtN1AAAAAAB++HnDMM2vgAABhgGAUAAAI5I5t7HEZ/sCAAAAAARZWg=="},
	"zst":  {"KLUv/SAOcQAAc3RyZWFtIHBheWxvYWQ=", "KLUv/WAAJ7UCAEQDcGF5bG9hZC50eHQAMDAwMDY0NDAwMDAwMTYAADAxMDEzNAAgMAB1c3RhcgAwc3RyZWFtIAsAXCXnAUIyFCgQmUGAAuINqQEgOcAAWg1wrFymJpAE"},
	"lzma": {"XQAAgAD//////////wA5nQqMjIpEnC/AAXQiamqHMQP//4AaAAA=", "XQAAgAD//////////wA4GEuZdQ9GCG/TOiCX9NlihuzLsNR10aOJIw6fU8hVNfTUeBfbH2gb7bQGIRssSPCn496y7RQEpv3VnjyIpnNX5zSLbKhVM2q52EQvjNd5AV7Q7hjp1LTDpEBRuFQ+zMj/Ohtukgshd+3//+4JpAA="},
}

func TestExtractReviewSingleAndCompoundStreams(t *testing.T) {
	requireArchiveTool(t)
	for extension, fixtures := range reviewStreamFixtures {
		for index, fixture := range fixtures {
			suffix := "." + extension
			if index == 1 {
				suffix = ".tar" + suffix
			}
			t.Run(suffix, func(t *testing.T) {
				data, err := base64.StdEncoding.DecodeString(fixture)
				if err != nil {
					t.Fatal(err)
				}
				root := t.TempDir()
				if err := os.WriteFile(filepath.Join(root, "archive"+suffix), data, 0640); err != nil {
					t.Fatal(err)
				}
				if err := run(testConfig(root, ModeExtract), io.Discard); err != nil {
					t.Fatal(err)
				}
				name := "archive"
				if index == 1 {
					name = "payload.txt"
				}
				assertLayout(t, root, map[string]string{"archive/" + name: "stream payload"})
			})
		}
		alias := map[string]string{"bz2": ".tbz2", "xz": ".txz", "zst": ".tzst"}[extension]
		if alias != "" {
			t.Run(alias, func(t *testing.T) {
				data, err := base64.StdEncoding.DecodeString(fixtures[1])
				if err != nil {
					t.Fatal(err)
				}
				root := t.TempDir()
				if err := os.WriteFile(filepath.Join(root, "archive"+alias), data, 0640); err != nil {
					t.Fatal(err)
				}
				if err := run(testConfig(root, ModeExtract), io.Discard); err != nil {
					t.Fatal(err)
				}
				assertLayout(t, root, map[string]string{"archive/payload.txt": "stream payload"})
			})
		}
	}
}

func TestExtractReviewInvalidStreamsRetainOriginal(t *testing.T) {
	requireArchiveTool(t)
	for extension, fixtures := range reviewStreamFixtures {
		for _, kind := range []string{"truncated", "misnamed-zip", "compound-without-tar"} {
			t.Run(extension+"/"+kind, func(t *testing.T) {
				root := t.TempDir()
				archive := filepath.Join(root, "archive."+extension)
				data, err := base64.StdEncoding.DecodeString(fixtures[0])
				if err != nil {
					t.Fatal(err)
				}
				switch kind {
				case "truncated":
					data = data[:len(data)-8]
				case "misnamed-zip":
					writeArchiveZIP(t, archive, map[string]string{"a.txt": "first", "b.txt": "second"})
					data, err = os.ReadFile(archive)
					if err != nil {
						t.Fatal(err)
					}
				case "compound-without-tar":
					archive = filepath.Join(root, "archive.tar."+extension)
				}
				if err := os.WriteFile(archive, data, 0640); err != nil {
					t.Fatal(err)
				}
				if err := run(testConfig(root, ModeExtract), io.Discard); err == nil {
					t.Fatal("accepted an invalid compressed stream")
				}
				assertLayout(t, root, map[string]string{filepath.Base(archive): string(data)})
			})
		}
	}
}
