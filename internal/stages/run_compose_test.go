package stages

import (
	"testing"

	composetypes "github.com/compose-spec/compose-go/v2/types"
)

func composeProjectWith(services map[string][]uint32) *composetypes.Project {
	p := &composetypes.Project{Services: composetypes.Services{}}
	for name, targets := range services {
		svc := composetypes.ServiceConfig{Name: name}
		for _, t := range targets {
			svc.Ports = append(svc.Ports, composetypes.ServicePortConfig{Target: t})
		}
		p.Services[name] = svc
	}
	return p
}

func TestResolveWebNoServiceExposesAPort(t *testing.T) {
	p := composeProjectWith(map[string][]uint32{"media-worker": nil})
	web, port, err := resolveWeb(p, buildOverrides{})
	if err != nil {
		t.Fatalf("resolveWeb = %v, want no error (a worker-only unit is legitimate)", err)
	}
	if web != "" || port != 0 {
		t.Fatalf("resolveWeb = (%q, %d), want (\"\", 0)", web, port)
	}
}

func TestResolveWebEmptyProjectIsStillAnError(t *testing.T) {
	if _, _, err := resolveWeb(composeProjectWith(nil), buildOverrides{}); err == nil {
		t.Fatal("resolveWeb on a project with zero services must error")
	}
}

func TestResolveWebExplicitPortOptsAPortlessProjectIn(t *testing.T) {
	p := composeProjectWith(map[string][]uint32{"app": nil, "sidecar": nil})
	web, port, err := resolveWeb(p, buildOverrides{Port: 3000})
	if err != nil {
		t.Fatalf("resolveWeb = %v", err)
	}
	if web != "app" || port != 3000 {
		t.Fatalf("resolveWeb = (%q, %d), want (app, 3000)", web, port)
	}
}

func TestResolveWebExplicitWebServiceUnchanged(t *testing.T) {
	p := composeProjectWith(map[string][]uint32{"api": {3000}, "worker": nil})
	web, port, err := resolveWeb(p, buildOverrides{Web: "api"})
	if err != nil {
		t.Fatalf("resolveWeb = %v", err)
	}
	if web != "api" || port != 3000 {
		t.Fatalf("resolveWeb = (%q, %d), want (api, 3000)", web, port)
	}
	if _, _, err := resolveWeb(p, buildOverrides{Web: "nope"}); err == nil {
		t.Fatal("buildConfig.web naming a missing service must error")
	}
}

func TestResolveWebAutoDetectUnchanged(t *testing.T) {
	p := composeProjectWith(map[string][]uint32{"api": {3000}, "worker": nil})
	web, port, err := resolveWeb(p, buildOverrides{})
	if err != nil {
		t.Fatalf("resolveWeb = %v", err)
	}
	if web != "api" || port != 3000 {
		t.Fatalf("resolveWeb = (%q, %d), want (api, 3000)", web, port)
	}

	// A web service found without a target port still falls back to defaultPort.
	p = composeProjectWith(map[string][]uint32{"api": {0}})
	if web, port, _ = resolveWeb(p, buildOverrides{}); web != "api" || port != defaultPort {
		t.Fatalf("resolveWeb = (%q, %d), want (api, %d)", web, port, defaultPort)
	}
}
