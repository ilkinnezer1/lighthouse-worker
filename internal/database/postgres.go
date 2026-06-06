package database

import (
	"bytes"
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
	"github.com/docker/docker/pkg/stdcopy"

	"github.com/ilkinnezer1/lighthouse-worker/internal/db"
	dbgen "github.com/ilkinnezer1/lighthouse-worker/internal/db/gen"
)

const (
	postgresUser  = "lighthouse"
	postgresPort  = 5432
	readyTimeout  = 60 * time.Second
	readyInterval = time.Second
)

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
