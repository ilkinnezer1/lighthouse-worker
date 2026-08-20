package stages

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"regexp"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
)

var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// Reclaimed reports what a reclaim actually freed. Callers log it verbatim: a
// removal that failed must never read as a success.
type Reclaimed struct {
	Images        []string
	ImageErrs     int
	WorkDirBytes  int64
	ResidualBytes int64
	Err           error
}

func (r Reclaimed) Complete() bool { return r.Err == nil && r.ImageErrs == 0 }

// Freed distinguishes "actually removed something" from "there was nothing left
// to remove", so a sweep can't report work it did not do.
func (r Reclaimed) Freed() bool { return len(r.Images) > 0 || r.WorkDirBytes > 0 }

func (r Reclaimed) Summary() string {
	s := fmt.Sprintf("freed %d image(s), %s", len(r.Images), HumanBytes(r.WorkDirBytes))
	if r.ImageErrs > 0 {
		s += fmt.Sprintf("; %d image(s) could not be removed", r.ImageErrs)
	}
	if r.Err != nil {
		s += fmt.Sprintf("; workdir NOT reclaimed (%s still on disk): %v", HumanBytes(r.ResidualBytes), r.Err)
	}
	return s
}

// OwnsImage reports whether an image tag was built for this deployment. Compose
// deployments also record the upstream images their file pulls (mysql:8.0,
// node:20), which are shared host-wide and must never be removed.
func OwnsImage(deploymentID, img string) bool {
	tag := ImageTag(deploymentID)
	return img == tag || strings.HasPrefix(img, tag+"-")
}

// OwnedImages reduces recorded and discovered tags to the ones this deployment
// owns. ImageTag is always included so a build that crashed before its service
// row landed still gets its image removed.
func OwnedImages(deploymentID string, candidates []string) []string {
	out := []string{ImageTag(deploymentID)}
	seen := map[string]struct{}{out[0]: {}}
	for _, img := range candidates {
		if img == "" || img == "<none>:<none>" || !OwnsImage(deploymentID, img) {
			continue
		}
		if _, dup := seen[img]; dup {
			continue
		}
		seen[img] = struct{}{}
		out = append(out, img)
	}
	return out
}

// ReclaimDeployment frees a superseded deployment's disk. Containers go first:
// they pin the image and, for compose deploys, bind-mount out of the work tree.
// Best-effort, but every failure is reported rather than swallowed.
func ReclaimDeployment(ctx context.Context, docker *client.Client, workDirBase, deploymentID string, recorded []string) Reclaimed {
	_ = removeComposeResources(ctx, docker, deploymentID)
	_ = docker.ContainerRemove(ctx, containerName(deploymentID), container.RemoveOptions{Force: true})

	var out Reclaimed
	for _, img := range OwnedImages(deploymentID, append(recorded, discoverImages(ctx, docker, deploymentID)...)) {
		if _, err := docker.ImageRemove(ctx, img, image.RemoveOptions{Force: true, PruneChildren: true}); err != nil {
			if !isNotFoundErr(err) {
				out.ImageErrs++
			}
			continue
		}
		out.Images = append(out.Images, img)
	}
	out.WorkDirBytes, out.ResidualBytes, out.Err = ReclaimWorkDir(workDirBase, deploymentID)
	return out
}

// discoverImages asks the daemon which deploy-<id>* tags exist, so reclaim still
// finds a compose deployment's per-service images once its rows are gone.
func discoverImages(ctx context.Context, docker *client.Client, deploymentID string) []string {
	list, err := docker.ImageList(ctx, image.ListOptions{
		Filters: filters.NewArgs(filters.Arg("reference", ImageTag(deploymentID)+"*")),
	})
	if err != nil {
		return nil
	}
	var out []string
	for _, img := range list {
		out = append(out, img.RepoTags...)
	}
	return out
}

// ReclaimWorkDir deletes the tree and then verifies it is gone. Build containers
// bind-mount the work dir and write root-owned node_modules/vendor into it, so a
// worker running as an ordinary user gets EPERM and must not report success.
func ReclaimWorkDir(workDirBase, deploymentID string) (freed, residual int64, err error) {
	// Anything but a plain UUID (""/"."/"..") can resolve back to the shared base
	// dir and wipe every deployment on the host.
	if workDirBase == "" || !uuidPattern.MatchString(deploymentID) {
		return 0, 0, nil
	}
	dir := filepath.Join(workDirBase, deploymentID)
	before := dirSize(dir)
	rmErr := os.RemoveAll(dir)
	if _, statErr := os.Stat(dir); statErr == nil {
		residual = dirSize(dir)
		if rmErr == nil {
			rmErr = fmt.Errorf("%s still present after removal", dir)
		}
		return before - residual, residual, rmErr
	}
	return before, 0, nil
}

func dirSize(dir string) int64 {
	var total int64
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if info, infoErr := d.Info(); infoErr == nil {
			total += info.Size()
		}
		return nil
	})
	return total
}

func HumanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit && exp < 3; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGT"[exp])
}

func isNotFoundErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "No such image") || strings.Contains(msg, "no such image")
}
