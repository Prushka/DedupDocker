//go:build !windows

package app

import "path/filepath"

func physicalRootPath(path string) (string, error) { return filepath.EvalSymlinks(path) }
