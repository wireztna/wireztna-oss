//go:build darwin

package tunnel

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"golang.zx2c4.com/wireguard/wgctrl"
)

type darwinCommandPlan struct {
	Name string
	Args []string
}

func (p darwinCommandPlan) command() *exec.Cmd {
	return exec.Command(p.Name, p.Args...)
}

func planDarwinWGQuick(action, interfaceName string) (darwinCommandPlan, error) {
	path, err := darwinWGQuickConfigPath(interfaceName)
	if err != nil {
		return darwinCommandPlan{}, err
	}
	return darwinCommandPlan{Name: "/Library/Application Support/WireZTNA/runtime/wg-quick", Args: []string{action, path}}, nil
}

// DarwinAppliedIdentity reads only the non-secret device identity from the
// root-owned wg-quick configuration. It lets uninstall and upgrade checks find
// a residual utun even when the owner-controlled client config has changed.
func DarwinAppliedIdentity() (Config, bool, error) {
	path, err := darwinWGQuickConfigPath(darwinInterfaceName)
	if err != nil {
		return Config{}, false, err
	}
	if err := validateExistingDarwinWireGuardDirectory(); err != nil {
		if os.IsNotExist(err) {
			return Config{}, false, nil
		}
		return Config{}, false, fmt.Errorf("validate applied WireGuard config parent: %w", err)
	}
	before, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return Config{}, false, nil
	}
	if err != nil {
		return Config{}, false, fmt.Errorf("inspect applied WireGuard config: %w", err)
	}
	if err := validateDarwinRootObject(path, before, false); err != nil {
		return Config{}, false, err
	}
	if before.Mode().Perm() != 0o600 {
		return Config{}, false, errors.New("applied WireGuard config must have mode 0600")
	}
	file, err := os.Open(path)
	if err != nil {
		return Config{}, false, fmt.Errorf("open applied WireGuard config: %w", err)
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(before, opened) {
		return Config{}, false, errors.Join(errors.New("applied WireGuard config changed while opening"), err)
	}

	var overlayIP, brokerPublicKey string
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
		key, value := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
		switch {
		case section == "Interface" && key == "Address":
			if overlayIP != "" {
				return Config{}, false, errors.New("applied WireGuard config contains multiple interface addresses")
			}
			overlayIP = strings.TrimSpace(strings.Split(value, ",")[0])
		case section == "Peer" && key == "PublicKey":
			if brokerPublicKey != "" {
				return Config{}, false, errors.New("applied WireGuard config contains multiple peer identities")
			}
			brokerPublicKey = value
		}
	}
	if err := scanner.Err(); err != nil {
		return Config{}, false, fmt.Errorf("read applied WireGuard identity: %w", err)
	}
	if overlayIP == "" || brokerPublicKey == "" {
		return Config{}, false, errors.New("applied WireGuard identity is incomplete")
	}
	return Config{InterfaceName: darwinInterfaceName, OverlayIP: overlayIP, BrokerPubKey: brokerPublicKey}, true, nil
}

func planDarwinBrokerRouteGet(brokerIP string) darwinCommandPlan {
	return darwinCommandPlan{Name: "route", Args: []string{"-n", "get", brokerIP}}
}

func planDarwinBrokerRouteAdd(route DarwinBrokerRoute) darwinCommandPlan {
	// The wireguard-go UDP socket is not bound to the physical interface. A
	// scoped route is invisible to its unscoped lookup and the /1 pair captures
	// the broker endpoint, so the owned host route must be global.
	return darwinCommandPlan{Name: "route", Args: []string{"-n", "add", "-host", strings.TrimSuffix(route.Destination, "/32"), route.Gateway}}
}

func planDarwinBrokerRouteDelete(route DarwinBrokerRoute) darwinCommandPlan {
	// Markers written before global route ownership used a scoped route. Keep
	// that deletion form for upgrade cleanup; current markers delete globally.
	args := []string{"-n", "delete", "-host", strings.TrimSuffix(route.Destination, "/32"), route.Gateway}
	if !route.Global {
		args = append(args, "-ifscope", route.Interface)
	}
	return darwinCommandPlan{Name: "route", Args: args}
}

func planDarwinAddress(interfaceName, overlayIP string) []darwinCommandPlan {
	ipOnly := strings.SplitN(overlayIP, "/", 2)[0]
	return []darwinCommandPlan{
		{Name: "ifconfig", Args: []string{interfaceName, "inet", ipOnly, ipOnly, "alias"}},
		{Name: "ifconfig", Args: []string{interfaceName, "up"}},
	}
}

func planDarwinAllowedRoutes(interfaceName string, allowedIPs []string) []darwinCommandPlan {
	plans := make([]darwinCommandPlan, 0, len(allowedIPs))
	for _, cidr := range allowedIPs {
		plans = append(plans, darwinCommandPlan{
			Name: "route",
			Args: []string{"-n", "add", "-net", cidr, "-interface", interfaceName},
		})
	}
	return plans
}

type darwinLookupIP func(string) ([]net.IP, error)

func resolveDarwinBrokerEndpoint(endpoint string, lookup darwinLookupIP) (string, string, error) {
	if endpoint == "" || strings.TrimSpace(endpoint) != endpoint || strings.IndexFunc(endpoint, func(r rune) bool { return r <= 0x20 || r == 0x7f }) >= 0 {
		return "", "", fmt.Errorf("invalid broker endpoint %q", endpoint)
	}
	host, port, err := net.SplitHostPort(endpoint)
	if err != nil || host == "" || port == "" {
		return "", "", fmt.Errorf("invalid broker endpoint %q", endpoint)
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 || strconv.Itoa(portNumber) != port {
		return "", "", fmt.Errorf("invalid broker endpoint port %q", port)
	}
	if ip := net.ParseIP(host); ip != nil {
		ipv4 := ip.To4()
		if ipv4 == nil {
			return "", "", fmt.Errorf("broker endpoint %q is not IPv4", endpoint)
		}
		value := ipv4.String()
		return value, net.JoinHostPort(value, port), nil
	}
	ips, err := lookup(host)
	if err != nil {
		return "", "", fmt.Errorf("resolve broker endpoint %q: %w", host, err)
	}
	unique := make(map[string]struct{})
	for _, ip := range ips {
		if ipv4 := ip.To4(); ipv4 != nil {
			unique[ipv4.String()] = struct{}{}
		}
	}
	if len(unique) != 1 {
		return "", "", fmt.Errorf("broker endpoint %q must resolve to exactly one IPv4 address (got %d)", host, len(unique))
	}
	var brokerIP string
	for value := range unique {
		brokerIP = value
	}
	return brokerIP, net.JoinHostPort(brokerIP, port), nil
}

// createInterfaceOS creates the WireGuard interface on macOS.
func (t *Tunnel) createInterfaceOS() error {
	return t.createInterfaceMacOS()
}

// createInterfaceMacOS writes the fixed wg-quick configuration and brings it
// up only after a full-tunnel broker route has been installed and verified.
func (t *Tunnel) createInterfaceMacOS() error {
	confPath, err := darwinWGQuickConfigPath(t.config.InterfaceName)
	if err != nil {
		return err
	}
	if err := ensureDarwinWireGuardDirectory(); err != nil {
		return fmt.Errorf("prepare macOS WireGuard directory: %w", err)
	}
	if info, statErr := os.Lstat(confPath); statErr == nil {
		if err := validateDarwinRootObject(confPath, info, false); err != nil {
			return err
		}
		if err := downDarwin(t.config.InterfaceName); err != nil {
			return fmt.Errorf("remove existing macOS tunnel: %w", err)
		}
	} else if !os.IsNotExist(statErr) {
		return fmt.Errorf("inspect existing macOS tunnel config: %w", statErr)
	}
	if err := recoverDarwinBrokerRoute(t.config.InterfaceName); err != nil {
		return fmt.Errorf("recover previous broker exclusion ownership: %w", err)
	}
	time.Sleep(500 * time.Millisecond)

	effective := t.config
	if effective.IsExitNode {
		brokerIP, pinnedEndpoint, err := resolveDarwinBrokerEndpoint(effective.BrokerEndpoint, net.LookupIP)
		if err != nil {
			return fmt.Errorf("exit node: %w", err)
		}
		effective.BrokerEndpoint = pinnedEndpoint
		routeOutput, err := planDarwinBrokerRouteGet(brokerIP).command().CombinedOutput()
		if err != nil {
			return fmt.Errorf("exit node: inspect physical route to broker: %s: %w", strings.TrimSpace(string(routeOutput)), err)
		}
		var physicalRoute DarwinBrokerRoute
		routeValues := parseDarwinRouteValues(string(routeOutput))
		if destination := routeValues["destination"]; destination == brokerIP || destination == brokerIP+"/32" {
			if !isDarwinKernelClonedHostRoute(string(routeOutput), brokerIP) {
				return fmt.Errorf("exit node: refusing to adopt an existing static host route for broker %s", brokerIP)
			}
			physicalRoute, err = parseDarwinPhysicalRouteGet(string(routeOutput), brokerIP)
			if err != nil {
				return fmt.Errorf("exit node: kernel-cloned broker route is unsafe: %w", err)
			}
			// WASCLONED is a lookup result, not a deletable route-table entry on
			// macOS. Installing the exact scoped static route atomically replaces
			// the cache result; trying to delete the clone returns "not in table".
		} else {
			physicalRoute, err = parseDarwinPhysicalRouteGet(string(routeOutput), brokerIP)
			if err != nil {
				return fmt.Errorf("exit node: physical broker route is unsafe: %w", err)
			}
		}
		// Persist intent before the first routing side effect. The route is global
		// so wireguard-go's unbound UDP socket can reach the endpoint even after
		// the full-tunnel /1 routes exist. Legacy markers omit Global and retain
		// their scoped cleanup behavior.
		physicalRoute.Global = true
		if err := writeDarwinBrokerRouteMarker(t.config.InterfaceName, physicalRoute); err != nil {
			return fmt.Errorf("exit node: persist broker exclusion ownership: %w", err)
		}
		addOutput, err := planDarwinBrokerRouteAdd(physicalRoute).command().CombinedOutput()
		if err != nil {
			addErr := fmt.Errorf("exit node: add broker exclusion route: %s: %w", strings.TrimSpace(string(addOutput)), err)
			return errors.Join(addErr, cleanupDarwinBrokerRoute(t.config.InterfaceName))
		}
		verifiedOutput, err := planDarwinBrokerRouteGet(brokerIP).command().CombinedOutput()
		if err != nil {
			verifyErr := fmt.Errorf("exit node: verify broker exclusion route: %s: %w", strings.TrimSpace(string(verifiedOutput)), err)
			return errors.Join(verifyErr, cleanupDarwinBrokerRoute(t.config.InterfaceName))
		}
		verified, err := parseDarwinRouteGet(string(verifiedOutput), brokerIP)
		verified.Global = physicalRoute.Global
		if err != nil || verified != physicalRoute {
			verifyErr := errors.New("exit node: broker exclusion route verification failed")
			return errors.Join(verifyErr, cleanupDarwinBrokerRoute(t.config.InterfaceName))
		}
		if err := writeDarwinBrokerRouteMarker(t.config.InterfaceName, verified); err != nil {
			markerErr := fmt.Errorf("exit node: finalize verified broker exclusion ownership: %w", err)
			return errors.Join(markerErr, cleanupDarwinBrokerRoute(t.config.InterfaceName))
		}
	}

	if err := atomicWriteDarwinRootFile(confPath, []byte(renderMacOSConfig(effective))); err != nil {
		writeErr := fmt.Errorf("write macOS WireGuard config: %w", err)
		if effective.IsExitNode {
			return errors.Join(writeErr, cleanupDarwinBrokerRoute(t.config.InterfaceName))
		}
		return writeErr
	}
	t.confPath = confPath
	upPlan, err := planDarwinWGQuick("up", t.config.InterfaceName)
	if err != nil {
		return err
	}
	output, err := upPlan.command().CombinedOutput()
	if err != nil {
		return fmt.Errorf("wg-quick up failed: %s: %w", strings.TrimSpace(string(output)), err)
	}
	if effective.IsExitNode {
		device, err := findDarwinConfigDevice(t.client, t.config.InterfaceName)
		if err != nil {
			return fmt.Errorf("find macOS WireGuard device for full-tunnel routes: %w", err)
		}
		for index, routePlan := range planDarwinAllowedRoutes(device.Name, effective.AllowedIPs) {
			routeOutput, routeErr := routePlan.command().CombinedOutput()
			if routeErr != nil {
				return fmt.Errorf("add macOS full-tunnel route %s: %s: %w", effective.AllowedIPs[index], strings.TrimSpace(string(routeOutput)), routeErr)
			}
		}
		marker, err := LoadDarwinBrokerRoute(t.config.InterfaceName)
		if err != nil {
			return fmt.Errorf("verify macOS broker route ownership after full-tunnel routes: %w", err)
		}
		if marker == nil {
			return errors.New("macOS broker route ownership disappeared after full-tunnel routes")
		}
		brokerIP := strings.TrimSuffix(marker.Destination, "/32")
		brokerOutput, err := planDarwinBrokerRouteGet(brokerIP).command().CombinedOutput()
		if err != nil {
			return fmt.Errorf("verify macOS broker route after full-tunnel routes: %s: %w", strings.TrimSpace(string(brokerOutput)), err)
		}
		observed, err := parseDarwinRouteGet(string(brokerOutput), brokerIP)
		observed.Global = marker.Global
		if err != nil || observed != *marker {
			return errors.New("macOS broker endpoint was captured by full-tunnel routes")
		}
	}
	return nil
}

func renderMacOSConfig(cfg Config) string {
	overlayIP := cfg.OverlayIP
	if !strings.Contains(overlayIP, "/") {
		overlayIP += "/32"
	}

	var pskLine string
	if cfg.PresharedKey != "" {
		pskLine = fmt.Sprintf("PresharedKey = %s\n", cfg.PresharedKey)
	}

	var tableLine string
	allowedIPs := cfg.AllowedIPs
	if cfg.IsExitNode {
		// wireguard-go on Darwin must receive a real default AllowedIP to select
		// the peer reliably. Keep route ownership separate: Table=off prevents
		// wg-quick from owning default/endpoint routes, while createInterfaceMacOS
		// installs the policy-neutral /1 pair and broker /32 explicitly.
		tableLine = "Table = off\n"
		allowedIPs = []string{"0.0.0.0/0"}
	}

	// Deliberately omit wg-quick's DNS directive. On macOS it mutates global
	// SystemConfiguration state, which is outside the owner-scoped resolver
	// rollback model and can leave the host without DNS after a failed apply.
	return fmt.Sprintf(`[Interface]
PrivateKey = %s
Address = %s
%s
[Peer]
PublicKey = %s
%sEndpoint = %s
AllowedIPs = %s
PersistentKeepalive = 25
`, cfg.PrivateKey, overlayIP, tableLine, cfg.BrokerPubKey, pskLine,
		cfg.BrokerEndpoint, strings.Join(allowedIPs, ", "))
}

// destroyInterfaceOS keeps the broker exclusion route whenever wg-quick down
// cannot prove that the full-tunnel interface was removed. Removing that route
// while /1 routes may remain would route the broker back into its own tunnel.
func (t *Tunnel) destroyInterfaceOS() {
	if err := downDarwin(t.config.InterfaceName); err != nil {
		t.log("warn", fmt.Sprintf("macOS tunnel teardown failed; retaining broker exclusion ownership: %v", err))
		return
	}
	if err := cleanupDarwinBrokerRoute(t.config.InterfaceName); err != nil {
		t.log("warn", fmt.Sprintf("Broker exclusion route cleanup: %v", err))
	}
}

func (t *Tunnel) assignAddressOS() error {
	plans := planDarwinAddress(t.config.InterfaceName, t.config.OverlayIP)
	if output, err := plans[0].command().CombinedOutput(); err != nil {
		return fmt.Errorf("ifconfig failed: %s: %w", string(output), err)
	}
	if output, err := plans[1].command().CombinedOutput(); err != nil {
		return fmt.Errorf("ifconfig up failed: %s: %w", string(output), err)
	}
	return nil
}

func getLinuxDefaultGateway() string { return "" }

func (t *Tunnel) addRoutesOS() error {
	var lastErr error
	for i, plan := range planDarwinAllowedRoutes(t.config.InterfaceName, t.config.AllowedIPs) {
		if output, err := plan.command().CombinedOutput(); err != nil {
			lastErr = fmt.Errorf("route add %s: %s: %w", t.config.AllowedIPs[i], string(output), err)
		}
	}
	return lastErr
}

func (t *Tunnel) interfaceExistsOS() bool {
	if t.client == nil {
		return false
	}
	_, err := findDarwinConfigDevice(t.client, t.config.InterfaceName)
	return err == nil
}

type darwinWireGuardDeviceNames func() ([]string, error)

func listDarwinWireGuardDeviceNames() ([]string, error) {
	client, err := wgctrl.New()
	if err != nil {
		return nil, err
	}
	defer client.Close()
	devices, err := client.Devices()
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(devices))
	for _, device := range devices {
		names = append(names, device.Name)
	}
	return names, nil
}

// recoverDarwinBrokerRoute releases stale broker-route ownership only after
// both WireGuard and the IPv4 routing table prove that no full tunnel remains.
func recoverDarwinBrokerRoute(ifaceName string) error {
	marker, err := LoadDarwinBrokerRoute(ifaceName)
	if err != nil || marker == nil {
		return err
	}
	return recoverDarwinBrokerRouteWith(*marker, listDarwinWireGuardDeviceNames, runDarwinCommand, func() error {
		return removeDarwinBrokerRouteMarker(ifaceName)
	})
}

func recoverDarwinBrokerRouteWith(
	marker DarwinBrokerRoute,
	listDevices darwinWireGuardDeviceNames,
	run darwinCommandOutput,
	removeMarker func() error,
) error {
	devices, err := listDevices()
	if err != nil {
		return fmt.Errorf("enumerate WireGuard devices before broker route recovery: %w", err)
	}
	for _, device := range devices {
		if strings.HasPrefix(device, "utun") {
			return fmt.Errorf("WireGuard device %q remains; retaining broker exclusion ownership", device)
		}
	}
	routes, err := run("netstat", "-rn", "-f", "inet")
	if err != nil {
		return fmt.Errorf("inspect IPv4 routes before broker route recovery: %s: %w", strings.TrimSpace(string(routes)), err)
	}
	if err := verifyDarwinFullTunnelRoutesAbsent(string(routes)); err != nil {
		return err
	}
	return cleanupDarwinBrokerRouteWith(marker, run, removeMarker)
}

func verifyDarwinFullTunnelRoutesAbsent(output string) error {
	scanner := bufio.NewScanner(strings.NewReader(output))
	inIPv4Table := false
	headerFound := false
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "Internet:" {
			inIPv4Table = true
			continue
		}
		if !inIPv4Table || line == "" {
			continue
		}
		if strings.HasPrefix(line, "Internet6:") {
			break
		}
		fields := strings.Fields(line)
		if !headerFound {
			if len(fields) < 4 || fields[0] != "Destination" {
				return errors.New("IPv4 routing table is ambiguous; retaining broker exclusion ownership")
			}
			headerFound = true
			continue
		}
		if len(fields) < 4 {
			return errors.New("IPv4 routing table contains an ambiguous row; retaining broker exclusion ownership")
		}
		switch fields[0] {
		case "0/1", "0.0.0.0/1":
			return errors.New("full-tunnel route 0.0.0.0/1 remains; retaining broker exclusion ownership")
		case "128/1", "128.0/1", "128.0.0/1", "128.0.0.0/1":
			return errors.New("full-tunnel route 128.0.0.0/1 remains; retaining broker exclusion ownership")
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("parse IPv4 routing table: %w", err)
	}
	if !headerFound {
		return errors.New("IPv4 routing table is missing; retaining broker exclusion ownership")
	}
	return nil
}

func downDarwin(ifaceName string) error {
	path, err := darwinWGQuickConfigPath(ifaceName)
	if err != nil {
		return err
	}
	if err := validateExistingDarwinWireGuardDirectory(); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("validate wg-quick config parent: %w", err)
	}
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		marker, markerErr := LoadDarwinBrokerRoute(ifaceName)
		if markerErr != nil {
			return fmt.Errorf("inspect broker exclusion ownership without wg-quick config: %w", markerErr)
		}
		if marker != nil {
			return recoverDarwinBrokerRouteWith(*marker, listDarwinWireGuardDeviceNames, runDarwinCommand, func() error {
				return removeDarwinBrokerRouteMarker(ifaceName)
			})
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect wg-quick config %q: %w", path, err)
	}
	if err := validateDarwinRootObject(path, info, false); err != nil {
		return err
	}
	if info.Mode().Perm() != 0600 {
		return fmt.Errorf("wg-quick config %q must have mode 0600", path)
	}
	plan, err := planDarwinWGQuick("down", ifaceName)
	if err != nil {
		return err
	}
	output, err := plan.command().CombinedOutput()
	if err == nil {
		return nil
	}
	message := strings.TrimSpace(string(output))
	if strings.Contains(message, "is not a WireGuard interface") {
		return nil
	}
	if message == "" {
		return fmt.Errorf("wg-quick down %s: %w", path, err)
	}
	return fmt.Errorf("wg-quick down %s: %s: %w", path, message, err)
}

func downOS(ifaceName string) error {
	return downDarwin(ifaceName)
}

func removeBrokerExclusionRouteIPOS(string) error {
	return errors.New("Darwin broker route cleanup requires the durable exact-route marker")
}
