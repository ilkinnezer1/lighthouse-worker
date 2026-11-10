package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/joho/godotenv"

	"github.com/ilkinnezer1/lighthouse-worker/internal/sweeper"
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
	MinIOEndpoint       string
	MinIOAccessKey      string
	MinIOSecretKey      string
	MinIOBucket         string
	SweepInterval       time.Duration
	SweepRetention      time.Duration
	DBImportMaxBytes    int64
}

func Load() (*Config, error) {
	_ = godotenv.Load()

	var err error
	cfg := &Config{
		DatabaseURL:         os.Getenv("DATABASE_URL"),
		RedisURL:            os.Getenv("REDIS_URL"),
		MasterEncryptionKey: os.Getenv("MASTER_ENCRYPTION_KEY"),
		LogLevel:            getEnv("LOG_LEVEL", "info"),
		HTTPAddr:            getEnv("HTTP_ADDR", ":8081"),
		WorkDir:             getEnv("WORK_DIR", "./data/deployments"),
		Mode:                getEnv("MODE", "local"),
		BaseDomain:          getEnv("BASE_DOMAIN", "localhost"),
		MinIOEndpoint:       getEnv("MINIO_ENDPOINT", "localhost:9000"),
		MinIOAccessKey:      os.Getenv("MINIO_ACCESS_KEY"),
		MinIOSecretKey:      os.Getenv("MINIO_SECRET_KEY"),
		MinIOBucket:         getEnv("MINIO_BUCKET", "snapshots"),
	}

	if cfg.SweepInterval, err = getDuration("SWEEP_INTERVAL", sweeper.DefaultInterval); err != nil {
		return nil, err
	}
	if cfg.SweepRetention, err = getDuration("SWEEP_RETENTION", sweeper.DefaultRetention); err != nil {
		return nil, err
	}

	if cfg.DBImportMaxBytes, err = getPositiveInt("DB_IMPORT_MAX_BYTES", defaultDBImportMaxBytes); err != nil {
		return nil, err
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

	abs, err := filepath.Abs(cfg.WorkDir)
	if err != nil {
		return nil, fmt.Errorf("resolve WORK_DIR: %w", err)
	}
	cfg.WorkDir = abs

	return cfg, nil
}

const defaultDBImportMaxBytes = 5 << 30

func getPositiveInt(key string, fallback int64) (int64, error) {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback, nil
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer, got %q", key, raw)
	}
	return n, nil
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getDuration(key string, fallback time.Duration) (time.Duration, error) {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s must be a duration like 168h: %w", key, err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("%s must be positive, got %s", key, raw)
	}
	return d, nil
}
