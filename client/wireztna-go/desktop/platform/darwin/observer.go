//go:build darwin

package darwin

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os/exec"
	"sort"
	"strconv"
	"strings"

	"github.com/wireztna/client/internal/tunnel"
	"golang.zx2c4.com/wireguard/wgctrl"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

type darwinDeviceIdentity struct {
	overlayIP netip.Addr
	peerKey   wgtypes.Key
}

type darwinRoute struct {
	destination netip.Prefix
	gateway     netip.Addr
	interfaceID string
}

type darwinObserver interface {
	Device(context.Context, darwinDeviceIdentity) (string, bool, error)
	Routes(context.Context) ([]darwinRoute, error)
}

type realDarwinObserver struct{}

func (realDarwinObserver) Device(ctx context.Context, identity darwinDeviceIdentity) (string, bool, error) {
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	client, err := wgctrl.New()
	if err != nil {
		return "", false, fmt.Errorf("open WireGuard observer: %w", err)
	}
	defer client.Close()
	devices, err := client.Devices()
	if err != nil {
		return "", false, fmt.Errorf("enumerate WireGuard devices: %w", err)
	}
	matches := make([]string, 0, 1)
	for _, device := range devices {
		if !strings.HasPrefix(device.Name, "utun") || !deviceHasPeer(device, identity.peerKey) {
			continue
		}
		iface, lookupErr := net.InterfaceByName(device.Name)
		if lookupErr != nil {
			return "", false, fmt.Errorf("inspect WireGuard device %q: %w", device.Name, lookupErr)
		}
		addresses, lookupErr := iface.Addrs()
		if lookupErr != nil {
			return "", false, fmt.Errorf("inspect WireGuard addresses on %q: %w", device.Name, lookupErr)
		}
		for _, address := range addresses {
			prefix, parseErr := netip.ParsePrefix(address.String())
			if parseErr == nil && prefix.Addr().Unmap() == identity.overlayIP.Unmap() {
				matches = append(matches, device.Name)
				break
			}
		}
	}
	if len(matches) == 0 {
		return "", false, nil
	}
	if len(matches) != 1 {
		return "", false, fmt.Errorf("WireGuard ownership is ambiguous: found %d matching devices", len(matches))
	}
	return matches[0], true, nil
}

func deviceHasPeer(device *wgtypes.Device, key wgtypes.Key) bool {
	for _, peer := range device.Peers {
		if peer.PublicKey == key {
			return true
		}
	}
	return false
}

func (realDarwinObserver) Routes(ctx context.Context) ([]darwinRoute, error) {
	observed := make([]darwinRoute, 0)
	for _, family := range []string{"inet", "inet6"} {
		ipv6 := family == "inet6"
		output, err := exec.CommandContext(ctx, "netstat", "-rn", "-f", family).CombinedOutput()
		if err != nil {
			return nil, fmt.Errorf("observe Darwin %s routes: %s: %w", family, strings.TrimSpace(string(output)), err)
		}
		for _, line := range strings.Split(string(output), "\n") {
			route, ok := parseDarwinNetstatLine(line, ipv6)
			if ok {
				observed = append(observed, route)
			}
		}
	}
	return observed, nil
}

func parseDarwinNetstatLine(line string, ipv6 bool) (darwinRoute, bool) {
	fields := strings.Fields(line)
	if len(fields) < 4 {
		return darwinRoute{}, false
	}
	prefix, err := parseDarwinRouteDestination(fields[0], ipv6)
	if err != nil {
		return darwinRoute{}, false
	}
	gatewayText := fields[1]
	if percent := strings.IndexByte(gatewayText, '%'); percent >= 0 {
		gatewayText = gatewayText[:percent]
	}
	gateway, _ := netip.ParseAddr(gatewayText)
	return darwinRoute{destination: prefix, gateway: gateway.Unmap(), interfaceID: fields[3]}, true
}

func parseDarwinRouteDestination(value string, ipv6 bool) (netip.Prefix, error) {
	if value == "default" {
		if ipv6 {
			return netip.MustParsePrefix("::/0"), nil
		}
		return netip.MustParsePrefix("0.0.0.0/0"), nil
	}
	if percent := strings.IndexByte(value, '%'); percent >= 0 {
		value = value[:percent]
	}
	if prefix, err := netip.ParsePrefix(value); err == nil {
		return prefix.Masked(), nil
	}
	addressText, bitsText, hasBits := strings.Cut(value, "/")
	if ipv6 {
		return netip.Prefix{}, errors.New("invalid IPv6 route destination")
	}
	parts := strings.Split(addressText, ".")
	if len(parts) == 0 || len(parts) > 4 {
		return netip.Prefix{}, errors.New("invalid IPv4 route destination")
	}
	componentCount := len(parts)
	for len(parts) < 4 {
		parts = append(parts, "0")
	}
	address, err := netip.ParseAddr(strings.Join(parts, "."))
	if err != nil {
		return netip.Prefix{}, err
	}
	bits := componentCount * 8
	if hasBits {
		bits, err = strconv.Atoi(bitsText)
		if err != nil || bits < 0 || bits > 32 {
			return netip.Prefix{}, errors.New("invalid IPv4 route prefix length")
		}
	}
	return netip.PrefixFrom(address, bits).Masked(), nil
}

func normalizeAllowedPrefixes(values []netip.Prefix) ([]netip.Prefix, error) {
	unique := make(map[netip.Prefix]struct{}, len(values)+1)
	for _, value := range values {
		if !value.IsValid() {
			return nil, errors.New("allowed prefix is invalid")
		}
		value = value.Masked()
		if value.Bits() == 0 {
			if value.Addr().Is4() {
				unique[netip.MustParsePrefix("0.0.0.0/1")] = struct{}{}
				unique[netip.MustParsePrefix("128.0.0.0/1")] = struct{}{}
			} else {
				unique[netip.MustParsePrefix("::/1")] = struct{}{}
				unique[netip.MustParsePrefix("8000::/1")] = struct{}{}
			}
			continue
		}
		unique[value] = struct{}{}
	}
	result := make([]netip.Prefix, 0, len(unique))
	for prefix := range unique {
		result = append(result, prefix)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].String() < result[j].String()
	})
	return result, nil
}

func exactRoutes(expected []netip.Prefix, interfaceID string, observed []darwinRoute) bool {
	if interfaceID == "" || len(observed) != len(expected) {
		return false
	}
	remaining := make(map[netip.Prefix]int, len(expected))
	for _, prefix := range expected {
		remaining[prefix.Masked()]++
	}
	for _, route := range observed {
		if route.interfaceID != interfaceID || remaining[route.destination.Masked()] != 1 {
			return false
		}
		delete(remaining, route.destination.Masked())
	}
	return len(remaining) == 0
}

func identityForConfig(cfg tunnel.Config) (darwinDeviceIdentity, error) {
	overlay, err := netip.ParsePrefix(withHostPrefix(cfg.OverlayIP))
	if err != nil {
		return darwinDeviceIdentity{}, errors.New("WireGuard overlay identity is invalid")
	}
	peerKey, err := wgtypes.ParseKey(cfg.BrokerPubKey)
	if err != nil {
		return darwinDeviceIdentity{}, errors.New("WireGuard peer identity is invalid")
	}
	return darwinDeviceIdentity{overlayIP: overlay.Addr(), peerKey: peerKey}, nil
}

func prefixesFromStrings(values []string) ([]netip.Prefix, error) {
	prefixes := make([]netip.Prefix, 0, len(values))
	for _, value := range values {
		prefix, err := netip.ParsePrefix(value)
		if err != nil {
			return nil, fmt.Errorf("invalid active allowed prefix %q", value)
		}
		prefixes = append(prefixes, prefix)
	}
	return normalizeAllowedPrefixes(prefixes)
}

func isFullTunnelPrefixes(prefixes []netip.Prefix) bool {
	seen := make(map[netip.Prefix]struct{}, len(prefixes))
	for _, prefix := range prefixes {
		seen[prefix.Masked()] = struct{}{}
	}
	_, lower4 := seen[netip.MustParsePrefix("0.0.0.0/1")]
	_, upper4 := seen[netip.MustParsePrefix("128.0.0.0/1")]
	_, lower6 := seen[netip.MustParsePrefix("::/1")]
	_, upper6 := seen[netip.MustParsePrefix("8000::/1")]
	return (lower4 && upper4) || (lower6 && upper6)
}

func ownedForwardingRoutes(state wireGuardState, observed []darwinRoute) []darwinRoute {
	result := make([]darwinRoute, 0, len(observed))
	for _, route := range observed {
		if route.interfaceID != state.ownedInterface {
			continue
		}
		address := route.destination.Addr().Unmap()
		if state.identity.overlayIP.IsValid() && address == state.identity.overlayIP.Unmap() && route.destination.Bits() == address.BitLen() {
			continue
		}
		if address.IsLinkLocalUnicast() || address.IsLinkLocalMulticast() || address.IsMulticast() {
			continue
		}
		result = append(result, route)
	}
	return result
}
