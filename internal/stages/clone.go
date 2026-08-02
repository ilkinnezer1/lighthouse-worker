package stages

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/ilkinnezer1/lighthouse-worker/internal/crypto"
	"github.com/ilkinnezer1/lighthouse-worker/internal/db"
	dbgen "github.com/ilkinnezer1/lighthouse-worker/internal/db/gen"
	"github.com/ilkinnezer1/lighthouse-worker/internal/framework"
	"github.com/ilkinnezer1/lighthouse-worker/internal/logstream"
	"github.com/ilkinnezer1/lighthouse-worker/internal/pipeline"
)

// Identity stamped on merge commits the worker creates when combining branches.
const (
	mergeAuthorName  = "Lighthouse"
	mergeAuthorEmail = "deploy@lighthouse"
)

// mergeTimeout bounds a single git merge so a pathological merge can't hang the build.
const mergeTimeout = 10 * time.Minute

type Clone struct {
	q      *dbgen.Queries
	cipher *crypto.Cipher
	logs   *logstream.Writer
}

func NewClone(q *dbgen.Queries, cipher *crypto.Cipher, logs *logstream.Writer) *Clone {
	return &Clone{q: q, cipher: cipher, logs: logs}
}

// remoteFor resolves dep.RepoURL into the git remote to clone from, injecting
// the triggering user's GitHub OAuth token for private github.com HTTPS repos.
// logSafe is the token-masked form and is the ONLY remote representation that
// may be logged. Missing token or any decrypt error falls back to the bare
// RepoURL so public clones keep working.
func (c *Clone) remoteFor(ctx context.Context, dep *pipeline.Deployment) (remote, logSafe string) {
	enc, err := c.q.GetGithubTokenForUser(ctx, db.MustUUID(dep.TriggeredByUserID))
	if err != nil || enc == nil {
		return dep.RepoURL, dep.RepoURL
	}
	token, err := c.cipher.Decrypt(*enc)
	if err != nil {
		return dep.RepoURL, dep.RepoURL
	}
	return authedRemote(dep.RepoURL, token)
}

func (*Clone) Name() string { return "clone" }

func (c *Clone) Run(ctx context.Context, dep *pipeline.Deployment) error {
	if len(dep.Branches) > 0 {
		if err := c.runMerge(ctx, dep); err != nil {
			return err
		}
	} else if err := c.runPlain(ctx, dep); err != nil {
		return err
	}
	c.captureCommit(ctx, dep)
	dep.Framework = framework.Detect(dep.WorkDir)
	return nil
}

// captureCommit records the worktree HEAD on dep so the runner can persist it.
// For merges this is the merge commit Lighthouse just created. Best-effort: a
// git failure logs to the deploy stream and never fails the deploy.
func (c *Clone) captureCommit(ctx context.Context, dep *pipeline.Deployment) {
	out, err := exec.CommandContext(ctx, "git", "-C", dep.WorkDir,
		"log", "-1", "--format=%H%x1f%an%x1f%s").Output()
	if err != nil {
		_ = c.logs.WriteLine(ctx, dep.ID, "clone", "capture commit failed: "+err.Error())
		return
	}
	parts := strings.SplitN(strings.TrimRight(string(out), "\n"), "\x1f", 3)
	if len(parts) != 3 {
		return
	}
	dep.Commit = &pipeline.CommitInfo{Sha: parts[0], Author: parts[1], Message: parts[2]}
}

// runPlain is the fast path for deploys without a pinned branch set: a shallow
// clone of dep.Branch. Idempotent — a redelivered job skips a done checkout.
func (c *Clone) runPlain(ctx context.Context, dep *pipeline.Deployment) error {
	if _, err := os.Stat(filepath.Join(dep.WorkDir, ".git")); err == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dep.WorkDir), 0o755); err != nil {
		return fmt.Errorf("mkdir parent: %w", err)
	}

	remote, logSafe := c.remoteFor(ctx, dep)
	_ = c.logs.WriteLine(ctx, dep.ID, "clone", "cloning "+logSafe)

	cmd := exec.CommandContext(ctx, "git", "clone",
		"--depth", "1",
		"--branch", dep.Branch,
		remote,
		dep.WorkDir,
	)
	if err := runCmd(ctx, cmd, c.logs, dep.ID, "clone"); err != nil {
		return fmt.Errorf("git clone: %w", err)
	}
	return nil
}

// runMerge builds the worktree from a SHA-pinned branch set: check out the
// SOURCE commit, then merge each MERGE commit in order. Fetches carry full
// history (a shallow fetch has no merge base). Always starts from a clean
// workdir so a half-applied merge from a crashed attempt cannot survive a retry.
func (c *Clone) runMerge(ctx context.Context, dep *pipeline.Deployment) error {
	if err := os.RemoveAll(dep.WorkDir); err != nil {
		return fmt.Errorf("clean workdir: %w", err)
	}
	if err := os.MkdirAll(dep.WorkDir, 0o755); err != nil {
		return fmt.Errorf("mkdir workdir: %w", err)
	}

	remote, logSafe := c.remoteFor(ctx, dep)
	_ = c.logs.WriteLine(ctx, dep.ID, "clone", "cloning "+logSafe)

	if err := c.git(ctx, dep, "init", "-q"); err != nil {
		return fmt.Errorf("git init: %w", err)
	}
	if err := c.git(ctx, dep, "remote", "add", "origin", remote); err != nil {
		return fmt.Errorf("git remote add: %w", err)
	}

	source := dep.Branches[0]
	if err := c.git(ctx, dep, "fetch", "--no-tags", "origin", fetchRef(source)); err != nil {
		return fmt.Errorf("fetch source %s: %w", label(source), err)
	}
	if err := c.git(ctx, dep, "checkout", "--detach", "FETCH_HEAD"); err != nil {
		return fmt.Errorf("checkout source %s: %w", label(source), err)
	}

	for _, b := range dep.Branches[1:] {
		if err := c.git(ctx, dep, "fetch", "--no-tags", "origin", fetchRef(b)); err != nil {
			return fmt.Errorf("fetch %s: %w", label(b), err)
		}
		merge := []string{
			"-c", "user.name=" + mergeAuthorName,
			"-c", "user.email=" + mergeAuthorEmail,
			"merge", "--no-edit",
			"-m", "Merge branch '" + b.Name + "' into " + source.Name,
			"FETCH_HEAD",
		}
		mctx, cancel := context.WithTimeout(ctx, mergeTimeout)
		err := c.git(mctx, dep, merge...)
		cancel()
		if err != nil {
			conflicts := gitConflicts(ctx, dep.WorkDir)
			_ = c.git(ctx, dep, "merge", "--abort")
			if len(conflicts) > 0 {
				return fmt.Errorf("merge %s into %s: conflicts in %s",
					b.Name, source.Name, strings.Join(conflicts, ", "))
			}
			return fmt.Errorf("merge %s into %s: %w", b.Name, source.Name, err)
		}
	}
	return nil
}

// fetchRef pins to the resolved SHA when present, else falls back to the branch
// name (the sha column is nullable). checkout/merge then use FETCH_HEAD either way.
func fetchRef(b pipeline.Branch) string {
	if b.Sha != "" {
		return b.Sha
	}
	return b.Name
}

func label(b pipeline.Branch) string {
	if b.Sha != "" {
		return b.Name + " (" + shortSha(b.Sha) + ")"
	}
	return b.Name
}

func (c *Clone) git(ctx context.Context, dep *pipeline.Deployment, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dep.WorkDir}, args...)...)
	return runCmd(ctx, cmd, c.logs, dep.ID, "clone")
}

// gitConflicts lists the unmerged paths after a failed merge, for the error.
func gitConflicts(ctx context.Context, workDir string) []string {
	out, err := exec.CommandContext(ctx, "git", "-C", workDir,
		"diff", "--name-only", "--diff-filter=U").Output()
	if err != nil {
		return nil
	}
	return strings.Fields(strings.TrimSpace(string(out)))
}

func shortSha(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

func (c *Clone) Rollback(_ context.Context, dep *pipeline.Deployment) error {
	if err := os.RemoveAll(dep.WorkDir); err != nil {
		return fmt.Errorf("remove workdir: %w", err)
	}
	return nil
}
