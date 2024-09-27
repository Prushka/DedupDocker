package main

import (
	log "github.com/sirupsen/logrus"
)

func main() {
	Configure()
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
