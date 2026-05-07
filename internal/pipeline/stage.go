package pipeline

import "context"

// Deployment is the immutable data passed to every stage in one run.
type Deployment struct {
	ID                string
	EnvironmentID     string
	ProjectID         string
	OwnerID           string
	TriggeredByUserID string // empty when system-triggered
	RepoURL           string
	Branch            string
	EnvName           string
	EnvSubdomain      string
	ProjectSubdomain  string
	OwnerUsername     string
	CustomDomain      string // empty when not set
	BuildConfig       []byte // raw JSONB; Build stage parses it
	WorkDir           string // data/deployments/<id>/
	// Public-facing URL (host built by routing.Host, scheme by mode).
	// Computed in Runner.buildDeployment so every stage sees the same value.
	Host   string
	Scheme string
}

// URL returns the full public URL of this deployment.
func (d *Deployment) URL() string { return d.Scheme + "://" + d.Host }

// Stage Run must be idempotent, a redelivered job after a worker crash will
// call Run again on a stage that may already have done partial work.
type Stage interface {
	Name() string
	Run(ctx context.Context, dep *Deployment) error
}
