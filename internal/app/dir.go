package app

import (
	"path/filepath"
	"sort"
)

func isMetadataFile(path string) bool {
	return filepath.Base(path) == ".DS_Store"
}

func (t *tree) planEmptyDirs(config Config, planned *plan, removeMetadata, keepChildren bool) error {
	if removeMetadata {
		for _, entry := range t.sortedNodes() {
			// Classify the original path, never a name produced by this plan.
			if !isMetadataFile(entry.file.path) || !config.includes(entry.path, entry.file.info) {
				continue
			}
			supported, err := dedupContentSupported(entry.file)
			if err != nil {
				return err
			}
			if supported {
				t.planDelete(planned, entry, nil)
			}
		}
	}
	var directories []*node
	for _, entry := range t.nodes {
		if entry.file.info.IsDir() {
			directories = append(directories, entry)
		}
	}
	// Descendants always precede their parents, including in a dry-run plan.
	sort.Slice(directories, func(i, j int) bool {
		if len(directories[i].path) != len(directories[j].path) {
			return len(directories[i].path) > len(directories[j].path)
		}
		return directories[i].path < directories[j].path
	})
	for _, directory := range directories {
		if pathKey(directory.path) == pathKey(t.root) || config.ignored(directory.path) ||
			(keepChildren && pathKey(filepath.Dir(directory.path)) == pathKey(t.root)) {
			continue
		}
		if t.children[pathKey(directory.path)] == 0 {
			planned.operations = append(planned.operations, operation{
				kind: "rmdir", root: t.root, from: directory.path, file: directory.file,
			})
			t.remove(directory.path)
		}
	}
	return nil
}
