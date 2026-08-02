package httpx

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/client"

	"github.com/ilkinnezer1/lighthouse-worker/internal/stages"
)

// diskUsageTimeout bounds the Docker disk-usage scan, which walks volume
// contents and gets slower the more data is stored.
const diskUsageTimeout = 30 * time.Second

type volumeInfo struct {
	Name      string `json:"name"`
	Volume    string `json:"volume"`
	SizeBytes int64  `json:"sizeBytes"`
	CreatedAt string `json:"createdAt"`
	InUse     bool   `json:"inUse"`
}

// environmentVolumes lists the persistent volumes belonging to one environment.
// Name is the user-facing name from buildConfig; Volume is the real Docker name.
// SizeBytes is -1 when Docker could not report a size, so the UI can say
// "unknown" instead of showing a misleading 0.
func environmentVolumes(docker *client.Client, log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		envID := r.PathValue("id")
		if envID == "" {
			http.Error(w, "missing environment id", http.StatusBadRequest)
			return
		}
		prefix := stages.EnvVolumePrefix(envID)

		list, err := docker.VolumeList(r.Context(), volume.ListOptions{
			Filters: filters.NewArgs(filters.Arg("name", prefix)),
		})
		if err != nil {
			http.Error(w, "list volumes: "+err.Error(), http.StatusBadGateway)
			return
		}

		sizes := volumeSizes(r.Context(), docker, log)
		inUse := mountedVolumes(r.Context(), docker, log)

		out := []volumeInfo{}
		for _, v := range list.Volumes {
			if !strings.HasPrefix(v.Name, prefix) {
				continue
			}
			size, ok := sizes[v.Name]
			if !ok {
				size = -1
			}
			out = append(out, volumeInfo{
				Name:      strings.TrimPrefix(v.Name, prefix),
				Volume:    v.Name,
				SizeBytes: size,
				CreatedAt: v.CreatedAt,
				InUse:     inUse[v.Name],
			})
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	}
}

// deleteEnvironmentVolume removes one volume, refusing while any container still
// references it. Docker counts a stopped container as a reference too, so the
// check deliberately matches that: a volume is removable only once no container
// holds it, which in practice means the deployment using it is gone.
func deleteEnvironmentVolume(docker *client.Client, log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		envID := r.PathValue("id")
		name := r.PathValue("name")
		if envID == "" || name == "" {
			http.Error(w, "missing environment id or volume name", http.StatusBadRequest)
			return
		}

		// Reconstruct the full name rather than trusting the caller, so no
		// request can reach a volume outside this environment.
		full := stages.VolumeName(envID, name)

		if mountedVolumes(r.Context(), docker, log)[full] {
			http.Error(w, "volume is still attached to a container — it can only be removed once no deployment uses it", http.StatusConflict)
			return
		}
		if err := docker.VolumeRemove(r.Context(), full, false); err != nil {
			if client.IsErrNotFound(err) {
				http.Error(w, "no such volume", http.StatusNotFound)
				return
			}
			http.Error(w, "remove volume: "+err.Error(), http.StatusBadGateway)
			return
		}
		log.Info("removed volume", slog.String("volume", full))
		w.WriteHeader(http.StatusNoContent)
	}
}

// volumeSizes maps volume name to bytes on disk. Best-effort: a failure yields
// an empty map so the listing still renders, just without sizes.
func volumeSizes(ctx context.Context, docker *client.Client, log *slog.Logger) map[string]int64 {
	ctx, cancel := context.WithTimeout(ctx, diskUsageTimeout)
	defer cancel()

	usage, err := docker.DiskUsage(ctx, types.DiskUsageOptions{
		Types: []types.DiskUsageObject{types.VolumeObject},
	})
	if err != nil {
		log.Warn("disk usage failed — sizes unavailable", slog.Any("err", err))
		return nil
	}
	sizes := make(map[string]int64, len(usage.Volumes))
	for _, v := range usage.Volumes {
		if v.UsageData != nil {
			sizes[v.Name] = v.UsageData.Size
		}
	}
	return sizes
}

// mountedVolumes is the set of volume names any container references. Stopped
// containers are included on purpose — Docker refuses to remove a volume they
// hold, so counting only running ones would report a volume as deletable when
// it isn't.
func mountedVolumes(ctx context.Context, docker *client.Client, log *slog.Logger) map[string]bool {
	inUse := map[string]bool{}
	containers, err := docker.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		log.Warn("container list failed — in-use state unavailable", slog.Any("err", err))
		return inUse
	}
	for _, c := range containers {
		for _, m := range c.Mounts {
			if m.Type == "volume" && m.Name != "" {
				inUse[m.Name] = true
			}
		}
	}
	return inUse
}
