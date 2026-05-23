package database

import (
	"context"
	"fmt"
	"strings"

	"github.com/docker/docker/api/types/network"
)

func (r *Router) ensureNetwork(ctx context.Context, name string) error {
	_, err := r.docker.NetworkCreate(ctx, name, network.CreateOptions{Driver: "bridge"})
	if err == nil {
		return nil
	}
	if strings.Contains(err.Error(), "already exists") {
		return nil
	}
	return fmt.Errorf("create network %s: %w", name, err)
}
