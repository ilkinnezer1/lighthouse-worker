package stages

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/ilkinnezer1/lighthouse-worker/internal/crypto"
	"github.com/ilkinnezer1/lighthouse-worker/internal/db"
	dbgen "github.com/ilkinnezer1/lighthouse-worker/internal/db/gen"
)

// mergeCheckTimeout bounds the whole probe. It is far shorter than a build's
// merge budget: this runs while someone waits on a dialog, and a repo too large
// to answer in a minute is better reported as "could not tell" than left
// spinning.
const mergeCheckTimeout = 90 * time.Second

// MergeRef is one branch to combine, pinned to the commit the caller resolved.
type MergeRef struct {
	Name string `json:"name"`
	Sha  string `json:"sha"`
}

// MergeCheck asks whether a branch set would combine cleanly.
type MergeCheck struct {
	RepoURL string     `json:"repoUrl"`
	UserID  string     `json:"userId"`
	Source  MergeRef   `json:"source"`
	Merges  []MergeRef `json:"merges"`
}

// MergeCheckResult mirrors what a deploy would do: it stops at the first branch
// that will not merge, because that is where the build would stop too.
type MergeCheckResult struct {
	Mergeable bool     `json:"mergeable"`
	Branch    string   `json:"branch,omitempty"`
	Files     []string `json:"files,omitempty"`
}

// CheckMerge dry-runs the deploy's own merge sequence in a throwaway worktree.
//
// It performs the real merges rather than reading a plumbing command, so the
// answer is authoritative — the same git, the same order, the same strategy the
// build would use. (`git merge-tree --write-tree` would be cheaper, but needs
// git 2.38 and the worker host is on 2.34.)
//
// Every call pays a full fetch, which is why nothing calls it automatically.
func CheckMerge(
	ctx context.Context,
	q *dbgen.Queries,
	cipher *crypto.Cipher,
	req MergeCheck,
) (MergeCheckResult, error) {
	if req.Source.Sha == "" {
		return MergeCheckResult{}, fmt.Errorf("source sha is required")
	}
	if len(req.Merges) == 0 {
		// Nothing to combine: a lone source branch always "merges".
		return MergeCheckResult{Mergeable: true}, nil
	}

	ctx, cancel := context.WithTimeout(ctx, mergeCheckTimeout)
	defer cancel()

	dir, err := os.MkdirTemp("", "lh-mergecheck-")
	if err != nil {
		return MergeCheckResult{}, fmt.Errorf("temp dir: %w", err)
	}
	// The probe leaves nothing behind — it exists only to answer one question.
	defer func() { _ = os.RemoveAll(dir) }()

	remote, _ := authedRemoteForUser(ctx, q, cipher, req.RepoURL, req.UserID)

	git := func(args ...string) error {
		cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
		return cmd.Run()
	}

	if err := git("init", "-q"); err != nil {
		return MergeCheckResult{}, fmt.Errorf("git init: %w", err)
	}
	if err := git("remote", "add", "origin", remote); err != nil {
		return MergeCheckResult{}, fmt.Errorf("git remote add: %w", err)
	}
	// Full history: a shallow fetch has no merge base, so every merge would
	// look like a conflict.
	if err := git("fetch", "--no-tags", "origin", req.Source.Sha); err != nil {
		return MergeCheckResult{}, fmt.Errorf("fetch source: %w", err)
	}
	if err := git("checkout", "--detach", "FETCH_HEAD"); err != nil {
		return MergeCheckResult{}, fmt.Errorf("checkout source: %w", err)
	}

	for _, b := range req.Merges {
		if b.Sha == "" {
			return MergeCheckResult{}, fmt.Errorf("sha is required for %s", b.Name)
		}
		if err := git("fetch", "--no-tags", "origin", b.Sha); err != nil {
			return MergeCheckResult{}, fmt.Errorf("fetch %s: %w", b.Name, err)
		}
		// --no-commit keeps the result inspectable; the identity flags match the
		// build so an author-less environment cannot fail the merge itself.
		err := git(
			"-c", "user.name="+mergeAuthorName,
			"-c", "user.email="+mergeAuthorEmail,
			"merge", "--no-commit", "--no-ff", "FETCH_HEAD",
		)
		if err != nil {
			conflicts := gitConflicts(ctx, dir)
			_ = git("merge", "--abort")
			return MergeCheckResult{
				Mergeable: false,
				Branch:    b.Name,
				Files:     conflicts,
			}, nil
		}
		// Commit it so the next branch merges onto the accumulated result,
		// exactly as the build's sequence does.
		if err := git(
			"-c", "user.name="+mergeAuthorName,
			"-c", "user.email="+mergeAuthorEmail,
			"commit", "--no-edit", "-m", "merge check",
		); err != nil {
			// An empty merge (already up to date) leaves nothing to commit,
			// which is success, not failure.
			continue
		}
	}

	return MergeCheckResult{Mergeable: true}, nil
}

// authedRemoteForUser is remoteFor without a Deployment to hang it off — the
// probe has a repo URL and a user, not a build.
func authedRemoteForUser(
	ctx context.Context,
	q *dbgen.Queries,
	cipher *crypto.Cipher,
	repoURL, userID string,
) (remote, logSafe string) {
	if userID == "" {
		return repoURL, repoURL
	}
	enc, err := q.GetGithubTokenForUser(ctx, db.MustUUID(userID))
	if err != nil || enc == nil {
		return repoURL, repoURL
	}
	token, err := cipher.Decrypt(*enc)
	if err != nil {
		return repoURL, repoURL
	}
	return authedRemote(repoURL, token)
}
