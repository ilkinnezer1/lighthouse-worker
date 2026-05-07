package routing

import "strings"

// Mirrors Nest's `buildDeployUrl` in lighthouse-api/src/common/url.ts.
// Keep these two in lock-step: a divergent host on either side breaks Traefik routing.

type Mode string

const (
	ModeLocal  Mode = "local"
	ModeServer Mode = "server"
)

type HostParts struct {
	Mode             Mode
	BaseDomain       string
	Username         string
	ProjectSubdomain string
	EnvSubdomain     string
	CustomDomain     string
}

// Host returns the bare hostname (no scheme) Traefik should match on.
// Custom domain wins outright when set.
func Host(p HostParts) string {
	if d := strings.TrimSpace(p.CustomDomain); d != "" {
		return d
	}
	return p.EnvSubdomain + "." + p.ProjectSubdomain + "." + p.Username + "." + p.BaseDomain
}

// IsServer reports whether TLS labels should be attached.
func (m Mode) IsServer() bool { return m == ModeServer }
