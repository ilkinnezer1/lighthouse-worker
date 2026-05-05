package logstream

import (
	"context"
	"fmt"

	"github.com/redis/go-redis/v9"
)

const (
	streamMaxLen = 100_000
	fieldLine    = "line"
	fieldStage   = "stage"
)

type Writer struct {
	rdb *redis.Client
}

func New(rdb *redis.Client) *Writer {
	return &Writer{rdb: rdb}
}

func (w *Writer) WriteLine(ctx context.Context, deploymentID, stage, line string) error {
	err := w.rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: streamKey(deploymentID),
		MaxLen: streamMaxLen,
		Approx: true,
		Values: map[string]interface{}{
			fieldLine:  line,
			fieldStage: stage,
		},
	}).Err()
	if err != nil {
		return fmt.Errorf("xadd: %w", err)
	}
	return nil
}

func streamKey(deploymentID string) string {
	return fmt.Sprintf("deploy:%s:logs", deploymentID)
}