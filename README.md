# mqtt-tunnel

Tunnel TCP connections (like SSH) through an MQTT broker. Access remote servers behind NAT/firewalls without open ports or static IPs.

## Overview

**mqtt-tunnel** creates a TCP tunnel through an MQTT broker, allowing you to connect to remote SSH servers even when they're behind restrictive NAT (double NAT, symmetric NAT, etc.).

- **No port forwarding** required
- **Works with any NAT** configuration
- **Zero-config** with free public MQTT brokers (Mosquitto, HiveMQ)
- Perfect for occasional maintenance connections and light work

> **Note:** While functional, performance won't match direct connections. For long sessions, consider using [gonc](https://github.com/threatexpert/gonc) or opening direct ports.

## How It Works

```
┌─────────────┐      ┌─────────────┐      ┌─────────────┐      ┌─────────────┐
│   SSH Client │◄────►│ mqtt-tunnel │◄────►│ MQTT Broker │◄────►│ mqtt-tunnel │◄────►│ SSH Server  │
│  (local)     │      │  (-stdio)   │      │             │      │  (-remote)  │      │  (remote)   │
└─────────────┘      └─────────────┘      └─────────────┘      └─────────────┘      └─────────────┘
```

## Installation

```bash
go install github.com/yourusername/mqtt-tunnel@latest
```

Or download a pre-built binary from the [releases page](https://github.com/yourusername/mqtt-tunnel/releases).

## Quick Start

### 1. Create a Configuration File

Create `config.json` on **both** local and remote machines:

```json
{
    "broker": "mqtt://broker.hivemq.com:1883",
    "topic": "a3f7b2e9d1"
}
```

> **Topic Best Practices:** The topic acts as a shared secret. For public MQTT brokers:
> - Use **10-20 random alphanumeric characters** (e.g., `a3f7b2e9d1`)
> - **Avoid slashes** (`/`) - they create topic hierarchies that may be accessible via wildcard subscriptions (`#` or `+`)
> - Generate a secure topic with: `./mqtt-tunnel -topic generate`
> 
> **Security Note:** Anyone who knows or guesses your topic can disrupt (DoS) the SSH session by publishing to it, but they **cannot decrypt** the session content since SSH encryption is end-to-end. Keep your topic secret!

**Supported broker URL formats:**

| URL Scheme | Description | Default Port |
|------------|-------------|--------------|
| `mqtt://` | MQTT over TCP (no TLS) | 1883 |
| `mqtts://` | MQTT over TLS | 8883 |
| `ws://` | MQTT over WebSocket | 80 |
| `wss://` | MQTT over Secure WebSocket | 443 |

**Recommended Public Brokers:**
- `mqtt://broker.hivemq.com:1883` - Reliable, free public broker
- `mqtt://test.mosquitto.org:1883` - Community MQTT broker

These services are provided for the common good. Please use responsibly—avoid large file transfers or sustained high-bandwidth usage.

### 2. Start the Remote Tunnel

On the remote server (the one you want to SSH into):

```bash
./mqtt-tunnel -c config.json -remote 127.0.0.1:22
```

Or without a config file:

```bash
./mqtt-tunnel -broker mqtt://broker.hivemq.com:1883 -topic my-secret-topic-12345 -remote 127.0.0.1:22
```

**Auto-start on boot** (add to crontab with `crontab -e`):

```bash
@reboot cd /home/user/tunnel && ./mqtt-tunnel -c config.json -remote 127.0.0.1:22 2>&1 &
```

### 3. Configure SSH

Add to your `~/.ssh/config`:

```
Host remote-via-mqtt
    HostName remote-server
    ProxyCommand /path/to/mqtt-tunnel -c /path/to/config.json -stdio
```

Then connect:

```bash
ssh remote-via-mqtt
```

### 4. Test the Connection

To verify the tunnel works without SSH:

```bash
./mqtt-tunnel -c config.json -stdio
```

If you see the SSH server banner (e.g., `SSH-2.0-OpenSSH_8.9`), the tunnel is working.

## Configuration Options

Generate a sample config:

```bash
./mqtt-tunnel -config help
```

## Command-Line Options

```
Usage: mqtt-tunnel [options]

Options:
  -broker string        MQTT broker URL
  -c string            Config file path
  -ca-cert string      CA certificate path
  -client-cert string  Client certificate path
  -connection-timeout int   Connection timeout in seconds (default 15)
  -log-file string     Log file path
  -password string     MQTT password
  -private-key string  Private key path
  -remote string       Remote target address (enables remote mode)
  -stdio               Use stdio for tunnel (enables local mode)
  -topic string        MQTT topic (use 'generate' to create a secure random topic)
  -username string     MQTT username
  -verbose             Enable verbose logging
```

## Security Considerations

> ⚠️ **This tool provides no encryption.** The MQTT tunnel itself is unencrypted.

- **Always use SSH** (or another encrypted protocol) through the tunnel
- The topic name acts as a shared secret—use a long(if random 10 chars is OK), random string (generate with `-topic generate`)
- **Avoid slashes** in topics on public brokers—they may be exposed via wildcard subscriptions
- Be aware that MQTT brokers with wildcard access could expose your traffic metadata
- You can consider:
  - **Transport encryption (`mqtts://` or `wss://`):** Protects the topic name from eavesdropping on the wire, but adds latency. Since SSH is already end-to-end encrypted, many users prefer the faster `mqtt://` for performance.
  - **MQTT broker authentication:** Requires accounts/credentials to maintain, but prevents unauthorized connections. Public brokers offer zero-config convenience with no accounts to manage and easy server switching.

## Acknowledgments

This project was originally inspired by [shirou/mqtunnel](https://github.com/shirou/mqtunnel). However, the two tools have diverged significantly:

- **Command-line interface** is completely different and incompatible
- **Wire protocol** has been modified and optimized for SSH use cases
- **Focus:** While mqtunnel is a general-purpose tunnel, this tool is specifically designed and optimized for SSH proxying

The tools cannot be used interchangeably.

## Important Operational Notes

### Protocol Compatibility

The wire protocol may change in future versions. **When updating, always upgrade both local and remote sides together** to ensure compatibility.

### Have a Backup Access Method

Since the protocol may change and you could lose access after an update, **always maintain an alternative way to access your remote server**, such as:
- A second `mqtt-tunnel` instance running with a different topic
- [gonc](https://github.com/bobvawter/gonc) or similar TCP hole-punching tool
- Traditional port forwarding or VPN

This ensures you don't lock yourself out when updating the software.

## License

[MIT License](LICENSE.txt)
