package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	log "github.com/sirupsen/logrus"
)

const (
	ModeDedup                = "dedup"
	ModeDeleteFilesIncluding = "delete-files-including"
	ModeFlatten              = "flatten"
)

type Config struct {
	Roots                []string
	Mode                 string
	DryRun               bool
	Log                  string
	EmptyDir             bool
	MinSize              int64
	ExcludeExt           []string
	DeleteFilesIncluding []string
	IgnoreKeywords       []string
}

func loadConfig() (Config, error) {
	value := func(name, fallback string) string {
		if raw := strings.TrimSpace(os.Getenv(name)); raw != "" {
			return raw
		}
		return fallback
	}
	root, set := os.LookupEnv("ROOT")
	if !set {
		root = "/dedup"
	}
	config := Config{
		Roots: strings.Split(root, ","), Mode: value("MODE", ModeDedup), Log: value("LOG", "debug"),
		ExcludeExt:           strings.Split(os.Getenv("EXCLUDE_EXT"), ","),
		IgnoreKeywords:       strings.Split(os.Getenv("IGNORE_KEYWORDS"), ","),
		DeleteFilesIncluding: strings.Split(os.Getenv("DELETE_FILES_INCLUDING"), ","),
	}
	var err error
	config.DryRun, err = strconv.ParseBool(value("DRY_RUN", "true"))
	if err != nil {
		return Config{}, fmt.Errorf("invalid DRY_RUN: %w", err)
	}
	config.EmptyDir, err = strconv.ParseBool(value("EMPTY_DIR", "false"))
	if err != nil {
		return Config{}, fmt.Errorf("invalid EMPTY_DIR: %w", err)
	}
	config.MinSize, err = strconv.ParseInt(value("MIN_SIZE", "0"), 10, 64)
	if err != nil {
		return Config{}, fmt.Errorf("invalid MIN_SIZE: %w", err)
	}
	return config.normalized()
}

func cleanList(values []string) []string {
	var cleaned []string
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			cleaned = append(cleaned, value)
		}
	}
	return cleaned
}

func (c Config) normalized() (Config, error) {
	c.Mode = strings.TrimSpace(c.Mode)
	switch c.Mode {
	case ModeDedup, ModeDeleteFilesIncluding, ModeFlatten:
	default:
		return Config{}, fmt.Errorf("invalid MODE %q: use dedup, delete-files-including, or flatten", c.Mode)
	}
	if c.MinSize < 0 {
		return Config{}, fmt.Errorf("MIN_SIZE must be nonnegative")
	}
	if _, err := log.ParseLevel(c.Log); err != nil {
		return Config{}, fmt.Errorf("invalid LOG %q: %w", c.Log, err)
	}
	c.Roots = cleanList(c.Roots)
	c.ExcludeExt = cleanList(c.ExcludeExt)
	c.DeleteFilesIncluding = cleanList(c.DeleteFilesIncluding)
	c.IgnoreKeywords = cleanList(c.IgnoreKeywords)
	if len(c.Roots) == 0 {
		return Config{}, fmt.Errorf("ROOT must contain at least one directory")
	}
	// Reject missing, symbolic-link and overlapping roots before planning edits.
	var rootAncestors [][]os.FileInfo
	for i, root := range c.Roots {
		absolute, err := filepath.Abs(root)
		if err != nil {
			return Config{}, fmt.Errorf("resolve root %q: %w", root, err)
		}
		info, err := os.Lstat(absolute)
		if err != nil {
			return Config{}, fmt.Errorf("inspect root %q: %w", absolute, err)
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return Config{}, fmt.Errorf("root %q must be a directory, not a symbolic link", absolute)
		}
		if !os.SameFile(info, info) {
			return Config{}, fmt.Errorf("cannot capture root identity: %q", absolute)
		}
		physical, err := physicalRootPath(absolute)
		if err != nil {
			return Config{}, fmt.Errorf("resolve root ancestors %q: %w", absolute, err)
		}
		ancestors, err := inspectRootAncestors(physical)
		if err != nil {
			return Config{}, err
		}
		if !os.SameFile(info, ancestors[0]) {
			return Config{}, fmt.Errorf("root changed while resolving ancestors: %q", absolute)
		}
		// Keep the configured path for full-path keyword filters, but compare
		// ancestor identities so aliases cannot overlap unnoticed, and distinct
		// case-sensitive directories are not confused by lexical path folding.
		c.Roots[i] = absolute
		for j, previous := range rootAncestors {
			if hasDirectoryIdentity(ancestors, previous[0]) || hasDirectoryIdentity(previous, info) {
				return Config{}, fmt.Errorf("ROOT directories overlap: %q and %q", c.Roots[j], absolute)
			}
		}
		rootAncestors = append(rootAncestors, ancestors)
	}
	return c, nil
}

func inspectRootAncestors(path string) ([]os.FileInfo, error) {
	var ancestors []os.FileInfo
	for {
		info, err := os.Lstat(path)
		if err != nil {
			return nil, fmt.Errorf("inspect root ancestor %q: %w", path, err)
		}
		if !info.IsDir() || !os.SameFile(info, info) {
			return nil, fmt.Errorf("cannot capture root ancestor identity: %q", path)
		}
		ancestors = append(ancestors, info)
		parent := filepath.Dir(path)
		if parent == path {
			return ancestors, nil
		}
		path = parent
	}
}

func hasDirectoryIdentity(ancestors []os.FileInfo, directory os.FileInfo) bool {
	for _, ancestor := range ancestors {
		if os.SameFile(ancestor, directory) {
			return true
		}
	}
	return false
}

func within(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func (c Config) IsExcluded(ext string) bool {
	for _, excluded := range c.ExcludeExt {
		if excluded != "" && strings.EqualFold(excluded, ext) {
			return true
		}
	}
	return false
}

func (c Config) ignored(path string) bool {
	path = strings.ToLower(path)
	for _, keyword := range c.IgnoreKeywords {
		if keyword != "" && strings.Contains(path, strings.ToLower(keyword)) {
			return true
		}
	}
	return false
}

func (c Config) includes(path string, info os.FileInfo) bool {
	return info.Mode().IsRegular() && info.Size() >= c.MinSize &&
		!c.IsExcluded(filepath.Ext(path)) && !c.ignored(path)
}
