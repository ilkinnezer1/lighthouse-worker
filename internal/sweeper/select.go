package sweeper

import (
	"regexp"
	"strings"

	"github.com/docker/docker/api/types"
)

const deployPrefix = "deploy-"

var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// LiveDeploymentIDs maps running containers back to the deployments that own
// them, covering the single container deploy-<id> and compose projects alike.
// A deployment here is skipped whatever the database says about it.
func LiveDeploymentIDs(containers []types.Container) map[string]struct{} {
	live := make(map[string]struct{})
	for _, c := range containers {
		if id, ok := strings.CutPrefix(c.Labels["com.docker.compose.project"], deployPrefix); ok && uuidPattern.MatchString(id) {
			live[id] = struct{}{}
		}
		for _, n := range c.Names {
			if id, ok := strings.CutPrefix(strings.TrimPrefix(n, "/"), deployPrefix); ok && uuidPattern.MatchString(id) {
				live[id] = struct{}{}
			}
		}
	}
	return live
}

// Eligible drops candidates a running container still belongs to. The SQL has
// already excluded in-flight deployments, each environment's newest successful
// one, and everything inside the retention window.
func Eligible(candidates []string, live map[string]struct{}) []string {
	out := make([]string, 0, len(candidates))
	for _, id := range candidates {
		if id == "" {
			continue
		}
		if _, running := live[id]; running {
			continue
		}
		out = append(out, id)
	}
	return out
}
