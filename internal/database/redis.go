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
