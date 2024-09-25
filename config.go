package main

import (
	"github.com/caarlos0/env"
	log "github.com/sirupsen/logrus"
)

type Config struct {
	Root     string `env:"ROOT" envDefault:"/dedup"`
	DoRemove bool   `env:"DO_REMOVE" envDefault:"false"`
	Log      string `env:"LOG" envDefault:"debug"`
}

var TheConfig = &Config{}

func Configure() {
	err := env.Parse(TheConfig)
	if err != nil {
		log.Fatalf("error parsing config: %v", err)
	}
	switch TheConfig.Log {
	case "trace":
		log.SetLevel(log.TraceLevel)
	case "debug":
		log.SetLevel(log.DebugLevel)
	case "info":
		log.SetLevel(log.InfoLevel)
	case "warn":
		log.SetLevel(log.WarnLevel)
	case "error":
		log.SetLevel(log.ErrorLevel)
	case "fatal":
		log.SetLevel(log.FatalLevel)
	case "panic":
		log.SetLevel(log.PanicLevel)
	default:
		log.SetLevel(log.DebugLevel)
	}
}
