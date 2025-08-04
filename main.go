package main

import (
	log "github.com/sirupsen/logrus"
	"os"
	"path/filepath"
	"strings"
)

func del(path string) error {
	for _, keyword := range TheConfig.IgnoreKeywords {
		if strings.Contains(strings.ToLower(path), strings.ToLower(keyword)) {
			return nil
		}
	}
	if TheConfig.DoRemove {
		if err := os.Remove(path); err != nil {
			return err
		}
		log.Infof("Deleted: %s", path)
	} else {
		log.Infof("Would delete: %s", path)
	}
	return nil
}

func deleteFilesIncluding(root string) (int, error) {
	var deletedCount int

	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		for _, substr := range TheConfig.DeleteFilesIncluding {
			if strings.Contains(info.Name(), substr) {
				err = del(path)
				if err != nil {
					return err
				}
				deletedCount++
				break
			}
		}
		return nil
	})

	return deletedCount, err
}

func main() {
	Configure()
	if len(TheConfig.DeleteFilesIncluding) > 0 {
		for _, root := range TheConfig.Roots {
			log.Infof("Deleting files with %v in %s", TheConfig.DeleteFilesIncluding, root)
			count, err := deleteFilesIncluding(root)
			if err != nil {
				log.Fatalf("Error deleting files: %v", err)
			}
			log.Infof("Deleted %d files", count)
		}
	}
	if TheConfig.Dedup {
		for _, root := range TheConfig.Roots {
			log.Infof("Deduping %s", root)
			dedup(root)
		}
	}
	if TheConfig.EmptyDir {
		for _, root := range TheConfig.Roots {
			log.Infof("Removing empty folders in %s", root)
			_, err := removeEmptyDirs(root)
			if err != nil {
				log.Fatalf("Error removing empty folders: %v", err)
			}
		}
	}

}
