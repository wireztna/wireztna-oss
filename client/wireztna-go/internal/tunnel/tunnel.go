// Package tunnel manages the WireGuard interface lifecycle.
//
// Platform-specific implementations are in:
//   - tunnel_common_unix.go (shared macOS/Linux helpers)
//   - tunnel_darwin.go (macOS)
//   - tunnel_linux.go (Linux)
//   - tunnel_windows.go (Windows)
package tunnel

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"golang.zx2c4.com/wireguard/wgctrl"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

const (
	darwinInterfaceName = "wg-wireztna"
	darwinConfigPath    = "/Library/Application Support/WireZTNA/wireguard/wg-wireztna.conf"
)

func darwinWGQuickConfigPath(ifaceName string) (string, error) {
	if ifaceName != darwinInterfaceName {
		return "", fmt.Errorf("Darwin WireGuard interface must be %q", darwinInterfaceName)
	}
	return darwinConfigPath, nil
}

// Config holds the parameters needed to create a WireGuard tunnel.
type Config struct {
	InterfaceName  string
	PrivateKey     string
	OverlayIP      string // e.g., "10.200.1.5/32"
	BrokerPubKey   string
	BrokerEndpoint string // e.g., "203.0.113.10:51820"
	PresharedKey   string
	AllowedIPs     []string                // e.g., ["10.200.0.1/32", "10.50.0.0/16"]
	DNS            string                  // e.g., "10.200.0.1"
	IsExitNode     bool                    // If true, this is a full-tunnel VPN connection
	LogFunc        func(level, msg string) // Optional callback for diagnostic logging
}

// Tunnel represents an active WireGuard tunnel.
type Tunnel struct {
	config             Config
	client             *wgctrl.Client
	confPath           string // Path to generated config file
	preExitNodeGateway string // Default gateway captured before tunnel creation (Linux exit node)
}

// StatusInfo holds current tunnel status.
type StatusInfo struct {
	OverlayIP     string
	Endpoint      string
	LastHandshake time.Time
	RxBytes       int64
	TxBytes       int64
}

// BrokerRouteCleanupError reports that the interface was removed but the
// best-effort host-route cleanup was incomplete. Callers recreating a tunnel
// may continue, while disconnect callers can still surface the warning.
type BrokerRouteCleanupError struct {
	Err error
}

func (e *BrokerRouteCleanupError) Error() string {
	return e.Err.Error()
}

func (e *BrokerRouteCleanupError) Unwrap() error {
	return e.Err
}

// New creates a new Tunnel instance (does not bring it up yet).
func New(cfg Config) (*Tunnel, error) {
	wgClient, err := wgctrl.New()
	if err != nil {
		return nil, fmt.Errorf("failed to create wgctrl client: %w", err)
	}

	return &Tunnel{
		config: cfg,
		client: wgClient,
	}, nil
}

// log emits a diagnostic message via the configured LogFunc (if any).
func (t *Tunnel) log(level, msg string) {
	if t.config.LogFunc != nil {
		t.config.LogFunc(level, msg)
	}
}

// Up creates the WireGuard interface and configures it.
func (t *Tunnel) Up() error {
	// Check if interface already exists — if so, tear it down first
	if t.interfaceExists() {
		t.destroyInterface()
		time.Sleep(1 * time.Second)
	}

	// For Linux exit-node mode: capture the default gateway NOW, before we
	// create the tunnel interface or add any routes. After full-tunnel routes
	// are in place, `ip route show default` may return stale/wrong results.
	if t.config.IsExitNode && runtime.GOOS == "linux" {
		t.preExitNodeGateway = getLinuxDefaultGateway()
		if t.preExitNodeGateway != "" {
			t.log("debug", fmt.Sprintf("Exit node: pre-captured default gateway: %s", t.preExitNodeGateway))
		} else {
			t.log("warn", "Exit node: could not detect default gateway before tunnel creation")
		}
	}

	// On macOS/Windows, the platform-specific createInterfaceOS handles everything
	// (config writing, service install, address assignment, routes).
	// On Linux, we do manual setup via wgctrl after creating the interface.
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		if err := t.createInterfaceOS(); err != nil {
			// createInterfaceOS may already have installed an exclusion route or
			// partially created an adapter. Always unwind both on failure.
			t.destroyInterface()
			return err
		}
		return nil
	}

	// Linux path: manual setup via wgctrl
	if err := t.createInterfaceOS(); err != nil {
		return fmt.Errorf("create interface: %w", err)
	}

	if err := t.configureWireGuard(); err != nil {
		t.destroyInterface()
		return fmt.Errorf("configure wireguard: %w", err)
	}

	if err := t.assignAddressOS(); err != nil {
		t.destroyInterface()
		return fmt.Errorf("assign address: %w", err)
	}

	if err := t.addRoutesOS(); err != nil {
		t.destroyInterface()
		return fmt.Errorf("add routes: %w", err)
	}

	return nil
}

// interfaceExists checks whether the WireGuard interface is already present.
func (t *Tunnel) interfaceExists() bool {
	// Try wgctrl first
	if t.client != nil {
		if _, err := t.client.Device(t.config.InterfaceName); err == nil {
			return true
		}
	}
	// Platform-specific check
	return t.interfaceExistsOS()
}

// Down tears down the WireGuard interface.
func (t *Tunnel) Down() {
	t.destroyInterface()
}

// destroyInterface delegates to platform-specific implementation.
func (t *Tunnel) destroyInterface() {
	t.destroyInterfaceOS()
}

// UpdatePSK hot-swaps the preshared key without tearing down the tunnel.
func (t *Tunnel) UpdatePSK(newPSK string) error {
	// Try platform-specific method first (Windows uses in-process device)
	if err := t.updatePSKOS(newPSK); err == nil {
		return nil
	}

	// Fallback: use wgctrl (works on Linux/macOS where kernel WG is used)
	psk, err := wgtypes.ParseKey(newPSK)
	if err != nil {
		return fmt.Errorf("invalid PSK: %w", err)
	}

	brokerKey, err := wgtypes.ParseKey(t.config.BrokerPubKey)
	if err != nil {
		return fmt.Errorf("invalid broker key: %w", err)
	}

	cfg := wgtypes.Config{
		Peers: []wgtypes.PeerConfig{
			{
				PublicKey:         brokerKey,
				UpdateOnly:        true,
				PresharedKey:      &psk,
				ReplaceAllowedIPs: false,
			},
		},
	}

	return t.client.ConfigureDevice(t.config.InterfaceName, cfg)
}

// configureWireGuard sets the private key, peer, and PSK via wgctrl (Linux only).
func (t *Tunnel) configureWireGuard() error {
	privKey, err := wgtypes.ParseKey(t.config.PrivateKey)
	if err != nil {
		return fmt.Errorf("invalid private key: %w", err)
	}

	brokerKey, err := wgtypes.ParseKey(t.config.BrokerPubKey)
	if err != nil {
		return fmt.Errorf("invalid broker public key: %w", err)
	}

	endpoint, err := net.ResolveUDPAddr("udp", t.config.BrokerEndpoint)
	if err != nil {
		return fmt.Errorf("invalid broker endpoint: %w", err)
	}

	var allowedIPs []net.IPNet
	for _, cidr := range t.config.AllowedIPs {
		_, ipNet, err := net.ParseCIDR(cidr)
		if err != nil {
			return fmt.Errorf("invalid CIDR %q: %w", cidr, err)
		}
		allowedIPs = append(allowedIPs, *ipNet)
	}

	var psk *wgtypes.Key
	if t.config.PresharedKey != "" {
		k, err := wgtypes.ParseKey(t.config.PresharedKey)
		if err != nil {
			return fmt.Errorf("invalid PSK: %w", err)
		}
		psk = &k
	}

	keepAlive := 25 * time.Second

	cfg := wgtypes.Config{
		PrivateKey: &privKey,
		Peers: []wgtypes.PeerConfig{
			{
				PublicKey:                   brokerKey,
				Endpoint:                    endpoint,
				PresharedKey:                psk,
				AllowedIPs:                  allowedIPs,
				PersistentKeepaliveInterval: &keepAlive,
				ReplaceAllowedIPs:           true,
			},
		},
		ReplacePeers: true,
	}

	return t.client.ConfigureDevice(t.config.InterfaceName, cfg)
}

// GetStatus returns the current status of a WireGuard interface.
func GetStatus(ifaceName string) (*StatusInfo, error) {
	// On Windows, use in-process device UAPI (wgctrl can't reach our userspace device)
	if runtime.GOOS == "windows" {
		return getStatusInProcess(ifaceName)
	}

	wgClient, err := wgctrl.New()
	if err != nil {
		return nil, err
	}
	defer wgClient.Close()

	device, err := wgClient.Device(ifaceName)
	if err != nil {
		if runtime.GOOS != "darwin" {
			return nil, fmt.Errorf("interface %q not found: %w", ifaceName, err)
		}

		// wg-quick exposes named configurations as utun devices on macOS. Resolve
		// only the device whose peer and overlay address match this configuration;
		// never adopt an unrelated WireGuard VPN merely because it has peers.
		device, err = findDarwinConfigDevice(wgClient, ifaceName)
		if err != nil {
			return nil, fmt.Errorf("interface %q not found: %w", ifaceName, err)
		}
	}

	info := &StatusInfo{}

	if len(device.Peers) > 0 {
		peer := device.Peers[0]
		info.LastHandshake = peer.LastHandshakeTime
		info.RxBytes = peer.ReceiveBytes
		info.TxBytes = peer.TransmitBytes
		if peer.Endpoint != nil {
			info.Endpoint = peer.Endpoint.String()
		}
	}

	iface, err := net.InterfaceByName(device.Name)
	if err == nil {
		addrs, _ := iface.Addrs()
		for _, addr := range addrs {
			info.OverlayIP = addr.String()
			break
		}
	}

	return info, nil
}

// findDarwinConfigDevice maps a wg-quick configuration name to its utun
// device by matching both the configured peer key and local overlay address.
func findDarwinConfigDevice(wgClient *wgctrl.Client, ifaceName string) (*wgtypes.Device, error) {
	confPath, err := darwinWGQuickConfigPath(ifaceName)
	if err != nil {
		return nil, err
	}
	file, err := os.Open(confPath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var peerKey *wgtypes.Key
	var overlayIP net.IP
	section := ""
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.Trim(line, "[]")
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])
		switch {
		case section == "Interface" && key == "Address":
			address := strings.TrimSpace(strings.Split(value, ",")[0])
			if ip, _, parseErr := net.ParseCIDR(address); parseErr == nil {
				overlayIP = ip
			} else {
				overlayIP = net.ParseIP(address)
			}
		case section == "Peer" && key == "PublicKey":
			parsed, parseErr := wgtypes.ParseKey(value)
			if parseErr != nil {
				return nil, fmt.Errorf("invalid peer key in %s: %w", confPath, parseErr)
			}
			peerKey = &parsed
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if peerKey == nil || overlayIP == nil {
		return nil, fmt.Errorf("configuration identity is incomplete")
	}

	devices, err := wgClient.Devices()
	if err != nil {
		return nil, err
	}
	var match *wgtypes.Device
	for _, candidate := range devices {
		peerMatches := false
		for _, peer := range candidate.Peers {
			if peer.PublicKey == *peerKey {
				peerMatches = true
				break
			}
		}
		if !peerMatches || !interfaceHasIP(candidate.Name, overlayIP) {
			continue
		}
		if match != nil {
			return nil, fmt.Errorf("multiple devices match configuration")
		}
		match = candidate
	}
	if match == nil {
		return nil, fmt.Errorf("no active device matches configuration")
	}
	return match, nil
}

func interfaceHasIP(ifaceName string, expected net.IP) bool {
	iface, err := net.InterfaceByName(ifaceName)
	if err != nil {
		return false
	}
	addrs, err := iface.Addrs()
	if err != nil {
		return false
	}
	for _, addr := range addrs {
		ipText := strings.SplitN(addr.String(), "/", 2)[0]
		if ip := net.ParseIP(ipText); ip != nil && ip.Equal(expected) {
			return true
		}
	}
	return false
}

// Down tears down a named interface (standalone, without a Tunnel instance).
func Down(ifaceName string) error {
	return downOS(ifaceName)
}

// DownWithEndpoint tears down a named interface and removes the host route
// that full-tunnel mode installed to keep the broker endpoint outside the VPN.
// Callers with loaded client configuration should prefer this over Down.
func DownWithEndpoint(ifaceName, brokerEndpoint string) error {
	return downWithEndpointOS(ifaceName, brokerEndpoint)
}

// PingHost sends a single ICMP ping to check reachability.
func PingHost(host string) bool {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("ping", "-c", "1", "-W", "2000", host)
	case "windows":
		cmd = exec.Command("ping", "-n", "1", "-w", "2000", host)
	default:
		cmd = exec.Command("ping", "-c", "1", "-W", "2", host)
	}
	return cmd.Run() == nil
}
