package stages

import (
	"context"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"

	"github.com/ilkinnezer1/lighthouse-worker/internal/db"
	dbgen "github.com/ilkinnezer1/lighthouse-worker/internal/db/gen"
	"github.com/ilkinnezer1/lighthouse-worker/internal/logstream"
	"github.com/ilkinnezer1/lighthouse-worker/internal/pipeline"
)

// Retire supersedes prior deployments of the same environment. It runs only
// after the new deployment passed its health check, so a failed deploy never
// reaches this stage and the previous one keeps serving. Best-effort: it must
// never fail a deploy that is already live and healthy.
type Retire struct {
	docker *client.Client
	q      *dbgen.Queries
	logs   *logstream.Writer
}

func NewRetire(docker *client.Client, q *dbgen.Queries, logs *logstream.Writer) *Retire {
	return &Retire{docker: docker, q: q, logs: logs}
}

func (*Retire) Name() string { return "retire_old_deployment" }

func (r *Retire) Run(ctx context.Context, dep *pipeline.Deployment) error {
	envID, err := db.ParseUUID(dep.EnvironmentID)
	if err != nil {
		r.logLine(ctx, dep.ID, "retire: bad environment id: "+err.Error())
		return nil
	}
	prior, err := r.q.ListDeploymentIDsByEnvironment(ctx, envID)
	if err != nil {
		r.logLine(ctx, dep.ID, "retire: list deployments: "+err.Error())
		return nil
	}
	for _, d := range prior {
		oldID := db.UUIDString(d.ID)
		if oldID == dep.ID {
			continue
		}
		r.remove(ctx, dep.ID, oldID)
	}
	return nil
}

// remove force-removes a prior deployment's containers and networks, covering
// both strategies: compose resources by project label, plus the Dockerfile
// single container deploy-<id>. Best-effort — errors are ignored.
func (r *Retire) remove(ctx context.Context, curID, oldID string) {
	_ = removeComposeResources(ctx, r.docker, oldID)
	_ = r.docker.ContainerRemove(ctx, containerName(oldID), container.RemoveOptions{Force: true})
	if id, err := db.ParseUUID(oldID); err == nil {
		_ = r.q.DeleteServicesByDeployment(ctx, id)
	}
	r.logLine(ctx, curID, "superseded previous deployment "+oldID)
}

func (*Retire) Rollback(context.Context, *pipeline.Deployment) error { return nil }

func (r *Retire) logLine(ctx context.Context, deploymentID, line string) {
	_ = r.logs.WriteLine(ctx, deploymentID, "retire_old_deployment", line)
}
