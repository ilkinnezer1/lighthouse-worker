package compose

import (
	"testing"

	"github.com/compose-spec/compose-go/v2/types"
)

func project(services map[string][]uint32) *types.Project {
	p := &types.Project{Services: types.Services{}}
	for name, targets := range services {
		svc := types.ServiceConfig{Name: name}
		for _, t := range targets {
			svc.Ports = append(svc.Ports, types.ServicePortConfig{Target: t})
		}
		p.Services[name] = svc
	}
	return p
}

func TestWebServicePrefersPreferredPort(t *testing.T) {
	p := project(map[string][]uint32{
		"api":   {3000},
		"proxy": {80},
	})
	name, port := WebService(p, 80)
	if name != "proxy" || port != 80 {
		t.Fatalf("WebService = (%q, %d), want (proxy, 80)", name, port)
	}
}

func TestWebServiceFallsBackToFirstWithPorts(t *testing.T) {
	p := project(map[string][]uint32{
		"worker": nil,
		"api":    {3000},
	})
	name, port := WebService(p, 80)
	if name != "api" || port != 3000 {
		t.Fatalf("WebService = (%q, %d), want (api, 3000)", name, port)
	}
}

func TestWebServiceIsNoneWhenNoServiceDeclaresAPort(t *testing.T) {
	p := project(map[string][]uint32{
		"media-worker": nil,
		"cron":         nil,
	})
	name, port := WebService(p, 80)
	if name != "" || port != 0 {
		t.Fatalf("WebService = (%q, %d), want (\"\", 0) — a portless project has no web service", name, port)
	}
}

func TestWebServiceIsNoneForEmptyProject(t *testing.T) {
	if name, port := WebService(project(nil), 80); name != "" || port != 0 {
		t.Fatalf("WebService = (%q, %d), want (\"\", 0)", name, port)
	}
}

func TestFirstService(t *testing.T) {
	p := project(map[string][]uint32{"worker": nil, "cron": nil, "api": nil})
	if got := FirstService(p); got != "api" {
		t.Fatalf("FirstService = %q, want api (alphabetically first)", got)
	}
	if got := FirstService(project(nil)); got != "" {
		t.Fatalf("FirstService on empty project = %q, want empty", got)
	}
}
