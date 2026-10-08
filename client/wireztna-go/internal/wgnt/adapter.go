//go:build windows

package wgnt

import (
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"net"
	"net/netip"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const KeyLength = 32

// AdapterHandle is an opaque handle to a WireGuard NT adapter.
type AdapterHandle uintptr

// AdapterState represents the up/down state of an adapter.
type AdapterState uint32

const (
	AdapterStateDown AdapterState = 0
	AdapterStateUp   AdapterState = 1
)

// PeerFlag controls which fields are set/read on a peer.
type PeerFlag uint32

const (
	PeerHasPublicKey          PeerFlag = 1 << 0
	PeerHasPresharedKey       PeerFlag = 1 << 1
	PeerHasPersistentKeepalive PeerFlag = 1 << 2
	PeerHasEndpoint           PeerFlag = 1 << 3
	PeerReplaceAllowedIPs     PeerFlag = 1 << 5
	PeerRemove                PeerFlag = 1 << 6
	PeerUpdateOnly            PeerFlag = 1 << 7
)

// InterfaceFlag controls which fields are set/read on the interface.
type InterfaceFlag uint32

const (
	InterfaceHasPublicKey  InterfaceFlag = 1 << 0
	InterfaceHasPrivateKey InterfaceFlag = 1 << 1
	InterfaceHasListenPort InterfaceFlag = 1 << 2
	InterfaceReplacePeers  InterfaceFlag = 1 << 3
)

// PeerStats holds the traffic and handshake stats for a peer (from GetConfiguration).
type PeerStats struct {
	TxBytes       int64
	RxBytes       int64
	LastHandshake time.Time
	Endpoint      string
}

// ─── Adapter lifecycle ───

// CreateAdapter creates a new WireGuard NT adapter with the given name.
// The GUID is deterministic based on the name to ensure NLA stability.
func CreateAdapter(name string) (AdapterHandle, error) {
	if err := Initialize(); err != nil {
		return 0, err
	}

	nameUTF16, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return 0, fmt.Errorf("invalid adapter name: %w", err)
	}
	tunnelType, _ := windows.UTF16PtrFromString("WireZTNA")

	// Use a deterministic GUID derived from the adapter name
	guid := deterministicGUID(name)

	ret, _, lastErr := procCreateAdapter.Call(
		uintptr(unsafe.Pointer(nameUTF16)),
		uintptr(unsafe.Pointer(tunnelType)),
		uintptr(unsafe.Pointer(&guid)),
	)
	if ret == 0 {
		return 0, fmt.Errorf("WireGuardCreateAdapter failed: %w", lastErr)
	}
	return AdapterHandle(ret), nil
}

// OpenAdapter opens an existing WireGuard NT adapter by name.
func OpenAdapter(name string) (AdapterHandle, error) {
	if err := Initialize(); err != nil {
		return 0, err
	}

	nameUTF16, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return 0, fmt.Errorf("invalid adapter name: %w", err)
	}

	ret, _, lastErr := procOpenAdapter.Call(uintptr(unsafe.Pointer(nameUTF16)))
	if ret == 0 {
		return 0, fmt.Errorf("WireGuardOpenAdapter failed: %w", lastErr)
	}
	return AdapterHandle(ret), nil
}

// Close releases adapter resources and removes the adapter.
func (h AdapterHandle) Close() {
	if h == 0 {
		return
	}
	procCloseAdapter.Call(uintptr(h))
}

// SetState sets the adapter up or down.
func (h AdapterHandle) SetState(state AdapterState) error {
	ret, _, lastErr := procSetAdapterState.Call(uintptr(h), uintptr(state))
	if ret == 0 {
		return fmt.Errorf("WireGuardSetAdapterState failed: %w", lastErr)
	}
	return nil
}

// GetState returns the current adapter state.
func (h AdapterHandle) GetState() (AdapterState, error) {
	var state AdapterState
	ret, _, lastErr := procGetAdapterState.Call(uintptr(h), uintptr(unsafe.Pointer(&state)))
	if ret == 0 {
		return 0, fmt.Errorf("WireGuardGetAdapterState failed: %w", lastErr)
	}
	return state, nil
}

// LUID returns the adapter's NET_LUID (8 bytes).
func (h AdapterHandle) LUID() uint64 {
	var luid uint64
	procGetAdapterLUID.Call(uintptr(h), uintptr(unsafe.Pointer(&luid)))
	return luid
}

// ─── Configuration ───

// WireGuard NT uses packed C structs for configuration. We build the raw byte
// buffer directly to avoid CGO and alignment headaches.

// Configure sets the WireGuard configuration (interface + one peer) on the adapter.
// This is the main function called during tunnel creation and PSK updates.
func (h AdapterHandle) Configure(privKeyB64, peerPubKeyB64, pskB64, endpoint string, allowedIPs []string, keepalive uint16) error {
	buf, err := buildConfigBuffer(privKeyB64, peerPubKeyB64, pskB64, endpoint, allowedIPs, keepalive)
	if err != nil {
		return fmt.Errorf("build config: %w", err)
	}

	ret, _, lastErr := procSetConfiguration.Call(
		uintptr(h),
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(len(buf)),
	)
	if ret == 0 {
		return fmt.Errorf("WireGuardSetConfiguration failed: %w", lastErr)
	}
	return nil
}

// UpdatePSK hot-swaps the preshared key without tearing down the tunnel.
func (h AdapterHandle) UpdatePSK(peerPubKeyB64, newPSKB64 string) error {
	pubKey, err := b64ToKey(peerPubKeyB64)
	if err != nil {
		return err
	}
	psk, err := b64ToKey(newPSKB64)
	if err != nil {
		return err
	}

	// Build a minimal config: interface (no changes) + peer (update_only + new PSK)
	// Interface: 80 bytes (flags=0, listenPort=0, privKey=zeros, pubKey=zeros, peersCount=1)
	// Peer: 136 bytes (flags=UPDATE_ONLY|HAS_PUBLIC_KEY|HAS_PRESHARED_KEY, rest zeros, allowedIPsCount=0)
	buf := make([]byte, 80+136)

	// Interface header
	binary.LittleEndian.PutUint32(buf[0:4], 0) // flags: none
	// listenPort, privateKey, publicKey: all zeros
	binary.LittleEndian.PutUint32(buf[76:80], 1) // peersCount = 1

	// Peer at offset 80
	peerOff := 80
	flags := PeerUpdateOnly | PeerHasPublicKey | PeerHasPresharedKey
	binary.LittleEndian.PutUint32(buf[peerOff:peerOff+4], uint32(flags))
	// Reserved: 4 bytes (already zero)
	copy(buf[peerOff+8:peerOff+8+32], pubKey[:])   // PublicKey
	copy(buf[peerOff+40:peerOff+40+32], psk[:])     // PresharedKey
	// PersistentKeepalive, Endpoint, TxBytes, RxBytes, LastHandshake: zeros
	// AllowedIPsCount = 0 (already zero at offset peerOff+132)

	ret, _, lastErr := procSetConfiguration.Call(
		uintptr(h),
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(len(buf)),
	)
	if ret == 0 {
		return fmt.Errorf("WireGuardSetConfiguration (PSK update) failed: %w", lastErr)
	}
	return nil
}

// GetPeerStats reads the current peer stats (traffic, handshake, endpoint) from the adapter.
func (h AdapterHandle) GetPeerStats() (*PeerStats, error) {
	// Start with a reasonable buffer size for 1 peer + a few allowed IPs
	size := uint32(4096)
	for {
		buf := make([]byte, size)
		ret, _, lastErr := procGetConfiguration.Call(
			uintptr(h),
			uintptr(unsafe.Pointer(&buf[0])),
			uintptr(unsafe.Pointer(&size)),
		)
		if ret != 0 {
			return parseStats(buf[:size])
		}
		// ERROR_MORE_DATA = 234
		if errno, ok := lastErr.(windows.Errno); ok && errno == 234 {
			continue // retry with updated size
		}
		return nil, fmt.Errorf("WireGuardGetConfiguration failed: %w", lastErr)
	}
}

// ─── Internal helpers ───

// buildConfigBuffer builds the raw byte buffer for WireGuardSetConfiguration.
// Layout: WIREGUARD_INTERFACE (80 bytes) + WIREGUARD_PEER (136 bytes) + N * WIREGUARD_ALLOWED_IP (24 bytes each)
func buildConfigBuffer(privKeyB64, peerPubKeyB64, pskB64, endpoint string, allowedIPs []string, keepalive uint16) ([]byte, error) {
	privKey, err := b64ToKey(privKeyB64)
	if err != nil {
		return nil, fmt.Errorf("private key: %w", err)
	}
	pubKey, err := b64ToKey(peerPubKeyB64)
	if err != nil {
		return nil, fmt.Errorf("peer public key: %w", err)
	}

	// Parse allowed IPs
	var parsedIPs []netip.Prefix
	for _, cidr := range allowedIPs {
		prefix, err := netip.ParsePrefix(cidr)
		if err != nil {
			continue
		}
		parsedIPs = append(parsedIPs, prefix)
	}

	// Struct sizes (8-byte aligned as per wireguard.h ALIGNED(8))
	const ifaceSize = 80  // WIREGUARD_INTERFACE
	const peerSize = 136  // WIREGUARD_PEER
	const allowedIPSize = 24 // WIREGUARD_ALLOWED_IP (8-byte aligned: union(16) + family(2) + cidr(1) + flags(4) + padding = 24)

	totalSize := ifaceSize + peerSize + len(parsedIPs)*allowedIPSize
	buf := make([]byte, totalSize)

	// ─── WIREGUARD_INTERFACE (80 bytes, ALIGNED(8)) ───
	// Offset 0: Flags (DWORD, 4 bytes)
	iflags := InterfaceHasPrivateKey
	binary.LittleEndian.PutUint32(buf[0:4], uint32(iflags))
	// Offset 4: ListenPort (WORD, 2 bytes) — 0 = random
	// Offset 6: PrivateKey (BYTE[32]) — immediately after ListenPort, no padding
	copy(buf[6:38], privKey[:])
	// Offset 38: PublicKey (BYTE[32]) — unused on set
	// Offset 70: padding (2 bytes for DWORD alignment of PeersCount)
	// Offset 72: PeersCount (DWORD, 4 bytes)
	binary.LittleEndian.PutUint32(buf[72:76], 1)
	// Offset 76: padding (4 bytes for ALIGNED(8) struct total = 80)

	// ─── WIREGUARD_PEER (136 bytes) ───
	peerOff := ifaceSize
	pflags := PeerHasPublicKey | PeerHasEndpoint | PeerHasPersistentKeepalive | PeerReplaceAllowedIPs
	if pskB64 != "" {
		pflags |= PeerHasPresharedKey
	}
	binary.LittleEndian.PutUint32(buf[peerOff:peerOff+4], uint32(pflags))
	// Offset +4: Reserved (4 bytes, zero)
	// Offset +8: PublicKey (32 bytes)
	copy(buf[peerOff+8:peerOff+40], pubKey[:])
	// Offset +40: PresharedKey (32 bytes)
	if pskB64 != "" {
		psk, err := b64ToKey(pskB64)
		if err != nil {
			return nil, fmt.Errorf("PSK: %w", err)
		}
		copy(buf[peerOff+40:peerOff+72], psk[:])
	}
	// Offset +72: PersistentKeepalive (2 bytes)
	binary.LittleEndian.PutUint16(buf[peerOff+72:peerOff+74], keepalive)
	// Offset +74: padding (2 bytes)
	// Offset +76: Endpoint — SOCKADDR_INET (28 bytes)
	if endpoint != "" {
		endpointBytes, err := encodeSockaddrInet(endpoint)
		if err == nil {
			copy(buf[peerOff+76:peerOff+104], endpointBytes)
		}
	}
	// Offset +104: TxBytes (8 bytes) — unused on set
	// Offset +112: RxBytes (8 bytes) — unused on set
	// Offset +120: LastHandshake (8 bytes) — unused on set
	// Offset +128: padding (4 bytes)
	// Offset +132: AllowedIPsCount (4 bytes)
	binary.LittleEndian.PutUint32(buf[peerOff+132:peerOff+136], uint32(len(parsedIPs)))

	// ─── WIREGUARD_ALLOWED_IP entries (24 bytes each) ───
	for i, prefix := range parsedIPs {
		off := ifaceSize + peerSize + i*allowedIPSize
		addr := prefix.Addr()
		if addr.Is4() {
			binary.LittleEndian.PutUint16(buf[off+16:off+18], windows.AF_INET) // AddressFamily
			ip4 := addr.As4()
			copy(buf[off:off+4], ip4[:]) // V4 address in union
		} else {
			binary.LittleEndian.PutUint16(buf[off+16:off+18], windows.AF_INET6)
			ip6 := addr.As16()
			copy(buf[off:off+16], ip6[:]) // V6 address in union
		}
		buf[off+18] = byte(prefix.Bits()) // CIDR
		// Flags at off+20 (4 bytes) — 0 = add
	}

	return buf, nil
}

// parseStats extracts PeerStats from a GetConfiguration response buffer.
func parseStats(buf []byte) (*PeerStats, error) {
	if len(buf) < 80 {
		return nil, fmt.Errorf("buffer too small for interface header")
	}

	peersCount := binary.LittleEndian.Uint32(buf[76:80])
	if peersCount == 0 {
		return nil, fmt.Errorf("no peers in configuration")
	}

	// Parse first peer at offset 80
	const peerOff = 80
	if len(buf) < peerOff+136 {
		return nil, fmt.Errorf("buffer too small for peer")
	}

	stats := &PeerStats{}
	stats.TxBytes = int64(binary.LittleEndian.Uint64(buf[peerOff+104 : peerOff+112]))
	stats.RxBytes = int64(binary.LittleEndian.Uint64(buf[peerOff+112 : peerOff+120]))

	// LastHandshake is in 100ns intervals since 1601-01-01 UTC (Windows FILETIME)
	handshake100ns := binary.LittleEndian.Uint64(buf[peerOff+120 : peerOff+128])
	if handshake100ns > 0 {
		// Convert Windows FILETIME to Unix time
		// Windows epoch: 1601-01-01, Unix epoch: 1970-01-01
		// Difference: 11644473600 seconds
		const windowsEpochDiff = 11644473600
		unixSeconds := int64(handshake100ns/10000000) - windowsEpochDiff
		if unixSeconds > 0 {
			stats.LastHandshake = time.Unix(unixSeconds, 0)
		}
	}

	// Parse endpoint from SOCKADDR_INET at offset +76
	stats.Endpoint = decodeSockaddrInet(buf[peerOff+76 : peerOff+104])

	return stats, nil
}

// encodeSockaddrInet encodes a "host:port" string into a 28-byte SOCKADDR_INET.
func encodeSockaddrInet(endpoint string) ([]byte, error) {
	host, portStr, err := net.SplitHostPort(endpoint)
	if err != nil {
		return nil, err
	}

	// Resolve hostname to IP if needed
	ip := net.ParseIP(host)
	if ip == nil {
		ips, err := net.LookupHost(host)
		if err != nil || len(ips) == 0 {
			return nil, fmt.Errorf("cannot resolve %s", host)
		}
		ip = net.ParseIP(ips[0])
	}

	var port uint16
	fmt.Sscanf(portStr, "%d", &port)

	buf := make([]byte, 28)

	if ip4 := ip.To4(); ip4 != nil {
		// SOCKADDR_IN: family(2) + port(2, big-endian) + addr(4) + padding(8) = 16 bytes
		binary.LittleEndian.PutUint16(buf[0:2], windows.AF_INET)
		binary.BigEndian.PutUint16(buf[2:4], port) // network byte order
		copy(buf[4:8], ip4)
	} else if ip6 := ip.To16(); ip6 != nil {
		// SOCKADDR_IN6: family(2) + port(2, big-endian) + flowinfo(4) + addr(16) + scope_id(4) = 28 bytes
		binary.LittleEndian.PutUint16(buf[0:2], windows.AF_INET6)
		binary.BigEndian.PutUint16(buf[2:4], port)
		copy(buf[8:24], ip6)
	}

	return buf, nil
}

// decodeSockaddrInet decodes a 28-byte SOCKADDR_INET to a "host:port" string.
func decodeSockaddrInet(buf []byte) string {
	if len(buf) < 4 {
		return ""
	}
	family := binary.LittleEndian.Uint16(buf[0:2])
	port := binary.BigEndian.Uint16(buf[2:4])

	switch family {
	case windows.AF_INET:
		if len(buf) < 8 {
			return ""
		}
		ip := net.IPv4(buf[4], buf[5], buf[6], buf[7])
		if ip.IsUnspecified() && port == 0 {
			return ""
		}
		return fmt.Sprintf("%s:%d", ip, port)
	case windows.AF_INET6:
		if len(buf) < 24 {
			return ""
		}
		ip := net.IP(buf[8:24])
		if ip.IsUnspecified() && port == 0 {
			return ""
		}
		return fmt.Sprintf("[%s]:%d", ip, port)
	}
	return ""
}

// b64ToKey decodes a base64-encoded WireGuard key to a 32-byte array.
func b64ToKey(b64 string) ([KeyLength]byte, error) {
	var key [KeyLength]byte
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return key, fmt.Errorf("invalid base64 key: %w", err)
	}
	if len(raw) != KeyLength {
		return key, fmt.Errorf("key must be %d bytes, got %d", KeyLength, len(raw))
	}
	copy(key[:], raw)
	return key, nil
}

// deterministicGUID generates a stable GUID from a string name.
// This ensures the same adapter name always gets the same NLA entry.
func deterministicGUID(name string) windows.GUID {
	// Simple hash-based GUID (not cryptographic — just needs to be deterministic)
	var guid windows.GUID
	h := uint32(0x57495245) // "WIRE" seed
	for _, c := range name {
		h = h*31 + uint32(c)
	}
	guid.Data1 = h
	guid.Data2 = uint16(h >> 16)
	guid.Data3 = uint16(h >> 8)
	// Fill Data4 with more hash bytes
	for i := 0; i < 8; i++ {
		h = h*31 + uint32(i+len(name))
		guid.Data4[i] = byte(h)
	}
	// Set version 4 (random) and variant bits for a valid UUID
	guid.Data3 = (guid.Data3 & 0x0FFF) | 0x4000 // version 4
	guid.Data4[0] = (guid.Data4[0] & 0x3F) | 0x80 // variant 1
	return guid
}

// DriverVersion returns the version of the loaded wireguard-nt driver, or 0 if not loaded.
func DriverVersion() uint32 {
	if err := Initialize(); err != nil {
		return 0
	}
	ret, _, _ := procGetRunningDriverVer.Call()
	return uint32(ret)
}
