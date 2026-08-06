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
	"github.com/ilkinnezer1/lighthouse-worker/internal/stages"
)

// restore replaces the volume's contents with a backup's.
//
// Writers are STOPPED, not paused: the archive replaces files underneath them,
// and a paused process resumes holding file descriptors onto data that no longer
// exists. They are started again afterwards, so a restore costs a short outage —
// which is the honest trade for not corrupting the running app.
func (r *Router) restore(ctx context.Context, backupID string) error {
	row, _, err := r.loadBackup(ctx, backupID)
	if err != nil {
		return err
	}
	if row.StorageKey == nil || *row.StorageKey == "" {
		return fmt.Errorf("volume backup %s has no stored archive", backupID)
	}

	envID := db.UUIDString(row.EnvironmentID)
	dockerVolume := stages.VolumeName(envID, row.VolumeName)
	log := r.log.With(
		slog.String("backupId", backupID),
		slog.String("volume", dockerVolume),
	)

	if err := r.ensureHelperImage(ctx); err != nil {
		return err
	}
	obj, err := r.mc.GetObject(ctx, *row.StorageKey)
	if err != nil {
		return fmt.Errorf("download archive: %w", err)
	}
	defer obj.Close()

	stopped, err := r.stopWriters(ctx, dockerVolume, log)
	if err != nil {
		return err
	}
	// Detached context: the app must come back up even if the job is cancelled.
	defer r.startWriters(context.WithoutCancel(ctx), stopped, log)

	if err := r.streamIn(ctx, dockerVolume, obj); err != nil {
		return err
	}
	log.Info("volume restored", slog.Int("restarted", len(stopped)))
	return nil
}

// streamIn empties the volume and untars the archive into it, feeding the
// object straight from storage into the helper's stdin.
func (r *Router) streamIn(ctx context.Context, dockerVolume string, src io.Reader) error {
	// Deleting inside the mount rather than replacing it keeps the volume — and
	// therefore its identity and any container bindings — intact. The three
	// globs cover dotfiles, which a bare * misses.
	script := fmt.Sprintf(
		"set -e; rm -rf %s/..?* %s/.[!.]* %s/* 2>/dev/null || true; tar -xf - -C %s",
		mountPath, mountPath, mountPath, mountPath)

	helperID, err := r.createHelper(ctx, dockerVolume, []string{"sh", "-c", script}, false, true)
	if err != nil {
		return err
	}
	defer r.removeHelper(context.WithoutCancel(ctx), helperID)

	attach, err := r.docker.ContainerAttach(ctx, helperID, container.AttachOptions{
		Stream: true,
		Stdin:  true,
		Stdout: true,
		Stderr: true,
	})
	if err != nil {
		return fmt.Errorf("attach helper: %w", err)
	}
	defer attach.Close()

	if err := r.docker.ContainerStart(ctx, helperID, container.StartOptions{}); err != nil {
		return fmt.Errorf("start helper: %w", err)
	}

	var stderr bytes.Buffer
	drained := make(chan struct{})
	go func() {
		_, _ = stdcopy.StdCopy(io.Discard, &stderr, attach.Reader)
		close(drained)
	}()

	if _, err := io.Copy(attach.Conn, src); err != nil {
		return fmt.Errorf("stream archive to helper: %w", err)
	}
	if err := attach.CloseWrite(); err != nil {
		return fmt.Errorf("close helper stdin: %w", err)
	}
	<-drained

	return r.waitHelper(ctx, helperID, stderr.String())
}

func (r *Router) stopWriters(ctx context.Context, dockerVolume string, log *slog.Logger) ([]string, error) {
	ids, err := r.containersUsing(ctx, dockerVolume)
	if err != nil {
		return nil, err
	}
	stopped := make([]string, 0, len(ids))
	for _, id := range ids {
		if err := r.docker.ContainerStop(ctx, id, container.StopOptions{}); err != nil {
			// Restart whatever we already stopped: a half-stopped app plus a
			// half-restored volume is the worst state to leave behind.
			r.startWriters(context.WithoutCancel(ctx), stopped, log)
			return nil, fmt.Errorf("stop container %s before restore: %w", id, err)
		}
		stopped = append(stopped, id)
	}
	if len(stopped) > 0 {
		log.Info("stopped writers for restore", slog.Int("count", len(stopped)))
	}
	return stopped, nil
}

func (r *Router) startWriters(ctx context.Context, ids []string, log *slog.Logger) {
	for _, id := range ids {
		if err := r.docker.ContainerStart(ctx, id, container.StartOptions{}); err != nil {
			log.Error("could not restart container after restore",
				slog.String("container", id), slog.Any("err", err))
		}
	}
}

// deleteObject drops the archive from storage. A backup that never reached
// READY has no key, and there is nothing to remove.
func (r *Router) deleteObject(ctx context.Context, storageKey string) error {
	if storageKey == "" {
		return nil
	}
	if err := r.mc.DeleteObject(ctx, storageKey); err != nil {
		return fmt.Errorf("delete object: %w", err)
	}
	return nil
}
