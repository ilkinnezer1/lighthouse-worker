package stages

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ilkinnezer1/lighthouse-worker/internal/crypto"
	"github.com/ilkinnezer1/lighthouse-worker/internal/db"
	dbgen "github.com/ilkinnezer1/lighthouse-worker/internal/db/gen"
	"github.com/ilkinnezer1/lighthouse-worker/internal/logstream"
	"github.com/ilkinnezer1/lighthouse-worker/internal/pipeline"
)

// ApplyFiles writes the environment's managed file overlays (e.g. .env.local)
// into the freshly-cloned workdir. Runs after clone, before build — each deploy
// is a clean clone, so overrides must be re-applied every time.
type ApplyFiles struct {
	q      *dbgen.Queries
	cipher *crypto.Cipher
	logs   *logstream.Writer
}

func NewApplyFiles(q *dbgen.Queries, cipher *crypto.Cipher, logs *logstream.Writer) *ApplyFiles {
	return &ApplyFiles{q: q, cipher: cipher, logs: logs}
}

func (*ApplyFiles) Name() string { return "apply_files" }

func (a *ApplyFiles) Run(ctx context.Context, dep *pipeline.Deployment) error {
	rows, err := a.q.ListManagedFilesByEnvironment(ctx, db.MustUUID(dep.EnvironmentID))
	if err != nil {
		return fmt.Errorf("list managed files: %w", err)
	}
	for _, row := range rows {
		target, err := safeJoin(dep.WorkDir, row.Path)
		if err != nil {
			return err
		}
		content, err := a.cipher.Decrypt(row.ContentEnc)
		if err != nil {
			return fmt.Errorf("decrypt %s: %w", row.Path, err)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return fmt.Errorf("mkdir for %s: %w", row.Path, err)
		}
		if err := os.WriteFile(target, []byte(content), 0o644); err != nil {
			return fmt.Errorf("write %s: %w", row.Path, err)
		}
		a.logLine(ctx, dep.ID, "wrote "+row.Path)
	}
	return nil
}

// safeJoin resolves rel against workDir and rejects any path that escapes it
// (.., absolute paths) — defense-in-depth even though the API validates.
func safeJoin(workDir, rel string) (string, error) {
	clean := filepath.Clean(rel)
	if filepath.IsAbs(clean) {
		return "", fmt.Errorf("managed file path %q must be relative", rel)
	}
	target := filepath.Join(workDir, clean)
	base := filepath.Clean(workDir)
	if target != base && !strings.HasPrefix(target, base+string(os.PathSeparator)) {
		return "", fmt.Errorf("managed file path %q escapes workdir", rel)
	}
	return target, nil
}

func (a *ApplyFiles) Rollback(_ context.Context, _ *pipeline.Deployment) error {
	return nil
}

func (a *ApplyFiles) logLine(ctx context.Context, deploymentID, line string) {
	_ = a.logs.WriteLine(ctx, deploymentID, "apply_files", line)
}
