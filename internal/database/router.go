package database

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/ilkinnezer1/lighthouse-worker/internal/logger"
)

const (
	JobProvision      = "provision"
	JobTeardown       = "teardown"
	JobSnapshot       = "snapshot"
	JobRestore        = "restore"
	JobSnapshotDelete = "snapshot-delete"

	fieldType       = "type"
	fieldDatabaseID = "databaseId"
	fieldSnapshotID = "snapshotId"
)

type Router struct {
	log *slog.Logger
}

func NewRouter(log *slog.Logger) *Router {
	return &Router{log: logger.Ctx(log, "Database")}
}

func (r *Router) Route(ctx context.Context, fields map[string]string) error {
	jobType := fields[fieldType]
	databaseID := fields[fieldDatabaseID]
	if jobType == "" || databaseID == "" {
		return fmt.Errorf("missing required fields: type=%q databaseId=%q", jobType, databaseID)
	}
	snapshotID := fields[fieldSnapshotID]

	log := r.log.With(slog.String("type", jobType), slog.String("databaseId", databaseID))
	if snapshotID != "" {
		log = log.With(slog.String("snapshotId", snapshotID))
	}

	switch jobType {
	case JobProvision:
		log.Info("provision requested (stub)")
	case JobTeardown:
		log.Info("teardown requested (stub)")
	case JobSnapshot:
		log.Info("snapshot requested (stub)")
	case JobRestore:
		log.Info("restore requested (stub)")
	case JobSnapshotDelete:
		log.Info("snapshot-delete requested (stub)")
	default:
		return fmt.Errorf("unknown job type %q", jobType)
	}
	return nil
}
