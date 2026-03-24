package tunnel

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"time"
)

const bufferSize = 4096
const tunnelKeepAlivePeriod = time.Second * 180

type TCPConnection struct {
	addr string

	conn        net.Conn
	isLocal     bool
	tunnel      *Tunnel
	tcpClosedCh chan error
}

func NewTCPConnection(addr string, tun *Tunnel) (*TCPConnection, error) {
	ret := TCPConnection{
		addr:        addr,
		tunnel:      tun,
		tcpClosedCh: tun.tcpClosedCh,
	}

	return &ret, nil
}

// connect connects to TCP address on server side.
func (con *TCPConnection) connect(ctx context.Context) (net.Conn, error) {
	debugf("start connecting addr=%s", con.addr)

	conn, err := net.Dial("tcp", con.addr)
	if err != nil {
		return nil, err
	}
	if tcpConn, ok := conn.(*net.TCPConn); ok {
		tcpConn.SetKeepAlive(true)
		tcpConn.SetKeepAlivePeriod(tunnelKeepAlivePeriod)
	}

	con.conn = conn
	debugf("connected addr=%s", con.addr)
	go con.handleRead(ctx)
	return conn, nil
}

// handleWrite writes to conn
func (con *TCPConnection) handleWrite(ctx context.Context, b []byte) (int, error) {
	debugf("handleWrite addr=%s size=%d", con.addr, len(b))

	if con.conn == nil {
		return 0, fmt.Errorf("write but not connected yet")
	}

	return con.conn.Write(b)
}

func (con *TCPConnection) handleRead(ctx context.Context) error {
	debugf("handleRead addr=%s", con.addr)

	defer con.conn.Close()
	for {
		buf := make([]byte, bufferSize)
		n, err := con.conn.Read(buf)
		if err != nil {
			if n > 0 {
				con.tunnel.publishCh <- buf[:n]
			}
			close(con.tunnel.publishCh)
			if err == io.EOF {
				return nil
			}
			log.Printf("[ERROR] tcp read error: %v", err)
			return err
		}
		debugf("handleReader size=%d", n)
		con.tunnel.publishCh <- buf[:n]
	}
}
