//go:build termux

package main

import (
    "context"
    "net"
    "runtime"
    "runtime/debug"
    "time"

    _ "time/tzdata" // Embed timezone database for CGO-disabled builds
)

func initPlatform() {
    runtime.GOMAXPROCS(1)
    debug.SetMaxThreads(10)

    // Override default resolver to use 1.1.1.1
    net.DefaultResolver = &net.Resolver{
        PreferGo: true,  // Force pure Go
        Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
            d := net.Dialer{Timeout: 5 * time.Second}
            // Use Cloudflare DNS directly, ignore system config
            return d.DialContext(ctx, "udp", "1.1.1.1:53")
        },
    }
}

// getDefaultManualKeepalive returns the default manual keepalive interval for this platform
// On Termux, we default to 60 seconds to work better with Android's Doze mode
func getDefaultManualKeepalive() int {
    return 60
}

func getPlatformInfo() string {
    return "Termux build: GOMAXPROCS=1, MaxThreads=10, DNS=1.1.1.1, DefaultManualKeepalive=60s"
}
