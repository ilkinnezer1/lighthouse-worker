package stages

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/ilkinnezer1/lighthouse-worker/internal/pipeline"
)

func TestResolveBuilder(t *testing.T) {
	if got := resolveBuilder(""); got != herokuBuilderImage {
		t.Fatalf("empty builder must default to heroku, got %q", got)
	}
	if got := resolveBuilder(BuilderHeroku); got != herokuBuilderImage {
		t.Fatalf("heroku preset = %q", got)
	}
	if got := resolveBuilder(BuilderPaketo); got != paketoBuilderImage {
		t.Fatalf("paketo preset = %q", got)
	}
	custom := "ghcr.io/acme/builder:1"
	if got := resolveBuilder(custom); got != custom {
		t.Fatalf("custom builder must pass through verbatim, got %q", got)
	}
}

func TestPaketoVersionEnvKey(t *testing.T) {
	if got := paketoVersionEnvKey(paketoBuilderImage, "node-prisma"); got != "BP_NODE_VERSION" {
		t.Fatalf("paketo node key = %q", got)
	}
	if got := paketoVersionEnvKey(paketoBuilderImage, frameworkLaravel); got != "BP_PHP_VERSION" {
		t.Fatalf("paketo laravel key = %q", got)
	}
	if got := paketoVersionEnvKey(paketoBuilderImage, "unknown"); got != "" {
		t.Fatalf("unknown framework must not pin a version, got %q", got)
	}
	if got := paketoVersionEnvKey(herokuBuilderImage, "node-prisma"); got != "" {
		t.Fatalf("heroku builder reads repo manifests — pinning must be a no-op, got %q", got)
	}
}

func TestBuildpacksBuildArgs(t *testing.T) {
	dep := &pipeline.Deployment{ID: "dep1", WorkDir: t.TempDir()}
	o := buildOverrides{Buildpacks: []string{"paketo-buildpacks/nodejs"}}

	args := strings.Join(buildpacksBuildArgs(dep, o, paketoBuilderImage, dep.WorkDir), " ")
	for _, want := range []string{
		"build " + ImageTag(dep.ID),
		"--path " + dep.WorkDir,
		"--builder " + paketoBuilderImage,
		"--pull-policy if-not-present",
		"--buildpack paketo-buildpacks/nodejs",
	} {
		if !strings.Contains(args, want) {
			t.Fatalf("args %q missing %q", args, want)
		}
	}

	o.RuntimeVersion = "20"
	args = strings.Join(buildpacksBuildArgs(dep, o, paketoBuilderImage, dep.WorkDir), " ")
	if strings.Contains(args, "--env") {
		t.Fatalf("an undetected stack must skip version pinning, got %q", args)
	}
}

func TestBuildSourceHonorsRootDir(t *testing.T) {
	dep := &pipeline.Deployment{ID: "dep1", WorkDir: "/work"}
	if got := buildSource(dep, buildOverrides{}); got != "/work" {
		t.Fatalf("no rootDir must use the workdir, got %q", got)
	}
	if got := buildSource(dep, buildOverrides{RootDir: "apps/api"}); got != filepath.Join("/work", "apps/api") {
		t.Fatalf("rootDir = %q", got)
	}
}

func TestResolvePort(t *testing.T) {
	if got := resolvePort(0, sourceBuildDefaultPort); got != sourceBuildDefaultPort {
		t.Fatalf("zero override must fall back, got %d", got)
	}
	if got := resolvePort(3000, sourceBuildDefaultPort); got != 3000 {
		t.Fatalf("override = %d", got)
	}
}
