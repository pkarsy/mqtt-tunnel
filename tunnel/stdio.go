package tunnel

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"sync"
	"time"
)

// StdioConnection wraps stdin/stdout as a net.Conn
type StdioConnection struct {
	mu          sync.Mutex
	readClosed  bool
	writeClosed bool
	localAddr   net.Addr
	remoteAddr  net.Addr
}

func NewStdioConnection() *StdioConnection {
	return &StdioConnection{
		localAddr:  &stdioAddr{addr: "stdio-local"},
		remoteAddr: &stdioAddr{addr: "stdio-remote"},
	}
}

// Read reads from stdin
func (c *StdioConnection) Read(b []byte) (n int, err error) {
	c.mu.Lock()
	if c.readClosed {
		c.mu.Unlock()
		return 0, io.EOF
	}
	c.mu.Unlock()

	n, err = os.Stdin.Read(b)
	if err != nil {
		c.mu.Lock()
		c.readClosed = true
		c.mu.Unlock()
	}
	return n, err
}

// Write writes to stdout
func (c *StdioConnection) Write(b []byte) (n int, err error) {
	c.mu.Lock()
	if c.writeClosed {
		c.mu.Unlock()
		return 0, io.ErrClosedPipe
	}
	c.mu.Unlock()

	n, err = os.Stdout.Write(b)
	if err != nil {
		c.mu.Lock()
		c.writeClosed = true
		c.mu.Unlock()
	}
	return n, err
}

// Close closes both stdin and stdout
func (c *StdioConnection) Close() error {
	c.mu.Lock()
	c.readClosed = true
	c.writeClosed = true
	c.mu.Unlock()
	return nil
}

// LocalAddr returns the local address
func (c *StdioConnection) LocalAddr() net.Addr {
	return c.localAddr
}

// RemoteAddr returns the remote address
func (c *StdioConnection) RemoteAddr() net.Addr {
	return c.remoteAddr
}

// SetDeadline sets the read and write deadlines
func (c *StdioConnection) SetDeadline(t time.Time) error {
	return fmt.Errorf("SetDeadline not implemented for StdioConnection")
}

// SetReadDeadline sets the deadline for future Read calls
func (c *StdioConnection) SetReadDeadline(t time.Time) error {
	return fmt.Errorf("SetReadDeadline not implemented for StdioConnection")
}

// SetWriteDeadline sets the deadline for future Write calls
func (c *StdioConnection) SetWriteDeadline(t time.Time) error {
	return fmt.Errorf("SetWriteDeadline not implemented for StdioConnection")
}

// stdioAddr implements net.Addr for stdio connections
type stdioAddr struct {
	addr string
}

func (a *stdioAddr) Network() string {
	return "stdio"
}

func (a *stdioAddr) String() string {
	return a.addr
}

// StdioTunnel manages a tunnel using stdin/stdout
type StdioTunnel struct {
	mqt         *MQTunnel
	ctx         context.Context
	conn        net.Conn
	tun         *Tunnel
	tcpClosedCh chan error
}

func NewStdioTunnel(mqt *MQTunnel, ctx context.Context, remotePort int) *StdioTunnel {
	return &StdioTunnel{
		mqt:         mqt,
		ctx:         ctx,
		conn:        NewStdioConnection(),
		tcpClosedCh: make(chan error),
	}
}

func (st *StdioTunnel) Start(remotePort int) error {
	log.Printf("[INFO] starting stdio tunnel remote_port=%d", remotePort)

	// Create a tunnel similar to how it's done for TCP connections
	t := st.mqt.conf.Topic
	topics := splitTopic(t)
	root := joinTopics(topics[:len(topics)-1])

	tun, err := NewTunnelFromConnect(st.ctx, st.mqt.mqttBroker, st.conn, root, 0, remotePort)
	if err != nil {
		return fmt.Errorf("NewTunnelFromConnect failed, %w", err)
	}
	st.tun = tun

	if err := tun.setupLocalTunnel(st.ctx); err != nil {
		return fmt.Errorf("setupLocalTunnel failed, %w", err)
	}
	if err := tun.openRequest(st.ctx); err != nil {
		return fmt.Errorf("OpenRequest failed, %w", err)
	}

	// Wait for the tunnel to be established
	select {
	case <-st.ctx.Done():
		return st.ctx.Err()
	case <-time.After(5 * time.Second):
		// Check if tunnel is connected
		st.mqt.mu.Lock()
		_, connected := st.mqt.connected[tun.ID]
		st.mqt.mu.Unlock()
		if !connected {
			return fmt.Errorf("tunnel connection timeout")
		}
	}

	return nil
}

func splitTopic(topic string) []string {
	var result []string
	start := 0
	for i, c := range topic {
		if c == '/' {
			if i > start {
				result = append(result, topic[start:i])
			}
			start = i + 1
		}
	}
	if start < len(topic) {
		result = append(result, topic[start:])
	}
	return result
}

func joinTopics(topics []string) string {
	result := ""
	for i, t := range topics {
		if i > 0 {
			result += "/"
		}
		result += t
	}
	return result
}
