package pipeline

import (
	"context"
	"log/slog"
	"time"

	dbgen "github.com/ilkinnezer1/lighthouse-worker/internal/db/gen"
)

const heartbeatInterval = 30 * time.Second

// The reaper uses last_heartbeat_at to fail orphaned in-flight deployments.
func runHeartbeat(ctx context.Context, q *dbgen.Queries, deploymentID string, log *slog.Logger) {
	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := q.UpdateHeartbeat(ctx, depUUID(deploymentID)); err != nil {
				log.Warn("heartbeat failed", slog.Any("err", err))
			}
		}
	}
}
