package main

import (
	"github.com/caarlos0/env"
	log "github.com/sirupsen/logrus"
	"strings"
)

type Config struct {
	Roots      []string `env:"ROOT" envDefault:"/dedup"`
	DoRemove   bool     `env:"DO_REMOVE" envDefault:"false"`
	Log        string   `env:"LOG" envDefault:"debug"`
	EmptyDir   bool     `env:"EMPTY_DIR" envDefault:"false"`
	Dedup      bool     `env:"DEDUP" envDefault:"true"`
	MinSize    int64    `env:"MIN_SIZE" envDefault:"0"`
	ExcludeExt []string `env:"EXCLUDE_EXT" envDefault:""`
}

var TheConfig = &Config{}

func (c *Config) IsExcluded(ext string) bool {
	for _, e := range c.ExcludeExt {
		if strings.ToLower(e) == strings.ToLower(ext) {
			return true
		}
	}
	return false
}

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
