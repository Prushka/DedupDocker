package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

var archiveExtensions = []string{
	".tar.gz", ".tar.bz2", ".tar.xz", ".tar.zst", ".tar.lzma",
	".zip", ".rar", ".7z", ".wim", ".tar", ".gz", ".tgz", ".tbz2", ".txz", ".tzst", ".bz2", ".xz", ".zst", ".lzma",
}

func detectArchiveExt(name string) string {
	lower := strings.ToLower(name)
	for _, extension := range archiveExtensions {
		if strings.HasSuffix(lower, extension) {
			return extension
		}
	}
	return ""
}

func archiveStreamFormat(extension string) string {
	switch extension {
	case ".bz2", ".tar.bz2", ".tbz2":
		return "bzip2"
	case ".xz", ".tar.xz", ".txz":
		return "xz"
	case ".zst", ".tar.zst", ".tzst":
		return "zstd"
	case ".lzma", ".tar.lzma":
		return "lzma"
	}
	return ""
}

func archiveFolderName(base string, suffix int) string {
	if base == "" || base == "." || base == ".." {
		base = "_"
	}
	ending := ""
	if suffix != 0 {
		ending = fmt.Sprintf(" (%d)", suffix)
	}
	return utf8Prefix(base, 255-len(ending)) + ending
}

func (t *tree) planExtract(config Config, planned *plan) {
	for _, entry := range t.sortedNodes() {
		extension := detectArchiveExt(entry.path)
		if extension == "" || !config.includes(entry.path, entry.file.info) || config.IsExcluded(extension) {
			continue
		}
		name := filepath.Base(entry.path)
		base := name[:len(name)-len(extension)] // Match suffixes without regard to case.
		target := filepath.Join(filepath.Dir(entry.path), archiveFolderName(base, 0))
		for suffix := 1; t.destinationExists(target); suffix++ {
			target = filepath.Join(filepath.Dir(entry.path), archiveFolderName(base, suffix))
		}
		if config.ignored(target) {
			continue
		}
		t.occupied[collisionKey(target)]++
		planned.operations = append(planned.operations, operation{kind: "extract", root: t.root, from: entry.path, to: target, file: entry.file})
	}
}

func snapshotArchive(source *fileRecord, root *os.Root, name string) (snapshot *fileRecord, resultErr error) {
	input, err := source.open()
	if err != nil {
		return nil, err
	}
	defer func() { resultErr = errors.Join(resultErr, input.Close()) }()
	output, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	defer func() { resultErr = errors.Join(resultErr, output.Close()) }()
	written, err := io.Copy(output, cancelReader{ctx: source.ctx, reader: input})
	if err != nil {
		return nil, err
	}
	if written != source.info.Size() {
		return nil, fmt.Errorf("archive size changed while copying")
	}
	if err := errors.Join(source.verify(), output.Sync()); err != nil {
		return nil, err
	}
	info, err := output.Stat()
	if err != nil {
		return nil, err
	}
	return &fileRecord{path: filepath.Join(root.Name(), filepath.FromSlash(name)), info: info, ctx: source.ctx}, nil
}

func extractArchive(ctx context.Context, source *fileRecord, destination string, parentInfo os.FileInfo, passwords []string) (resultErr error) {
	tool, err := findArchiveTool()
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	stage, err := os.MkdirTemp(filepath.Dir(destination), ".dedup-extract-")
	if err != nil {
		return err
	}
	stageInfo, err := os.Lstat(stage)
	if err != nil {
		return err
	}
	stageRoot, err := os.OpenRoot(stage)
	if err != nil {
		return errors.Join(err, removeEmptyDirectory(&fileRecord{path: stage, info: stageInfo}))
	}
	defer func() {
		// Remove only children through the held root, then remove its verified
		// original name. A replaced staging path cannot redirect this cleanup.
		for _, name := range []string{"input", "out", "decompressed"} {
			resultErr = errors.Join(resultErr, stageRoot.RemoveAll(name))
		}
		resultErr = errors.Join(resultErr, stageRoot.Close())
		resultErr = errors.Join(resultErr, removeEmptyDirectory(&fileRecord{path: stage, info: stageInfo}))
	}()
	if err := stageRoot.Mkdir("input", 0700); err != nil {
		return err
	}
	snapshot, err := snapshotArchive(source, stageRoot, "input/"+filepath.Base(source.path))
	if err != nil {
		return fmt.Errorf("snapshot archive: %w", err)
	}
	equal, err := sameContents(source, snapshot)
	if err != nil || !equal {
		return errors.Join(err, fmt.Errorf("archive changed while taking a snapshot"))
	}
	extension := detectArchiveExt(source.path)
	// Single-stream decoders have no member inventory. Force their format so
	// an unrelated archive cannot be concatenated into a single output file.
	tool.format = archiveStreamFormat(extension)
	password, err := tool.password(ctx, snapshot.path, passwords)
	if err != nil {
		return fmt.Errorf("validate archive: %w", err)
	}
	if err := stageRoot.Mkdir("out", 0700); err != nil {
		return err
	}
	outputRoot, err := stageRoot.OpenRoot("out")
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, outputRoot.Close()) }()
	archive := snapshot.path
	if strings.HasPrefix(extension, ".tar.") || extension == ".tgz" || extension == ".tbz2" || extension == ".txz" || extension == ".tzst" {
		if err := stageRoot.Mkdir("decompressed", 0700); err != nil {
			return err
		}
		intermediate, err := stageRoot.OpenRoot("decompressed")
		if err != nil {
			return err
		}
		var files map[string]extractedFile
		var unpackErr error
		fallback := filepath.Base(source.path)
		fallback = fallback[:len(fallback)-len(extension)] + ".tar"
		if extension == ".tar.gz" || extension == ".tgz" {
			files, unpackErr = unpackGzip(ctx, archive, intermediate, fallback)
		} else {
			files, unpackErr = tool.unpackSingleStream(ctx, archive, password, intermediate, fallback)
		}
		closeErr := intermediate.Close()
		if err := errors.Join(unpackErr, closeErr); err != nil {
			return err
		}
		if len(files) != 1 {
			return fmt.Errorf("compound archive must contain exactly one tar stream")
		}
		for name := range files {
			archive = filepath.Join(stage, "decompressed", filepath.FromSlash(name))
		}
		tool.format = "tar"
		password, err = tool.password(ctx, archive, nil)
		if err != nil {
			return err
		}
	}
	var files map[string]extractedFile
	if tool.format == "tar" || extension == ".tar" {
		files, err = unpackTar(ctx, archive, outputRoot)
	} else if extension == ".gz" {
		base := filepath.Base(source.path)
		files, err = unpackGzip(ctx, archive, outputRoot, base[:len(base)-len(extension)])
	} else if tool.format != "" {
		base := filepath.Base(source.path)
		files, err = tool.unpackSingleStream(ctx, archive, password, outputRoot, base[:len(base)-len(extension)])
	} else {
		files, err = tool.unpack(ctx, archive, password, outputRoot)
	}
	if err != nil {
		return fmt.Errorf("unpack archive: %w", err)
	}
	if err := setExtractedOwnership(outputRoot, source.info); err != nil {
		return fmt.Errorf("set extracted ownership and permissions: %w", err)
	}
	if err := verifyExtractedTree(ctx, outputRoot, files); err != nil {
		return fmt.Errorf("verify staged extraction: %w", err)
	}
	// Capture the original only for the short publication/removal phase. Long
	// decompression and password attempts leave the original name untouched.
	original, err := captureFile(source, false, true)
	if err != nil {
		return fmt.Errorf("capture original archive for removal: %w", err)
	}
	defer func() { resultErr = errors.Join(resultErr, original.close()) }()
	equal, err = sameContents(original.record(), snapshot)
	if err != nil || !equal {
		return errors.Join(err, fmt.Errorf("archive changed before publication"))
	}
	outputInfo, err := outputRoot.Stat(".")
	if err != nil {
		return err
	}
	output := &fileRecord{path: filepath.Join(stage, "out"), info: outputInfo}
	if err := publishArchiveDirectory(output, destination, parentInfo); err != nil {
		return fmt.Errorf("publish extracted directory: %w", err)
	}
	// The held root still refers to the published directory, even if its name
	// is moved. Verify both its identity and all output bytes before deletion.
	actual, err := os.Lstat(destination)
	if err != nil || !os.SameFile(outputInfo, actual) {
		return errors.Join(err, fmt.Errorf("extraction destination changed before archive removal"))
	}
	if err := verifyExtractedTree(ctx, outputRoot, files); err != nil {
		return fmt.Errorf("verify published extraction: %w", err)
	}
	if err := syncArchiveParent(destination); err != nil {
		return fmt.Errorf("sync extraction parent directory: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := original.remove(); err != nil {
		return fmt.Errorf("remove verified original archive: %w", err)
	}
	return nil
}
