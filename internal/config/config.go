package config

import (
	"errors"
	"fmt"
	"os"

	"github.com/joho/godotenv"
)

type Config struct {
	DatabaseURL         string
	RedisURL            string
	MasterEncryptionKey string
	LogLevel            string
	HTTPAddr            string
	WorkDir             string
	Mode                string
	BaseDomain          string
}

func Load() (*Config, error) {
	_ = godotenv.Load()

	cfg := &Config{
		DatabaseURL:         os.Getenv("DATABASE_URL"),
		RedisURL:            os.Getenv("REDIS_URL"),
		MasterEncryptionKey: os.Getenv("MASTER_ENCRYPTION_KEY"),
		LogLevel:            getEnv("LOG_LEVEL", "info"),
		HTTPAddr:            getEnv("HTTP_ADDR", ":8081"),
		WorkDir:             getEnv("WORK_DIR", "./data/deployments"),
		Mode:                getEnv("MODE", "local"),
		BaseDomain:          getEnv("BASE_DOMAIN", "localhost"),
	}

	var missing []string
	if cfg.DatabaseURL == "" {
		missing = append(missing, "DATABASE_URL")
	}
	if cfg.RedisURL == "" {
		missing = append(missing, "REDIS_URL")
	}
	if cfg.MasterEncryptionKey == "" {
		missing = append(missing, "MASTER_ENCRYPTION_KEY")
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("missing required env vars: %v", missing)
	}

	if cfg.MasterEncryptionKey == "replace-with-openssl-rand-base64-32" {
		return nil, errors.New("MASTER_ENCRYPTION_KEY is still the placeholder; generate one with `openssl rand -base64 32`")
	}

	if cfg.Mode != "local" && cfg.Mode != "server" {
		return nil, fmt.Errorf("MODE must be 'local' or 'server', got %q", cfg.Mode)
	}

	return cfg, nil
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
