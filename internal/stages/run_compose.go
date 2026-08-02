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
	"github.com/ilkinnezer1/lighthouse-worker/internal/db"
	dbgen "github.com/ilkinnezer1/lighthouse-worker/internal/db/gen"
	"github.com/ilkinnezer1/lighthouse-worker/internal/pipeline"
)

func (r *Run) runCompose(ctx context.Context, dep *pipeline.Deployment, o buildOverrides) error {
	if o.CPULimit > 0 || o.MemoryMB > 0 {
		r.logLine(ctx, dep.ID, "note: cpuLimit/memoryMb are not applied to compose deploys — set deploy.resources.limits per service in your compose file")
	}
	project, err := compose.ParseAt(ctx, dep.WorkDir, o.ComposePath, composeProject(dep.ID))
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

	// The compose web service is user-defined, so the auto-migrate step (which
	// execs into a known single container) is limited to the image path; a
	// Laravel compose deploy still gets DB_* / APP_KEY via deployEnvs.
	envs, _, err := r.deployEnvs(ctx, dep, o)
	if err != nil {
		return err
	}
	if len(envs) > 0 {
		compose.InjectEnv(project, envs)
	}

	if err := r.ensureNetwork(ctx, publicNetwork); err != nil {
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
	if err := r.connectEnvNetwork(ctx, dep, web); err != nil {
		return err
	}
	return nil
}

// connectEnvNetwork joins the deployment's non-web service containers to the
// per-environment network so they can resolve managed databases (db-<id>) by
// name. The web/Traefik service is left untouched to avoid Traefik re-resolving
// its backend to the wrong network IP.
func (r *Run) connectEnvNetwork(ctx context.Context, dep *pipeline.Deployment, web string) error {
	envNet := envNetwork(dep.EnvironmentID)
	if err := r.ensureNetwork(ctx, envNet); err != nil {
		return err
	}
	args := filters.NewArgs(filters.Arg("label", "com.docker.compose.project="+composeProject(dep.ID)))
	containers, err := r.docker.ContainerList(ctx, container.ListOptions{Filters: args, All: true})
	if err != nil {
		return fmt.Errorf("list compose containers: %w", err)
	}
	for _, c := range containers {
		if c.Labels["com.docker.compose.service"] == web {
			continue
		}
		if err := r.docker.NetworkConnect(ctx, envNet, c.ID, nil); err != nil && !isAlreadyInNetwork(err) {
			return fmt.Errorf("connect %s to %s: %w", c.ID[:12], envNet, err)
		}
	}
	return nil
}

func isAlreadyInNetwork(err error) bool {
	return err != nil && strings.Contains(err.Error(), "already exists in network")
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
	if err := removeComposeResources(ctx, r.docker, dep.ID); err != nil {
		return err
	}
	if err := r.q.DeleteServicesByDeployment(ctx, db.MustUUID(dep.ID)); err != nil {
		return fmt.Errorf("delete services: %w", err)
	}
	return nil
}

// removeComposeResources force-removes the containers and networks of a compose
// deployment, matched by its compose project label. Shared by the run stage's
// rollback and the retire stage's supersede.
func removeComposeResources(ctx context.Context, docker *client.Client, deploymentID string) error {
	args := filters.NewArgs(filters.Arg("label", "com.docker.compose.project="+composeProject(deploymentID)))
	containers, err := docker.ContainerList(ctx, container.ListOptions{Filters: args, All: true})
	if err != nil {
		return fmt.Errorf("list compose containers: %w", err)
	}
	for _, c := range containers {
		if err := docker.ContainerRemove(ctx, c.ID, container.RemoveOptions{Force: true}); err != nil && !errdefs.IsNotFound(err) {
			return fmt.Errorf("container remove %s: %w", c.ID[:12], err)
		}
	}
	nets, err := docker.NetworkList(ctx, network.ListOptions{Filters: args})
	if err != nil {
		return fmt.Errorf("list compose networks: %w", err)
	}
	for _, n := range nets {
		if err := docker.NetworkRemove(ctx, n.ID); err != nil && !errdefs.IsNotFound(err) {
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
