package httpx

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"

	"github.com/ilkinnezer1/lighthouse-worker/internal/db"
	dbgen "github.com/ilkinnezer1/lighthouse-worker/internal/db/gen"
)

// maintenanceRun runs a single non-interactive command inside a deployment's
// service container and streams the merged stdout/stderr back, terminated by a
// record-separator-framed JSON exit line: "\x1e{\"exitCode\":N}\n".
//
// Unlike containerExec this is one-shot and TTY-less, so Docker multiplexes the
// stream (demuxed with stdcopy, like runtimeLogs). The command is curated by the
// API (server-side allowlist) — the worker just executes what it's handed, same
// internal trust boundary as exec/logs.
func maintenanceRun(docker *client.Client, q *dbgen.Queries, log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		depID, err := db.ParseUUID(r.PathValue("id"))
		if err != nil {
			http.Error(w, "invalid deployment id", http.StatusBadRequest)
			return
		}
		name := r.PathValue("name")

		var body struct {
			Command string `json:"command"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Command == "" {
			http.Error(w, "command required", http.StatusBadRequest)
			return
		}

		// Resolve the target container: the named service, or the web service
		// when name is the sentinel "web" (the API passes a concrete name).
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
			http.Error(w, "no running container for deployment", http.StatusNotFound)
			return
		}

		ctx := r.Context()

		// `-c` (not `-lc`) keeps the container's WORKDIR as cwd, so app-relative
		// commands like `php artisan ...` run from the project root.
		execID, err := docker.ContainerExecCreate(ctx, *containerID, container.ExecOptions{
			Cmd:          []string{"/bin/sh", "-c", body.Command},
			AttachStdout: true,
			AttachStderr: true,
		})
		if err != nil {
			http.Error(w, "exec create: "+err.Error(), http.StatusBadGateway)
			return
		}

		attach, err := docker.ContainerExecAttach(ctx, execID.ID, container.ExecStartOptions{})
		if err != nil {
			http.Error(w, "exec attach: "+err.Error(), http.StatusBadGateway)
			return
		}
		defer attach.Close()

		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.WriteHeader(http.StatusOK)

		fw := &flushWriter{w: w}
		if f, ok := w.(http.Flusher); ok {
			fw.f = f
			f.Flush()
		}

		// Non-TTY exec → Docker multiplexes stdout/stderr into one stream.
		if _, err := stdcopy.StdCopy(fw, fw, attach.Reader); err != nil && !errors.Is(err, context.Canceled) {
			log.Debug("maintenance run stream ended", slog.Any("err", err))
		}

		exit, _ := json.Marshal(map[string]any{"exitCode": execExitCode(docker, execID.ID)})
		_, _ = fmt.Fprintf(fw, "\x1e%s\n", exit)
	}
}
