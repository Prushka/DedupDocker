package app

import (
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"
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
		for suffix := 1; t.destinationExists(target); suffix++ {
			target = filepath.Join(child, collisionName(name, suffix))
		}
		if config.includes(target, entry.file.info) {
			t.planMove(planned, entry, target)
		}
	}
}

// Reserve room for the suffix within the common 255-byte component limit.
// The byte bound is conservative on Windows and never splits a UTF-8 rune.
func collisionName(name string, suffix int) string {
	ext := filepath.Ext(name)
	if ext == name { // Keep dotfiles such as .env recognizable.
		ext = ""
	}
	stem := strings.TrimSuffix(name, ext)
	number := fmt.Sprintf(" (%d)", suffix)
	budget := 255 - len(number)
	if len(ext) >= budget {
		// An unusually long extension cannot fit intact alongside the suffix.
		// Keep its beginning and at least one complete rune of the original stem.
		_, firstRuneBytes := utf8.DecodeRuneInString(stem)
		stem = utf8Prefix(stem, firstRuneBytes)
		ext = utf8Prefix(ext, budget-len(stem))
	} else {
		stem = utf8Prefix(stem, budget-len(ext))
	}
	return stem + number + ext
}

func utf8Prefix(value string, maxBytes int) string {
	if len(value) <= maxBytes {
		return value
	}
	for maxBytes > 0 && !utf8.RuneStart(value[maxBytes]) {
		maxBytes--
	}
	return value[:maxBytes]
}
