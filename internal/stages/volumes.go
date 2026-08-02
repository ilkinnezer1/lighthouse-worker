package stages

import (
	"context"
	"fmt"
	"path"
	"regexp"
	"strings"

	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/volume"

	"github.com/ilkinnezer1/lighthouse-worker/internal/pipeline"
)

// VolumePrefix scopes a named volume to one environment, so production and a
// preview env never share data and teardown can find an environment's volumes
// by name alone.
const VolumePrefix = "lh-"

var volumeNamePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,62}$`)

// VolumeName is the Docker volume backing buildConfig volume `name` for one
// environment. Deployments are ephemeral and their containers are recreated on
// every deploy, so the volume is keyed on the environment — that is what makes
// the data outlive a redeploy.
func VolumeName(environmentID, name string) string {
	return VolumePrefix + environmentID + "-" + name
}

// EnvVolumePrefix is the name prefix shared by every volume of one environment.
func EnvVolumePrefix(environmentID string) string {
	return VolumePrefix + environmentID + "-"
}

// validateVolume rejects a name Docker would mangle and a mount path that isn't
// a sane absolute directory, so a typo fails the deploy loudly instead of
// silently mounting somewhere surprising.
func validateVolume(v volumeMount) error {
	if !volumeNamePattern.MatchString(v.Name) {
		return fmt.Errorf("volume name %q must be alphanumeric and may contain _ . -", v.Name)
	}
	if !path.IsAbs(v.Path) {
		return fmt.Errorf("volume %q mount path %q must be absolute", v.Name, v.Path)
	}
	if clean := path.Clean(v.Path); clean == "/" || strings.Contains(v.Path, "..") {
		return fmt.Errorf("volume %q mount path %q is not a valid mount target", v.Name, v.Path)
	}
	return nil
}

// ensureVolumes creates the deployment's declared volumes if they don't exist.
// VolumeCreate is idempotent for an existing name, so a redeploy reattaches the
// same data rather than starting fresh.
func (r *Run) ensureVolumes(ctx context.Context, dep *pipeline.Deployment, o buildOverrides) ([]mount.Mount, error) {
	if len(o.Volumes) == 0 {
		return nil, nil
	}
	mounts := make([]mount.Mount, 0, len(o.Volumes))
	for _, v := range o.Volumes {
		if err := validateVolume(v); err != nil {
			return nil, err
		}
		name := VolumeName(dep.EnvironmentID, v.Name)
		if _, err := r.docker.VolumeCreate(ctx, volume.CreateOptions{
			Name:   name,
			Labels: map[string]string{"lighthouse.environment": dep.EnvironmentID},
		}); err != nil {
			return nil, fmt.Errorf("create volume %s: %w", name, err)
		}
		r.logLine(ctx, dep.ID, "volume "+v.Name+" mounted at "+v.Path+" (persists across deploys)")
		mounts = append(mounts, mount.Mount{
			Type:   mount.TypeVolume,
			Source: name,
			Target: v.Path,
		})
	}
	return mounts, nil
}
