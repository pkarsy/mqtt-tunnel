package tunnel

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"strconv"
)

type Tunnel struct {
	ID          string
	LocalPort   int
	LocalTopic  string
	RemotePort  int
	RemoteTopic string

	tcpConnection *TCPConnection
	mqttBroker    *mqttBroker

	ctx    context.Context
	cancel context.CancelFunc

	writeCh     chan []byte // writeCh writes a payload from MQTT Broker to local connection
	publishCh   chan []byte // publishCh publish a payload to MQTT Broker
	tcpClosedCh chan error
}

// NewTunnelFromConnect creates a new Tunnel on local
func NewTunnelFromConnect(ctx context.Context, mqttBroker *mqttBroker, conn net.Conn, topicRoot string, localPort, remotePort int) (*Tunnel, error) {
	ret := &Tunnel{
		ID:          randStr(16),
		LocalPort:   localPort,
		LocalTopic:  fmt.Sprintf("%s/%d-%s", topicRoot, localPort, randStr(8)),
		RemotePort:  remotePort,
		RemoteTopic: fmt.Sprintf("%s/%d-%s", topicRoot, remotePort, randStr(8)),

		writeCh:     make(chan []byte, bufferSize*10),
		publishCh:   make(chan []byte, bufferSize*10),
		tcpClosedCh: make(chan error),
		mqttBroker:  mqttBroker,
	}

	// For local side, we use the connection directly (stdio mode)
	tcon, err := NewTCPConnection("", ret)
	if err != nil {
		return nil, fmt.Errorf("new tcp connection error, %w", err)
	}
	tcon.conn = conn
	ret.tcpConnection = tcon
	go tcon.handleRead(ctx)

	return ret, nil
}

// NewTunnelFromControl creates a new Tunnel on remote side.
func NewTunnelFromControl(ctx context.Context, mqttBroker *mqttBroker, ctl controlPacket) (*Tunnel, error) {

	// create a new child context
	ctx, cancel := context.WithCancel(ctx)

	// Parse remote address to extract port for topic naming
	remoteAddr := mqttBroker.conf.RemoteAddr
	if remoteAddr == "" {
		remoteAddr = "127.0.0.1:22" // default
	}
	
	// Extract port from address for topic naming
	_, portStr, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return nil, fmt.Errorf("invalid remote address %s: %w", remoteAddr, err)
	}
	localPort, _ := strconv.Atoi(portStr)

	ret := Tunnel{
		ID: ctl.TunnelID,

		ctx:    ctx,
		cancel: cancel,

		// Use the port from RemoteAddr for topic naming
		LocalPort:   localPort,
		LocalTopic:  ctl.RemoteTopic,
		RemotePort:  ctl.LocalPort,
		RemoteTopic: ctl.LocalTopic,

		writeCh:     make(chan []byte, bufferSize*10),
		publishCh:   make(chan []byte, bufferSize*10),
		tcpClosedCh: make(chan error),
		mqttBroker:  mqttBroker,
	}

	// Use full remote address for TCP connection
	tcon, err := NewTCPConnection(remoteAddr, &ret)
	if err != nil {
		return nil, fmt.Errorf("new tcp connection error, %w", err)
	}
	ret.tcpConnection = tcon

	return &ret, nil
}

// setupLocalTunnel opens
func (tun *Tunnel) setupLocalTunnel(ctx context.Context) error {
	if err := tun.mqttBroker.subscribeTunnelTopic(tun.RemoteTopic, tun); err != nil {
		return fmt.Errorf("broker open error, %w", err)
	}
	return nil
}

// setupRemoteTunnel opens on remote
func (tun *Tunnel) setupRemoteTunnel(ctx context.Context) error {
	if err := tun.mqttBroker.subscribeTunnelTopic(tun.RemoteTopic, tun); err != nil {
		return fmt.Errorf("broker open error, %w", err)
	}

	if _, err := tun.tcpConnection.connect(ctx); err != nil {
		return fmt.Errorf("tcp connection error, %w", err)
	}

	ack, _ := json.Marshal(tun.createAck())

	token := tun.mqttBroker.publish(ctx, tun.mqttBroker.controlTopic, 1, false, ack)
	token.Wait()

	return token.Error()
}

// openRequest sends a control packet to remote side
func (tun *Tunnel) openRequest(ctx context.Context) error {

	ctl := tun.createConnectRequest()
	buf, _ := json.Marshal(ctl)
	token := tun.mqttBroker.publish(ctx, tun.mqttBroker.controlTopic, 1, false, buf)
	token.Wait()
	if err := token.Error(); err != nil {
		return fmt.Errorf("publish control msg error, %w", err)
	}

	return nil
}

func (tun *Tunnel) mainLoop(ctx context.Context) {
	defer tun.mqttBroker.unsubscribe(tun.RemoteTopic)

	// Infow("start MainLoop", "ID", tun.ID)
	for {
		select {
		case b := <-tun.writeCh:
			debugf("writeCh remote_topic=%s size=%d", tun.RemoteTopic, len(b))
			_, err := tun.tcpConnection.handleWrite(ctx, b)
			if err != nil {
				log.Printf("[ERROR] %v", err)
			}
		case b, ok := <-tun.publishCh:
			if !ok {
				c := tun.createConnectionClosed()
				debugf("connection closed remote_topic=%s size=%d", tun.LocalTopic, len(b))
				tun.mqttBroker.publish(ctx, tun.mqttBroker.controlTopic, 0, false, c)
				if len(b) > 0 {
					// send last bytes
					tun.mqttBroker.publish(ctx, tun.LocalTopic, 0, false, b)
				}
				return
			}
			debugf("publishCh remote_topic=%s size=%d", tun.LocalTopic, len(b))
			tun.mqttBroker.publish(ctx, tun.LocalTopic, 0, false, b)
		case <-ctx.Done():
			return
		}
	}
}

func (tun *Tunnel) createConnectRequest() controlPacket {
	ret := controlPacket{
		Type:        controlTypeConnectRequest,
		TunnelID:    tun.ID,
		LocalPort:   tun.LocalPort,
		LocalTopic:  tun.LocalTopic,
		RemotePort:  tun.RemotePort,
		RemoteTopic: tun.RemoteTopic,
	}
	return ret
}
func (tun *Tunnel) createAck() controlPacket {
	ret := controlPacket{
		Type:     controlTypeConnectAck,
		TunnelID: tun.ID,
	}
	return ret
}
func (tun *Tunnel) createConnectionClosed() controlPacket {
	ret := controlPacket{
		Type:     controlTypeConnectionClosed,
		TunnelID: tun.ID,
	}
	return ret
}
