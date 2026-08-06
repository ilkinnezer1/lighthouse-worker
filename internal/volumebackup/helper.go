package volumebackup

import (
	"context"
	"fmt"
	"io"
	"log/slog"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/mount"
)

// ensureHelperImage pulls the tar host image if it isn't present locally.
func (r *Router) ensureHelperImage(ctx context.Context) error {
	if _, _, err := r.docker.ImageInspectWithRaw(ctx, helperImage); err == nil {
		return nil
	}
	rc, err := r.docker.ImagePull(ctx, helperImage, image.PullOptions{})
	if err != nil {
		return fmt.Errorf("pull %s: %w", helperImage, err)
	}
	defer rc.Close()
	if _, err := io.Copy(io.Discard, rc); err != nil {
		return fmt.Errorf("pull %s: %w", helperImage, err)
	}
	return nil
}

// createHelper makes (but does not start) a throwaway container with the volume
// mounted at mountPath. readOnly guards the backup direction so a bug in the tar
// command can't write into the user's data.
func (r *Router) createHelper(
	ctx context.Context,
	dockerVolume string,
	cmd []string,
	readOnly bool,
	attachStdin bool,
) (string, error) {
	resp, err := r.docker.ContainerCreate(ctx,
		&container.Config{
			Image:        helperImage,
			Cmd:          cmd,
			AttachStdin:  attachStdin,
			OpenStdin:    attachStdin,
			StdinOnce:    attachStdin,
			AttachStdout: true,
			AttachStderr: true,
			// No network: this container only ever moves bytes over the Docker
			// attach socket, so it has no business reaching anything.
			NetworkDisabled: true,
		},
		&container.HostConfig{
			AutoRemove: false,
			Mounts: []mount.Mount{{
				Type:     mount.TypeVolume,
				Source:   dockerVolume,
				Target:   mountPath,
				ReadOnly: readOnly,
			}},
		}, nil, nil, "")
	if err != nil {
		return "", fmt.Errorf("create helper container: %w", err)
	}
	return resp.ID, nil
}

func (r *Router) removeHelper(ctx context.Context, id string) {
	if err := r.docker.ContainerRemove(ctx, id, container.RemoveOptions{Force: true}); err != nil {
		r.log.Warn("remove helper container", slog.String("id", id), slog.Any("err", err))
	}
}

// waitHelper blocks until the helper exits and turns a non-zero exit into an
// error carrying whatever the container wrote to stderr.
func (r *Router) waitHelper(ctx context.Context, id, stderr string) error {
	okCh, errCh := r.docker.ContainerWait(ctx, id, container.WaitConditionNotRunning)
	select {
	case err := <-errCh:
		return fmt.Errorf("wait helper: %w", err)
	case status := <-okCh:
		if status.StatusCode != 0 {
			if stderr != "" {
				return fmt.Errorf("tar exited %d: %s", status.StatusCode, stderr)
			}
			return fmt.Errorf("tar exited %d", status.StatusCode)
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// containersUsing lists running containers with this volume mounted. Used to
// freeze writers during a backup and to stop them during a restore.
func (r *Router) containersUsing(ctx context.Context, dockerVolume string) ([]string, error) {
	list, err := r.docker.ContainerList(ctx, container.ListOptions{
		Filters: filters.NewArgs(filters.Arg("volume", dockerVolume)),
	})
	if err != nil {
		return nil, fmt.Errorf("list containers using %s: %w", dockerVolume, err)
	}
	ids := make([]string, 0, len(list))
	for _, c := range list {
		ids = append(ids, c.ID)
	}
	return ids, nil
}
