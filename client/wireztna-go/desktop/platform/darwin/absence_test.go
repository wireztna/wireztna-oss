//go:build darwin

package darwin

import (
	"context"
	"strings"
	"testing"

	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"

	"github.com/wireztna/client/internal/tunnel"
)

func TestVerifyOwnedWireGuardAbsentBlocksHistoricalAppliedIdentity(t *testing.T) {
	currentKey, err := wgtypes.GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	historicalKey, err := wgtypes.GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	current := tunnel.Config{OverlayIP: "10.200.0.22/32", BrokerPubKey: currentKey.PublicKey().String()}
	historical := tunnel.Config{OverlayIP: "10.200.0.11/32", BrokerPubKey: historicalKey.PublicKey().String()}
	historicalIdentity, err := identityForConfig(historical)
	if err != nil {
		t.Fatal(err)
	}

	observed := 0
	err = verifyOwnedWireGuardAbsent(
		context.Background(),
		current,
		func() (tunnel.Config, bool, error) { return historical, true, nil },
		func(_ context.Context, identity darwinDeviceIdentity) (string, bool, error) {
			observed++
			if identity == historicalIdentity {
				return "utun42", true, nil
			}
			return "", false, nil
		},
	)
	if err == nil || !strings.Contains(err.Error(), "utun42") {
		t.Fatalf("historical identity verification error = %v", err)
	}
	if observed != 2 {
		t.Fatalf("observed identities = %d, want current and historical", observed)
	}
}
