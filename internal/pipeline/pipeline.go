package pipeline

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgtype"

	dbgen "github.com/ilkinnezer1/lighthouse-worker/internal/db/gen"
	"github.com/ilkinnezer1/lighthouse-worker/internal/logger"
)

// Runner executes one deployment from queue pickup to completion.
type Runner struct {
	q   *dbgen.Queries
	log *slog.Logger
}

func New(q *dbgen.Queries, log *slog.Logger) *Runner {
	return &Runner{
		q:   q,
		log: logger.Ctx(log, "Pipeline"),
	}
}

// Run is the queue.Handler for one job. Returning nil triggers XACK.
func (r *Runner) Run(ctx context.Context, deploymentID string) error {
	row, err := r.lookup(ctx, deploymentID)
	if err != nil {
		return err
	}

	r.log.Info("picked up job",
		slog.String("deploymentId", deploymentID),
		slog.String("repo", row.RepoUrl),
		slog.String("branch", branchOrDefault(row)),
		slog.String("subdomain", row.Subdomain),
	)
	return nil
}

func (r *Runner) lookup(ctx context.Context, deploymentID string) (dbgen.GetDeploymentForJobRow, error) {
	var depUUID pgtype.UUID
	if err := depUUID.Scan(deploymentID); err != nil {
		return dbgen.GetDeploymentForJobRow{}, fmt.Errorf("parse uuid %q: %w", deploymentID, err)
	}
	row, err := r.q.GetDeploymentForJob(ctx, depUUID)
	if err != nil {
		return dbgen.GetDeploymentForJobRow{}, fmt.Errorf("lookup deployment: %w", err)
	}
	return row, nil
}

// branchOrDefault returns the environment-specific branch if set.
func branchOrDefault(row dbgen.GetDeploymentForJobRow) string {
	if row.Branch != nil && *row.Branch != "" {
		return *row.Branch
	}
	return row.DefaultBranch
}
