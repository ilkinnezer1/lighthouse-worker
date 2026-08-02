package stages

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"time"

	"github.com/ilkinnezer1/lighthouse-worker/internal/framework"
	"github.com/ilkinnezer1/lighthouse-worker/internal/pipeline"
)

const (
	railpackBinary = "railpack"
	// railpackBuildTimeout bounds a BuildKit build so a wedged daemon can't pin a
	// worker slot until the reaper notices.
	railpackBuildTimeout = 30 * time.Minute
	buildkitHostEnv      = "BUILDKIT_HOST"
)

// buildRailpack builds a repo into an OCI image with railpack, which — unlike
// every other strategy — drives BuildKit rather than the Docker daemon directly,
// so it needs BUILDKIT_HOST pointing at the buildkitd service. The image is
// tagged ImageTag(dep.ID) so the shared run/health/retire/rollback machinery
// picks it up unchanged.
func (b *Build) buildRailpack(ctx context.Context, dep *pipeline.Deployment) error {
	if _, err := exec.LookPath(railpackBinary); err != nil {
		return fmt.Errorf("railpack not found on PATH — install it on the worker host/image: %w", err)
	}
	if os.Getenv(buildkitHostEnv) == "" {
		return fmt.Errorf("%s is unset — railpack builds need a BuildKit daemon (start the buildkitd service and set %s, e.g. tcp://127.0.0.1:1234)", buildkitHostEnv, buildkitHostEnv)
	}
	o := parseBuildOverrides(dep.BuildConfig)

	bctx, cancel := context.WithTimeout(ctx, railpackBuildTimeout)
	defer cancel()

	cmd := exec.CommandContext(bctx, railpackBinary, railpackBuildArgs(dep, o)...)
	if err := runCmd(bctx, cmd, b.logs, dep.ID, "build"); err != nil {
		return fmt.Errorf("railpack build: %w", err)
	}
	return b.assertImagePresent(ctx, dep)
}

// assertImagePresent turns railpack's cross-daemon export gap into a clear build
// failure: BuildKit can succeed while the image never lands in the local Docker
// daemon, which would otherwise surface as a confusing "no such image" at run.
func (b *Build) assertImagePresent(ctx context.Context, dep *pipeline.Deployment) error {
	if err := exec.CommandContext(ctx, "docker", "image", "inspect", ImageTag(dep.ID)).Run(); err != nil {
		return fmt.Errorf("railpack build reported success but %s is not in the local docker daemon — check that BUILDKIT_HOST exports images back to the worker's daemon", ImageTag(dep.ID))
	}
	return nil
}

func railpackBuildArgs(dep *pipeline.Deployment, o buildOverrides) []string {
	args := []string{"build", buildSource(dep, o), "--name", ImageTag(dep.ID)}
	if o.BuildCommand != "" {
		args = append(args, "--build-cmd", o.BuildCommand)
	}
	if o.StartCommand != "" {
		args = append(args, "--start-cmd", o.StartCommand)
	}
	if o.RuntimeVersion != "" {
		if tool := railpackMiseTool(framework.Detect(buildSource(dep, o))); tool != "" {
			args = append(args, "--env", "RAILPACK_PACKAGES="+tool+"@"+o.RuntimeVersion)
		}
	}
	return args
}

// railpackMiseTool maps a detected framework to the mise tool railpack installs
// via RAILPACK_PACKAGES, its only documented runtime-pinning knob. Empty when the
// language is unknown, so an unrecognized project skips pinning rather than
// requesting a package that doesn't exist.
func railpackMiseTool(fw string) string {
	switch fw {
	case "node-prisma":
		return "node"
	case "django":
		return "python"
	case "rails":
		return "ruby"
	case frameworkLaravel, "symfony":
		return "php"
	}
	return ""
}

// runRailpack starts the railpack-built image through the shared image run path,
// telling the app which port to bind via $PORT (kept equal to the port Traefik
// routes to), exactly as the nixpacks and generated paths do.
func (r *Run) runRailpack(ctx context.Context, dep *pipeline.Deployment, o buildOverrides) error {
	envs, migrate, err := r.deployEnvs(ctx, dep, o)
	if err != nil {
		return err
	}
	port := resolvePort(o.Port, sourceBuildDefaultPort)
	envs["PORT"] = strconv.Itoa(port)
	return r.runImage(ctx, dep, o, envs, port, migrate)
}
