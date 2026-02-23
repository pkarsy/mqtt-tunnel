package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"

	"mqtt-tunnel/tunnel"
)

func setupLog(verbose bool, logFile string) {
	flags := log.Ldate | log.Ltime
	prefix := ""

	if logFile != "" {
		f, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
		if err != nil {
			log.Fatalf("Failed to open log file: %v", err)
		}
		log.SetOutput(f)
	} else {
		// All logs go to stderr to avoid interfering with tunnel data on stdout/stdin
		log.SetOutput(os.Stderr)
	}

	log.SetFlags(flags)
	log.SetPrefix(prefix)
	tunnel.SetVerboseLogging(verbose)
}



func printUsage() {
	fmt.Fprintf(os.Stderr, "Usage: %s [options]\n\n", os.Args[0])
	fmt.Fprintf(os.Stderr, "SSH proxy via MQTT broker\n\n")
	fmt.Fprintf(os.Stderr, "Options:\n")
	flag.PrintDefaults()
	fmt.Fprintf(os.Stderr, "\nExamples:\n")
	fmt.Fprintf(os.Stderr, "  %s -topic generate (generate a secure random topic)\n", os.Args[0])
	fmt.Fprintf(os.Stderr, "  %s -broker mqtt://localhost:1883 -topic device/1/control -local\n", os.Args[0])
	fmt.Fprintf(os.Stderr, "  %s -config config.json -remote 127.0.0.1:22\n", os.Args[0])
	fmt.Fprintf(os.Stderr, "  %s -config help (print sample config)\n", os.Args[0])
}

func printSampleConfig() {
	fmt.Println(`{
    "broker": "mqtt://broker.hivemq.com:1883",
    "topic": "gFAftaCLyD",
    "username": "",
    "password": "",
    "caCert": "",
    "clientCert": "",
    "privateKey": "",
    "remoteAddr": "127.0.0.1:22",
    "connectionTimeout": 15
}

Required fields:
  - broker: MQTT broker URL
  - topic:  Control topic (generate with: mqtt-tunnel -topic generate)`)
}

func main() {
	var (
		configFile        = flag.String("config", "", "config file path (use it to hide secrets from command line; use -config help to print a sample config)")
		verbose           = flag.Bool("verbose", false, "verbose logging")
		logFile           = flag.String("log-file", "", "log file path")
		broker            = flag.String("broker", "", "MQTT broker URL (e.g., mqtt://host:port, mqtts://host:port, ws://host:port, wss://host:port). Default ports: mqtt=1883, mqtts=8883, ws=80, wss=443)")
		username          = flag.String("username", "", "MQTT username value")
		password          = flag.String("password", "", "MQTT password value")
		caCert            = flag.String("ca-cert", "", "CA certificate path")
		clientCert        = flag.String("client-cert", "", "client certificate path")
		privateKey        = flag.String("private-key", "", "private key path")
		topic             = flag.String("topic", "", "control topic value (use 'generate' to create a secure random topic)")
		remote            = flag.String("remote", "", "Enables remote mode. The address (usually 127.0.0.1:22) is the address of the target server (most probably SSH) as viewed by the remote mqtt-tunnel process")
		local             = flag.Bool("local", false, "Enables local mode. Connects to remote instance and stdio is connected with the remote socket (use it with SSH ProxyCommand)")
		connectionTimeout = flag.Int("connection-timeout", 15, "connection timeout in seconds")
	)

	flag.Usage = printUsage
	flag.Parse()

	// Check if user wants to print sample config
	if *configFile == "help" {
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

	setupLog(*verbose, *logFile)

	// Try to read config file if specified
	var conf tunnel.Config
	var err error
	if *configFile != "" {
		conf, err = tunnel.ReadConfig(*configFile)
		if err != nil {
			log.Fatal(err)
		}
	}

	// Override config with command-line options if specified
	if *broker != "" {
		conf.BrokerURL = *broker
	}

	// Validate broker URL format
	_, err = tunnel.ParseBrokerURL(conf.BrokerURL)
	if err != nil {
		log.Fatalf("invalid broker URL: %v", err)
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
		log.Fatal("MQTT broker URL is required (specify with -broker or in config file)")
	}
	if conf.Topic == "" {
		log.Fatal("control topic is required (specify with -topic or in config file)")
	}

	// For remote mode, store the remote address in config (command line overrides config)
	if *remote != "" {
		conf.RemoteAddr = *remote
	}

	// Validate mode: exactly one of -local or remote address must be specified
	modeCount := 0
	if *local {
		modeCount++
	}
	if conf.RemoteAddr != "" {
		modeCount++
	}

	if modeCount != 1 {
		log.Fatal("must specify exactly one mode: -local (local stdio) or -remote/remoteAddr (remote mode)")
	}

	// Set connection timeout from command line
	conf.ConnectionTimeout = *connectionTimeout

	mqt, err := tunnel.NewMQTunnel(conf)
	if err != nil {
		log.Fatal(err)
	}

	ctx := context.Background()

	if *local {
		// Local mode using stdio (for SSH ProxyCommand)
		if err := mqt.StartStdio(ctx, 0); err != nil {
			log.Fatal(err)
		}
	} else {
		// Remote mode - start the MQTT broker and wait for connections
		if err := mqt.StartRemote(ctx); err != nil {
			log.Fatal(err)
		}
	}
}
