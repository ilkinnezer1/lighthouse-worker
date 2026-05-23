package database

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/errdefs"
	"github.com/jackc/pgx/v5"

	"github.com/ilkinnezer1/lighthouse-worker/internal/db"
)

// Nest hard-deletes the managed_databases row before enqueueing teardown, so
// the row is usually gone when we arrive. We tear down by container name
// (derived from databaseId) regardless. Row lookup is informational only.
func (r *Router) teardown(ctx context.Context, databaseID string) error {
	log := r.log.With(slog.String("databaseId", databaseID))

	if row, err := r.q.GetDatabase(ctx, db.MustUUID(databaseID)); err == nil {
		if !row.OwnsContainer {
			log.Info("ownsContainer=false (attach-existing), nothing to teardown")
			return nil
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("get database: %w", err)
	}

	name := containerName(databaseID)
	if err := r.docker.ContainerRemove(ctx, name, container.RemoveOptions{Force: true}); err != nil && !errdefs.IsNotFound(err) {
		if oerr := r.emit(ctx, eventTeardownFailed, databaseID, map[string]any{"error": err.Error()}); oerr != nil {
			log.Warn("outbox teardown_failed event", slog.Any("err", oerr))
		}
		return fmt.Errorf("container remove: %w", err)
	}

	if err := r.docker.VolumeRemove(ctx, name+"-data", true); err != nil && !errdefs.IsNotFound(err) {
		log.Warn("volume remove failed (continuing)", slog.Any("err", err))
	}

	log.Info("database torn down")
	return nil
}
