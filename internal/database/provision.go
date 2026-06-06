package database

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ilkinnezer1/lighthouse-worker/internal/db"
	dbgen "github.com/ilkinnezer1/lighthouse-worker/internal/db/gen"
)

func (r *Router) provision(ctx context.Context, databaseID string) error {
	id := db.MustUUID(databaseID)
	row, err := r.q.GetDatabase(ctx, id)
	if err != nil {
		return fmt.Errorf("get database: %w", err)
	}

	log := r.log.With(slog.String("databaseId", databaseID), slog.String("name", row.Name))

	if row.Status != dbgen.DatabaseStatusPROVISIONING {
		log.Info("not in PROVISIONING state, skipping", slog.String("status", string(row.Status)))
		return nil
	}
	if !row.OwnsContainer {
		log.Info("ownsContainer=false (attach-existing), skipping provision")
		return nil
	}
	if row.Type != dbgen.DatabaseTypePOSTGRES {
		return r.markProvisionFailed(ctx, id, databaseID, fmt.Errorf("only POSTGRES is supported, got %s", row.Type))
	}

	host, port, connStr, err := r.provisionPostgres(ctx, row, log)
	if err != nil {
		return r.markProvisionFailed(ctx, id, databaseID, err)
	}

	enc, err := r.crypto.Encrypt(connStr)
	if err != nil {
		return r.markProvisionFailed(ctx, id, databaseID, fmt.Errorf("encrypt conn string: %w", err))
	}

	if err := r.q.MarkDatabaseReady(ctx, dbgen.MarkDatabaseReadyParams{
		ID:            id,
		Host:          &host,
		Port:          &port,
		ConnSecretEnc: &enc,
	}); err != nil {
		return fmt.Errorf("mark ready: %w", err)
	}

	if row.RunMigrate {
		if err := r.runMigrate(ctx, row, connStr, log); err != nil {
			log.Warn("runMigrate skipped/failed (continuing)", slog.Any("err", err))
		}
	}

	if err := r.emit(ctx, eventProvisioned, databaseID, nil); err != nil {
		log.Warn("outbox provisioned event failed", slog.Any("err", err))
	}
	log.Info("database provisioned", slog.String("host", host), slog.Int("port", int(port)))
	return nil
}

func (r *Router) markProvisionFailed(ctx context.Context, id pgtype.UUID, databaseID string, runErr error) error {
	summary := runErr.Error()
	if err := r.q.MarkDatabaseFailed(ctx, dbgen.MarkDatabaseFailedParams{ID: id, ErrorSummary: &summary}); err != nil {
		r.log.Error("mark failed write", slog.Any("err", err))
	}
	r.log.Error("provision failed", slog.String("databaseId", databaseID), slog.Any("err", runErr))
	if oerr := r.emit(ctx, eventProvisionFailed, databaseID, map[string]any{"error": summary}); oerr != nil {
		r.log.Warn("outbox provision_failed event", slog.Any("err", oerr))
	}
	return runErr
}

func containerName(databaseID string) string {
	return "db-" + databaseID
}
