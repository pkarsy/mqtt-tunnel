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
	"runtime"
	"strings"
	"time"

	"mqtt-tunnel/tunnel"
)

// fileExists checks if a file exists
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// getDefaultConfigPath returns the default config file path.
// Looks for default.json first, then config.json for backward compatibility.
// Returns empty string if home directory cannot be determined or no config found.
func getDefaultConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}

	configDir := filepath.Join(home, ".config", "mqtt-tunnel")

	// Try default.json first
	defaultPath := filepath.Join(configDir, "default.json")
	if fileExists(defaultPath) {
		return defaultPath
	}

	// Fall back to config.json for backward compatibility
	configPath := filepath.Join(configDir, "config.json")
	if fileExists(configPath) {
		return configPath
	}

	return ""
}

// resolveConfigPath resolves a config file path.
// If the path contains no directory separator, it first checks in
// ~/.config/mqtt-tunnel/, then falls back to current directory.
// Returns the resolved path.
func resolveConfigPath(filename string) string {
	// If path contains / or \ (Windows), use as-is
	if strings.ContainsAny(filename, "/\\") {
		return filename
	}

	// Try ~/.config/mqtt-tunnel/ first
	home, err := os.UserHomeDir()
	if err == nil {
		configDirPath := filepath.Join(home, ".config", "mqtt-tunnel", filename)
		if fileExists(configDirPath) {
			return configDirPath
		}
	}

	// Fall back to current directory
	return filename
}

// crlfWriter wraps an io.Writer and converts \n to \r\n for terminal raw mode
type crlfWriter struct {
	w io.Writer
}

func (cw *crlfWriter) Write(p []byte) (n int, err error) {
	// Convert \n to \r\n, but avoid double \r\r\n
	// Simple approach: replace all \n with \r\n
	// This is not the most efficient but works for log output
	modified := make([]byte, 0, len(p)+bytes.Count(p, []byte{'\n'}))
	for i, b := range p {
		if b == '\n' {
			// Check if previous char was \r (to avoid \r\r\n)
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
		// Check if it's a regular file (not /dev/tty, /dev/null, etc.)
		fi, err := os.Stat(logFile)
		isRegularFile := err == nil && fi.Mode().IsRegular()

		// Determine max size
		maxSize := logFileSize
		if maxSize == 0 {
			maxSize = defaultLogFileSize
		} else if !isRegularFile {
			// log-file-size explicitly set but log-file is a special file
			fmt.Fprintf(os.Stderr, "[INFO] log-file '%s' is a special file, log-file-size setting ignored\n", logFile)
			maxSize = defaultLogFileSize
		}

		if maxSize <= 0 {
			// Discard mode - discard all log output
			output = io.Discard
		} else if isRegularFile && fi.Size() >= int64(2*maxSize) {
			// File too large, truncate to last maxSize bytes at line boundary
			content, err := os.ReadFile(logFile)
			if err == nil && len(content) > maxSize {
				// Find start of last maxSize bytes
				start := len(content) - maxSize
				// Find first newline after start (to avoid cutting a line)
				for start < len(content) && content[start] != '\n' {
					start++
				}
				if start < len(content) {
					// Write from start+1 (after newline) to end
					os.WriteFile(logFile, content[start+1:], 0666)
				}
			}
			// Open for append
			f, err := os.OpenFile(logFile, os.O_WRONLY|os.O_APPEND, 0666)
			if err != nil {
				log.Fatalf("Failed to open log file: %v", err)
			}
			output = f
		} else {
			// Normal append mode
			f, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
			if err != nil {
				log.Fatalf("Failed to open log file: %v", err)
			}

			// Check if file is a regular file with existing contents (follows symlinks)
			if realFi, err := os.Stat(logFile); err == nil && realFi.Mode().IsRegular() && realFi.Size() > 0 {
				f.WriteString("\n ######## log starting ############\n")
			}

			output = f
		}
	} else if isLocal && runtime.GOOS != "windows" {
		// Client mode on Unix: use /dev/tty for visibility
		// This ensures logs are visible even when stderr is redirected by SSH
		tty, err := os.OpenFile("/dev/tty", os.O_WRONLY, 0)
		if err == nil {
			output = tty
		} else {
			// Fallback to stderr if /dev/tty is not available
			output = os.Stderr
		}
	} else {
		// Default: stderr
		output = os.Stderr
	}

	// In local mode, wrap with CRLF converter for raw terminal mode
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
// Examples:
//
//	":22" -> "127.0.0.1:22"
//	":8022" -> "127.0.0.1:8022"
//	"localhost:22" -> "localhost:22"
//	"192.168.1.1:22" -> "192.168.1.1:22"
func expandServerAddr(addr string) string {
	if len(addr) > 0 && addr[0] == ':' {
		return "127.0.0.1" + addr
	}
	return addr
}

func printUsage() {
	fmt.Fprintf(os.Stderr, "Usage: %s [options]\n\n", os.Args[0])
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
	fmt.Fprintf(os.Stderr, "  %s -topic generate (generate a secure random topic)\n", os.Args[0])
	fmt.Fprintf(os.Stderr, "  %s -broker mqtt://localhost:1883 -topic device/1/control\n", os.Args[0])
	fmt.Fprintf(os.Stderr, "  %s -config config.json -server :22\n", os.Args[0])
	fmt.Fprintf(os.Stderr, "  %s -config help (print sample config)\n", os.Args[0])
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
    "server": ":22",  // absence of this line implies client mode
    "log-file": "",
    "log-file-size": 50000,  // Max log file size in bytes (default 50000). 0=default, negative=discard
    "debug": false,
    "print-lines": false,
    "connection-timeout": 15,
    "mqtt-keepalive": 60,
    "manual-keepalive": 0  // Server only: send manual ping after N seconds of inactivity (0=disabled)
}

Required fields:
  - broker: MQTT broker URL
  - topic:  Control topic (generate with: mqtt-tunnel -topic generate)

Defaults:
  - connection-timeout: 15 seconds (tunnel establishment timeout)
  - mqtt-keepalive: 60 seconds (MQTT ping interval, 0 or negative to disable)

Path expansion:
  Path fields (ca-cert, client-cert, private-key, log-file) support ~ and $HOME expansion.
  Example: "log-file": "~/.config/mqtt-tunnel/mqtt-tunnel.log"`)
}

func main() {
	// termux or pc
	initPlatform()
	//
	var (
		configFile         = flag.String("c", "", "alias for -config")
		configFileFull     = flag.String("config", "", "config file path (use it to hide secrets from command line; use -config help to print a sample config)")
		verbose            = flag.Bool("verbose", false, "")
		debug              = flag.Bool("debug", false, "enable debug logging")
		debugPaho          = flag.Bool("debug-paho", false, "enable Paho MQTT library debug logging (very noisy)")
		printLines         = flag.Bool("print-lines", false, "include file and line number in log messages")
		logFile            = flag.String("log-file", "", "log file path")
		broker             = flag.String("broker", "", "MQTT broker URL (e.g., mqtt://host:port, mqtts://host:port, ws://host:port, wss://host:port). Default ports: mqtt=1883, mqtts=8883, ws=80, wss=443)")
		username           = flag.String("username", "", "MQTT username value")
		password           = flag.String("password", "", "MQTT password value")
		caCert             = flag.String("ca-cert", "", "CA certificate path")
		clientCert         = flag.String("client-cert", "", "client certificate path")
		privateKey         = flag.String("private-key", "", "private key path")
		topic              = flag.String("topic", "", "control topic value (use 'generate' to create a secure random topic)")
		server             = flag.String("server", "", "Enables server mode. The address (usually 127.0.0.1:22) is the address of the target service (most probably SSH) as viewed by the server mqtt-tunnel process. Absence of this option implies client mode.")
		connectionTimeout  = flag.Int("connection-timeout", 15, "connection timeout in seconds")
		mqttKeepalive      = flag.Int("mqtt-keepalive", 0, "MQTT keepalive interval in seconds. 0 or 3600+ disables Paho keepalive. Default: 60s (PC server), 3600s (client/Termux)")
		manualKeepalive = flag.Int("manual-keepalive", 0, "Manual ping interval in seconds (server mode only, 0=disabled). Sends PING to baseTopic/ping and expects echo response within 5s.")
	)

	flag.Usage = printUsage
	flag.Parse()

	// Check if user wants to print sample config
	// Use -config or -c, with -config taking precedence
	effectiveConfigFile := *configFileFull
	if effectiveConfigFile == "" {
		effectiveConfigFile = *configFile
	}

	// If no config file specified, try the default config file
	if effectiveConfigFile == "" {
		effectiveConfigFile = getDefaultConfigPath()
	} else {
		// Resolve config file path (handles bare filenames)
		effectiveConfigFile = resolveConfigPath(effectiveConfigFile)
	}

	if effectiveConfigFile == "help" {
		printSampleConfig()
		os.Exit(0)
	}

	// Check if user wants to generate a topic
	if *topic == "generate" {
		// Generate 10-char topic (alphanumeric, higher entropy than hex)
		fmt.Println(tunnel.GenerateRandomID(10))
		os.Exit(0)
	}

	// Show help if no options are provided AND no default config exists
	if flag.NFlag() == 0 && effectiveConfigFile == "" {
		printUsage()
		os.Exit(0)
	}

	// Note: setupLog is called later after we determine the mode

	// Try to read config file if specified
	var conf tunnel.Config
	var err error
	if effectiveConfigFile != "" {
		conf, err = tunnel.ReadConfig(effectiveConfigFile)
		if err != nil {
			// Provide helpful error message for bare filenames
			if !strings.ContainsAny(effectiveConfigFile, "/\\") {
				home, _ := os.UserHomeDir()
				fmt.Fprintf(os.Stderr, "Error: config file not found: %s\n", effectiveConfigFile)
				fmt.Fprintf(os.Stderr, "Looked in:\n")
				fmt.Fprintf(os.Stderr, "  - %s\n", filepath.Join(home, ".config", "mqtt-tunnel", effectiveConfigFile))
				fmt.Fprintf(os.Stderr, "  - ./%s\n", effectiveConfigFile)
			} else {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			}
			os.Exit(1)
		}
	}

	// Override config with command-line options if specified
	if *broker != "" {
		conf.BrokerURL = *broker
	}

	// Validate broker URL format
	_, err = tunnel.ParseBrokerURL(conf.BrokerURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: invalid broker URL: %v\n", err)
		os.Exit(1)
	}
	if *username != "" {
		conf.Username = *username
	}
	if *password != "" {
		conf.Password = *password
	}
	if *caCert != "" {
		conf.CaCert = *caCert
	}
	if *clientCert != "" {
		conf.ClientCert = *clientCert
	}
	if *privateKey != "" {
		conf.PrivateKey = *privateKey
	}
	if *topic != "" {
		conf.Topic = *topic
	}

	// Validate required config
	if conf.BrokerURL == "" {
		fmt.Fprintf(os.Stderr, "Error: MQTT broker URL is required (specify with -broker or in config file)\n")
		os.Exit(1)
	}
	if conf.Topic == "" {
		fmt.Fprintf(os.Stderr, "Error: control topic is required (specify with -topic or in config file)\n")
		os.Exit(1)
	}

	// Validate topic format
	if err := tunnel.ValidateTopic(conf.Topic); err != nil {
		fmt.Fprintf(os.Stderr, "Error: invalid topic: %v\n", err)
		os.Exit(1)
	}

	// Check for blacklisted example topics from documentation
	if conf.Topic == "gFAftaCLyD" || conf.Topic == "gFAftaCL" {
		fmt.Fprintf(os.Stderr, "Error: topic '%s' is an example from the current or previous README and may be used by other users\n", conf.Topic)
		fmt.Fprintf(os.Stderr, "Please generate your own unique topic with: mqtt-tunnel -topic generate\n")
		os.Exit(1)
	}

	// For server mode, store the server address in config (command line overrides config)
	if *server != "" {
		conf.ServerAddr = expandServerAddr(*server)
	} else if conf.ServerAddr != "" {
		conf.ServerAddr = expandServerAddr(conf.ServerAddr)
	}

	// Set connection timeout from command line
	conf.ConnectionTimeout = *connectionTimeout

	// Track if user explicitly set keepalive values
	userSetMqttKeepalive := *mqttKeepalive > 0 || conf.MqttKeepalive > 0
	userSetManualKeepalive := *manualKeepalive > 0 || conf.ManualKeepalive > 0

	// Set MQTT keepalive from command line
	if *mqttKeepalive > 0 {
		conf.MqttKeepalive = *mqttKeepalive
	}

	// Set manual ping interval from command line
	if *manualKeepalive > 0 {
		conf.ManualKeepalive = *manualKeepalive
	}

	// Determine mode: server mode if ServerAddr is set, otherwise client mode
	isServerMode := conf.ServerAddr != ""

	// Apply keepalive defaults and logic
	if isServerMode {
		// Server mode
		if conf.ManualKeepalive > 0 {
			// Manual keepalive is set - disable Paho keepalive
			if userSetMqttKeepalive {
				log.Printf("[WARN] both manual-keepalive and mqtt-keepalive are set, mqtt-keepalive ignored")
			}
			conf.MqttKeepalive = 3600 // Effectively disable Paho keepalive
		} else if userSetMqttKeepalive {
			// Only mqtt-keepalive is set
			if conf.MqttKeepalive == 0 {
				conf.MqttKeepalive = 3600 // 0 means disabled
			}
			conf.ManualKeepalive = 0
		} else {
			// Nothing set - apply platform defaults
			conf.ManualKeepalive = getDefaultManualKeepalive()
			if conf.ManualKeepalive > 0 {
				// Termux: manual-keepalive default, disable Paho
				conf.MqttKeepalive = 3600
			} else {
				// Other platforms: Paho keepalive default
				conf.MqttKeepalive = 60
			}
		}
	} else {
		// Client mode - default to disabled unless explicitly set
		if !userSetMqttKeepalive && !userSetManualKeepalive {
			conf.MqttKeepalive = 3600 // Effectively disabled
			conf.ManualKeepalive = 0
		} else {
			// User set at least one - use their values
			if conf.MqttKeepalive == 0 && userSetMqttKeepalive {
				conf.MqttKeepalive = 3600 // 0 means disabled
			}
			if !userSetMqttKeepalive {
				conf.MqttKeepalive = 3600 // Default if not set
			}
		}
	}

	// Use log file from config if not provided on command line
	effectiveLogFile := *logFile
	if effectiveLogFile == "" {
		effectiveLogFile = conf.LogFile
	}

	// Use log file size from config
	effectiveLogFileSize := conf.LogFileSize

	// Use debug from config if not provided on command line
	effectiveVerbose := *verbose || *debug || conf.Debug

	// Use print-lines from config if not provided on command line
	effectivePrintLines := *printLines || conf.PrintLines

	// Setup logging (client mode needs CRLF conversion)
	logOutput := setupLog(effectiveVerbose, effectiveLogFile, effectiveLogFileSize, !isServerMode, effectivePrintLines)

	// Enable Paho debug logging if requested
	if *debugPaho {
		tunnel.SetDebugPahoLogging(true)
	}

	// Log the mode of operation
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

	mqt, err := tunnel.NewMQTunnel(conf, isServerMode, logOutput)
	if err != nil {
		log.Fatal(err)
	}

	ctx := context.Background()

	// Setup config file hot reload for server mode only
	if isServerMode && effectiveConfigFile != "" {
		reloader, err := tunnel.NewConfigReloader(
			effectiveConfigFile,
			10*time.Second, // 10 second debounce
			func() {
				// Read and validate new config
				newConf, err := tunnel.ReadConfig(effectiveConfigFile)
				if err != nil {
					log.Printf("[ERROR] Config reload failed: %v", err)
					log.Printf("[INFO] Continuing with current config")
					return
				}

				// Apply command-line overrides to new config
				if *broker != "" {
					newConf.BrokerURL = *broker
				}
				if *username != "" {
					newConf.Username = *username
				}
				if *password != "" {
					newConf.Password = *password
				}
				if *caCert != "" {
					newConf.CaCert = *caCert
				}
				if *clientCert != "" {
					newConf.ClientCert = *clientCert
				}
				if *privateKey != "" {
					newConf.PrivateKey = *privateKey
				}
				if *topic != "" {
					newConf.Topic = *topic
				}
				if *server != "" {
					newConf.ServerAddr = expandServerAddr(*server)
				} else if newConf.ServerAddr != "" {
					newConf.ServerAddr = expandServerAddr(newConf.ServerAddr)
				}
				newConf.ConnectionTimeout = *connectionTimeout
				if *mqttKeepalive > 0 {
					newConf.MqttKeepalive = *mqttKeepalive
				}
				if *manualKeepalive > 0 {
					newConf.ManualKeepalive = *manualKeepalive
				}

				// Apply platform-specific default for manual keepalive if not set
				// Note: reload only happens in server mode, so we always apply the default
				if newConf.ManualKeepalive == 0 {
					newConf.ManualKeepalive = getDefaultManualKeepalive()
				}

				// Apply debug settings (command-line overrides config file)
				// Note: newConf.Debug already includes newConf.Verbose (deprecated alias)
				newConf.Debug = *verbose || *debug || newConf.Debug

				// Validate required fields
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

				// Trigger reload
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

	if isServerMode {
		// Server mode - wait for connections and forward to target service
		if err := mqt.StartRemote(ctx); err != nil {
			log.Fatal(err)
		}
	} else {
		// Client mode using stdio (for SSH ProxyCommand)
		if err := mqt.StartStdio(ctx, 0); err != nil {
			log.Fatal(err)
		}
	}
}
