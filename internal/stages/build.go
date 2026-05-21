package stages

import (
	"context"
	"fmt"
	"os/exec"

	"github.com/ilkinnezer1/lighthouse-worker/internal/logstream"
	"github.com/ilkinnezer1/lighthouse-worker/internal/pipeline"
)

type Build struct {
	logs *logstream.Writer
}

func NewBuild(logs *logstream.Writer) *Build {
	return &Build{logs: logs}
}

func (*Build) Name() string { return "build" }

func (b *Build) Run(ctx context.Context, dep *pipeline.Deployment) error {
	cmd := exec.CommandContext(ctx, "docker", "build",
		"--tag", ImageTag(dep.ID),
		dep.WorkDir,
	)

	if err := runCmd(ctx, cmd, b.logs, dep.ID, "build"); err != nil {
		return fmt.Errorf("docker build: %w", err)
	}
	return nil
}

func (b *Build) Rollback(ctx context.Context, dep *pipeline.Deployment) error {
	cmd := exec.CommandContext(ctx, "docker", "image", "rm", "-f", ImageTag(dep.ID))
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker image rm: %w", err)
	}
	return nil
}

func ImageTag(deploymentID string) string {
	return "deploy-" + deploymentID
}
