package stages

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"github.com/ilkinnezer1/lighthouse-worker/internal/framework"
	"github.com/ilkinnezer1/lighthouse-worker/internal/pipeline"
)

const (
	packBinary = "pack"
	// buildpacksBuildTimeout bounds a CNB build (builder pull + detect + compile)
	// so a wedged lifecycle can't pin a worker slot until the reaper notices.
	buildpacksBuildTimeout = 30 * time.Minute
	procfileName           = "Procfile"

	BuilderHeroku = "heroku"
	BuilderPaketo = "paketo"

	herokuBuilderImage = "heroku/builder:24"
	paketoBuilderImage = "paketobuildpacks/builder-jammy-base"
)

// resolveBuilder maps a buildConfig.builder preset to a CNB builder image. An
// unrecognized value is passed through verbatim so a user can name any builder.
func resolveBuilder(name string) string {
	switch name {
	case "", BuilderHeroku:
		return herokuBuilderImage
	case BuilderPaketo:
		return paketoBuilderImage
	}
	return name
}

// buildBuildpacks builds a repo into an OCI image with Cloud Native Buildpacks
// via the pack CLI, against the same Docker daemon the dockerfile path uses.
// Tagging the image ImageTag(dep.ID) lets the existing run/health/retire/rollback
// machinery pick it up unchanged.
func (b *Build) buildBuildpacks(ctx context.Context, dep *pipeline.Deployment) error {
	if _, err := exec.LookPath(packBinary); err != nil {
		return fmt.Errorf("pack not found on PATH — install the Cloud Native Buildpacks CLI on the worker host/image: %w", err)
	}
	o := parseBuildOverrides(dep.BuildConfig)
	src := buildSource(dep, o)

	cleanup, err := b.writeProcfile(ctx, dep, src, o.StartCommand)
	if err != nil {
		return err
	}
	defer cleanup()

	builder := resolveBuilder(o.Builder)
	b.logLine(ctx, dep.ID, "building with Cloud Native Buildpacks (builder: "+builder+")")
	if o.BuildCommand != "" {
		b.logLine(ctx, dep.ID, "note: buildConfig.buildCommand is ignored by buildpacks — the buildpack decides the build step")
	}

	bctx, cancel := context.WithTimeout(ctx, buildpacksBuildTimeout)
	defer cancel()

	cmd := exec.CommandContext(bctx, packBinary, buildpacksBuildArgs(dep, o, builder, src)...)
	if err := runCmd(bctx, cmd, b.logs, dep.ID, "build"); err != nil {
		return fmt.Errorf("pack build: %w", err)
	}
	return nil
}

// writeProcfile honors buildConfig.startCommand, which CNB expresses as a
// Procfile rather than a flag (--default-process names a type, not a command).
// A Procfile the repo already ships always wins, so we never clobber user intent.
func (b *Build) writeProcfile(ctx context.Context, dep *pipeline.Deployment, src, startCommand string) (func(), error) {
	noop := func() {}
	if startCommand == "" {
		return noop, nil
	}
	path := filepath.Join(src, procfileName)
	if _, err := os.Stat(path); err == nil {
		b.logLine(ctx, dep.ID, "note: repo ships a Procfile — it wins over buildConfig.startCommand")
		return noop, nil
	}
	if err := os.WriteFile(path, []byte("web: "+startCommand+"\n"), 0o644); err != nil {
		return noop, fmt.Errorf("write procfile: %w", err)
	}
	b.logLine(ctx, dep.ID, "wrote a Procfile for the start command: web: "+startCommand)
	return func() { _ = os.Remove(path) }, nil
}

func buildpacksBuildArgs(dep *pipeline.Deployment, o buildOverrides, builder, src string) []string {
	args := []string{
		"build", ImageTag(dep.ID),
		"--path", src,
		"--builder", builder,
		"--pull-policy", "if-not-present",
	}
	for _, bp := range o.Buildpacks {
		args = append(args, "--buildpack", bp)
	}
	if o.RuntimeVersion != "" {
		if key := paketoVersionEnvKey(builder, framework.Detect(src)); key != "" {
			args = append(args, "--env", key+"="+o.RuntimeVersion)
		}
	}
	return args
}

// paketoVersionEnvKey maps a detected framework to the Paketo buildpack env var
// that pins its language runtime. Empty for the Heroku builder, which reads the
// repo's own manifests (.node-version, runtime.txt, …) instead — so pinning
// there is a no-op rather than a misapplied flag.
func paketoVersionEnvKey(builder, fw string) string {
	if builder != paketoBuilderImage {
		return ""
	}
	switch fw {
	case "node-prisma":
		return "BP_NODE_VERSION"
	case "django":
		return "BP_CPYTHON_VERSION"
	case "rails":
		return "BP_MRI_VERSION"
	case frameworkLaravel, "symfony":
		return "BP_PHP_VERSION"
	}
	return ""
}

// runBuildpacks starts the CNB-built image through the shared image run path,
// telling the app which port to bind via $PORT (kept equal to the port Traefik
// routes to), exactly as the nixpacks and generated paths do.
func (r *Run) runBuildpacks(ctx context.Context, dep *pipeline.Deployment, o buildOverrides) error {
	envs, migrate, err := r.deployEnvs(ctx, dep, o)
	if err != nil {
		return err
	}
	port := resolvePort(o.Port, sourceBuildDefaultPort)
	envs["PORT"] = strconv.Itoa(port)
	return r.runImage(ctx, dep, o, envs, port, migrate)
}
