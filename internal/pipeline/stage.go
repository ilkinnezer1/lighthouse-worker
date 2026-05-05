package pipeline

import "context"

// Deployment is the immutable data passed to every stage in one run.
type Deployment struct {
	ID            string
	EnvironmentID string
	ProjectID     string
	OwnerID       string
	RepoURL       string
	Branch        string
	Subdomain     string
	BuildConfig   []byte // raw JSONB; Build stage parses it
	WorkDir       string // data/deployments/<id>/
}

// Stage Run must be idempotent, a redelivered job after a worker crash will
// call Run again on a stage that may already have done partial work.
type Stage interface {
	Name() string
	Run(ctx context.Context, dep *Deployment) error
}
