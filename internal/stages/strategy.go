package stages

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/docker/docker/api/types/container"

	"github.com/ilkinnezer1/lighthouse-worker/internal/compose"
	"github.com/ilkinnezer1/lighthouse-worker/internal/pipeline"
)

const (
	StrategyDockerfile = "dockerfile"
	StrategyCompose    = "compose"
	StrategyNixpacks   = "nixpacks"
	StrategyGenerated  = "generated"
	StrategyBuildpacks = "buildpacks"
	StrategyRailpack   = "railpack"
	StrategyImage      = "image"
)

// sourceBuildDefaultPort is the port a from-source build is told to listen on
// (via $PORT) and that Traefik routes to when buildConfig sets no explicit port.
const sourceBuildDefaultPort = 8080

// detectStrategy honors a non-empty valid override; otherwise returns "compose"
// if a compose file is present, "dockerfile" if a Dockerfile is present, or
// "generated" when neither is — a repo with no build recipe gets a
// version-matched Dockerfile synthesized for it. The external builders
// (nixpacks, buildpacks, railpack) are opt-in only, because each needs a CLI on
// the worker and their frozen toolchains can't target every legacy stack.
func detectStrategy(workDir, override string) string {
	switch override {
	case StrategyCompose, StrategyDockerfile, StrategyNixpacks, StrategyGenerated,
		StrategyBuildpacks, StrategyRailpack, StrategyImage:
		return override
	}
	for _, name := range compose.FileNames {
		if _, err := os.Stat(filepath.Join(workDir, name)); err == nil {
			return StrategyCompose
		}
	}
	if _, err := os.Stat(filepath.Join(workDir, "Dockerfile")); err == nil {
		return StrategyDockerfile
	}
	return StrategyGenerated
}

// buildOverrides mirrors the fields the worker reads from Project.buildConfig.
// BuildCommand/StartCommand/RuntimeVersion/RootDir steer the from-source paths
// (generated Dockerfile, nixpacks, buildpacks, railpack); empty values are
// no-ops there. Builder/Buildpacks apply to the buildpacks strategy only.
type buildOverrides struct {
	Strategy    string      `json:"strategy"`
	Web         string      `json:"web"`
	Port        int         `json:"port"`
	ComposePath string      `json:"composePath"`
	SetupSteps  []setupStep `json:"setupSteps"`

	BuildCommand   string `json:"buildCommand"`
	StartCommand   string `json:"startCommand"`
	RuntimeVersion string `json:"runtimeVersion"`
	RootDir        string `json:"rootDir"`

	Builder    string   `json:"builder"`
	Buildpacks []string `json:"buildpacks"`

	Image    string  `json:"image"`
	CPULimit float64 `json:"cpuLimit"`
	MemoryMB int64   `json:"memoryMb"`
}

// buildSource is the build context for the from-source strategies: the workdir,
// narrowed to a monorepo subdirectory when buildConfig.rootDir is set.
func buildSource(dep *pipeline.Deployment, o buildOverrides) string {
	if o.RootDir != "" {
		return filepath.Join(dep.WorkDir, o.RootDir)
	}
	return dep.WorkDir
}

func resolvePort(override, fallback int) int {
	if override != 0 {
		return override
	}
	return fallback
}

// resourceLimits maps buildConfig's cpuLimit (in cores) and memoryMb onto the
// Docker HostConfig fields. Zero values leave the container unconstrained, which
// is what Docker treats as "no limit".
func resourceLimits(o buildOverrides) container.Resources {
	var res container.Resources
	if o.CPULimit > 0 {
		res.NanoCPUs = int64(o.CPULimit * 1e9)
	}
	if o.MemoryMB > 0 {
		res.Memory = o.MemoryMB * 1024 * 1024
	}
	return res
}

type setupStep struct {
	Command string `json:"command"`
	Image   string `json:"image"`
	Docker  bool   `json:"docker"`
}

// parseBuildOverrides extracts the fields the worker reads from
// Project.buildConfig JSON. Malformed input falls back to zero values.
func parseBuildOverrides(data []byte) buildOverrides {
	var o buildOverrides
	_ = json.Unmarshal(data, &o)
	return o
}
