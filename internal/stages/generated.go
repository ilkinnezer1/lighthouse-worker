package stages

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ilkinnezer1/lighthouse-worker/internal/dockerfilegen"
	"github.com/ilkinnezer1/lighthouse-worker/internal/framework"
	"github.com/ilkinnezer1/lighthouse-worker/internal/pipeline"
)

// generatedDockerfileName is written into the build context to feed `docker build
// -f`; the ".lighthouse" suffix keeps it from ever shadowing a repo's own file.
const generatedDockerfileName = "Dockerfile.lighthouse"

// buildGenerated synthesizes a version-matched Dockerfile for a repo that ships
// no Dockerfile/compose, writes it into the build context, and builds it to
// ImageTag(dep.ID) — so the shared run/health/retire/rollback machinery picks it
// up exactly like the hand-written Dockerfile path. The generated file is logged
// verbatim so the user can copy and customize it.
func (b *Build) buildGenerated(ctx context.Context, dep *pipeline.Deployment) error {
	o := parseBuildOverrides(dep.BuildConfig)
	src := nixpacksSource(dep, o)

	content, port, err := dockerfilegen.Generate(src, framework.Detect(src), dockerfilegen.Options{
		BuildCommand:   o.BuildCommand,
		StartCommand:   o.StartCommand,
		RuntimeVersion: o.RuntimeVersion,
		Port:           o.Port,
	})
	if err != nil {
		b.logLine(ctx, dep.ID, err.Error())
		return fmt.Errorf("generate dockerfile: %w", err)
	}

	path := filepath.Join(src, generatedDockerfileName)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return fmt.Errorf("write generated dockerfile: %w", err)
	}
	defer os.Remove(path)

	b.logLine(ctx, dep.ID, fmt.Sprintf("no Dockerfile or compose file found — Lighthouse generated one (serving on port %d). Copy it into your repo to customize:", port))
	for _, line := range strings.Split(strings.TrimRight(content, "\n"), "\n") {
		b.logLine(ctx, dep.ID, "  | "+line)
	}

	cmd := exec.CommandContext(ctx, "docker", "build",
		"--tag", ImageTag(dep.ID),
		"--file", path,
		src,
	)
	if err := runCmd(ctx, cmd, b.logs, dep.ID, "build"); err != nil {
		return fmt.Errorf("docker build (generated): %w", err)
	}
	return nil
}

// runGenerated starts the generated image through the shared image run path,
// telling the app which port to bind via $PORT (kept equal to the port Traefik
// routes to). It inherits Laravel migrate/APP_KEY/DB_* via deployEnvs for free.
func (r *Run) runGenerated(ctx context.Context, dep *pipeline.Deployment, o buildOverrides) error {
	envs, migrate, err := r.deployEnvs(ctx, dep, o)
	if err != nil {
		return err
	}
	port := resolveGeneratedPort(o.Port)
	envs["PORT"] = strconv.Itoa(port)
	return r.runImage(ctx, dep, envs, port, migrate)
}

func resolveGeneratedPort(override int) int {
	if override != 0 {
		return override
	}
	return dockerfilegen.DefaultPort
}
