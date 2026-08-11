package database

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/docker/docker/api/types/container"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ilkinnezer1/lighthouse-worker/internal/db"
	dbgen "github.com/ilkinnezer1/lighthouse-worker/internal/db/gen"
)

// seedFromSnapshot (mode 4): download the snapshot from MinIO and pg_restore
// into the freshly-provisioned container.
func (r *Router) seedFromSnapshot(ctx context.Context, dstContainer, dbName string, snapID pgtype.UUID, log *slog.Logger) error {
	snap, err := r.q.GetSnapshot(ctx, snapID)
	if err != nil {
		return fmt.Errorf("get snapshot row: %w", err)
	}
	if snap.StorageKey == nil || *snap.StorageKey == "" {
		return fmt.Errorf("snapshot %s has no storage_key", db.UUIDString(snapID))
	}

	obj, err := r.mc.GetObject(ctx, *snap.StorageKey)
	if err != nil {
		return fmt.Errorf("download snapshot: %w", err)
	}
	defer func() { _ = obj.Close() }()

	log.Info("restoring snapshot into new container", slog.String("snapshotId", db.UUIDString(snapID)))
	return r.pgRestore(ctx, dstContainer, dbName, obj)
}

// seedFromDatabase (mode 3): pg_dump from a live source container streamed
// directly into pg_restore in the destination container. No MinIO involved.
func (r *Router) seedFromDatabase(ctx context.Context, dstContainer, dstDBName string, srcID pgtype.UUID, log *slog.Logger) error {
	src, err := r.q.GetDatabase(ctx, srcID)
	if err != nil {
		return fmt.Errorf("get source database row: %w", err)
	}
	if src.Status != dbgen.DatabaseStatusREADY {
		return fmt.Errorf("source database not READY (status=%s)", src.Status)
	}
	srcContainer := containerName(db.UUIDString(srcID))

	log.Info("copying from source database",
		slog.String("sourceDatabaseId", db.UUIDString(srcID)),
		slog.String("sourceContainer", srcContainer),
	)

	dumpExec, err := r.docker.ContainerExecCreate(ctx, srcContainer, container.ExecOptions{
		Cmd:          []string{"pg_dump", "--format=custom", "-U", postgresUser, src.Name},
		AttachStdout: true,
		AttachStderr: true,
	})
	if err != nil {
		return fmt.Errorf("source pg_dump exec create: %w", err)
	}
	dumpAttach, err := r.docker.ContainerExecAttach(ctx, dumpExec.ID, container.ExecStartOptions{})
	if err != nil {
		return fmt.Errorf("source pg_dump attach: %w", err)
	}
	defer dumpAttach.Close()

	restoreExec, err := r.docker.ContainerExecCreate(ctx, dstContainer, container.ExecOptions{
		Cmd:          []string{"pg_restore", "--clean", "--if-exists", "-U", postgresUser, "-d", dstDBName},
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
	})
	if err != nil {
		return fmt.Errorf("dest pg_restore exec create: %w", err)
	}
	restoreAttach, err := r.docker.ContainerExecAttach(ctx, restoreExec.ID, container.ExecStartOptions{})
	if err != nil {
		return fmt.Errorf("dest pg_restore attach: %w", err)
	}
	defer restoreAttach.Close()

	if err := pipeExecs(dumpAttach, restoreAttach); err != nil {
		return err
	}
	if err := r.expectExecSuccess(ctx, dumpExec.ID, "pg_dump"); err != nil {
		return err
	}
	if err := r.expectExecSuccess(ctx, restoreExec.ID, "pg_restore"); err != nil {
		return err
	}
	return nil
}

// runMigrate (mode 5): execute the project's buildConfig.dbMigrate command in
// a one-shot container of the latest successful deployment's image, with
// DATABASE_URL set to the freshly-provisioned conn string. If no deployment
// image exists yet, logs and returns nil — the user's first deploy will run
// migrations on app startup.
func (r *Router) runMigrate(ctx context.Context, row dbgen.GetDatabaseRow, connStr string, log *slog.Logger) error {
	cfgRow, err := r.q.GetProjectBuildConfigByEnvironment(ctx, row.EnvironmentID)
	if err != nil {
		return fmt.Errorf("lookup project build_config: %w", err)
	}
	var bc struct {
		DBMigrate []string `json:"dbMigrate"`
	}
	if err := json.Unmarshal(cfgRow.BuildConfig, &bc); err != nil {
		return fmt.Errorf("parse build_config: %w", err)
	}
	if len(bc.DBMigrate) == 0 {
		return errors.New("buildConfig.dbMigrate is empty")
	}

	depID, err := r.q.GetLatestSuccessfulDeploymentForEnvironment(ctx, row.EnvironmentID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			log.Info("runMigrate deferred: no successful deployment yet")
			return nil
		}
		return fmt.Errorf("lookup latest deployment: %w", err)
	}
	image := "deploy-" + db.UUIDString(depID)
	networkName := "env-" + db.UUIDString(row.EnvironmentID)

	log.Info("running migrate", slog.String("image", image), slog.Any("cmd", bc.DBMigrate))

	cid, err := r.docker.ContainerCreate(ctx, &container.Config{
		Image: image,
		Cmd:   bc.DBMigrate,
		Env:   []string{"DATABASE_URL=" + connStr},
	}, &container.HostConfig{}, nil, nil, "")
	if err != nil {
		return fmt.Errorf("create migrate container: %w", err)
	}
	defer func() {
		_ = r.docker.ContainerRemove(ctx, cid.ID, container.RemoveOptions{Force: true})
	}()

	if err := r.docker.NetworkConnect(ctx, networkName, cid.ID, nil); err != nil {
		return fmt.Errorf("attach migrate container to %s: %w", networkName, err)
	}
	if err := r.docker.ContainerStart(ctx, cid.ID, container.StartOptions{}); err != nil {
		return fmt.Errorf("start migrate container: %w", err)
	}

	statusCh, errCh := r.docker.ContainerWait(ctx, cid.ID, container.WaitConditionNotRunning)
	select {
	case err := <-errCh:
		return fmt.Errorf("wait migrate container: %w", err)
	case s := <-statusCh:
		if s.StatusCode != 0 {
			return fmt.Errorf("migrate command exit %d", s.StatusCode)
		}
	case <-ctx.Done():
		return ctx.Err()
	}
	return nil
}

func (r *Router) expectExecSuccess(ctx context.Context, execID, label string) error {
	info, err := r.docker.ContainerExecInspect(ctx, execID)
	if err != nil {
		return fmt.Errorf("%s inspect: %w", label, err)
	}
	if info.ExitCode != 0 {
		return fmt.Errorf("%s exit %d", label, info.ExitCode)
	}
	return nil
}
