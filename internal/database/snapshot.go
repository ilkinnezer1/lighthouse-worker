package database

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/jackc/pgx/v5"
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
	if dbRow.Type != dbgen.DatabaseTypePOSTGRES {
		return r.markSnapshotFailed(ctx, snapID, snapshotID, fmt.Errorf("only POSTGRES supported in 7a, got %s", dbRow.Type))
	}
	if dbRow.Status != dbgen.DatabaseStatusREADY {
		return r.markSnapshotFailed(ctx, snapID, snapshotID, fmt.Errorf("database not READY (got %s)", dbRow.Status))
	}

	log := r.log.With(slog.String("databaseId", databaseID), slog.String("snapshotId", snapshotID))

	if err := r.mc.EnsureBucket(ctx); err != nil {
		return r.markSnapshotFailed(ctx, snapID, snapshotID, fmt.Errorf("ensure bucket: %w", err))
	}

	storageKey := fmt.Sprintf("%s/%s.dump", databaseID, snapshotID)

	dump, err := r.pgDump(ctx, containerName(databaseID), dbRow.Name)
	if err != nil {
		return r.markSnapshotFailed(ctx, snapID, snapshotID, err)
	}

	size := int64(dump.Len())
	if err := r.mc.PutObject(ctx, storageKey, bytes.NewReader(dump.Bytes()), size); err != nil {
		return r.markSnapshotFailed(ctx, snapID, snapshotID, fmt.Errorf("upload: %w", err))
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
	dbID := db.MustUUID(databaseID)

	snap, err := r.q.GetSnapshot(ctx, snapID)
	if err != nil {
		return fmt.Errorf("get snapshot: %w", err)
	}
	if snap.StorageKey == nil || *snap.StorageKey == "" {
		return fmt.Errorf("snapshot %s has no storage_key", snapshotID)
	}

	dbRow, err := r.q.GetDatabase(ctx, dbID)
	if err != nil {
		return fmt.Errorf("get database: %w", err)
	}

	log := r.log.With(slog.String("databaseId", databaseID), slog.String("snapshotId", snapshotID))

	obj, err := r.mc.GetObject(ctx, *snap.StorageKey)
	if err != nil {
		return fmt.Errorf("download snapshot: %w", err)
	}
	defer obj.Close()

	if err := r.pgRestore(ctx, containerName(databaseID), dbRow.Name, obj); err != nil {
		return fmt.Errorf("pg_restore: %w", err)
	}

	if err := r.emit(ctx, eventSnapshotRestored, databaseID, map[string]any{"snapshotId": snapshotID}); err != nil {
		log.Warn("outbox snapshot_restored event", slog.Any("err", err))
	}
	log.Info("snapshot restored")
	return nil
}

func (r *Router) snapshotDelete(ctx context.Context, snapshotID string) error {
	if snapshotID == "" {
		return errors.New("snapshot-delete job missing snapshotId")
	}
	snapID := db.MustUUID(snapshotID)
	snap, err := r.q.GetSnapshot(ctx, snapID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return fmt.Errorf("get snapshot: %w", err)
	}
	if snap.StorageKey == nil || *snap.StorageKey == "" {
		return nil
	}
	if err := r.mc.DeleteObject(ctx, *snap.StorageKey); err != nil {
		return fmt.Errorf("delete object: %w", err)
	}
	return nil
}

func (r *Router) pgDump(ctx context.Context, containerName, dbName string) (*bytes.Buffer, error) {
	exec, err := r.docker.ContainerExecCreate(ctx, containerName, container.ExecOptions{
		Cmd:          []string{"pg_dump", "--format=custom", "-U", postgresUser, dbName},
		AttachStdout: true,
		AttachStderr: true,
	})
	if err != nil {
		return nil, fmt.Errorf("exec create: %w", err)
	}
	attach, err := r.docker.ContainerExecAttach(ctx, exec.ID, container.ExecStartOptions{})
	if err != nil {
		return nil, fmt.Errorf("exec attach: %w", err)
	}
	defer attach.Close()

	var stdout, stderr bytes.Buffer
	if _, err := stdcopy.StdCopy(&stdout, &stderr, attach.Reader); err != nil {
		return nil, fmt.Errorf("read exec output: %w", err)
	}
	info, err := r.docker.ContainerExecInspect(ctx, exec.ID)
	if err != nil {
		return nil, fmt.Errorf("exec inspect: %w", err)
	}
	if info.ExitCode != 0 {
		return nil, fmt.Errorf("pg_dump exit %d: %s", info.ExitCode, stderr.String())
	}
	return &stdout, nil
}

func (r *Router) pgRestore(ctx context.Context, containerName, dbName string, src io.Reader) error {
	exec, err := r.docker.ContainerExecCreate(ctx, containerName, container.ExecOptions{
		Cmd:          []string{"pg_restore", "--clean", "--if-exists", "-U", postgresUser, "-d", dbName},
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
	})
	if err != nil {
		return fmt.Errorf("exec create: %w", err)
	}
	attach, err := r.docker.ContainerExecAttach(ctx, exec.ID, container.ExecStartOptions{})
	if err != nil {
		return fmt.Errorf("exec attach: %w", err)
	}
	defer attach.Close()

	copyErrCh := make(chan error, 1)
	go func() {
		_, err := io.Copy(attach.Conn, src)
		_ = attach.CloseWrite()
		copyErrCh <- err
	}()

	var stdout, stderr bytes.Buffer
	if _, err := stdcopy.StdCopy(&stdout, &stderr, attach.Reader); err != nil {
		return fmt.Errorf("read exec output: %w", err)
	}
	if err := <-copyErrCh; err != nil {
		return fmt.Errorf("stream snapshot to exec: %w", err)
	}
	info, err := r.docker.ContainerExecInspect(ctx, exec.ID)
	if err != nil {
		return fmt.Errorf("exec inspect: %w", err)
	}
	if info.ExitCode != 0 {
		return fmt.Errorf("pg_restore exit %d: %s", info.ExitCode, stderr.String())
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
