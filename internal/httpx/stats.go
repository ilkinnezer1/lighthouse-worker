package httpx

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"

	"github.com/ilkinnezer1/lighthouse-worker/internal/db"
	dbgen "github.com/ilkinnezer1/lighthouse-worker/internal/db/gen"
)

// serviceStats reports an instantaneous CPU%/memory snapshot for a single
// service container. CPU% needs two frames a moment apart: the streaming stats
// endpoint primes precpu_stats on the first frame, so the second frame already
// carries the delta and we don't have to subtract two reads ourselves.
func serviceStats(docker *client.Client, q *dbgen.Queries, log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		depID, err := db.ParseUUID(r.PathValue("id"))
		if err != nil {
			http.Error(w, "invalid deployment id", http.StatusBadRequest)
			return
		}
		name := r.PathValue("name")

		// Resolve the target container: the named service, or the web service
		// when name is the sentinel ""/"web".
		var containerID *string
		if name != "" && name != "web" {
			svc, qerr := q.GetServiceByDeploymentAndName(r.Context(), dbgen.GetServiceByDeploymentAndNameParams{
				DeploymentID: depID,
				Name:         name,
			})
			if qerr == nil {
				containerID = svc.ContainerID
			}
		} else {
			svc, qerr := q.GetWebServiceByDeployment(r.Context(), depID)
			if qerr == nil {
				containerID = svc.ContainerID
			}
		}
		if containerID == nil || *containerID == "" {
			http.Error(w, "no running container for service", http.StatusNotFound)
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()

		// A recorded containerId can point at a stopped/exited container (e.g. a
		// one-shot init container); ContainerStats would then emit a degenerate
		// all-zero frame, so treat a non-live container as "no container".
		info, err := docker.ContainerInspect(ctx, *containerID)
		if err != nil || info.State == nil || !info.State.Running {
			http.Error(w, "no running container for service", http.StatusNotFound)
			return
		}

		stats, err := docker.ContainerStats(ctx, *containerID, true)
		if err != nil {
			http.Error(w, "stats: "+err.Error(), http.StatusBadGateway)
			return
		}
		defer func() { _ = stats.Body.Close() }()

		dec := json.NewDecoder(stats.Body)
		var frame container.StatsResponse
		// First frame has zeroed precpu; the second carries a usable delta.
		for i := 0; i < 2; i++ {
			if err := dec.Decode(&frame); err != nil {
				http.Error(w, "stats decode: "+err.Error(), http.StatusBadGateway)
				return
			}
		}

		netRx, netTx := netBytes(frame)
		blkRead, blkWrite := blkBytes(frame)

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"service":       name,
			"cpuPct":        cpuPercent(frame),
			"memUsedBytes":  memUsedBytes(frame),
			"memLimitBytes": frame.MemoryStats.Limit,
			"netRxBytes":    netRx,
			"netTxBytes":    netTx,
			"blkReadBytes":  blkRead,
			"blkWriteBytes": blkWrite,
		})
	}
}

func cpuPercent(s container.StatsResponse) float64 {
	cpuDelta := float64(s.CPUStats.CPUUsage.TotalUsage) - float64(s.PreCPUStats.CPUUsage.TotalUsage)
	systemDelta := float64(s.CPUStats.SystemUsage) - float64(s.PreCPUStats.SystemUsage)
	if systemDelta <= 0 || cpuDelta <= 0 {
		return 0
	}
	cpus := float64(s.CPUStats.OnlineCPUs)
	if cpus == 0 {
		cpus = float64(len(s.CPUStats.CPUUsage.PercpuUsage))
	}
	if cpus == 0 {
		cpus = 1
	}
	return cpuDelta / systemDelta * cpus * 100
}

// netBytes sums cumulative rx/tx across every interface in the frame; the
// Networks map is absent under host networking, in which case both stay 0.
func netBytes(s container.StatsResponse) (rx, tx uint64) {
	for _, n := range s.Networks {
		rx += n.RxBytes
		tx += n.TxBytes
	}
	return rx, tx
}

// blkBytes sums cumulative read/write bytes; IoServiceBytesRecursive is empty
// on cgroup v2 without io accounting, which yields 0 rather than an error.
func blkBytes(s container.StatsResponse) (read, write uint64) {
	for _, e := range s.BlkioStats.IoServiceBytesRecursive {
		switch strings.ToLower(e.Op) {
		case "read":
			read += e.Value
		case "write":
			write += e.Value
		}
	}
	return read, write
}

func memUsedBytes(s container.StatsResponse) uint64 {
	used := s.MemoryStats.Usage
	if v, ok := s.MemoryStats.Stats["inactive_file"]; ok {
		if v < used {
			used -= v
		}
		return used
	}
	if v, ok := s.MemoryStats.Stats["cache"]; ok && v < used {
		used -= v
	}
	return used
}
