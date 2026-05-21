package stages

import (
	"os"
	"path/filepath"
)

const (
	StrategyDockerfile = "dockerfile"
	StrategyCompose    = "compose"
)

var composeFiles = []string{"compose.yml", "compose.yaml", "docker-compose.yml", "docker-compose.yaml"}

// detectStrategy returns "compose" if a compose file is present, "dockerfile"
// if a Dockerfile is present, or "" if neither.
func detectStrategy(workDir string) string {
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
