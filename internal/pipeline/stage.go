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
	// Host/Scheme are computed once in Runner.buildDeployment so every stage
	// agrees on the same public URL even if mode/base-domain change mid-run.
	Host   string
	Scheme string
}

func (d *Deployment) URL() string { return d.Scheme + "://" + d.Host }

// Run must be idempotent: a redelivered job after a worker crash will call it
// again on a stage that may already have done partial work.
type Stage interface {
	Name() string
	Run(ctx context.Context, dep *Deployment) error
}
