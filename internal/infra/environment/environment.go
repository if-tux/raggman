package environment

import (
	"errors"
	"fmt"
	"io/fs"
	"strings"

	"github.com/spf13/viper"
)

// Environment is Raggman's configuration.
//
//   - LLMBaseURL, LLMAPIKey, LLMModel: Answer generation, on any
//     OpenAI-compatible endpoint (ADR-0004).
//   - LLMThinking: lets local thinking models reason before answering: a
//     slower first token, but far fewer invented facts. Only applies to local
//     Ollama (no LLMAPIKey); cloud providers keep their own defaults.
//   - OllamaHost: embeddings always run on local Ollama (ADR-0001, ADR-0004).
//   - TracesExporter: "otlp" sends spans to OTLPEndpoint (Jaeger); "none"
//     keeps them in process.
type Environment struct {
	Env            string  `mapstructure:"APP_ENV"`
	LogLevel       string  `mapstructure:"LOG_LEVEL"`
	HTTPAddr       string  `mapstructure:"HTTP_ADDR"`
	DatabaseURL    string  `mapstructure:"DATABASE_URL"`
	LLMBaseURL     string  `mapstructure:"LLM_BASE_URL"`
	LLMAPIKey      string  `mapstructure:"LLM_API_KEY"`
	LLMModel       string  `mapstructure:"LLM_MODEL"`
	OllamaHost     string  `mapstructure:"OLLAMA_HOST"`
	OTLPEndpoint   string  `mapstructure:"OTEL_EXPORTER_OTLP_ENDPOINT"`
	TracesExporter string  `mapstructure:"OTEL_TRACES_EXPORTER"`
	MinSimilarity  float64 `mapstructure:"MIN_SIMILARITY"`
	TopK           int     `mapstructure:"TOP_K"`
	LLMThinking    bool    `mapstructure:"LLM_THINKING"`
}

// EmbeddingModel embeds every Chunk in the Corpus. It is deliberately not
// configurable: mixing models breaks similarity, and the schema's vector(1024)
// is sized for it. Changing it means a migration and re-ingesting everything
// (ADR-0001). docker-compose.yml's ollama-init pulls the same name.
const EmbeddingModel = "qwen3-embedding:0.6b"

// EmbeddingDims is EmbeddingModel's vector size, matching chunks.embedding.
const EmbeddingDims = 1024

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
	"LLM_THINKING":                true,
	"OLLAMA_HOST":                 "http://localhost:11434",
	"OTEL_EXPORTER_OTLP_ENDPOINT": "http://localhost:4317",
	"OTEL_TRACES_EXPORTER":        "otlp",
	"MIN_SIMILARITY":              0.3,
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

// EmbeddingsBaseURL is the OpenAI-compatible endpoint of the local Ollama.
func (e *Environment) EmbeddingsBaseURL() string {
	return strings.TrimSuffix(e.OllamaHost, "/") + "/v1"
}
