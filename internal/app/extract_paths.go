package app

import (
	"fmt"
	"path"
	"runtime"
	"sort"
	"strings"
	"unicode/utf8"
)

func archivePathKey(name string) string {
	if runtime.GOOS == "windows" {
		return strings.ToUpper(name)
	}
	return name
}

// planArchivePaths reserves every original name before shortening anything.
// Shared directory prefixes receive one mapping, including implicit parents.
func planArchivePaths(members []archiveMember) (map[string]string, error) {
	kinds := map[string]bool{".": true}
	identities := make(map[string]string)
	add := func(name string, directory bool) error {
		key := archivePathKey(name)
		if previous, ok := identities[key]; ok && previous != name {
			return fmt.Errorf("archive paths differ only in case: %q and %q", previous, name)
		}
		identities[key] = name
		if previous, ok := kinds[name]; ok && previous != directory {
			return fmt.Errorf("archive path is both a file and a directory: %q", name)
		}
		kinds[name] = directory
		return nil
	}
	for _, member := range members {
		if err := add(member.relative, member.directory); err != nil {
			return nil, err
		}
		for parent := path.Dir(member.relative); parent != "."; parent = path.Dir(parent) {
			if err := add(parent, true); err != nil {
				return nil, err
			}
		}
	}
	children := make(map[string][]string)
	for name := range kinds {
		if name != "." {
			children[path.Dir(name)] = append(children[path.Dir(name)], name)
		}
	}
	result := map[string]string{".": "."}
	queue := []string{"."}
	for len(queue) != 0 {
		parent := queue[0]
		queue = queue[1:]
		siblings := children[parent]
		sort.Strings(siblings)
		occupied := make(map[string]bool)
		for _, source := range siblings {
			if name := path.Base(source); len(name) <= 255 {
				occupied[archivePathKey(name)] = true
			}
		}
		for _, source := range siblings {
			name := path.Base(source)
			if len(name) > 255 {
				for suffix := 0; ; suffix++ {
					candidate := shortenedArchiveName(path.Base(source), kinds[source], suffix)
					if !occupied[archivePathKey(candidate)] {
						name = candidate
						break
					}
				}
			}
			occupied[archivePathKey(name)] = true
			result[source] = path.Join(result[parent], name)
			if kinds[source] {
				queue = append(queue, source)
			}
		}
	}
	return result, nil
}

func shortenedArchiveName(name string, directory bool, suffix int) string {
	extension := ""
	if !directory {
		extension = path.Ext(name)
		if compound := detectArchiveExt(name); len(compound) > len(extension) {
			extension = name[len(name)-len(compound):]
		}
		if extension == name {
			extension = "" // Preserve dotfiles as a single stem.
		}
	}
	stem := strings.TrimSuffix(name, extension)
	number := ""
	if suffix != 0 {
		number = fmt.Sprintf(" (%d)", suffix)
	}
	budget := 255 - len(number)
	if len(extension) >= budget {
		_, first := utf8.DecodeRuneInString(stem)
		extension = strings.TrimRight(utf8Prefix(extension, budget-first), " .")
	}
	stem = strings.TrimRight(utf8Prefix(stem, budget-len(extension)), " .")
	if stem == "" {
		stem = "_"
		extension = utf8Prefix(extension, budget-1)
	}
	return stem + number + extension
}
