package httpx

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/coder/websocket"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"

	"github.com/ilkinnezer1/lighthouse-worker/internal/db"
	dbgen "github.com/ilkinnezer1/lighthouse-worker/internal/db/gen"
)

// Prefer bash, fall back to sh. The path is resolved up front instead of
// `exec bash 2>/dev/null` so stderr stays on the TTY: bash checks isatty(2)
// to decide it's interactive, and without it there is no prompt or line editing.
var execShell = []string{"/bin/sh", "-c", "exec $(command -v bash || command -v sh)"}

const (
	execDefaultCols = 80
	execDefaultRows = 24
	// Output is forwarded in chunks of this size; a terminal is bursty but small.
	execReadBuf = 32 * 1024
)

// containerExec opens an interactive shell inside a deployment's container over
// a WebSocket. The API proxies this from the browser (socket.io ↔ this WS), so
// the trust boundary is the same as runtime logs — authz happens in the API.
//
// Wire protocol (both directions framed as WS messages):
//   - binary message  → raw terminal bytes (keystrokes in / pty output out)
//   - text message    → JSON control frame, currently {"type":"resize","cols":N,"rows":N}
//
// The exec runs with a TTY, so Docker does NOT multiplex the stream: the attach
// reader is a single raw byte stream copied straight to the client.
func containerExec(docker *client.Client, q *dbgen.Queries, log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		depID, err := db.ParseUUID(r.PathValue("id"))
		if err != nil {
			http.Error(w, "invalid deployment id", http.StatusBadRequest)
			return
		}
		name := r.PathValue("name")

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

		// The worker sits behind the API on an internal network; the browser
		// never connects here directly, so skip the same-origin check.
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			log.Debug("ws accept failed", slog.Any("err", err))
			return
		}
		// CloseNow is idempotent; the deferred call is the backstop teardown.
		defer func() { _ = conn.CloseNow() }()

		// One session-scoped context: either side ending tears down the other.
		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()

		cols, rows := execInitialSize(r)

		execID, err := docker.ContainerExecCreate(ctx, *containerID, container.ExecOptions{
			Cmd:          execShell,
			Tty:          true,
			AttachStdin:  true,
			AttachStdout: true,
			AttachStderr: true,
			Env:          []string{"TERM=xterm-256color"},
		})
		if err != nil {
			_ = conn.Close(websocket.StatusInternalError, "exec create failed")
			return
		}

		attach, err := docker.ContainerExecAttach(ctx, execID.ID, container.ExecStartOptions{Tty: true})
		if err != nil {
			_ = conn.Close(websocket.StatusInternalError, "exec attach failed")
			return
		}
		defer attach.Close()

		// Allow large pastes; coder/websocket caps reads at 32KiB by default.
		conn.SetReadLimit(1 << 20)

		// Best-effort initial sizing before the client's first resize arrives.
		_ = docker.ContainerExecResize(ctx, execID.ID, container.ResizeOptions{Height: rows, Width: cols})

		// Input pump runs in the background; the output pump below owns ALL WS
		// writes (coder/websocket forbids concurrent writers). When the client
		// disconnects, the input loop closes the attach conn to unblock the
		// output pump's raw Read (ctx cancel alone can't interrupt it).
		go func() {
			execInputLoop(ctx, conn, attach.Conn, docker, execID.ID, log)
			cancel()
			attach.Close()
		}()

		// Output pump: pty → client. Sole writer, so it also sends the exit frame.
		buf := make([]byte, execReadBuf)
		for {
			n, rerr := attach.Reader.Read(buf)
			if n > 0 {
				if werr := conn.Write(ctx, websocket.MessageBinary, buf[:n]); werr != nil {
					break
				}
			}
			if rerr != nil {
				break
			}
		}
		cancel()

		// Close the attach before inspecting: Docker reports the exec Running
		// until its streams close, so this reaps it immediately instead of
		// waiting on the input goroutine's late Close. Double Close is harmless.
		attach.Close()

		// Poll for the exit code first, THEN take a fresh 2s write deadline so the
		// poll can't burn the deadline and blow the exit-frame write.
		code := execExitCode(docker, execID.ID)
		wctx, wcancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = conn.Write(wctx, websocket.MessageText, execExitFrame(code))
		wcancel()
		_ = conn.Close(websocket.StatusNormalClosure, "session ended")
	}
}

// execInputLoop reads client frames until the connection closes: binary frames
// are written to the pty stdin, text frames are decoded as control messages.
func execInputLoop(ctx context.Context, conn *websocket.Conn, stdin io.Writer, docker *client.Client, execID string, log *slog.Logger) {
	for {
		typ, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		switch typ {
		case websocket.MessageBinary:
			if _, werr := stdin.Write(data); werr != nil {
				return
			}
		case websocket.MessageText:
			var ctrl struct {
				Type string `json:"type"`
				Cols uint   `json:"cols,omitempty"`
				Rows uint   `json:"rows,omitempty"`
			}
			if jerr := json.Unmarshal(data, &ctrl); jerr != nil {
				log.Debug("bad control frame", slog.Any("err", jerr))
				continue
			}
			if ctrl.Type == "resize" && ctrl.Cols > 0 && ctrl.Rows > 0 {
				// Short timeout: a resize must never block keystroke handling.
				rctx, rcancel := context.WithTimeout(ctx, 5*time.Second)
				_ = docker.ContainerExecResize(rctx, execID, container.ResizeOptions{Height: ctrl.Rows, Width: ctrl.Cols})
				rcancel()
			}
		}
	}
}

// execInitialSize reads an optional starting terminal size from query params,
// falling back to a conventional 80x24.
func execInitialSize(r *http.Request) (cols, rows uint) {
	cols, rows = execDefaultCols, execDefaultRows
	if v, err := strconv.ParseUint(r.URL.Query().Get("cols"), 10, 16); err == nil && v > 0 {
		cols = uint(v)
	}
	if v, err := strconv.ParseUint(r.URL.Query().Get("rows"), 10, 16); err == nil && v > 0 {
		rows = uint(v)
	}
	return cols, rows
}

// execExitCode inspects the finished exec; -1 means the code is unknown (e.g.
// the inspect failed or the exec was still running when the client left).
// Docker often hasn't reaped the exec the instant the pty hits EOF, so poll
// until it's no longer Running before trusting ExitCode.
func execExitCode(docker *client.Client, execID string) int {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for {
		info, err := docker.ContainerExecInspect(ctx, execID)
		if err != nil {
			return -1
		}
		if !info.Running {
			return info.ExitCode
		}
		select {
		case <-ctx.Done():
			return -1
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func execExitFrame(code int) []byte {
	b, _ := json.Marshal(map[string]any{"type": "exit", "code": code})
	return b
}
