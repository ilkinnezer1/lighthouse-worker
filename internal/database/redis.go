package database

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/docker/docker/api/types/container"

	"github.com/ilkinnezer1/lighthouse-worker/internal/db"
	dbgen "github.com/ilkinnezer1/lighthouse-worker/internal/db/gen"
)

const (
	redisPort         = 6379
	redisReadyTimeout = 60 * time.Second
)

// provisionRedis starts a password-protected Redis container (AOF persistence).
// Redis has no named databases, so row.Name is the lighthouse-side identifier
// only; the connection string targets db 0.
func (r *Router) provisionRedis(ctx context.Context, row dbgen.GetDatabaseRow, log *slog.Logger) (host string, port int32, connStr string, err error) {
	networkName := "env-" + db.UUIDString(row.EnvironmentID)
	if err := r.ensureNetwork(ctx, networkName); err != nil {
		return "", 0, "", err
	}

	name := containerName(db.UUIDString(row.ID))
	imageRef := "redis:" + row.Version
	if err := r.ensureImage(ctx, imageRef, log); err != nil {
		return "", 0, "", fmt.Errorf("pull image: %w", err)
	}

	pass, err := randomPassword()
	if err != nil {
		return "", 0, "", fmt.Errorf("generate password: %w", err)
	}

	cid, err := r.startEngineContainer(ctx, containerSpec{
		name:        name,
		image:       imageRef,
		networkName: networkName,
		cmd:         []string{"redis-server", "--requirepass", pass, "--appendonly", "yes"},
		volumeName:  name + "-data",
		mountTarget: "/data",
	})
	if err != nil {
		return "", 0, "", err
	}
	log.Info("container started", slog.String("containerId", cid[:12]))

	ready := func(c context.Context) bool {
		return r.execOK(c, cid, []string{"redis-cli", "--no-auth-warning", "-a", pass, "ping"})
	}
	if err := r.waitUntilReady(ctx, "redis-cli ping", redisReadyTimeout, ready); err != nil {
		return "", 0, "", fmt.Errorf("wait for ready: %w", err)
	}

	host = name
	port = int32(redisPort)
	connStr = fmt.Sprintf("redis://default:%s@%s:%d/0", pass, host, port)
	return host, port, connStr, nil
}

// A Redis snapshot is a tar of the whole data directory, not an RDB.
//
// An RDB is the obvious choice and it is the wrong one: these containers run
// with --appendonly yes, and at startup Redis loads the AOF *only* — it never
// falls back to dump.rdb. Restoring an RDB and clearing the AOF to make room for
// it therefore brings the server back empty, silently destroying the data the
// restore was meant to recover. Capturing the data directory keeps the AOF and
// the RDB together, so Redis reloads exactly what it was serving.
func (r *Router) redisBackupStream(ctx context.Context, name string) (io.ReadCloser, func() error, error) {
	if err := r.ensureHelperImage(ctx); err != nil {
		return nil, nil, err
	}

	paused, err := r.pauseIfRunning(ctx, name)
	if err != nil {
		return nil, nil, err
	}
	unpause := func() {
		if !paused {
			return
		}
		// Detached context: a cancelled job must never leave Redis suspended.
		if err := r.docker.ContainerUnpause(context.WithoutCancel(ctx), name); err != nil {
			r.log.Error("could not unpause redis after backup",
				slog.String("container", name), slog.Any("err", err))
		}
	}

	// Paused rather than stopped: the process is frozen mid-flight so the AOF and
	// RDB are tarred as one consistent set, without an outage.
	stream, finish, err := r.streamVolumeOut(ctx, name+"-data", "tar -cf - -C "+helperMount+" .")
	if err != nil {
		unpause()
		return nil, nil, err
	}
	return stream, func() error {
		defer unpause()
		return finish()
	}, nil
}

// redisRestore replaces the instance's data directory with a snapshot's.
//
// This cannot be a live operation: the files are replaced underneath a server
// that has them open, and Redis only reads them at startup. So the server is
// stopped and started again, and a restore openly costs a short outage — the
// same honest trade a volume restore makes.
func (r *Router) redisRestore(ctx context.Context, name string, src io.Reader) error {
	if err := r.ensureHelperImage(ctx); err != nil {
		return err
	}
	if err := r.docker.ContainerStop(ctx, name, container.StopOptions{}); err != nil {
		return fmt.Errorf("stop redis before restore: %w", err)
	}
	// Detached context: the database must come back up even if the job is cancelled.
	defer func() {
		startCtx := context.WithoutCancel(ctx)
		if err := r.docker.ContainerStart(startCtx, name, container.StartOptions{}); err != nil {
			r.log.Error("could not restart redis after restore",
				slog.String("container", name), slog.Any("err", err))
		}
	}()

	// The three globs cover dotfiles, which a bare * misses.
	script := fmt.Sprintf(
		"set -e; rm -rf %s/..?* %s/.[!.]* %s/* 2>/dev/null || true; tar -xf - -C %s",
		helperMount, helperMount, helperMount, helperMount)

	if err := r.streamVolumeIn(ctx, name+"-data", script, src); err != nil {
		return err
	}
	r.log.Info("redis data directory replaced", slog.String("container", name))
	return nil
}

// pauseIfRunning freezes the container and reports whether it did, so a database
// that was already stopped is left as it was found.
func (r *Router) pauseIfRunning(ctx context.Context, name string) (bool, error) {
	info, err := r.docker.ContainerInspect(ctx, name)
	if err != nil {
		return false, fmt.Errorf("inspect %s: %w", name, err)
	}
	if info.State == nil || !info.State.Running || info.State.Paused {
		return false, nil
	}
	if err := r.docker.ContainerPause(ctx, name); err != nil {
		return false, fmt.Errorf("pause redis for backup: %w", err)
	}
	return true, nil
}
