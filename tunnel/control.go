package tunnel

import (
	"encoding/base64"
	"math/rand"
	"time"
)

type controlType string

const (
	controlTypeConnectRequest   controlType = "connect"
	controlTypeConnectAck       controlType = "connect_ack"
	controlTypeConnectConfirm   controlType = "connect_confirm"
	controlTypeFailure          controlType = "failure"
	controlTypeConnectionClosed controlType = "closed"
)

const ProtocolVersion = 2

type controlPacket struct {
	Type     controlType `json:"type"`
	TunnelID string      `json:"tunnel_id,omitempty"`
	Version  int         `json:"version,omitempty"`

	LocalPort      int    `json:"local_port,omitempty"`
	ClientPubTopic string `json:"client_pub_topic,omitempty"`
	RemotePort     int    `json:"remote_port,omitempty"`
	ServerPubTopic string `json:"server_pub_topic,omitempty"`

	RootTopic string `json:"root_topic,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

const randomLetters = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"

func randStr(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = randomLetters[rand.Intn(len(randomLetters))]
	}
	return string(b)
}

func randTopic() string {
	oneYearInSec := 365 * 24 * 60 * 60
	oneYearInUsec := oneYearInSec * 1000000
	now := time.Now().UnixMicro()
	r := now % int64(oneYearInUsec)
	b := []byte{
		byte(r >> 56),
		byte(r >> 48),
		byte(r >> 40),
		byte(r >> 32),
		byte(r >> 24),
		byte(r >> 16),
	}
	encoded := base64.URLEncoding.EncodeToString(b)
	return encoded[:6]
}
