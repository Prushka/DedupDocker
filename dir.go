package main

import (
	"fmt"
	log "github.com/sirupsen/logrus"
	"os"
	"path/filepath"
)

func removeDir(path string) {
	fileInfo, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			log.Fatalf("The path %s does not exist", path)
		} else {
			log.Fatalf("Error accessing the path %s: %v", path, err)
		}
	}
	if !fileInfo.IsDir() {
		log.Fatalf("The path %s is not a directory", path)
	}

	if TheConfig.DoRemove {
		if err := os.Remove(path); err != nil {
			log.Fatalf("Failed to remove directory %s: %s", path, err)
		}
		log.Infof("Deleted folder: %s", path)
	} else {
		log.Infof("Would delete folder: %s", path)
	}
}

func removeEmptyDirs(dir string) (bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false, fmt.Errorf("failed to read directory %q: %w", dir, err)
	}

	isEmpty := true
	for _, entry := range entries {
		fullPath := filepath.Join(dir, entry.Name())

		if entry.IsDir() {
			empty, err := removeEmptyDirs(fullPath)
			if err != nil {
				return false, err
			}
			if empty {
				removeDir(fullPath)
			} else {
				isEmpty = false
			}
		} else {
			if entry.Name() == ".DS_Store" {
				fInfo, err := entry.Info()
				if err != nil {
					return false, fmt.Errorf("failed to get file info for %q: %w", fullPath, err)
				}
				size := fInfo.Size()
				remove(&DupFile{Path: fullPath, Size: size})
			} else {
				isEmpty = false
			}
		}
	}

	return isEmpty, nil
}
