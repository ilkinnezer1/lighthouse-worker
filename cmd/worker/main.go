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
	"github.com/ilkinnezer1/lighthouse-worker/internal/cancelbus"
	"golang.org/x/sync/errgroup"

	"github.com/ilkinnezer1/lighthouse-worker/internal/config"
	"github.com/ilkinnezer1/lighthouse-worker/internal/crypto"
	"github.com/ilkinnezer1/lighthouse-worker/internal/database"
	"github.com/ilkinnezer1/lighthouse-worker/internal/db"
	dbgen "github.com/ilkinnezer1/lighthouse-worker/internal/db/gen"
	"github.com/ilkinnezer1/lighthouse-worker/internal/httpx"
	"github.com/ilkinnezer1/lighthouse-worker/internal/logger"
	"github.com/ilkinnezer1/lighthouse-worker/internal/logstream"
	"github.com/ilkinnezer1/lighthouse-worker/internal/minio"
	"github.com/ilkinnezer1/lighthouse-worker/internal/pipeline"
	"github.com/ilkinnezer1/lighthouse-worker/internal/queue"
	"github.com/ilkinnezer1/lighthouse-worker/internal/redisx"
	"github.com/ilkinnezer1/lighthouse-worker/internal/routing"
	"github.com/ilkinnezer1/lighthouse-worker/internal/stages"
	"github.com/ilkinnezer1/lighthouse-worker/internal/teardown"
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
	mode := routing.Mode(cfg.Mode)

	cipher, err := crypto.New(cfg.MasterEncryptionKey)
	if err != nil {
		boot.Error("crypto init failed", slog.Any("err", err))
		os.Exit(1)
	}
	stageList := []pipeline.Stage{
		stages.NewClone(logs),
		stages.NewBuild(logs),
		stages.NewRun(docker, logs, q, cipher, mode),
		stages.NewHealthCheck(docker, q, logs),
		stages.NewRetire(docker, q, logs),
	}
	cancelListener := cancelbus.New(rdb, root)
	pipe := pipeline.NewPipeline(q, root, stageList, cancelListener)
	runner := pipeline.NewRunner(q, pipe, root, cfg.WorkDir, mode, cfg.BaseDomain)

	srv := httpx.New(cfg.HTTPAddr, root)

	buildConsumer := queue.New(rdb, root, queue.Config{
		Stream:   "jobs:build",
		Group:    "worker",
		IDField:  "deploymentId",
		LogLabel: "Queue.build",
	}, runner.Run)

	teardownRunner := teardown.New(docker, q, root, cfg.WorkDir)
	teardownConsumer := queue.New(rdb, root, queue.Config{
		Stream:   "jobs:teardown",
		Group:    "worker",
		IDField:  "environmentId",
		LogLabel: "Queue.teardown",
	}, teardownRunner.Run)

	mc, err := minio.New(cfg.MinIOEndpoint, cfg.MinIOAccessKey, cfg.MinIOSecretKey, cfg.MinIOBucket, false, root)
	if err != nil {
		boot.Error("minio init failed", slog.Any("err", err))
		os.Exit(1)
	}
	databaseRouter := database.NewRouter(docker, q, cipher, mc, root)
	databaseConsumer := queue.NewFields(rdb, root, queue.Config{
		Stream:   "jobs:database",
		Group:    "worker",
		LogLabel: "Queue.database",
	}, databaseRouter.Route)

	boot.Info("ready")

	g, gCtx := errgroup.WithContext(ctx)
	g.Go(func() error { return srv.Run(gCtx) })
	g.Go(func() error { return buildConsumer.Run(gCtx) })
	g.Go(func() error { return teardownConsumer.Run(gCtx) })
	g.Go(func() error { return databaseConsumer.Run(gCtx) })

	if err := g.Wait(); err != nil {
		boot.Error("stopped with error", slog.Any("err", err))
		os.Exit(1)
	}
	boot.Info("shutdown complete")
}
