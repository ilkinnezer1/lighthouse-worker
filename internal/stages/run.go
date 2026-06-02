package stages

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	composetypes "github.com/compose-spec/compose-go/v2/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/docker/errdefs"

	"github.com/ilkinnezer1/lighthouse-worker/internal/compose"
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

func (*Run) Name() string { return "run" }

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

func (r *Run) rollbackCompose(ctx context.Context, dep *pipeline.Deployment) error {
	if err := r.removeComposeResources(ctx, dep.ID); err != nil {
		return err
	}
	if err := r.q.DeleteServicesByDeployment(ctx, db.MustUUID(dep.ID)); err != nil {
		return fmt.Errorf("delete services: %w", err)
	}
	return nil
}

func (r *Run) removeComposeResources(ctx context.Context, deploymentID string) error {
	args := filters.NewArgs(filters.Arg("label", "com.docker.compose.project="+composeProject(deploymentID)))
	containers, err := r.docker.ContainerList(ctx, container.ListOptions{Filters: args, All: true})
	if err != nil {
		return fmt.Errorf("list compose containers: %w", err)
	}
	for _, c := range containers {
		if err := r.docker.ContainerRemove(ctx, c.ID, container.RemoveOptions{Force: true}); err != nil && !errdefs.IsNotFound(err) {
			return fmt.Errorf("container remove %s: %w", c.ID[:12], err)
		}
	}
	nets, err := r.docker.NetworkList(ctx, network.ListOptions{Filters: args})
	if err != nil {
		return fmt.Errorf("list compose networks: %w", err)
	}
	for _, n := range nets {
		if err := r.docker.NetworkRemove(ctx, n.ID); err != nil && !errdefs.IsNotFound(err) {
			return fmt.Errorf("network remove %s: %w", n.Name, err)
		}
	}
	return nil
}

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

func resolvePort(override int) int {
	if override != 0 {
		return override
	}
	return defaultPort
}

func (r *Run) runDockerfile(ctx context.Context, dep *pipeline.Deployment, o buildOverrides) error {
	if err := r.ensureNetwork(ctx); err != nil {
		return err
	}

	envs, err := r.databaseEnvs(ctx, dep)
	if err != nil {
		return err
	}

	port := resolvePort(o.Port)

	containerID, err := r.startContainer(ctx, dep, envs, port)
	if err != nil {
		return err
	}

	r.logLine(ctx, dep.ID, "container started: "+containerID[:12])
	if err := r.recordService(ctx, dep, containerID, port); err != nil {
		return err
	}
	r.supersedePrevious(ctx, dep)
	return nil
}

func (r *Run) runCompose(ctx context.Context, dep *pipeline.Deployment, o buildOverrides) error {
	project, err := compose.Parse(ctx, dep.WorkDir, composeProject(dep.ID))
	if err != nil {
		return fmt.Errorf("parse compose: %w", err)
	}
	compose.Normalize(project, dep.EnvironmentID)

	web := o.Web
	var port int
	if web != "" {
		if _, ok := project.Services[web]; !ok {
			return fmt.Errorf("buildConfig.web=%q but no such service in compose", web)
		}
		port = compose.ServicePort(project, web)
	} else {
		web, port = compose.WebService(project, defaultPort)
	}
	if web == "" {
		return fmt.Errorf("compose project has no services")
	}
	if o.Port != 0 {
		port = o.Port
	} else if port == 0 {
		port = defaultPort
	}

	if err := compose.InjectLabels(project, web, traefikLabels(dep, r.mode, port)); err != nil {
		return fmt.Errorf("inject traefik labels: %w", err)
	}
	if err := compose.AttachExternalNetwork(project, web, publicNetwork); err != nil {
		return fmt.Errorf("attach traefik network: %w", err)
	}

	envs, err := r.databaseEnvs(ctx, dep)
	if err != nil {
		return err
	}
	if len(envs) > 0 {
		compose.InjectEnv(project, envs)
	}

	if err := r.ensureNetwork(ctx); err != nil {
		return err
	}

	const modifiedName = "lighthouse-compose.yml"
	if err := compose.Write(project, filepath.Join(dep.WorkDir, modifiedName)); err != nil {
		return err
	}

	cmd := exec.CommandContext(ctx, "docker", "compose",
		"-f", modifiedName,
		"--project-name", composeProject(dep.ID),
		"up", "-d", "--no-build",
	)
	cmd.Dir = dep.WorkDir
	if err := runCmd(ctx, cmd, r.logs, dep.ID, "run"); err != nil {
		return fmt.Errorf("docker compose up: %w", err)
	}

	r.logLine(ctx, dep.ID, "compose project up: web service = "+web)
	if err := r.recordComposeServices(ctx, dep, project, web); err != nil {
		return err
	}
	r.supersedePrevious(ctx, dep)
	return nil
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

func (r *Run) recordComposeServices(ctx context.Context, dep *pipeline.Deployment, project *composetypes.Project, web string) error {
	args := filters.NewArgs(filters.Arg("label", "com.docker.compose.project="+composeProject(dep.ID)))
	containers, err := r.docker.ContainerList(ctx, container.ListOptions{Filters: args, All: true})
	if err != nil {
		return fmt.Errorf("list compose containers: %w", err)
	}

	for _, c := range containers {
		name := c.Labels["com.docker.compose.service"]
		if name == "" {
			continue
		}
		cid := c.ID
		image := c.Image
		host := name
		status := "running"
		var port *int32
		if svc, ok := project.Services[name]; ok && len(svc.Ports) > 0 {
			p := int32(svc.Ports[0].Target)
			port = &p
		}
		err := r.q.InsertService(ctx, dbgen.InsertServiceParams{
			DeploymentID: db.MustUUID(dep.ID),
			Name:         name,
			Image:        &image,
			ContainerID:  &cid,
			Host:         &host,
			Port:         port,
			IsWeb:        name == web,
			Status:       &status,
		})
		if err != nil {
			return fmt.Errorf("insert service %s: %w", name, err)
		}
	}
	return nil
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

func (r *Run) startContainer(ctx context.Context, dep *pipeline.Deployment, envs map[string]string, port int) (string, error) {
	name := containerName(dep.ID)
	cfg := &container.Config{
		Image:  ImageTag(dep.ID),
		Labels: traefikLabels(dep, r.mode, port),
		Env:    envSlice(envs),
	}
	hostCfg := &container.HostConfig{
		RestartPolicy: container.RestartPolicy{Name: "unless-stopped"},
	}
	netCfg := &network.NetworkingConfig{
		EndpointsConfig: map[string]*network.EndpointSettings{
			publicNetwork: {},
		},
	}

	created, err := r.docker.ContainerCreate(ctx, cfg, hostCfg, netCfg, nil, name)
	if err != nil {
		return "", fmt.Errorf("container create: %w", err)
	}
	if err := r.docker.ContainerStart(ctx, created.ID, container.StartOptions{}); err != nil {
		return "", fmt.Errorf("container start: %w", err)
	}
	return created.ID, nil
}

func (r *Run) recordService(ctx context.Context, dep *pipeline.Deployment, containerID string, portNum int) error {
	port := int32(portNum)
	containerName := containerName(dep.ID)
	image := ImageTag(dep.ID)
	status := "running"

	err := r.q.InsertService(ctx, dbgen.InsertServiceParams{
		DeploymentID: db.MustUUID(dep.ID),
		Name:         "web",
		Image:        &image,
		ContainerID:  &containerID,
		Host:         &containerName,
		Port:         &port,
		IsWeb:        true,
		Status:       &status,
	})
	if err != nil {
		return fmt.Errorf("insert service: %w", err)
	}
	return nil
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

