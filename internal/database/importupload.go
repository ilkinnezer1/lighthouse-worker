package database

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ilkinnezer1/lighthouse-worker/internal/db"
	dbgen "github.com/ilkinnezer1/lighthouse-worker/internal/db/gen"
)

var (
	ErrImportNotFound    = errors.New("import snapshot not found")
	ErrImportConflict    = errors.New("snapshot is not a pending import")
	ErrImportBadFile     = errors.New("unsupported import file")
	ErrImportTooLarge    = errors.New("import exceeds the size limit")
	ErrImportLengthUnset = errors.New("Content-Length is required")
)

type ImportUpload struct {
	DatabaseID    string
	SnapshotID    string
	FileName      string
	ContentLength int64
	Body          io.Reader
}

func importExt(t dbgen.DatabaseType, fileName string) (string, error) {
	name := strings.ToLower(fileName)
	switch {
	case strings.HasSuffix(name, ".sql.gz"):
		return "sql.gz", nil
	case strings.HasSuffix(name, ".sql"):
		return "sql", nil
	case strings.HasSuffix(name, ".dump") && t == dbgen.DatabaseTypePOSTGRES:
		return "dump", nil
	}
	return "", fmt.Errorf("%w: %q", ErrImportBadFile, fileName)
}

// StoreImport streams an uploaded dump into the bucket and marks the snapshot
// READY. Rows that are not a pending import are left untouched; anything that
// fails after that check marks the row failed, which may race the API marking
// it failed too, so the write keeps whichever summary landed first.
func (r *Router) StoreImport(ctx context.Context, up ImportUpload) (int64, error) {
	snapID, err := db.ParseUUID(up.SnapshotID)
	if err != nil {
		return 0, ErrImportNotFound
	}
	dbID, err := db.ParseUUID(up.DatabaseID)
	if err != nil {
		return 0, ErrImportNotFound
	}
	snap, err := r.q.GetSnapshot(ctx, snapID)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && snap.DatabaseID != dbID) {
		return 0, ErrImportNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("get snapshot: %w", err)
	}
	if snap.Origin != dbgen.SnapshotOriginIMPORT || snap.Status != dbgen.SnapshotStatusPENDING {
		return 0, ErrImportConflict
	}

	size, err := r.storeImportBlob(ctx, snap, up)
	if err != nil {
		r.markImportUploadFailed(ctx, snapID, err)
		return 0, err
	}
	return size, nil
}

func (r *Router) storeImportBlob(ctx context.Context, snap dbgen.GetSnapshotRow, up ImportUpload) (int64, error) {
	if up.ContentLength < 0 {
		return 0, ErrImportLengthUnset
	}
	if up.ContentLength > r.importMaxBytes {
		return 0, fmt.Errorf("%w (%d > %d bytes)", ErrImportTooLarge, up.ContentLength, r.importMaxBytes)
	}

	dbRow, err := r.q.GetDatabase(ctx, snap.DatabaseID)
	if err != nil {
		return 0, fmt.Errorf("get database: %w", err)
	}
	ext, err := importExt(dbRow.Type, up.FileName)
	if err != nil {
		return 0, err
	}
	if err := r.mc.EnsureBucket(ctx); err != nil {
		return 0, fmt.Errorf("ensure bucket: %w", err)
	}

	storageKey := fmt.Sprintf("%s/%s.%s", db.UUIDString(snap.DatabaseID), db.UUIDString(snap.ID), ext)
	body := &cappedReader{r: up.Body, remaining: r.importMaxBytes}
	size, err := r.mc.PutObjectStream(ctx, storageKey, body)
	if err == nil && size != up.ContentLength {
		err = fmt.Errorf("stored %d bytes, expected %d", size, up.ContentLength)
	}
	if err == nil {
		err = r.markImportStored(ctx, snap.ID, storageKey, size)
	}
	if err != nil {
		if derr := r.mc.DeleteObject(context.WithoutCancel(ctx), storageKey); derr != nil {
			r.log.Warn("could not remove partial import", slog.String("key", storageKey), slog.Any("err", derr))
		}
		if body.exceeded {
			return 0, ErrImportTooLarge
		}
		return 0, fmt.Errorf("upload: %w", err)
	}
	r.log.Info("import stored", slog.String("snapshotId", up.SnapshotID), slog.Int64("bytes", size))
	return size, nil
}

func (r *Router) markImportStored(ctx context.Context, id pgtype.UUID, storageKey string, size int64) error {
	n, err := r.q.MarkImportStored(ctx, dbgen.MarkImportStoredParams{ID: id, StorageKey: &storageKey, SizeBytes: &size})
	if err != nil {
		return fmt.Errorf("mark import stored: %w", err)
	}
	if n == 0 {
		return ErrImportConflict
	}
	return nil
}

func (r *Router) markImportUploadFailed(ctx context.Context, id pgtype.UUID, cause error) {
	summary := cause.Error()
	if err := r.q.MarkImportUploadFailed(context.WithoutCancel(ctx), dbgen.MarkImportUploadFailedParams{
		ID:           id,
		ErrorSummary: &summary,
	}); err != nil {
		r.log.Error("mark import upload failed", slog.Any("err", err))
	}
}

type cappedReader struct {
	r         io.Reader
	remaining int64
	exceeded  bool
}

func (c *cappedReader) Read(p []byte) (int, error) {
	if c.remaining <= 0 {
		var probe [1]byte
		if n, _ := c.r.Read(probe[:]); n > 0 {
			c.exceeded = true
			return 0, ErrImportTooLarge
		}
		return 0, io.EOF
	}
	if int64(len(p)) > c.remaining {
		p = p[:c.remaining]
	}
	n, err := c.r.Read(p)
	c.remaining -= int64(n)
	return n, err
}
