//go:build !darwin

package v2

import (
	"context"
	"errors"
)

// ServeDarwin preserves the composition-root API on non-Darwin builds.
func ServeDarwin(ctx context.Context, server *Server, config DarwinTransportConfig) error {
	if ctx == nil {
		return errors.New("Darwin IPC context is required")
	}
	if server == nil {
		return errors.New("Darwin IPC server is required")
	}
	if _, err := config.normalized(); err != nil {
		return err
	}
	return ErrDarwinTransportUnsupported
}
