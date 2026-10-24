package app

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	log "github.com/sirupsen/logrus"
)

type fileRecord struct {
	path string
	info os.FileInfo
}

type node struct {
	path string
	file *fileRecord
}

type tree struct {
	root     string
	nodes    map[string]*node
	children map[string]int
	occupied map[string]int
}

type operation struct {
	kind   string
	root   string
	from   string
	to     string
	file   *fileRecord
	keeper *fileRecord
}

type plan struct {
	operations     []operation
	dirs           map[string]os.FileInfo
	protectContent bool
}

func pathKey(path string) string {
	return filepath.Clean(path)
}

func collisionKey(path string) string {
	path = pathKey(path)
	if runtime.GOOS == "windows" {
		// Preserve distinct scanned paths on case-sensitive NTFS directories.
		// Reserve case variants of destination names conservatively within each
		// exact parent, including names created only in a dry-run plan.
		return filepath.Dir(path) + "\x00" + strings.ToUpper(filepath.Base(path))
	}
	return path
}

// Run loads the environment, plans the selected mode, and applies or previews it.
func Run() error {
	config, err := loadConfig()
	if err != nil {
		return err
	}
	return run(config, os.Stderr)
}

func run(config Config, output io.Writer) error {
	config, err := config.normalized()
	if err != nil {
		return err
	}
	logger := log.New()
	logger.SetOutput(output)
	level, _ := log.ParseLevel(config.Log)
	logger.SetLevel(level)
	planned, err := buildPlan(config, logger)
	if err != nil {
		return err
	}
	return planned.execute(config.DryRun, logger)
}

func scanTree(root string, config Config, planned *plan) (*tree, error) {
	result := &tree{root: root, nodes: make(map[string]*node), children: make(map[string]int), occupied: make(map[string]int)}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return fmt.Errorf("scan %q: %w", path, walkErr)
		}
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("inspect %q: %w", path, err)
		}
		record := &fileRecord{path: path, info: info}
		if !config.ignored(path) && (info.IsDir() || config.includes(path, info)) {
			record, err = newFileRecord(path, info)
			if err != nil {
				return err
			}
		}
		result.add(&node{path: path, file: record})
		if info.IsDir() {
			planned.dirs[pathKey(path)] = info
			if config.ignored(path) {
				return filepath.SkipDir
			}
		}
		return nil
	})
	return result, err
}

func (t *tree) add(entry *node) {
	t.nodes[pathKey(entry.path)] = entry
	t.children[pathKey(filepath.Dir(entry.path))]++
	t.occupied[collisionKey(entry.path)]++
}

func (t *tree) remove(path string) {
	delete(t.nodes, pathKey(path))
	t.children[pathKey(filepath.Dir(path))]--
	t.occupied[collisionKey(path)]--
}

func (t *tree) destinationExists(path string) bool {
	return t.occupied[collisionKey(path)] != 0
}

func (t *tree) sortedNodes() []*node {
	entries := make([]*node, 0, len(t.nodes))
	for _, entry := range t.nodes {
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].path < entries[j].path })
	return entries
}

func buildPlan(config Config, logger *log.Logger) (*plan, error) {
	planned := &plan{dirs: make(map[string]os.FileInfo), protectContent: config.Mode == ModeDedup}
	for _, root := range config.Roots {
		logger.Infof("Planning mode=%s dry_run=%t root=%s", config.Mode, config.DryRun, root)
		files, err := scanTree(root, config, planned)
		if err != nil {
			return nil, err
		}
		switch config.Mode {
		case ModeDedup:
			if err := files.planDedup(config, planned, logger); err != nil {
				return nil, err
			}
			if config.EmptyDir {
				if err := files.planEmptyDirs(config, planned, true, false); err != nil {
					return nil, err
				}
			}
		case ModeDeleteFilesIncluding:
			for _, entry := range files.sortedNodes() {
				if !config.includes(entry.path, entry.file.info) {
					continue
				}
				for _, substring := range config.DeleteFilesIncluding {
					if strings.Contains(filepath.Base(entry.path), substring) {
						files.planDelete(planned, entry, nil)
						break
					}
				}
			}
		case ModeFlatten:
			files.planFlatten(config, planned)
			if err := files.planEmptyDirs(config, planned, false, true); err != nil {
				return nil, err
			}
		}
	}
	return planned, nil
}

func (t *tree) planDelete(planned *plan, entry *node, keeper *fileRecord) {
	planned.operations = append(planned.operations, operation{
		kind: "delete", root: t.root, from: entry.path, file: entry.file, keeper: keeper,
	})
	t.remove(entry.path)
}

func (t *tree) planMove(planned *plan, entry *node, target string) {
	planned.operations = append(planned.operations, operation{
		kind: "move", root: t.root, from: entry.path, to: target, file: entry.file,
	})
	t.remove(entry.path)
	entry.path = target
	t.add(entry)
}

func (p *plan) verifyParents(root, path string) error {
	for parent := filepath.Dir(path); ; parent = filepath.Dir(parent) {
		expected, ok := p.dirs[pathKey(parent)]
		if !ok || !within(root, parent) {
			return fmt.Errorf("unplanned parent directory %q", parent)
		}
		actual, err := os.Lstat(parent)
		if err != nil {
			return fmt.Errorf("inspect parent %q: %w", parent, err)
		}
		if !actual.IsDir() || !os.SameFile(expected, actual) {
			return fmt.Errorf("parent directory changed: %q", parent)
		}
		if pathKey(parent) == pathKey(root) {
			return nil
		}
	}
}

func (p *plan) execute(dryRun bool, logger *log.Logger) error {
	// Preflight every original source before the first edit, including sources
	// that an earlier planned move will relocate.
	checked := make(map[*fileRecord]bool)
	for _, op := range p.operations {
		if err := p.verifyParents(op.root, op.from); err != nil {
			return err
		}
		if op.to != "" {
			if err := p.verifyParents(op.root, op.to); err != nil {
				return err
			}
		}
		for _, file := range []*fileRecord{op.file, op.keeper} {
			if file != nil && !checked[file] {
				if err := file.verify(); err != nil {
					return err
				}
				if p.protectContent && file.info.Mode().IsRegular() {
					if err := verifyDedupContent(file); err != nil {
						return err
					}
				}
				checked[file] = true
			}
		}
	}

	current := make(map[*fileRecord]*fileRecord)
	lookup := func(file *fileRecord) *fileRecord {
		if updated := current[file]; updated != nil {
			return updated
		}
		return file
	}
	var deleted, moved, directories int
	var bytesDeleted int64
	for _, op := range p.operations {
		if err := p.verifyParents(op.root, op.from); err != nil {
			return err
		}
		if !dryRun {
			file := lookup(op.file)
			switch op.kind {
			case "move":
				if err := p.verifyParents(op.root, op.to); err != nil {
					return err
				}
				updated, err := moveNoReplace(file, op.to)
				if err != nil {
					return err
				}
				current[op.file] = updated
			case "delete":
				if p.protectContent {
					if err := verifyDedupContent(file); err != nil {
						return err
					}
				}
				var deleteErr error
				if op.keeper != nil {
					keeper := lookup(op.keeper)
					if err := p.verifyParents(op.root, keeper.path); err != nil {
						return err
					}
					deleteErr = removeDuplicateVerified(file, keeper)
				} else {
					remove := removeVerified
					if p.protectContent {
						remove = removeVerifiedContent
					}
					deleteErr = remove(file)
				}
				if deleteErr != nil {
					return fmt.Errorf("delete %q: %w", file.path, deleteErr)
				}
			case "rmdir":
				if err := file.verify(); err != nil {
					return err
				}
				// Remove never recursively deletes a directory that acquired files.
				if err := os.Remove(op.from); err != nil {
					return fmt.Errorf("remove empty directory %q: %w", op.from, err)
				}
			}
		}
		prefix := ""
		if dryRun {
			prefix = "Would "
		}
		switch op.kind {
		case "move":
			moved++
			logger.Infof("%smove: %s -> %s", prefix, op.from, op.to)
		case "delete":
			deleted++
			bytesDeleted += op.file.info.Size()
			logger.Infof("%sdelete: %s", prefix, op.from)
		case "rmdir":
			directories++
			logger.Infof("%sremove empty directory: %s", prefix, op.from)
		}
	}
	if dryRun {
		logger.Infof("Dry run: would delete %d files (%d bytes), move %d files, and remove %d empty directories", deleted, bytesDeleted, moved, directories)
	} else {
		logger.Infof("%d files deleted (%d bytes), %d files moved, %d empty directories removed", deleted, bytesDeleted, moved, directories)
	}
	return nil
}

func verifyDedupContent(file *fileRecord) error {
	supported, err := dedupContentSupported(file)
	if err != nil {
		return err
	}
	if !supported {
		return fmt.Errorf("additional file contents must be preserved: %q", file.path)
	}
	return nil
}
