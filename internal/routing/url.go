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

func Host(p HostParts) string {
	if d := strings.TrimSpace(p.CustomDomain); d != "" {
		return d
	}
	return p.EnvSubdomain + "." + p.ProjectSubdomain + "." + p.Username + "." + p.BaseDomain
}

func (m Mode) IsServer() bool { return m == ModeServer }
