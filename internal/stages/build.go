package stages

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/ilkinnezer1/lighthouse-worker/internal/compose"
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
	switch detectStrategy(dep.WorkDir, parseBuildOverrides(dep.BuildConfig).Strategy) {
	case StrategyDockerfile:
		return b.buildDockerfile(ctx, dep)
	case StrategyCompose:
		return b.buildCompose(ctx, dep)
	case StrategyNixpacks:
		return b.buildNixpacks(ctx, dep)
	case StrategyGenerated:
		return b.buildGenerated(ctx, dep)
	case StrategyBuildpacks:
		return b.buildBuildpacks(ctx, dep)
	case StrategyRailpack:
		return b.buildRailpack(ctx, dep)
	case StrategyImage:
		return b.buildImage(ctx, dep)
	default:
		return fmt.Errorf("unknown build strategy for %s", dep.WorkDir)
	}
}

func (b *Build) buildCompose(ctx context.Context, dep *pipeline.Deployment) error {
	composeFile, err := compose.ResolvePath(parseBuildOverrides(dep.BuildConfig).ComposePath)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, "docker", "compose",
		"-f", composeFile,
		"--project-name", composeProject(dep.ID),
		"build",
	)
	cmd.Dir = dep.WorkDir
	if err := runCmd(ctx, cmd, b.logs, dep.ID, "build"); err != nil {
		return fmt.Errorf("docker compose build: %w", err)
	}
	return nil
}

func composeProject(deploymentID string) string {
	return "deploy-" + deploymentID
}

func (b *Build) buildDockerfile(ctx context.Context, dep *pipeline.Deployment) error {
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
	if detectStrategy(dep.WorkDir, parseBuildOverrides(dep.BuildConfig).Strategy) == StrategyCompose {
		return b.rollbackCompose(ctx, dep)
	}
	cmd := exec.CommandContext(ctx, "docker", "image", "rm", "-f", ImageTag(dep.ID))
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker image rm: %w", err)
	}
	return nil
}

func (b *Build) rollbackCompose(ctx context.Context, dep *pipeline.Deployment) error {
	pattern := composeProject(dep.ID) + "-*"
	out, err := exec.CommandContext(ctx, "docker", "images", "--filter", "reference="+pattern, "-q").Output()
	if err != nil {
		return fmt.Errorf("list compose images: %w", err)
	}
	ids := strings.Fields(string(out))
	if len(ids) == 0 {
		return nil
	}
	if err := exec.CommandContext(ctx, "docker", append([]string{"rmi", "-f"}, ids...)...).Run(); err != nil {
		return fmt.Errorf("rmi compose images: %w", err)
	}
	return nil
}

func ImageTag(deploymentID string) string {
	return "deploy-" + deploymentID
}

func (b *Build) logLine(ctx context.Context, deploymentID, line string) {
	_ = b.logs.WriteLine(ctx, deploymentID, "build", line)
}
