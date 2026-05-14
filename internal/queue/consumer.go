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

// Returning nil triggers XACK; a non-nil error leaves the message in PEL.
type Handler func(ctx context.Context, payloadID string) error

type Config struct {
	Stream   string
	Group    string
	IDField  string
	LogLabel string
}

type Consumer struct {
	rdb      *redis.Client
	log      *slog.Logger
	cfg      Config
	consumer string // "<hostname>-<pid>"
	handler  Handler
}

func New(rdb *redis.Client, log *slog.Logger, cfg Config, handler Handler) *Consumer {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "worker"
	}
	label := cfg.LogLabel
	if label == "" {
		label = "Queue"
	}
	return &Consumer{
		rdb:      rdb,
		log:      logger.Ctx(log, label),
		cfg:      cfg,
		consumer: fmt.Sprintf("%s-%d", host, os.Getpid()),
		handler:  handler,
	}
}

func (c *Consumer) Run(ctx context.Context) error {
	if err := c.ensureGroup(ctx); err != nil {
		return err
	}

	c.log.Info("consuming",
		slog.String("stream", c.cfg.Stream),
		slog.String("group", c.cfg.Group),
		slog.String("consumer", c.consumer),
	)

	for {
		if err := ctx.Err(); err != nil {
			c.log.Info("stopped")
			return nil
		}

		streams, err := c.rdb.XReadGroup(ctx, &redis.XReadGroupArgs{
			Group:    c.cfg.Group,
			Consumer: c.consumer,
			Streams:  []string{c.cfg.Stream, ">"},
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
	err := c.rdb.XGroupCreateMkStream(ctx, c.cfg.Stream, c.cfg.Group, "$").Err()
	if err == nil {
		c.log.Info("group created", slog.String("group", c.cfg.Group))
		return nil
	}
	// BUSYGROUP = group already exists from a previous boot. Expected, ignore.
	if strings.Contains(err.Error(), "BUSYGROUP") {
		return nil
	}
	return fmt.Errorf("create consumer group: %w", err)
}

func (c *Consumer) dispatch(ctx context.Context, msg redis.XMessage) {
	payloadID, ok := msg.Values[c.cfg.IDField].(string)
	if !ok || payloadID == "" {
		c.log.Error("malformed job, acking and skipping",
			slog.String("messageId", msg.ID),
			slog.String("field", c.cfg.IDField),
			slog.Any("values", msg.Values),
		)
		// Re-delivering a malformed job won't help. Ack so it doesn't sit in PEL forever.
		if err := c.rdb.XAck(ctx, c.cfg.Stream, c.cfg.Group, msg.ID).Err(); err != nil {
			c.log.Error("ack malformed job failed",
				slog.String("messageId", msg.ID),
				slog.Any("err", err),
			)
		}
		return
	}

	if err := c.handler(ctx, payloadID); err != nil {
		c.log.Error("handler failed — leaving in PEL",
			slog.String(c.cfg.IDField, payloadID),
			slog.String("messageId", msg.ID),
			slog.Any("err", err),
		)
		return
	}

	if err := c.rdb.XAck(ctx, c.cfg.Stream, c.cfg.Group, msg.ID).Err(); err != nil {
		c.log.Error("ack failed", slog.String("messageId", msg.ID), slog.Any("err", err))
	}
}