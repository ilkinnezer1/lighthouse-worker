package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ilkinnezer1/lighthouse-worker/internal/config"
	"github.com/ilkinnezer1/lighthouse-worker/internal/db"
	"github.com/ilkinnezer1/lighthouse-worker/internal/httpx"
	"github.com/ilkinnezer1/lighthouse-worker/internal/logger"
	"github.com/ilkinnezer1/lighthouse-worker/internal/redisx"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "config load failed: %v\n", err)
		os.Exit(1)
	}

	root := slog.New(logger.New(os.Stdout, logger.Options{
		Level:  logger.ParseLevel(cfg.LogLevel),
		Prefix: "Worker",
		Color:  true,
	}))
	slog.SetDefault(root)
	boot := logger.Ctx(root, "Bootstrap")

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	connectCtx, connectCancel := context.WithTimeout(ctx, 10*time.Second)
	defer connectCancel()

	pool, err := db.Connect(connectCtx, cfg.DatabaseURL)
	if err != nil {
		boot.Error("postgres connect failed", slog.Any("err", err))
		os.Exit(1)
	}
	defer pool.Close()
	boot.Info("postgres connected")

	rdb, err := redisx.Connect(connectCtx, cfg.RedisURL)
	if err != nil {
		boot.Error("redis connect failed", slog.Any("err", err))
		os.Exit(1)
	}
	defer func() { _ = rdb.Close() }()
	boot.Info("redis connected")

	srv := httpx.New(cfg.HTTPAddr, root)
	httpDone := make(chan error, 1)
	go func() { httpDone <- srv.Run(ctx) }()

	boot.Info("ready")

	select {
	case <-ctx.Done():
		boot.Info("shutting down")
		if err := <-httpDone; err != nil {
			boot.Error("http shutdown", slog.Any("err", err))
		}
	case err := <-httpDone:
		if err != nil {
			boot.Error("http server failed", slog.Any("err", err))
			os.Exit(1)
		}
	}
}