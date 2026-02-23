package tunnel

import (
	"context"
	"fmt"
	"log"
	"net"
	"strings"
	"sync"
	"time"
)

const tunnelQoS = 0

// MQTunnel is a main component of mqtunnel.
type MQTunnel struct {
	conf       Config
	mqttBroker *mqttBroker

	controlCh chan controlPacket
	localCh   chan net.Conn

	ackWaiting map[string]*Tunnel
	connected  map[string]*Tunnel

	isLocal bool

	mu sync.Mutex
}

func NewMQTunnel(conf Config) (*MQTunnel, error) {
	ret := MQTunnel{
		conf: conf,

		controlCh: make(chan controlPacket),
		localCh:   make(chan net.Conn),

		ackWaiting: make(map[string]*Tunnel),
		connected:  make(map[string]*Tunnel),
	}

	mqBroker, err := NewMQTTBroker(conf, ret.controlCh)
	if err != nil {
		return nil, fmt.Errorf("MQTT connection error, %w", err)
	}
	ret.mqttBroker = mqBroker

	return &ret, nil
}

// StartStdio starts a MQTT tunnel using stdin/stdout instead of a listening port.
func (mqt *MQTunnel) StartStdio(ctx context.Context, remotePort int) error {
	go mqt.mqttBroker.start(ctx)
	mqt.isLocal = true

	// Create a stdio connection
	conn := NewStdioConnection()

	// Send the connection to localCh to initiate the tunnel
	go func() {
		mqt.localCh <- conn
	}()

	// Start the main loop
	for {
		select {
		case ctl := <-mqt.mqttBroker.controlCh:
			debugf("control type=%s ID=%s isLocal=%v", string(ctl.Type), ctl.TunnelID, mqt.isLocal)
			if err := mqt.handleControl(ctx, ctl); err != nil {
				log.Printf("[ERROR] handleControl failed error=%v", err)
			}
		case conn := <-mqt.localCh:
			// topics should be same level as control topic
			t := strings.Split(mqt.conf.Topic, "/")
			root := strings.Join(t[:len(t)-1], "/")

			tun, err := NewTunnelFromConnect(ctx, mqt.mqttBroker, conn, root, 0, remotePort)
			if err != nil {
				log.Printf("[ERROR] NewTunnelFromConnect failed error=%v", err)
				return fmt.Errorf("NewTunnelFromConnect failed, %w", err)
			}
			if err := tun.setupLocalTunnel(ctx); err != nil {
				log.Printf("[ERROR] setupLocalTunnel failed error=%v", err)
				return fmt.Errorf("setupLocalTunnel failed, %w", err)
			}
			if err := tun.openRequest(ctx); err != nil {
				log.Printf("[ERROR] OpenRequest failed error=%v", err)
				return fmt.Errorf("OpenRequest failed, %w", err)
			}

			mqt.ackWaiting[tun.ID] = tun

			// Start a goroutine to handle connection timeout
			go func(tunnelID string, conn net.Conn) {
				timeout := time.Duration(mqt.conf.ConnectionTimeout) * time.Second
				timer := time.NewTimer(timeout)
				defer timer.Stop()

				select {
				case <-timer.C:
					// Timeout occurred, check if tunnel is still waiting for ack
					mqt.mu.Lock()
					_, waiting := mqt.ackWaiting[tunnelID]
					mqt.mu.Unlock()

					if waiting {
						log.Printf("[ERROR] tunnel connection timeout tunnel_id=%s timeout=%v", tunnelID, timeout)

						// Cancel the tunnel
						mqt.mu.Lock()
						if tun, exists := mqt.ackWaiting[tunnelID]; exists {
							tun.cancel()
							delete(mqt.ackWaiting, tunnelID)
						}
						mqt.mu.Unlock()

						// Close the connection
						conn.Close()
					}
				case <-ctx.Done():
					// Context cancelled, timer will be stopped
				}
			}(tun.ID, conn)

		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// StartRemote starts a MQTT tunnel in remote mode.
// The remote mode waits for connection requests from the local side
// and connects to the local service specified by RemoteAddr in the config.
func (mqt *MQTunnel) StartRemote(ctx context.Context) error {
	go mqt.mqttBroker.start(ctx)
	mqt.isLocal = false

	// Start the main loop
	for {
		select {
		case ctl := <-mqt.mqttBroker.controlCh:
			debugf("control type=%s ID=%s isLocal=%v", string(ctl.Type), ctl.TunnelID, mqt.isLocal)
			if err := mqt.handleControl(ctx, ctl); err != nil {
				log.Printf("[ERROR] handleControl failed error=%v", err)
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (mqt *MQTunnel) handleControl(ctx context.Context, ctl controlPacket) error {

	switch ctl.Type {
	case controlTypeConnectRequest:
		if mqt.isLocal { // remote only
			return nil
		}
		tun, err := NewTunnelFromControl(ctx, mqt.mqttBroker, ctl)
		if err != nil {
			return fmt.Errorf("NewTunnelFromControl failed, %w", err)
		}
		if err := tun.setupRemoteTunnel(ctx); err != nil {
			return fmt.Errorf("setupRemoteTunnel failed, %w", err)
		}
		go tun.mainLoop(ctx)
	case controlTypeConnectAck:
		if !mqt.isLocal { // local only
			return nil
		}
		tun, exists := mqt.ackWaiting[ctl.TunnelID]
		if exists {
			go tun.mainLoop(ctx)
			mqt.mu.Lock()
			delete(mqt.ackWaiting, ctl.TunnelID)
			mqt.connected[ctl.TunnelID] = tun
			mqt.mu.Unlock()
		}
	case controlTypeConnectionClosed:
		tun, exists := mqt.connected[ctl.TunnelID]
		if exists {
			tun.cancel()
			delete(mqt.connected, ctl.TunnelID)
		}
	default:
		return fmt.Errorf("unknown control type, %s", ctl.Type)
	}
	return nil
}
