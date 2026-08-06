package volumebackup

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/pkg/stdcopy"

	"github.com/ilkinnezer1/lighthouse-worker/internal/db"
	dbgen "github.com/ilkinnezer1/lighthouse-worker/internal/db/gen"
	"github.com/ilkinnezer1/lighthouse-worker/internal/stages"
)

// backup tars the environment's volume and streams it into MinIO.
//
// Writers are PAUSED for the duration rather than left running. A live tar of a
// volume under write can capture a torn file set — for a SQLite-backed app
// (Vaultwarden, Ghost, Gitea) that is the difference between a backup and a
// corrupt archive you only discover you can't restore. Pausing freezes the
// processes without a restart, and tarring the whole directory (including any
// -wal/-journal sidecars) gives the engine a recoverable set.
func (r *Router) backup(ctx context.Context, backupID string) error {
	row, id, err := r.loadBackup(ctx, backupID)
	if err != nil {
		return r.markFailed(ctx, id, err)
	}

	envID := db.UUIDString(row.EnvironmentID)
	dockerVolume := stages.VolumeName(envID, row.VolumeName)
	log := r.log.With(
		slog.String("backupId", backupID),
		slog.String("volume", dockerVolume),
	)

	if _, err := r.docker.VolumeInspect(ctx, dockerVolume); err != nil {
		return r.markFailed(ctx, id, fmt.Errorf(
			"volume %s does not exist yet — deploy once with this volume declared before backing it up",
			dockerVolume))
	}
	if err := r.ensureHelperImage(ctx); err != nil {
		return r.markFailed(ctx, id, err)
	}
	if err := r.mc.EnsureBucket(ctx); err != nil {
		return r.markFailed(ctx, id, fmt.Errorf("ensure bucket: %w", err))
	}

	frozen := r.freeze(ctx, dockerVolume, log)
	defer r.unfreeze(context.WithoutCancel(ctx), frozen, log)

	storageKey := fmt.Sprintf("volumes/%s/%s/%s.tar", envID, row.VolumeName, backupID)
	size, err := r.streamOut(ctx, dockerVolume, storageKey)
	if err != nil {
		return r.markFailed(ctx, id, err)
	}

	if err := r.q.MarkVolumeBackupReady(ctx, dbgen.MarkVolumeBackupReadyParams{
		ID:         id,
		StorageKey: &storageKey,
		SizeBytes:  &size,
	}); err != nil {
		return fmt.Errorf("mark volume backup ready: %w", err)
	}
	log.Info("volume backup uploaded", slog.Int64("bytes", size))
	return nil
}

// streamOut runs `tar` in a helper container and pipes its stdout straight into
// object storage — never through a buffer, because a volume can be far larger
// than this process should hold in memory.
func (r *Router) streamOut(ctx context.Context, dockerVolume, storageKey string) (int64, error) {
	helperID, err := r.createHelper(ctx, dockerVolume,
		[]string{"tar", "-cf", "-", "-C", mountPath, "."}, true, false)
	if err != nil {
		return 0, err
	}
	defer r.removeHelper(context.WithoutCancel(ctx), helperID)

	attach, err := r.docker.ContainerAttach(ctx, helperID, container.AttachOptions{
		Stream: true,
		Stdout: true,
		Stderr: true,
	})
	if err != nil {
		return 0, fmt.Errorf("attach helper: %w", err)
	}
	defer attach.Close()

	if err := r.docker.ContainerStart(ctx, helperID, container.StartOptions{}); err != nil {
		return 0, fmt.Errorf("start helper: %w", err)
	}

	pr, pw := io.Pipe()
	var stderr bytes.Buffer
	go func() {
		_, copyErr := stdcopy.StdCopy(pw, &stderr, attach.Reader)
		_ = pw.CloseWithError(copyErr)
	}()

	size, err := r.mc.PutObjectStream(ctx, storageKey, pr)
	if err != nil {
		_ = pr.CloseWithError(err)
		return 0, fmt.Errorf("upload: %w", err)
	}
	if err := r.waitHelper(ctx, helperID, stderr.String()); err != nil {
		// The object is already in the bucket but the archive is not trustworthy.
		_ = r.mc.DeleteObject(context.WithoutCancel(ctx), storageKey)
		return 0, err
	}
	return size, nil
}

// freeze pauses every running container mounting the volume, returning the ones
// actually paused so they can be resumed. Best-effort: a container that refuses
// to pause is logged and left running rather than failing the whole backup.
func (r *Router) freeze(ctx context.Context, dockerVolume string, log *slog.Logger) []string {
	ids, err := r.containersUsing(ctx, dockerVolume)
	if err != nil {
		log.Warn("could not list volume users; backing up without freezing", slog.Any("err", err))
		return nil
	}
	paused := make([]string, 0, len(ids))
	for _, id := range ids {
		if err := r.docker.ContainerPause(ctx, id); err != nil {
			log.Warn("could not pause writer", slog.String("container", id), slog.Any("err", err))
			continue
		}
		paused = append(paused, id)
	}
	if len(paused) > 0 {
		log.Info("paused writers for a consistent tar", slog.Int("count", len(paused)))
	}
	return paused
}

// unfreeze resumes what freeze paused. Runs on a context detached from the job's
// so a cancelled or timed-out backup can never leave the user's app suspended.
func (r *Router) unfreeze(ctx context.Context, ids []string, log *slog.Logger) {
	for _, id := range ids {
		if err := r.docker.ContainerUnpause(ctx, id); err != nil {
			log.Error("could not unpause writer — container may be left paused",
				slog.String("container", id), slog.Any("err", err))
		}
	}
}
