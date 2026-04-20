# Testing mqtt-tunnel Locally

This guide describes a convenient local testing setup for developing and testing mqtt-tunnel without needing remote servers or SSH daemons.

## Overview

The test setup uses 4 terminals to simulate the complete mqtt-tunnel flow:
- **mosquitto** MQTT broker running locally (no credentials required)
- **socat** simulating the "remote" SSH server
- **mqtt-tunnel server** connecting to socat
- **mqtt-tunnel client** initiating the connection

## Prerequisites

```bash
# mosquitto MQTT broker
sudo apt-get install mosquitto mosquitto-clients

# socat for simulating the SSH server
sudo apt-get install socat

# mqtt-tunnel binary (build from source)
go build -o mqtt-tunnel
```

## Terminal Setup

### Terminal 1: Simulated SSH Server (socat)

This terminal simulates a remote SSH server that echoes back any input it receives:

```bash
socat -T 60 TCP-LISTEN:7022,fork SYSTEM:'echo "SOCAT: CONNECTED" ; while IFS= read -r line; do echo "ECHO: $line"; echo $line>/dev/tty ; done; echo "SOCAT: CLOSED" >/dev/tty'
```

**What it does:**
- Listens on TCP port 7022
- Prints "SOCAT" on each new connection
- Echoes back any received line prefixed with "ECHO:"
- Forks to handle multiple connections

### Terminal 2: MQTT Traffic Monitor

Monitor all MQTT messages on the `ssh/#` topic hierarchy:

```bash
mosquitto_sub -t "ssh/#" -v
```

**What it shows:**
- Control messages (connect, connect_ack, connect_confirm)
- Data flow between client and server
- Topic creation and subscription activity

### Terminal 3: mqtt-tunnel Server

Create `test-server.json`:
```json
{
    "broker": "mqtt://localhost:1883",
    "topic": "ssh",
    "server": ":7022",
    "debug": true,
    "log-file": "/dev/tty"
}
```

Run the mqtt-tunnel server component (the "remote" side):

```bash
./mqtt-tunnel -c test-server.json
```

**What it does:**
- Connects to local mosquitto broker
- Subscribes to control topic `ssh/ctl`
- Waits for client connections
- Forwards traffic to the socat "SSH server" on port 7022

### Terminal 4: mqtt-tunnel Client

Create `test-client.json`:
```json
{
    "broker": "mqtt://localhost:1883",
    "topic": "ssh",
    "debug": true,
    "log-file": "/dev/tty"
}
```

Run the mqtt-tunnel client component:

```bash
./mqtt-tunnel -c test-client.json
```

**What it does:**
- Connects to local mosquitto broker
- Initiates connection to the server
- Reads from stdin, writes to stdout (stdio mode)
- Forwards data through the MQTT tunnel

## Testing the Tunnel

Once all 4 terminals are running:

1. **In Terminal 4 (client)** - Type anything and press Enter:
   ```
   hello
   ```

2. **You should see in Terminal 1 (socat):**
   ```
   SOCAT
   ECHO: hello
   ```

3. **You should see in Terminal 2 (MQTT monitor):**
   - Messages on topics like `ssh/ctl`, `ssh/<id>/c`, `ssh/<id>/s`

4. **The response appears in Terminal 4 (client):**
   ```
   ECHO: hello
   ```

## Quick Test Commands

### One-shot test (no typing required)

In Terminal 4, pipe data through:

```bash
echo "test message" | ./mqtt-tunnel -c test-client.json
```

### Using netcat as alternative to socat

If you prefer netcat over socat:

```bash
# Terminal 1 (alternative)
while true; do echo "SSH-2.0-Test" | nc -l 7022; done
```

## Troubleshooting

### mosquitto not running

```bash
# Check if mosquitto is running
pgrep mosquitto

# Start mosquitto in background
mosquitto &

# Or with verbose logging
mosquitto -v
```

### Port already in use

```bash
# Kill processes using port 7022
fuser -k 7022/tcp

# Or use a different port (update test-server.json to use ":7023")
socat -T 60 TCP-LISTEN:7023,fork ...
./mqtt-tunnel -c test-server.json
```

### MQTT connection refused

Ensure mosquitto is listening on the default port:

```bash
# Check mosquitto listeners
netstat -tlnp | grep mosquitto

# Should show port 1883
# tcp 0 0 0.0.0.0:1883 0.0.0.0:* LISTEN
```

### Build issues

```bash
# Clean build
rm -f mqtt-tunnel
go build -o mqtt-tunnel

# Or use the build script
./build.sh
```

## Expected Output

### Terminal 3 (Server) - Successful startup:
```
2026/03/26 14:30:00 [INFO] Server mode
2026/03/26 14:30:00 [INFO]   app-version=0.5.1
2026/03/26 14:30:00 [INFO]   wire-protocol=2
2026/03/26 14:30:00 [INFO]   root-topic=ssh
2026/03/26 14:30:00 [INFO]   addr=127.0.0.1:7022
2026/03/26 14:30:00 [INFO] successfully connected to MQTT broker
2026/03/26 14:30:00 [INFO] topic subscribing ssh/ctl
```

### Terminal 4 (Client) - Successful connection:
```
2026/03/26 14:31:00 [INFO] Client mode
2026/03/26 14:31:00 [INFO]   app-version=0.5.1
2026/03/26 14:31:00 [INFO]   wire-protocol=2
2026/03/26 14:31:00 [INFO]   root-topic=ssh
2026/03/26 14:31:00 [INFO] successfully connected to MQTT broker
2026/03/26 14:31:00 [INFO] topic subscribing ssh/ctl
2026/03/26 14:31:05 [INFO] ack received
2026/03/26 14:31:05 [INFO] connected to server
```

### Terminal 2 (MQTT monitor) - Typical message flow:
```
ssh/ctl {"id":"abc123","cmd":"connect","ver":2}
ssh/ctl {"id":"abc123","cmd":"connect_ack","client_pub":"ssh/abc123/c","server_pub":"ssh/abc123/s"}
ssh/abc123/c {"id":"abc123","cmd":"connect_confirm"}
ssh/abc123/c <binary data>
ssh/abc123/s <binary data>
```

## Advanced Testing

### Multiple concurrent connections

The server supports multiple concurrent connections. Open additional Terminal 4 instances:

```bash
# Terminal 4a
./mqtt-tunnel -c test-client.json

# Terminal 4b (in another window)
./mqtt-tunnel -c test-client.json
```

### Using configuration files

The `-c` flag is required to specify the config file:

**test-server.json:**
```json
{
    "broker": "mqtt://localhost:1883",
    "topic": "ssh",
    "server": ":7022",
    "debug": true
}
```

**test-client.json:**
```json
{
    "broker": "mqtt://localhost:1883",
    "topic": "ssh",
    "debug": true
}
```

Then run:
```bash
# Terminal 3
./mqtt-tunnel -c test-server.json

# Terminal 4
./mqtt-tunnel -c test-client.json
```

### Testing with actual SSH protocol

Replace the socat "SSH server" with a real OpenSSH server on a non-standard port:

```bash
# Terminal 1 - Run SSH server on port 7022
sudo /usr/sbin/sshd -p 7022 -D

# Terminal 4 - Connect via mqtt-tunnel
./mqtt-tunnel -c test-client.json | head -1

# You should see the SSH banner:
# SSH-2.0-OpenSSH_8.9p1 Ubuntu-3ubuntu0.10
```

### Server Debugging with Direct MQTT Commands

Instead of running the mqtt-tunnel client in Terminal 4, you can manually send control packets using `mosquitto_pub` to test the server's response to various scenarios. This is useful for debugging server-side validation and error handling.

**Setup:**
```bash
# Terminal 4 - Subscribe to see server responses
mosquitto_sub -t "ssh/ctl" -v

# Terminal 5 - Send test commands
mosquitto_pub -t "ssh/ctl" -m '<json-payload>'
```

**Valid connect request (should receive connect_ack):**
```json
{"type":"connect","tunnel_id":"Ab12","version":3,"origin":"client","local_port":0,"remote_port":0}
```

**Invalid ID - empty (should receive failure):**
```json
{"type":"connect","tunnel_id":"","version":3,"origin":"client","local_port":0,"remote_port":0}
```

**Invalid ID - too long (7 chars):**
```json
{"type":"connect","tunnel_id":"Ab12Xyz","version":3,"origin":"client","local_port":0,"remote_port":0}
```

**Invalid ID - special characters:**
```json
{"type":"connect","tunnel_id":"Ab-12","version":3,"origin":"client","local_port":0,"remote_port":0}
```

```json
{"type":"connect","tunnel_id":"Ab_12","version":3,"origin":"client","local_port":0,"remote_port":0}
```

```json
{"type":"connect","tunnel_id":"Ab 12","version":3,"origin":"client","local_port":0,"remote_port":0}
```

**Duplicate ID test** (send this twice, second should fail):
```json
{"type":"connect","tunnel_id":"Test12","version":3,"origin":"client","local_port":0,"remote_port":0}
```

**Protocol version mismatch:**
```json
{"type":"connect","tunnel_id":"Ab12","version":2,"origin":"client","local_port":0,"remote_port":0}
```

**Edge cases - valid IDs:**

Exactly 6 chars:
```json
{"type":"connect","tunnel_id":"Ab12Cd","version":3,"origin":"client","local_port":0,"remote_port":0}
```

Single char:
```json
{"type":"connect","tunnel_id":"A","version":3,"origin":"client","local_port":0,"remote_port":0}
```

Only digits:
```json
{"type":"connect","tunnel_id":"1234","version":3,"origin":"client","local_port":0,"remote_port":0}
```

Only letters:
```json
{"type":"connect","tunnel_id":"AbCd","version":3,"origin":"client","local_port":0,"remote_port":0}
```

**Note:** The server sends failure responses to the control topic (`ssh/ctl`) which you can see in Terminal 4 (the `mosquitto_sub`). Success responses include `connect_ack` messages.

## Cleanup

To stop all components:

```bash
# Kill mosquitto
pkill mosquitto

# Kill socat
pkill socat

# Kill mqtt-tunnel instances
pkill mqtt-tunnel
```
