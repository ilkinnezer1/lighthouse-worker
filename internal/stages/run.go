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
	default:
		return fmt.Errorf("no Dockerfile or compose file found in %s", dep.WorkDir)
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
// overlaid on top (a managed DATABASE_URL wins over any user-set value).
func (r *Run) deployEnvs(ctx context.Context, dep *pipeline.Deployment) (map[string]string, error) {
	env, err := r.userEnvs(dep)
	if err != nil {
		return nil, err
	}
	dbEnv, err := r.databaseEnvs(ctx, dep)
	if err != nil {
		return nil, err
	}
	for k, v := range dbEnv {
		env[k] = v
	}
	return env, nil
}

// userEnvs decrypts the environment's env_vars_enc blob — an AES-256-GCM JSON
// object { "KEY": "value", ... } in Nest's CryptoService layout — into a map.
// Returns an empty (non-nil) map when unset.
func (r *Run) userEnvs(dep *pipeline.Deployment) (map[string]string, error) {
	out := map[string]string{}
	if dep.EnvVarsEnc == nil || *dep.EnvVarsEnc == "" {
		return out, nil
	}
	plain, err := r.cipher.Decrypt(*dep.EnvVarsEnc)
	if err != nil {
		return nil, fmt.Errorf("decrypt env vars: %w", err)
	}
	if err := json.Unmarshal([]byte(plain), &out); err != nil {
		return nil, fmt.Errorf("parse env vars: %w", err)
	}
	return out, nil
}

// databaseEnvs returns env vars to inject into deployment containers for any
// READY managed databases attached to the deployment's environment.
// DATABASE_URL points at the first one (deterministic by insertion order from
// the query). Each is also exposed as DATABASE_URL_<UPPER_NAME>.
func (r *Run) databaseEnvs(ctx context.Context, dep *pipeline.Deployment) (map[string]string, error) {
	envUUID := db.MustUUID(dep.EnvironmentID)
	rows, err := r.q.ListReadyDatabasesByEnvironment(ctx, envUUID)
	if err != nil {
		return nil, fmt.Errorf("list ready databases: %w", err)
	}
	if len(rows) == 0 {
		return nil, nil
	}
	env := make(map[string]string, len(rows)+1)
	for i, row := range rows {
		if row.ConnSecretEnc == nil || *row.ConnSecretEnc == "" {
			continue
		}
		conn, err := r.cipher.Decrypt(*row.ConnSecretEnc)
		if err != nil {
			return nil, fmt.Errorf("decrypt conn for %s: %w", row.Name, err)
		}
		if i == 0 {
			env["DATABASE_URL"] = conn
		}
		env["DATABASE_URL_"+strings.ToUpper(row.Name)] = conn
	}
	return env, nil
}

// supersedePrevious tears down older deployments of the same environment once the
// new one is up. Best-effort: it must never roll back the deploy that succeeded.
func (r *Run) supersedePrevious(ctx context.Context, dep *pipeline.Deployment) {
	envID, err := db.ParseUUID(dep.EnvironmentID)
	if err != nil {
		r.logLine(ctx, dep.ID, "supersede: bad environment id: "+err.Error())
		return
	}
	prior, err := r.q.ListDeploymentIDsByEnvironment(ctx, envID)
	if err != nil {
		r.logLine(ctx, dep.ID, "supersede: list deployments: "+err.Error())
		return
	}
	for _, d := range prior {
		oldID := db.UUIDString(d.ID)
		if oldID == dep.ID {
			continue
		}
		r.removeDeployment(ctx, dep.ID, oldID)
	}
}

// removeDeployment force-removes a deployment's containers and networks, covering
// both strategies: compose resources by project label, plus the Dockerfile
// single container named deploy-<id>. Best-effort — errors are ignored.
func (r *Run) removeDeployment(ctx context.Context, curID, oldID string) {
	_ = r.removeComposeResources(ctx, oldID)
	_ = r.docker.ContainerRemove(ctx, containerName(oldID), container.RemoveOptions{Force: true})
	if id, err := db.ParseUUID(oldID); err == nil {
		_ = r.q.DeleteServicesByDeployment(ctx, id)
	}
	r.logLine(ctx, curID, "superseded previous deployment "+oldID)
}

func (r *Run) ensureNetwork(ctx context.Context) error {
	_, err := r.docker.NetworkCreate(ctx, publicNetwork, network.CreateOptions{
		Driver: "bridge",
	})
	if err == nil || isAlreadyExists(err) {
		return nil
	}
	return fmt.Errorf("create network: %w", err)
}

func (r *Run) logLine(ctx context.Context, deploymentID, line string) {
	_ = r.logs.WriteLine(ctx, deploymentID, "run", line)
}

func resolvePort(override int) int {
	if override != 0 {
		return override
	}
	return defaultPort
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
