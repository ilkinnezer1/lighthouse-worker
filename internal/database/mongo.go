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
	mongoUser         = "root"
	mongoPort         = 27017
	mongoReadyTimeout = 90 * time.Second
)

// provisionMongo starts a MongoDB container with a root user. row.Name becomes
// the initial database; the connection authenticates against the admin db.
// Readiness uses mongosh, so the chosen version must ship it (mongo:5+).
func (r *Router) provisionMongo(ctx context.Context, row dbgen.GetDatabaseRow, log *slog.Logger) (host string, port int32, connStr string, err error) {
	networkName := "env-" + db.UUIDString(row.EnvironmentID)
	if err := r.ensureNetwork(ctx, networkName); err != nil {
		return "", 0, "", err
	}

	name := containerName(db.UUIDString(row.ID))
	imageRef := "mongo:" + row.Version
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
			"MONGO_INITDB_ROOT_USERNAME=" + mongoUser,
			"MONGO_INITDB_ROOT_PASSWORD=" + pass,
			"MONGO_INITDB_DATABASE=" + row.Name,
		},
		volumeName:  name + "-data",
		mountTarget: "/data/db",
	})
	if err != nil {
		return "", 0, "", err
	}
	log.Info("container started", slog.String("containerId", cid[:12]))

	ready := func(c context.Context) bool {
		return r.execOK(c, cid, []string{"mongosh", "--quiet", "--eval", "db.adminCommand('ping').ok"})
	}
	if err := r.waitUntilReady(ctx, "mongosh ping", mongoReadyTimeout, ready); err != nil {
		return "", 0, "", fmt.Errorf("wait for ready: %w", err)
	}

	host = name
	port = int32(mongoPort)
	connStr = fmt.Sprintf("mongodb://%s:%s@%s:%d/%s?authSource=admin", mongoUser, pass, host, port, row.Name)
	return host, port, connStr, nil
}
