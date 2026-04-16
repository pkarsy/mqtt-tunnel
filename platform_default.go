//go:build !termux

package main

func initPlatform() {
    // PC: use defaults (all cores, no thread limits)
}

// getDefaultManualKeepalive returns the default manual keepalive interval for this platform
// On PC, manual keepalive is disabled by default (0)
func getDefaultManualKeepalive() int {
    return 0
}

func getPlatformInfo() string {
    return ""
}