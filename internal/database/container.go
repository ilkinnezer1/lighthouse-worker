package database

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/errdefs"
)

const readyInterval = time.Second

// containerSpec is the engine-agnostic shape of a managed-database container.
type containerSpec struct {
	name        string
	image       string
	networkName string
	env         []string
	cmd         []string
	volumeName  string
	mountTarget string // engine data dir, e.g. /var/lib/postgresql/data
}

// startEngineContainer creates and starts a managed-database container from spec,
// tolerating a redelivered job (reuses an existing container of the same name).
func (r *Router) startEngineContainer(ctx context.Context, spec containerSpec) (string, error) {
	cfg := &container.Config{Image: spec.image, Env: spec.env, Cmd: spec.cmd}
	hostCfg := &container.HostConfig{
		Mounts: []mount.Mount{{
			Type:   mount.TypeVolume,
			Source: spec.volumeName,
			Target: spec.mountTarget,
		}},
		RestartPolicy: container.RestartPolicy{Name: "unless-stopped"},
	}
	netCfg := &network.NetworkingConfig{
		EndpointsConfig: map[string]*network.EndpointSettings{spec.networkName: {}},
	}

	created, err := r.docker.ContainerCreate(ctx, cfg, hostCfg, netCfg, nil, spec.name)
	if err != nil {
		if !isAlreadyExists(err) {
			return "", fmt.Errorf("container create: %w", err)
		}
		inspect, ierr := r.docker.ContainerInspect(ctx, spec.name)
		if ierr != nil {
			return "", fmt.Errorf("inspect existing container: %w", ierr)
		}
		created.ID = inspect.ID
	}
	if err := r.docker.ContainerStart(ctx, created.ID, container.StartOptions{}); err != nil && !isAlreadyStarted(err) {
		return "", fmt.Errorf("container start: %w", err)
	}
	return created.ID, nil
}

func (r *Router) ensureImage(ctx context.Context, ref string, log *slog.Logger) error {
	log.Info("ensuring image", slog.String("image", ref))
	rc, err := r.docker.ImagePull(ctx, ref, image.PullOptions{})
	if err != nil {
		return err
	}
	defer rc.Close()
	if _, err := io.Copy(io.Discard, rc); err != nil {
		return err
	}
	return nil
}

// execOK runs cmd inside the container and reports whether it exited 0. Shared
// by the per-engine readiness probes (pg_isready, mysqladmin ping, redis-cli
// ping, mongosh ping).
func (r *Router) execOK(ctx context.Context, containerID string, cmd []string) bool {
	exec, err := r.docker.ContainerExecCreate(ctx, containerID, container.ExecOptions{Cmd: cmd})
	if err != nil {
		return false
	}
	if err := r.docker.ContainerExecStart(ctx, exec.ID, container.ExecStartOptions{}); err != nil {
		return false
	}
	for i := 0; i < 10; i++ {
		info, err := r.docker.ContainerExecInspect(ctx, exec.ID)
		if err != nil {
			return false
		}
		if !info.Running {
			return info.ExitCode == 0
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

// waitUntilReady polls probe every readyInterval until it returns true or the
// timeout elapses. label names the engine probe in the timeout error.
func (r *Router) waitUntilReady(ctx context.Context, label string, timeout time.Duration, probe func(context.Context) bool) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return err
		}
		if probe(ctx) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(readyInterval):
		}
	}
	return fmt.Errorf("%s: not ready after %s", label, timeout)
}

func randomPassword() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return strings.TrimRight(base64.URLEncoding.EncodeToString(b), "="), nil
}

func isAlreadyExists(err error) bool {
	if err == nil {
		return false
	}
	if errdefs.IsConflict(err) {
		return true
	}
	return strings.Contains(err.Error(), "already in use")
}

func isAlreadyStarted(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "is already started")
}
