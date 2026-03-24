//go:build !termux

package main

func initPlatform() {
    // PC: use defaults (all cores, no thread limits)
}

func getPlatformInfo() string {
    return ""
}