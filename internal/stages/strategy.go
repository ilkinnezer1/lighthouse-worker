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
)

// detectStrategy honors a non-empty valid override; otherwise returns "compose"
// if a compose file is present, "dockerfile" if a Dockerfile is present, or ""
// if neither.
func detectStrategy(workDir, override string) string {
	switch override {
	case StrategyCompose, StrategyDockerfile:
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
	return ""
}

type buildOverrides struct {
	Strategy    string      `json:"strategy"`
	Web         string      `json:"web"`
	Port        int         `json:"port"`
	ComposePath string      `json:"composePath"`
	SetupSteps  []setupStep `json:"setupSteps"`
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
