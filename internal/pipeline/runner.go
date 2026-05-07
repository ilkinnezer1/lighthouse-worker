package pipeline

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"

	"github.com/jackc/pgx/v5/pgtype"

	dbgen "github.com/ilkinnezer1/lighthouse-worker/internal/db/gen"
	"github.com/ilkinnezer1/lighthouse-worker/internal/logger"
	"github.com/ilkinnezer1/lighthouse-worker/internal/routing"
)

type Runner struct {
	q           *dbgen.Queries
	pipeline    *Pipeline
	log         *slog.Logger
	workDirBase string
	mode        routing.Mode
	baseDomain  string
}

func NewRunner(q *dbgen.Queries, p *Pipeline, log *slog.Logger, workDirBase string, mode routing.Mode, baseDomain string) *Runner {
	return &Runner{
		q:           q,
		pipeline:    p,
		log:         logger.Ctx(log, "Pipeline"),
		workDirBase: workDirBase,
		mode:        mode,
		baseDomain:  baseDomain,
	}
}

func (r *Runner) Run(ctx context.Context, deploymentID string) error {
	row, err := r.lookup(ctx, deploymentID)
	if err != nil {
		return err
	}

	if isTerminal(row.Status) {
		r.log.Info("already terminal — skipping",
			slog.String("deploymentId", deploymentID),
			slog.String("status", string(row.Status)),
		)
		return nil
	}

	dep := buildDeployment(row, deploymentID, r.workDirBase, r.mode, r.baseDomain)

	r.log.Info("running deployment",
		slog.String("deploymentId", deploymentID),
		slog.String("repo", dep.RepoURL),
		slog.String("branch", dep.Branch),
		slog.String("env", dep.EnvName),
	)

	return r.pipeline.Execute(ctx, dep)
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

func buildDeployment(row dbgen.GetDeploymentForJobRow, deploymentID, workDirBase string, mode routing.Mode, baseDomain string) *Deployment {
	custom := ""
	if row.CustomDomain != nil {
		custom = *row.CustomDomain
	}
	host := routing.Host(routing.HostParts{
		Mode:             mode,
		BaseDomain:       baseDomain,
		Username:         row.OwnerUsername,
		ProjectSubdomain: row.ProjectSubdomain,
		EnvSubdomain:     row.EnvSubdomain,
		CustomDomain:     custom,
	})
	scheme := "http"
	if mode.IsServer() {
		scheme = "https"
	}
	return &Deployment{
		ID:                deploymentID,
		EnvironmentID:     uuidString(row.EnvironmentID),
		ProjectID:         uuidString(row.ProjectID),
		OwnerID:           uuidString(row.OwnerID),
		TriggeredByUserID: uuidString(row.TriggeredByUserID),
		RepoURL:           row.RepoUrl,
		Branch:            branchOrDefault(row),
		EnvName:           row.EnvName,
		EnvSubdomain:      row.EnvSubdomain,
		ProjectSubdomain:  row.ProjectSubdomain,
		OwnerUsername:     row.OwnerUsername,
		CustomDomain:      custom,
		BuildConfig:       row.BuildConfig,
		WorkDir:           filepath.Join(workDirBase, deploymentID),
		Host:              host,
		Scheme:            scheme,
	}
}

func branchOrDefault(row dbgen.GetDeploymentForJobRow) string {
	if row.Branch != nil && *row.Branch != "" {
		return *row.Branch
	}
	return row.DefaultBranch
}

func isTerminal(s dbgen.DeploymentStatus) bool {
	return s == dbgen.DeploymentStatusSUCCEEDED ||
		s == dbgen.DeploymentStatusFAILED ||
		s == dbgen.DeploymentStatusCANCELLED
}

func uuidString(u pgtype.UUID) string {
	if !u.Valid {
		return ""
	}
	v, _ := u.Value()
	s, _ := v.(string)
	return s
}
