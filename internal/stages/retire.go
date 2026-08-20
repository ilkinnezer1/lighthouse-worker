package stages

import (
	"context"

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
	docker      *client.Client
	q           *dbgen.Queries
	logs        *logstream.Writer
	workDirBase string
}

func NewRetire(docker *client.Client, q *dbgen.Queries, logs *logstream.Writer, workDirBase string) *Retire {
	return &Retire{docker: docker, q: q, logs: logs, workDirBase: workDirBase}
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
		if oldID == "" || oldID == dep.ID {
			continue
		}
		r.remove(ctx, dep.ID, oldID)
	}
	return nil
}

// remove reclaims a prior deployment's containers, images and work tree. The
// service rows name the images to remove, so they are read before the delete.
func (r *Retire) remove(ctx context.Context, curID, oldID string) {
	oldUUID, err := db.ParseUUID(oldID)
	if err != nil {
		return
	}
	svcs, err := r.q.ListServicesByDeployment(ctx, oldUUID)
	if err != nil {
		r.logLine(ctx, curID, "retire: list services for "+oldID+": "+err.Error())
	}
	recorded := make([]string, 0, len(svcs))
	for _, s := range svcs {
		if s.Image != nil {
			recorded = append(recorded, *s.Image)
		}
	}

	freed := ReclaimDeployment(ctx, r.docker, r.workDirBase, oldID, recorded)
	// Rows outlive a partial reclaim so the next sweep can still find the images.
	if freed.Complete() {
		_ = r.q.DeleteServicesByDeployment(ctx, oldUUID)
	}
	r.logLine(ctx, curID, "superseded previous deployment "+oldID+" — "+freed.Summary())
}

func (*Retire) Rollback(context.Context, *pipeline.Deployment) error { return nil }

func (r *Retire) logLine(ctx context.Context, deploymentID, line string) {
	_ = r.logs.WriteLine(ctx, deploymentID, "retire_old_deployment", line)
}
