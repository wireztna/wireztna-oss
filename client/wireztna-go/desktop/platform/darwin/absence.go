//go:build darwin

package darwin

import (
	"context"
	"errors"

	"github.com/wireztna/client/desktop/controller"
	"github.com/wireztna/client/internal/config"
	"github.com/wireztna/client/internal/tunnel"
)

// VerifyOwnedResourcesAbsent is a read-only fail-closed check used before an
// uninstall or upgrade removes recovery evidence. It checks both the current
// owner config and the root-owned applied wg-quick identity, because the owner
// may have changed config.yaml after the residual utun was created.
func VerifyOwnedResourcesAbsent(ctx context.Context, owner controller.OwnerID, cfg *config.ClientConfig) error {
	if ctx == nil || owner == "" || cfg == nil {
		return errors.New("Darwin absence verification dependencies are incomplete")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	validated := cloneClientConfig(cfg)
	if err := ValidateClientConfig(validated); err != nil {
		return err
	}
	if err := verifyOwnedWireGuardAbsent(
		ctx,
		tunnel.Config{OverlayIP: validated.OverlayIP, BrokerPubKey: validated.BrokerPublicKey},
		tunnel.DarwinAppliedIdentity,
		func(observeCtx context.Context, identity darwinDeviceIdentity) (string, bool, error) {
			return (realDarwinObserver{}).Device(observeCtx, identity)
		},
	); err != nil {
		return err
	}
	marker, err := (realBrokerRouteStore{interfaceName: validated.Interface}).Load()
	if err != nil {
		return err
	}
	if marker != nil {
		return errors.New("owned broker exclusion route marker is still present")
	}
	resolvers, err := newResolverDNSBackend("/etc/resolver").Snapshot(owner)
	if err != nil {
		return err
	}
	if len(resolvers) != 0 {
		return errors.New("owned resolver entries are still present")
	}
	return ctx.Err()
}

type appliedIdentityLoader func() (tunnel.Config, bool, error)
type ownedDeviceObserver func(context.Context, darwinDeviceIdentity) (string, bool, error)

func verifyOwnedWireGuardAbsent(ctx context.Context, currentConfig tunnel.Config, loadApplied appliedIdentityLoader, observe ownedDeviceObserver) error {
	if ctx == nil || loadApplied == nil || observe == nil {
		return errors.New("WireGuard absence dependencies are incomplete")
	}
	currentIdentity, err := identityForConfig(currentConfig)
	if err != nil {
		return err
	}
	identities := []darwinDeviceIdentity{currentIdentity}
	appliedConfig, appliedPresent, err := loadApplied()
	if err != nil {
		return err
	}
	if appliedPresent {
		appliedIdentity, identityErr := identityForConfig(appliedConfig)
		if identityErr != nil {
			return identityErr
		}
		if appliedIdentity != currentIdentity {
			identities = append(identities, appliedIdentity)
		}
	}
	for _, identity := range identities {
		if interfaceID, present, observeErr := observe(ctx, identity); observeErr != nil {
			return observeErr
		} else if present {
			return errors.New("owned WireGuard device is still present: " + interfaceID)
		}
	}
	return ctx.Err()
}
