package stages

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/ilkinnezer1/lighthouse-worker/internal/logstream"
	"github.com/ilkinnezer1/lighthouse-worker/internal/pipeline"
)

type Clone struct {
	logs *logstream.Writer
}

func NewClone(logs *logstream.Writer) *Clone {
	return &Clone{logs: logs}
}

func (*Clone) Name() string { return "clone" }

func (c *Clone) Run(ctx context.Context, dep *pipeline.Deployment) error {
	// idempotent: a redelivered job after a crash skips work already done
	if _, err := os.Stat(filepath.Join(dep.WorkDir, ".git")); err == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dep.WorkDir), 0o755); err != nil {
		return fmt.Errorf("mkdir parent: %w", err)
	}

	cmd := exec.CommandContext(ctx, "git", "clone",
		"--depth", "1",
		"--branch", dep.Branch,
		dep.RepoURL,
		dep.WorkDir,
	)

	if err := runCmd(ctx, cmd, c.logs, dep.ID, "clone"); err != nil {
		return fmt.Errorf("git clone: %w", err)
	}
	return nil
}

func (c *Clone) Rollback(_ context.Context, dep *pipeline.Deployment) error {
	if err := os.RemoveAll(dep.WorkDir); err != nil {
		return fmt.Errorf("remove workdir: %w", err)
	}
	return nil
}
