package main

import (
	log "github.com/sirupsen/logrus"
)

func main() {
	Configure()
	if TheConfig.Dedup {
		log.Info("Removing duplicates")
		dedup(TheConfig.Root)
	}
	if TheConfig.EmptyDir {
		log.Info("Removing empty folders")
		_, err := removeEmptyDirs(TheConfig.Root)
		if err != nil {
			log.Fatalf("Error removing empty directories: %v", err)
		}
	}

}
