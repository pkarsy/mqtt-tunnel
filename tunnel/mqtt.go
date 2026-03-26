package tunnel

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

var (
	verboseLogging bool
)

// SetVerboseLogging enables or disables verbose (debug) logging
func SetVerboseLogging(enabled bool) {
	verboseLogging = enabled
}

// randInt returns a uniform random int in [0, max) using crypto/rand with rejection sampling
func randInt(max int) int {
	if max <= 0 || max > 256 {
		panic("randInt: max must be between 1 and 256")
	}
	// Find largest multiple of max that fits in 256 (byte range)
	// This minimizes rejection rate while maintaining uniform distribution
	limit := 256 - (256 % max)
	buf := make([]byte, 1)
	for {
		rand.Read(buf)
		n := int(buf[0])
		if n < limit {
			return n % max
		}
	}
}

func debugf(format string, args ...interface{}) {
	if verboseLogging {
		log.Printf("[DEBUG] "+format, args...)
	}
}

// GenerateRandomID generates a random alphanumeric ID of the specified length.
// The first character is always a letter (to be valid for MQTT client IDs).
// Uses all letters (a-z, A-Z) and digits (0-9) for maximum entropy.
func GenerateRandomID(length int) string {
	const letters = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
	const alphanum = letters + "0123456789"

	if length < 1 {
		length = 6
	}

	var result strings.Builder
	result.Grow(length)

	// First character: random letter (required for valid MQTT client IDs)
	result.WriteByte(letters[randInt(len(letters))])

	// Remaining characters: random alphanumeric
	for i := 1; i < length; i++ {
		result.WriteByte(alphanum[randInt(len(alphanum))])
	}

	return result.String()
}

type mqttBroker struct {
	client           mqtt.Client
	conf             Config
	clientID         string
	mqttDisconnectCh chan bool
	controlTopic     string
	tunnelTopics     map[string]*Tunnel // topic: tunnel
	isServerMode     bool               // true for server mode, false for client mode

	controlCh      chan controlPacket
	reconnectCh    chan bool     // signals that reconnection is needed (server mode)
	tunnelDoneCh   chan struct{} // signals when tunnel closes (client mode)
	tunnelClosedCh chan string   // signals when tunnel closes (server mode, sends tunnel ID)

	// References to MQTunnel maps for filtering
	connected       *map[string]*Tunnel
	ackWaiting      *map[string]*Tunnel
	pendingConfirms *map[string]*Tunnel
}

const mqttCommandsTimeout = 30 * time.Second
const topicQoS = 0

func NewMQTTBroker(conf Config, controlCh chan controlPacket, isServerMode bool) (*mqttBroker, error) {
	// Generate ClientID
	clientID := GenerateRandomID(6)

	ret := mqttBroker{
		conf:             conf,
		clientID:         clientID,
		mqttDisconnectCh: make(chan bool),
		tunnelTopics:     make(map[string]*Tunnel),
		isServerMode:     isServerMode,

		controlTopic: ControlTopic(conf.Topic),

		controlCh:      controlCh,
		reconnectCh:    make(chan bool),
		tunnelDoneCh:   make(chan struct{}, 1),  // Buffered to avoid deadlock in client exit
		tunnelClosedCh: make(chan string),

		connected:  nil,
		ackWaiting: nil,
	}

	opts, err := getMQTTOptions(conf, clientID)
	if err != nil {
		return nil, fmt.Errorf("failed to get MQTT options, %w", err)
	}

	// add callback
	opts.SetConnectionLostHandler(ret.onMqttConnectionLost)
	opts.SetOnConnectHandler(ret.onConnect)
	opts.SetReconnectingHandler(ret.onReconnect)

	// Set up MQTT logging to use standard log package
	mqtt.ERROR = log.New(os.Stderr, "[MQTT-ERROR] ", log.Ldate|log.Ltime)
	mqtt.CRITICAL = log.New(os.Stderr, "[MQTT-CRITICAL] ", log.Ldate|log.Ltime)

	// connect to MQTT Broker
	client := mqtt.NewClient(opts)
	ret.client = client

	// connect first time with retry logic
	ctx := context.Background()
	if err := ret.connectWithRetry(ctx); err != nil {
		return nil, fmt.Errorf("failed to connect broker, %w", err)
	}

	return &ret, nil
}

// SetTunnelMaps sets the connected, ackWaiting, and pendingConfirms map references for filtering
func (mqb *mqttBroker) SetTunnelMaps(connected, ackWaiting, pendingConfirms map[string]*Tunnel) {
	mqb.connected = &connected
	mqb.ackWaiting = &ackWaiting
	mqb.pendingConfirms = &pendingConfirms
}

func (mqb *mqttBroker) start(ctx context.Context) error {
	for {
		select {
		case <-mqb.mqttDisconnectCh:
			// Client mode: exit immediately (handled by onMqttConnectionLost calling os.Exit)
			// This case shouldn't be reached in client mode, but handle it anyway
			if !mqb.isServerMode {
				return fmt.Errorf("mqtt disconnected in client mode")
			}
			// Server mode: the reconnect will be handled by StartRemote via ReconnectCh
			// Just continue - this channel signal is for cleanup coordination
		case <-ctx.Done():
			log.Printf("[WARN] MQTTConnection finished, %v", ctx.Err())
			return ctx.Err()
		}
	}
}

// DisconnectCh returns the channel that signals when MQTT connection is lost
// and reconnection failed. Useful for handling roaming scenarios.
// ReconnectCh returns the channel that signals reconnection is needed (remote mode)
func (mqb *mqttBroker) ReconnectCh() <-chan bool {
	return mqb.reconnectCh
}

// Reconnect attempts to reconnect to the MQTT broker
func (mqb *mqttBroker) Reconnect(ctx context.Context) error {
	return mqb.connectWithRetry(ctx)
}

func (mqb *mqttBroker) publish(ctx context.Context, topic string, qos byte, retained bool, payload interface{}) mqtt.Token {
	debugf("mqtt publish topic=%s", topic)

	return mqb.client.Publish(topic, qos, retained, payload)
}

func (mqb *mqttBroker) connect() error {
	token := mqb.client.Connect()
	// Use connection timeout from config, default to 15 seconds if not set
	timeout := time.Duration(mqb.conf.ConnectionTimeout) * time.Second
	if timeout == 0 {
		timeout = 15 * time.Second
	}
	if c := token.WaitTimeout(timeout); !c {
		return fmt.Errorf("connect timed out")
	}
	return token.Error()
}

// connectWithRetry attempts to connect to the MQTT broker with retry logic
func (mqb *mqttBroker) connectWithRetry(ctx context.Context) error {
	const (
		maxRetries        = 10
		initialDelay      = 10 * time.Second
		maxDelay          = 60 * time.Second
		backoffMultiplier = 2.0
	)

	delay := initialDelay
	for attempt := 1; attempt <= maxRetries; attempt++ {
		log.Println("[INFO] attempting to connect to MQTT broker")
		/*,
		"attempt", attempt,
		"max_retries", maxRetries,
		"delay", delay) */

		err := mqb.connect()
		if err == nil {
			log.Printf("[INFO] successfully connected to MQTT broker, client_id=%s", mqb.clientID)
			return nil
		}

		log.Printf("[WARN] failed to connect to MQTT broker attempt=%d error=%v", attempt, err)

		// Check if context is cancelled
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
			// Continue to next attempt
		}

		// Exponential backoff with cap at maxDelay
		delay = time.Duration(float64(delay) * backoffMultiplier)
		if delay > maxDelay {
			delay = maxDelay
		}
	}

	return fmt.Errorf("failed to connect to MQTT broker after %d attempts", maxRetries)
}

// subscribeTunnelTopic subscribe topic
func (mqb *mqttBroker) subscribeTunnelTopic(topic string, tunnel *Tunnel) error {
	mqb.tunnelTopics[topic] = tunnel

	log.Printf("[INFO] subscribing to %s", topic)

	return mqb.subscribe()
}

func (mqb *mqttBroker) subscribe() error {
	topics := make(map[string]byte)

	if mqb.controlTopic != "" {
		topics[mqb.controlTopic] = 1
	}
	for t, _ := range mqb.tunnelTopics {
		topics[t] = topicQoS
	}

	if len(topics) == 0 {
		return nil
	}

	subscribeToken := mqb.client.SubscribeMultiple(topics, mqb.onMessage)
	if c := subscribeToken.WaitTimeout(mqttCommandsTimeout); !c {
		return fmt.Errorf("subscribe timed out")
	}
	return subscribeToken.Error()
}

func (mqb *mqttBroker) unsubscribe(topic string) error {
	if topic == "" {
		return nil
	}

	log.Printf("[INFO] topic unsubscribing topic=%s", topic)

	token := mqb.client.Unsubscribe(topic)
	if !token.WaitTimeout(mqttCommandsTimeout) {
		return fmt.Errorf("unsubscribe timeout (%s)", topic)
	}
	if token.Error() != nil {
		return token.Error()
	}
	delete(mqb.tunnelTopics, topic)

	return nil
}

func (mqb *mqttBroker) onMessage(client mqtt.Client, msg mqtt.Message) {
	debugf("on message topic=%s size=%d", msg.Topic(), len(msg.Payload()))

	if msg.Topic() == mqb.controlTopic {
		if err := mqb.controlPacketReceived(msg); err != nil {
			log.Printf("[ERROR] %v", err)
		}
		return
	}
	tun, exists := mqb.tunnelTopics[msg.Topic()]
	if !exists {
		debugf("requested topic is not exists topic=%s", msg.Topic())
		return
	}
	tun.writeCh <- msg.Payload()
}

func (mqb *mqttBroker) controlPacketReceived(msg mqtt.Message) error {
	var control controlPacket
	if err := json.Unmarshal(msg.Payload(), &control); err != nil {
		return fmt.Errorf("unmarshal error, %v", err)
	}

	// Filter based on origin and mode
	if mqb.isServerMode {
		// Validate control type for server mode
		switch control.Type {
		case controlTypeConnectRequest, controlTypeConnectConfirm, controlTypeConnectionClosed:
			// Valid server types, continue processing
		default:
			debugf("invalid type=%s for server, dropping", control.Type)
			return nil
		}
		// Server mode filtering
		if control.Origin == "server" {
			debugf("rejecting our(server) message tunnel_id=%s", control.TunnelID)
			return nil
		}
		// Validate connect_confirm is for a known pending tunnel
		if control.Type == controlTypeConnectConfirm {
			_, pendingExists := (*mqb.pendingConfirms)[control.TunnelID]
			if !pendingExists {
				debugf("unknown tunnel_id=%s, dropping", control.TunnelID)
				return nil
			}
		}
		// Validate connect request tunnel_id is not already in use
		if control.Type == controlTypeConnectRequest {
			_, connectedExists := (*mqb.connected)[control.TunnelID]
			_, pendingExists := (*mqb.pendingConfirms)[control.TunnelID]
			if connectedExists || pendingExists {
				debugf("tunnel_id already in use=%s, dropping", control.TunnelID)
				return nil
			}
		}
		// Validate connection_closed is for a known tunnel
		if control.Type == controlTypeConnectionClosed {
			_, connectedExists := (*mqb.connected)[control.TunnelID]
			_, pendingExists := (*mqb.pendingConfirms)[control.TunnelID]
			if !connectedExists && !pendingExists {
				debugf("connection_closed for unknown tunnel_id=%s, dropping", control.TunnelID)
				return nil
			}
		}
		// Protocol v3+: missing origin = drop
		// Exception: failure messages from older servers must be accepted
		if control.Origin == "" && control.Type != controlTypeFailure {
			debugf("origin missing, dropping tunnel_id=%s", control.TunnelID)
			return nil
		}
	} else {
		// Validate control type for client mode
		switch control.Type {
		case controlTypeConnectAck, controlTypeFailure, controlTypeConnectionClosed:
			// Valid client types, continue processing
		default:
			debugf("invalid type=%s for client, dropping", control.Type)
			return nil
		}
		// Client mode filtering
		if control.Origin == "client" {
			debugf("rejecting own message tunnel_id=%s", control.TunnelID)
			return nil
		}
		// Client only knows about its own tunnel ID from ackWaiting or connected
		_, connectedExists := (*mqb.connected)[control.TunnelID]
		_, ackWaitingExists := (*mqb.ackWaiting)[control.TunnelID]
		tunnelKnown := connectedExists || ackWaitingExists

		if !tunnelKnown {
			// Drop silently - not our tunnel
			return nil
		}
		// Protocol v3+: missing origin = drop
		// Exception: failure messages from older servers must be accepted
		if control.Origin == "" && control.Type != controlTypeFailure {
			debugf("origin missing, dropping tunnel_id=%s", control.TunnelID)
			return nil
		}
	}

	mqb.controlCh <- control
	return nil
}

func (mqb *mqttBroker) onConnect(client mqtt.Client) {
	// log.Println("[INFO] connected")
	log.Printf("[INFO] subscribing to %s", mqb.controlTopic)
	if err := mqb.subscribe(); err != nil {
		log.Printf("[ERROR] subscribe failed error=%v", err)
	}
}

func (mqb *mqttBroker) onReconnect(client mqtt.Client, opts *mqtt.ClientOptions) {
	log.Println("[INFO] reconnecting...")
}

func (mqb *mqttBroker) onMqttConnectionLost(client mqtt.Client, err error) {
	log.Printf("[ERROR] MQTT connection lost: %v", err)

	// Client mode: exit immediately, no retry, no cleanup needed
	if !mqb.isServerMode {
		// Use explicit \r\n for raw terminal mode (SSH leaves terminal in raw mode)
		fmt.Fprintf(os.Stderr, "[ERROR] client mode: exiting on disconnect\r\n")
		// Flush and give terminal time to process
		os.Stderr.Sync()
		time.Sleep(100 * time.Millisecond)
		os.Exit(1)
	}

	// Server mode: signal that reconnection is needed
	// The StartRemote loop will handle closing tunnels and reconnecting
	log.Println("[WARN] server mode: signalling reconnection needed")
	select {
	case mqb.reconnectCh <- true:
	default:
		// Already signaled
	}
}

func newTLSConfig(config Config) (*tls.Config, error) {
	tlsConfig := &tls.Config{
		InsecureSkipVerify: true,
		ClientAuth:         tls.NoClientCert,
		ClientCAs:          nil,
	}

	// Load CA certificate if provided
	if config.CaCert != "" {
		rootCA, err := os.ReadFile(config.CaCert)
		if err != nil {
			return nil, err
		}
		certpool := x509.NewCertPool()
		certpool.AppendCertsFromPEM(rootCA)
		tlsConfig.RootCAs = certpool
	}

	// Load client certificate if provided
	if config.ClientCert != "" && config.PrivateKey != "" {
		cert, err := tls.LoadX509KeyPair(config.ClientCert, config.PrivateKey)
		if err != nil {
			return nil, err
		}
		cert.Leaf, err = x509.ParseCertificate(cert.Certificate[0])
		if err != nil {
			return nil, err
		}
		tlsConfig.Certificates = []tls.Certificate{cert}
		tlsConfig.NextProtos = []string{"x-amzn-mqtt-ca"}
	}

	return tlsConfig, nil
}

func getMQTTOptions(conf Config, clientID string) (*mqtt.ClientOptions, error) {
	// Parse broker URL to extract broker information
	brokerInfo, err := ParseBrokerURL(conf.BrokerURL)
	if err != nil {
		return nil, fmt.Errorf("failed to parse broker URL: %w", err)
	}

	opts := mqtt.NewClientOptions()

	// Construct broker URL based on parsed information
	var brokerAddr string
	if brokerInfo.WebSocket {
		if brokerInfo.TLS {
			brokerAddr = fmt.Sprintf("wss://%s:%d/mqtt", brokerInfo.Host, brokerInfo.Port)
		} else {
			brokerAddr = fmt.Sprintf("ws://%s:%d/mqtt", brokerInfo.Host, brokerInfo.Port)
		}
	} else {
		if brokerInfo.TLS {
			brokerAddr = fmt.Sprintf("ssl://%s:%d", brokerInfo.Host, brokerInfo.Port)
			tlsConfig, err := newTLSConfig(conf)
			if err != nil {
				return nil, fmt.Errorf("failed to construct tls config, %v", err)
			}
			opts.SetTLSConfig(tlsConfig)
		} else {
			brokerAddr = fmt.Sprintf("tcp://%s:%d", brokerInfo.Host, brokerInfo.Port)
		}
	}
	opts.AddBroker(brokerAddr)
	opts.SetClientID(clientID)
	opts.SetCleanSession(true)
	opts.SetAutoReconnect(true) // Enable to avoid Paho bug, but we exit on disconnect in local mode
	opts.SetConnectRetryInterval(20 * time.Second)
	// MQTT keepalive interval
	keepalive := time.Duration(conf.MqttKeepalive) * time.Second
	if keepalive == 0 {
		keepalive = 60 * time.Second
	}
	opts.SetKeepAlive(keepalive)

	return opts, nil
}

// logTopic is a util function to log multiple topics
func logTopic(topics map[string]byte) []string {
	ret := make([]string, 0, len(topics))
	for k := range topics {
		ret = append(ret, k)
	}

	return ret
}
