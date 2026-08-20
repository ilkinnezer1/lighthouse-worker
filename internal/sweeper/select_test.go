package sweeper

import (
	"testing"

	"github.com/docker/docker/api/types"
)

const (
	liveID     = "00ab31f8-4c21-4895-93ff-d6b00f0aea30"
	composeID  = "4d76b442-36f2-414c-9b11-8bfd6a48e1f4"
	staleID    = "f507febe-4eef-4abc-885f-adbe609431e7"
	unrelated  = "lighthouse-postgres"
	composeLbl = "com.docker.compose.project"
)

func TestLiveDeploymentIDsFromContainerName(t *testing.T) {
	live := LiveDeploymentIDs([]types.Container{
		{Names: []string{"/deploy-" + liveID}},
		{Names: []string{"/" + unrelated}},
	})
	if _, ok := live[liveID]; !ok {
		t.Fatalf("live deployment %s not detected: %v", liveID, live)
	}
	if len(live) != 1 {
		t.Fatalf("unexpected extra entries: %v", live)
	}
}

func TestLiveDeploymentIDsFromComposeProject(t *testing.T) {
	live := LiveDeploymentIDs([]types.Container{
		{
			Names:  []string{"/deploy-" + composeID + "-php-1"},
			Labels: map[string]string{composeLbl: "deploy-" + composeID},
		},
		{
			Names:  []string{"/deploy-" + composeID + "-nginx-1"},
			Labels: map[string]string{composeLbl: "deploy-" + composeID},
		},
	})
	if _, ok := live[composeID]; !ok {
		t.Fatalf("compose deployment %s not detected: %v", composeID, live)
	}
	// Per-service container names must not register as deployments of their own.
	if len(live) != 1 {
		t.Fatalf("service container names leaked into the live set: %v", live)
	}
}

func TestLiveDeploymentIDsIgnoresNonDeployment(t *testing.T) {
	live := LiveDeploymentIDs([]types.Container{
		{Names: []string{"/lighthouse-traefik"}, Labels: map[string]string{composeLbl: "lighthouse"}},
		{Names: []string{"/db-" + liveID}},
		{Names: []string{"/deploy-not-a-uuid"}},
		{Labels: map[string]string{composeLbl: "deploy-also-not-a-uuid"}},
	})
	if len(live) != 0 {
		t.Fatalf("expected no live deployments, got %v", live)
	}
}

func TestEligibleSkipsRunningDeployments(t *testing.T) {
	live := LiveDeploymentIDs([]types.Container{
		{Names: []string{"/deploy-" + liveID}},
		{Labels: map[string]string{composeLbl: "deploy-" + composeID}},
	})
	got := Eligible([]string{staleID, liveID, composeID, ""}, live)
	if len(got) != 1 || got[0] != staleID {
		t.Fatalf("Eligible = %v, want [%s]", got, staleID)
	}
}

func TestEligibleWithNoLiveContainers(t *testing.T) {
	got := Eligible([]string{staleID, liveID}, map[string]struct{}{})
	if len(got) != 2 {
		t.Fatalf("Eligible = %v, want both candidates", got)
	}
}
