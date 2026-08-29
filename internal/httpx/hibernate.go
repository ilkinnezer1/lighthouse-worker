package httpx

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sort"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/docker/docker/errdefs"

	"github.com/ilkinnezer1/lighthouse-worker/internal/db"
	dbgen "github.com/ilkinnezer1/lighthouse-worker/internal/db/gen"
)

const (
	actionHibernate = "hibernate"
	actionWake      = "wake"

	// A container whose row still names it but which Docker no longer has.
	stateMissing = "missing"
	stateUnknown = "unknown"
	stateError   = "error"
)

type hibernationTarget struct {
	name        string
	containerID string
	isWeb       bool
}

type hibernationServiceState struct {
	Name  string `json:"name"`
	State string `json:"state"`
}

type hibernationResult struct {
	DeploymentID string                    `json:"deploymentId"`
	Action       string                    `json:"action"`
	Services     []hibernationServiceState `json:"services"`
}

// orderTargets sorts by the only ordering that keeps a stack coherent while it
// is half-up: web goes down first so nothing serves traffic against dependencies
// that are already dying, and comes back up last so the app never boots against
// a database that is not listening yet. Stable, so the query's name ordering
// still decides between the non-web services.
func orderTargets(targets []hibernationTarget, webFirst bool) []hibernationTarget {
	ordered := make([]hibernationTarget, len(targets))
	copy(ordered, targets)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].isWeb == ordered[j].isWeb {
			return false
		}
		if webFirst {
			return ordered[i].isWeb
		}
		return ordered[j].isWeb
	})
	return ordered
}

// hibernationAction stops or starts every container of a deployment in one call,
// which is the whole point of it being deployment-level: the ordering between
// services cannot be expressed by N independent per-service requests.
//
// It is idempotent by contract — the control plane's scheduler retries — so a
// container already in the wanted state, or gone from Docker entirely, is a
// success that reports its real state rather than an error.
func hibernationAction(docker *client.Client, q *dbgen.Queries, log *slog.Logger, action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rawID := r.PathValue("id")
		depID, err := db.ParseUUID(rawID)
		if err != nil {
			http.Error(w, "invalid deployment id", http.StatusBadRequest)
			return
		}

		rows, err := q.ListServiceContainersByDeployment(r.Context(), depID)
		if err != nil {
			http.Error(w, "list services: "+err.Error(), http.StatusBadGateway)
			return
		}
		if len(rows) == 0 {
			http.Error(w, "no services for deployment", http.StatusNotFound)
			return
		}

		targets := make([]hibernationTarget, 0, len(rows))
		for _, row := range rows {
			t := hibernationTarget{name: row.Name, isWeb: row.IsWeb}
			if row.ContainerID != nil {
				t.containerID = *row.ContainerID
			}
			targets = append(targets, t)
		}

		res := hibernationResult{
			DeploymentID: rawID,
			Action:       action,
			Services:     make([]hibernationServiceState, 0, len(targets)),
		}
		degraded := false

		for _, t := range orderTargets(targets, action == actionHibernate) {
			state, err := applyHibernationAction(r.Context(), docker, action, t)
			if err != nil {
				degraded = true
				log.Warn(action+" failed for service",
					slog.String("deploymentId", rawID),
					slog.String("service", t.name),
					slog.Any("err", err))
			}
			res.Services = append(res.Services, hibernationServiceState{Name: t.name, State: state})
		}

		log.Info(action+" done",
			slog.String("deploymentId", rawID),
			slog.Int("services", len(res.Services)))

		w.Header().Set("Content-Type", "application/json")
		if degraded {
			w.WriteHeader(http.StatusBadGateway)
		}
		_ = json.NewEncoder(w).Encode(res)
	}
}

// applyHibernationAction returns the container's resulting state, and an error
// only for failures worth failing the request over. Docker answers 304 for a
// container already stopped or already started, which its client reports as
// success — the retry case therefore needs no special handling here.
func applyHibernationAction(parent context.Context, docker *client.Client, action string, t hibernationTarget) (string, error) {
	if t.containerID == "" {
		return stateMissing, nil
	}

	ctx, cancel := context.WithTimeout(parent, (stopGraceSeconds+10)*time.Second)
	defer cancel()

	var err error
	if action == actionHibernate {
		grace := stopGraceSeconds
		err = docker.ContainerStop(ctx, t.containerID, container.StopOptions{Timeout: &grace})
	} else {
		err = docker.ContainerStart(ctx, t.containerID, container.StartOptions{})
	}
	if err != nil && !errdefs.IsNotModified(err) {
		if errdefs.IsNotFound(err) {
			return stateMissing, nil
		}
		return stateError, err
	}

	info, err := docker.ContainerInspect(ctx, t.containerID)
	if err != nil {
		if errdefs.IsNotFound(err) {
			return stateMissing, nil
		}
		return stateUnknown, err
	}
	if info.State == nil {
		return stateUnknown, nil
	}
	return info.State.Status, nil
}
