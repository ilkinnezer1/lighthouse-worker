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

	for _, stage := range p.stages {
		if err := p.runStage(workCtx, dep, stage); err != nil {
			// markFailed error = infra problem worth retrying (caller leaves in PEL).
			// Stage an error with the markFailed success = recorded; ACK and stop.
			if markErr := p.markFailed(ctx, dep, stage.Name(), err); markErr != nil {
				return markErr
			}
			if outboxErr := emitDeployFailed(ctx, p.q, dep, stage.Name(), err); outboxErr != nil {
				p.log.Warn("outbox failed event", slog.Any("err", outboxErr))
			}
			return nil
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

	if err := p.q.StartStage(ctx, dbgen.StartStageParams{DeploymentID: id, Name: name}); err != nil {
		return fmt.Errorf("mark stage running: %w", err)
	}

	log.Info("stage started")
	if err := s.Run(ctx, dep); err != nil {
		// best-effort write; if this also fails, the original error wins
		_ = p.q.FinishStage(ctx, dbgen.FinishStageParams{
			DeploymentID: id,
			Name:         name,
			Status:       dbgen.DeploymentStageStatusFAILED,
			ErrorJson:    errorJSON(err),
		})
		log.Error("stage failed", slog.Any("err", err))
		return err
	}

	err := p.q.FinishStage(ctx, dbgen.FinishStageParams{
		DeploymentID: id,
		Name:         name,
		Status:       dbgen.DeploymentStageStatusSUCCEEDED,
	})
	if err != nil {
		return fmt.Errorf("mark stage succeeded: %w", err)
	}
	log.Info("stage completed")
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
