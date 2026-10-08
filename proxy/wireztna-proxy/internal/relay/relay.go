// Package relay implements the local TCP listener that accepts connections
// from the agent and relays them through the WebSocket tunnel.
//
// Architecture: Each local TCP connection gets its own WebSocket session.
// This maps to the broker design where 1 WebSocket = 1 TCP connection to target.
// When the local TCP closes, the WebSocket closes. When a new TCP connects,
// a new WebSocket is opened.
package relay

import (
	"fmt"
	"net"
	"sync"
	"sync/atomic"

	"github.com/wireztna/proxy/internal/tunnel"
)

// Config holds relay configuration.
type Config struct {
	LocalPort int // Port to listen on (127.0.0.1 only)
}

// TunnelConfig holds the parameters needed to create new tunnel connections.
type TunnelConfig struct {
	BrokerURL string
	PassID    string
}

// Relay manages the local TCP listener. Each accepted TCP connection
// gets its own WebSocket tunnel to the broker.
type Relay struct {
	config    Config
	tunConfig TunnelConfig
	listener  net.Listener
	done      chan struct{}
	wg        sync.WaitGroup

	// Stats (accumulated across all connections)
	BytesIn  atomic.Int64
	BytesOut atomic.Int64
	Conns    atomic.Int32
}

// New creates a new Relay instance.
func New(cfg Config, tunCfg TunnelConfig) *Relay {
	return &Relay{
		config:    cfg,
		tunConfig: tunCfg,
		done:      make(chan struct{}),
	}
}

// ValidateLocalPort ensures the port is valid and will bind to localhost only.
func ValidateLocalPort(port int) error {
	if port <= 0 || port > 65535 {
		return fmt.Errorf("port must be between 1 and 65535, got %d", port)
	}
	return nil
}

// ValidateBindAddress ensures only loopback addresses are used.
func ValidateBindAddress(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}

	ip := net.ParseIP(host)
	if ip == nil {
		if host == "localhost" {
			return nil
		}
		return fmt.Errorf("invalid bind address: %s", addr)
	}

	if !ip.IsLoopback() {
		return fmt.Errorf("security: only loopback addresses allowed, got %s", addr)
	}
	return nil
}

// Start begins listening on 127.0.0.1:<port>.
func (r *Relay) Start() error {
	addr := fmt.Sprintf("127.0.0.1:%d", r.config.LocalPort)

	if err := ValidateBindAddress(addr); err != nil {
		return err
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", addr, err)
	}
	r.listener = ln

	r.wg.Add(1)
	go r.acceptLoop()

	return nil
}

// Stop gracefully shuts down the relay.
func (r *Relay) Stop() {
	close(r.done)
	if r.listener != nil {
		r.listener.Close()
	}
	r.wg.Wait()
}

// Addr returns the listener address.
func (r *Relay) Addr() string {
	if r.listener != nil {
		return r.listener.Addr().String()
	}
	return fmt.Sprintf("127.0.0.1:%d", r.config.LocalPort)
}

func (r *Relay) acceptLoop() {
	defer r.wg.Done()

	for {
		select {
		case <-r.done:
			return
		default:
		}

		conn, err := r.listener.Accept()
		if err != nil {
			select {
			case <-r.done:
				return
			default:
				continue
			}
		}

		// Verify loopback
		remoteAddr := conn.RemoteAddr().(*net.TCPAddr)
		if !remoteAddr.IP.IsLoopback() {
			conn.Close()
			continue
		}

		r.wg.Add(1)
		go r.handleConnection(conn)
	}
}

// handleConnection opens a NEW WebSocket to the broker for each local TCP connection.
// This matches the broker design: 1 WS = 1 target TCP.
func (r *Relay) handleConnection(conn net.Conn) {
	defer r.wg.Done()
	defer conn.Close()

	r.Conns.Add(1)

	// Open a new WebSocket tunnel for this connection
	t, err := tunnel.Connect(tunnel.Config{
		BrokerURL: r.tunConfig.BrokerURL,
		PassID:    r.tunConfig.PassID,
	})
	if err != nil {
		fmt.Printf("[wzctl] Failed to open tunnel for connection: %v\n", err)
		return
	}
	defer t.Close()

	// Bidirectional relay: local TCP ↔ this WebSocket
	var wg sync.WaitGroup
	wg.Add(2)
	connDone := make(chan struct{})

	// Local TCP → WebSocket (upload)
	go func() {
		defer wg.Done()
		buf := make([]byte, 65536)
		for {
			n, err := conn.Read(buf)
			if err != nil {
				return
			}
			if n > 0 {
				if err := t.WriteMessage(buf[:n]); err != nil {
					return
				}
				r.BytesOut.Add(int64(n))
			}
		}
	}()

	// WebSocket → Local TCP (download)
	go func() {
		defer wg.Done()
		for {
			data, err := t.ReadMessage()
			if err != nil {
				// Close the local TCP so the other goroutine exits too
				conn.Close()
				return
			}
			if len(data) > 0 {
				if _, err := conn.Write(data); err != nil {
					return
				}
				r.BytesIn.Add(int64(len(data)))
			}
		}
	}()

	// Wait for both directions to finish
	go func() {
		wg.Wait()
		close(connDone)
	}()

	select {
	case <-connDone:
	case <-r.done:
	}
}
