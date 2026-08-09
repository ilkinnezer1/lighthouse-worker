package database

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"time"

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

func (r *Router) pgRestore(ctx context.Context, containerName, dbName string, src io.Reader) error {
	return r.execWithStdin(ctx, containerName,
		[]string{"pg_restore", "--clean", "--if-exists", "-U", postgresUser, "-d", dbName}, nil, src)
}
