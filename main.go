package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"

	"mqtt-tunnel/tunnel"
)

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

func setupLog(verbose bool, logFile string, isLocal bool) io.Writer {
	flags := log.Ldate | log.Ltime
	prefix := ""

	var output io.Writer
	if logFile != "" {
		f, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
		if err != nil {
			log.Fatalf("Failed to open log file: %v", err)
		}

		// Check if file is a regular file with existing contents (follows symlinks)
		if realFi, err := os.Stat(logFile); err == nil && realFi.Mode().IsRegular() && realFi.Size() > 0 {
			f.WriteString("\n ######## log starting ############\n")
		}

		output = f
	} else {
		// All logs go to stderr to avoid interfering with tunnel data on stdout/stdin
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
    "topic": "gFAftaCLyD",
    "username": "",
    "password": "",
    "ca-cert": "",
    "client-cert": "",
    "private-key": "",
    "server": ":22",  // absence of this line implies client mode
    "log-file": "",
    "verbose": false,
    "connection-timeout": 15,
    "mqtt-keepalive": 60
}

Required fields:
  - broker: MQTT broker URL
  - topic:  Control topic (generate with: mqtt-tunnel -topic generate)

Defaults:
  - connection-timeout: 15 seconds (tunnel establishment timeout)
  - mqtt-keepalive: 60 seconds (MQTT ping interval)
  
Note: If SSH keepalive is configured shorter than mqtt-keepalive, SSH will detect disconnects first.`)
}

func main() {
	// termux or pc
	initPlatform()
	//
	var (
		configFile        = flag.String("c", "", "alias for -config")
		configFileFull    = flag.String("config", "", "config file path (use it to hide secrets from command line; use -config help to print a sample config)")
		verbose           = flag.Bool("verbose", false, "verbose logging")
		logFile           = flag.String("log-file", "", "log file path")
		broker            = flag.String("broker", "", "MQTT broker URL (e.g., mqtt://host:port, mqtts://host:port, ws://host:port, wss://host:port). Default ports: mqtt=1883, mqtts=8883, ws=80, wss=443)")
		username          = flag.String("username", "", "MQTT username value")
		password          = flag.String("password", "", "MQTT password value")
		caCert            = flag.String("ca-cert", "", "CA certificate path")
		clientCert        = flag.String("client-cert", "", "client certificate path")
		privateKey        = flag.String("private-key", "", "private key path")
		topic             = flag.String("topic", "", "control topic value (use 'generate' to create a secure random topic)")
		server            = flag.String("server", "", "Enables server mode. The address (usually 127.0.0.1:22) is the address of the target service (most probably SSH) as viewed by the server mqtt-tunnel process. Absence of this option implies client mode.")
		connectionTimeout = flag.Int("connection-timeout", 15, "connection timeout in seconds")
		mqttKeepalive     = flag.Int("mqtt-keepalive", 0, "MQTT keepalive interval in seconds (default: 60)")
	)

	flag.Usage = printUsage
	flag.Parse()

	// Check if user wants to print sample config
	// Use -config or -c, with -config taking precedence
	effectiveConfigFile := *configFileFull
	if effectiveConfigFile == "" {
		effectiveConfigFile = *configFile
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

	// Show help if no options are provided
	if flag.NFlag() == 0 {
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
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
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

	// For server mode, store the server address in config (command line overrides config)
	if *server != "" {
		conf.ServerAddr = expandServerAddr(*server)
	} else if conf.ServerAddr != "" {
		conf.ServerAddr = expandServerAddr(conf.ServerAddr)
	}

	// Set connection timeout from command line
	conf.ConnectionTimeout = *connectionTimeout

	// Set MQTT keepalive from command line (0 means use config file value or default)
	if *mqttKeepalive > 0 {
		conf.MqttKeepalive = *mqttKeepalive
	}

	// Determine mode: server mode if ServerAddr is set, otherwise client mode
	isServerMode := conf.ServerAddr != ""

	// Use log file from config if not provided on command line
	effectiveLogFile := *logFile
	if effectiveLogFile == "" {
		effectiveLogFile = conf.LogFile
	}

	// Use verbose from config if not provided on command line
	effectiveVerbose := *verbose || conf.Verbose

	// Setup logging (client mode needs CRLF conversion)
	logOutput := setupLog(effectiveVerbose, effectiveLogFile, !isServerMode)

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

	mqt, err := tunnel.NewMQTunnel(conf, isServerMode, logOutput)
	if err != nil {
		log.Fatal(err)
	}

	ctx := context.Background()

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
