package stages

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"net/url"
	"strings"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/pkg/stdcopy"

	"github.com/ilkinnezer1/lighthouse-worker/internal/pipeline"
)

const (
	frameworkLaravel  = "laravel"
	laravelMigrateCmd = "php artisan migrate --force"
)

// applyLaravelEnvs adds the env vars a stock Laravel app needs on top of the
// managed DATABASE_URL, and reports whether a post-start migrate should run
// (only when a SQL managed DB actually wired DB_*). It fails the deploy only on a
// crypto/rand read error while minting an APP_KEY.
//
// Precedence mirrors the DB-URL policy in run.go: managed DB creds (DB_*) win
// over any user-set value, exactly like DATABASE_URL does. APP_KEY is the
// opposite — a user-set key wins, so sessions/encryption survive across deploys.
func (r *Run) applyLaravelEnvs(ctx context.Context, dep *pipeline.Deployment, env map[string]string, conns []managedDBConn) (migrate bool, err error) {
	for _, c := range conns {
		dbEnv := laravelDBEnvs(c.URL)
		if dbEnv == nil {
			continue // not a SQL engine (redis/mongo) — can't back Laravel's DB
		}
		for k, v := range dbEnv {
			env[k] = v
		}
		migrate = true
		break
	}

	key, generated, err := laravelAppKey(env["APP_KEY"])
	if err != nil {
		return false, fmt.Errorf("generate APP_KEY: %w", err)
	}
	env["APP_KEY"] = key
	if generated {
		r.logLine(ctx, dep.ID, "laravel: no APP_KEY in env vars — generated an ephemeral key for this deploy. "+
			"Set a permanent APP_KEY in the environment's env vars to keep sessions and encrypted data valid across deploys.")
	}
	return migrate, nil
}

// laravelDBEnvs maps a managed-DB connection URL to the DB_* vars stock
// config/database.php reads. Nil for non-SQL engines (Laravel's default
// connections are mysql/pgsql/sqlite/sqlsrv), so a Redis/Mongo managed DB is
// skipped rather than mis-mapped. Managed passwords are base64url (no reserved
// chars), so url.Parse round-trips the userinfo cleanly.
func laravelDBEnvs(connURL string) map[string]string {
	u, err := url.Parse(connURL)
	if err != nil || u.Host == "" {
		return nil
	}
	var conn string
	switch u.Scheme {
	case "mysql":
		conn = "mysql"
	case "postgres", "postgresql":
		conn = "pgsql"
	default:
		return nil
	}
	env := map[string]string{
		"DB_CONNECTION": conn,
		"DB_HOST":       u.Hostname(),
		"DB_DATABASE":   strings.TrimPrefix(u.Path, "/"),
	}
	if port := u.Port(); port != "" {
		env["DB_PORT"] = port
	}
	if u.User != nil {
		env["DB_USERNAME"] = u.User.Username()
		if pw, ok := u.User.Password(); ok {
			env["DB_PASSWORD"] = pw
		}
	}
	return env
}

// laravelAppKey returns the APP_KEY to inject and whether it was freshly
// generated (so the caller can advise the user). A non-empty user value is used
// as-is; otherwise a base64:<32 random bytes> key is minted — Laravel's own
// `key:generate` format.
func laravelAppKey(userSet string) (key string, generated bool, err error) {
	if strings.TrimSpace(userSet) != "" {
		return userSet, false, nil
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", false, fmt.Errorf("read random key: %w", err)
	}
	return "base64:" + base64.StdEncoding.EncodeToString(buf), true, nil
}

// migrateLaravel runs `artisan migrate` in the just-started app container and
// streams it to the deploy log. Best-effort: a migrate failure is logged and
// swallowed so a first deploy with no migrations, or a benign migrate error,
// can't tear down an otherwise-healthy release. The container is already
// serving; a real schema failure surfaces here and in the app's own errors.
func (r *Run) migrateLaravel(ctx context.Context, deploymentID, containerID string) {
	r.logLine(ctx, deploymentID, "laravel: running "+laravelMigrateCmd)
	if err := r.execInContainer(ctx, deploymentID, containerID, laravelMigrateCmd); err != nil {
		r.logLine(ctx, deploymentID, "laravel: migrate did not complete cleanly (continuing) — "+err.Error())
	}
}

// execInContainer runs a shell command in a running container and streams merged
// stdout/stderr to the deploy's run-stage log. Non-zero exit is returned as an
// error. Mirrors the maintenance-run exec path in httpx/run.go.
func (r *Run) execInContainer(ctx context.Context, deploymentID, containerID, command string) error {
	execID, err := r.docker.ContainerExecCreate(ctx, containerID, container.ExecOptions{
		Cmd:          []string{"/bin/sh", "-c", command},
		AttachStdout: true,
		AttachStderr: true,
	})
	if err != nil {
		return fmt.Errorf("exec create: %w", err)
	}
	attach, err := r.docker.ContainerExecAttach(ctx, execID.ID, container.ExecStartOptions{})
	if err != nil {
		return fmt.Errorf("exec attach: %w", err)
	}
	defer attach.Close()

	pr, pw := io.Pipe()
	go func() {
		_, _ = stdcopy.StdCopy(pw, pw, attach.Reader)
		_ = pw.Close()
	}()
	streamLines(ctx, r.logs, deploymentID, "run", pr)

	info, err := r.docker.ContainerExecInspect(ctx, execID.ID)
	if err != nil {
		return fmt.Errorf("exec inspect: %w", err)
	}
	if info.ExitCode != 0 {
		return fmt.Errorf("%q exited with code %d", command, info.ExitCode)
	}
	return nil
}
