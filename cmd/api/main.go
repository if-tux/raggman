package main

import (
	"github.com/BrunoPereiradosSantos/raggman/internal/infra/config"
	"github.com/BrunoPereiradosSantos/raggman/internal/infra/logger"
)

func main() {
	cfg := config.Load()
	logger.Init("development", cfg.LogLevel)

}
