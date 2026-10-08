package firewall

import (
	"fmt"
	"net"
	"strings"

	"github.com/coreos/go-iptables/iptables"
	"github.com/vishvananda/netlink"
	"github.com/wireztna/publisher/internal/config"
)

const (
	brokerSubnet  = "10.100.0.0/16"
	overlaySubnet = "10.200.0.0/16"
)

// Setup configures iptables masquerade and forwarding rules
func Setup() error {
	ipt, err := iptables.New()
	if err != nil {
		return fmt.Errorf("failed to initialize iptables: %w", err)
	}

	mainIF, err := detectMainInterface()
	if err != nil {
		return fmt.Errorf("failed to detect main interface: %w", err)
	}

	wgIF := config.DefaultWGInterface
	fmt.Printf("[*] Configuring firewall (%s <-> %s with masquerade)...\n", wgIF, mainIF)

	// NAT rules: masquerade traffic from broker/overlay subnets going out to LAN
	natRules := [][]string{
		{"-s", brokerSubnet, "-o", mainIF, "-j", "MASQUERADE"},
		{"-s", overlaySubnet, "-o", mainIF, "-j", "MASQUERADE"},
	}

	for _, rule := range natRules {
		exists, err := ipt.Exists("nat", "POSTROUTING", rule...)
		if err != nil {
			return fmt.Errorf("failed to check NAT rule: %w", err)
		}
		if !exists {
			if err := ipt.Append("nat", "POSTROUTING", rule...); err != nil {
				return fmt.Errorf("failed to add NAT rule: %w", err)
			}
		}
	}

	// Forward rules: allow traffic between WG and main interface
	fwdRules := []struct {
		pos  int
		rule []string
	}{
		{1, []string{"-i", wgIF, "-o", mainIF, "-j", "ACCEPT"}},
		{2, []string{"-i", mainIF, "-o", wgIF, "-m", "state", "--state", "RELATED,ESTABLISHED", "-j", "ACCEPT"}},
	}

	for _, fr := range fwdRules {
		exists, err := ipt.Exists("filter", "FORWARD", fr.rule...)
		if err != nil {
			return fmt.Errorf("failed to check FORWARD rule: %w", err)
		}
		if !exists {
			if err := ipt.Insert("filter", "FORWARD", fr.pos, fr.rule...); err != nil {
				return fmt.Errorf("failed to add FORWARD rule: %w", err)
			}
		}
	}

	fmt.Printf("[✓] Firewall configured (%s <-> %s)\n", wgIF, mainIF)
	return nil
}

// Teardown removes all iptables rules created by Setup
func Teardown() error {
	ipt, err := iptables.New()
	if err != nil {
		return fmt.Errorf("failed to initialize iptables: %w", err)
	}

	mainIF, err := detectMainInterface()
	if err != nil {
		// Interface might not exist anymore during uninstall, try common names
		mainIF = "eth0"
	}

	wgIF := config.DefaultWGInterface

	// Remove NAT rules
	natRules := [][]string{
		{"-s", brokerSubnet, "-o", mainIF, "-j", "MASQUERADE"},
		{"-s", overlaySubnet, "-o", mainIF, "-j", "MASQUERADE"},
	}

	for _, rule := range natRules {
		// Ignore errors (rules may not exist)
		_ = ipt.Delete("nat", "POSTROUTING", rule...)
	}

	// Remove forward rules
	fwdRules := [][]string{
		{"-i", wgIF, "-o", mainIF, "-j", "ACCEPT"},
		{"-i", mainIF, "-o", wgIF, "-m", "state", "--state", "RELATED,ESTABLISHED", "-j", "ACCEPT"},
	}

	for _, rule := range fwdRules {
		_ = ipt.Delete("filter", "FORWARD", rule...)
	}

	return nil
}

// EnableIPForwarding enables net.ipv4.ip_forward via sysctl
func EnableIPForwarding() error {
	data := []byte("1")
	if err := writeFile("/proc/sys/net/ipv4/ip_forward", data); err != nil {
		return fmt.Errorf("failed to enable IP forwarding (need root/CAP_NET_ADMIN): %w", err)
	}
	return nil
}

// detectMainInterface finds the interface with the default route
func detectMainInterface() (string, error) {
	routes, err := netlink.RouteList(nil, netlink.FAMILY_V4)
	if err != nil {
		return "", fmt.Errorf("failed to list routes: %w", err)
	}

	for _, route := range routes {
		// Default route has nil Dst
		if route.Dst == nil || route.Dst.IP.Equal(net.IPv4zero) {
			link, err := netlink.LinkByIndex(route.LinkIndex)
			if err != nil {
				continue
			}
			name := link.Attrs().Name
			// Skip WireGuard and loopback interfaces
			if name != config.DefaultWGInterface && name != "lo" && !strings.HasPrefix(name, "wg-") {
				return name, nil
			}
		}
	}

	return "", fmt.Errorf("no default route found")
}

// writeFile is a helper to write to /proc files
func writeFile(path string, data []byte) error {
	f, err := openFile(path)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(data)
	return err
}
