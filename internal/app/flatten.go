package app

import (
	"fmt"
	"path/filepath"
	"strings"
)

func (t *tree) planFlatten(config Config, planned *plan) {
	for _, entry := range t.sortedNodes() {
		if !config.includes(entry.path, entry.file.info) {
			continue
		}
		relative, _ := filepath.Rel(t.root, entry.path)
		parts := strings.SplitN(relative, string(filepath.Separator), 2)
		if len(parts) < 2 {
			continue // Files directly in ROOT stay in place.
		}
		child := filepath.Join(t.root, parts[0])
		if pathKey(filepath.Dir(entry.path)) == pathKey(child) {
			continue
		}
		name := filepath.Base(entry.path)
		target := filepath.Join(child, name)
		for suffix := 1; t.nodes[pathKey(target)] != nil; suffix++ {
			ext := filepath.Ext(name)
			if ext == name { // Keep dotfiles such as .env recognizable.
				ext = ""
			}
			target = filepath.Join(child, fmt.Sprintf("%s (%d)%s", strings.TrimSuffix(name, ext), suffix, ext))
		}
		if config.includes(target, entry.file.info) {
			t.planMove(planned, entry, target)
		}
	}
}
