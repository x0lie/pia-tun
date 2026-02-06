package portforward

import (
	"context"
	"fmt"
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

// ConnectionConfig holds the VPN connection details needed for port forwarding.
type ConnectionConfig struct {
	Token     string // PIA authentication token
	ClientIP  string // Client's tunnel IP
	ServerCN  string // Server certificate name
	PFGateway string // Port forwarding gateway IP
}

// Run starts the port forwarding service. This is the main entry point called by the orchestrator.
// connCfg provides the VPN connection details needed for port forwarding.
// onReconnect is called when a reconnect is needed (e.g., port change or API failure).
func Run(ctx context.Context, connCfg ConnectionConfig, onReconnect func(), ready chan<- struct{}) error {
	signalReady := func() {
		if ready != nil {
			close(ready)
		}
	}

	if connCfg.PFGateway == "" {
		log.Error("Port forwarding unavailable: no PF gateway")
		signalReady()
		<-ctx.Done()
		return nil
	}

	cfg := &Config{
		Token:                connCfg.Token,
		PeerIP:               connCfg.ClientIP,
		MetaCN:               connCfg.ServerCN,
		PFGateway:            connCfg.PFGateway,
		BindInterval:         time.Duration(config.GetEnvInt("PF_BIND_INTERVAL", 900)) * time.Second,
		SignatureRefreshDays: config.GetEnvInt("PF_SIGNATURE_REFRESH_DAYS", 31),
		SignatureSafetyHours: config.GetEnvInt("PF_SIGNATURE_SAFETY_HOURS", 24),
		PortFile:             config.GetEnvOrDefault("PORT_FILE", "/run/pia-tun/port"),
		DebugMode:            config.IsDebugMode(),
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
