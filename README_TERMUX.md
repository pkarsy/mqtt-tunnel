# mqtt-tunnel on Termux (Android)

## Overview

Running mqtt-tunnel on Android via Termux has some special considerations due to Android's power management (Doze mode) and wake lock behavior.

## Installation

Binaries are provided in the [releases page](https://github.com/yourusername/mqtt-tunnel/releases). The termux binary is optimized (GOMAXPROCS=1, THREADS=10, builtin DNS) to avoid being killed by some Android systems. It is tested with Android 15 and 16 and Termux (F-Droid) without problems, but not all phones are the same.

## Manual Keepalive vs Paho Keepalive

### The Problem with Paho Keepalive on Android

Android's Doze mode can delay timers when the device is idle and no wake lock is held. The standard Paho MQTT keepalive uses precise timers that can be delayed by Android, causing the MQTT broker to drop the connection when pings don't arrive within the expected window.

### Solution: Manual Keepalive

mqtt-tunnel provides a `manual-keepalive` option that works better on Android:

- **Default on Termux**: 60 seconds (automatically applied, no config needed)
- **How it works**: Sends a PING message to the topic and waits for an echo response. The server does not enforce a time window.
- **Advantage**: Tolerant of timer delays caused by Doze mode. The server does not test if the connection is healthy, only the client.

### Configuration

In your `config.json`:

```json
{
    "broker": "mqtt://broker.hivemq.com:1883",
    "topic": "your-topic",
    "server": ":8022",
    "manual-keepalive": 60 # This is the default, you can ommit
}
```

### Trade-offs

**Shorter intervals (e.g., 10-30 seconds):**
- More responsive - may prevent Doze mode from activating
- Higher CPU usage
- More battery drain
- More data usage
- use it only if the default does not work.

**Longer intervals (e.g., 60-120 seconds):**
- Better battery life
- Less data usage
- May allow Doze mode to activate between pings(but the server does not drop the connection). The response from the SSH server will be sluggish but the bettery will be preserved. When connected you can run "termux-wake-lock" and on disconnect "termux-wake-unlock"
- Slower detection of connection issues (but 1-2 min is usually acceptable)

### Recommendation

- **With wake lock held**: Standard 60-second interval works well
- **Without wake lock**: Use 60 seconds or shorter if you need more responsiveness
- **Battery critical scenarios**: Use 120 seconds and accept occasional reconnections

## Wake Lock Considerations

Without a wake lock, Android may:
- Delay timers (affecting Paho keepalive more than manual keepalive)
- Suspend network connections
- Kill background processes

Using `manual-keepalive` with an appropriate interval helps maintain the connection without requiring a permanent wake lock.

## Setup

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
# Generate topic: mqtt-tunnel -topic generate
# Place config in ~/.config/mqtt-tunnel/server.json and run:
/path/to/mqtt-tunnel

# On laptop (~/.ssh/config):
Host termux-phone
    HostName termux
    ServerAliveInterval 10
    ServerAliveCountMax 3
    ProxyCommand /path/to/mqtt-tunnel -c client.json
```

## Battery Optimization vs Connection Stability

> **Note:** This section applies to **server mode** (running `mqtt-tunnel -server :8022`
> on your Termux device), which is the typical use case for Termux.

**WARNING:** Fighting with Android battery optimizations can be very exhausting (without guarantee of success). The Android system generally speaking (depending on the provider) blocks long running processes and long running TCP connections. If you are not dependent on Termux, do not bother with all this.

Android's Doze mode and power management can cause frequent MQTT disconnections (typically every 30-60 seconds). This varies significantly by:
- Android version
- OEM (Samsung, Xiaomi, Pixel, etc. have different strategies)
- Battery optimization settings

### Enable logging to diagnose disconnections

Add to your config:
```json
{
    "broker": "mqtt://broker.hivemq.com:1883",
    "topic": "your-topic",
    "server": ":8022",
    "log-file": "~/mqtt-tunnel.log",
    "debug": true
}
```

### Solutions to try (in order)

1. **termux-wake-lock** (most reliable, higher battery drain)
   ```bash
   termux-wake-lock
   ```
   Keeps CPU awake. The battery drain may (depending on phone) make the solution impractical.

2. **Experiment with MQTT keepalive** (balance between battery and stability)
   ```json
   {
       "broker": "mqtt://broker.hivemq.com:1883",
       "topic": "your-topic",
       "server": ":8022",
       "mqtt-keepalive": 15,
       "manual-keepalive": 15
   }
   ```
   Shorter keepalive (15-20s) **may** prevent Doze from kicking in. In my phone it does not work at all.
   
   Experiment with values: 10, 15, 20, 30 seconds.
   My phone (Android 16) works with `"manual-keepalive": 10`, without wake-lock.
   Note this method eats some data from your plan.

3. **Disable battery optimization for Termux** (system setting)
   Settings → Apps → Termux → Battery → Unrestricted
   Also set the cellular data usage as you wish
   (Exact path varies by OEM)

4. **Targeted approach**
   Use wakelock and start mqtt-tunnel only during active SSH sessions. It needs some manual intervention but generally works very well.

**Note:** Every Android device behaves differently. You may need to experiment to find the right balance for your specific device.

## Timezone / Local Time in Logs

The Termux binary is statically linked and includes an embedded timezone database.
To display local time in logs (instead of UTC), set the `TZ` environment variable:

```bash
# Find your timezone
getprop persist.sys.timezone

# Run with TZ set
TZ=Europe/Athens mqtt-tunnel -c config.json

# Or make it permanent
export TZ=Europe/Athens
echo 'export TZ=Europe/Athens' >> ~/.bashrc
```

Common timezone values: `Europe/Athens`, `Europe/London`, `America/New_York`, `Asia/Tokyo`,
or use `EET`, `CET`, `EST` for short forms.

## Example Use Cases

```bash
# Send notification to phone
ssh termux-mqtt 'termux-notification -c "Remember the Milk"; echo Notification sent'

# Copy to clipboard
ssh termux-mqtt termux-clipboard-set MyPassword
```

Works the same on home or work, wifi or mobile data.
