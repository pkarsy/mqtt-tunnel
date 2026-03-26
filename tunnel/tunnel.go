package tunnel

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"strconv"
	//"time"
)

type Tunnel struct {
	ID             string
	LocalPort      int
	ClientPubTopic string // topic where client publishes data
	RemotePort     int
	ServerPubTopic string // topic where server publishes data
	Confirmed      bool   // true when client has sent connect_confirm
	closedByRemote bool   // true if close was initiated by remote side

	tcpConnection *TCPConnection
	mqttBroker    *mqttBroker

	ctx    context.Context
	cancel context.CancelFunc

	writeCh     chan []byte // writeCh writes a payload from MQTT Broker to local connection
	publishCh   chan []byte // publishCh publish a payload to MQTT Broker
	tcpClosedCh chan error
}

// NewTunnelFromConnect creates a new Tunnel on local
func NewTunnelFromConnect(ctx context.Context, mqttBroker *mqttBroker, conn net.Conn, localPort, remotePort int) (*Tunnel, error) {
	ctx, cancel := context.WithCancel(ctx)

	ret := &Tunnel{
		ID:         randStr(6),
		LocalPort:  localPort,
		RemotePort: remotePort,

		ctx:    ctx,
		cancel: cancel,

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

	// Parse server address to extract port for topic naming
	serverAddr := mqttBroker.conf.ServerAddr

	// Extract port from address for topic naming
	_, portStr, err := net.SplitHostPort(serverAddr)
	if err != nil {
		return nil, fmt.Errorf("invalid server address %s: %w", serverAddr, err)
	}
	localPort, _ := strconv.Atoi(portStr)

	// Use data topic root from base topic
	rootTopic := DataTopicRoot(mqttBroker.conf.Topic)

	clientPubTopic := fmt.Sprintf("%s/%s", rootTopic, randStr(4))
	serverPubTopic := fmt.Sprintf("%s/%s", rootTopic, randStr(4))

	ret := Tunnel{
		ID: ctl.TunnelID,

		ctx:    ctx,
		cancel: cancel,

		// Use the port from ServerAddr for topic naming
		LocalPort:      localPort,
		ClientPubTopic: clientPubTopic,
		RemotePort:     ctl.LocalPort,
		ServerPubTopic: serverPubTopic,

		writeCh:     make(chan []byte, bufferSize*10),
		publishCh:   make(chan []byte, bufferSize*10),
		tcpClosedCh: make(chan error),
		mqttBroker:  mqttBroker,
	}

	// Use full server address for TCP connection
	tcon, err := NewTCPConnection(serverAddr, &ret)
	if err != nil {
		return nil, fmt.Errorf("new tcp connection error, %w", err)
	}
	ret.tcpConnection = tcon

	return &ret, nil
}

// setupLocalTunnel opens
func (tun *Tunnel) setupLocalTunnel(ctx context.Context) error {
	if err := tun.mqttBroker.subscribeTunnelTopic(tun.ServerPubTopic, tun); err != nil {
		return fmt.Errorf("broker open error, %w", err)
	}
	return nil
}

// setupRemoteTunnel opens on remote - waits for confirm before starting TCP connection
func (tun *Tunnel) setupRemoteTunnel(ctx context.Context) error {
	if err := tun.mqttBroker.subscribeTunnelTopic(tun.ServerPubTopic, tun); err != nil {
		return fmt.Errorf("broker open error, %w", err)
	}

	ack, _ := json.Marshal(tun.createAck())
	token := tun.mqttBroker.publish(ctx, tun.mqttBroker.controlTopic, 1, false, ack)
	token.Wait()

	return token.Error()
}

// StartConfirmedTunnel is called after client sends connect_confirm
// It connects to the target and starts the data loop
func (tun *Tunnel) StartConfirmedTunnel(ctx context.Context) error {
	tun.Confirmed = true

	if _, err := tun.tcpConnection.connect(ctx); err != nil {
		return fmt.Errorf("tcp connection error, %w", err)
	}

	go tun.mainLoop(ctx)
	return nil
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
	defer tun.mqttBroker.unsubscribe(tun.ServerPubTopic)

	// Infow("start MainLoop", "ID", tun.ID)
	for {
		select {
		case b := <-tun.writeCh:
			debugf("writeCh server_pub_topic=%s size=%d", tun.ServerPubTopic, len(b))
			_, err := tun.tcpConnection.handleWrite(ctx, b)
			if err != nil {
				log.Printf("[ERROR] %v", err)
			}
		case b, ok := <-tun.publishCh:
			if !ok {
				// Only send connection_closed if remote didn't already initiate close
				if !tun.closedByRemote {
					debugf("publishCh closed - sending connection_closed")
					c := tun.createConnectionClosed()
					buf, _ := json.Marshal(c)
					debugf("connection closed client_pub_topic=%s size=%d", tun.ClientPubTopic, len(buf))
					token := tun.mqttBroker.publish(ctx, tun.mqttBroker.controlTopic, 0, false, buf)
					token.Wait()
					if token.Error() != nil {
						debugf("connection_closed publish FAILED: %v", token.Error())
					} else {
						debugf("connection_closed published successfully")
					}
					if len(b) > 0 {
						// send last bytes
						token = tun.mqttBroker.publish(ctx, tun.ClientPubTopic, 0, false, b)
						token.Wait()
					}
				}
				// Signal exit for client mode (bypasses MQTT)
				if !tun.mqttBroker.isServerMode {
					tun.mqttBroker.tunnelDoneCh <- struct{}{}
				}
				// Signal cleanup for server mode (sends tunnel ID)
				if tun.mqttBroker.isServerMode {
					debugf("sending tunnelClosedCh signal")
					tun.mqttBroker.tunnelClosedCh <- tun.ID
				}
				return
			}
			debugf("publishCh client_pub_topic=%s size=%d", tun.ClientPubTopic, len(b))
			tun.mqttBroker.publish(ctx, tun.ClientPubTopic, 0, false, b)
		case <-ctx.Done():
			return
		}
	}
}

func (tun *Tunnel) createConnectRequest() controlPacket {
	ret := controlPacket{
		Type:       controlTypeConnectRequest,
		TunnelID:   tun.ID,
		Version:    ProtocolVersion,
		LocalPort:  tun.LocalPort,
		RemotePort: tun.RemotePort,
		Origin:     "client",
	}
	return ret
}
func (tun *Tunnel) createAck() controlPacket {
	ret := controlPacket{
		Type:           controlTypeConnectAck,
		TunnelID:       tun.ID,
		ClientPubTopic: tun.ServerPubTopic,
		ServerPubTopic: tun.ClientPubTopic,
		Origin:         "server",
	}
	return ret
}
func (tun *Tunnel) createConnectionClosed() controlPacket {
	origin := "client"
	if tun.mqttBroker.isServerMode {
		origin = "server"
	}
	ret := controlPacket{
		Type:     controlTypeConnectionClosed,
		TunnelID: tun.ID,
		Origin:   origin,
	}
	return ret
}

func (tun *Tunnel) createConnectConfirm() controlPacket {
	ret := controlPacket{
		Type:     controlTypeConnectConfirm,
		TunnelID: tun.ID,
		Origin:   "client",
	}
	return ret
}

func createFailure(tunnelID, reason string) controlPacket {
	ret := controlPacket{
		Type:     controlTypeFailure,
		TunnelID: tunnelID,
		Reason:   reason,
		Origin:   "server",
	}
	return ret
}
