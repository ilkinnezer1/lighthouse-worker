package stages

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"time"

	"github.com/ilkinnezer1/lighthouse-worker/internal/pipeline"
)

// imagePullTimeout bounds a registry pull so a stalled or very large image can't
// pin a worker slot until the reaper notices.
const imagePullTimeout = 15 * time.Minute

// buildImage deploys a prebuilt image instead of building one: it pulls the ref
// from buildConfig.image and retags it ImageTag(dep.ID), so the shared
// run/health/retire/rollback machinery treats it exactly like a built image.
// Private registries authenticate through the worker host's docker credentials.
func (b *Build) buildImage(ctx context.Context, dep *pipeline.Deployment) error {
	o := parseBuildOverrides(dep.BuildConfig)
	if o.Image == "" {
		return errors.New("buildConfig.image is required for the image strategy")
	}

	b.logLine(ctx, dep.ID, "prebuilt image strategy — pulling "+o.Image)

	bctx, cancel := context.WithTimeout(ctx, imagePullTimeout)
	defer cancel()

	cmd := exec.CommandContext(bctx, "docker", "pull", o.Image)
	if err := runCmd(bctx, cmd, b.logs, dep.ID, "build"); err != nil {
		return fmt.Errorf("docker pull %s: %w", o.Image, err)
	}
	if err := exec.CommandContext(ctx, "docker", "tag", o.Image, ImageTag(dep.ID)).Run(); err != nil {
		return fmt.Errorf("docker tag %s: %w", o.Image, err)
	}
	return nil
}

// runPrebuiltImage starts the pulled image through the shared image run path. It
// deliberately does not inject $PORT: a prebuilt image listens on whatever port
// it was built for, so buildConfig.port is the routing target rather than a
// value the app is asked to bind.
func (r *Run) runPrebuiltImage(ctx context.Context, dep *pipeline.Deployment, o buildOverrides) error {
	envs, migrate, err := r.deployEnvs(ctx, dep, o)
	if err != nil {
		return err
	}
	return r.runImage(ctx, dep, o, envs, resolvePort(o.Port, defaultPort), migrate)
}
