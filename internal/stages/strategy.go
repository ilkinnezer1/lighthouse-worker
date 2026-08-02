package stages

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/ilkinnezer1/lighthouse-worker/internal/compose"
)

const (
	StrategyDockerfile = "dockerfile"
	StrategyCompose    = "compose"
	StrategyNixpacks   = "nixpacks"
	StrategyGenerated  = "generated"
)

// detectStrategy honors a non-empty valid override; otherwise returns "compose"
// if a compose file is present, "dockerfile" if a Dockerfile is present, or
// "generated" when neither is — a repo with no build recipe gets a
// version-matched Dockerfile synthesized for it. Nixpacks is opt-in only (an
// explicit override), because its frozen toolchain can't target legacy stacks.
func detectStrategy(workDir, override string) string {
	switch override {
	case StrategyCompose, StrategyDockerfile, StrategyNixpacks, StrategyGenerated:
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
// (generated Dockerfile and nixpacks); empty values are no-ops there.
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
