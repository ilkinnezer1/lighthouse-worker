package stages

import (
	"encoding/json"
	"os"
	"path/filepath"
)

const (
	StrategyDockerfile = "dockerfile"
	StrategyCompose    = "compose"
)

var composeFiles = []string{"compose.yml", "compose.yaml", "docker-compose.yml", "docker-compose.yaml"}

// detectStrategy honors a non-empty valid override; otherwise returns "compose"
// if a compose file is present, "dockerfile" if a Dockerfile is present, or ""
// if neither.
func detectStrategy(workDir, override string) string {
	switch override {
	case StrategyCompose, StrategyDockerfile:
		return override
	}
	for _, name := range composeFiles {
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
	Strategy string `json:"strategy"`
	Web      string `json:"web"`
	Port     int    `json:"port"`
}

// parseBuildOverrides extracts the fields the worker reads from
// Project.buildConfig JSON. Malformed input falls back to zero values.
func parseBuildOverrides(data []byte) buildOverrides {
	var o buildOverrides
	_ = json.Unmarshal(data, &o)
	return o
}
