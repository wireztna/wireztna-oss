//go:build windows

// Windows tunnel implementation using WireGuard NT (kernel driver).
//
// This replaces the previous wintun + wireguard-go userspace implementation.
// WireGuard NT performs all crypto in the NT kernel, eliminating the
// kernel→userspace→kernel packet copy overhead of the userspace approach.
//
// Architecture:
//   - Adapter lifecycle (create/close): via wireguard.dll (internal/wgnt package)
//   - WireGuard configuration: via wgctrl (IOCTL to WireGuard NT driver)
//   - IP/routes/DNS: unchanged (netsh, route commands)
//
// Requirements:
//   - wireguard.dll (embedded, extracted at runtime)
//   - Must run with administrator privileges (LocalSystem via service, or UAC)
//   - Windows 10 1803+ or Windows 11
package tunnel

import (
	"fmt"
	"net"
	"net/netip"
	"os/exec"
	"strings"
	"time"

	"golang.zx2c4.com/wireguard/wgctrl"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"

	"github.com/wireztna/client/internal/wgnt"
)

// wgAdapter holds the active WireGuard NT adapter handle.
var wgAdapter wgnt.AdapterHandle

// CleanupZombieAdapter removes any leftover adapter from a previous crash.
func CleanupZombieAdapter(ifaceName string) {
	if wgAdapter != 0 {
		wgAdapter.Close()
		wgAdapter = 0
		time.Sleep(500 * time.Millisecond)
	}
	if orphan, err := wgnt.OpenAdapter(ifaceName); err == nil {
		orphan.Close()
		time.Sleep(500 * time.Millisecond)
	}
}

func (t *Tunnel) createInterfaceOS() error {
	// Clean up any existing adapter
	if wgAdapter != 0 {
		wgAdapter.Close()
		wgAdapter = 0
		time.Sleep(500 * time.Millisecond)
	}

	// Create WireGuard NT adapter via wireguard.dll
	adapter, err := wgnt.CreateAdapter(t.config.InterfaceName)
	if err != nil {
		return fmt.Errorf("failed to create WireGuard NT adapter '%s': %w\n"+
			"Ensure wireguard.dll is available and you're running with administrator privileges",
			t.config.InterfaceName, err)
	}

	// Resolve endpoint
	endpoint, err := resolveEndpoint(t.config.BrokerEndpoint)
	if err != nil {
		adapter.Close()
		return fmt.Errorf("cannot resolve broker endpoint: %w", err)
	}

	// Configure WireGuard via wgctrl (uses proper struct serialization via IOCTL)
	if err := configureViaWgctrl(t.config, endpoint); err != nil {
		adapter.Close()
		return fmt.Errorf("WireGuard configuration failed: %w", err)
	}

	// For exit-node: add broker exclusion route BEFORE bringing up
	if t.config.IsExitNode {
		t.addBrokerExclusionRoute()
	}

	// Bring the adapter up
	if err := adapter.SetState(wgnt.AdapterStateUp); err != nil {
		adapter.Close()
		return fmt.Errorf("WireGuard adapter up failed: %w", err)
	}
	wgAdapter = adapter

	// Wait for interface to register in Windows
	time.Sleep(1 * time.Second)

	// Assign IP, MTU, routes
	if err := t.assignAddressOS(); err != nil {
		adapter.Close()
		wgAdapter = 0
		return fmt.Errorf("IP assignment failed: %w", err)
	}
	t.setMTU(1420)

	if err := t.addRoutesOS(); err != nil {
		_ = err
	}

	return nil
}

// configureViaWgctrl uses the wgctrl library to configure the WireGuard device.
// wgctrl handles the struct serialization correctly (proper offsets, alignment)
// for the WireGuard NT IOCTL interface — no manual byte buffer construction needed.
func configureViaWgctrl(cfg Config, endpoint string) error {
	wgClient, err := wgctrl.New()
	if err != nil {
		return fmt.Errorf("wgctrl client: %w", err)
	}
	defer wgClient.Close()

	privKey, err := wgtypes.ParseKey(cfg.PrivateKey)
	if err != nil {
		return fmt.Errorf("invalid private key: %w", err)
	}

	peerPubKey, err := wgtypes.ParseKey(cfg.BrokerPubKey)
	if err != nil {
		return fmt.Errorf("invalid broker public key: %w", err)
	}

	udpEndpoint, err := net.ResolveUDPAddr("udp", endpoint)
	if err != nil {
		return fmt.Errorf("invalid endpoint: %w", err)
	}

	// Build AllowedIPs — split 0.0.0.0/0 into /1 pairs to avoid WireGuard NT kill-switch
	var allowedIPs []net.IPNet
	for _, cidr := range cfg.AllowedIPs {
		if cidr == "0.0.0.0/0" {
			_, net1, _ := net.ParseCIDR("0.0.0.0/1")
			_, net2, _ := net.ParseCIDR("128.0.0.0/1")
			allowedIPs = append(allowedIPs, *net1, *net2)
			continue
		}
		_, ipNet, err := net.ParseCIDR(cidr)
		if err != nil {
			continue
		}
		allowedIPs = append(allowedIPs, *ipNet)
	}

	keepalive := 25 * time.Second
	replaceAllowedIPs := true

	peer := wgtypes.PeerConfig{
		PublicKey:                   peerPubKey,
		Endpoint:                    udpEndpoint,
		AllowedIPs:                  allowedIPs,
		PersistentKeepaliveInterval: &keepalive,
		ReplaceAllowedIPs:           replaceAllowedIPs,
	}

	if cfg.PresharedKey != "" {
		psk, err := wgtypes.ParseKey(cfg.PresharedKey)
		if err != nil {
			return fmt.Errorf("invalid PSK: %w", err)
		}
		peer.PresharedKey = &psk
	}

	return wgClient.ConfigureDevice(cfg.InterfaceName, wgtypes.Config{
		PrivateKey: &privKey,
		Peers:      []wgtypes.PeerConfig{peer},
	})
}

func (t *Tunnel) destroyInterfaceOS() {
	if t.config.IsExitNode {
		_ = exec.Command("route", "delete", "0.0.0.0", "mask", "128.0.0.0").Run()
		_ = exec.Command("route", "delete", "128.0.0.0", "mask", "128.0.0.0").Run()
		t.removeBrokerExclusionRoute()
	}
	if wgAdapter != 0 {
		wgAdapter.Close()
		wgAdapter = 0
	}
}

func (t *Tunnel) updatePSKOS(newPSK string) error {
	// Use wgctrl to update PSK — proper struct serialization
	wgClient, err := wgctrl.New()
	if err != nil {
		return fmt.Errorf("wgctrl: %w", err)
	}
	defer wgClient.Close()

	psk, err := wgtypes.ParseKey(newPSK)
	if err != nil {
		return fmt.Errorf("invalid PSK: %w", err)
	}
	brokerKey, err := wgtypes.ParseKey(t.config.BrokerPubKey)
	if err != nil {
		return fmt.Errorf("invalid broker key: %w", err)
	}

	return wgClient.ConfigureDevice(t.config.InterfaceName, wgtypes.Config{
		Peers: []wgtypes.PeerConfig{{
			PublicKey:    brokerKey,
			UpdateOnly:   true,
			PresharedKey: &psk,
		}},
	})
}

func (t *Tunnel) interfaceExistsOS() bool {
	return wgAdapter != 0
}

func (t *Tunnel) assignAddressOS() error {
	ip := t.config.OverlayIP
	if !strings.Contains(ip, "/") {
		ip += "/32"
	}
	addr, ipNet, err := net.ParseCIDR(ip)
	if err != nil {
		return fmt.Errorf("invalid overlay IP: %w", err)
	}
	mask := net.IP(ipNet.Mask).String()

	idx := t.getInterfaceIndex()
	target := t.config.InterfaceName
	if idx != "" {
		target = idx
	}

	_ = exec.Command("netsh", "interface", "ip", "delete", "address",
		fmt.Sprintf("name=%s", target), fmt.Sprintf("addr=%s", addr.String())).Run()

	cmd := exec.Command("netsh", "interface", "ip", "set", "address",
		fmt.Sprintf("name=%s", target), "source=static",
		fmt.Sprintf("addr=%s", addr.String()),
		fmt.Sprintf("mask=%s", mask), "gateway=none")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("netsh failed: %s: %w", strings.TrimSpace(string(output)), err)
	}
	return nil
}

func (t *Tunnel) addRoutesOS() error {
	idx := t.getInterfaceIndex()
	if idx == "" {
		return fmt.Errorf("cannot find interface index for %s", t.config.InterfaceName)
	}

	var lastErr error
	for _, cidr := range t.config.AllowedIPs {
		prefix, err := netip.ParsePrefix(cidr)
		if err != nil || !prefix.Addr().Is4() {
			continue
		}

		if prefix.Bits() == 0 {
			for _, halfRoute := range []struct{ network, mask string }{
				{"0.0.0.0", "128.0.0.0"},
				{"128.0.0.0", "128.0.0.0"},
			} {
				cmd := exec.Command("route", "add", halfRoute.network, "mask", halfRoute.mask, "0.0.0.0", "if", idx)
				if output, err := cmd.CombinedOutput(); err != nil {
					outStr := strings.TrimSpace(string(output))
					if !strings.Contains(outStr, "already exists") && !strings.Contains(outStr, "ya existe") {
						lastErr = fmt.Errorf("route add %s/%s: %s", halfRoute.network, halfRoute.mask, outStr)
					}
				}
			}
			continue
		}

		network := prefix.Masked().Addr().String()
		mask := prefixLenToMask(prefix.Bits())
		cmd := exec.Command("route", "add", network, "mask", mask, "0.0.0.0", "if", idx)
		if output, err := cmd.CombinedOutput(); err != nil {
			outStr := strings.TrimSpace(string(output))
			if !strings.Contains(outStr, "already exists") && !strings.Contains(outStr, "ya existe") {
				lastErr = fmt.Errorf("route add %s: %s", cidr, outStr)
			}
		}
	}
	return lastErr
}

func (t *Tunnel) getInterfaceIndex() string {
	iface, err := net.InterfaceByName(t.config.InterfaceName)
	if err != nil {
		time.Sleep(500 * time.Millisecond)
		iface, err = net.InterfaceByName(t.config.InterfaceName)
		if err != nil {
			return ""
		}
	}
	return fmt.Sprintf("%d", iface.Index)
}

func (t *Tunnel) setMTU(mtu int) {
	idx := t.getInterfaceIndex()
	target := t.config.InterfaceName
	if idx != "" {
		target = idx
	}
	cmd := exec.Command("netsh", "interface", "ipv4", "set", "subinterface",
		target, fmt.Sprintf("mtu=%d", mtu), "store=active")
	if output, err := cmd.CombinedOutput(); err != nil {
		_ = output
		_ = err
	}
}

func downOS(ifaceName string) error {
	if wgAdapter != 0 {
		wgAdapter.Close()
		wgAdapter = 0
	}
	return nil
}

func downWithEndpointOS(ifaceName, brokerEndpoint string) error {
	brokerHost := brokerEndpoint
	if host, _, err := net.SplitHostPort(brokerHost); err == nil {
		brokerHost = host
	}
	if net.ParseIP(brokerHost) == nil {
		ips, _ := net.LookupHost(brokerHost)
		if len(ips) > 0 {
			brokerHost = ips[0]
		}
	}

	downErr := downOS(ifaceName)
	if brokerHost != "" {
		_ = exec.Command("route", "delete", brokerHost).Run()
	}
	return downErr
}

// GetStatusWindows reads tunnel stats from the WireGuard NT kernel driver via wgctrl.
func GetStatusWindows() (rx, tx int64, handshake time.Time, endpoint string) {
	if wgAdapter == 0 {
		return 0, 0, time.Time{}, ""
	}
	wgClient, err := wgctrl.New()
	if err != nil {
		return 0, 0, time.Time{}, ""
	}
	defer wgClient.Close()

	dev, err := wgClient.Device("wg-wireztna")
	if err != nil {
		return 0, 0, time.Time{}, ""
	}
	if len(dev.Peers) == 0 {
		return 0, 0, time.Time{}, ""
	}

	peer := dev.Peers[0]
	if peer.Endpoint != nil {
		endpoint = peer.Endpoint.String()
	}
	return peer.ReceiveBytes, peer.TransmitBytes, peer.LastHandshakeTime, endpoint
}

// getStatusInProcess returns status from the WireGuard NT adapter.
func getStatusInProcess(ifaceName string) (*StatusInfo, error) {
	rx, tx, handshake, endpoint := GetStatusWindows()
	if rx == 0 && tx == 0 && handshake.IsZero() && endpoint == "" {
		return nil, fmt.Errorf("no active WireGuard device")
	}
	info := &StatusInfo{
		RxBytes:       rx,
		TxBytes:       tx,
		LastHandshake: handshake,
		Endpoint:      endpoint,
	}
	iface, err := net.InterfaceByName(ifaceName)
	if err == nil {
		addrs, _ := iface.Addrs()
		for _, addr := range addrs {
			info.OverlayIP = addr.String()
			break
		}
	}
	return info, nil
}

// UpdatePSKWindows updates the PSK via wgctrl.
func UpdatePSKWindows(brokerPubKey, newPSK string) error {
	if wgAdapter == 0 {
		return fmt.Errorf("no active WireGuard adapter")
	}
	wgClient, err := wgctrl.New()
	if err != nil {
		return err
	}
	defer wgClient.Close()

	psk, err := wgtypes.ParseKey(newPSK)
	if err != nil {
		return err
	}
	brokerKey, err := wgtypes.ParseKey(brokerPubKey)
	if err != nil {
		return err
	}

	return wgClient.ConfigureDevice("wg-wireztna", wgtypes.Config{
		Peers: []wgtypes.PeerConfig{{
			PublicKey:    brokerKey,
			UpdateOnly:   true,
			PresharedKey: &psk,
		}},
	})
}

// --- Helpers ---

func (t *Tunnel) addBrokerExclusionRoute() {
	brokerHost := t.config.BrokerEndpoint
	if host, _, err := net.SplitHostPort(brokerHost); err == nil {
		brokerHost = host
	}
	if net.ParseIP(brokerHost) == nil {
		ips, err := net.LookupHost(brokerHost)
		if err != nil || len(ips) == 0 {
			return
		}
		brokerHost = ips[0]
	}
	gateway := getDefaultGateway()
	if gateway == "" {
		return
	}
	cmd := exec.Command("route", "add", brokerHost, "mask", "255.255.255.255", gateway)
	cmd.CombinedOutput()
}

func (t *Tunnel) removeBrokerExclusionRoute() {
	brokerHost := t.config.BrokerEndpoint
	if host, _, err := net.SplitHostPort(brokerHost); err == nil {
		brokerHost = host
	}
	if net.ParseIP(brokerHost) == nil {
		ips, _ := net.LookupHost(brokerHost)
		if len(ips) > 0 {
			brokerHost = ips[0]
		}
	}
	exec.Command("route", "delete", brokerHost).Run()
}

func getDefaultGateway() string {
	output, err := exec.Command("route", "print", "0.0.0.0").CombinedOutput()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "0.0.0.0") {
			fields := strings.Fields(line)
			if len(fields) >= 3 {
				gw := fields[2]
				if net.ParseIP(gw) != nil && gw != "0.0.0.0" {
					return gw
				}
			}
		}
	}
	return ""
}

func resolveEndpoint(endpoint string) (string, error) {
	host, port, err := net.SplitHostPort(endpoint)
	if err != nil {
		return endpoint, nil
	}
	if net.ParseIP(host) != nil {
		return endpoint, nil
	}
	ips, err := net.LookupHost(host)
	if err != nil {
		return "", fmt.Errorf("DNS failed for %s: %w", host, err)
	}
	for _, ip := range ips {
		if net.ParseIP(ip).To4() != nil {
			return net.JoinHostPort(ip, port), nil
		}
	}
	if len(ips) > 0 {
		return net.JoinHostPort(ips[0], port), nil
	}
	return "", fmt.Errorf("no IPs for %s", host)
}

func prefixLenToMask(bits int) string {
	mask := net.CIDRMask(bits, 32)
	return net.IP(mask).String()
}

func getLinuxDefaultGateway() string {
	return ""
}
