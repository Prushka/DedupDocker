package main

import (
	"github.com/caarlos0/env"
	log "github.com/sirupsen/logrus"
)

type Config struct {
	Root     string `env:"Root" envDefault:"."`
	DoRemove bool   `env:"DoRemove" envDefault:"false"`
}

var TheConfig = &Config{}

func Configure() {
	err := env.Parse(TheConfig)
	if err != nil {
		log.Fatalf("error parsing config: %v", err)
	}
}
