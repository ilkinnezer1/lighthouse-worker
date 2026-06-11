package httpx

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"

	"github.com/ilkinnezer1/lighthouse-worker/internal/db"
	dbgen "github.com/ilkinnezer1/lighthouse-worker/internal/db/gen"
)

const defaultLogTail = "200"

// runtimeLogs streams the web container's stdout/stderr for a deployment.
// With follow=1 (default) it blocks and tails until the client disconnects;
// the API proxies this to the browser over WS, same trust boundary as build logs.
func runtimeLogs(docker *client.Client, q *dbgen.Queries, log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		depID, err := db.ParseUUID(r.PathValue("id"))
		if err != nil {
			http.Error(w, "invalid deployment id", http.StatusBadRequest)
			return
		}

		// `service` selects a container by name for multi-service (compose)
		// deployments; absent, default to the web service.
		var containerID *string
		if name := r.URL.Query().Get("service"); name != "" {
			svc, err := q.GetServiceByDeploymentAndName(r.Context(), dbgen.GetServiceByDeploymentAndNameParams{
				DeploymentID: depID,
				Name:         name,
			})
			if err == nil {
				containerID = svc.ContainerID
			}
		} else {
			svc, err := q.GetWebServiceByDeployment(r.Context(), depID)
			if err == nil {
				containerID = svc.ContainerID
			}
		}
		if containerID == nil || *containerID == "" {
			http.Error(w, "no running container for deployment", http.StatusNotFound)
			return
		}

		tail := r.URL.Query().Get("tail")
		if tail == "" {
			tail = defaultLogTail
		}

		rc, err := docker.ContainerLogs(r.Context(), *containerID, container.LogsOptions{
			ShowStdout: true,
			ShowStderr: true,
			Follow:     r.URL.Query().Get("follow") != "0",
			Tail:       tail,
		})
		if err != nil {
			http.Error(w, "container logs: "+err.Error(), http.StatusBadGateway)
			return
		}
		defer func() { _ = rc.Close() }()

		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.WriteHeader(http.StatusOK)

		fw := &flushWriter{w: w}
		if f, ok := w.(http.Flusher); ok {
			fw.f = f
			f.Flush()
		}

		// Deploy containers run without a TTY, so docker multiplexes the stream.
		if _, err := stdcopy.StdCopy(fw, fw, rc); err != nil && !errors.Is(err, context.Canceled) {
			log.Debug("runtime logs stream ended", slog.Any("err", err))
		}
	}
}

// flushWriter flushes after every write so tailed lines reach the client live.
type flushWriter struct {
	w io.Writer
	f http.Flusher
}

func (fw *flushWriter) Write(p []byte) (int, error) {
	n, err := fw.w.Write(p)
	if fw.f != nil {
		fw.f.Flush()
	}
	return n, err
}
