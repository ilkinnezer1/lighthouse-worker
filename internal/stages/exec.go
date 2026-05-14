package stages

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os/exec"

	"github.com/ilkinnezer1/lighthouse-worker/internal/logstream"
)

func runCmd(ctx context.Context, cmd *exec.Cmd, logs *logstream.Writer, deploymentID, stage string) error {
	pipe, err := combinedPipe(cmd)
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start: %w", err)
	}
	streamLines(ctx, logs, deploymentID, stage, pipe)
	return cmd.Wait()
}

// git/docker write progress to stderr; merging keeps lines in order.
func combinedPipe(cmd *exec.Cmd) (io.Reader, error) {
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	cmd.Stderr = cmd.Stdout
	return stdout, nil
}

func streamLines(ctx context.Context, logs *logstream.Writer, deploymentID, stage string, r io.Reader) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		_ = logs.WriteLine(ctx, deploymentID, stage, scanner.Text())
	}
}
