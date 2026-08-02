package stages

import (
	"strings"
	"testing"

	"github.com/ilkinnezer1/lighthouse-worker/internal/pipeline"
)

func TestRailpackBuildArgs(t *testing.T) {
	dep := &pipeline.Deployment{ID: "dep1", WorkDir: t.TempDir()}
	o := buildOverrides{BuildCommand: "npm run build", StartCommand: "node server.js"}

	args := strings.Join(railpackBuildArgs(dep, o), " ")
	for _, want := range []string{
		"build " + dep.WorkDir,
		"--name " + ImageTag(dep.ID),
		"--build-cmd npm run build",
		"--start-cmd node server.js",
	} {
		if !strings.Contains(args, want) {
			t.Fatalf("args %q missing %q", args, want)
		}
	}

	o.RuntimeVersion = "20"
	if args = strings.Join(railpackBuildArgs(dep, o), " "); strings.Contains(args, "RAILPACK_PACKAGES") {
		t.Fatalf("an undetected stack must skip version pinning, got %q", args)
	}
}

func TestRailpackMiseTool(t *testing.T) {
	if got := railpackMiseTool("node-prisma"); got != "node" {
		t.Fatalf("node tool = %q", got)
	}
	if got := railpackMiseTool(frameworkLaravel); got != "php" {
		t.Fatalf("laravel tool = %q", got)
	}
	if got := railpackMiseTool("unknown"); got != "" {
		t.Fatalf("unknown framework must not request a package, got %q", got)
	}
}

func TestDetectStrategyHonorsExternalBuilders(t *testing.T) {
	dir := t.TempDir()
	for _, s := range []string{StrategyBuildpacks, StrategyRailpack, StrategyNixpacks, StrategyGenerated} {
		if got := detectStrategy(dir, s); got != s {
			t.Fatalf("override %q was not honored, got %q", s, got)
		}
	}
	if got := detectStrategy(dir, "sandbox-yml"); got != StrategyGenerated {
		t.Fatalf("an unknown override must fall through to auto-detect, got %q", got)
	}
}
