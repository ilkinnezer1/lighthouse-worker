package queue

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/ilkinnezer1/lighthouse-worker/internal/logger"
)

const (
	streamName        = "jobs:build"
	groupName         = "worker"
	fieldDeploymentID = "deploymentId"
)

// Handler is invoked for each delivered job. Returning nil triggers XACK.
type Handler func(ctx context.Context, deploymentID string) error

type Consumer struct {
	rdb      *redis.Client
	log      *slog.Logger
	stream   string // "jobs:build"
	group    string // "worker"
	consumer string // "<hostname>-<pid>"
	handler  Handler
}

func New(rdb *redis.Client, log *slog.Logger, handler Handler) *Consumer {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "worker"
	}
	return &Consumer{
		rdb:      rdb,
		log:      logger.Ctx(log, "Queue"),
		stream:   streamName,
		group:    groupName,
		consumer: fmt.Sprintf("%s-%d", host, os.Getpid()),
		handler:  handler,
	}
}

// Run creates the consumer group on first boot (idempotent), then loops on
func (c *Consumer) Run(ctx context.Context) error {
	if err := c.ensureGroup(ctx); err != nil {
		return err
	}

	c.log.Info("consuming",
		slog.String("stream", c.stream),
		slog.String("group", c.group),
		slog.String("consumer", c.consumer),
	)

	for {
		if err := ctx.Err(); err != nil {
			c.log.Info("stopped")
			return nil
		}

		streams, err := c.rdb.XReadGroup(ctx, &redis.XReadGroupArgs{
			Group:    c.group,
			Consumer: c.consumer,
			Streams:  []string{c.stream, ">"},
			Count:    1,
			Block:    5 * time.Second,
		}).Result()

		if errors.Is(err, redis.Nil) {
			continue // BLOCK timed out, no new messages
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil
		}
		if err != nil {
			c.log.Error("xreadgroup failed", slog.Any("err", err))
			// Avoid busy-looping on persistent errors
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(time.Second):
				continue
			}
		}

		for _, s := range streams {
			for _, msg := range s.Messages {
				c.dispatch(ctx, msg)
			}
		}
	}
}

func (c *Consumer) ensureGroup(ctx context.Context) error {
	err := c.rdb.XGroupCreateMkStream(ctx, c.stream, c.group, "$").Err()
	if err == nil {
		c.log.Info("group created", slog.String("group", c.group))
		return nil
	}
	// BUSYGROUP = group already exists from a previous boot. Expected, ignore.
	if strings.Contains(err.Error(), "BUSYGROUP") {
		return nil
	}
	return fmt.Errorf("create consumer group: %w", err)
}

func (c *Consumer) dispatch(ctx context.Context, msg redis.XMessage) {
	deploymentID, ok := msg.Values[fieldDeploymentID].(string)
	if !ok || deploymentID == "" {
		c.log.Error("malformed job, acking and skipping",
			slog.String("messageId", msg.ID),
			slog.Any("values", msg.Values),
		)
		// Re-delivering a malformed job won't help. Ack so it doesn't sit in PEL forever.
		if err := c.rdb.XAck(ctx, c.stream, c.group, msg.ID).Err(); err != nil {
			c.log.Error("ack malformed job failed",
				slog.String("messageId", msg.ID),
				slog.Any("err", err),
			)
		}
		return
	}

	if err := c.handler(ctx, deploymentID); err != nil {
		c.log.Error("handler failed — leaving in PEL",
			slog.String("deploymentId", deploymentID),
			slog.String("messageId", msg.ID),
			slog.Any("err", err),
		)
		return
	}

	if err := c.rdb.XAck(ctx, c.stream, c.group, msg.ID).Err(); err != nil {
		c.log.Error("ack failed", slog.String("messageId", msg.ID), slog.Any("err", err))
	}
}
