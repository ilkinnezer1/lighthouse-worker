// Package teardown handles `jobs:teardown` messages emitted by Nest on
// `pull_request.closed`.
package teardown

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/client"
	"github.com/docker/docker/errdefs"

	"github.com/ilkinnezer1/lighthouse-worker/internal/db"
	dbgen "github.com/ilkinnezer1/lighthouse-worker/internal/db/gen"
	"github.com/ilkinnezer1/lighthouse-worker/internal/logger"
	"github.com/ilkinnezer1/lighthouse-worker/internal/stages"
)

const eventEnvironmentTornDown = "environment.torn_down"

type Runner struct {
	docker      *client.Client
	q           *dbgen.Queries
	log         *slog.Logger
	workDirBase string
}

func New(docker *client.Client, q *dbgen.Queries, log *slog.Logger, workDirBase string) *Runner {
	return &Runner{
		docker:      docker,
		q:           q,
		log:         logger.Ctx(log, "Teardown"),
		workDirBase: workDirBase,
	}
}

func (r *Runner) Run(ctx context.Context, environmentID string) error {
	envUUID, err := db.ParseUUID(environmentID)
	if err != nil {
		return err
	}

	env, err := r.q.GetEnvironmentForTeardown(ctx, envUUID)
	if err != nil {
		return fmt.Errorf("lookup environment: %w", err)
	}

	log := r.log.With(
		slog.String("environmentId", environmentID),
		slog.String("env", env.EnvName),
	)

	if !env.Active {
		log.Info("already inactive — skipping")
		return nil
	}

	deployments, err := r.q.ListDeploymentIDsByEnvironment(ctx, envUUID)
	if err != nil {
		return fmt.Errorf("list deployments: %w", err)
	}
	// Phase 5 cancellation pubsub is the proper place to interrupt in-flight builds.
	// For now we just flag them; the still-running build may produce orphaned artifacts.
	for _, d := range deployments {
		if !db.IsDeploymentTerminal(d.Status) {
			log.Warn("deployment still in flight at teardown",
				slog.String("deploymentId", db.UUIDString(d.ID)),
				slog.String("status", string(d.Status)),
			)
		}
	}

	services, err := r.q.ListServicesByEnvironment(ctx, envUUID)
	if err != nil {
		return fmt.Errorf("list services: %w", err)
	}

	for _, s := range services {
		r.removeContainer(ctx, log, s)
		r.removeImage(ctx, log, s)
		if err := r.q.MarkServiceTornDown(ctx, s.ID); err != nil {
			log.Error("mark service torn_down failed",
				slog.String("serviceId", db.UUIDString(s.ID)),
				slog.Any("err", err),
			)
		}
	}

	for _, d := range deployments {
		r.removeWorkDir(log, db.UUIDString(d.ID))
	}

	r.removeVolumes(ctx, log, environmentID)

	if err := r.q.DeactivateEnvironment(ctx, envUUID); err != nil {
		return fmt.Errorf("deactivate environment: %w", err)
	}

	if err := r.emit(ctx, env, environmentID); err != nil {
		log.Warn("outbox torn_down event failed", slog.Any("err", err))
	}

	log.Info("environment torn down",
		slog.Int("services", len(services)),
		slog.Int("deployments", len(deployments)),
	)
	return nil
}

// removeVolumes deletes the environment's persistent volumes. Teardown destroys
// the environment itself (a closed PR's preview env), so leaving them behind
// would grow disk without bound. Each removal is logged by name, because this is
// the one step that destroys user data rather than rebuildable artifacts.
func (r *Runner) removeVolumes(ctx context.Context, log *slog.Logger, environmentID string) {
	prefix := stages.EnvVolumePrefix(environmentID)
	list, err := r.docker.VolumeList(ctx, volume.ListOptions{
		Filters: filters.NewArgs(filters.Arg("name", prefix)),
	})
	if err != nil {
		log.Error("list volumes failed", slog.Any("err", err))
		return
	}
	for _, v := range list.Volumes {
		if !strings.HasPrefix(v.Name, prefix) {
			continue
		}
		if err := r.docker.VolumeRemove(ctx, v.Name, true); err != nil && !isNotFound(err) {
			log.Error("remove volume failed", slog.String("volume", v.Name), slog.Any("err", err))
			continue
		}
		log.Info("removed volume", slog.String("volume", v.Name))
	}
}

func (r *Runner) removeContainer(ctx context.Context, log *slog.Logger, s dbgen.Service) {
	if s.ContainerID == nil || *s.ContainerID == "" {
		return
	}
	err := r.docker.ContainerRemove(ctx, *s.ContainerID, container.RemoveOptions{Force: true})
	if err != nil && !isNotFound(err) {
		log.Warn("container remove failed",
			slog.String("containerId", *s.ContainerID),
			slog.Any("err", err),
		)
	}
}

func (r *Runner) removeImage(ctx context.Context, log *slog.Logger, s dbgen.Service) {
	if s.Image == nil || *s.Image == "" {
		return
	}
	_, err := r.docker.ImageRemove(ctx, *s.Image, image.RemoveOptions{Force: true, PruneChildren: true})
	if err != nil && !isNotFound(err) {
		log.Warn("image remove failed", slog.String("image", *s.Image), slog.Any("err", err))
	}
}

// Build containers bind-mount the work dir and write root-owned node_modules /
// vendor into it, so a worker running as an ordinary user gets EPERM here and
// must say so rather than leaving gigabytes behind under a warning.
func (r *Runner) removeWorkDir(log *slog.Logger, deploymentID string) {
	freed, residual, err := stages.ReclaimWorkDir(r.workDirBase, deploymentID)
	if err != nil {
		log.Warn("workdir not reclaimed",
			slog.String("deploymentId", deploymentID),
			slog.String("residual", stages.HumanBytes(residual)),
			slog.Any("err", err),
		)
		return
	}
	if freed > 0 {
		log.Info("workdir reclaimed",
			slog.String("deploymentId", deploymentID),
			slog.String("freed", stages.HumanBytes(freed)),
		)
	}
}

func (r *Runner) emit(ctx context.Context, env dbgen.GetEnvironmentForTeardownRow, environmentID string) error {
	payload := map[string]any{
		"environmentId": environmentID,
		"projectId":     db.UUIDString(env.ProjectID),
		"name":          env.EnvName,
		"subdomain":     env.EnvSubdomain,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}
	return r.q.InsertOutboxEvent(ctx, dbgen.InsertOutboxEventParams{
		Type:    eventEnvironmentTornDown,
		UserID:  env.CreatedByUserID,
		Payload: body,
	})
}

func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	if errdefs.IsNotFound(err) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "No such container") || strings.Contains(msg, "No such image")
}
