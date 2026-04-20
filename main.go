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

package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"mqtt-tunnel/tunnel"
)

// Version and gitHash are defined in version.go

// fileExists checks if a file exists
func fileExists(filename string) bool {
	_, err := os.Stat(filename)
	return err == nil
}

// resolveConfigPath resolves a config file path.
// If the path contains no directory separator, it looks in ~/.config/mqtt-tunnel/.
// Use ./filename to reference the current directory explicitly.
func resolveConfigPath(filename string) string {
	// If path contains / or \ (Windows), use as-is
	if strings.ContainsAny(filename, "/\\") {
		return filename
	}

	// Look in ~/.config/mqtt-tunnel/
	home, err := os.UserHomeDir()
	if err == nil {
		return filepath.Join(home, ".config", "mqtt-tunnel", filename)
	}

	// Fallback to original filename if we can't get home dir
	return filename
}

// crlfWriter wraps an io.Writer and converts \n to \r\n for terminal raw mode
type crlfWriter struct {
	w io.Writer
}

func (cw *crlfWriter) Write(p []byte) (n int, err error) {
	modified := make([]byte, 0, len(p)+bytes.Count(p, []byte{'\n'}))
	for i, b := range p {
		if b == '\n' {
			if i == 0 || p[i-1] != '\r' {
				modified = append(modified, '\r')
			}
		}
		modified = append(modified, b)
	}
	return cw.w.Write(modified)
}

const defaultLogFileSize = 50000 // 50KB default

func setupLog(verbose bool, logFile string, logFileSize int, isLocal bool, printLines bool) io.Writer {
	flags := log.Ldate | log.Ltime
	if printLines {
		flags |= log.Lshortfile
	}
	prefix := ""

	var output io.Writer
	if logFile != "" {
		fi, err := os.Stat(logFile)
		isRegularFile := err == nil && fi.Mode().IsRegular()

		maxSize := logFileSize
		if maxSize == 0 {
			maxSize = defaultLogFileSize
		} else if !isRegularFile {
			fmt.Fprintf(os.Stderr, "[INFO] log-file '%s' is a special file, log-file-size setting ignored\n", logFile)
			maxSize = defaultLogFileSize
		}

		if maxSize <= 0 {
			output = io.Discard
		} else if isRegularFile && fi.Size() >= int64(2*maxSize) {
			content, err := os.ReadFile(logFile)
			if err == nil && len(content) > maxSize {
				start := len(content) - maxSize
				for start < len(content) && content[start] != '\n' {
					start++
				}
				if start < len(content) {
					os.WriteFile(logFile, content[start+1:], 0666)
				}
			}
			f, err := os.OpenFile(logFile, os.O_WRONLY|os.O_APPEND, 0666)
			if err != nil {
				log.Fatalf("Failed to open log file: %v", err)
			}
			output = f
		} else {
			f, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
			if err != nil {
				log.Fatalf("Failed to open log file: %v", err)
			}
			output = f
		}
	} else {
		output = os.Stderr
	}

	if isLocal {
		output = &crlfWriter{w: output}
	}

	log.SetOutput(output)
	log.SetFlags(flags)
	log.SetPrefix(prefix)
	tunnel.SetVerboseLogging(verbose)

	return output
}

// expandServerAddr expands a server address shorthand.
// If the address starts with ":", "127.0.0.1" is prepended.
func expandServerAddr(addr string) string {
	if len(addr) > 0 && addr[0] == ':' {
		return "127.0.0.1" + addr
	}
	return addr
}

func printUsage() {
	fmt.Fprintf(os.Stderr, "Usage: %s -c <config.json> [options]\n\n", os.Args[0])
	fmt.Fprintf(os.Stderr, "SSH proxy via MQTT\n")
	fmt.Fprintf(os.Stderr, "Version: %s", Version)
	if gitHash != "" {
		fmt.Fprintf(os.Stderr, " (git: %s)", gitHash)
	}
	fmt.Fprintln(os.Stderr)
	if info := getPlatformInfo(); info != "" {
		fmt.Fprintf(os.Stderr, "%s\n", info)
	}
	fmt.Fprintln(os.Stderr)
	fmt.Fprintf(os.Stderr, "Options:\n")
	flag.PrintDefaults()
	fmt.Fprintf(os.Stderr, "\nExamples:\n")
	fmt.Fprintf(os.Stderr, "  %s -c server.json             (run in server mode)\n", os.Args[0])
	fmt.Fprintf(os.Stderr, "  %s -c client-to-home.json     (run in client mode)\n", os.Args[0])
	fmt.Fprintf(os.Stderr, "  %s -c ~/configs/office.json   (config with full path)\n", os.Args[0])
	fmt.Fprintf(os.Stderr, "  %s -generate                  (generate a secure random topic)\n", os.Args[0])
	fmt.Fprintf(os.Stderr, "  %s -c help                    (print sample config)\n", os.Args[0])
}

func printSampleConfig() {
	fmt.Println(`{
    "broker": "mqtt://broker.hivemq.com:1883",
    "topic": "gFAftaCL",
    "username": "",
    "password": "",
    "ca-cert": "",
    "client-cert": "",
    "private-key": "",
    "server": ":22",
    "log-file": "",
    "log-file-size": 50000,
    "debug": false,
    "print-lines": false,
    "connection-timeout": 15,
    "mqtt-keepalive": 60,
    "manual-keepalive": 0
}

Required fields:
  - broker: MQTT broker URL (e.g., mqtt://host:1883, mqtts://host:8883)
  - topic:  Control topic (generate with: mqtt-tunnel -generate)

Mode selection:
  - server: Target address (e.g., "127.0.0.1:22" or ":22"). 
            If present, runs in server mode. If absent, runs in client mode.

Keepalive options:
  - mqtt-keepalive:    MQTT ping interval in seconds (default: 60 for server, disabled for client)
                       Set to 0 or >=3600 to disable.
  - manual-keepalive:  Manual ping interval in seconds (0=disabled, default: 60 on Termux server)
                       Each instance uses a unique subtopic to avoid cross-traffic.

Path expansion:
  Path fields (ca-cert, client-cert, private-key, log-file) support ~ and $HOME expansion.`)
}

func main() {
	initPlatform()

	var (
		configFile     = flag.String("c", "", "config file path (required, use 'help' for sample)")
		configFileFull = flag.String("config", "", "config file path (alias for -c)")
		generate       = flag.Bool("generate", false, "generate a secure random topic and exit")
	)

	flag.Usage = printUsage
	flag.Parse()

	// Handle -generate
	if *generate {
		fmt.Println(tunnel.GenerateRandomID(10))
		os.Exit(0)
	}

	// Determine effective config file path
	effectiveConfigFile := *configFileFull
	if effectiveConfigFile == "" {
		effectiveConfigFile = *configFile
	}

	// Handle -config help (special case)
	if effectiveConfigFile == "help" {
		printSampleConfig()
		os.Exit(0)
	}

	// Require -c/-config flag
	if effectiveConfigFile == "" {
		printUsage()
		fmt.Fprintf(os.Stderr, "\nError: -c flag is required (config file path)\n")
		fmt.Fprintf(os.Stderr, "\nExamples:\n")
		fmt.Fprintf(os.Stderr, "  %s -c server.json\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "  %s -c client-to-home.json\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "  %s -c ~/.config/mqtt-tunnel/office.json\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "\nUse '%s -config help' to print a sample config.\n", os.Args[0])
		os.Exit(1)
	}

	// Resolve config file path
	effectiveConfigFile = resolveConfigPath(effectiveConfigFile)

	// Read config file
	conf, err := tunnel.ReadConfig(effectiveConfigFile)
	if err != nil {
		// Check if this is a bare filename (no path separators)
		isBareFilename := !strings.ContainsAny(*configFile, "/\\") && *configFile != ""
		isBareFilenameFull := !strings.ContainsAny(*configFileFull, "/\\") && *configFileFull != ""
		
		if isBareFilename || isBareFilenameFull {
			// User provided a bare filename
			bareName := *configFile
			if bareName == "" {
				bareName = *configFileFull
			}
			home, _ := os.UserHomeDir()
			lookedIn := filepath.Join(home, ".config", "mqtt-tunnel", bareName)
			
			fmt.Fprintf(os.Stderr, "Error: config file not found: %s\n", bareName)
			fmt.Fprintf(os.Stderr, "Looked in: %s\n", lookedIn)
			
			// Check if file exists in current directory
			if fileExists(bareName) {
				fmt.Fprintf(os.Stderr, "\nFound ./%s in current directory.\n", bareName)
				fmt.Fprintf(os.Stderr, "Use -c ./%s to use the file in current directory.\n", bareName)
			} else {
				fmt.Fprintf(os.Stderr, "\nUse -c ./%s to use a file in the current directory.\n", bareName)
			}
		} else {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		}
		os.Exit(1)
	}

	// Server mode: acquire exclusive lock on config file to prevent multiple instances
	if conf.ServerAddr != "" {
		_, err := acquireServerLock(effectiveConfigFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		// Lock is held until process exits (including crash/SIGKILL - kernel cleans up on Unix)
	}

	// Apply defaults for missing config values
	if conf.ConnectionTimeout <= 0 {
		conf.ConnectionTimeout = 15 // Default 15 seconds
	}

	// Validate broker URL
	_, err = tunnel.ParseBrokerURL(conf.BrokerURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: invalid broker URL: %v\n", err)
		os.Exit(1)
	}
	if conf.BrokerURL == "" {
		fmt.Fprintf(os.Stderr, "Error: MQTT broker URL is required in config file\n")
		os.Exit(1)
	}

	// Validate topic
	if conf.Topic == "" {
		fmt.Fprintf(os.Stderr, "Error: control topic is required in config file\n")
		os.Exit(1)
	}
	if err := tunnel.ValidateTopic(conf.Topic); err != nil {
		fmt.Fprintf(os.Stderr, "Error: invalid topic: %v\n", err)
		os.Exit(1)
	}

	// Check for blacklisted example topics
	if conf.Topic == "gFAftaCLyD" || conf.Topic == "gFAftaCL" {
		fmt.Fprintf(os.Stderr, "Error: topic '%s' is an example from documentation and may be used by other users\n", conf.Topic)
		fmt.Fprintf(os.Stderr, "Generate your own topic with: mqtt-tunnel -generate\n")
		os.Exit(1)
	}

	// Expand server address if present
	if conf.ServerAddr != "" {
		conf.ServerAddr = expandServerAddr(conf.ServerAddr)
	}

	// Determine mode
	isServerMode := conf.ServerAddr != ""

	// Apply keepalive defaults
	if isServerMode {
		// Server mode defaults
		if conf.ManualKeepalive > 0 {
			// Manual keepalive enabled - disable Paho
			if conf.MqttKeepalive > 0 && conf.MqttKeepalive < 3600 {
				log.Printf("[WARN] both manual-keepalive and mqtt-keepalive are set, mqtt-keepalive ignored")
			}
			conf.MqttKeepalive = 3600
		} else if conf.MqttKeepalive > 0 && conf.MqttKeepalive < 3600 {
			// Paho keepalive enabled
			conf.ManualKeepalive = 0
		} else {
			// Neither set - apply platform defaults
			conf.ManualKeepalive = getDefaultManualKeepalive()
			if conf.ManualKeepalive > 0 {
				conf.MqttKeepalive = 3600
			} else {
				conf.MqttKeepalive = 60
			}
		}
	} else {
		// Client mode - default to disabled
		if conf.MqttKeepalive == 0 || conf.MqttKeepalive >= 3600 {
			conf.MqttKeepalive = 3600 // Disabled
		}
		if conf.ManualKeepalive < 0 {
			conf.ManualKeepalive = 0
		}
	}

	// Setup logging
	logOutput := setupLog(conf.Debug, conf.LogFile, conf.LogFileSize, !isServerMode, conf.PrintLines)

	// Enable Paho debug if requested
	if conf.Debug {
		tunnel.SetDebugPahoLogging(true)
	}

	// Log startup info
	if isServerMode {
		log.Printf("[INFO] Server mode")
		log.Printf("[INFO]   app-version=%s", Version)
		if gitHash != "" {
			log.Printf("[INFO]   git=%s", gitHash)
		}
		log.Printf("[INFO]   wire-protocol=%d", tunnel.ProtocolVersion)
		log.Printf("[INFO]   root-topic=%s", conf.Topic)
		log.Printf("[INFO]   addr=%s", conf.ServerAddr)
	} else {
		log.Printf("[INFO] Client mode")
		log.Printf("[INFO]   app-version=%s", Version)
		if gitHash != "" {
			log.Printf("[INFO]   git=%s", gitHash)
		}
		log.Printf("[INFO]   wire-protocol=%d", tunnel.ProtocolVersion)
		log.Printf("[INFO]   root-topic=%s", conf.Topic)
	}

	// Log keepalive settings
	if conf.MqttKeepalive > 0 && conf.MqttKeepalive < 3600 {
		log.Printf("[INFO] mqtt-keepalive enabled: %ds", conf.MqttKeepalive)
	} else {
		log.Printf("[INFO] mqtt-keepalive disabled")
	}
	if conf.ManualKeepalive > 0 {
		log.Printf("[INFO] manual-keepalive enabled: %ds", conf.ManualKeepalive)
	} else {
		log.Printf("[INFO] manual-keepalive disabled")
	}

	// Create tunnel
	mqt, err := tunnel.NewMQTunnel(conf, isServerMode, logOutput)
	if err != nil {
		log.Fatal(err)
	}

	ctx := context.Background()

	// Setup config file hot reload for server mode
	if isServerMode {
		reloader, err := tunnel.NewConfigReloader(
			effectiveConfigFile,
			10*time.Second,
			func() {
				newConf, err := tunnel.ReadConfig(effectiveConfigFile)
				if err != nil {
					log.Printf("[ERROR] Config reload failed: %v", err)
					log.Printf("[INFO] Continuing with current config")
					return
				}

				// Apply defaults for reloaded config
				if newConf.ServerAddr != "" {
					newConf.ServerAddr = expandServerAddr(newConf.ServerAddr)
				}
				if newConf.ConnectionTimeout <= 0 {
					newConf.ConnectionTimeout = 15
				}

				if newConf.ManualKeepalive > 0 {
					newConf.MqttKeepalive = 3600
				} else if newConf.MqttKeepalive <= 0 || newConf.MqttKeepalive >= 3600 {
					newConf.MqttKeepalive = 3600
					newConf.ManualKeepalive = getDefaultManualKeepalive()
					if newConf.ManualKeepalive == 0 {
						newConf.MqttKeepalive = 60
					}
				}

				if newConf.BrokerURL == "" {
					log.Printf("[ERROR] Config reload failed: MQTT broker URL is required")
					log.Printf("[INFO] Continuing with current config")
					return
				}
				if newConf.Topic == "" {
					log.Printf("[ERROR] Config reload failed: control topic is required")
					log.Printf("[INFO] Continuing with current config")
					return
				}
				if err := tunnel.ValidateTopic(newConf.Topic); err != nil {
					log.Printf("[ERROR] Config reload failed: invalid topic: %v", err)
					log.Printf("[INFO] Continuing with current config")
					return
				}

				mqt.ReloadConfig(newConf)
			},
		)
		if err != nil {
			log.Printf("[WARN] Failed to setup config file watcher: %v", err)
		} else {
			reloader.Start()
			log.Printf("[INFO] Config file hot reload enabled (debounce: 10s)")
		}
	}

	// Run
	if isServerMode {
		if err := mqt.StartRemote(ctx); err != nil {
			log.Fatal(err)
		}
	} else {
		if err := mqt.StartStdio(ctx, 0); err != nil {
			log.Fatal(err)
		}
	}
}
