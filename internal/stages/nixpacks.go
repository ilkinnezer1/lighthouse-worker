package stages

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"github.com/ilkinnezer1/lighthouse-worker/internal/framework"
	"github.com/ilkinnezer1/lighthouse-worker/internal/pipeline"
)

const (
	nixpacksBinary = "nixpacks"
	// nixpacksDefaultPort is the port a nixpacks app is told to listen on (via
	// $PORT) and that Traefik is routed to when buildConfig sets no explicit port.
	nixpacksDefaultPort = 8080
	// nixpacksBuildTimeout bounds a from-source build (toolchain install + compile)
	// so a wedged nixpacks can't pin a worker slot until the reaper notices.
	nixpacksBuildTimeout = 30 * time.Minute
)

// buildNixpacks builds a repo that has no Dockerfile/compose into an OCI image
// via nixpacks, which drives the same Docker daemon the dockerfile path uses.
// Tagging the image ImageTag(dep.ID) lets the existing run/health/retire/rollback
// machinery pick it up unchanged. A missing binary surfaces as a plain stage
// error (written to the deploy log) rather than a panic.
func (b *Build) buildNixpacks(ctx context.Context, dep *pipeline.Deployment) error {
	if _, err := exec.LookPath(nixpacksBinary); err != nil {
		return fmt.Errorf("nixpacks not found on PATH — install it on the worker host/image: %w", err)
	}
	o := parseBuildOverrides(dep.BuildConfig)

	bctx, cancel := context.WithTimeout(ctx, nixpacksBuildTimeout)
	defer cancel()

	cmd := exec.CommandContext(bctx, nixpacksBinary, nixpacksBuildArgs(dep, o)...)
	if err := runCmd(bctx, cmd, b.logs, dep.ID, "build"); err != nil {
		return fmt.Errorf("nixpacks build: %w", err)
	}
	return nil
}

// runNixpacks starts the nixpacks-built image through the shared image run path.
// It differs from the dockerfile path only in telling the app which port to bind
// via $PORT, kept equal to the port Traefik routes to.
func (r *Run) runNixpacks(ctx context.Context, dep *pipeline.Deployment, o buildOverrides) error {
	envs, migrate, err := r.deployEnvs(ctx, dep, o)
	if err != nil {
		return err
	}
	port := resolveNixpacksPort(o.Port)
	envs["PORT"] = strconv.Itoa(port)
	return r.runImage(ctx, dep, envs, port, migrate)
}

func resolveNixpacksPort(override int) int {
	if override != 0 {
		return override
	}
	return nixpacksDefaultPort
}

// nixpacksSource is the build context: the workdir, narrowed to a monorepo
// subdirectory when buildConfig.rootDir is set.
func nixpacksSource(dep *pipeline.Deployment, o buildOverrides) string {
	if o.RootDir != "" {
		return filepath.Join(dep.WorkDir, o.RootDir)
	}
	return dep.WorkDir
}

func nixpacksBuildArgs(dep *pipeline.Deployment, o buildOverrides) []string {
	src := nixpacksSource(dep, o)
	args := []string{"build", src, "--name", ImageTag(dep.ID)}
	if o.BuildCommand != "" {
		args = append(args, "--build-cmd", o.BuildCommand)
	}
	if o.StartCommand != "" {
		args = append(args, "--start-cmd", o.StartCommand)
	}
	if o.RuntimeVersion != "" {
		if key := nixpacksVersionEnvKey(framework.Detect(src)); key != "" {
			args = append(args, "--env", key+"="+o.RuntimeVersion)
		}
	}
	return args
}

// nixpacksVersionEnvKey maps a detected framework to the nixpacks provider env
// var that pins its language runtime. Empty when the language is unknown, so an
// unrecognized project silently skips version pinning rather than misapplying it.
func nixpacksVersionEnvKey(fw string) string {
	switch fw {
	case "node-prisma":
		return "NIXPACKS_NODE_VERSION"
	case "django":
		return "NIXPACKS_PYTHON_VERSION"
	case "rails":
		return "NIXPACKS_RUBY_VERSION"
	case "laravel", "symfony":
		return "NIXPACKS_PHP_VERSION"
	}
	return ""
}
