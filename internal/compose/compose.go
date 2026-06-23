package compose

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/compose-spec/compose-go/v2/loader"
	"github.com/compose-spec/compose-go/v2/types"
	"github.com/joho/godotenv"
)

// FileNames are the recognized compose file names, in priority order.
var FileNames = []string{"compose.yml", "compose.yaml", "docker-compose.yml", "docker-compose.yaml"}

// Parse loads the first compose file found in workDir and returns the resolved
// project using the given project name. Errors if no compose file exists.
func Parse(ctx context.Context, workDir, projectName string) (*types.Project, error) {
	return ParseAt(ctx, workDir, "", projectName)
}

// ParseAt loads the compose file at relPath (relative to workDir) and returns
// the resolved project. An empty relPath falls back to default discovery.
func ParseAt(ctx context.Context, workDir, relPath, projectName string) (*types.Project, error) {
	file, err := resolveFile(workDir, relPath)
	if err != nil {
		return nil, err
	}
	content, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("read compose file: %w", err)
	}
	project, err := loader.LoadWithContext(ctx, types.ConfigDetails{
		WorkingDir:  workDir,
		ConfigFiles: []types.ConfigFile{{Filename: file, Content: content}},
		Environment: dotEnv(workDir),
	}, func(o *loader.Options) {
		o.SetProjectName(projectName, true)
	})
	if err != nil {
		return nil, fmt.Errorf("load compose: %w", err)
	}
	return project, nil
}

// ResolvePath returns the cleaned compose path relative to workDir, defaulting
// to "docker-compose.yml". It rejects any path that escapes workDir.
func ResolvePath(relPath string) (string, error) {
	if relPath == "" {
		relPath = "docker-compose.yml"
	}
	clean := filepath.Clean(relPath)
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("compose path %q escapes workdir", relPath)
	}
	return clean, nil
}

func resolveFile(workDir, relPath string) (string, error) {
	if relPath == "" {
		return findFile(workDir)
	}
	clean, err := ResolvePath(relPath)
	if err != nil {
		return "", err
	}
	path := filepath.Join(workDir, clean)
	if _, err := os.Stat(path); err != nil {
		return "", fmt.Errorf("compose file %s: %w", clean, err)
	}
	return path, nil
}

func dotEnv(workDir string) types.Mapping {
	for _, name := range []string{".env", ".env.example"} {
		if m, err := godotenv.Read(filepath.Join(workDir, name)); err == nil {
			return types.Mapping(m)
		}
	}
	return types.Mapping{}
}

func findFile(workDir string) (string, error) {
	for _, name := range FileNames {
		path := filepath.Join(workDir, name)
		if _, err := os.Stat(path); err == nil {
			return path, nil
		}
	}
	return "", fmt.Errorf("no compose file in %s", workDir)
}

// InjectEnv adds environment variables to every service. Existing keys are
// overwritten — caller is the authority on these (e.g. lighthouse-managed
// DATABASE_URL takes precedence over anything the user put in their compose).
func InjectEnv(project *types.Project, vars map[string]string) {
	for name, svc := range project.Services {
		if svc.Environment == nil {
			svc.Environment = types.MappingWithEquals{}
		}
		for k, v := range vars {
			val := v
			svc.Environment[k] = &val
		}
		project.Services[name] = svc
	}
}

// AttachExternalNetwork ensures the named external network exists on the
// project and that the given service is attached to it.
func AttachExternalNetwork(project *types.Project, serviceName, networkName string) error {
	svc, ok := project.Services[serviceName]
	if !ok {
		return fmt.Errorf("service %q not found in compose project", serviceName)
	}
	if project.Networks == nil {
		project.Networks = types.Networks{}
	}
	if _, exists := project.Networks[networkName]; !exists {
		project.Networks[networkName] = types.NetworkConfig{
			Name:     networkName,
			External: types.External(true),
		}
	}
	if svc.Networks == nil {
		svc.Networks = map[string]*types.ServiceNetworkConfig{}
	}
	svc.Networks[networkName] = nil
	project.Services[serviceName] = svc
	return nil
}

// Normalize rewrites a parsed project for isolated per-deployment runs. Strip
// before scope: name stripping must run before volumes are scoped.
func Normalize(project *types.Project, envID string) {
	StripFixedNames(project)
	ScopeVolumes(project, envID)
}

func StripFixedNames(project *types.Project) {
	for name, svc := range project.Services {
		if svc.ContainerName != "" {
			addNetworkAlias(&svc, svc.ContainerName)
			svc.ContainerName = ""
		}
		for i := range svc.Ports {
			svc.Ports[i].Published = ""
			svc.Ports[i].HostIP = ""
		}
		project.Services[name] = svc
	}
	for name, net := range project.Networks {
		if net.External {
			continue
		}
		net.Name = ""
		project.Networks[name] = net
	}
}

func ScopeVolumes(project *types.Project, envID string) {
	for key, vol := range project.Volumes {
		if vol.External {
			continue
		}
		vol.Name = "env-" + envID + "-" + key
		project.Volumes[key] = vol
	}
}

func addNetworkAlias(svc *types.ServiceConfig, alias string) {
	if len(svc.Networks) == 0 {
		svc.Networks = map[string]*types.ServiceNetworkConfig{
			"default": {Aliases: []string{alias}},
		}
		return
	}
	for k, nc := range svc.Networks {
		if nc == nil {
			nc = &types.ServiceNetworkConfig{}
		}
		nc.Aliases = append(nc.Aliases, alias)
		svc.Networks[k] = nc
	}
}

// Write serializes the project as YAML and writes it to path (0644).
func Write(project *types.Project, path string) error {
	data, err := project.MarshalYAML()
	if err != nil {
		return fmt.Errorf("marshal compose: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write compose: %w", err)
	}
	return nil
}

// InjectLabels adds labels to the named service. Returns an error if the
// service does not exist on the project.
func InjectLabels(project *types.Project, serviceName string, labels map[string]string) error {
	svc, ok := project.Services[serviceName]
	if !ok {
		return fmt.Errorf("service %q not found in compose project", serviceName)
	}
	if svc.Labels == nil {
		svc.Labels = types.Labels{}
	}
	for k, v := range labels {
		svc.Labels[k] = v
	}
	project.Services[serviceName] = svc
	return nil
}

func WebService(project *types.Project, preferred int) (string, int) {
	names := make([]string, 0, len(project.Services))
	for name := range project.Services {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		for _, p := range project.Services[name].Ports {
			if int(p.Target) == preferred {
				return name, preferred
			}
		}
	}
	for _, name := range names {
		if ports := project.Services[name].Ports; len(ports) > 0 {
			return name, int(ports[0].Target)
		}
	}
	if len(names) > 0 {
		return names[0], 0
	}
	return "", 0
}

func ServicePort(project *types.Project, name string) int {
	if svc, ok := project.Services[name]; ok && len(svc.Ports) > 0 {
		return int(svc.Ports[0].Target)
	}
	return 0
}
