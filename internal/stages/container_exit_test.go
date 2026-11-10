package stages

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/api/types"
)

type fakeLiveness struct {
	exits    []containerExit
	calls    int
	logs     []string
	logsErr  error
	tailedBy int
}

func (f *fakeLiveness) ExitState(context.Context, string) (containerExit, error) {
	i := min(f.calls, len(f.exits)-1)
	f.calls++
	return f.exits[i], nil
}

func (f *fakeLiveness) TailLogs(_ context.Context, _ string, n int) ([]string, error) {
	f.tailedBy = n
	return f.logs, f.logsErr
}

func newProber(probe func(context.Context) error, live containerLiveness, lines *[]string) httpProber {
	return httpProber{
		probe:    probe,
		liveness: live,
		port:     8080,
		timeout:  20 * time.Millisecond,
		interval: time.Millisecond,
		log:      func(l string) { *lines = append(*lines, l) },
	}
}

func refused(context.Context) error {
	return errors.New("dial tcp 172.25.0.9:8080: connect: connection refused")
}

func TestHTTPProberFailsFastWhenContainerExits(t *testing.T) {
	live := &fakeLiveness{
		exits: []containerExit{{}, {exited: true, code: 255}},
		logs:  []string{"PHP Fatal error:  Uncaught Error: Failed opening required 'vendor/autoload.php'", "  thrown in /app/artisan on line 18", ""},
	}
	var lines []string
	err := newProber(refused, live, &lines).wait(context.Background(), "c1")
	want := "container exited (code 255) before answering on port 8080: thrown in /app/artisan on line 18"
	if err == nil || err.Error() != want {
		t.Fatalf("wait = %v, want %q", err, want)
	}
	if live.tailedBy != exitLogTailLines {
		t.Fatalf("tailed %d lines, want %d", live.tailedBy, exitLogTailLines)
	}
	if !strings.Contains(strings.Join(lines, "\n"), "Failed opening required") {
		t.Fatalf("container logs not written to the deploy log: %q", lines)
	}
}

func TestHTTPProberSilentAppTimesOutWithHumanMessage(t *testing.T) {
	var lines []string
	err := newProber(refused, &fakeLiveness{exits: []containerExit{{}}}, &lines).wait(context.Background(), "c1")
	if err == nil || err.Error() != "app did not answer on port 8080 within 20ms" {
		t.Fatalf("wait = %v", err)
	}
}

func TestHTTPProberPassesOnAnswer(t *testing.T) {
	live := &fakeLiveness{exits: []containerExit{{}}}
	var lines []string
	if err := newProber(func(context.Context) error { return nil }, live, &lines).wait(context.Background(), "c1"); err != nil {
		t.Fatalf("wait = %v, want nil", err)
	}
	if live.calls != 0 {
		t.Fatalf("liveness checked %d times on a passing probe", live.calls)
	}
}

func TestHTTPProberExitWithUnreadableLogs(t *testing.T) {
	live := &fakeLiveness{exits: []containerExit{{exited: true, code: 1}}, logsErr: errors.New("gone")}
	var lines []string
	err := newProber(refused, live, &lines).wait(context.Background(), "c1")
	if err == nil || err.Error() != "container exited (code 1) before answering on port 8080: no output" {
		t.Fatalf("wait = %v", err)
	}
}

func TestExitFromInspect(t *testing.T) {
	inspect := func(state types.ContainerState, restarts int) types.ContainerJSON {
		return types.ContainerJSON{ContainerJSONBase: &types.ContainerJSONBase{State: &state, RestartCount: restarts}}
	}
	cases := []struct {
		name string
		in   types.ContainerJSON
		want containerExit
	}{
		{"running", inspect(types.ContainerState{Status: "running", Running: true}, 0), containerExit{}},
		{"exited", inspect(types.ContainerState{Status: "exited", ExitCode: 1}, 0), containerExit{exited: true, code: 1}},
		{"restarting", inspect(types.ContainerState{Status: "restarting", Restarting: true, ExitCode: 255}, 3), containerExit{exited: true, code: 255}},
		{"running after a restart", inspect(types.ContainerState{Status: "running", Running: true, ExitCode: 255}, 1), containerExit{exited: true, code: 255}},
		{"no state", types.ContainerJSON{}, containerExit{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := exitFromInspect(c.in); got != c.want {
				t.Fatalf("exitFromInspect = %+v, want %+v", got, c.want)
			}
		})
	}
}

func TestLastLogLineTrims(t *testing.T) {
	long := strings.Repeat("x", 300)
	if got := lastLogLine([]string{"a", long, "   "}); got != strings.Repeat("x", exitSummaryMaxLen) {
		t.Fatalf("lastLogLine len = %d, want %d", len(got), exitSummaryMaxLen)
	}
}

func TestDropRuntimeExecNoise(t *testing.T) {
	in := "Migrating: 2024_01_01\nOCI runtime exec failed: exec failed: write init-p: broken pipe\nDone\n"
	out, err := io.ReadAll(dropRuntimeExecNoise(strings.NewReader(in)))
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "Migrating: 2024_01_01\nDone\n" {
		t.Fatalf("filtered = %q", out)
	}
}
