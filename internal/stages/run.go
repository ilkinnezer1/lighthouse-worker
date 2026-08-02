package stages

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/docker/errdefs"

	"github.com/ilkinnezer1/lighthouse-worker/internal/crypto"
	"github.com/ilkinnezer1/lighthouse-worker/internal/db"
	dbgen "github.com/ilkinnezer1/lighthouse-worker/internal/db/gen"
	"github.com/ilkinnezer1/lighthouse-worker/internal/framework"
	"github.com/ilkinnezer1/lighthouse-worker/internal/logstream"
	"github.com/ilkinnezer1/lighthouse-worker/internal/pipeline"
	"github.com/ilkinnezer1/lighthouse-worker/internal/routing"
)

const (
	publicNetwork = "lighthouse-public"
	defaultPort   = 80
	// Must match certificatesresolvers.le.* in docker-compose.dev.yml.
	certResolver = "le"
)

type Run struct {
	docker *client.Client
	logs   *logstream.Writer
	q      *dbgen.Queries
	cipher *crypto.Cipher
	mode   routing.Mode
}

func NewRun(docker *client.Client, logs *logstream.Writer, q *dbgen.Queries, cipher *crypto.Cipher, mode routing.Mode) *Run {
	return &Run{docker: docker, logs: logs, q: q, cipher: cipher, mode: mode}
}

func (*Run) Name() string { return "run" }

// Run starts the deployment, dispatching on the detected build strategy.
// The Dockerfile and compose paths live in run_dockerfile.go / run_compose.go.
func (r *Run) Run(ctx context.Context, dep *pipeline.Deployment) error {
	o := parseBuildOverrides(dep.BuildConfig)
	switch detectStrategy(dep.WorkDir, o.Strategy) {
	case StrategyDockerfile:
		return r.runDockerfile(ctx, dep, o)
	case StrategyCompose:
		return r.runCompose(ctx, dep, o)
	case StrategyNixpacks:
		return r.runNixpacks(ctx, dep, o)
	case StrategyGenerated:
		return r.runGenerated(ctx, dep, o)
	case StrategyBuildpacks:
		return r.runBuildpacks(ctx, dep, o)
	case StrategyRailpack:
		return r.runRailpack(ctx, dep, o)
	case StrategyImage:
		return r.runPrebuiltImage(ctx, dep, o)
	default:
		return fmt.Errorf("unknown build strategy for %s", dep.WorkDir)
	}
}

func (r *Run) Rollback(ctx context.Context, dep *pipeline.Deployment) error {
	if detectStrategy(dep.WorkDir, parseBuildOverrides(dep.BuildConfig).Strategy) == StrategyCompose {
		return r.rollbackCompose(ctx, dep)
	}
	name := containerName(dep.ID)
	err := r.docker.ContainerRemove(ctx, name, container.RemoveOptions{Force: true})
	if err != nil && !errdefs.IsNotFound(err) {
		return fmt.Errorf("container remove: %w", err)
	}
	if err := r.q.DeleteServicesByDeployment(ctx, db.MustUUID(dep.ID)); err != nil {
		return fmt.Errorf("delete services: %w", err)
	}
	return nil
}

// deployEnvs is the full env var set injected into deployment containers: the
// environment's user-defined vars, with lighthouse-managed database creds
// overlaid on top (a managed DATABASE_URL wins over any user-set value). For a
// Laravel app (re-detected here because dep.Framework is cleared post-clone) it
// also injects the DB_* / APP_KEY vars stock Laravel needs — see laravel.go —
// and reports whether a post-start `artisan migrate` should run.
func (r *Run) deployEnvs(ctx context.Context, dep *pipeline.Deployment, o buildOverrides) (envs map[string]string, migrate bool, err error) {
	env, err := r.userEnvs(dep)
	if err != nil {
		return nil, false, err
	}
	conns, err := r.readyDBConns(ctx, dep)
	if err != nil {
		return nil, false, err
	}
	for k, v := range dbURLEnvs(conns) {
		env[k] = v
	}
	if framework.Detect(buildSource(dep, o)) == frameworkLaravel {
		migrate, err = r.applyLaravelEnvs(ctx, dep, env, conns)
		if err != nil {
			return nil, false, err
		}
	}
	return env, migrate, nil
}

// userEnvs decrypts the environment's env_vars_enc blob — an AES-256-GCM JSON
// object { "KEY": "value", ... } in Nest's CryptoService layout — into a map.
// Returns an empty (non-nil) map when unset.
func (r *Run) userEnvs(dep *pipeline.Deployment) (map[string]string, error) {
	return decodeUserEnvs(r.cipher, dep.EnvVarsEnc)
}

// decodeUserEnvs decrypts and parses an env_vars_enc blob into a map. Returns an
// empty (non-nil) map when the blob is unset.
func decodeUserEnvs(cipher *crypto.Cipher, enc *string) (map[string]string, error) {
	out := map[string]string{}
	if enc == nil || *enc == "" {
		return out, nil
	}
	plain, err := cipher.Decrypt(*enc)
	if err != nil {
		return nil, fmt.Errorf("decrypt env vars: %w", err)
	}
	if err := json.Unmarshal([]byte(plain), &out); err != nil {
		return nil, fmt.Errorf("parse env vars: %w", err)
	}
	return out, nil
}

// managedDBConn is a READY managed database's name and decrypted connection URL.
type managedDBConn struct {
	Name string
	URL  string
}

// readyDBConns returns the decrypted connection URL of every READY managed
// database on the deployment's environment, in the query's insertion order.
func (r *Run) readyDBConns(ctx context.Context, dep *pipeline.Deployment) ([]managedDBConn, error) {
	rows, err := r.q.ListReadyDatabasesByEnvironment(ctx, db.MustUUID(dep.EnvironmentID))
	if err != nil {
		return nil, fmt.Errorf("list ready databases: %w", err)
	}
	conns := make([]managedDBConn, 0, len(rows))
	for _, row := range rows {
		if row.ConnSecretEnc == nil || *row.ConnSecretEnc == "" {
			continue
		}
		connURL, err := r.cipher.Decrypt(*row.ConnSecretEnc)
		if err != nil {
			return nil, fmt.Errorf("decrypt conn for %s: %w", row.Name, err)
		}
		conns = append(conns, managedDBConn{Name: row.Name, URL: connURL})
	}
	return conns, nil
}

// dbURLEnvs exposes each managed DB as DATABASE_URL_<UPPER_NAME>, with the first
// also bound to the bare DATABASE_URL. Nil when no managed DB is attached.
func dbURLEnvs(conns []managedDBConn) map[string]string {
	if len(conns) == 0 {
		return nil
	}
	env := make(map[string]string, len(conns)+1)
	for i, c := range conns {
		if i == 0 {
			env["DATABASE_URL"] = c.URL
		}
		env["DATABASE_URL_"+strings.ToUpper(c.Name)] = c.URL
	}
	return env
}

func (r *Run) ensureNetwork(ctx context.Context, name string) error {
	_, err := r.docker.NetworkCreate(ctx, name, network.CreateOptions{
		Driver: "bridge",
	})
	if err == nil || isAlreadyExists(err) {
		return nil
	}
	return fmt.Errorf("create network %s: %w", name, err)
}

// envNetwork is the per-environment network shared by a deployment's containers
// and any managed databases in the same environment (so the app can resolve
// db-<id> by name). Must match the network the database package provisions onto.
func envNetwork(environmentID string) string {
	return "env-" + environmentID
}

func (r *Run) logLine(ctx context.Context, deploymentID, line string) {
	_ = r.logs.WriteLine(ctx, deploymentID, "run", line)
}

func traefikLabels(dep *pipeline.Deployment, mode routing.Mode, port int) map[string]string {
	router := "deploy-" + dep.ID
	rule := fmt.Sprintf("Host(`%s`)", dep.Host)
	entrypoint := "web"
	if mode.IsServer() {
		entrypoint = "websecure"
	}
	labels := map[string]string{
		"traefik.enable": "true",
		fmt.Sprintf("traefik.http.routers.%s.rule", router):                      rule,
		fmt.Sprintf("traefik.http.routers.%s.entrypoints", router):               entrypoint,
		fmt.Sprintf("traefik.http.services.%s.loadbalancer.server.port", router): fmt.Sprintf("%d", port),
	}
	if mode.IsServer() {
		labels[fmt.Sprintf("traefik.http.routers.%s.tls", router)] = "true"
		labels[fmt.Sprintf("traefik.http.routers.%s.tls.certresolver", router)] = certResolver
	}
	return labels
}

func containerName(deploymentID string) string {
	return "deploy-" + deploymentID
}

func envSlice(m map[string]string) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for k, v := range m {
		out = append(out, k+"="+v)
	}
	return out
}

func isAlreadyExists(err error) bool {
	if err == nil {
		return false
	}
	if errdefs.IsConflict(err) {
		return true
	}
	return strings.Contains(err.Error(), "already exists")
}
