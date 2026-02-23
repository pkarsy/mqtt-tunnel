// Copyright 2026 Panagiotis Karagiannis
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//
// Portions of this file were derived from mqtunnel (https://github.com/shirou/mqtunnel)
// which is also licensed under the Apache License 2.0.

package tunnel

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	BrokerURL  string `json:"broker"`
	Username   string `json:"username"`
	Password   string `json:"password"`

	CaCert     string `json:"caCert"`
	ClientCert string `json:"clientCert"`
	PrivateKey string `json:"privateKey"`

	Topic      string `json:"topic"`
	RemoteAddr string `json:"remoteAddr"`

	// ConnectionTimeout is the maximum time to wait for tunnel establishment (in seconds)
	// Default: 15 seconds
	ConnectionTimeout int `json:"connectionTimeout"`
}

func ReadConfig(filePath string) (Config, error) {
	var ret Config

	buf, err := os.ReadFile(filePath)
	if err != nil {
		return ret, fmt.Errorf("read config error, %w", err)
	}

	if err := json.Unmarshal(buf, &ret); err != nil {
		return ret, fmt.Errorf("read config marshal error, %w", err)
	}
	return ret, nil
}

// BrokerInfo contains parsed information from the broker URL
type BrokerInfo struct {
	Host     string
	Port     int
	TLS      bool
	WebSocket bool
}

// ParseBrokerURL parses the broker URL and extracts broker information
// Supported URL formats:
// - mqtt://host:port (MQTT over TCP, no TLS)
// - mqtts://host:port (MQTT over TLS)
// - ws://host:port (MQTT over WebSocket, no TLS)
// - wss://host:port (MQTT over Secure WebSocket, with TLS)
func ParseBrokerURL(brokerURL string) (*BrokerInfo, error) {
	if brokerURL == "" {
		return nil, fmt.Errorf("broker URL is required")
	}

	// Parse URL
	u, err := url.Parse(brokerURL)
	if err != nil {
		return nil, fmt.Errorf("invalid broker URL: %w", err)
	}

	// Validate scheme
	scheme := strings.ToLower(u.Scheme)
	var tls, webSocket bool
	var defaultPort int

	switch scheme {
	case "mqtt":
		defaultPort = 1883
		tls = false
		webSocket = false
	case "mqtts":
		defaultPort = 8883
		tls = true
		webSocket = false
	case "ws":
		defaultPort = 80
		tls = false
		webSocket = true
	case "wss":
		defaultPort = 443
		tls = true
		webSocket = true
	default:
		return nil, fmt.Errorf("unsupported broker URL scheme: %s (supported: mqtt, mqtts, ws, wss)", scheme)
	}

	// Extract host
	host := u.Hostname()
	if host == "" {
		return nil, fmt.Errorf("broker host is required in URL")
	}

	// Extract port or use default
	port := defaultPort
	if u.Port() != "" {
		port, err = strconv.Atoi(u.Port())
		if err != nil {
			return nil, fmt.Errorf("invalid broker port: %w", err)
		}
	}

	// Validate port
	if port < 1 || port > 65535 {
		return nil, fmt.Errorf("broker port must be between 1 and 65535")
	}

	return &BrokerInfo{
		Host:      host,
		Port:      port,
		TLS:       tls,
		WebSocket: webSocket,
	}, nil
}
