//go:build darwin

package tunnel

import (
	"errors"
	"net"
	"reflect"
	"strings"
	"testing"
)

func TestRenderMacOSConfigCharacterization(t *testing.T) {
	config := renderMacOSConfig(Config{
		PrivateKey:     "private-key",
		OverlayIP:      "10.200.1.2",
		BrokerPubKey:   "broker-key",
		BrokerEndpoint: "192.0.2.10:51820",
		PresharedKey:   "psk",
		AllowedIPs:     []string{"0.0.0.0/1", "128.0.0.0/1"},
		DNS:            "10.200.0.53",
		IsExitNode:     true,
	})

	const expected = `[Interface]
PrivateKey = private-key
Address = 10.200.1.2/32
Table = off

[Peer]
PublicKey = broker-key
PresharedKey = psk
Endpoint = 192.0.2.10:51820
AllowedIPs = 0.0.0.0/0
PersistentKeepalive = 25
`
	if config != expected {
		t.Fatalf("renderMacOSConfig() mismatch\nwant:\n%s\ngot:\n%s", expected, config)
	}
	if strings.Contains(config, "DNS =") {
		t.Fatalf("full-tunnel config unexpectedly delegates global DNS to wg-quick:\n%s", config)
	}
}

func TestRenderMacOSConfigOmitsOptionalLinesForSplitTunnel(t *testing.T) {
	config := renderMacOSConfig(Config{
		PrivateKey:     "private-key",
		OverlayIP:      "10.200.1.2/32",
		BrokerPubKey:   "broker-key",
		BrokerEndpoint: "192.0.2.10:51820",
		AllowedIPs:     []string{"10.20.0.0/16"},
		DNS:            "10.200.0.53",
		IsExitNode:     false,
	})

	if strings.Contains(config, "DNS =") {
		t.Fatalf("split-tunnel config unexpectedly contains DNS directive:\n%s", config)
	}
	if strings.Contains(config, "PresharedKey =") {
		t.Fatalf("config unexpectedly contains an empty preshared key directive:\n%s", config)
	}
	if !strings.Contains(config, "Address = 10.200.1.2/32\n") {
		t.Fatalf("config changed an explicit overlay prefix:\n%s", config)
	}
}

func TestDarwinTunnelCommandPlansUseAbsoluteConfigAndExactRoute(t *testing.T) {
	route := DarwinBrokerRoute{Destination: "192.0.2.10/32", Gateway: "192.0.2.1", Interface: "en0", Global: true}
	up, err := planDarwinWGQuick("up", "wg-wireztna")
	if err != nil {
		t.Fatal(err)
	}
	down, err := planDarwinWGQuick("down", "wg-wireztna")
	if err != nil {
		t.Fatal(err)
	}
	plans := []darwinCommandPlan{
		up,
		down,
		planDarwinBrokerRouteGet("192.0.2.10"),
		planDarwinBrokerRouteAdd(route),
		planDarwinBrokerRouteDelete(route),
	}
	plans = append(plans, planDarwinAddress("wg-wireztna", "10.200.1.2/32")...)
	plans = append(plans, planDarwinAllowedRoutes("wg-wireztna", []string{"10.20.0.0/16", "10.30.0.0/16"})...)

	want := []darwinCommandPlan{
		{Name: "/Library/Application Support/WireZTNA/runtime/wg-quick", Args: []string{"up", "/Library/Application Support/WireZTNA/wireguard/wg-wireztna.conf"}},
		{Name: "/Library/Application Support/WireZTNA/runtime/wg-quick", Args: []string{"down", "/Library/Application Support/WireZTNA/wireguard/wg-wireztna.conf"}},
		{Name: "route", Args: []string{"-n", "get", "192.0.2.10"}},
		{Name: "route", Args: []string{"-n", "add", "-host", "192.0.2.10", "192.0.2.1"}},
		{Name: "route", Args: []string{"-n", "delete", "-host", "192.0.2.10", "192.0.2.1"}},
		{Name: "ifconfig", Args: []string{"wg-wireztna", "inet", "10.200.1.2", "10.200.1.2", "alias"}},
		{Name: "ifconfig", Args: []string{"wg-wireztna", "up"}},
		{Name: "route", Args: []string{"-n", "add", "-net", "10.20.0.0/16", "-interface", "wg-wireztna"}},
		{Name: "route", Args: []string{"-n", "add", "-net", "10.30.0.0/16", "-interface", "wg-wireztna"}},
	}
	if !reflect.DeepEqual(plans, want) {
		t.Fatalf("Darwin command plans changed\nwant: %#v\ngot:  %#v", want, plans)
	}
	if _, err := planDarwinWGQuick("up", "../victim"); err == nil {
		t.Fatal("wg-quick accepted a non-fixed interface")
	}
	for _, plan := range plans {
		if plan.Name == "sudo" {
			t.Fatalf("tunnel plan unexpectedly wraps command in sudo: %#v", plan)
		}
	}
}

func TestResolveDarwinBrokerEndpointRequiresOneIPv4(t *testing.T) {
	cases := []struct {
		name     string
		endpoint string
		ips      []net.IP
		wantIP   string
		want     string
		wantErr  bool
	}{
		{name: "literal", endpoint: "192.0.2.10:51820", wantIP: "192.0.2.10", want: "192.0.2.10:51820"},
		{name: "one unique with duplicate", endpoint: "broker.example:51820", ips: []net.IP{net.ParseIP("192.0.2.10"), net.ParseIP("192.0.2.10")}, wantIP: "192.0.2.10", want: "192.0.2.10:51820"},
		{name: "multiple IPv4", endpoint: "broker.example:51820", ips: []net.IP{net.ParseIP("192.0.2.10"), net.ParseIP("192.0.2.11")}, wantErr: true},
		{name: "only IPv6", endpoint: "broker.example:51820", ips: []net.IP{net.ParseIP("2001:db8::10")}, wantErr: true},
		{name: "malformed endpoint", endpoint: "broker.example", wantErr: true},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			ip, endpoint, err := resolveDarwinBrokerEndpoint(test.endpoint, func(string) ([]net.IP, error) { return test.ips, nil })
			if (err != nil) != test.wantErr || ip != test.wantIP || endpoint != test.want {
				t.Fatalf("resolveDarwinBrokerEndpoint() = %q, %q, %v", ip, endpoint, err)
			}
		})
	}
}

func TestParseDarwinRouteIdentityRequiresExactPhysicalRoute(t *testing.T) {
	physical := "route to: 192.0.2.10\ndestination: default\ngateway: 192.0.2.1\ninterface: en0\n"
	route, err := parseDarwinPhysicalRouteGet(physical, "192.0.2.10")
	if err != nil || route != (DarwinBrokerRoute{Destination: "192.0.2.10/32", Gateway: "192.0.2.1", Interface: "en0"}) {
		t.Fatalf("physical route = %#v, %v", route, err)
	}
	exact := "route to: 192.0.2.10\ndestination: 192.0.2.10\ngateway: 192.0.2.1\ninterface: en0\n"
	if got, err := parseDarwinRouteGet(exact, "192.0.2.10"); err != nil || got != route {
		t.Fatalf("exact route = %#v, %v", got, err)
	}
	for _, output := range []string{
		"route to: 192.0.2.10\ndestination: 192.0.2.10\ngateway: 192.0.2.2\ninterface: utun7\n",
		"route to: 192.0.2.11\ndestination: 192.0.2.11\ngateway: 192.0.2.1\ninterface: en0\n",
	} {
		if _, err := parseDarwinRouteGet(output, "192.0.2.10"); err == nil {
			t.Fatalf("unsafe route output accepted: %q", output)
		}
	}
}

func TestDarwinKernelClonedHostRouteClassification(t *testing.T) {
	const brokerIP = "203.0.113.10"
	cloned := "route to: 203.0.113.10\ndestination: 203.0.113.10\ngateway: 10.196.1.1\ninterface: en7\nflags: <UP,GATEWAY,HOST,DONE,WASCLONED,IFSCOPE,IFREF,GLOBAL>\n"
	if !isDarwinKernelClonedHostRoute(cloned, brokerIP) {
		t.Fatal("kernel-cloned physical host route was not recognized")
	}
	for name, output := range map[string]string{
		"static host": strings.Replace(cloned, "WASCLONED,IFSCOPE,IFREF,GLOBAL", "STATIC", 1),
		"wrong host":  strings.ReplaceAll(cloned, brokerIP, "203.0.113.11"),
		"utun":        strings.Replace(cloned, "interface: en7", "interface: utun4", 1),
		"no host":     strings.Replace(cloned, "HOST,", "", 1),
	} {
		t.Run(name, func(t *testing.T) {
			if isDarwinKernelClonedHostRoute(output, brokerIP) {
				t.Fatalf("unsafe route classified as a removable kernel clone: %q", output)
			}
		})
	}
}

func TestCleanupDarwinBrokerRouteRetainsMarkerUnlessAbsenceIsProven(t *testing.T) {
	marker := DarwinBrokerRoute{Destination: "192.0.2.10/32", Gateway: "192.0.2.1", Interface: "en0"}
	exact := []byte("route to: 192.0.2.10\ndestination: 192.0.2.10\ngateway: 192.0.2.1\ninterface: en0\nflags: <UP,GATEWAY,HOST,DONE,STATIC,IFSCOPE>\n")

	t.Run("drift never deletes", func(t *testing.T) {
		removed := false
		calls := 0
		err := cleanupDarwinBrokerRouteWith(marker, func(string, ...string) ([]byte, error) {
			calls++
			return []byte("route to: 192.0.2.10\ndestination: 192.0.2.10\ngateway: 192.0.2.254\ninterface: en0\n"), nil
		}, func() error { removed = true; return nil })
		if err == nil || removed || calls != 1 {
			t.Fatalf("drift cleanup err=%v removed=%v calls=%d", err, removed, calls)
		}
	})

	t.Run("inconclusive post-delete retains marker", func(t *testing.T) {
		removed := false
		calls := 0
		err := cleanupDarwinBrokerRouteWith(marker, func(string, ...string) ([]byte, error) {
			calls++
			switch calls {
			case 1:
				return exact, nil
			case 2:
				return nil, nil
			default:
				return []byte("permission denied"), errors.New("injected observation failure")
			}
		}, func() error { removed = true; return nil })
		if err == nil || removed || calls != 3 {
			t.Fatalf("inconclusive cleanup err=%v removed=%v calls=%d", err, removed, calls)
		}
	})

	t.Run("kernel clone after exact delete proves static route absence", func(t *testing.T) {
		removed := false
		calls := 0
		err := cleanupDarwinBrokerRouteWith(marker, func(name string, args ...string) ([]byte, error) {
			calls++
			switch calls {
			case 1:
				return exact, nil
			case 2:
				want := []string{"-n", "delete", "-host", "192.0.2.10", "192.0.2.1", "-ifscope", "en0"}
				if name != "route" || !reflect.DeepEqual(args, want) {
					t.Fatalf("exact delete = %s %v, want route %v", name, args, want)
				}
				return nil, nil
			default:
				return []byte("route to: 192.0.2.10\ndestination: 192.0.2.10\ngateway: 192.0.2.1\ninterface: en0\nflags: <UP,GATEWAY,HOST,DONE,WASCLONED,IFSCOPE,IFREF,GLOBAL>\n"), nil
			}
		}, func() error { removed = true; return nil })
		if err != nil || !removed || calls != 3 {
			t.Fatalf("clone cleanup err=%v removed=%v calls=%d", err, removed, calls)
		}
	})

	t.Run("static route after delete retains marker", func(t *testing.T) {
		removed := false
		calls := 0
		err := cleanupDarwinBrokerRouteWith(marker, func(string, ...string) ([]byte, error) {
			calls++
			if calls == 2 {
				return nil, nil
			}
			return exact, nil
		}, func() error { removed = true; return nil })
		if err == nil || removed || calls != 3 {
			t.Fatalf("static cleanup err=%v removed=%v calls=%d", err, removed, calls)
		}
	})

	t.Run("kernel clone drift after delete retains marker", func(t *testing.T) {
		removed := false
		calls := 0
		err := cleanupDarwinBrokerRouteWith(marker, func(string, ...string) ([]byte, error) {
			calls++
			switch calls {
			case 1:
				return exact, nil
			case 2:
				return nil, nil
			default:
				return []byte("route to: 192.0.2.10\ndestination: 192.0.2.10\ngateway: 192.0.2.254\ninterface: en0\nflags: <UP,GATEWAY,HOST,DONE,WASCLONED,IFSCOPE>\n"), nil
			}
		}, func() error { removed = true; return nil })
		if err == nil || removed || calls != 3 {
			t.Fatalf("post-delete drift cleanup err=%v removed=%v calls=%d", err, removed, calls)
		}
	})

	t.Run("fallback physical route proves host route absence", func(t *testing.T) {
		removed := false
		calls := 0
		err := cleanupDarwinBrokerRouteWith(marker, func(string, ...string) ([]byte, error) {
			calls++
			if calls == 1 {
				return exact, nil
			}
			if calls == 2 {
				return nil, nil
			}
			return []byte("route to: 192.0.2.10\ndestination: default\ngateway: 192.0.2.1\ninterface: en0\n"), nil
		}, func() error { removed = true; return nil })
		if err != nil || !removed || calls != 3 {
			t.Fatalf("proven absence cleanup err=%v removed=%v calls=%d", err, removed, calls)
		}
	})
}

func TestCleanupDarwinBrokerRouteInitialKernelCloneRemovesOnlyMarker(t *testing.T) {
	marker := DarwinBrokerRoute{Destination: "192.0.2.10/32", Gateway: "192.0.2.1", Interface: "en0"}
	clone := []byte("route to: 192.0.2.10\ndestination: 192.0.2.10\ngateway: 192.0.2.1\ninterface: en0\nflags: <UP,GATEWAY,HOST,DONE,WASCLONED,IFSCOPE>\n")
	removed := false
	calls := 0
	err := cleanupDarwinBrokerRouteWith(marker, func(name string, args ...string) ([]byte, error) {
		calls++
		if name != "route" || !reflect.DeepEqual(args, []string{"-n", "get", "192.0.2.10"}) {
			t.Fatalf("unexpected command: %s %v", name, args)
		}
		return clone, nil
	}, func() error { removed = true; return nil })
	if err != nil || !removed || calls != 1 {
		t.Fatalf("initial clone cleanup err=%v removed=%v calls=%d", err, removed, calls)
	}
}

func TestRecoverDarwinBrokerRouteHandlesPreConfigCrashPhases(t *testing.T) {
	marker := DarwinBrokerRoute{Destination: "192.0.2.10/32", Gateway: "192.0.2.1", Interface: "en0"}
	safeRoutes := []byte("Routing tables\n\nInternet:\nDestination Gateway Flags Netif Expire\ndefault 192.0.2.1 UGScg en0\n")
	physical := []byte("route to: 192.0.2.10\ndestination: default\ngateway: 192.0.2.1\ninterface: en0\n")
	exact := []byte("route to: 192.0.2.10\ndestination: 192.0.2.10\ngateway: 192.0.2.1\ninterface: en0\nflags: <UP,GATEWAY,HOST,DONE,STATIC>\n")

	for _, phase := range []struct {
		name           string
		routeInstalled bool
		wantCalls      int
	}{
		{name: "marker persisted before route add", wantCalls: 2},
		{name: "route added before config write", routeInstalled: true, wantCalls: 4},
		{name: "verified marker finalized before config write", routeInstalled: true, wantCalls: 4},
	} {
		t.Run(phase.name, func(t *testing.T) {
			removed := false
			calls := 0
			routeGets := 0
			err := recoverDarwinBrokerRouteWith(marker, func() ([]string, error) { return nil, nil }, func(name string, args ...string) ([]byte, error) {
				calls++
				if name == "netstat" {
					return safeRoutes, nil
				}
				if name != "route" {
					t.Fatalf("unexpected command: %s %v", name, args)
				}
				if len(args) >= 2 && args[1] == "delete" {
					return nil, nil
				}
				routeGets++
				if phase.routeInstalled && routeGets == 1 {
					return exact, nil
				}
				return physical, nil
			}, func() error { removed = true; return nil })
			if err != nil || !removed || calls != phase.wantCalls {
				t.Fatalf("recovery err=%v removed=%v calls=%d, want %d", err, removed, calls, phase.wantCalls)
			}
		})
	}
}

func TestRecoverDarwinBrokerRouteFailsClosed(t *testing.T) {
	marker := DarwinBrokerRoute{Destination: "192.0.2.10/32", Gateway: "192.0.2.1", Interface: "en0"}
	safeRoutes := "Routing tables\n\nInternet:\nDestination Gateway Flags Netif Expire\ndefault 192.0.2.1 UGScg en0\n"
	cases := []struct {
		name      string
		devices   []string
		deviceErr error
		routes    string
		routeErr  error
	}{
		{name: "WireGuard utun remains", devices: []string{"utun7"}, routes: safeRoutes},
		{name: "device enumeration fails", deviceErr: errors.New("injected wgctrl failure"), routes: safeRoutes},
		{name: "lower half route remains", routes: strings.Replace(safeRoutes, "default 192.0.2.1", "0/1 link#20 UCS utun7", 1)},
		{name: "upper half route remains", routes: strings.Replace(safeRoutes, "default 192.0.2.1", "128.0/1 link#20 UCS utun7", 1)},
		{name: "route inspection fails", routes: "permission denied", routeErr: errors.New("injected netstat failure")},
		{name: "route table is ambiguous", routes: "unexpected output"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			removed := false
			cleanupObserved := false
			err := recoverDarwinBrokerRouteWith(marker, func() ([]string, error) {
				return tc.devices, tc.deviceErr
			}, func(name string, _ ...string) ([]byte, error) {
				if name == "route" {
					cleanupObserved = true
				}
				return []byte(tc.routes), tc.routeErr
			}, func() error { removed = true; return nil })
			if err == nil || removed || cleanupObserved {
				t.Fatalf("fail-closed recovery err=%v removed=%v cleanup=%v", err, removed, cleanupObserved)
			}
		})
	}
}
