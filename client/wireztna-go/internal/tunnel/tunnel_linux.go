//go:build linux

package tunnel

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
)

// createInterfaceOS creates the WireGuard interface on Linux.
func (t *Tunnel) createInterfaceOS() error {
	// Linux: try kernel module first, fall back to wireguard-go
	cmd := exec.Command("ip", "link", "add", t.config.InterfaceName, "type", "wireguard")
	output, err := cmd.CombinedOutput()
	if err != nil {
		outStr := string(output)
		// If it failed due to permissions, don't try wireguard-go (same issue)
		if strings.Contains(outStr, "Operation not permitted") ||
			strings.Contains(outStr, "RTNETLINK answers: Operation not permitted") {
			return fmt.Errorf("permission denied — run with sudo or apply: sudo setcap 'cap_net_admin+ep cap_net_raw+ep' %s",
				os.Args[0])
		}
		// Kernel module not available — use wireguard-go
		cmd = exec.Command("wireguard-go", t.config.InterfaceName)
		output, err = cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("wireguard-go failed: %s: %w", string(output), err)
		}
	}
	return nil
}

// destroyInterfaceOS removes the WireGuard interface on Linux and then
// removes the explicit broker exclusion route created by full-tunnel mode.
func (t *Tunnel) destroyInterfaceOS() {
	// Resolve while the tunnel and its DNS path are still available. The route
	// itself is removed after the interface so teardown stays deterministic.
	brokerIP, resolveErr := brokerExclusionIPForCleanup(t.config.InterfaceName, t.config.BrokerEndpoint)

	exec.Command("ip", "link", "del", t.config.InterfaceName).Run()

	if resolveErr != nil {
		t.log("warn", fmt.Sprintf("Broker exclusion route cleanup: %v", resolveErr))
		return
	}
	if brokerIP == "" {
		return
	}
	if err := removeBrokerExclusionRouteIPOS(brokerIP); err != nil {
		t.log("warn", err.Error())
	} else {
		_ = removeBrokerRouteMarker(t.config.InterfaceName)
		t.log("debug", fmt.Sprintf("Broker exclusion route removed for %s", brokerIP))
	}
}

// assignAddressOS assigns the overlay IP to the interface on Linux.
func (t *Tunnel) assignAddressOS() error {
	ip := t.config.OverlayIP
	if !strings.Contains(ip, "/") {
		ip += "/32"
	}

	cmd := exec.Command("ip", "addr", "add", ip, "dev", t.config.InterfaceName)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ip addr add failed: %s: %w", string(output), err)
	}
	cmd = exec.Command("ip", "link", "set", t.config.InterfaceName, "up")
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ip link set up failed: %s: %w", string(output), err)
	}
	return nil
}

// getLinuxDefaultGateway reads the physical default gateway before tunnel routes modify it.
// Returns empty string if it cannot be determined.
func getLinuxDefaultGateway() string {
	out, err := exec.Command("ip", "route", "show", "default").Output()
	if err != nil || len(out) == 0 {
		return ""
	}
	// May have multiple default routes; pick the first non-WG one.
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, "wg-") || strings.Contains(line, "wireztna") {
			continue // skip any WG-related default route
		}
		parts := strings.Fields(line)
		for i, p := range parts {
			if p == "via" && i+1 < len(parts) {
				return parts[i+1]
			}
		}
	}
	return ""
}

// addRoutesOS adds routes for allowed IPs on Linux.
func (t *Tunnel) addRoutesOS() error {
	var lastErr error

	// For exit node (full tunnel): add exclusion route for broker endpoint
	// so WG traffic to the broker doesn't loop through the tunnel itself.
	if t.config.IsExitNode {
		// Parse broker endpoint IP (without port)
		brokerHost := t.config.BrokerEndpoint
		if idx := strings.LastIndex(brokerHost, ":"); idx > 0 {
			brokerHost = brokerHost[:idx]
		}

		// Resolve hostname to IP if needed
		brokerIP := brokerHost
		if net.ParseIP(brokerHost) == nil {
			ips, err := net.LookupHost(brokerHost)
			if err != nil || len(ips) == 0 {
				t.log("error", fmt.Sprintf("Exit node: cannot resolve broker '%s' — full tunnel routes NOT applied (would cause loop)", brokerHost))
				return fmt.Errorf("exit node: cannot resolve broker endpoint '%s': %v — aborting full-tunnel routes to prevent connectivity loss", brokerHost, err)
			}
			brokerIP = ips[0]
			t.log("debug", fmt.Sprintf("Exit node: broker '%s' resolved to %s", brokerHost, brokerIP))
		}

		// Use the pre-captured gateway (captured before tunnel creation in Up()).
		// Fallback to reading it now (may be unreliable if another tunnel was active).
		gateway := t.preExitNodeGateway
		if gateway == "" {
			t.log("warn", "Exit node: pre-captured gateway was empty, trying to detect now (may be unreliable)")
			out, err := exec.Command("ip", "route", "show", "default").Output()
			if err == nil && len(out) > 0 {
				for _, line := range strings.Split(string(out), "\n") {
					if strings.Contains(line, "wg-") || strings.Contains(line, "wireztna") {
						continue
					}
					parts := strings.Fields(line)
					for i, p := range parts {
						if p == "via" && i+1 < len(parts) {
							gateway = parts[i+1]
							break
						}
					}
					if gateway != "" {
						break
					}
				}
			}
		} else {
			t.log("debug", fmt.Sprintf("Exit node: using pre-captured gateway: %s", gateway))
		}

		if gateway == "" {
			t.log("error", "Exit node: no default gateway detected — full tunnel routes NOT applied (would cause total connectivity loss)")
			return fmt.Errorf("exit node: cannot determine default gateway — aborting full-tunnel routes to prevent connectivity loss. Check 'ip route show default'")
		}

		// Add /32 route to broker via physical gateway (bypasses tunnel).
		t.log("info", fmt.Sprintf("Exit node: adding exclusion route %s/32 via %s", brokerIP, gateway))
		cmd := exec.Command("ip", "route", "replace", brokerIP+"/32", "via", gateway)
		if out, err := cmd.CombinedOutput(); err != nil {
			outStr := strings.TrimSpace(string(out))
			t.log("error", fmt.Sprintf("Exit node: exclusion route FAILED: %s (%v) — full tunnel routes NOT applied", outStr, err))
			return fmt.Errorf("exit node: exclusion route for %s via %s failed: %s — aborting full-tunnel routes to prevent connectivity loss", brokerIP, gateway, outStr)
		}
		if err := writeBrokerRouteMarker(t.config.InterfaceName, brokerIP); err != nil {
			t.log("warn", fmt.Sprintf("Exit node: could not persist exclusion route ownership: %v", err))
		}
		t.log("info", fmt.Sprintf("Exit node: exclusion route added successfully (%s via %s)", brokerIP, gateway))
	}

	for _, cidr := range t.config.AllowedIPs {
		cmd := exec.Command("ip", "route", "add", cidr, "dev", t.config.InterfaceName)
		if output, err := cmd.CombinedOutput(); err != nil {
			lastErr = fmt.Errorf("ip route add %s: %s: %w", cidr, string(output), err)
		}
	}
	return lastErr
}

// interfaceExistsOS returns false because wgctrl performs the Linux check.
func (t *Tunnel) interfaceExistsOS() bool {
	return false
}

// downOS tears down a named interface on Linux (standalone, no Tunnel instance).
func downOS(ifaceName string) error {
	if _, err := net.InterfaceByName(ifaceName); err != nil {
		return nil
	}
	return exec.Command("ip", "link", "del", ifaceName).Run()
}

func removeBrokerExclusionRouteIPOS(brokerIP string) error {
	output, err := exec.Command("ip", "route", "del", brokerIP+"/32").CombinedOutput()
	if err == nil {
		return nil
	}
	message := strings.TrimSpace(string(output))
	if strings.Contains(message, "not in table") || strings.Contains(message, "No such process") {
		return nil
	}
	return fmt.Errorf("remove broker exclusion route %s: %s: %w", brokerIP, message, err)
}
