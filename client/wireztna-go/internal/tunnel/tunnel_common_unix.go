//go:build darwin || linux

package tunnel

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
)

const (
	darwinWireGuardDirectory = "/Library/Application Support/WireZTNA/wireguard"
	darwinBrokerMarkerName   = "wg-wireztna.broker-route.json"
)

// DarwinBrokerRoute is the durable identity of the physical route that keeps
// the broker endpoint outside a full tunnel.
type DarwinBrokerRoute struct {
	Destination string `json:"destination"`
	Gateway     string `json:"gateway"`
	Interface   string `json:"interface"`
	// Global distinguishes current unscoped host routes from legacy scoped
	// routes. Missing in old markers means false, preserving upgrade cleanup.
	Global bool `json:"global"`
}

// downWithEndpointOS tears down the interface and then removes only the route
// whose complete durable identity is still owned by WireZTNA.
func downWithEndpointOS(ifaceName, brokerEndpoint string) error {
	downErr := downOS(ifaceName)
	if runtime.GOOS == "darwin" {
		// If teardown is ambiguous, preserve the exact broker route and marker.
		// The still-active /1 routes would otherwise capture the broker endpoint.
		if downErr != nil {
			return downErr
		}
		if routeErr := cleanupDarwinBrokerRoute(ifaceName); routeErr != nil {
			return &BrokerRouteCleanupError{Err: routeErr}
		}
		return nil
	}

	brokerIP, resolveErr := brokerExclusionIPForCleanup(ifaceName, brokerEndpoint)
	var routeErr error
	if resolveErr == nil && brokerIP != "" {
		routeErr = removeBrokerExclusionRouteIPOS(brokerIP)
		if routeErr == nil {
			routeErr = removeBrokerRouteMarker(ifaceName)
		}
	}
	cleanupErr := errors.Join(resolveErr, routeErr)
	if downErr != nil {
		return errors.Join(downErr, cleanupErr)
	}
	if cleanupErr != nil {
		return &BrokerRouteCleanupError{Err: cleanupErr}
	}
	return nil
}

func brokerRouteMarkerPath(ifaceName string) string {
	return filepath.Join("/var/run", "wireztna-"+filepath.Base(ifaceName)+".broker-route")
}

func writeBrokerRouteMarker(ifaceName, brokerIP string) error {
	return os.WriteFile(brokerRouteMarkerPath(ifaceName), []byte(brokerIP+"\n"), 0600)
}

func removeBrokerRouteMarker(ifaceName string) error {
	err := os.Remove(brokerRouteMarkerPath(ifaceName))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func brokerExclusionIPForCleanup(ifaceName, endpoint string) (string, error) {
	data, err := os.ReadFile(brokerRouteMarkerPath(ifaceName))
	if err == nil {
		brokerIP := strings.TrimSpace(string(data))
		if ip := net.ParseIP(brokerIP); ip != nil && ip.To4() != nil {
			return ip.To4().String(), nil
		}
		return "", fmt.Errorf("invalid broker route marker for %s", ifaceName)
	}
	if !os.IsNotExist(err) {
		return "", fmt.Errorf("read broker route marker for %s: %w", ifaceName, err)
	}

	// Linux compatibility fallback for routes created before ownership markers.
	return resolveBrokerIPv4(endpoint)
}

func resolveBrokerIPv4(endpoint string) (string, error) {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return "", nil
	}

	host := endpoint
	if splitHost, _, err := net.SplitHostPort(endpoint); err == nil {
		host = splitHost
	} else if strings.Count(endpoint, ":") == 1 {
		host = strings.SplitN(endpoint, ":", 2)[0]
	}
	host = strings.Trim(host, "[]")

	if ip := net.ParseIP(host); ip != nil {
		if ipv4 := ip.To4(); ipv4 != nil {
			return ipv4.String(), nil
		}
		return "", fmt.Errorf("broker endpoint %q does not resolve to IPv4", endpoint)
	}

	ips, err := net.LookupIP(host)
	if err != nil {
		return "", fmt.Errorf("cannot resolve broker endpoint %q: %w", host, err)
	}
	for _, ip := range ips {
		if ipv4 := ip.To4(); ipv4 != nil {
			return ipv4.String(), nil
		}
	}
	return "", fmt.Errorf("broker endpoint %q has no IPv4 address", host)
}

func darwinBrokerMarkerPath(ifaceName string) (string, error) {
	if ifaceName != darwinInterfaceName {
		return "", fmt.Errorf("Darwin WireGuard interface must be %q", darwinInterfaceName)
	}
	return filepath.Join(darwinWireGuardDirectory, darwinBrokerMarkerName), nil
}

func ensureDarwinWireGuardDirectory() error {
	paths := []struct {
		path   string
		create bool
	}{
		{path: "/Library"},
		{path: "/Library/Application Support"},
		{path: "/Library/Application Support/WireZTNA", create: true},
		{path: darwinWireGuardDirectory, create: true},
	}
	for _, item := range paths {
		info, err := os.Lstat(item.path)
		if os.IsNotExist(err) && item.create {
			if err := os.Mkdir(item.path, 0700); err != nil {
				return fmt.Errorf("create secure directory %q: %w", item.path, err)
			}
			info, err = os.Lstat(item.path)
		}
		if err != nil {
			return fmt.Errorf("inspect secure directory %q: %w", item.path, err)
		}
		if err := validateDarwinRootObject(item.path, info, true); err != nil {
			return err
		}
	}
	return nil
}

func validateExistingDarwinWireGuardDirectory() error {
	for _, path := range []string{"/Library", "/Library/Application Support", "/Library/Application Support/WireZTNA", darwinWireGuardDirectory} {
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if err := validateDarwinRootObject(path, info, true); err != nil {
			return err
		}
	}
	return nil
}

func validateDarwinRootObject(path string, info os.FileInfo, directory bool) error {
	if info.Mode()&os.ModeSymlink != 0 || (directory && !info.IsDir()) || (!directory && !info.Mode().IsRegular()) {
		return fmt.Errorf("secure path %q is not the expected real object", path)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != 0 {
		return fmt.Errorf("secure path %q is not root-owned", path)
	}
	if info.Mode().Perm()&0022 != 0 {
		return fmt.Errorf("secure path %q is writable by group or others", path)
	}
	return nil
}

func atomicWriteDarwinRootFile(path string, data []byte) (retErr error) {
	if err := ensureDarwinWireGuardDirectory(); err != nil {
		return err
	}
	if info, err := os.Lstat(path); err == nil {
		if err := validateDarwinRootObject(path, info, false); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect secure file %q: %w", path, err)
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), ".wireztna-*")
	if err != nil {
		return fmt.Errorf("create temporary secure file: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() {
		if tmp != nil {
			_ = tmp.Close()
		}
		if retErr != nil {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(0600); err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	tmp = nil
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("atomically replace %q: %w", path, err)
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return err
	}
	return nil
}

func writeDarwinBrokerRouteMarker(ifaceName string, route DarwinBrokerRoute) error {
	path, err := darwinBrokerMarkerPath(ifaceName)
	if err != nil {
		return err
	}
	if _, err := validateDarwinBrokerRoute(route); err != nil {
		return err
	}
	data, err := json.Marshal(route)
	if err != nil {
		return err
	}
	return atomicWriteDarwinRootFile(path, append(data, '\n'))
}

// LoadDarwinBrokerRoute loads and strictly validates the durable route marker.
// A missing marker returns (nil, nil).
func LoadDarwinBrokerRoute(ifaceName string) (*DarwinBrokerRoute, error) {
	path, err := darwinBrokerMarkerPath(ifaceName)
	if err != nil {
		return nil, err
	}
	if err := validateExistingDarwinWireGuardDirectory(); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("inspect Darwin broker route marker: %w", err)
	}
	if err := validateDarwinRootObject(path, info, false); err != nil {
		return nil, err
	}
	if info.Mode().Perm() != 0600 {
		return nil, fmt.Errorf("Darwin broker route marker %q must have mode 0600", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read Darwin broker route marker: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var route DarwinBrokerRoute
	if err := decoder.Decode(&route); err != nil {
		return nil, fmt.Errorf("decode Darwin broker route marker: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, errors.New("Darwin broker route marker contains trailing data")
	}
	validated, err := validateDarwinBrokerRoute(route)
	if err != nil {
		return nil, err
	}
	return &validated, nil
}

func validateDarwinBrokerRoute(route DarwinBrokerRoute) (DarwinBrokerRoute, error) {
	destination, err := netip.ParsePrefix(route.Destination)
	if err != nil || !destination.Addr().Is4() || destination.Bits() != 32 || destination != destination.Masked() {
		return DarwinBrokerRoute{}, errors.New("Darwin broker route destination must be a canonical IPv4 /32")
	}
	gateway, err := netip.ParseAddr(route.Gateway)
	if err != nil || !gateway.Is4() {
		return DarwinBrokerRoute{}, errors.New("Darwin broker route gateway must be IPv4")
	}
	if route.Interface == "" || strings.HasPrefix(route.Interface, "utun") || strings.ContainsAny(route.Interface, " /\\\t\r\n") {
		return DarwinBrokerRoute{}, errors.New("Darwin broker route interface must be a physical interface")
	}
	return DarwinBrokerRoute{Destination: destination.String(), Gateway: gateway.String(), Interface: route.Interface, Global: route.Global}, nil
}

func removeDarwinBrokerRouteMarker(ifaceName string) error {
	path, err := darwinBrokerMarkerPath(ifaceName)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

type darwinCommandOutput func(name string, args ...string) ([]byte, error)

func runDarwinCommand(name string, args ...string) ([]byte, error) {
	return exec.Command(name, args...).CombinedOutput()
}

func cleanupDarwinBrokerRoute(ifaceName string) error {
	marker, err := LoadDarwinBrokerRoute(ifaceName)
	if err != nil || marker == nil {
		return err
	}
	return cleanupDarwinBrokerRouteWith(*marker, runDarwinCommand, func() error {
		return removeDarwinBrokerRouteMarker(ifaceName)
	})
}

func cleanupDarwinBrokerRouteWith(marker DarwinBrokerRoute, run darwinCommandOutput, removeMarker func() error) error {
	destination := strings.TrimSuffix(marker.Destination, "/32")
	output, getErr := run("route", "-n", "get", destination)
	if getErr != nil {
		message := strings.TrimSpace(string(output))
		if strings.Contains(message, "not in table") || strings.Contains(message, "No such process") {
			return removeMarker()
		}
		return fmt.Errorf("inspect owned broker route %s: %s: %w", marker.Destination, message, getErr)
	}
	observed, exactHost, err := inspectDarwinBrokerRouteGet(string(output), destination)
	if err != nil {
		return fmt.Errorf("inspect owned broker route %s: %w", marker.Destination, err)
	}
	if !exactHost {
		return removeMarker()
	}
	// route(8) reports lookup-result flags, not whether the owned table entry
	// was installed scoped or globally. The durable marker is authoritative for
	// deletion form; destination, gateway, and interface still must match.
	observed.Global = marker.Global
	if observed != marker {
		return fmt.Errorf("broker route %s drifted; refusing to delete and retaining marker", marker.Destination)
	}
	// A matching WASCLONED lookup is only a kernel cache result; the owned
	// static route is already absent, so issuing route delete would target an
	// entry that does not exist. Remove only the durable ownership marker.
	if isDarwinKernelClonedHostRoute(string(output), destination) {
		return removeMarker()
	}
	deleteArgs := []string{"-n", "delete", "-host", destination, marker.Gateway}
	if !marker.Global {
		deleteArgs = append(deleteArgs, "-ifscope", marker.Interface)
	}
	deleteOutput, err := run("route", deleteArgs...)
	if err != nil {
		return fmt.Errorf("remove owned broker route %s: %s: %w", marker.Destination, strings.TrimSpace(string(deleteOutput)), err)
	}
	postOutput, postErr := run("route", "-n", "get", destination)
	if postErr != nil {
		message := strings.TrimSpace(string(postOutput))
		if strings.Contains(message, "not in table") || strings.Contains(message, "No such process") {
			return removeMarker()
		}
		return fmt.Errorf("verify removal of owned broker route %s: %s: %w; retaining marker", marker.Destination, message, postErr)
	}
	postRoute, exactHost, parseErr := inspectDarwinBrokerRouteGet(string(postOutput), destination)
	if parseErr != nil {
		return fmt.Errorf("verify removal of owned broker route %s: %w; retaining marker", marker.Destination, parseErr)
	}
	if exactHost {
		postRoute.Global = marker.Global
		if postRoute != marker {
			return fmt.Errorf("broker route %s changed during cleanup; retaining marker", marker.Destination)
		}
		if isDarwinKernelClonedHostRoute(string(postOutput), destination) {
			return removeMarker()
		}
		return fmt.Errorf("owned broker route %s remained after deletion; retaining marker", marker.Destination)
	}
	return removeMarker()
}

func isDarwinKernelClonedHostRoute(output, brokerIP string) bool {
	values := parseDarwinRouteValues(output)
	if values["route to"] != brokerIP || (values["destination"] != brokerIP && values["destination"] != brokerIP+"/32") {
		return false
	}
	flags := make(map[string]struct{})
	for _, flag := range strings.Split(strings.Trim(values["flags"], "<>"), ",") {
		flags[strings.TrimSpace(flag)] = struct{}{}
	}
	if _, cloned := flags["WASCLONED"]; !cloned {
		return false
	}
	if _, host := flags["HOST"]; !host {
		return false
	}
	if _, static := flags["STATIC"]; static {
		return false
	}
	_, err := parseDarwinPhysicalRouteGet(output, brokerIP)
	return err == nil
}

func parseDarwinPhysicalRouteGet(output, expectedIPv4 string) (DarwinBrokerRoute, error) {
	values := parseDarwinRouteValues(output)
	if values["route to"] != expectedIPv4 {
		return DarwinBrokerRoute{}, fmt.Errorf("route lookup target %q does not match %s", values["route to"], expectedIPv4)
	}
	route := DarwinBrokerRoute{Destination: expectedIPv4 + "/32", Gateway: values["gateway"], Interface: values["interface"]}
	return validateDarwinBrokerRoute(route)
}

func inspectDarwinBrokerRouteGet(output, expectedIPv4 string) (DarwinBrokerRoute, bool, error) {
	values := parseDarwinRouteValues(output)
	if values["route to"] != expectedIPv4 {
		return DarwinBrokerRoute{}, false, fmt.Errorf("route lookup target %q does not match %s", values["route to"], expectedIPv4)
	}
	destination := values["destination"]
	if destination != expectedIPv4 && destination != expectedIPv4+"/32" {
		if destination == "" {
			return DarwinBrokerRoute{}, false, errors.New("route lookup omitted destination")
		}
		return DarwinBrokerRoute{}, false, nil
	}
	route := DarwinBrokerRoute{Destination: expectedIPv4 + "/32", Gateway: values["gateway"], Interface: values["interface"]}
	validated, err := validateDarwinBrokerRoute(route)
	return validated, true, err
}

func parseDarwinRouteGet(output, expectedIPv4 string) (DarwinBrokerRoute, error) {
	route, exactHost, err := inspectDarwinBrokerRouteGet(output, expectedIPv4)
	if err != nil {
		return DarwinBrokerRoute{}, err
	}
	if !exactHost {
		return DarwinBrokerRoute{}, fmt.Errorf("route destination does not match %s/32", expectedIPv4)
	}
	return route, nil
}

func parseDarwinRouteValues(output string) map[string]string {
	values := make(map[string]string)
	for _, line := range strings.Split(output, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok {
			continue
		}
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		if key == "destination" || key == "route to" || key == "gateway" || key == "interface" || key == "flags" {
			values[key] = value
		}
	}
	return values
}

// CleanupZombieAdapter is a no-op on macOS/Linux.
func CleanupZombieAdapter(ifaceName string) {}

// updatePSKOS returns an error on unix (falls through to wgctrl in tunnel.go).
func (t *Tunnel) updatePSKOS(newPSK string) error {
	return fmt.Errorf("not implemented on this platform")
}

// getStatusInProcess is a no-op on macOS/Linux.
func getStatusInProcess(ifaceName string) (*StatusInfo, error) {
	return nil, fmt.Errorf("not implemented on %s", runtime.GOOS)
}
