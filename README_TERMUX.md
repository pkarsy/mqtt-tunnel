### mqtt-tunnel on Termux (Android)

### Overview

Running mqtt-tunnel on Android via Termux has some special considerations due to Android's power management (Doze mode) and wake lock behavior. This readme is only about the special considerations on TERMUX, the documentation is in the README.md

### Installation
The termux binary is optimized (GOMAXPROCS=1, THREADS=10, builtin DNS) to avoid being killed by some Android systems. It is tested with Android 15 and 16 and Termux (F-Droid) without problems, but not all phones are the same.

---

### Manual Keepalive vs Paho Keepalive

### The Problem with Paho Keepalive on Android

Android's Doze mode can delay timers when the device is idle and no wake lock is held. The standard Paho MQTT keepalive uses precise timers that can be delayed by Android, causing the MQTT broker to drop the connection when pings don't arrive within the expected window.

### Solution: Manual Keepalive

mqtt-tunnel provides a `manual-keepalive` option that works better on Android:

- **Default on Termux**: 60 seconds (automatically applied, no config needed)
- **How it works**: Sends a PING message to the topic and waits for an echo response. The server does not enforce a time window.
- **Advantage**: Tolerant of timer delays caused by Doze mode. The server does not test if the connection is healthy, only the client.

### Configuration

Example `termux-server.json`:

```json
{
    "broker": "mqtt://broker.hivemq.com:1883",
    "topic": "your-topic",
    "server": ":8022",
    "manual-keepalive": 60 // this is the default on termux y
}
```

Note: `manual-keepalive` defaults to 60 seconds on Termux, so you can omit it. If you do not have problems, keep the default.

### Trade-offs

**Shorter intervals (e.g., 10-30 seconds):**
- More responsive - may prevent Doze mode from activating
- Higher CPU usage
- More battery drain
- More data usage

**Longer intervals (e.g., 60-120 seconds):**
- Better battery life
- Less data usage
- May allow Doze mode to activate between pings(but the server does not drop the connection). The response from the SSH server will be sluggish but the bettery will be preserved. When connected you can run "termux-wake-lock" and on disconnect "termux-wake-unlock"
- Slower detection of connection issues (but 1-2 min is usually acceptable)

Using `manual-keepalive` with an appropriate interval helps maintain the connection without requiring a permanent wake lock.

---

### Configure sshd keepalive in Termux

```bash
# Edit sshd config
nano $PREFIX/etc/ssh/sshd_config

# Add these lines:
ClientAliveInterval 60
ClientAliveCountMax 3

# Restart sshd
pkill sshd && sshd
```

### Auto-start sshd when Termux opens (add to `~/.bashrc`)

```bash
# Start sshd if not running
if ! pgrep -x "sshd" > /dev/null; then
    sshd
fi
```

### Typical setup

```bash
# On phone (Termux):
# Install: pkg install openssh termux-api
# Generate topic: mqtt-tunnel -generate
# Place config in ~/.config/mqtt-tunnel/termux-server.json and run:
/path/to/mqtt-tunnel -c termux-server.json
```
On laptop (~/.ssh/config):
```
# 
Host termux-phone
    HostName termux
    ServerAliveInterval 10
    ServerAliveCountMax 3
    ProxyCommand /path/to/mqtt-tunnel -c termux-client.json
```

## Termux Optimizations and Connection Stability

> **Note:** This section applies to **server mode** (config with `"server": ":8022"`
> on your Termux device), which is the typical use case for Termux.

**WARNING:** Fighting with Android battery optimizations can be very exhausting (without guarantee of success).

Android's Doze mode and power management can cause timeouts and slugish behaviour on termux. This varies significantly by:
- Android version
- OEM (Samsung, Xiaomi, Pixel, etc. have different strategies)
- Battery optimization settings

### Solutions to try, probably combined

1. **Disable battery optimization for Termux** (system setting)
   Settings → Apps → Termux → Battery → Unrestricted
   Also set the cellular data usage as "unrestricted" if yu want to login to your phone even on cellular data.
   (Exact path varies by OEM)
   You may need to find many different setting about battery/power optimizations. Search for your specific model.

2. **Permanent termux-wake-lock** (higher battery drain)
   ```bash
   termux-wake-lock
   ```
   Keeps CPU awake. SSH is very responsive. The battery drain may (depending on phone) may make the solution impractical.


3. **Targeted approach**
   Use wakelock and start mqtt-tunnel only during active SSH sessions. It needs some manual intervention but generally works very well.

4. You can try different "manual-keepalive" values but is not tested, as the default value(60) seems to work OK.

## Timezone / Local Time in Logs

The Termux binary is statically linked and includes an embedded timezone database.
To display local time in logs (instead of UTC), set the `TZ` environment variable:

```bash
# Find your timezone
getprop persist.sys.timezone

# Run with TZ set
TZ=Europe/Athens mqtt-tunnel -c termux-server.json

# Or make it permanent
export TZ=Europe/Athens
echo 'export TZ=Europe/Athens' >> ~/.bashrc
```

