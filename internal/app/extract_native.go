package app

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
)

func unpackGzip(ctx context.Context, archive string, root *os.Root, fallback string) (files map[string]extractedFile, resultErr error) {
	input, err := os.Open(archive)
	if err != nil {
		return nil, err
	}
	defer func() { resultErr = errors.Join(resultErr, input.Close()) }()
	reader, err := gzip.NewReader(cancelReader{ctx: ctx, reader: input})
	if err != nil {
		return nil, err
	}
	defer func() { resultErr = errors.Join(resultErr, reader.Close()) }()
	name := reader.Name
	if name == "" {
		name = fallback
	}
	relative, err := safeNativeArchivePath(name)
	if err != nil || relative == "." {
		return nil, errors.Join(err, fmt.Errorf("invalid gzip output filename"))
	}
	paths, err := planArchivePaths([]archiveMember{{relative: relative}})
	if err != nil {
		return nil, err
	}
	relative = paths[relative]
	if err := root.MkdirAll(path.Dir(relative), 0755); err != nil {
		return nil, err
	}
	output, err := root.OpenFile(relative, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return nil, err
	}
	defer func() { resultErr = errors.Join(resultErr, output.Close()) }()
	hash := sha256.New()
	// gzip's size field wraps at 4 GiB. Read through verified EOF instead of
	// treating the 32-bit footer size as the total uncompressed byte count.
	written, err := io.Copy(io.MultiWriter(output, hash), cancelReader{ctx: ctx, reader: reader})
	if err != nil {
		return nil, err
	}
	if !reader.ModTime.IsZero() {
		if err := validateArchiveTimestamp(reader.ModTime); err != nil {
			return nil, err
		}
		if err := setFileTimes(output, reader.ModTime); err != nil {
			return nil, err
		}
	}
	if err := output.Sync(); err != nil {
		return nil, err
	}
	return map[string]extractedFile{relative: {size: written, digest: fmt.Sprintf("%x", hash.Sum(nil))}}, nil
}

func unpackTar(ctx context.Context, archive string, root *os.Root) (map[string]extractedFile, error) {
	var members []archiveMember
	err := walkTar(ctx, archive, func(header *tar.Header, relative string, input io.Reader) error {
		members = append(members, archiveMember{relative: relative, directory: header.Typeflag == tar.TypeDir})
		return nil
	})
	if err != nil {
		return nil, err
	}
	paths, err := planArchivePaths(members)
	if err != nil {
		return nil, err
	}
	files := make(map[string]extractedFile)
	err = walkTar(ctx, archive, func(header *tar.Header, original string, input io.Reader) error {
		relative := paths[original]
		if header.Typeflag == tar.TypeDir {
			if err := root.MkdirAll(relative, 0755); err != nil {
				return err
			}
			files[relative] = extractedFile{directory: true}
			return nil
		}
		if !header.ModTime.IsZero() {
			if err := validateArchiveTimestamp(header.ModTime); err != nil {
				return err
			}
		}
		record, err := writeArchiveMember(root, archiveMember{relative: relative, size: header.Size, modified: header.ModTime}, input)
		if err == nil {
			files[relative] = record
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	return files, nil
}

type archiveSeekReader struct {
	cancelReader
	seeker io.Seeker
}

func (reader archiveSeekReader) Seek(offset int64, whence int) (int64, error) {
	if err := contextError(reader.ctx); err != nil {
		return 0, err
	}
	return reader.seeker.Seek(offset, whence)
}

// Inventory tar names before writing anything, so unchanged names always win
// collisions. Seeking skips file payloads during this metadata pass; tar.Reader
// still checks their final byte and headers to detect truncated archives.
func walkTar(ctx context.Context, archive string, visit func(*tar.Header, string, io.Reader) error) (resultErr error) {
	input, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, input.Close()) }()
	stream := archiveSeekReader{cancelReader: cancelReader{ctx: ctx, reader: input}, seeker: input}
	reader := tar.NewReader(stream)
	seen := make(map[string]bool)
	for {
		header, err := reader.Next()
		if err == io.EOF {
			var buffer [32 * 1024]byte
			for {
				count, readErr := stream.Read(buffer[:])
				for _, value := range buffer[:count] {
					if value != 0 {
						return fmt.Errorf("tar has additional data after its end marker")
					}
				}
				if readErr == io.EOF {
					break
				}
				if readErr != nil {
					return readErr
				}
			}
			return nil
		}
		if err != nil {
			return err
		}
		if header.Typeflag == tar.TypeXGlobalHeader {
			// Go's tar.Reader returns global headers without applying them to
			// subsequent entries. Ignore extra metadata, but refuse structural
			// overrides that this reader cannot apply to subsequent file data.
			for key := range header.PAXRecords {
				switch key {
				case "path", "linkpath", "size", "mtime", "SCHILY.realsize":
					return fmt.Errorf("tar contains unsupported global file override: %q", key)
				}
				if strings.HasPrefix(key, "GNU.sparse.") {
					return fmt.Errorf("tar contains unsupported global sparse-file layout: %q", key)
				}
			}
			continue
		}
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeDir {
			return fmt.Errorf("tar contains a link or unsupported special entry")
		}
		relative, err := safeNativeArchivePath(header.Name)
		if err != nil {
			return err
		}
		directory := header.Typeflag == tar.TypeDir
		if relative == "." && !directory {
			return fmt.Errorf("tar root entry must be a directory")
		}
		key := archivePathKey(relative)
		if seen[key] {
			return fmt.Errorf("duplicate tar path: %q", relative)
		}
		seen[key] = true
		if directory && header.Size != 0 {
			return fmt.Errorf("tar directory contains payload that cannot be preserved")
		}
		if err := visit(header, relative, reader); err != nil {
			return err
		}
	}
}
