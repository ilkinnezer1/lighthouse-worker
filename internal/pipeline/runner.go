package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"

	"github.com/ilkinnezer1/lighthouse-worker/internal/db"
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

	if db.IsDeploymentTerminal(row.Status) {
		r.log.Info("already terminal — skipping",
			slog.String("deploymentId", deploymentID),
			slog.String("status", string(row.Status)),
		)
		return nil
	}

	if row.CancelRequestedAt.Valid {
		return r.cancelAtPickup(ctx, row, deploymentID)
	}

	dep := buildDeployment(row, deploymentID, r.workDirBase, r.mode, r.baseDomain)

	if err := r.loadBranches(ctx, dep); err != nil {
		return err
	}

	r.log.Info("running deployment",
		slog.String("deploymentId", deploymentID),
		slog.String("repo", dep.RepoURL),
		slog.String("branch", dep.Branch),
		slog.String("env", dep.EnvName),
	)

	return r.pipeline.Execute(ctx, dep)
}

func (r *Runner) cancelAtPickup(ctx context.Context, row dbgen.GetDeploymentForJobRow, deploymentID string) error {
	r.log.Info("cancel requested before pickup — short-circuiting",
		slog.String("deploymentId", deploymentID),
		slog.String("status", string(row.Status)),
	)
	reason := "cancelled by user request"
	err := r.q.FinishDeployment(ctx, dbgen.FinishDeploymentParams{
		ID:           row.ID,
		Status:       dbgen.DeploymentStatusCANCELLED,
		ErrorSummary: &reason,
	})
	if err != nil {
		return fmt.Errorf("mark deployment cancelled: %w", err)
	}
	payload, err := json.Marshal(map[string]any{
		"deploymentId": deploymentID,
		"stage":        "pickup",
	})
	if err != nil {
		return fmt.Errorf("marshal cancelled payload: %w", err)
	}
	if err := r.q.InsertOutboxEvent(ctx, dbgen.InsertOutboxEventParams{
		Type:    eventDeployCancelled,
		UserID:  row.TriggeredByUserID,
		Payload: payload,
	}); err != nil {
		r.log.Warn("outbox cancelled event", slog.Any("err", err))
	}
	return nil
}

// loadBranches attaches the deployment's SHA-pinned branch set, if any. The
// SOURCE branch name also overrides dep.Branch so logs and the clone agree.
func (r *Runner) loadBranches(ctx context.Context, dep *Deployment) error {
	id, err := db.ParseUUID(dep.ID)
	if err != nil {
		return err
	}
	rows, err := r.q.GetDeploymentBranches(ctx, id)
	if err != nil {
		return fmt.Errorf("load deployment branches: %w", err)
	}
	for _, row := range rows {
		sha := ""
		if row.Sha != nil {
			sha = *row.Sha
		}
		b := Branch{Merge: row.Role == dbgen.DeploymentBranchRoleMERGE, Name: row.Name, Sha: sha}
		dep.Branches = append(dep.Branches, b)
		if !b.Merge {
			dep.Branch = b.Name
		}
	}
	return nil
}

func (r *Runner) lookup(ctx context.Context, deploymentID string) (dbgen.GetDeploymentForJobRow, error) {
	id, err := db.ParseUUID(deploymentID)
	if err != nil {
		return dbgen.GetDeploymentForJobRow{}, err
	}
	row, err := r.q.GetDeploymentForJob(ctx, id)
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
		EnvironmentID:     db.UUIDString(row.EnvironmentID),
		ProjectID:         db.UUIDString(row.ProjectID),
		OwnerID:           db.UUIDString(row.OwnerID),
		TriggeredByUserID: db.UUIDString(row.TriggeredByUserID),
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
		EnvVarsEnc:        row.EnvVarsEnc,
	}
}

func branchOrDefault(row dbgen.GetDeploymentForJobRow) string {
	if row.Branch != nil && *row.Branch != "" {
		return *row.Branch
	}
	return row.DefaultBranch
}
