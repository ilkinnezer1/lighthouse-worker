package database

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/pkg/stdcopy"
)

const (
	helperImage = "alpine:3.20"
	helperMount = "/data"
)

func (r *Router) ensureHelperImage(ctx context.Context) error {
	if _, _, err := r.docker.ImageInspectWithRaw(ctx, helperImage); err == nil {
		return nil
	}
	rc, err := r.docker.ImagePull(ctx, helperImage, image.PullOptions{})
	if err != nil {
		return fmt.Errorf("pull %s: %w", helperImage, err)
	}
	defer func() { _ = rc.Close() }()
	if _, err := io.Copy(io.Discard, rc); err != nil {
		return fmt.Errorf("pull %s: %w", helperImage, err)
	}
	return nil
}

// createDataHelper makes (but does not start) a throwaway container with an
// engine's data volume mounted. readOnly guards the backup direction so a bug in
// the tar command cannot write into the user's data.
func (r *Router) createDataHelper(
	ctx context.Context,
	volumeName, script string,
	readOnly, attachStdin bool,
) (string, error) {
	resp, err := r.docker.ContainerCreate(ctx,
		&container.Config{
			Image:        helperImage,
			Cmd:          []string{"sh", "-c", script},
			AttachStdin:  attachStdin,
			OpenStdin:    attachStdin,
			StdinOnce:    attachStdin,
			AttachStdout: true,
			AttachStderr: true,
			// This container only moves bytes over the Docker attach socket, so
			// it has no business reaching anything.
			NetworkDisabled: true,
		},
		&container.HostConfig{
			Mounts: []mount.Mount{{
				Type:     mount.TypeVolume,
				Source:   volumeName,
				Target:   helperMount,
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

// streamVolumeOut tars a data volume to this process as a stream. finish must be
// called once the reader is drained, so a helper that died partway cannot pass
// off a truncated archive as a backup.
func (r *Router) streamVolumeOut(ctx context.Context, volumeName, script string) (io.ReadCloser, func() error, error) {
	helperID, err := r.createDataHelper(ctx, volumeName, script, true, false)
	if err != nil {
		return nil, nil, err
	}
	attach, err := r.docker.ContainerAttach(ctx, helperID, container.AttachOptions{
		Stream: true,
		Stdout: true,
		Stderr: true,
	})
	if err != nil {
		r.removeHelper(context.WithoutCancel(ctx), helperID)
		return nil, nil, fmt.Errorf("attach helper: %w", err)
	}
	if err := r.docker.ContainerStart(ctx, helperID, container.StartOptions{}); err != nil {
		attach.Close()
		r.removeHelper(context.WithoutCancel(ctx), helperID)
		return nil, nil, fmt.Errorf("start helper: %w", err)
	}

	pr, pw := io.Pipe()
	var stderr bytes.Buffer
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		_, copyErr := stdcopy.StdCopy(pw, &stderr, attach.Reader)
		_ = pw.CloseWithError(copyErr)
	}()

	finish := func() error {
		<-drained
		attach.Close()
		defer r.removeHelper(context.WithoutCancel(ctx), helperID)
		return r.waitHelper(ctx, helperID, stderr.String())
	}
	return pr, finish, nil
}

// streamVolumeIn empties a data volume and untars an archive into it, feeding
// the object straight from storage into the helper's stdin.
func (r *Router) streamVolumeIn(ctx context.Context, volumeName, script string, src io.Reader) error {
	helperID, err := r.createDataHelper(ctx, volumeName, script, false, true)
	if err != nil {
		return err
	}
	defer r.removeHelper(context.WithoutCancel(ctx), helperID)

	attach, err := r.docker.ContainerAttach(ctx, helperID, container.AttachOptions{
		Stream: true,
		Stdin:  true,
		Stdout: true,
		Stderr: true,
	})
	if err != nil {
		return fmt.Errorf("attach helper: %w", err)
	}
	defer attach.Close()

	if err := r.docker.ContainerStart(ctx, helperID, container.StartOptions{}); err != nil {
		return fmt.Errorf("start helper: %w", err)
	}

	var stderr bytes.Buffer
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		_, _ = stdcopy.StdCopy(io.Discard, &stderr, attach.Reader)
	}()

	if _, err := io.Copy(attach.Conn, src); err != nil {
		return fmt.Errorf("stream archive to helper: %w", err)
	}
	if err := attach.CloseWrite(); err != nil {
		return fmt.Errorf("close helper stdin: %w", err)
	}
	<-drained

	return r.waitHelper(ctx, helperID, stderr.String())
}

func (r *Router) waitHelper(ctx context.Context, id, stderr string) error {
	okCh, errCh := r.docker.ContainerWait(ctx, id, container.WaitConditionNotRunning)
	select {
	case err := <-errCh:
		return fmt.Errorf("wait helper: %w", err)
	case status := <-okCh:
		if status.StatusCode != 0 {
			if s := strings.TrimSpace(stderr); s != "" {
				return fmt.Errorf("helper exited %d: %s", status.StatusCode, s)
			}
			return fmt.Errorf("helper exited %d", status.StatusCode)
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
