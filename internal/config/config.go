package config

import (
	"fmt"

	"github.com/caarlos0/env/v11"
)

const minAPIKeyLength = 16

type Config struct {
	DatabaseURL         string `env:"DATABASE_URL,required"`
	BrainAPIKey         string `env:"BRAIN_API_KEY,required"`
	Port                int    `env:"PORT" envDefault:"8080"`
	EmbeddingProvider   string `env:"EMBEDDING_PROVIDER" envDefault:"openai_compatible"`
	EmbeddingBaseURL    string `env:"EMBEDDING_BASE_URL" envDefault:"https://openrouter.ai/api/v1"`
	EmbeddingModel      string `env:"EMBEDDING_MODEL" envDefault:"openai/text-embedding-3-small"`
	EmbeddingDimensions int    `env:"EMBEDDING_DIMENSIONS" envDefault:"1536"`
	EmbeddingAPIKey     string `env:"EMBEDDING_API_KEY"`
	LogLevel            string `env:"LOG_LEVEL" envDefault:"info"`
}

func Load() (Config, error) {
	var cfg Config
	if err := env.Parse(&cfg); err != nil {
		return Config{}, err
	}
	if len(cfg.BrainAPIKey) < minAPIKeyLength {
		return Config{}, fmt.Errorf("BRAIN_API_KEY must be at least %d characters", minAPIKeyLength)
	}
	switch cfg.EmbeddingProvider {
	case "openai_compatible", "none":
	default:
		return Config{}, fmt.Errorf("EMBEDDING_PROVIDER must be \"openai_compatible\" or \"none\", got %q", cfg.EmbeddingProvider)
	}
	if cfg.EmbeddingDimensions < 1 {
		return Config{}, fmt.Errorf("EMBEDDING_DIMENSIONS must be positive, got %d", cfg.EmbeddingDimensions)
	}
	return cfg, nil
}
