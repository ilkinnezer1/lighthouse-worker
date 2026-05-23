package database

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/errdefs"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ilkinnezer1/lighthouse-worker/internal/db"
	dbgen "github.com/ilkinnezer1/lighthouse-worker/internal/db/gen"
)

const (
	postgresUser  = "lighthouse"
	postgresPort  = 5432
	readyTimeout  = 60 * time.Second
	readyInterval = time.Second
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
		return r.markProvisionFailed(ctx, id, databaseID, fmt.Errorf("only POSTGRES supported in 7a, got %s", row.Type))
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

func (r *Router) provisionPostgres(ctx context.Context, row dbgen.GetDatabaseRow, log *slog.Logger) (host string, port int32, connStr string, err error) {
	envID := db.UUIDString(row.EnvironmentID)
	networkName := "env-" + envID
	if err := r.ensureNetwork(ctx, networkName); err != nil {
		return "", 0, "", err
	}

	name := containerName(db.UUIDString(row.ID))
	volumeName := name + "-data"
	imageRef := "postgres:" + row.Version

	if err := r.ensureImage(ctx, imageRef, log); err != nil {
		return "", 0, "", fmt.Errorf("pull image: %w", err)
	}

	pass, err := randomPassword()
	if err != nil {
		return "", 0, "", fmt.Errorf("generate password: %w", err)
	}

	cid, err := r.startContainer(ctx, name, volumeName, networkName, imageRef, row.Name, pass)
	if err != nil {
		return "", 0, "", err
	}
	log.Info("container started", slog.String("containerId", cid[:12]))

	if err := r.waitReady(ctx, cid); err != nil {
		return "", 0, "", fmt.Errorf("wait for ready: %w", err)
	}

	host = name
	port = int32(postgresPort)
	connStr = fmt.Sprintf("postgres://%s:%s@%s:%d/%s?sslmode=disable", postgresUser, pass, host, port, row.Name)

	if row.SeedFromSnapshotID.Valid {
		if err := r.seedFromSnapshot(ctx, name, row.Name, row.SeedFromSnapshotID, log); err != nil {
			return "", 0, "", fmt.Errorf("seed from snapshot: %w", err)
		}
	} else if row.SeedFromDatabaseID.Valid {
		if err := r.seedFromDatabase(ctx, name, row.Name, row.SeedFromDatabaseID, log); err != nil {
			return "", 0, "", fmt.Errorf("seed from database: %w", err)
		}
	}

	return host, port, connStr, nil
}

func (r *Router) ensureImage(ctx context.Context, ref string, log *slog.Logger) error {
	log.Info("ensuring image", slog.String("image", ref))
	rc, err := r.docker.ImagePull(ctx, ref, image.PullOptions{})
	if err != nil {
		return err
	}
	defer rc.Close()
	if _, err := io.Copy(io.Discard, rc); err != nil {
		return err
	}
	return nil
}

func (r *Router) startContainer(ctx context.Context, name, volumeName, networkName, image, dbName, pass string) (string, error) {
	cfg := &container.Config{
		Image: image,
		Env: []string{
			"POSTGRES_USER=" + postgresUser,
			"POSTGRES_PASSWORD=" + pass,
			"POSTGRES_DB=" + dbName,
		},
	}
	hostCfg := &container.HostConfig{
		Mounts: []mount.Mount{{
			Type:   mount.TypeVolume,
			Source: volumeName,
			Target: "/var/lib/postgresql/data",
		}},
		RestartPolicy: container.RestartPolicy{Name: "unless-stopped"},
	}
	netCfg := &network.NetworkingConfig{
		EndpointsConfig: map[string]*network.EndpointSettings{networkName: {}},
	}

	created, err := r.docker.ContainerCreate(ctx, cfg, hostCfg, netCfg, nil, name)
	if err != nil {
		if !isAlreadyExists(err) {
			return "", fmt.Errorf("container create: %w", err)
		}
		inspect, ierr := r.docker.ContainerInspect(ctx, name)
		if ierr != nil {
			return "", fmt.Errorf("inspect existing container: %w", ierr)
		}
		created.ID = inspect.ID
	}
	if err := r.docker.ContainerStart(ctx, created.ID, container.StartOptions{}); err != nil && !isAlreadyStarted(err) {
		return "", fmt.Errorf("container start: %w", err)
	}
	return created.ID, nil
}

func (r *Router) waitReady(ctx context.Context, containerID string) error {
	deadline := time.Now().Add(readyTimeout)
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return err
		}
		ok, _ := r.pgIsReady(ctx, containerID)
		if ok {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(readyInterval):
		}
	}
	return fmt.Errorf("pg_isready timed out after %s", readyTimeout)
}

func (r *Router) pgIsReady(ctx context.Context, containerID string) (bool, error) {
	exec, err := r.docker.ContainerExecCreate(ctx, containerID, container.ExecOptions{
		Cmd: []string{"pg_isready", "-U", postgresUser},
	})
	if err != nil {
		return false, err
	}
	if err := r.docker.ContainerExecStart(ctx, exec.ID, container.ExecStartOptions{}); err != nil {
		return false, err
	}
	for i := 0; i < 10; i++ {
		info, err := r.docker.ContainerExecInspect(ctx, exec.ID)
		if err != nil {
			return false, err
		}
		if !info.Running {
			return info.ExitCode == 0, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false, nil
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

func randomPassword() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return strings.TrimRight(base64.URLEncoding.EncodeToString(b), "="), nil
}

func isAlreadyExists(err error) bool {
	if err == nil {
		return false
	}
	if errdefs.IsConflict(err) {
		return true
	}
	return strings.Contains(err.Error(), "already in use")
}

func isAlreadyStarted(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "is already started")
}

func containerName(databaseID string) string {
	return "db-" + databaseID
}
