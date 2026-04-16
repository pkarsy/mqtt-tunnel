package tunnel

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"strings"
	"sync"
	"time"
)

const tunnelQoS = 0

const confirmTimeout = 5 * time.Second

// MQTunnel is a main component of mqtunnel.
type MQTunnel struct {
	conf       Config
	mqttBroker *mqttBroker

	controlCh chan controlPacket
	localCh   chan net.Conn

	ackWaiting      map[string]*Tunnel
	pendingConfirms map[string]*Tunnel // tunnels waiting for connect_confirm
	connected       map[string]*Tunnel

	isServerMode bool // true if running in server mode, false if client mode

	reloadCh chan Config // channel for config reload (server mode only)

	mu sync.Mutex
}

func NewMQTunnel(conf Config, isServerMode bool, logOutput io.Writer) (*MQTunnel, error) {
	ret := MQTunnel{
		conf: conf,

		controlCh: make(chan controlPacket, 100), // Buffered to prevent deadlock during high message volume
		localCh:   make(chan net.Conn, 1), // buffered to prevent blocking on initial send

		ackWaiting:      make(map[string]*Tunnel),
		pendingConfirms: make(map[string]*Tunnel),
		connected:       make(map[string]*Tunnel),
		isServerMode:    isServerMode,
		reloadCh:        make(chan Config, 1), // Buffered to prevent deadlock during config reload
	}

	mqBroker, err := NewMQTTBroker(conf, ret.controlCh, isServerMode, logOutput)
	if err != nil {
		return nil, fmt.Errorf("MQTT connection error, %w", err)
	}
	ret.mqttBroker = mqBroker
	ret.mqttBroker.SetTunnelMaps(ret.connected, ret.ackWaiting, ret.pendingConfirms)

	return &ret, nil
}

// ReloadConfig triggers a config reload (server mode only)
func (mqt *MQTunnel) ReloadConfig(conf Config) {
	// Update debug logging setting
	SetVerboseLogging(conf.Debug)
	mqt.reloadCh <- conf
}

// StartStdio starts a MQTT tunnel using stdin/stdout instead of a listening port.
func (mqt *MQTunnel) StartStdio(ctx context.Context, remotePort int) error {
	go mqt.mqttBroker.start(ctx)

	// Create a stdio connection
	conn := NewStdioConnection()

	// Send the connection to localCh to initiate the tunnel (non-blocking due to buffer)
	mqt.localCh <- conn

	// Start the main loop
	// Note: In local mode, onMqttConnectionLost calls os.Exit(1) directly
	for {
		select {
		case ctl := <-mqt.mqttBroker.controlCh:
			debugf("control type=%s ID=%s isServerMode=%v", string(ctl.Type), ctl.TunnelID, mqt.isServerMode)
			if err := mqt.handleControl(ctx, ctl); err != nil {
				log.Printf("[ERROR] handleControl failed error=%v", err)
			}

		case <-mqt.mqttBroker.tunnelDoneCh:
			mqt.mqttBroker.client.Disconnect(250)
			return nil

		case <-mqt.localCh:
			tun, err := NewTunnelFromConnect(ctx, mqt.mqttBroker, conn, 0, remotePort)
			if err != nil {
				log.Printf("[ERROR] NewTunnelFromConnect failed error=%v", err)
				return fmt.Errorf("NewTunnelFromConnect failed, %w", err)
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

						// Exit the program in client mode - no tunnel means nothing to do
						os.Exit(1)
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

// StartRemote starts a MQTT tunnel in server mode.
// The server mode waits for connection requests from the client side
// and connects to the local service specified by ServerAddr in the config.
func (mqt *MQTunnel) StartRemote(ctx context.Context) error {
	go mqt.mqttBroker.start(ctx)

	// Start the main loop
	for {
		select {
		case ctl := <-mqt.mqttBroker.controlCh:
			debugf("control type=%s ID=%s isServerMode=%v", string(ctl.Type), ctl.TunnelID, mqt.isServerMode)
			if err := mqt.handleControl(ctx, ctl); err != nil {
				log.Printf("[ERROR] handleControl failed error=%v", err)
			}
		case <-mqt.mqttBroker.ReconnectCh():
			// MQTT disconnected in remote mode - close all tunnels and reconnect
			log.Println("[WARN] MQTT connection lost, closing all tunnels and reconnecting")
			mqt.closeAllTunnels()

			// Attempt to reconnect to MQTT broker
			//debugf("attempting to reconnect to MQTT broker")
			if err := mqt.mqttBroker.Reconnect(ctx); err != nil {
				//log.Printf("[ERROR] failed to reconnect to MQTT broker error=%v", err)
				return fmt.Errorf("failed to reconnect: %w", err)
			}
			//debugf("successfully reconnected to MQTT broker")

		case tunnelID := <-mqt.mqttBroker.tunnelClosedCh:
			// Tunnel closed (server's TCP to SSH dropped) - cleanup connected map
			mqt.mu.Lock()
			if _, exists := mqt.connected[tunnelID]; exists {
				log.Printf("[INFO] tunnel closed, removing from connected tunnel_id=%s", tunnelID)
				delete(mqt.connected, tunnelID)
			}
			mqt.mu.Unlock()

		case newConf := <-mqt.reloadCh:
			// Config reload requested - close all tunnels and reconnect with new config
			log.Println("[INFO] Reloading config file, closing all tunnels and reconnecting")
			mqt.closeAllTunnels()

			// Disconnect from MQTT broker
			mqt.mqttBroker.client.Disconnect(250)

			// Update config
			mqt.conf = newConf

			// Create new MQTT broker with new config
			mqBroker, err := NewMQTTBroker(mqt.conf, mqt.controlCh, mqt.isServerMode, nil)
			if err != nil {
				log.Printf("[ERROR] Failed to create new MQTT broker after config reload: %v", err)
				return fmt.Errorf("failed to create MQTT broker after reload: %w", err)
			}
			mqt.mqttBroker = mqBroker
			mqt.mqttBroker.SetTunnelMaps(mqt.connected, mqt.ackWaiting, mqt.pendingConfirms)

			// Start the new MQTT broker
			go mqt.mqttBroker.start(ctx)

			log.Println("[INFO] Config file reloaded successfully")

		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// closeAllTunnels closes all active tunnels
func (mqt *MQTunnel) closeAllTunnels() {
	mqt.mu.Lock()
	defer mqt.mu.Unlock()

	for id, tun := range mqt.connected {
		log.Printf("[INFO] closing tunnel tunnel_id=%s", id)
		tun.cancel()
		delete(mqt.connected, id)
	}
	for id, tun := range mqt.ackWaiting {
		log.Printf("[INFO] closing pending tunnel tunnel_id=%s", id)
		tun.cancel()
		delete(mqt.ackWaiting, id)
	}
	for id, tun := range mqt.pendingConfirms {
		log.Printf("[INFO] closing pending confirm tunnel tunnel_id=%s", id)
		tun.cancel()
		mqt.mqttBroker.unsubscribe(tun.ServerPubTopic)
		delete(mqt.pendingConfirms, id)
	}
}
func (mqt *MQTunnel) handleControl(ctx context.Context, ctl controlPacket) error {

	switch ctl.Type {
	case controlTypeConnectRequest:
		if !mqt.isServerMode { // server only, skip on client
			return nil
		}
		// Validate tunnel ID format (1-6 alphanumeric characters)
		if err := validateTunnelID(ctl.TunnelID); err != nil {
			log.Printf("[ERROR] invalid tunnel id: %v", err)
			failure := createFailure(ctl.TunnelID, FailureReasonInvalidID)
			buf, _ := json.Marshal(failure)
			mqt.mqttBroker.publish(ctx, mqt.mqttBroker.controlTopic, 1, false, buf)
			return nil
		}
		// Check if tunnel ID is already in use
		mqt.mu.Lock()
		_, inPending := mqt.pendingConfirms[ctl.TunnelID]
		_, inConnected := mqt.connected[ctl.TunnelID]
		mqt.mu.Unlock()
		if inPending || inConnected {
			log.Printf("[ERROR] tunnel id already in use: %s", ctl.TunnelID)
			failure := createFailure(ctl.TunnelID, FailureReasonIDInUse)
			buf, _ := json.Marshal(failure)
			mqt.mqttBroker.publish(ctx, mqt.mqttBroker.controlTopic, 1, false, buf)
			return nil
		}
		// Check protocol version
		if ctl.Version != ProtocolVersion {
			log.Printf("[ERROR] protocol version mismatch: got %d, want %d", ctl.Version, ProtocolVersion)
			failure := createFailure(ctl.TunnelID, fmt.Sprintf("protocol version mismatch: got %d, want %d", ctl.Version, ProtocolVersion))
			buf, _ := json.Marshal(failure)
			mqt.mqttBroker.publish(ctx, mqt.mqttBroker.controlTopic, 1, false, buf)
			return nil
		}
		tun, err := NewTunnelFromControl(ctx, mqt.mqttBroker, ctl)
		if err != nil {
			return fmt.Errorf("NewTunnelFromControl failed, %w", err)
		}
		if err := tun.setupRemoteTunnel(ctx); err != nil {
			return fmt.Errorf("setupRemoteTunnel failed, %w", err)
		}
		// Store in pendingConfirms - wait for connect_confirm before starting
		mqt.mu.Lock()
		mqt.pendingConfirms[tun.ID] = tun
		mqt.mu.Unlock()
		// Start timeout for connect_confirm
		go func(tunnelID string) {
			timer := time.NewTimer(confirmTimeout)
			defer timer.Stop()
			<-timer.C
			mqt.mu.Lock()
			if tun, exists := mqt.pendingConfirms[tunnelID]; exists {
				log.Printf("[ERROR] connect_confirm timeout tunnel_id=%s", tunnelID)
				tun.cancel()
				mqt.mqttBroker.unsubscribe(tun.ServerPubTopic)
				delete(mqt.pendingConfirms, tunnelID)
			}
			mqt.mu.Unlock()
		}(tun.ID)
	case controlTypeConnectAck:
		if mqt.isServerMode { // server only, skip on client
			return nil
		}
		tun, exists := mqt.ackWaiting[ctl.TunnelID]
		if exists {
			log.Printf("[INFO] ack received: tunnel_id=%s client_pub_topic=%s server_pub_topic=%s",
				ctl.TunnelID, ctl.ClientPubTopic, ctl.ServerPubTopic)
			log.Printf("[INFO] publishing to %s", ctl.ClientPubTopic)
			tun.ClientPubTopic = ctl.ClientPubTopic
			tun.ServerPubTopic = ctl.ServerPubTopic
			if err := tun.setupLocalTunnel(ctx); err != nil {
				log.Printf("[ERROR] setupLocalTunnel failed error=%v", err)
				return fmt.Errorf("setupLocalTunnel failed, %w", err)
			}
			// Send connect_confirm
			confirm := tun.createConnectConfirm()
			buf, _ := json.Marshal(confirm)
			token := mqt.mqttBroker.publish(ctx, mqt.mqttBroker.controlTopic, 1, false, buf)
			token.Wait()
			go tun.mainLoop(ctx)
			mqt.mu.Lock()
			delete(mqt.ackWaiting, ctl.TunnelID)
			mqt.connected[ctl.TunnelID] = tun
			mqt.mu.Unlock()
		}
	case controlTypeConnectConfirm:
		if !mqt.isServerMode { // server only, skip on client
			return nil
		}
		mqt.mu.Lock()
		tun, exists := mqt.pendingConfirms[ctl.TunnelID]
		if exists {
			delete(mqt.pendingConfirms, ctl.TunnelID)
		}
		mqt.mu.Unlock()
		if exists {
			log.Printf("[INFO] connect_confirm received tunnel_id=%s", ctl.TunnelID)
			if err := tun.StartConfirmedTunnel(ctx); err != nil {
				log.Printf("[ERROR] StartConfirmedTunnel failed: %v", err)
				return fmt.Errorf("StartConfirmedTunnel failed: %w", err)
			}
			mqt.mu.Lock()
			mqt.connected[ctl.TunnelID] = tun
			mqt.mu.Unlock()
		}
	case controlTypeFailure:
		if mqt.isServerMode { // client only, skip on server
			return nil
		}
		log.Printf("[ERROR] server reported failure: tunnel_id=%s reason=%s", ctl.TunnelID, ctl.Reason)
		// Add hint for protocol version mismatch
		if strings.Contains(ctl.Reason, "protocol version mismatch") {
			log.Printf("[HINT] Server is running an older version. Please upgrade the server to 0.6.0+")
			log.Printf("[HINT] Or temporarily use mqtt-tunnel 0.5.0 on client side")
		}
		tun, exists := mqt.ackWaiting[ctl.TunnelID]
		if exists {
			delete(mqt.ackWaiting, ctl.TunnelID)
			tun.cancel()
		}
		// Exit immediately on fatal errors that prevent connection
		if ctl.Reason == FailureReasonInvalidID || ctl.Reason == FailureReasonIDInUse {
			os.Exit(1)
		}
	case controlTypeConnectionClosed:
		tun, exists := mqt.connected[ctl.TunnelID]
		if exists {
			tun.closedByRemote = true // Mark that remote initiated close
			tun.cancel()
			delete(mqt.connected, ctl.TunnelID)
			// Explicitly close TCP connection to unblock handleRead and cleanup
			if tun.tcpConnection != nil && tun.tcpConnection.conn != nil {
				tun.tcpConnection.conn.Close()
			}
		}
		// Client mode: ALWAYS exit when server signals tunnel closed
		if !mqt.isServerMode {
			debugf("sending tunnelDoneCh signal from handleControl")
			if !safeSendWithDebug(mqt.mqttBroker.tunnelDoneCh, struct{}{}, "tunnelDoneCh") {
				log.Printf("[WARN] Failed to signal tunnel done from handleControl - channel full")
			}
		}
	default:
		return fmt.Errorf("unknown control type, %s", ctl.Type)
	}
	return nil
}
