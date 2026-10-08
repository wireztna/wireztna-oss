package tunnel

import (
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/vishvananda/netlink"
	"github.com/wireztna/publisher/internal/config"
	"golang.zx2c4.com/wireguard/wgctrl"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

// Create sets up the WireGuard interface with the given configuration.
// This replaces wg-quick entirely using netlink + wgctrl.
func Create(state *config.PublisherState, privateKeyStr string, listenPort int) error {
	ifaceName := config.DefaultWGInterface

	// Clean up any existing interface
	if link, err := netlink.LinkByName(ifaceName); err == nil {
		fmt.Printf("[*] Removing existing interface %s...\n", ifaceName)
		if err := netlink.LinkDel(link); err != nil {
			return fmt.Errorf("failed to delete existing interface: %w", err)
		}
		time.Sleep(500 * time.Millisecond)
	}

	// 1. Create WireGuard interface
	la := netlink.NewLinkAttrs()
	la.Name = ifaceName
	wgLink := &netlink.GenericLink{
		LinkAttrs: la,
		LinkType:  "wireguard",
	}

	if err := netlink.LinkAdd(wgLink); err != nil {
		return fmt.Errorf("failed to create WireGuard interface (is the wireguard kernel module loaded?): %w", err)
	}

	// 2. Get the created link
	link, err := netlink.LinkByName(ifaceName)
	if err != nil {
		return fmt.Errorf("failed to find created interface: %w", err)
	}

	// 3. Assign tunnel IP
	tunnelIP := net.ParseIP(state.TunnelIP)
	if tunnelIP == nil {
		netlink.LinkDel(link)
		return fmt.Errorf("invalid tunnel IP: %s", state.TunnelIP)
	}

	addr := &netlink.Addr{
		IPNet: &net.IPNet{
			IP:   tunnelIP,
			Mask: net.CIDRMask(32, 32),
		},
	}
	if err := netlink.AddrAdd(link, addr); err != nil {
		netlink.LinkDel(link)
		return fmt.Errorf("failed to assign IP address: %w", err)
	}

	// 4. Configure WireGuard via wgctrl
	privateKey, err := wgtypes.ParseKey(privateKeyStr)
	if err != nil {
		netlink.LinkDel(link)
		return fmt.Errorf("failed to parse private key: %w", err)
	}

	brokerPubKey, err := wgtypes.ParseKey(state.BrokerPubKey)
	if err != nil {
		netlink.LinkDel(link)
		return fmt.Errorf("failed to parse broker public key: %w", err)
	}

	// Parse broker endpoint
	brokerEndpoint, err := net.ResolveUDPAddr("udp", state.BrokerEndpoint)
	if err != nil {
		netlink.LinkDel(link)
		return fmt.Errorf("failed to resolve broker endpoint %s: %w", state.BrokerEndpoint, err)
	}

	// Parse allowed IPs
	allowedIPs, err := parseAllowedIPs(state.AllowedIPs)
	if err != nil {
		netlink.LinkDel(link)
		return fmt.Errorf("failed to parse allowed IPs: %w", err)
	}

	keepalive := 25 * time.Second

	wgConfig := wgtypes.Config{
		PrivateKey:   &privateKey,
		ListenPort:   &listenPort,
		ReplacePeers: true,
		Peers: []wgtypes.PeerConfig{
			{
				PublicKey:                   brokerPubKey,
				Endpoint:                    brokerEndpoint,
				AllowedIPs:                  allowedIPs,
				PersistentKeepaliveInterval: &keepalive,
				ReplaceAllowedIPs:           true,
			},
		},
	}

	client, err := wgctrl.New()
	if err != nil {
		netlink.LinkDel(link)
		return fmt.Errorf("failed to create wgctrl client: %w", err)
	}
	defer client.Close()

	if err := client.ConfigureDevice(ifaceName, wgConfig); err != nil {
		netlink.LinkDel(link)
		return fmt.Errorf("failed to configure WireGuard device: %w", err)
	}

	// 5. Bring interface up
	if err := netlink.LinkSetUp(link); err != nil {
		netlink.LinkDel(link)
		return fmt.Errorf("failed to bring interface up: %w", err)
	}

	// 6. Add routes for allowed IPs
	for _, ipNet := range allowedIPs {
		route := &netlink.Route{
			Dst:       &ipNet,
			LinkIndex: link.Attrs().Index,
		}
		// Ignore "file exists" errors (route may already exist)
		if err := netlink.RouteAdd(route); err != nil && !strings.Contains(err.Error(), "file exists") {
			fmt.Printf("[WARN] Failed to add route for %s: %v\n", ipNet.String(), err)
		}
	}

	return nil
}

// Destroy removes the WireGuard interface
func Destroy() error {
	ifaceName := config.DefaultWGInterface

	link, err := netlink.LinkByName(ifaceName)
	if err != nil {
		// Interface doesn't exist — nothing to do
		return nil
	}

	if err := netlink.LinkDel(link); err != nil {
		return fmt.Errorf("failed to delete WireGuard interface: %w", err)
	}

	return nil
}

// IsUp checks if the WireGuard interface exists and is running
func IsUp() bool {
	link, err := netlink.LinkByName(config.DefaultWGInterface)
	if err != nil {
		return false
	}
	return link.Attrs().OperState == netlink.OperUp || link.Attrs().Flags&net.FlagUp != 0
}

// GetStatus returns current WireGuard peer status (handshake age, transfer)
type PeerStatus struct {
	PublicKey       string
	Endpoint       string
	HandshakeAge   time.Duration
	RxBytes        int64
	TxBytes        int64
	HasHandshake   bool
}

func GetPeerStatus() (*PeerStatus, error) {
	client, err := wgctrl.New()
	if err != nil {
		return nil, fmt.Errorf("failed to create wgctrl client: %w", err)
	}
	defer client.Close()

	device, err := client.Device(config.DefaultWGInterface)
	if err != nil {
		return nil, fmt.Errorf("failed to get WireGuard device: %w", err)
	}

	if len(device.Peers) == 0 {
		return nil, fmt.Errorf("no peers configured")
	}

	peer := device.Peers[0]
	status := &PeerStatus{
		PublicKey: peer.PublicKey.String(),
		RxBytes:  peer.ReceiveBytes,
		TxBytes:  peer.TransmitBytes,
	}

	if peer.Endpoint != nil {
		status.Endpoint = peer.Endpoint.String()
	}

	if !peer.LastHandshakeTime.IsZero() {
		status.HasHandshake = true
		status.HandshakeAge = time.Since(peer.LastHandshakeTime)
	}

	return status, nil
}

// UpdatePeer updates the broker peer configuration (used by watchdog for recovery)
func UpdatePeer(newPubKey string, newEndpoint string, allowedIPsStr string) error {
	client, err := wgctrl.New()
	if err != nil {
		return fmt.Errorf("failed to create wgctrl client: %w", err)
	}
	defer client.Close()

	ifaceName := config.DefaultWGInterface

	// Get current device to find existing peer
	device, err := client.Device(ifaceName)
	if err != nil {
		return fmt.Errorf("failed to get device: %w", err)
	}

	// Remove old peers
	var removePeers []wgtypes.PeerConfig
	for _, p := range device.Peers {
		removePeers = append(removePeers, wgtypes.PeerConfig{
			PublicKey: p.PublicKey,
			Remove:   true,
		})
	}
	if len(removePeers) > 0 {
		removeConfig := wgtypes.Config{Peers: removePeers}
		if err := client.ConfigureDevice(ifaceName, removeConfig); err != nil {
			return fmt.Errorf("failed to remove old peers: %w", err)
		}
	}

	// Add new peer
	pubKey, err := wgtypes.ParseKey(newPubKey)
	if err != nil {
		return fmt.Errorf("failed to parse new public key: %w", err)
	}

	endpoint, err := net.ResolveUDPAddr("udp", newEndpoint)
	if err != nil {
		return fmt.Errorf("failed to resolve new endpoint: %w", err)
	}

	allowedIPs, err := parseAllowedIPs(allowedIPsStr)
	if err != nil {
		return fmt.Errorf("failed to parse allowed IPs: %w", err)
	}

	keepalive := 25 * time.Second
	addConfig := wgtypes.Config{
		Peers: []wgtypes.PeerConfig{
			{
				PublicKey:                   pubKey,
				Endpoint:                    endpoint,
				AllowedIPs:                  allowedIPs,
				PersistentKeepaliveInterval: &keepalive,
				ReplaceAllowedIPs:           true,
			},
		},
	}

	if err := client.ConfigureDevice(ifaceName, addConfig); err != nil {
		return fmt.Errorf("failed to add new peer: %w", err)
	}

	return nil
}

// parseAllowedIPs parses a comma-separated list of CIDRs
func parseAllowedIPs(allowedIPs string) ([]net.IPNet, error) {
	var result []net.IPNet
	for _, cidr := range strings.Split(allowedIPs, ",") {
		cidr = strings.TrimSpace(cidr)
		if cidr == "" {
			continue
		}
		_, ipNet, err := net.ParseCIDR(cidr)
		if err != nil {
			return nil, fmt.Errorf("invalid CIDR %q: %w", cidr, err)
		}
		result = append(result, *ipNet)
	}
	return result, nil
}
