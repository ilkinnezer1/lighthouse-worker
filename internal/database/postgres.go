package database

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/pkg/stdcopy"

	"github.com/ilkinnezer1/lighthouse-worker/internal/db"
	dbgen "github.com/ilkinnezer1/lighthouse-worker/internal/db/gen"
)

const (
	postgresUser         = "lighthouse"
	postgresPort         = 5432
	postgresReadyTimeout = 60 * time.Second
)

func (r *Router) provisionPostgres(ctx context.Context, row dbgen.GetDatabaseRow, log *slog.Logger) (host string, port int32, connStr string, err error) {
	networkName := "env-" + db.UUIDString(row.EnvironmentID)
	if err := r.ensureNetwork(ctx, networkName); err != nil {
		return "", 0, "", err
	}

	name := containerName(db.UUIDString(row.ID))
	imageRef := "postgres:" + row.Version
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
		env: []string{
			"POSTGRES_USER=" + postgresUser,
			"POSTGRES_PASSWORD=" + pass,
			"POSTGRES_DB=" + row.Name,
		},
		volumeName:  name + "-data",
		mountTarget: "/var/lib/postgresql/data",
	})
	if err != nil {
		return "", 0, "", err
	}
	log.Info("container started", slog.String("containerId", cid[:12]))

	ready := func(c context.Context) bool {
		return r.execOK(c, cid, []string{"pg_isready", "-U", postgresUser})
	}
	if err := r.waitUntilReady(ctx, "pg_isready", postgresReadyTimeout, ready); err != nil {
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
