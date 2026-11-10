package compose

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
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

func writeComposeFile(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "docker-compose.yml"), []byte(body), 0o644); err != nil {
		t.Fatalf("write compose: %v", err)
	}
	return dir
}

const envFileCompose = `
services:
  api:
    image: alpine
    env_file: .env
  worker:
    image: alpine
    env_file:
      - .env
      - config/extra.env
`

func TestDeclaredEnvFilesReadsPathsWithoutOpeningThem(t *testing.T) {
	dir := writeComposeFile(t, envFileCompose)
	paths, err := DeclaredEnvFiles(t.Context(), dir, "", "test", nil)
	if err != nil {
		t.Fatalf("DeclaredEnvFiles = %v, want no error even though no env file exists", err)
	}
	want := []string{".env", "config/extra.env"}
	if !slices.Equal(paths, want) {
		t.Fatalf("DeclaredEnvFiles = %v, want %v", paths, want)
	}
}

// The reason DeclaredEnvFiles exists: the full load reads env_file off disk.
func TestParseFailsWhenADeclaredEnvFileIsMissing(t *testing.T) {
	dir := writeComposeFile(t, envFileCompose)
	if _, err := Parse(t.Context(), dir, "", "test", nil); err == nil {
		t.Fatal("Parse with a missing env_file must fail")
	}
}

func TestParseInterpolatesFromVars(t *testing.T) {
	dir := writeComposeFile(t, "services:\n  api:\n    image: alpine:${TAG}\n")
	project, err := Parse(t.Context(), dir, "", "test", map[string]string{"TAG": "3.20"})
	if err != nil {
		t.Fatalf("Parse = %v", err)
	}
	if got := project.Services["api"].Image; got != "alpine:3.20" {
		t.Fatalf("image = %q, want alpine:3.20 — environment vars must reach ${...}", got)
	}
}

func TestExternalNetworksAndVolumes(t *testing.T) {
	p := &types.Project{
		Networks: types.Networks{
			"shared":  {Name: "snapnow", External: true},
			"unnamed": {External: true},
			"default": {Name: "scoped"},
		},
		Volumes: types.Volumes{
			"media": {Name: "snapnow-media", External: true},
			"data":  {Name: "env-1-data"},
		},
	}
	if got := ExternalNetworks(p); !slices.Equal(got, []string{"snapnow", "unnamed"}) {
		t.Fatalf("ExternalNetworks = %v, want [snapnow unnamed]", got)
	}
	if got := ExternalVolumes(p); !slices.Equal(got, []string{"snapnow-media"}) {
		t.Fatalf("ExternalVolumes = %v, want [snapnow-media]", got)
	}
}

// Pins the wording the web classifier matches on.
// See lighthouse-web/src/features/deployments/failureReason.ts, kind "compose-include".
func TestParseNamesIncludeAsTheCause(t *testing.T) {
	dir := writeComposeFile(t, "include:\n  - ../database/docker-compose.yml\nservices:\n  api:\n    image: alpine\n")
	_, err := Parse(t.Context(), dir, "", "test", nil)
	if err == nil {
		t.Fatal("an unresolvable include must fail")
	}
	if !strings.Contains(err.Error(), "compose include could not be resolved") {
		t.Fatalf("error = %q, want it to name include as the cause", err)
	}
}

func TestParseErrorIsUnchangedWhenIncludeIsNotTheCause(t *testing.T) {
	dir := writeComposeFile(t, "services:\n  api:\n    image: alpine\n    env_file: .env\n")
	_, err := Parse(t.Context(), dir, "", "test", nil)
	if err == nil || strings.Contains(err.Error(), "include") {
		t.Fatalf("error = %v, want the original env_file failure", err)
	}
}
