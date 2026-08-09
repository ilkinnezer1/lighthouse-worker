package database

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/docker/docker/client"

	"github.com/ilkinnezer1/lighthouse-worker/internal/crypto"
	dbgen "github.com/ilkinnezer1/lighthouse-worker/internal/db/gen"
	"github.com/ilkinnezer1/lighthouse-worker/internal/logger"
	"github.com/ilkinnezer1/lighthouse-worker/internal/minio"
)

const (
	JobProvision      = "provision"
	JobTeardown       = "teardown"
	JobSnapshot       = "snapshot"
	JobRestore        = "restore"
	JobSnapshotDelete = "snapshot_delete"

	fieldType       = "type"
	fieldDatabaseID = "databaseId"
	fieldSnapshotID = "snapshotId"
	fieldStorageKey = "storageKey"
)

type Router struct {
	docker *client.Client
	q      *dbgen.Queries
	crypto *crypto.Cipher
	mc     *minio.Client
	log    *slog.Logger
}

func NewRouter(docker *client.Client, q *dbgen.Queries, cipher *crypto.Cipher, mc *minio.Client, log *slog.Logger) *Router {
	return &Router{
		docker: docker,
		q:      q,
		crypto: cipher,
		mc:     mc,
		log:    logger.Ctx(log, "Database"),
	}
}

func (r *Router) Route(ctx context.Context, fields map[string]string) error {
	jobType := fields[fieldType]
	databaseID := fields[fieldDatabaseID]
	if jobType == "" || databaseID == "" {
		return fmt.Errorf("missing required fields: type=%q databaseId=%q", jobType, databaseID)
	}
	snapshotID := fields[fieldSnapshotID]

	switch jobType {
	case JobProvision:
		return r.provision(ctx, databaseID)
	case JobTeardown:
		return r.teardown(ctx, databaseID)
	case JobSnapshot:
		return r.snapshot(ctx, databaseID, snapshotID)
	case JobRestore:
		return r.restore(ctx, databaseID, snapshotID)
	case JobSnapshotDelete:
		return r.snapshotDelete(ctx, snapshotID, fields[fieldStorageKey])
	default:
		return fmt.Errorf("unknown job type %q", jobType)
	}
}
