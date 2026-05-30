package pipeline

import "context"

type Deployment struct {
	ID                string
	EnvironmentID     string
	ProjectID         string
	OwnerID           string
	TriggeredByUserID string
	RepoURL           string
	Branch            string
	EnvName           string
	EnvSubdomain      string
	ProjectSubdomain  string
	OwnerUsername     string
	CustomDomain      string
	BuildConfig       []byte
	WorkDir           string
	// Branches is empty on plain deploys; otherwise the SOURCE branch (index 0)
	// followed by MERGE branches in apply order, each pinned to a SHA.
	Branches []Branch
	// Host/Scheme are computed once in Runner.buildDeployment so every stage
	// agrees on the same public URL even if mode/base-domain change mid-run.
	Host   string
	Scheme string
}

func (d *Deployment) URL() string { return d.Scheme + "://" + d.Host }

type Branch struct {
	Merge bool
	Name  string
	Sha   string
}

// Stage Run and Rollback must both be idempotent: redelivery, retry, and reverse-order
// cleanup all call them on partial state.
type Stage interface {
	Name() string
	Run(ctx context.Context, dep *Deployment) error
	Rollback(ctx context.Context, dep *Deployment) error
}
