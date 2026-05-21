package compose

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/compose-spec/compose-go/v2/loader"
	"github.com/compose-spec/compose-go/v2/types"
)

var fileNames = []string{"compose.yml", "compose.yaml", "docker-compose.yml", "docker-compose.yaml"}

// Parse loads the first compose file found in workDir and returns the resolved
// project using the given project name. Errors if no compose file exists.
func Parse(ctx context.Context, workDir, projectName string) (*types.Project, error) {
	file, err := findFile(workDir)
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
		Environment: types.Mapping{},
	}, func(o *loader.Options) {
		o.SetProjectName(projectName, true)
	})
	if err != nil {
		return nil, fmt.Errorf("load compose: %w", err)
	}
	return project, nil
}

func findFile(workDir string) (string, error) {
	for _, name := range fileNames {
		path := filepath.Join(workDir, name)
		if _, err := os.Stat(path); err == nil {
			return path, nil
		}
	}
	return "", fmt.Errorf("no compose file in %s", workDir)
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

// WebService picks the service to expose publicly via Traefik. Preference:
// first service with declared ports (alphabetically), then alphabetical first
// of any service. Returns "" if the project has no services.
func WebService(project *types.Project) string {
	names := make([]string, 0, len(project.Services))
	for name := range project.Services {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if len(project.Services[name].Ports) > 0 {
			return name
		}
	}
	if len(names) > 0 {
		return names[0]
	}
	return ""
}