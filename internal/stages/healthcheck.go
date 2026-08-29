package stages

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/client"
	"github.com/jackc/pgx/v5"

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

// HealthCheck gates traffic on the new deployment being ready before the old
// deployment is retired. A container that declares its own healthcheck is
// judged by that healthcheck; anything else is probed over HTTP by container IP
// (deploy ports are unpublished). Either way a failure fails the stage → the
// new container is rolled back and the previous deploy keeps serving.
type HealthCheck struct {
	docker    *client.Client
	inspector healthInspector
	q         *dbgen.Queries
	logs      *logstream.Writer
}

func NewHealthCheck(docker *client.Client, q *dbgen.Queries, logs *logstream.Writer) *HealthCheck {
	return &HealthCheck{docker: docker, inspector: dockerInspector{docker: docker}, q: q, logs: logs}
}

func (*HealthCheck) Name() string { return "health_check" }

func (h *HealthCheck) Run(ctx context.Context, dep *pipeline.Deployment) error {
	target, err := resolveHealthTarget(h.q.GetWebServiceByDeployment(ctx, db.MustUUID(dep.ID)))
	if err != nil {
		return err
	}
	if target.skip != "" {
		h.logLine(ctx, dep.ID, target.skip)
		return nil
	}

	declared, err := h.inspector.InspectHealth(ctx, target.containerID)
	if err != nil {
		return fmt.Errorf("inspect container health: %w", err)
	}
	if classifyHealth(declared) != verdictProbeHTTP {
		h.logLine(ctx, dep.ID, fmt.Sprintf("health check: container declares a healthcheck; waiting on docker (status: %s)", declared.status))
		waiter := healthWaiter{
			inspector: h.inspector,
			timeout:   healthTimeout,
			interval:  healthInterval,
			log:       func(line string) { h.logLine(ctx, dep.ID, line) },
		}
		return waiter.wait(ctx, target.containerID)
	}

	ip, err := h.containerIP(ctx, target.containerID)
	if err != nil {
		return fmt.Errorf("resolve container ip: %w", err)
	}
	url := fmt.Sprintf("http://%s:%d/", ip, target.port)
	h.logLine(ctx, dep.ID, "health check: no container healthcheck; probing "+url)

	httpClient := &http.Client{Timeout: healthProbe}
	deadline := time.Now().Add(healthTimeout)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return fmt.Errorf("build health probe request: %w", err)
		}
		resp, err := httpClient.Do(req)
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

// healthTarget is what the web-service lookup resolved to. A non-empty skip is
// a deployment with nothing to check — no web service at all, or one whose
// container was never recorded — and is a pass, not a failure.
type healthTarget struct {
	containerID string
	port        int32
	skip        string
}

// resolveHealthTarget folds the three lookup outcomes. ErrNoRows means the run
// stage found no service exposing a port; that unit is a worker, so there is no
// HTTP surface to probe and inventing defaultPort would fail a healthy deploy.
func resolveHealthTarget(svc dbgen.GetWebServiceByDeploymentRow, err error) (healthTarget, error) {
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return healthTarget{skip: "no service exposes a port; nothing to health check"}, nil
	case err != nil:
		return healthTarget{}, fmt.Errorf("lookup web service: %w", err)
	}
	if svc.ContainerID == nil || *svc.ContainerID == "" {
		return healthTarget{skip: "no web container recorded; skipping health check"}, nil
	}
	port := int32(defaultPort)
	if svc.Port != nil && *svc.Port != 0 {
		port = *svc.Port
	}
	return healthTarget{containerID: *svc.ContainerID, port: port}, nil
}

// containerHealth is the whole of what the decision below needs from Docker:
// whether the container declares a healthcheck and its latest status.
type containerHealth struct {
	configured bool
	status     string
}

type healthInspector interface {
	InspectHealth(ctx context.Context, containerID string) (containerHealth, error)
}

type dockerInspector struct {
	docker *client.Client
}

func (d dockerInspector) InspectHealth(ctx context.Context, containerID string) (containerHealth, error) {
	info, err := d.docker.ContainerInspect(ctx, containerID)
	if err != nil {
		return containerHealth{}, err
	}
	return healthFromState(info.State), nil
}

func healthFromState(state *types.ContainerState) containerHealth {
	if state == nil || state.Health == nil {
		return containerHealth{}
	}
	return containerHealth{configured: true, status: state.Health.Status}
}

type healthVerdict int

const (
	verdictProbeHTTP healthVerdict = iota
	verdictWait
	verdictPass
	verdictFail
)

func classifyHealth(h containerHealth) healthVerdict {
	if !h.configured || h.status == "" || h.status == types.NoHealthcheck {
		return verdictProbeHTTP
	}
	switch h.status {
	case types.Healthy:
		return verdictPass
	case types.Unhealthy:
		return verdictFail
	default:
		// "starting" is the normal opening state, so it must keep waiting; an
		// unrecognised status from a newer daemon waits too, and the deadline —
		// never a string we failed to recognise — is what ends the stage.
		return verdictWait
	}
}

type healthWaiter struct {
	inspector healthInspector
	timeout   time.Duration
	interval  time.Duration
	log       func(string)
}

func (w healthWaiter) wait(ctx context.Context, containerID string) error {
	deadline := time.Now().Add(w.timeout)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		cur, err := w.inspector.InspectHealth(ctx, containerID)
		if err == nil {
			switch classifyHealth(cur) {
			case verdictPass:
				w.log("health check: ok (docker healthcheck reports healthy)")
				return nil
			case verdictFail:
				return errors.New("container healthcheck reported unhealthy")
			}
		}
		if time.Now().After(deadline) {
			if err != nil {
				return fmt.Errorf("health check timed out after %s: %w", w.timeout, err)
			}
			return fmt.Errorf("health check timed out after %s: container healthcheck still %q", w.timeout, cur.status)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(w.interval):
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
