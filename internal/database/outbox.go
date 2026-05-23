package database

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"

	dbgen "github.com/ilkinnezer1/lighthouse-worker/internal/db/gen"
)

const (
	eventProvisioned        = "database.provisioned"
	eventProvisionFailed    = "database.provision_failed"
	eventTeardownFailed     = "database.teardown_failed"
	eventSnapshotReady      = "database.snapshot_ready"
	eventSnapshotFailed     = "database.snapshot_failed"
	eventSnapshotRestored   = "database.snapshot_restored"
)

func (r *Router) emit(ctx context.Context, eventType, databaseID string, extras map[string]any) error {
	payload := map[string]any{"databaseId": databaseID}
	for k, v := range extras {
		payload[k] = v
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}
	return r.q.InsertOutboxEvent(ctx, dbgen.InsertOutboxEventParams{
		Type:    eventType,
		UserID:  pgtype.UUID{},
		Payload: body,
	})
}
