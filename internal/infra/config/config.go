package config

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/spf13/viper"
)

type Config struct {
	Env         string `mapstructure:"APP_ENV"`
	LogLevel    string `mapstructure:"LOG_LEVEL"`
	DatabaseURL string `mapstructure:"DATABASE_URL"`
	OllamaHost  string `mapstructure:"OLLAMA_HOST"`
	QwenModel   string `mapstructure:"QWEN_MODEL"`
	JaegerHost  string `mapstructure:"JAEGER_HOST"`
}

func Load() *Config {
	viper.SetConfigFile(".env")
	viper.SetConfigType("env")
	viper.AddConfigPath(".")
	viper.AutomaticEnv()

	err := viper.ReadInConfig()

	if err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			panic(fmt.Errorf("fatal error reading config file: %w", err))
		}
	}

	var cfg Config

	if err := viper.Unmarshal(&cfg); err != nil {
		slog.Error("Failed to unmarshal configuration", "error", err)
		os.Exit(1)
	}

	if cfg.DatabaseURL == "" {
		slog.Error("Critical configuration missing. Terminating application.", "missing_env", "DATABASE_URL")
		os.Exit(1)
	}

	return &cfg
}
