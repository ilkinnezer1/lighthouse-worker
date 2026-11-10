package database

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ilkinnezer1/lighthouse-worker/internal/db"
	dbgen "github.com/ilkinnezer1/lighthouse-worker/internal/db/gen"
)

func (r *Router) snapshot(ctx context.Context, databaseID, snapshotID string) error {
	if snapshotID == "" {
		return errors.New("snapshot job missing snapshotId")
	}
	snapID := db.MustUUID(snapshotID)
	dbID := db.MustUUID(databaseID)

	dbRow, err := r.q.GetDatabase(ctx, dbID)
	if err != nil {
		return r.markSnapshotFailed(ctx, snapID, snapshotID, fmt.Errorf("get database: %w", err))
	}
	if dbRow.Status != dbgen.DatabaseStatusREADY {
		return r.markSnapshotFailed(ctx, snapID, snapshotID, fmt.Errorf("database not READY (got %s)", dbRow.Status))
	}

	log := r.log.With(slog.String("databaseId", databaseID), slog.String("snapshotId", snapshotID))

	if err := r.mc.EnsureBucket(ctx); err != nil {
		return r.markSnapshotFailed(ctx, snapID, snapshotID, fmt.Errorf("ensure bucket: %w", err))
	}

	storageKey := fmt.Sprintf("%s/%s.%s", databaseID, snapshotID, snapshotExt(dbRow.Type))

	dump, finish, err := r.dumpStream(ctx, dbRow, containerName(databaseID))
	if err != nil {
		return r.markSnapshotFailed(ctx, snapID, snapshotID, err)
	}
	defer func() { _ = dump.Close() }()

	// Streamed, not buffered: a dump can be far larger than this process should
	// hold. The size comes back from the upload rather than being known upfront.
	size, err := r.mc.PutObjectStream(ctx, storageKey, dump)
	if err != nil {
		return r.markSnapshotFailed(ctx, snapID, snapshotID, fmt.Errorf("upload: %w", err))
	}
	// A tool that failed partway still uploaded whatever it managed to write.
	// Drop those bytes: a truncated dump that looks READY is worse than no
	// snapshot at all, because it is only discovered at restore time.
	if err := finish(); err != nil {
		if derr := r.mc.DeleteObject(context.WithoutCancel(ctx), storageKey); derr != nil {
			log.Warn("could not remove partial dump", slog.Any("err", derr))
		}
		return r.markSnapshotFailed(ctx, snapID, snapshotID, err)
	}

	if err := r.q.MarkSnapshotReady(ctx, dbgen.MarkSnapshotReadyParams{
		ID:         snapID,
		StorageKey: &storageKey,
		SizeBytes:  &size,
	}); err != nil {
		return fmt.Errorf("mark snapshot ready: %w", err)
	}

	if err := r.emit(ctx, eventSnapshotReady, databaseID, map[string]any{
		"snapshotId": snapshotID,
		"storageKey": storageKey,
		"sizeBytes":  size,
	}); err != nil {
		log.Warn("outbox snapshot_ready event", slog.Any("err", err))
	}
	log.Info("snapshot uploaded", slog.Int64("bytes", size))
	return nil
}

func (r *Router) restore(ctx context.Context, databaseID, snapshotID string) error {
	if snapshotID == "" {
		return errors.New("restore job missing snapshotId")
	}
	snapID := db.MustUUID(snapshotID)

	snap, err := r.q.GetSnapshot(ctx, snapID)
	if err != nil {
		return fmt.Errorf("get snapshot: %w", err)
	}
	imported := snap.Origin == dbgen.SnapshotOriginIMPORT

	err = r.restoreSnapshot(ctx, databaseID, snapshotID, snap, imported)
	if imported {
		r.recordImportOutcome(ctx, snapID, err)
	}
	return err
}

func (r *Router) restoreSnapshot(ctx context.Context, databaseID, snapshotID string, snap dbgen.GetSnapshotRow, replace bool) error {
	if snap.StorageKey == nil || *snap.StorageKey == "" {
		return fmt.Errorf("snapshot %s has no storage_key", snapshotID)
	}

	dbRow, err := r.q.GetDatabase(ctx, db.MustUUID(databaseID))
	if err != nil {
		return fmt.Errorf("get database: %w", err)
	}

	log := r.log.With(slog.String("databaseId", databaseID), slog.String("snapshotId", snapshotID))

	obj, err := r.mc.GetObject(ctx, *snap.StorageKey)
	if err != nil {
		return fmt.Errorf("download snapshot: %w", err)
	}
	defer func() { _ = obj.Close() }()

	if err := r.restoreInto(ctx, dbRow, containerName(databaseID), obj, replace); err != nil {
		return fmt.Errorf("restore %s: %w", dbRow.Type, err)
	}

	if err := r.emit(ctx, eventSnapshotRestored, databaseID, map[string]any{"snapshotId": snapshotID}); err != nil {
		log.Warn("outbox snapshot_restored event", slog.Any("err", err))
	}
	log.Info("snapshot restored", slog.Bool("replace", replace))
	return nil
}

func (r *Router) recordImportOutcome(ctx context.Context, snapID pgtype.UUID, restoreErr error) {
	status := dbgen.ImportStatusSUCCEEDED
	var summary *string
	if restoreErr != nil {
		status = dbgen.ImportStatusFAILED
		msg := restoreErr.Error()
		summary = &msg
	}
	if err := r.q.SetImportStatus(context.WithoutCancel(ctx), dbgen.SetImportStatusParams{
		ID:           snapID,
		ImportStatus: &status,
		ErrorSummary: summary,
	}); err != nil {
		r.log.Error("record import outcome", slog.Any("err", err))
	}
}

// snapshotDelete removes the stored dump. The key travels on the job because
// the API drops the row as soon as it enqueues, so there is nothing left to read
// it from — the same shape as a volume-backup delete.
func (r *Router) snapshotDelete(ctx context.Context, snapshotID, storageKey string) error {
	if snapshotID == "" {
		return errors.New("snapshot-delete job missing snapshotId")
	}
	if storageKey == "" {
		return nil
	}
	if err := r.mc.DeleteObject(ctx, storageKey); err != nil {
		return fmt.Errorf("delete object: %w", err)
	}
	return nil
}

func (r *Router) markSnapshotFailed(ctx context.Context, id pgtype.UUID, snapshotID string, runErr error) error {
	summary := runErr.Error()
	if err := r.q.MarkSnapshotFailed(ctx, dbgen.MarkSnapshotFailedParams{ID: id, ErrorSummary: &summary}); err != nil {
		r.log.Error("mark snapshot failed write", slog.Any("err", err))
	}
	if oerr := r.emit(ctx, eventSnapshotFailed, snapshotID, map[string]any{"error": summary}); oerr != nil {
		r.log.Warn("outbox snapshot_failed event", slog.Any("err", oerr))
	}
	return runErr
}
