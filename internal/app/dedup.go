package app

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	log "github.com/sirupsen/logrus"
)

func (t *tree) planDedup(config Config, planned *plan, logger *log.Logger) error {
	type fileClass struct {
		size     int64
		metadata bool
	}
	bySize := make(map[fileClass][]*node)
	for _, entry := range t.sortedNodes() {
		if config.includes(entry.path, entry.file.info) {
			supported, err := dedupContentSupported(entry.file)
			if err != nil {
				return err
			}
			if !supported {
				logger.Warnf("Skipping duplicate removal for file with additional data or attributes: %s", entry.path)
				continue
			}
			class := fileClass{size: entry.file.info.Size(), metadata: isMetadataFile(entry.file.path)}
			if class.metadata && config.EmptyDir {
				continue // Metadata cleanup will remove these original files.
			}
			// A metadata filename must never become an ordinary file's retained
			// name: a later metadata cleanup could otherwise delete its last copy.
			bySize[class] = append(bySize[class], entry)
		}
	}
	var groups [][]*node
	for _, files := range bySize {
		if len(files) < 2 {
			continue
		}
		byHash := make(map[string][]*node)
		for _, entry := range files {
			hash, err := hashFile(entry.file)
			if err != nil {
				return err
			}
			byHash[hash] = append(byHash[hash], entry)
		}
		for _, duplicates := range byHash {
			if len(duplicates) > 1 {
				groups = append(groups, duplicates)
			}
		}
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i][0].path < groups[j][0].path })
	for _, group := range groups {
		// Preserve the shortest directory path and the longest duplicate filename.
		// Resolve ties lexically, independently of discovery or map iteration order.
		preferredDir := filepath.Dir(group[0].path)
		longestName := filepath.Base(group[0].path)
		for _, entry := range group[1:] {
			directory, name := filepath.Dir(entry.path), filepath.Base(entry.path)
			if len(directory) < len(preferredDir) || (len(directory) == len(preferredDir) && directory < preferredDir) {
				preferredDir = directory
			}
			if len(name) > len(longestName) || (len(name) == len(longestName) && name < longestName) {
				longestName = name
			}
		}
		target := filepath.Join(preferredDir, longestName)
		var keeper *node
		for _, entry := range group {
			if pathKey(filepath.Dir(entry.path)) == pathKey(preferredDir) {
				if keeper == nil || pathKey(entry.path) == pathKey(target) {
					keeper = entry
				}
			}
		}
		for _, entry := range group {
			if entry == keeper {
				continue
			}
			equal, err := sameContents(keeper.file, entry.file)
			if err != nil {
				return err
			}
			if !equal {
				return fmt.Errorf("hash collision between %q and %q; neither file was removed", keeper.path, entry.path)
			}
		}
		if pathKey(keeper.path) != pathKey(target) && config.includes(target, keeper.file.info) {
			if !t.destinationExists(target) {
				t.planMove(planned, keeper, target)
			} else {
				logger.Warnf("Keeping filename %s because destination %s already exists", keeper.path, target)
			}
		}
		for _, entry := range group {
			if entry != keeper && !os.SameFile(entry.file.info, keeper.file.info) {
				// These aliases already share storage. Preserve them rather than
				// weakening the retained file's protection to unlink another name.
				t.planDelete(planned, entry, keeper.file)
			}
		}
	}
	return nil
}
