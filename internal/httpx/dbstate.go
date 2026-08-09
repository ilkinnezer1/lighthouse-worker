package httpx

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/docker/docker/client"
	"github.com/docker/docker/errdefs"
)

// maxStateIDs bounds a single lookup. Each id costs one container inspect, and
// this endpoint is called on page renders.
const maxStateIDs = 100

type databaseState struct {
	Exists  bool `json:"exists"`
	Running bool `json:"running"`
	Paused  bool `json:"paused"`
}

// databaseStates reports whether each managed database's container is actually
// there and running.
//
// The control plane records a database as READY once provisioning succeeded and
// never hears about it again, so a container stopped by a reboot or by hand
// leaves the panel showing a healthy database that cannot be reached. Only the
// worker can see Docker, so only the worker can answer this.
func databaseStates(docker *client.Client, log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		raw := r.URL.Query().Get("ids")
		if raw == "" {
			writeJSON(w, map[string]databaseState{})
			return
		}

		ids := strings.Split(raw, ",")
		if len(ids) > maxStateIDs {
			http.Error(w, "too many ids", http.StatusBadRequest)
			return
		}

		out := make(map[string]databaseState, len(ids))
		for _, id := range ids {
			id = strings.TrimSpace(id)
			if id == "" {
				continue
			}
			info, err := docker.ContainerInspect(r.Context(), "db-"+id)
			if err != nil {
				// A missing container is an answer, not a failure: the database
				// was torn down, or never finished provisioning.
				if !errdefs.IsNotFound(err) {
					log.Warn("inspect database container",
						slog.String("databaseId", id), slog.Any("err", err))
				}
				out[id] = databaseState{}
				continue
			}
			st := databaseState{Exists: true}
			if info.State != nil {
				st.Running = info.State.Running
				st.Paused = info.State.Paused
			}
			out[id] = st
		}

		writeJSON(w, out)
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		http.Error(w, "encode: "+err.Error(), http.StatusInternalServerError)
	}
}
