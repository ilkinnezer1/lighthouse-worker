package stages

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/jackc/pgx/v5"

	dbgen "github.com/ilkinnezer1/lighthouse-worker/internal/db/gen"
)

type inspectResult struct {
	health containerHealth
	err    error
}

// fakeInspector replays a script of inspects and then repeats the last one, so
// a test can express "starting, starting, healthy" without timing games.
type fakeInspector struct {
	script []inspectResult
	calls  int
}

func (f *fakeInspector) InspectHealth(context.Context, string) (containerHealth, error) {
	i := f.calls
	f.calls++
	if i >= len(f.script) {
		i = len(f.script) - 1
	}
	return f.script[i].health, f.script[i].err
}

func healthy(status string) containerHealth {
	return containerHealth{configured: true, status: status}
}

func verdictName(v healthVerdict) string {
	switch v {
	case verdictProbeHTTP:
		return "probeHTTP"
	case verdictWait:
		return "wait"
	case verdictPass:
		return "pass"
	case verdictFail:
		return "fail"
	}
	return "unknown"
}

func TestClassifyHealth(t *testing.T) {
	cases := []struct {
		name string
		in   containerHealth
		want healthVerdict
	}{
		{"no healthcheck configured falls through to http", containerHealth{}, verdictProbeHTTP},
		{"explicit none falls through to http", healthy(types.NoHealthcheck), verdictProbeHTTP},
		{"configured but blank status falls through to http", healthy(""), verdictProbeHTTP},
		{"healthy passes", healthy(types.Healthy), verdictPass},
		{"starting keeps waiting", healthy(types.Starting), verdictWait},
		{"unhealthy fails", healthy(types.Unhealthy), verdictFail},
		{"unknown status keeps waiting", healthy("reticulating"), verdictWait},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyHealth(tc.in); got != tc.want {
				t.Fatalf("classifyHealth(%+v) = %s, want %s", tc.in, verdictName(got), verdictName(tc.want))
			}
		})
	}
}

func TestHealthFromState(t *testing.T) {
	if got := healthFromState(nil); got.configured {
		t.Fatalf("nil state must not be configured: %+v", got)
	}
	if got := healthFromState(&types.ContainerState{Status: "running"}); got.configured {
		t.Fatalf("state without Health must not be configured: %+v", got)
	}
	got := healthFromState(&types.ContainerState{Health: &types.Health{Status: types.Starting}})
	if !got.configured || got.status != types.Starting {
		t.Fatalf("state with Health = %+v, want configured starting", got)
	}
}

func newWaiter(in healthInspector, lines *[]string) healthWaiter {
	return healthWaiter{
		inspector: in,
		timeout:   200 * time.Millisecond,
		interval:  time.Millisecond,
		log:       func(line string) { *lines = append(*lines, line) },
	}
}

func TestHealthWaiterPassesOnHealthy(t *testing.T) {
	in := &fakeInspector{script: []inspectResult{{health: healthy(types.Healthy)}}}
	var lines []string
	if err := newWaiter(in, &lines).wait(context.Background(), "c1"); err != nil {
		t.Fatalf("wait = %v, want nil", err)
	}
	if in.calls != 1 {
		t.Fatalf("inspect calls = %d, want 1", in.calls)
	}
	if len(lines) != 1 || !strings.Contains(lines[0], "docker healthcheck") {
		t.Fatalf("log lines = %q, want one naming the docker healthcheck", lines)
	}
}

func TestHealthWaiterKeepsWaitingWhileStarting(t *testing.T) {
	in := &fakeInspector{script: []inspectResult{
		{health: healthy(types.Starting)},
		{health: healthy(types.Starting)},
		{health: healthy(types.Healthy)},
	}}
	var lines []string
	if err := newWaiter(in, &lines).wait(context.Background(), "c1"); err != nil {
		t.Fatalf("wait = %v, want nil", err)
	}
	if in.calls != 3 {
		t.Fatalf("inspect calls = %d, want 3 (starting must not end the wait)", in.calls)
	}
}

func TestHealthWaiterFailsOnUnhealthy(t *testing.T) {
	in := &fakeInspector{script: []inspectResult{{health: healthy(types.Unhealthy)}}}
	var lines []string
	err := newWaiter(in, &lines).wait(context.Background(), "c1")
	if err == nil || !strings.Contains(err.Error(), "unhealthy") {
		t.Fatalf("wait = %v, want an unhealthy failure", err)
	}
	if in.calls != 1 {
		t.Fatalf("inspect calls = %d, want 1 (unhealthy must not be retried)", in.calls)
	}
}

func TestHealthWaiterTimesOutWhileStillStarting(t *testing.T) {
	in := &fakeInspector{script: []inspectResult{{health: healthy(types.Starting)}}}
	var lines []string
	err := newWaiter(in, &lines).wait(context.Background(), "c1")
	if err == nil || !strings.Contains(err.Error(), types.Starting) {
		t.Fatalf("wait = %v, want a timeout naming the last status", err)
	}
}

func TestHealthWaiterSurvivesTransientInspectError(t *testing.T) {
	in := &fakeInspector{script: []inspectResult{
		{err: errors.New("boom")},
		{health: healthy(types.Healthy)},
	}}
	var lines []string
	if err := newWaiter(in, &lines).wait(context.Background(), "c1"); err != nil {
		t.Fatalf("wait = %v, want nil", err)
	}
}

func TestHealthWaiterHonoursContextCancellation(t *testing.T) {
	in := &fakeInspector{script: []inspectResult{{health: healthy(types.Starting)}}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	var lines []string
	w := newWaiter(in, &lines)
	w.timeout = time.Hour
	if err := w.wait(ctx, "c1"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("wait = %v, want context.DeadlineExceeded", err)
	}
}

func TestResolveHealthTargetNoWebServiceSkips(t *testing.T) {
	got, err := resolveHealthTarget(dbgen.GetWebServiceByDeploymentRow{}, pgx.ErrNoRows)
	if err != nil {
		t.Fatalf("resolveHealthTarget = %v, want no error (a worker unit is not a failed deploy)", err)
	}
	if got.skip == "" {
		t.Fatal("no web service must skip, not probe")
	}
	if !strings.Contains(got.skip, "no service exposes a port") {
		t.Fatalf("skip reason = %q, want it to say why", got.skip)
	}
}

func TestResolveHealthTargetNoContainerSkips(t *testing.T) {
	blank := ""
	for _, row := range []dbgen.GetWebServiceByDeploymentRow{{}, {ContainerID: &blank}} {
		got, err := resolveHealthTarget(row, nil)
		if err != nil || got.skip == "" {
			t.Fatalf("row %+v: got (%+v, %v), want a skip", row, got, err)
		}
	}
}

func TestResolveHealthTargetRealLookupErrorFails(t *testing.T) {
	_, err := resolveHealthTarget(dbgen.GetWebServiceByDeploymentRow{}, errors.New("connection refused"))
	if err == nil {
		t.Fatal("a real lookup error must fail the stage, not skip it")
	}
}

func TestResolveHealthTargetPortDefaulting(t *testing.T) {
	cid, zero, real := "abc123", int32(0), int32(3000)
	cases := []struct {
		name string
		port *int32
		want int32
	}{
		{"nil port falls back to default", nil, defaultPort},
		{"zero port falls back to default", &zero, defaultPort},
		{"recorded port is used", &real, 3000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveHealthTarget(dbgen.GetWebServiceByDeploymentRow{ContainerID: &cid, Port: tc.port}, nil)
			if err != nil || got.skip != "" {
				t.Fatalf("got (%+v, %v), want a probe target", got, err)
			}
			if got.containerID != cid || got.port != tc.want {
				t.Fatalf("target = %+v, want container %s port %d", got, cid, tc.want)
			}
		})
	}
}
