package app

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"io/fs"
	"os"
	"path"
	"strings"
)

type extractedFile struct {
	size   int64
	digest string
}

func archiveOutputMode(source os.FileInfo, directory bool) os.FileMode {
	mode := source.Mode().Perm()&0666 | 0600
	if directory {
		mode |= 0100 | (mode&0044)>>2
	}
	return mode
}

func (tool archiveTool) stream(ctx context.Context, archive, password string, consume func(io.Reader) error, selector ...string) error {
	cmd := tool.command(ctx, "x", archive, password, "-so")
	cmd.Args = append(cmd.Args, selector...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return archiveCommandError(ctx, "extract", err)
	}
	readErr := consume(cancelReader{ctx: ctx, reader: stdout})
	if readErr == nil {
		var extra [1]byte
		count, err := stdout.Read(extra[:])
		if count != 0 || err != io.EOF {
			readErr = fmt.Errorf("extracted data does not match the archive inventory")
		}
	}
	if readErr != nil {
		_ = cmd.Process.Kill()
	}
	waitErr := cmd.Wait()
	if readErr != nil {
		return readErr
	}
	if waitErr != nil {
		return archiveCommandError(ctx, "extract", waitErr)
	}
	return nil
}

func writeArchiveMember(root *os.Root, member archiveMember, input io.Reader) (record extractedFile, resultErr error) {
	if err := root.MkdirAll(path.Dir(member.relative), 0755); err != nil {
		return record, err
	}
	file, err := root.OpenFile(member.relative, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return record, err
	}
	defer func() { resultErr = errors.Join(resultErr, file.Close()) }()
	hash, checksum := sha256.New(), crc32.NewIEEE()
	written, err := io.CopyN(io.MultiWriter(file, hash, checksum), input, member.size)
	if err != nil {
		return record, fmt.Errorf("read archived file %q: %w", member.relative, err)
	}
	if member.crc != "" && !strings.EqualFold(member.crc, fmt.Sprintf("%08X", checksum.Sum32())) {
		return record, fmt.Errorf("archive entry order or checksum differs for %q", member.relative)
	}
	if !member.modified.IsZero() {
		if err := setFileTimes(file, member.modified); err != nil {
			return record, err
		}
	}
	if err := errors.Join(file.Chmod(0644), file.Sync()); err != nil {
		return record, err
	}
	return extractedFile{size: written, digest: fmt.Sprintf("%x", hash.Sum(nil))}, nil
}

func (tool archiveTool) unpack(ctx context.Context, archive, password string, root *os.Root) (map[string]extractedFile, error) {
	members, err := tool.list(ctx, archive, password)
	if err != nil {
		return nil, err
	}
	if err := tool.verifySelectors(ctx, archive, password, members); err != nil {
		return nil, err
	}
	files := make(map[string]extractedFile)
	batch := true
	for _, member := range members {
		if member.directory {
			if err := root.MkdirAll(member.relative, 0755); err != nil {
				return nil, err
			}
		} else if member.size != 0 && len(member.crc) != 8 {
			batch = false
		}
	}
	if batch {
		// Solid archives must be decompressed once rather than once per file.
		// Per-entry CRCs verify the concatenated stream's order and boundaries.
		err = tool.stream(ctx, archive, password, func(input io.Reader) error {
			for _, member := range members {
				if member.directory {
					continue
				}
				record, err := writeArchiveMember(root, member, input)
				if err != nil {
					return err
				}
				files[member.relative] = record
			}
			return nil
		})
	} else {
		// Formats without per-entry checksums (notably tar and WIM) need exact
		// member selection so the stream cannot be assigned to a different name.
		for _, member := range members {
			if member.directory {
				continue
			}
			err = tool.stream(ctx, archive, password, func(input io.Reader) error {
				record, err := writeArchiveMember(root, member, input)
				files[member.relative] = record
				return err
			}, member.name)
			if err != nil {
				break
			}
		}
	}
	if err != nil {
		return nil, err
	}
	return files, nil
}

func setExtractedOwnership(root *os.Root, source os.FileInfo) error {
	return fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		file, err := root.Open(name)
		if err != nil {
			return err
		}
		return errors.Join(setArchiveOwnership(file, source, entry.IsDir()), file.Close())
	})
}

func verifyExtractedTree(ctx context.Context, root *os.Root, expected map[string]extractedFile) error {
	seen := make(map[string]bool)
	err := fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		info, err := root.Lstat(name)
		if err != nil {
			return err
		}
		if info.IsDir() {
			file, err := root.Open(name)
			if err != nil {
				return err
			}
			return errors.Join(syncArchiveDirectory(file), file.Close())
		}
		record, ok := expected[name]
		if !ok || !info.Mode().IsRegular() || info.Size() != record.size {
			return fmt.Errorf("extracted tree changed at %q", name)
		}
		file, err := root.Open(name)
		if err != nil {
			return err
		}
		hash := sha256.New()
		_, readErr := io.Copy(hash, cancelReader{ctx: ctx, reader: file})
		if err := errors.Join(readErr, file.Close()); err != nil {
			return err
		}
		if fmt.Sprintf("%x", hash.Sum(nil)) != record.digest {
			return fmt.Errorf("extracted file contents changed: %q", name)
		}
		seen[name] = true
		return nil
	})
	if err != nil {
		return err
	}
	if len(seen) != len(expected) {
		return fmt.Errorf("extracted files are missing; original archive retained")
	}
	return nil
}
