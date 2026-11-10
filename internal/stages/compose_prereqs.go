package stages

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	composetypes "github.com/compose-spec/compose-go/v2/types"
	"github.com/docker/docker/api/types/volume"
	"github.com/joho/godotenv"

	"github.com/ilkinnezer1/lighthouse-worker/internal/compose"
	"github.com/ilkinnezer1/lighthouse-worker/internal/pipeline"
)

// prepareEnvFiles materialises every env_file the compose file names but the
// checkout does not ship. Lighthouse keeps env vars in the database and injects
// them into `environment:`, so `env_file: .env` — one of the most common compose
// idioms there is — failed the load with a raw "not found" path error.
//
// It belongs to setup, not run: `docker compose build` parses the file too, so a
// missing env_file killed the build stage before run could repair it.
func (s *Setup) prepareEnvFiles(ctx context.Context, dep *pipeline.Deployment) error {
	o := parseBuildOverrides(dep.BuildConfig)
	if detectStrategy(dep.WorkDir, o.Strategy) != StrategyCompose {
		return nil
	}
	envs, err := decodeUserEnvs(s.cipher, dep.EnvVarsEnc)
	if err != nil {
		return err
	}
	// A compose file this stage cannot parse is not this stage's to report —
	// build and run fail on it with their own context, so an unparseable file
	// simply declares no env files.
	paths, _ := compose.DeclaredEnvFiles(ctx, dep.WorkDir, o.ComposePath, composeProject(dep.ID), envs)
	written, err := writeEnvFiles(dep.WorkDir, paths, envs)
	if err != nil {
		return err
	}
	for _, rel := range written {
		s.logLine(ctx, dep.ID, fmt.Sprintf("wrote %s from %d environment variable(s)", rel, len(envs)))
	}
	return nil
}

// writeEnvFiles creates only the missing ones: a file the checkout or a managed
// overlay already supplies is the user's, not ours, and one resolving outside
// the workdir is refused rather than written.
func writeEnvFiles(workDir string, paths []string, envs map[string]string) ([]string, error) {
	body, err := godotenv.Marshal(envs)
	if err != nil {
		return nil, fmt.Errorf("render env file: %w", err)
	}
	var written []string
	for _, rel := range paths {
		if _, err := os.Stat(absOrJoin(workDir, rel)); err == nil {
			continue
		}
		target, err := safeJoin(workDir, rel)
		if err != nil {
			return nil, fmt.Errorf("env_file %s: %w", rel, err)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return nil, fmt.Errorf("env_file %s: %w", rel, err)
		}
		if err := os.WriteFile(target, []byte(body+"\n"), 0o600); err != nil {
			return nil, fmt.Errorf("env_file %s: %w", rel, err)
		}
		written = append(written, rel)
	}
	return written, nil
}

func absOrJoin(workDir, rel string) string {
	if filepath.IsAbs(rel) {
		return rel
	}
	return filepath.Join(workDir, rel)
}

// ensureExternalResources creates what the compose file declares `external: true`.
// Compose never creates those itself — it aborts with a docker error that never
// says the fix is `docker network create`.
func (r *Run) ensureExternalResources(ctx context.Context, dep *pipeline.Deployment, project *composetypes.Project) error {
	for _, name := range compose.ExternalNetworks(project) {
		if name == publicNetwork || name == envNetwork(dep.EnvironmentID) {
			continue
		}
		if err := r.ensureNetwork(ctx, name); err != nil {
			return err
		}
		r.logLine(ctx, dep.ID, "external network ready: "+name)
	}
	for _, name := range compose.ExternalVolumes(project) {
		if _, err := r.docker.VolumeCreate(ctx, volume.CreateOptions{Name: name}); err != nil {
			return fmt.Errorf("create volume %s: %w", name, err)
		}
		r.logLine(ctx, dep.ID, "external volume ready: "+name)
	}
	return nil
}
