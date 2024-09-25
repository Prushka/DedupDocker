package main

import log "github.com/sirupsen/logrus"

func main() {
	log.Info("Deduplication starting...")
	Configure()
	dedup(TheConfig.Root)
}
