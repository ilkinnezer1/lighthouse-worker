package stages

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/docker/docker/client"

	"github.com/ilkinnezer1/lighthouse-worker/internal/db"
	dbgen "github.com/ilkinnezer1/lighthouse-worker/internal/db/gen"
	"github.com/ilkinnezer1/lighthouse-worker/internal/logstream"
	"github.com/ilkinnezer1/lighthouse-worker/internal/pipeline"
)

const (
	healthTimeout  = 30 * time.Second
	healthInterval = time.Second
	healthProbe    = 2 * time.Second
)

// HealthCheck gates traffic on the new deployment being reachable. It probes the
// web container directly (by container IP, since deploy ports are unpublished)
// before the old deployment is retired. A timeout fails the stage → the new
// container is rolled back and the previous deploy keeps serving.
type HealthCheck struct {
	docker *client.Client
	q      *dbgen.Queries
	logs   *logstream.Writer
}

func NewHealthCheck(docker *client.Client, q *dbgen.Queries, logs *logstream.Writer) *HealthCheck {
	return &HealthCheck{docker: docker, q: q, logs: logs}
}

func (*HealthCheck) Name() string { return "health_check" }

func (h *HealthCheck) Run(ctx context.Context, dep *pipeline.Deployment) error {
	svc, err := h.q.GetWebServiceByDeployment(ctx, db.MustUUID(dep.ID))
	if err != nil {
		return fmt.Errorf("lookup web service: %w", err)
	}
	if svc.ContainerID == nil || *svc.ContainerID == "" {
		h.logLine(ctx, dep.ID, "no web container recorded; skipping health check")
		return nil
	}
	port := int32(defaultPort)
	if svc.Port != nil && *svc.Port != 0 {
		port = *svc.Port
	}

	ip, err := h.containerIP(ctx, *svc.ContainerID)
	if err != nil {
		return fmt.Errorf("resolve container ip: %w", err)
	}
	url := fmt.Sprintf("http://%s:%d/", ip, port)
	h.logLine(ctx, dep.ID, "health check: probing "+url)

	httpClient := &http.Client{Timeout: healthProbe}
	deadline := time.Now().Add(healthTimeout)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		resp, err := httpClient.Get(url)
		if err == nil {
			_ = resp.Body.Close()
			h.logLine(ctx, dep.ID, fmt.Sprintf("health check: ok (HTTP %d)", resp.StatusCode))
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("health check timed out after %s: %w", healthTimeout, err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(healthInterval):
		}
	}
}

// containerIP returns the container's address, preferring the public network
// and falling back to any attached network.
func (h *HealthCheck) containerIP(ctx context.Context, containerID string) (string, error) {
	info, err := h.docker.ContainerInspect(ctx, containerID)
	if err != nil {
		return "", err
	}
	if info.NetworkSettings != nil {
		if ep, ok := info.NetworkSettings.Networks[publicNetwork]; ok && ep.IPAddress != "" {
			return ep.IPAddress, nil
		}
		for _, ep := range info.NetworkSettings.Networks {
			if ep.IPAddress != "" {
				return ep.IPAddress, nil
			}
		}
	}
	return "", fmt.Errorf("container %s has no network ip", containerID[:12])
}

func (*HealthCheck) Rollback(context.Context, *pipeline.Deployment) error { return nil }

func (h *HealthCheck) logLine(ctx context.Context, deploymentID, line string) {
	_ = h.logs.WriteLine(ctx, deploymentID, "health_check", line)
}
