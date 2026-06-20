package httpx

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/docker/docker/client"
	"github.com/docker/docker/errdefs"

	"github.com/ilkinnezer1/lighthouse-worker/internal/db"
	dbgen "github.com/ilkinnezer1/lighthouse-worker/internal/db/gen"
)

// serviceHealth reports the live Docker state of a single service container.
// Unlike serviceStats it deliberately surfaces stopped/exited/missing
// containers instead of 404-ing them, so the UI can show why a service is down.
func serviceHealth(docker *client.Client, q *dbgen.Queries, log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		depID, err := db.ParseUUID(r.PathValue("id"))
		if err != nil {
			http.Error(w, "invalid deployment id", http.StatusBadRequest)
			return
		}
		name := r.PathValue("name")

		var containerID *string
		if name != "" && name != "web" {
			svc, qerr := q.GetServiceByDeploymentAndName(r.Context(), dbgen.GetServiceByDeploymentAndNameParams{
				DeploymentID: depID,
				Name:         name,
			})
			if qerr == nil {
				containerID = svc.ContainerID
			}
		} else {
			svc, qerr := q.GetWebServiceByDeployment(r.Context(), depID)
			if qerr == nil {
				containerID = svc.ContainerID
			}
		}
		if containerID == nil || *containerID == "" {
			http.Error(w, "no container for service", http.StatusNotFound)
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()

		state, health, restartCount, exitCode := "missing", "none", 0, 0
		info, err := docker.ContainerInspect(ctx, *containerID)
		switch {
		case errdefs.IsNotFound(err):
			// The container was removed from Docker but the row still names it;
			// report it as missing rather than failing the request.
		case err != nil:
			http.Error(w, "inspect: "+err.Error(), http.StatusBadGateway)
			return
		default:
			restartCount = info.RestartCount
			if info.State != nil {
				state, exitCode = info.State.Status, info.State.ExitCode
				if info.State.Health != nil {
					health = info.State.Health.Status
				}
			}
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"service":      name,
			"state":        state,
			"health":       health,
			"restartCount": restartCount,
			"exitCode":     exitCode,
		})
	}
}
