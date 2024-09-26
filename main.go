package main

import (
	log "github.com/sirupsen/logrus"
)

func main() {
	Configure()
	if TheConfig.EmptyDirOnly {
		log.Info("Removing empty folders only")
		_, err := removeEmptyDirs(TheConfig.Root)
		if err != nil {
			log.Fatalf("Error removing empty directories: %v", err)
		}
	} else {
		log.Info("Removing duplicates")
		dedup(TheConfig.Root)
	}
}
