package portforward

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/x0lie/pia-tun/internal/config"
	"github.com/x0lie/pia-tun/internal/log"
)

// Config holds port forwarding configuration.
type Config struct {
	Token                string
	PeerIP               string
	MetaCN               string
	PFGateway            string
	BindInterval         time.Duration
	SignatureRefreshDays int
	SignatureSafetyHours int
	PortFile             string
	DebugMode            bool
}

func loadConfig() (*Config, error) {
	token, err := os.ReadFile("/tmp/pia_login_token")
	if err != nil {
		return nil, fmt.Errorf("failed to read token: %w", err)
	}

	peerIP, err := os.ReadFile("/tmp/client_ip")
	if err != nil {
		return nil, fmt.Errorf("failed to read client IP: %w", err)
	}

	metaCN, err := os.ReadFile("/tmp/pia_cn")
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

	cfg := &Config{
		Token:                strings.TrimSpace(string(token)),
		PeerIP:               strings.TrimSpace(string(peerIP)),
		MetaCN:               strings.TrimSpace(string(metaCN)),
		PFGateway:            gateway,
		BindInterval:         time.Duration(config.GetEnvInt("PF_BIND_INTERVAL", 900)) * time.Second,
		SignatureRefreshDays: config.GetEnvInt("PF_SIGNATURE_REFRESH_DAYS", 31),
		SignatureSafetyHours: config.GetEnvInt("PF_SIGNATURE_SAFETY_HOURS", 24),
		PortFile:             config.GetEnvOrDefault("PORT_FILE", "/run/pia-tun/port"),
		DebugMode:            config.IsDebugMode(),
	}

	return cfg, nil
}

// Run starts the port forwarding service. This is the main entry point called by the dispatcher.
// onReconnect is an optional callback for orchestrated mode. When set, the keepalive manager
// calls it instead of os.Exit(1) when a reconnect is needed. Pass nil for legacy standalone mode.
func Run(ctx context.Context, onReconnect func(), ready chan<- struct{}) error {
	signalReady := func() {
		if ready != nil {
			close(ready)
		}
	}

	cfg, err := loadConfig()
	if err != nil {
		log.Error(fmt.Sprintf("Port forwarding failed: %v", err))

		logger := &log.Logger{Enabled: config.IsDebugMode()}
		logger.Debug("Failed to load config: %v", err)

		signalReady()
		<-ctx.Done()
		return nil
	}

	logger := &log.Logger{
		Enabled: cfg.DebugMode,
	}

	logger.Debug("Port forwarding configuration:")
	logger.Debug("  BIND_INTERVAL=%v (%dmin)", cfg.BindInterval, int(cfg.BindInterval.Minutes()))
	logger.Debug("  SIGNATURE_REFRESH_DAYS=%d", cfg.SignatureRefreshDays)
	logger.Debug("  SIGNATURE_SAFETY_HOURS=%d", cfg.SignatureSafetyHours)
	logger.Debug("  PF_GATEWAY=%s", cfg.PFGateway)
	logger.Debug("  TOKEN length: %d", len(cfg.Token))
	logger.Debug("  PEER_IP: %s", cfg.PeerIP)
	logger.Debug("  PIA_CN: %s", cfg.MetaCN)

	client := NewPIAClient(cfg, logger)
	manager := NewKeepaliveManager(cfg, client, logger, onReconnect)

	if err := manager.Run(ctx, signalReady); err != nil {
		return fmt.Errorf("port forwarding failed: %w", err)
	}

	return nil
}
