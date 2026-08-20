// Package sweeper reclaims disk from deployments the retire stage never sees:
// failed and cancelled builds, and orphans left by a worker that died mid-deploy.
package sweeper

import (
	"context"
	"log/slog"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ilkinnezer1/lighthouse-worker/internal/db"
	dbgen "github.com/ilkinnezer1/lighthouse-worker/internal/db/gen"
	"github.com/ilkinnezer1/lighthouse-worker/internal/logger"
	"github.com/ilkinnezer1/lighthouse-worker/internal/stages"
)

const (
	DefaultInterval  = time.Hour
	DefaultRetention = 7 * 24 * time.Hour
)

type Sweeper struct {
	docker      *client.Client
	q           *dbgen.Queries
	log         *slog.Logger
	workDirBase string
	interval    time.Duration
	retention   time.Duration
}

func New(docker *client.Client, q *dbgen.Queries, log *slog.Logger, workDirBase string, interval, retention time.Duration) *Sweeper {
	return &Sweeper{
		docker:      docker,
		q:           q,
		log:         logger.Ctx(log, "Sweeper"),
		workDirBase: workDirBase,
		interval:    interval,
		retention:   retention,
	}
}

func (s *Sweeper) Run(ctx context.Context) error {
	s.log.Info("sweeping",
		slog.Duration("interval", s.interval),
		slog.Duration("retention", s.retention),
	)
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	s.sweep(ctx)
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			s.sweep(ctx)
		}
	}
}

func (s *Sweeper) sweep(ctx context.Context) {
	cutoff := time.Now().Add(-s.retention)
	rows, err := s.q.ListReclaimableDeployments(ctx, pgtype.Timestamptz{Time: cutoff, Valid: true})
	if err != nil {
		s.log.Error("list reclaimable deployments failed", slog.Any("err", err))
		return
	}
	if len(rows) == 0 {
		return
	}

	running, err := s.docker.ContainerList(ctx, container.ListOptions{})
	if err != nil {
		// Fail closed: without the running set nothing proves a target isn't serving.
		s.log.Error("list running containers failed — sweep skipped", slog.Any("err", err))
		return
	}

	ids := make([]string, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, db.UUIDString(r.ID))
	}
	targets := Eligible(ids, LiveDeploymentIDs(running))

	var freed, stuck int64
	var images, reclaimed, incomplete int
	for _, id := range targets {
		if ctx.Err() != nil {
			return
		}
		res := stages.ReclaimDeployment(ctx, s.docker, s.workDirBase, id, s.recordedImages(ctx, id))
		freed += res.WorkDirBytes
		images += len(res.Images)
		if !res.Complete() {
			incomplete++
			stuck += res.ResidualBytes
			s.log.Warn("deployment not fully reclaimed",
				slog.String("deploymentId", id),
				slog.String("detail", res.Summary()),
			)
			continue
		}
		if res.Freed() {
			reclaimed++
		}
		if uid, parseErr := db.ParseUUID(id); parseErr == nil {
			_ = s.q.DeleteServicesByDeployment(ctx, uid)
		}
	}

	if incomplete > 0 {
		s.log.Warn("sweep finished with unreclaimed deployments",
			slog.Int("reclaimed", reclaimed),
			slog.Int("incomplete", incomplete),
			slog.String("freed", stages.HumanBytes(freed)),
			slog.String("stillOnDisk", stages.HumanBytes(stuck)),
		)
		return
	}
	if reclaimed == 0 {
		return
	}
	s.log.Info("sweep complete",
		slog.Int("reclaimed", reclaimed),
		slog.Int("images", images),
		slog.String("freed", stages.HumanBytes(freed)),
	)
}

func (s *Sweeper) recordedImages(ctx context.Context, id string) []string {
	uid, err := db.ParseUUID(id)
	if err != nil {
		return nil
	}
	svcs, err := s.q.ListServicesByDeployment(ctx, uid)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(svcs))
	for _, sv := range svcs {
		if sv.Image != nil {
			out = append(out, *sv.Image)
		}
	}
	return out
}
