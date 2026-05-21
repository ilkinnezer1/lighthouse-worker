package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/ilkinnezer1/lighthouse-worker/internal/cancelbus"
	"github.com/ilkinnezer1/lighthouse-worker/internal/db"
	dbgen "github.com/ilkinnezer1/lighthouse-worker/internal/db/gen"
	"github.com/ilkinnezer1/lighthouse-worker/internal/logger"
)

type Pipeline struct {
	q         *dbgen.Queries
	log       *slog.Logger
	stages    []Stage
	cancelBus *cancelbus.Listener
}

func NewPipeline(q *dbgen.Queries, log *slog.Logger, stages []Stage, cancelBus *cancelbus.Listener) *Pipeline {
	return &Pipeline{
		q:         q,
		log:       logger.Ctx(log, "Pipeline"),
		stages:    stages,
		cancelBus: cancelBus,
	}
}

// Execute Status writes happen before the work so a crash mid-stage leaves the row
// in the correct in-flight state for the reaper.
func (p *Pipeline) Execute(ctx context.Context, dep *Deployment) error {
	workCtx, cancelWork := context.WithCancel(ctx)
	defer cancelWork()

	go runHeartbeat(workCtx, p.q, dep.ID, p.log)
	go p.cancelBus.Listen(workCtx, dep.ID, cancelWork)

	id := db.MustUUID(dep.ID)
	if err := p.q.StartDeployment(ctx, id); err != nil {
		return fmt.Errorf("mark started: %w", err)
	}

	for i, stage := range p.stages {
		if err := p.runStage(workCtx, dep, stage); err != nil {
			// parent ctx so cleanup survives a cancel of workCtx
			p.rollbackPreceding(ctx, dep, i)
			return p.finalizeFailure(ctx, dep, stage.Name(), err)
		}
	}

	if err := p.markSucceeded(ctx, dep); err != nil {
		return err
	}
	if err := emitDeploySucceeded(ctx, p.q, dep); err != nil {
		p.log.Warn("outbox succeeded event", slog.Any("err", err))
	}
	return nil
}

func (p *Pipeline) runStage(ctx context.Context, dep *Deployment, s Stage) error {
	name := s.Name()
	id := db.MustUUID(dep.ID)
	log := p.log.With(slog.String("stage", name), slog.String("deploymentId", dep.ID))

	if next, ok := deploymentStatusFor(name); ok {
		err := p.q.TransitionDeploymentStatus(ctx, dbgen.TransitionDeploymentStatusParams{
			ID:     id,
			Status: next,
		})
		if err != nil {
			return fmt.Errorf("transition deployment to %s: %w", next, err)
		}
	}

	for {
		info, err := p.q.StartStage(ctx, dbgen.StartStageParams{DeploymentID: id, Name: name})
		if err != nil {
			return fmt.Errorf("mark stage running: %w", err)
		}

		log.Info("stage started", slog.Int("attempt", int(info.Attempt)), slog.Int("max", int(info.MaxAttempts)))
		runErr := s.Run(ctx, dep)
		if runErr == nil {
			if err := p.q.FinishStage(ctx, dbgen.FinishStageParams{
				DeploymentID: id,
				Name:         name,
				Status:       dbgen.DeploymentStageStatusSUCCEEDED,
			}); err != nil {
				return fmt.Errorf("mark stage succeeded: %w", err)
			}
			log.Info("stage completed")
			return nil
		}

		if ctx.Err() != nil || info.Attempt >= info.MaxAttempts {
			// best-effort write; if this also fails, the original error wins
			_ = p.q.FinishStage(ctx, dbgen.FinishStageParams{
				DeploymentID: id,
				Name:         name,
				Status:       dbgen.DeploymentStageStatusFAILED,
				ErrorJson:    errorJSON(runErr),
			})
			log.Error("stage failed", slog.Any("err", runErr), slog.Int("attempt", int(info.Attempt)))
			return runErr
		}

		log.Warn("stage failed, retrying", slog.Any("err", runErr), slog.Int("attempt", int(info.Attempt)))
		if rbErr := s.Rollback(ctx, dep); rbErr != nil {
			log.Warn("retry rollback failed", slog.Any("err", rbErr))
		}
	}
}

func (p *Pipeline) rollbackPreceding(ctx context.Context, dep *Deployment, failedIdx int) {
	id := db.MustUUID(dep.ID)
	for j := failedIdx - 1; j >= 0; j-- {
		s := p.stages[j]
		log := p.log.With(slog.String("stage", s.Name()), slog.String("deploymentId", dep.ID))
		log.Info("rolling back stage")
		if err := s.Rollback(ctx, dep); err != nil {
			log.Warn("rollback failed (continuing)", slog.Any("err", err))
		}
		if err := p.q.FinishStage(ctx, dbgen.FinishStageParams{
			DeploymentID: id,
			Name:         s.Name(),
			Status:       dbgen.DeploymentStageStatusROLLEDBACK,
		}); err != nil {
			log.Warn("mark rolled_back failed", slog.Any("err", err))
		}
	}
}

func (p *Pipeline) finalizeFailure(ctx context.Context, dep *Deployment, stageName string, runErr error) error {
	cancelled, checkErr := p.wasCancelled(ctx, dep.ID)
	if checkErr != nil {
		p.log.Warn("cancel-state check failed (defaulting to FAILED)", slog.Any("err", checkErr))
	}
	if cancelled {
		if err := p.markCancelled(ctx, dep); err != nil {
			return err
		}
		if err := emitDeployCancelled(ctx, p.q, dep, stageName); err != nil {
			p.log.Warn("outbox cancelled event", slog.Any("err", err))
		}
		return nil
	}
	if err := p.markFailed(ctx, dep, stageName, runErr); err != nil {
		return err
	}
	if err := emitDeployFailed(ctx, p.q, dep, stageName, runErr); err != nil {
		p.log.Warn("outbox failed event", slog.Any("err", err))
	}
	return nil
}

func (p *Pipeline) wasCancelled(ctx context.Context, deploymentID string) (bool, error) {
	ts, err := p.q.GetCancelRequestedAt(ctx, db.MustUUID(deploymentID))
	if err != nil {
		return false, err
	}
	return ts.Valid, nil
}

func (p *Pipeline) markCancelled(ctx context.Context, dep *Deployment) error {
	reason := "cancelled by user request"
	err := p.q.FinishDeployment(ctx, dbgen.FinishDeploymentParams{
		ID:           db.MustUUID(dep.ID),
		Status:       dbgen.DeploymentStatusCANCELLED,
		ErrorSummary: &reason,
	})
	if err != nil {
		return fmt.Errorf("mark deployment cancelled: %w", err)
	}
	return nil
}

func (p *Pipeline) markFailed(ctx context.Context, dep *Deployment, stageName string, runErr error) error {
	summary := fmt.Sprintf("%s: %s", stageName, runErr.Error())
	err := p.q.FinishDeployment(ctx, dbgen.FinishDeploymentParams{
		ID:           db.MustUUID(dep.ID),
		Status:       dbgen.DeploymentStatusFAILED,
		ErrorSummary: &summary,
	})
	if err != nil {
		p.log.Error("mark deployment failed",
			slog.String("deploymentId", dep.ID),
			slog.Any("err", err),
		)
		return fmt.Errorf("mark deployment failed: %w", err)
	}
	return nil
}

func (p *Pipeline) markSucceeded(ctx context.Context, dep *Deployment) error {
	err := p.q.FinishDeployment(ctx, dbgen.FinishDeploymentParams{
		ID:     db.MustUUID(dep.ID),
		Status: dbgen.DeploymentStatusSUCCEEDED,
	})
	if err != nil {
		return fmt.Errorf("mark deployment succeeded: %w", err)
	}
	p.log.Info("deployment succeeded", slog.String("deploymentId", dep.ID))
	return nil
}

func deploymentStatusFor(stageName string) (dbgen.DeploymentStatus, bool) {
	switch stageName {
	case "clone":
		return dbgen.DeploymentStatusCLONING, true
	case "build":
		return dbgen.DeploymentStatusBUILDING, true
	case "run":
		return dbgen.DeploymentStatusSTARTING, true
	}
	return "", false
}

func errorJSON(err error) []byte {
	if err == nil {
		return nil
	}
	body, _ := json.Marshal(map[string]string{"message": err.Error()})
	return body
}
