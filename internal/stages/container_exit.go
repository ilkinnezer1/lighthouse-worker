package stages

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
)

const (
	exitLogTailLines  = 20
	exitSummaryMaxLen = 200
)

type containerExit struct {
	exited bool
	code   int
}

type containerLiveness interface {
	ExitState(ctx context.Context, containerID string) (containerExit, error)
	TailLogs(ctx context.Context, containerID string, lines int) ([]string, error)
}

func (d dockerInspector) ExitState(ctx context.Context, containerID string) (containerExit, error) {
	return inspectExit(ctx, d.docker, containerID)
}

func (d dockerInspector) TailLogs(ctx context.Context, containerID string, lines int) ([]string, error) {
	rc, err := d.docker.ContainerLogs(ctx, containerID, container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Tail:       strconv.Itoa(lines),
	})
	if err != nil {
		return nil, err
	}
	defer func() { _ = rc.Close() }()
	var buf bytes.Buffer
	if _, err := stdcopy.StdCopy(&buf, &buf, rc); err != nil {
		return nil, err
	}
	return strings.Split(strings.TrimRight(buf.String(), "\n"), "\n"), nil
}

func inspectExit(ctx context.Context, docker *client.Client, containerID string) (containerExit, error) {
	info, err := docker.ContainerInspect(ctx, containerID)
	if err != nil {
		return containerExit{}, err
	}
	return exitFromInspect(info), nil
}

// a restart under unless-stopped means the process already died once, even if
// the inspect lands in the brief window where it is running again.
func exitFromInspect(info types.ContainerJSON) containerExit {
	if info.ContainerJSONBase == nil || info.State == nil {
		return containerExit{}
	}
	s := info.State
	if s.Restarting || s.Status == "exited" || s.Status == "dead" || info.RestartCount > 0 {
		return containerExit{exited: true, code: s.ExitCode}
	}
	return containerExit{}
}

// the runtime's "OCI runtime exec failed" line is never app output; the caller
// reports the container's exit in its place.
func dropRuntimeExecNoise(r io.Reader) io.Reader {
	pr, pw := io.Pipe()
	go func() {
		scanner := bufio.NewScanner(r)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "OCI runtime exec failed") {
				continue
			}
			if _, err := io.WriteString(pw, line+"\n"); err != nil {
				_, _ = io.Copy(io.Discard, r)
				return
			}
		}
		pw.CloseWithError(scanner.Err())
	}()
	return pr
}

func exitedBeforeAnswerError(code int, port int32, logLines []string) error {
	return fmt.Errorf("container exited (code %d) before answering on port %d: %s", code, port, lastLogLine(logLines))
}

func silentAppError(port int32, timeout time.Duration) error {
	return fmt.Errorf("app did not answer on port %d within %s", port, timeout)
}

func lastLogLine(lines []string) string {
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		if r := []rune(line); len(r) > exitSummaryMaxLen {
			line = string(r[:exitSummaryMaxLen])
		}
		return line
	}
	return "no output"
}

type httpProber struct {
	probe    func(ctx context.Context) error
	liveness containerLiveness
	port     int32
	timeout  time.Duration
	interval time.Duration
	log      func(string)
}

func (p httpProber) wait(ctx context.Context, containerID string) error {
	deadline := time.Now().Add(p.timeout)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if p.probe(ctx) == nil {
			return nil
		}
		if exit, err := p.liveness.ExitState(ctx, containerID); err == nil && exit.exited {
			return p.exitedError(ctx, containerID, exit.code)
		}
		if time.Now().After(deadline) {
			return silentAppError(p.port, p.timeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(p.interval):
		}
	}
}

func (p httpProber) exitedError(ctx context.Context, containerID string, code int) error {
	lines, err := p.liveness.TailLogs(ctx, containerID, exitLogTailLines)
	if err != nil {
		p.log("health check: could not read the container's logs: " + err.Error())
	} else {
		p.log(fmt.Sprintf("health check: container exited (code %d); its last log lines:", code))
		for _, line := range lines {
			p.log(line)
		}
	}
	return exitedBeforeAnswerError(code, p.port, lines)
}
