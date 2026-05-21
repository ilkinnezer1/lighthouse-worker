package cancelbus

import (
	"context"
	"log/slog"

	"github.com/ilkinnezer1/lighthouse-worker/internal/logger"
	"github.com/redis/go-redis/v9"
)

const channelPrefix = "cancel:deploy"

type Listener struct {
	rdb *redis.Client
	log *slog.Logger
}

func New(rdb *redis.Client, log *slog.Logger) *Listener {
	return &Listener{
		rdb: rdb,
		log: logger.Ctx(log, "CancelBus"),
	}
}

// Listen subscribes to cancel:deploy:<id> for the lifetime of ctx. If a message arrives, fires onCancel exactly once and returns. If ctx ends first (deployment
// finished or worker shutting down), returns without firing.
func (l *Listener) Listen(ctx context.Context, deploymentID string, onCancel func()) {
	channel := channelPrefix + deploymentID
	sub := l.rdb.Subscribe(ctx, channel)
	defer func() { _ = sub.Close() }()

	log := l.log.With(slog.String("deploymentId", deploymentID))

	// Receive blocks until the SUBSCRIBE ack lands, that prevents a race where a
	// publication slips through before the channel is actually registered.

	if _, err := sub.Receive(ctx); err != nil {
		if ctx.Err() != nil {
			log.Warn("subscription failed", slog.Any("err", err))
		}
		return
	}

	select {
	case <-ctx.Done():
		return

	case msg, ok := <-sub.Channel():
		if !ok {
			return
		}
		log.Info("received cancel message", slog.String("payload", msg.Payload))
		onCancel()
	}

}
