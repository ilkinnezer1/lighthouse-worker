package stages

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"

	composetypes "github.com/compose-spec/compose-go/v2/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/errdefs"

	"github.com/ilkinnezer1/lighthouse-worker/internal/compose"
	"github.com/ilkinnezer1/lighthouse-worker/internal/db"
	dbgen "github.com/ilkinnezer1/lighthouse-worker/internal/db/gen"
	"github.com/ilkinnezer1/lighthouse-worker/internal/pipeline"
)

func (r *Run) runCompose(ctx context.Context, dep *pipeline.Deployment, o buildOverrides) error {
	project, err := compose.Parse(ctx, dep.WorkDir, composeProject(dep.ID))
	if err != nil {
		return fmt.Errorf("parse compose: %w", err)
	}
	compose.Normalize(project, dep.EnvironmentID)

	web, port, err := resolveWeb(project, o)
	if err != nil {
		return err
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

// resolveWeb picks the public-facing compose service and its port. An explicit
// buildConfig.web must name a real service; otherwise the web service is
// auto-detected. buildConfig.port, when set, overrides the resolved port.
func resolveWeb(project *composetypes.Project, o buildOverrides) (web string, port int, err error) {
	if o.Web != "" {
		if _, ok := project.Services[o.Web]; !ok {
			return "", 0, fmt.Errorf("buildConfig.web=%q but no such service in compose", o.Web)
		}
		web, port = o.Web, compose.ServicePort(project, o.Web)
	} else {
		web, port = compose.WebService(project, defaultPort)
	}
	if web == "" {
		return "", 0, fmt.Errorf("compose project has no services")
	}
	if o.Port != 0 {
		port = o.Port
	} else if port == 0 {
		port = defaultPort
	}
	return web, port, nil
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
