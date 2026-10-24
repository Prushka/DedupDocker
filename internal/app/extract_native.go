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
	relative, err := safeArchivePath(name)
	if err != nil || relative == "." {
		return nil, errors.Join(err, fmt.Errorf("invalid gzip output filename"))
	}
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

func unpackTar(ctx context.Context, archive string, root *os.Root) (files map[string]extractedFile, resultErr error) {
	input, err := os.Open(archive)
	if err != nil {
		return nil, err
	}
	defer func() { resultErr = errors.Join(resultErr, input.Close()) }()
	reader := tar.NewReader(cancelReader{ctx: ctx, reader: input})
	files = make(map[string]extractedFile)
	kinds := make(map[string]bool)
	for {
		header, err := reader.Next()
		if err == io.EOF {
			var buffer [32 * 1024]byte
			for {
				count, readErr := (cancelReader{ctx: ctx, reader: input}).Read(buffer[:])
				for _, value := range buffer[:count] {
					if value != 0 {
						return nil, fmt.Errorf("tar has additional data after its end marker")
					}
				}
				if readErr == io.EOF {
					break
				}
				if readErr != nil {
					return nil, readErr
				}
			}
			return files, nil
		}
		if err != nil {
			return nil, err
		}
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeDir {
			return nil, fmt.Errorf("tar contains a link or unsupported special entry")
		}
		if len(header.Xattrs) != 0 {
			return nil, fmt.Errorf("tar contains extended attributes that cannot be preserved")
		}
		for key := range header.PAXRecords {
			if strings.Contains(strings.ToLower(key), "acl") || strings.Contains(strings.ToLower(key), "xattr") {
				return nil, fmt.Errorf("tar contains additional permissions or attributes")
			}
		}
		relative, err := safeArchivePath(header.Name)
		if err != nil {
			return nil, err
		}
		directory := header.Typeflag == tar.TypeDir
		if relative == "." && !directory {
			return nil, fmt.Errorf("tar root entry must be a directory")
		}
		key := collisionKey(relative)
		if _, exists := kinds[key]; exists {
			return nil, fmt.Errorf("duplicate tar path: %q", relative)
		}
		kinds[key] = directory
		if directory {
			if err := root.MkdirAll(relative, 0755); err != nil {
				return nil, err
			}
			continue
		}
		record, err := writeArchiveMember(root, archiveMember{relative: relative, size: header.Size}, reader)
		if err != nil {
			return nil, err
		}
		if !header.ModTime.IsZero() {
			if err := validateArchiveTimestamp(header.ModTime); err != nil {
				return nil, err
			}
			if err := root.Chtimes(relative, header.ModTime, header.ModTime); err != nil {
				return nil, err
			}
		}
		files[relative] = record
	}
}
