package pipeline

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"

	dbgen "github.com/ilkinnezer1/lighthouse-worker/internal/db/gen"
)

const (
	eventDeploySucceeded = "deploy.succeeded"
	eventDeployFailed    = "deploy.failed"
)

func emitDeploySucceeded(ctx context.Context, q *dbgen.Queries, dep *Deployment) error {
	payload := map[string]any{
		"deploymentId":  dep.ID,
		"environmentId": dep.EnvironmentID,
		"host":          dep.Host,
		"url":           dep.URL(),
	}
	return insertOutbox(ctx, q, eventDeploySucceeded, dep.TriggeredByUserID, payload)
}

func emitDeployFailed(ctx context.Context, q *dbgen.Queries, dep *Deployment, stageName string, runErr error) error {
	payload := map[string]any{
		"deploymentId": dep.ID,
		"stage":        stageName,
		"error":        runErr.Error(),
	}
	return insertOutbox(ctx, q, eventDeployFailed, dep.TriggeredByUserID, payload)
}

func insertOutbox(ctx context.Context, q *dbgen.Queries, eventType, userID string, payload map[string]any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}
	return q.InsertOutboxEvent(ctx, dbgen.InsertOutboxEventParams{
		Type:    eventType,
		UserID:  userIDOrNull(userID),
		Payload: body,
	})
}

func userIDOrNull(s string) pgtype.UUID {
	var u pgtype.UUID
	if s == "" {
		return u
	}
	_ = u.Scan(s)
	return u
}
