package stages

import (
	"context"
	"fmt"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"

	"github.com/ilkinnezer1/lighthouse-worker/internal/db"
	dbgen "github.com/ilkinnezer1/lighthouse-worker/internal/db/gen"
	"github.com/ilkinnezer1/lighthouse-worker/internal/pipeline"
)

func (r *Run) runDockerfile(ctx context.Context, dep *pipeline.Deployment, o buildOverrides) error {
	envs, migrate, err := r.deployEnvs(ctx, dep, o)
	if err != nil {
		return err
	}
	return r.runImage(ctx, dep, envs, resolvePort(o.Port), migrate)
}

// runImage starts the single deploy container from a prebuilt image tagged
// ImageTag(dep.ID): network wiring, env injection, and web-service recording.
// Shared by the dockerfile and nixpacks strategies (nixpacks produces the same
// image tag), so both flow through one run/health/retire/rollback path. migrate
// runs `artisan migrate` once the container is up (Laravel + SQL managed DB).
func (r *Run) runImage(ctx context.Context, dep *pipeline.Deployment, envs map[string]string, port int, migrate bool) error {
	if err := r.ensureNetwork(ctx, publicNetwork); err != nil {
		return err
	}
	if err := r.ensureNetwork(ctx, envNetwork(dep.EnvironmentID)); err != nil {
		return err
	}

	containerID, err := r.startContainer(ctx, dep, envs, port)
	if err != nil {
		return err
	}

	r.logLine(ctx, dep.ID, "container started: "+containerID[:12])
	if err := r.recordService(ctx, dep, containerID, port); err != nil {
		return err
	}
	if migrate {
		r.migrateLaravel(ctx, dep.ID, containerID)
	}
	return nil
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
			publicNetwork:                 {},
			envNetwork(dep.EnvironmentID): {},
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
