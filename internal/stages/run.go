package stages

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/docker/errdefs"
	"github.com/jackc/pgx/v5/pgtype"

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
	mode   routing.Mode
}

func NewRun(docker *client.Client, logs *logstream.Writer, q *dbgen.Queries, mode routing.Mode) *Run {
	return &Run{docker: docker, logs: logs, q: q, mode: mode}
}

func (*Run) Name() string { return "run" }

func (r *Run) Run(ctx context.Context, dep *pipeline.Deployment) error {
	if err := r.ensureNetwork(ctx); err != nil {
		return err
	}

	containerID, err := r.startContainer(ctx, dep)
	if err != nil {
		return err
	}

	r.logLine(ctx, dep.ID, "container started: "+containerID[:12])
	return r.recordService(ctx, dep, containerID)
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

func (r *Run) startContainer(ctx context.Context, dep *pipeline.Deployment) (string, error) {
	name := containerName(dep.ID)
	cfg := &container.Config{
		Image:  ImageTag(dep.ID),
		Labels: traefikLabels(dep, r.mode),
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

func (r *Run) recordService(ctx context.Context, dep *pipeline.Deployment, containerID string) error {
	port := int32(defaultPort)
	containerName := containerName(dep.ID)
	image := ImageTag(dep.ID)
	status := "running"

	err := r.q.InsertService(ctx, dbgen.InsertServiceParams{
		DeploymentID: deploymentUUID(dep.ID),
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

func traefikLabels(dep *pipeline.Deployment, mode routing.Mode) map[string]string {
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
		fmt.Sprintf("traefik.http.services.%s.loadbalancer.server.port", router): fmt.Sprintf("%d", defaultPort),
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

func isAlreadyExists(err error) bool {
	if err == nil {
		return false
	}
	if errdefs.IsConflict(err) {
		return true
	}
	return strings.Contains(err.Error(), "already exists")
}

func deploymentUUID(id string) pgtype.UUID {
	var u pgtype.UUID
	_ = u.Scan(id)
	return u
}

var _ = errors.New
