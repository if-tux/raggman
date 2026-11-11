package environment

import (
	"errors"
	"fmt"
	"io/fs"

	"github.com/spf13/viper"
)

type Environment struct {
	Env         string `mapstructure:"APP_ENV"`
	LogLevel    string `mapstructure:"LOG_LEVEL"`
	HTTPAddr    string `mapstructure:"HTTP_ADDR"`
	DatabaseURL string `mapstructure:"DATABASE_URL"`

	// Answer generation: any OpenAI-compatible endpoint (ADR-0004).
	LLMBaseURL string `mapstructure:"LLM_BASE_URL"`
	LLMAPIKey  string `mapstructure:"LLM_API_KEY"`
	LLMModel   string `mapstructure:"LLM_MODEL"`

	// Embeddings always run on local Ollama (ADR-0001, ADR-0004).
	OllamaHost     string `mapstructure:"OLLAMA_HOST"`
	EmbeddingModel string `mapstructure:"EMBEDDING_MODEL"`

	OTLPEndpoint string `mapstructure:"OTEL_EXPORTER_OTLP_ENDPOINT"`

	MinSimilarity float64 `mapstructure:"MIN_SIMILARITY"`
	TopK          int     `mapstructure:"TOP_K"`
}

// defaults also registers every key with viper: Unmarshal ignores environment
// variables for keys viper doesn't know about, so every key needs an entry.
var defaults = map[string]any{
	"APP_ENV":                     "development",
	"LOG_LEVEL":                   "INFO",
	"HTTP_ADDR":                   ":8080",
	"DATABASE_URL":                "",
	"LLM_BASE_URL":                "http://localhost:11434/v1",
	"LLM_API_KEY":                 "",
	"LLM_MODEL":                   "qwen3:8b",
	"OLLAMA_HOST":                 "http://localhost:11434",
	"EMBEDDING_MODEL":             "qwen3-embedding:0.6b",
	"OTEL_EXPORTER_OTLP_ENDPOINT": "http://localhost:4317",
	"MIN_SIMILARITY":              0.5,
	"TOP_K":                       5,
}

// Load reads configuration from environment variables, optionally seeded by a
// .env file in the working directory (absent inside containers).
func Load() (*Environment, error) {
	v := viper.New()
	for key, value := range defaults {
		v.SetDefault(key, value)
	}
	v.SetConfigFile(".env")
	v.SetConfigType("env")
	v.AutomaticEnv()

	if err := v.ReadInConfig(); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("reading .env: %w", err)
	}

	var env Environment
	if err := v.Unmarshal(&env); err != nil {
		return nil, fmt.Errorf("decoding configuration: %w", err)
	}

	if env.DatabaseURL == "" {
		return nil, errors.New("missing required configuration: DATABASE_URL")
	}

	return &env, nil
}
