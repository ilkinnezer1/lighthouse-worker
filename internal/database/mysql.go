package database

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/ilkinnezer1/lighthouse-worker/internal/db"
	dbgen "github.com/ilkinnezer1/lighthouse-worker/internal/db/gen"
)

const (
	mysqlUser         = "lighthouse"
	mysqlPort         = 3306
	mysqlReadyTimeout = 120 * time.Second // MySQL cold-start (data-dir init) is slower than Postgres.
)

// provisionMysql starts a MySQL container for a create-empty managed database.
// Seeding modes (copy-from-database, create-from-snapshot) stay Postgres-only,
// so this path never seeds; snapshot and in-place restore work on every engine.
func (r *Router) provisionMysql(ctx context.Context, row dbgen.GetDatabaseRow, log *slog.Logger) (host string, port int32, connStr string, err error) {
	networkName := "env-" + db.UUIDString(row.EnvironmentID)
	if err := r.ensureNetwork(ctx, networkName); err != nil {
		return "", 0, "", err
	}

	name := containerName(db.UUIDString(row.ID))
	imageRef := "mysql:" + row.Version
	if err := r.ensureImage(ctx, imageRef, log); err != nil {
		return "", 0, "", fmt.Errorf("pull image: %w", err)
	}

	rootPass, err := randomPassword()
	if err != nil {
		return "", 0, "", fmt.Errorf("generate root password: %w", err)
	}
	userPass, err := randomPassword()
	if err != nil {
		return "", 0, "", fmt.Errorf("generate user password: %w", err)
	}

	cid, err := r.startEngineContainer(ctx, containerSpec{
		name:        name,
		image:       imageRef,
		networkName: networkName,
		env: []string{
			"MYSQL_ROOT_PASSWORD=" + rootPass,
			"MYSQL_DATABASE=" + row.Name,
			"MYSQL_USER=" + mysqlUser,
			"MYSQL_PASSWORD=" + userPass,
		},
		volumeName:  name + "-data",
		mountTarget: "/var/lib/mysql",
	})
	if err != nil {
		return "", 0, "", err
	}
	log.Info("container started", slog.String("containerId", cid[:12]))

	ready := func(c context.Context) bool {
		return r.execOK(c, cid, []string{"mysqladmin", "ping", "-h", "127.0.0.1", "-uroot", "-p" + rootPass, "--silent"})
	}
	if err := r.waitUntilReady(ctx, "mysqladmin ping", mysqlReadyTimeout, ready); err != nil {
		return "", 0, "", fmt.Errorf("wait for ready: %w", err)
	}

	host = name
	port = int32(mysqlPort)
	connStr = fmt.Sprintf("mysql://%s:%s@%s:%d/%s", mysqlUser, userPass, host, port, row.Name)
	return host, port, connStr, nil
}
