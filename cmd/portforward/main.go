package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type Config struct {
	Token                  string
	PeerIP                 string
	MetaCN                 string
	PFGateway              string
	BindInterval           time.Duration
	SignatureRefreshDays   int
	SignatureSafetyHours   int
	PortFile               string
	WebhookURL             string
	DebugMode              bool
}

const (
	colorReset  = "\033[0m"
	colorRed    = "\033[0;31m"
	colorGreen  = "\033[0;32m"
	colorBlue   = "\033[0;34m"
	colorYellow = "\033[0;33m"
	colorBold   = "\033[1m"
)

func loadConfig() (*Config, error) {
	getEnvInt := func(key string, defaultVal int) int {
		if val := os.Getenv(key); val != "" {
			if i, err := strconv.Atoi(val); err == nil {
				return i
			}
		}
		return defaultVal
	}

	getEnvBool := func(key string) bool {
		val := os.Getenv(key)
		return val == "debug" || val == "2"
	}

	// Read required files
	token, err := os.ReadFile("/tmp/pia_token")
	if err != nil {
		return nil, fmt.Errorf("failed to read token: %w", err)
	}

	peerIP, err := os.ReadFile("/tmp/client_ip")
	if err != nil {
		return nil, fmt.Errorf("failed to read client IP: %w", err)
	}

	metaCN, err := os.ReadFile("/tmp/meta_cn")
	if err != nil {
		return nil, fmt.Errorf("failed to read meta CN: %w", err)
	}

	pfGateway, err := os.ReadFile("/tmp/pf_gateway")
	if err != nil {
		return nil, fmt.Errorf("failed to read PF gateway: %w", err)
	}

	gateway := strings.TrimSpace(string(pfGateway))
	if gateway == "" || gateway == "null" {
		return nil, fmt.Errorf("no PF gateway available")
	}

	config := &Config{
		Token:                  strings.TrimSpace(string(token)),
		PeerIP:                 strings.TrimSpace(string(peerIP)),
		MetaCN:                 strings.TrimSpace(string(metaCN)),
		PFGateway:              gateway,
		BindInterval:           time.Duration(getEnvInt("PF_BIND_INTERVAL", 600)) * time.Second,
		SignatureRefreshDays:   getEnvInt("PF_SIGNATURE_REFRESH_DAYS", 30),
		SignatureSafetyHours:   getEnvInt("PF_SIGNATURE_SAFETY_HOURS", 24),
		PortFile:               getEnvOrDefault("PORT_FILE", "/etc/wireguard/port"),
		WebhookURL:             os.Getenv("WEBHOOK_URL"),
		DebugMode:              getEnvBool("_LOG_LEVEL"),
	}

	return config, nil
}

func getEnvOrDefault(key, defaultVal string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return defaultVal
}

func debugLog(config *Config, format string, args ...interface{}) {
	if config.DebugMode {
		timestamp := time.Now().Format("15:04:05")
		msg := fmt.Sprintf(format, args...)
		fmt.Fprintf(os.Stderr, "    %s[DEBUG]%s %s - %s\n", colorBlue, colorReset, timestamp, msg)
	}
}

func showInfo() {
	fmt.Println()
}

func showStep(msg string) {
	fmt.Printf("%s▶%s %s\n", colorBlue, colorReset, msg)
}

func showSuccess(msg string) {
	fmt.Printf("  %s✓%s %s\n", colorGreen, colorReset, msg)
}

func showError(msg string) {
	fmt.Printf("  %s✗%s %s\n", colorRed, colorReset, msg)
}

func showWarning(msg string) {
	fmt.Printf("  %s⚠%s %s\n", colorYellow, colorReset, msg)
}

func showVPNConnected() {
	grn := colorGreen
	bold := colorBold
	nc := colorReset
    fmt.Printf("\n%s╔════════════════════════════════════════════════╗%s\n", grn, nc)
    fmt.Printf("%s║%s                %s✓%s %sVPN Connected%s                 %s║%s\n", grn, nc, grn, nc, bold, nc, grn, nc)
    fmt.Printf("%s╚════════════════════════════════════════════════╝%s\n\n", grn, nc)
}

func showVPNConnectedWarning() {
	ylw := colorYellow
	bold := colorBold
	nc := colorReset
    fmt.Printf("\n%s╔════════════════════════════════════════════════╗%s\n", ylw, nc)
    fmt.Printf("%s║%s                %s⚠%s %sVPN Connected%s                 %s║%s\n", ylw, nc, ylw, nc, bold, nc, ylw, nc)
    fmt.Printf("%s╚════════════════════════════════════════════════╝%s\n\n", ylw, nc)
}

func main() {
	// Load configuration
	config, err := loadConfig()
	if err != nil {
		showError(fmt.Sprintf("Port forwarding failed: %v", err))
		debugLog(&Config{DebugMode: os.Getenv("_LOG_LEVEL") == "2"}, "Failed to load config: %v", err)
		showVPNConnectedWarning()

		// Create completion flag and block forever
		os.WriteFile("/tmp/port_forwarding_complete", []byte(""), 0644)
		select {}
	}

	debugLog(config, "Port forwarding configuration:")
	debugLog(config, "  BIND_INTERVAL=%v (%dmin)", config.BindInterval, int(config.BindInterval.Minutes()))
	debugLog(config, "  SIGNATURE_REFRESH_DAYS=%d", config.SignatureRefreshDays)
	debugLog(config, "  SIGNATURE_SAFETY_HOURS=%d", config.SignatureSafetyHours)
	debugLog(config, "  PF_GATEWAY=%s", config.PFGateway)
	debugLog(config, "  TOKEN length: %d", len(config.Token))
	debugLog(config, "  PEER_IP: %s", config.PeerIP)
	debugLog(config, "  META_CN: %s", config.MetaCN)

	// Create PIA client
	client := NewPIAClient(config)

	// Create keepalive manager
	manager := NewKeepaliveManager(config, client)

	// Setup signal handling
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGTERM, syscall.SIGINT, syscall.SIGQUIT)

	go func() {
		sig := <-sigChan
		debugLog(config, "Received signal: %v", sig)
		cancel()
	}()

	// Run the port forwarding service
	if err := manager.Run(ctx); err != nil {
		log.Fatalf("Port forwarding failed: %v", err)
	}
}
