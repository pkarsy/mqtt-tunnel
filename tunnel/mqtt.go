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

	controlCh chan controlPacket
}

const mqttCommandsTimeout = 30 * time.Second
const topicQoS = 0

func NewMQTTBroker(conf Config, controlCh chan controlPacket) (*mqttBroker, error) {
	// Generate ClientID
	clientID := GenerateRandomID(6)

	ret := mqttBroker{
		conf:             conf,
		clientID:         clientID,
		mqttDisconnectCh: make(chan bool),
		tunnelTopics:     make(map[string]*Tunnel),

		controlTopic: conf.Topic,

		controlCh: controlCh,
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

func (mqb *mqttBroker) start(ctx context.Context) error {
	for {
		select {
		case <-mqb.mqttDisconnectCh:
			log.Println("[ERROR] mqtt disconnect message. try to reconnect")
			// do nothing. auto-reconnect should work
		case <-ctx.Done():
			log.Printf("[WARN] MQTTConnection finished, %v", ctx.Err())
			return ctx.Err()
		}
	}
}

func (mqb *mqttBroker) publish(ctx context.Context, topic string, qos byte, retained bool, payload interface{}) mqtt.Token {
	debugf("mqtt publish topic=%s", topic)

	return mqb.client.Publish(topic, qos, retained, payload)
}

func (mqb *mqttBroker) connect() error {
	debugf("connect start")
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
		maxRetries      = 10
		initialDelay    = 10 * time.Second
		maxDelay        = 60 * time.Second
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

	log.Printf("[INFO] topic subscribing topic=%s", strings.Join(logTopic(topics), ", "))

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

	debugf("topic unsubscribing topic=%s", topic)

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

	if msg.Topic() == mqb.conf.Topic {
		if err := mqb.controlPacketReceived(msg); err != nil {
			log.Printf("[ERROR] %v", err)
		}
		return
	}
	tun, exists := mqb.tunnelTopics[msg.Topic()]
	if !exists {
		log.Printf("[ERROR] requested topic is not exists topic=%s", msg.Topic())
		return
	}
	tun.writeCh <- msg.Payload()
}

func (mqb *mqttBroker) controlPacketReceived(msg mqtt.Message) error {
	var control controlPacket
	if err := json.Unmarshal(msg.Payload(), &control); err != nil {
		return fmt.Errorf("unmarshal error, %v", err)
	}
	mqb.controlCh <- control
	return nil
}

func (mqb *mqttBroker) onConnect(client mqtt.Client) {
	// log.Println("[INFO] connected")
	if err := mqb.subscribe(); err != nil {
		log.Printf("[ERROR] subscribe failed error=%v", err)
	}
}

func (mqb *mqttBroker) onReconnect(client mqtt.Client, opts *mqtt.ClientOptions) {
	log.Println("[INFO] reconnecting...")
}

func (mqb *mqttBroker) onMqttConnectionLost(client mqtt.Client, err error) {
	log.Printf("[ERROR] MQTT connection lost: %v", err)

	// Attempt to reconnect with retry logic
	ctx := context.Background()
	if err := mqb.connectWithRetry(ctx); err != nil {
		log.Printf("[ERROR] failed to reconnect to MQTT broker error=%v", err)
		mqb.mqttDisconnectCh <- true
	} else {
		log.Println("[INFO] successfully reconnected to MQTT broker")
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
	opts.SetAutoReconnect(true)
	opts.SetConnectRetryInterval(20 * time.Second)

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
