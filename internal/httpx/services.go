package httpx

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"

	"github.com/ilkinnezer1/lighthouse-worker/internal/db"
	dbgen "github.com/ilkinnezer1/lighthouse-worker/internal/db/gen"
)

// Grace period for a graceful stop/restart before Docker SIGKILLs the container.
const stopGraceSeconds = 30

// serviceAction stops/starts/restarts a single named container of a deployment,
// leaving every other service untouched. Returns the container's resulting live
// state so the UI reflects Docker, not the cached services.status column.
func serviceAction(docker *client.Client, q *dbgen.Queries, log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		depID, err := db.ParseUUID(r.PathValue("id"))
		if err != nil {
			http.Error(w, "invalid deployment id", http.StatusBadRequest)
			return
		}
		name := r.PathValue("name")
		action := r.PathValue("action")

		svc, err := q.GetServiceByDeploymentAndName(r.Context(), dbgen.GetServiceByDeploymentAndNameParams{
			DeploymentID: depID,
			Name:         name,
		})
		if err != nil || svc.ContainerID == nil || *svc.ContainerID == "" {
			http.Error(w, "no container for service", http.StatusNotFound)
			return
		}
		id := *svc.ContainerID

		// A graceful stop/restart can take up to the grace period; bound it.
		ctx, cancel := context.WithTimeout(r.Context(), (stopGraceSeconds+10)*time.Second)
		defer cancel()

		grace := stopGraceSeconds
		switch action {
		case "stop":
			err = docker.ContainerStop(ctx, id, container.StopOptions{Timeout: &grace})
		case "start":
			err = docker.ContainerStart(ctx, id, container.StartOptions{})
		case "restart":
			err = docker.ContainerRestart(ctx, id, container.StopOptions{Timeout: &grace})
		default:
			http.Error(w, "unknown action: "+action, http.StatusBadRequest)
			return
		}
		if err != nil {
			http.Error(w, action+": "+err.Error(), http.StatusBadGateway)
			return
		}

		state := "unknown"
		if info, ierr := docker.ContainerInspect(ctx, id); ierr == nil && info.State != nil {
			state = info.State.Status
		} else if ierr != nil {
			log.Debug("inspect after action failed", slog.Any("err", ierr))
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"service": name,
			"action":  action,
			"state":   state,
		})
	}
}
