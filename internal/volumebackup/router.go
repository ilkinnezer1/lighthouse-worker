// Package volumebackup tars a persistent volume into MinIO and restores it
// back. It exists because volumes are the only place user data lives that no
// other backup covers: managed-database snapshots are Postgres-only, and
// teardown deletes an environment's volumes outright.
package volumebackup

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/docker/docker/client"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ilkinnezer1/lighthouse-worker/internal/db"
	dbgen "github.com/ilkinnezer1/lighthouse-worker/internal/db/gen"
	"github.com/ilkinnezer1/lighthouse-worker/internal/logger"
	"github.com/ilkinnezer1/lighthouse-worker/internal/minio"
)

const (
	JobBackup  = "volume_backup"
	JobRestore = "volume_restore"
	JobDelete  = "volume_backup_delete"

	fieldType     = "type"
	fieldBackupID = "backupId"
	// Delete carries the key rather than re-reading the row: the API drops the
	// row as soon as it enqueues, so a lookup here would race with it.
	fieldStorageKey = "storageKey"

	// Image used as the tar host. It only needs busybox tar and never touches
	// the network, so the smallest maintained base is the right pick.
	helperImage = "alpine:3.20"
	mountPath   = "/volume"
)

type Router struct {
	docker *client.Client
	q      *dbgen.Queries
	mc     *minio.Client
	log    *slog.Logger
}

func NewRouter(docker *client.Client, q *dbgen.Queries, mc *minio.Client, log *slog.Logger) *Router {
	return &Router{
		docker: docker,
		q:      q,
		mc:     mc,
		log:    logger.Ctx(log, "VolumeBackup"),
	}
}

func (r *Router) Route(ctx context.Context, fields map[string]string) error {
	jobType := fields[fieldType]
	backupID := fields[fieldBackupID]
	if jobType == "" || backupID == "" {
		return fmt.Errorf("missing required fields: type=%q backupId=%q", jobType, backupID)
	}

	switch jobType {
	case JobBackup:
		return r.backup(ctx, backupID)
	case JobRestore:
		return r.restore(ctx, backupID)
	case JobDelete:
		return r.deleteObject(ctx, fields[fieldStorageKey])
	default:
		return fmt.Errorf("unknown job type %q", jobType)
	}
}

func (r *Router) markFailed(ctx context.Context, id pgtype.UUID, runErr error) error {
	summary := runErr.Error()
	if err := r.q.MarkVolumeBackupFailed(ctx, dbgen.MarkVolumeBackupFailedParams{
		ID:           id,
		ErrorSummary: &summary,
	}); err != nil {
		r.log.Error("mark volume backup failed", slog.Any("err", err))
	}
	return runErr
}

func (r *Router) loadBackup(ctx context.Context, backupID string) (dbgen.VolumeBackup, pgtype.UUID, error) {
	id := db.MustUUID(backupID)
	row, err := r.q.GetVolumeBackup(ctx, id)
	if err != nil {
		return dbgen.VolumeBackup{}, id, fmt.Errorf("get volume backup: %w", err)
	}
	if row.VolumeName == "" {
		return dbgen.VolumeBackup{}, id, errors.New("volume backup has no volume name")
	}
	return row, id, nil
}
