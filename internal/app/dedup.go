package app

import (
	"fmt"
	"path/filepath"
	"sort"

	log "github.com/sirupsen/logrus"
)

func (t *tree) planDedup(config Config, planned *plan, logger *log.Logger) error {
	bySize := make(map[int64][]*node)
	for _, entry := range t.sortedNodes() {
		if config.includes(entry.path, entry.file.info) {
			bySize[entry.file.info.Size()] = append(bySize[entry.file.info.Size()], entry)
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
			if t.nodes[pathKey(target)] == nil {
				t.planMove(planned, keeper, target)
			} else {
				logger.Warnf("Keeping filename %s because destination %s already exists", keeper.path, target)
			}
		}
		for _, entry := range group {
			if entry != keeper {
				t.planDelete(planned, entry, keeper.file)
			}
		}
	}
	return nil
}
