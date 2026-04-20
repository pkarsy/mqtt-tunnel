# mqtt-tunnel

SSH proxy via MQTT

- **No port forwarding** required.
- **No DNS/ Dynamic DNS** is needed.
- **No open ports** in server.
- **Works with any NAT** configuration. Even when hole punching has problems, like double NAT, Symmetric NAT.
- **Zero-config** with free public MQTT brokers (Mosquitto, HiveMQ). Expired/lost login credentials are a frequent point of failure.
- Suitable for occasional maintenance connections and light work, one off commands
- Useful for android/termux
  ``` bash
  ssh termux-mqtt 'termux-notification -c "Remember the Milk" ; echo Notification sent'
  
  ssh termux-mqtt termux-clipboard-set MyPassword
  ```  
  works the same on home or work, wifi or mobile data.
  
  See [README_TERMUX.md](README_TERMUX.md) for detailed Termux setup and Android-specific considerations.
- Useful as a second/backup login method for the excellent [gonc](https://github.com/threatexpert/gonc) tool. mqtt-tunnel has faster initial connections, but it is not a gonc replacement (gonc offers direct connections and by implementing encryption, can be used on more tasks other than SSH tunneling).
- mqtt-tunnel data always go via the broker, so the lag is usually noticeable, but perfectly usable.
- The mqtt broker cannot be rate limited.
- It is not a network anonymizer. Use VPN for this purpose.
- The data are encapsulated inside mqtt messages, so there is significant data overhead, given that the interactive SSH sessions use very small packets. This may be a concern on metered networks.

---

### `🔧 Installation`

Binaries are provided in [releases page](https://github.com/yourusername/mqtt-tunnel/releases).

This is the simplest possible installation, without login credentials and only a shared secret, the topic. Start with this for an easy setup, then you can modify as you wish.

#### `1. Generate a random non guessagle topic`

>mqtt-tunnel -generate

you can use a substring of this but not less than 8 chars, to avoid collissions(on open brokers).

#### `2. 🌐 Configure the SSH server`

On the remote SSH server and using a normal account(no root), create the file

`~/.config/mqtt-tunnel/server.json`
```json
{
    "broker": "mqtt://broker.hivemq.com:1883", // may be mqtts: ws: wss:
    "topic": "gFAftaCL", // Use the one you generated at step 1
    "server": ":22" // or "127.0.0.1:22" or ":8022" for termux, see the dedicated README_TERMUX
}
```
and interactivelly run:

```bash
mqtt-tunnel -c server.json            # Looks for ~/.config/mqtt-tunnel/server.json only
mqtt-tunnel -c /path/to/server.json   # Full path
mqtt-tunnel -c ./server.json          # looks in Current directory only
```

**Leave it running**. We will see the negotiation when the client connects. At the end we will make the server autostart.


#### `3. 🔐 Configuring the SSH client`

Create a file `~/.config/mqtt-tunnel/client.json`
```json
{
    "broker": "mqtt://broker.hivemq.com:1883", // or mqtts: ws: wss: but the same open broker
    "topic": "gFAftaCL" // The same as the server !
}
```
**Important:** In client mode, mqtt-tunnel is started as a subprocess of ssh client. Before configuring SSH, first test the mqtt-tunnel standalone, to verify it works correctly:

mqtt-tunnel -c client.json

After the negotiation process, you should see the **Remote** SSH server banner (e.g., `SSH-2.0-OpenSSH_8.9`).This means the tunnel is working ! Now you can proceed with the SSH configuration :

Add to your `~/.ssh/config`:

```bash
Host remote-via-mqtt
    HostName remote-server
    ProxyCommand mqtt-tunnel -c client.json
    # The following are optional but highly recommended, to easily detect broken connections.
    # and in fact useful independently of mqtt-tunnel
    ServerAliveInterval 10  # Send keepalive every 10 seconds
    ServerAliveCountMax 3  # Disconnect after 2 missed (10s total)
```

And finally:

```bash
ssh remote-via-mqtt # You will see the negotiation here and on the server(the interactive command you have started).
```
Congratulations !

**Now we need some final actions:**


#### `4. mqtt-tunnel on server autostart, and SSH fine tunning`

For example

>crontab -e

and add the entry
>@reboot setsid -f mqtt-tunnel -c server.json

To be able to view the daemon messages, add

```json
"log-file": "~/mqtt-server.log"
```

to `server.json` . The config is reloaded automatically

Also it is recommended to add theese options to sshd_config
**Server side** (conservative, just to clean up stale sessions):
```
# /etc/ssh/sshd_config
ClientAliveInterval 60         # Check every minute
ClientAliveCountMax 3          # Disconnect after 3 minutes of silence
```

#### `5. Client fine tunning`
By default in client mode the output is /dev/tty. Add the entry

>"log-file": "~/mqtt-client.log"

in client.json to avoid seeing the mqtt-tunnel logs during SSH logins.

---

### `✅ Topic Best Practices`
The topic does not contribute to the SSH security. 

**For public MQTT brokers with no login credentials:**

The topic must remain secret and not guessable.
- Use 8-10 random alphanumeric characters (e.g., `gFAftaCL`). Run `mqtt-tunnel -generate` to get one
- Avoid slashes (`/`) - they create topic hierarchies that may be accessible via wildcard subscriptions (`#` or `+`). The individual tunnel topics will be subtopics of this.

**For brokers with login credentials (for example flespi.io):**

There is no fighting for topics with all the other users, neither need to hide the topic, so:
- You can use shorter topics e.g. `"topic": "ssh"` or `"topic": "comm/ssh"`
- In this case `/` can be useful to limit the mqtt messages in a specific topic hierarchy.

In both cases do not use topics longer than necessary (let's say up to 10 chars), to avoid increasing the packet size.

---

### `💡Supported broker URL formats`

| URL Scheme | Description | Default Port |
|------------|-------------|--------------|
| `mqtt://` | MQTT over TCP (no TLS) | 1883 |
| `mqtts://` | MQTT over TLS | 8883 |
| `ws://` | MQTT over WebSocket | 80 |
| `wss://` | MQTT over Secure WebSocket | 443 |

**Recommended Public Brokers, no credentials needed:**
- `mqtt://broker.hivemq.com:1883` - Reliable, free public broker
- `mqtt://test.mosquitto.org:1883` - Community MQTT broker

**These services are provided for the common good. Please use responsibly**. Avoid large file transfers or sustained high-bandwidth usage.

---

#### `Sample Configuration file`

Generate a sample config:

```bash
mqtt-tunnel -config help
```

The config understands `$HOME/to/file` and `~/to/file` expansions.

---

#### `Connection Keepalive(MQTT underlying level, no SSH)`
Generally the default keepalive settings are usually working OK.
The following options control connection keepalive on server:

**mqtt-keepalive** (default is 60 seconds, set 0 or negative to disable)
- Paho MQTT library's built-in keepalive mechanism, implementing the mqtt keepalive protocol.
- Sends PINGREQ packets at this interval to keep connection alive, otherwise the server initiates a disconnect.
- On termux is disabled (see manuall-keepalive)

**manual-keepalive** (by default disabled, but it is enabled on Termux)
- Custom keepalive mechanism. The normal keepalive is disabled, so the broker does not require PINGREQ packets.
- After the specified seconds of inactivity, sends a PING to `baseTopic/wxyz`
- By subscribing to the same topic, expects the broker to echo the packet back within 5 seconds.
- If no echo received, the client disconnects and reconnection is triggered.
- Useful for Android/Termux where Doze mode interferes with standard keepalive. The problem is the Doze mode postpones the timers of the application, so a PINGRESP can be delayed(for many seconds/minutes) and the broker considers the socket dead.
- **On Termux**, defaults to 60 seconds automatically. See [README_TERMUX.md](README_TERMUX.md)

On client mode keepalive is disabled, the SSH tunnel can detect dead connections by itself. You can enable mqtt/manual keepalive if you wish but this is not tested at all.

---

#### Command-Line Options

```
mqtt-tunnel -h
```
---

#### Troubleshooting

When connection issues arise, the first step is to enable debug logging to see what's happening. Add `"debug": true` to your config file:

```json
{
    "broker": "mqtt://broker.hivemq.com:1883",
    "topic": "your-topic",
    "debug": true,
    "log-file": "/dev/tty" // 
}
```

**What to look for in the logs:**

The startup log lines show critical connection details:
```
2026/03/25 07:12:51 [INFO] Client mode
2026/03/25 07:12:51 [INFO]   app-version=0.8.0
2026/03/25 07:12:51 [INFO]   wire-protocol=3
2026/03/25 07:12:51 [INFO]   root-topic=Ktt91J5q
```

- **app-version**: Check if the client matches your server version
- **wire-protocol**: Must match between client and server (currently 3)
- **root-topic**: Verify this matches your server's topic



**Still having issues?**

- Ensure the server is running first, then connect the client
- Verify the topic matches between client and server config
- Check that the MQTT broker is accessible from both machines
- For TLS issues, verify certificates are correct

---

#### `Security Considerations`

⚠️ **This tool provides no encryption.** The MQTT tunnel itself is unencrypted.

- **Always use SSH** (or another encrypted protocol) through the tunnel
- The topic name acts as a shared secret. Use a (10 chars is OK) random string (generate with `-topic generate`)
- **Avoid slashes** in topics on public brokers, they may be exposed via wildcard subscriptions
- Be aware that MQTT brokers with wildcard access (`#`) could expose your traffic metadata anyway.

You can consider:
  - **Transport encryption (`mqtts://` or `wss://`):** Protects the topic name from eavesdropping on the wire, but adds latency. Since SSH is already end-to-end encrypted, you may prefer `mqtt://` for better performance. However, do your own tests.
- **MQTT broker authentication:** Requires account to maintain, but prevents topic leakage. Public brokers offer zero-config convenience with no accounts to manage and easy server switching.
- On open MQTT brokers the topic must be kept secret. Knowing the topic allows for DoS attacks and for accessing the SSH server (the login prompt). SSH key based login is very important for servers exposed to the Internet.
- Even if a third party knows the topic, they **cannot decrypt** the session content since SSH encryption is end-to-end.

> ⚠️ **Running as root is dangerous.** As a network server, mqtt-tunnel could theoretically be compromised, giving an attacker a shell. While Go's memory safety makes this unlikely, it is not impossible. **Never run mqtt-tunnel as root.** Create a dedicated user with minimal privileges (e.g., `useradd -r -s /sbin/nologin mqtt-tunnel`) and run the server as that user, and/or run with firejail.

---

#### Maintenance

MQTT servers can have downtime. Also the protocol may change and you could lose access after an update. **Always maintain an alternative way to access your remote server**, such as:
- [gonc](https://github.com/threatexpert/gonc) or another UDP hole-punching tool. It is better for long sessions/large files, but may need a little more time to connect. For Termux it is more friendly to your mobile data (direct connection, no MQTT overhead)
- Traditional port forwarding or VPN
- Another mqtt-tunnel instance connecting to a different server. I do not recommend this - it is confusing; you cannot tell with which instance you are connected.

#### Connection Keepalive / Timeout Detection

For fast detection of dead connections (especially useful when switching between networks), configure SSH keepalive with different values for client and server:

**Client side** (fast detection):
```
# ~/.ssh/config
Host remote-via-mqtt
    ServerAliveInterval 5      # Send keepalive every 5 seconds
    ServerAliveCountMax 3      # Disconnect after 3 missed (10s total)
    ProxyCommand /path/to/mqtt-tunnel -c /path/to/config.json
```



Then restart sshd

#### Semi automatic topic change with rotate-topic

The `rotate-topic` script helps rotate topics periodically for security:

We assume the server has a server.json under the standard path
`~/.config/mqtt-tunnel/server.json` and the client a `client.json` and that both
files have the same topic.

```bash
# Generate new random topic and update both local and remote configs
./rotate-topic client.json server@host:server.json

# Or specify a custom topic (for tests mainly)
./rotate-topic client.json server@host:server.json MyCustomTopic123
```

The script resolves bare filenames to `~/.config/mqtt-tunnel/` (like mqtt-tunnel itself) and ensures both files have matching topics before updating. See the script source for details.

---

### How It Works
![connection](connection.png)
All negotiation is performed in the control topic (e.g., baseTopic/ctl) but the data are using 2 randomly generated topics baseTopic/ClientPubTopic and baseTopic/ServerPubTopic
1. Client: subscribes to control topic
2. Client → Server: connect (ID, version)  [ID generated by client]
3. Server: checks protocol version - if mismatch, sends failure and closes
4. Server: generates topics (ClientPubTopic, ServerPubTopic)
5. Server: subscribes to ClientPubTopic
6. Server → Client: connect_ack (ID, ClientPubTopic, ServerPubTopic) [or failure if version mismatch]
7. Client: subscribes to ServerPubTopic, waits for MQTT broker ACK
8. Client → Server: connect_confirm (ID)
9. Server: receives confirm
10. Server: connects to SSH target (e.g., 127.0.0.1:22)
11. Server: starts reading from SSH, publishing data to ServerPubTopic
12. Client publishes to ClientPubTopic, so bidirectional data transfer begins.

---

### Acknowledgments

This project was originally inspired by [shirou/mqtunnel](https://github.com/shirou/mqtunnel). However, the two tools have diverged significantly:

- **Command-line interface** is completely different and incompatible
- **Wire protocol** has been modified extensively.
- **Focus:** This tool is specifically focused on SSH proxying (the local instance uses stdio for SSH ProxyCommand integration)

The tools cannot be used interchangeably.

---

### License

[Apache License 2.0](LICENSE.txt)
