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
