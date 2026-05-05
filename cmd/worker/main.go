package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/docker/docker/client"
	"golang.org/x/sync/errgroup"

	"github.com/ilkinnezer1/lighthouse-worker/internal/config"
	"github.com/ilkinnezer1/lighthouse-worker/internal/db"
	dbgen "github.com/ilkinnezer1/lighthouse-worker/internal/db/gen"
	"github.com/ilkinnezer1/lighthouse-worker/internal/httpx"
	"github.com/ilkinnezer1/lighthouse-worker/internal/logger"
	"github.com/ilkinnezer1/lighthouse-worker/internal/logstream"
	"github.com/ilkinnezer1/lighthouse-worker/internal/pipeline"
	"github.com/ilkinnezer1/lighthouse-worker/internal/queue"
	"github.com/ilkinnezer1/lighthouse-worker/internal/redisx"
	"github.com/ilkinnezer1/lighthouse-worker/internal/stages"
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

	docker, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		boot.Error("docker connect failed", slog.Any("err", err))
		os.Exit(1)
	}
	defer func() { _ = docker.Close() }()
	boot.Info("docker connected")

	q := dbgen.New(pool)
	logs := logstream.New(rdb)
	stageList := []pipeline.Stage{
		stages.NewClone(logs),
		stages.NewBuild(logs),
		stages.NewRun(docker, logs, q),
	}
	pipe := pipeline.NewPipeline(q, root, stageList)
	runner := pipeline.NewRunner(q, pipe, root, cfg.WorkDir)

	srv := httpx.New(cfg.HTTPAddr, root)
	consumer := queue.New(rdb, root, runner.Run)

	boot.Info("ready")

	g, gCtx := errgroup.WithContext(ctx)
	g.Go(func() error { return srv.Run(gCtx) })
	g.Go(func() error { return consumer.Run(gCtx) })

	if err := g.Wait(); err != nil {
		boot.Error("stopped with error", slog.Any("err", err))
		os.Exit(1)
	}
	boot.Info("shutdown complete")
}