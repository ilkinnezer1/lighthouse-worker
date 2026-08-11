package stages

import (
	"context"
	"fmt"
	"io"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/client"
	"github.com/docker/docker/errdefs"
	"github.com/docker/docker/pkg/stdcopy"

	"github.com/ilkinnezer1/lighthouse-worker/internal/crypto"
	"github.com/ilkinnezer1/lighthouse-worker/internal/logstream"
	"github.com/ilkinnezer1/lighthouse-worker/internal/pipeline"
)

const (
	setupWorkDir      = "/app"
	setupDefaultImage = "alpine"
	dockerSock        = "/var/run/docker.sock"
)

// Setup runs project-defined bootstrap commands after the file overlays are in
// place and before the build, so a project's generator (e.g. `make setup`) can
// produce its docker-compose.yml/.env from the user's env-vars. Each step runs
// in a one-shot container with the deployment WorkDir bind-mounted at /app.
// Empty/absent setupSteps make this a no-op.
type Setup struct {
	docker *client.Client
	logs   *logstream.Writer
	cipher *crypto.Cipher
}

func NewSetup(docker *client.Client, logs *logstream.Writer, cipher *crypto.Cipher) *Setup {
	return &Setup{docker: docker, logs: logs, cipher: cipher}
}

func (*Setup) Name() string { return "setup" }

func (s *Setup) Run(ctx context.Context, dep *pipeline.Deployment) error {
	steps := parseBuildOverrides(dep.BuildConfig).SetupSteps
	if len(steps) == 0 {
		return nil
	}

	env, err := decodeUserEnvs(s.cipher, dep.EnvVarsEnc)
	if err != nil {
		return err
	}
	envs := envSlice(env)

	for i, step := range steps {
		if step.Command == "" {
			continue
		}
		s.logLine(ctx, dep.ID, fmt.Sprintf("setup step %d/%d: %s", i+1, len(steps), step.Command))
		if err := s.runStep(ctx, dep, step, envs); err != nil {
			return fmt.Errorf("setup step %d (%q): %w", i+1, step.Command, err)
		}
	}
	return nil
}

func (s *Setup) runStep(ctx context.Context, dep *pipeline.Deployment, step setupStep, envs []string) error {
	img := step.Image
	if img == "" {
		img = setupDefaultImage
	}

	mounts := []mount.Mount{{
		Type:   mount.TypeBind,
		Source: dep.WorkDir,
		Target: setupWorkDir,
	}}
	if step.Docker {
		mounts = append(mounts, mount.Mount{
			Type:   mount.TypeBind,
			Source: dockerSock,
			Target: dockerSock,
		})
	}

	cfg := &container.Config{
		Image:      img,
		Cmd:        []string{"sh", "-c", step.Command},
		Env:        envs,
		WorkingDir: setupWorkDir,
	}
	host := &container.HostConfig{Mounts: mounts}

	created, err := s.docker.ContainerCreate(ctx, cfg, host, nil, nil, "")
	if errdefs.IsNotFound(err) {
		s.logLine(ctx, dep.ID, "pulling image "+img)
		if perr := s.ensureImage(ctx, img); perr != nil {
			return fmt.Errorf("pull image %q: %w", img, perr)
		}
		created, err = s.docker.ContainerCreate(ctx, cfg, host, nil, nil, "")
	}
	if err != nil {
		return fmt.Errorf("create container: %w", err)
	}
	defer func() {
		_ = s.docker.ContainerRemove(ctx, created.ID, container.RemoveOptions{Force: true})
	}()

	if err := s.docker.ContainerStart(ctx, created.ID, container.StartOptions{}); err != nil {
		return fmt.Errorf("start container: %w", err)
	}

	s.streamLogs(ctx, dep.ID, created.ID)

	statusCh, errCh := s.docker.ContainerWait(ctx, created.ID, container.WaitConditionNotRunning)
	select {
	case err := <-errCh:
		return fmt.Errorf("wait container: %w", err)
	case st := <-statusCh:
		if st.StatusCode != 0 {
			return fmt.Errorf("exit %d", st.StatusCode)
		}
	case <-ctx.Done():
		return ctx.Err()
	}
	return nil
}

func (s *Setup) ensureImage(ctx context.Context, ref string) error {
	rc, err := s.docker.ImagePull(ctx, ref, image.PullOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = rc.Close() }()
	_, err = io.Copy(io.Discard, rc)
	return err
}

func (s *Setup) streamLogs(ctx context.Context, deploymentID, containerID string) {
	rc, err := s.docker.ContainerLogs(ctx, containerID, container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Follow:     true,
	})
	if err != nil {
		if !errdefs.IsNotFound(err) {
			s.logLine(ctx, deploymentID, "log attach failed: "+err.Error())
		}
		return
	}
	defer func() { _ = rc.Close() }()

	pr, pw := io.Pipe()
	go func() {
		_, _ = stdcopy.StdCopy(pw, pw, rc)
		_ = pw.Close()
	}()
	streamLines(ctx, s.logs, deploymentID, "setup", pr)
}

func (s *Setup) Rollback(_ context.Context, _ *pipeline.Deployment) error {
	return nil
}

func (s *Setup) logLine(ctx context.Context, deploymentID, line string) {
	_ = s.logs.WriteLine(ctx, deploymentID, "setup", line)
}
