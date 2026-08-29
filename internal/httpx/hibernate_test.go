package httpx

import (
	"strings"
	"testing"
)

func names(targets []hibernationTarget) string {
	out := make([]string, len(targets))
	for i, t := range targets {
		out[i] = t.name
	}
	return strings.Join(out, ",")
}

func stack() []hibernationTarget {
	return []hibernationTarget{
		{name: "cache", containerID: "c1"},
		{name: "db", containerID: "c2"},
		{name: "web", containerID: "c3", isWeb: true},
		{name: "worker", containerID: "c4"},
	}
}

func TestOrderTargetsStopsWebFirst(t *testing.T) {
	got := names(orderTargets(stack(), true))
	if want := "web,cache,db,worker"; got != want {
		t.Fatalf("hibernate order = %q, want %q", got, want)
	}
}

func TestOrderTargetsStartsWebLast(t *testing.T) {
	got := names(orderTargets(stack(), false))
	if want := "cache,db,worker,web"; got != want {
		t.Fatalf("wake order = %q, want %q", got, want)
	}
}

func TestOrderTargetsWebAlreadyLastStillLeads(t *testing.T) {
	in := []hibernationTarget{
		{name: "db"},
		{name: "web", isWeb: true},
	}
	if got := names(orderTargets(in, true)); got != "web,db" {
		t.Fatalf("hibernate order = %q, want %q", got, "web,db")
	}
}

func TestOrderTargetsWebAlreadyFirstStillTrails(t *testing.T) {
	in := []hibernationTarget{
		{name: "web", isWeb: true},
		{name: "db"},
	}
	if got := names(orderTargets(in, false)); got != "db,web" {
		t.Fatalf("wake order = %q, want %q", got, "db,web")
	}
}

func TestOrderTargetsLeavesInputUntouched(t *testing.T) {
	in := stack()
	orderTargets(in, true)
	if got := names(in); got != "cache,db,web,worker" {
		t.Fatalf("input mutated: %q", got)
	}
}

func TestOrderTargetsWithoutWebService(t *testing.T) {
	in := []hibernationTarget{{name: "db"}, {name: "worker"}}
	for _, webFirst := range []bool{true, false} {
		if got := names(orderTargets(in, webFirst)); got != "db,worker" {
			t.Fatalf("webFirst=%v order = %q, want %q", webFirst, got, "db,worker")
		}
	}
}
